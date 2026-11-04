package backup

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/dnwandana/tether-agent/internal/memory"
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
// skills/ of dataDir. Create adds state.db and config.yaml. The memory/ items
// come first, in this order: the folder, the tier-1 files, memory/notes, and
// the notes in name order. The items of scripts/ and skills/ are in lexical
// order, and each folder comes before its contents. skipped holds the Rel of
// each symlink, each special file, and each entry of memory/notes/ that is
// not a note. collectItems does not follow links.
func collectItems(dataDir string) (items []item, skipped []string, err error) {
	items, skipped, err = collectMemory(dataDir)
	if err != nil {
		return nil, nil, err
	}
	got, skip, err := collectFolders(dataDir)
	if err != nil {
		return nil, nil, err
	}
	return append(items, got...), append(skipped, skip...), nil
}

// collectFolders is the part of collectItems for walkedFolders.
func collectFolders(dataDir string) (items []item, skipped []string, err error) {
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

// collectMemory returns the memory/ folder, the tier-1 files, and
// memory/notes with its note files. The folder item is present only when a
// tier-1 file or a note exists.
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
	notes, skip, err := collectNotes(dataDir)
	if err != nil {
		return nil, nil, err
	}
	skipped = append(skipped, skip...)
	files := len(items)
	for _, it := range notes {
		if !it.Dir {
			files++
		}
	}
	if files == 0 {
		return nil, skipped, nil
	}
	items = append([]item{{Rel: "memory", Src: dir, Dir: true, Mode: fi.Mode()}}, items...)
	return append(items, notes...), skipped, nil
}

// collectNotes returns the memory/notes folder and each regular file in it
// with a note name, in name order. A missing folder gives no items. It does
// not go into a subfolder.
func collectNotes(dataDir string) (items []item, skipped []string, err error) {
	dir := filepath.Join(dataDir, "memory", "notes")
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil, nil
	case err != nil:
		return nil, nil, err
	case !fi.IsDir():
		// A symlink or a file at memory/notes: the store refuses it too.
		return nil, []string{"memory/notes"}, nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	items = []item{{Rel: "memory/notes", Src: dir, Dir: true, Mode: fi.Mode()}}
	for _, e := range ents {
		rel := "memory/notes/" + e.Name()
		if !e.Type().IsRegular() || !memory.IsNoteName(e.Name()) {
			skipped = append(skipped, rel)
			continue
		}
		info, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			// A store write removed the note after ReadDir.
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		items = append(items, item{Rel: rel, Src: filepath.Join(dir, e.Name()), Mode: info.Mode()})
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
// is copied with no change and with no full read into memory. changed reports
// whether the mask changed the text.
func maskTextFile(src, dst string, r *security.Redactor) (changed bool, err error) {
	sf, err := openRegular(src)
	if err != nil {
		return false, err
	}
	defer sf.Close()
	head := make([]byte, binarySniffLen)
	n, err := io.ReadFull(sf, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, fmt.Errorf("read %s: %w", src, err)
	}
	head = head[:n]
	if bytes.IndexByte(head, 0) >= 0 {
		return false, writeCopy(dst, io.MultiReader(bytes.NewReader(head), sf), src)
	}
	// The mask needs the full text. A note has no size limit, but the memory store also
	// reads and writes each note in full, so one full read is safe.
	rest, err := io.ReadAll(sf)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", src, err)
	}
	in := string(head) + string(rest)
	out := r.RedactExact(in)
	changed = out != in
	return changed, writeCopy(dst, strings.NewReader(out), src)
}

// writeCopy writes the bytes of in to dst with mode 0600. src is the name of the
// source file for a read error.
func writeCopy(dst string, in io.Reader, src string) error {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	// Chmod also fixes the mode of a dst that existed before.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := io.Copy(f, in); err != nil {
		f.Close()
		return fmt.Errorf("copy %s: %w", src, err)
	}
	return f.Close()
}

// openRegular opens path for a read and refuses a file that is not a
// regular file. The collect checks the type before the read, but a file can
// change after that check. O_NOFOLLOW refuses a symlink, so the backup reads
// no file outside the data dir. O_NONBLOCK stops the open of a FIFO from a
// wait, and has no effect on a regular file.
func openRegular(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, fmt.Errorf("%s is a symlink, and the backup does not follow links", path)
	}
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return f, nil
}
