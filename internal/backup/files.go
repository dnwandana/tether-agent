package backup

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/dnwandana/tether-agent/internal/security"
)

// binarySniffLen is the number of leading bytes that maskTextFile checks for
// a NUL byte.
const binarySniffLen = 8 << 10

// item is one folder or one regular file that goes into the archive.
type item struct {
	// Rel is the slash path in the archive, relative to the data dir.
	Rel string
	// Src is the file to read: the file on the host or a prepared copy.
	Src  string
	Dir  bool
	Mode fs.FileMode
}

// walkedFolders are the allow-list folders that the backup copies with all
// their contents.
var walkedFolders = []string{"scripts", "skills"}

// collectItems lists the allow-list items under memory/, scripts/, and
// skills/ of dataDir. Create adds state.db and config.yaml. The order is
// lexical, and each folder comes before its contents. skipped holds the Rel
// of each symlink and each special file. collectItems does not follow links.
func collectItems(dataDir string) (items []item, skipped []string, err error) {
	items, skipped, err = collectMemory(dataDir)
	if err != nil {
		return nil, nil, err
	}
	for _, top := range walkedFolders {
		got, skip, err := collectFolder(dataDir, top)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, got...)
		skipped = append(skipped, skip...)
	}
	return items, skipped, nil
}

// collectMemory returns the memory/ folder and the allow-list files in it.
// The folder item is present only when one of those files exists.
func collectMemory(dataDir string) (items []item, skipped []string, err error) {
	dir := filepath.Join(dataDir, "memory")
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil, nil
	case err != nil:
		return nil, nil, err
	case !fi.IsDir():
		return nil, []string{"memory"}, nil
	}
	for _, rel := range memoryFiles {
		src := filepath.Join(dataDir, filepath.FromSlash(rel))
		ffi, err := os.Lstat(src)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return nil, nil, err
		case !ffi.Mode().IsRegular():
			skipped = append(skipped, rel)
			continue
		}
		items = append(items, item{Rel: rel, Src: src, Mode: ffi.Mode()})
	}
	if len(items) > 0 {
		items = append([]item{{Rel: "memory", Src: dir, Dir: true, Mode: fi.Mode()}}, items...)
	}
	return items, skipped, nil
}

// collectFolder walks the top-level folder top of dataDir. A missing folder
// gives no items.
func collectFolder(dataDir, top string) (items []item, skipped []string, err error) {
	root := filepath.Join(dataDir, top)
	if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	// WalkDir does not follow symlinks: it calls Lstat on the root, and a
	// symlink in the tree has the ModeSymlink type, so the walk does not go
	// into it.
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relOS, err := filepath.Rel(dataDir, p)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(relOS)
		if !allowed(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			skipped = append(skipped, rel)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		items = append(items, item{Rel: rel, Src: p, Dir: d.IsDir(), Mode: info.Mode()})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return items, skipped, nil
}

// maskTextFile copies src to dst with the known secret values masked. dst
// gets mode 0600. A file with a NUL byte in its first 8 KB is binary, so it
// is copied with no change. changed reports whether the mask changed the text.
func maskTextFile(src, dst string, r *security.Redactor) (changed bool, err error) {
	// The memory, skill, and script files are small, so one full read is safe.
	in, err := os.ReadFile(src)
	if err != nil {
		return false, err
	}
	out := in
	if bytes.IndexByte(in[:min(len(in), binarySniffLen)], 0) < 0 {
		masked := r.RedactExact(string(in))
		if masked != string(in) {
			out, changed = []byte(masked), true
		}
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return false, err
	}
	// Chmod also fixes the mode of a dst that existed before.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return false, err
	}
	if _, err := f.Write(out); err != nil {
		f.Close()
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	return changed, nil
}
