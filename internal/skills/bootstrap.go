package skills

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// bootstrapFS holds the bootstrap skills, one folder per skill.
//
//go:embed bootstrap
var bootstrapFS embed.FS

// bootstrapRoot is the embed folder that holds the skill folders.
const bootstrapRoot = "bootstrap"

// BootstrapNames returns the names of the embedded bootstrap skills, sorted.
func BootstrapNames() []string {
	entries, err := bootstrapFS.ReadDir(bootstrapRoot)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// CopyMissing copies each bootstrap skill into dir when its SKILL.md is not
// there. It never overwrites a file that exists, so it is safe to call at
// each start. It returns the names of the skills that it copied.
func CopyMissing(dir string) (copied []string, err error) {
	for _, name := range BootstrapNames() {
		_, statErr := os.Stat(filepath.Join(dir, name, skillFile))
		if statErr == nil {
			continue
		}
		if !errors.Is(statErr, fs.ErrNotExist) {
			return copied, fmt.Errorf("check bootstrap skill %s: %w", name, statErr)
		}
		if err := writeBootstrap(dir, name, false); err != nil {
			return copied, err
		}
		copied = append(copied, name)
	}
	return copied, nil
}

// Reset writes the embedded files of the bootstrap skill name into dir. It
// overwrites the files that exist and keeps other files in the skill folder.
// An unknown name is an error.
func Reset(dir, name string) error {
	if !isBootstrapName(name) {
		return fmt.Errorf("%q is not a bootstrap skill; the bootstrap skills are %s",
			name, strings.Join(BootstrapNames(), ", "))
	}
	return writeBootstrap(dir, name, true)
}

// isBootstrapName reports whether name is one of the embedded bootstrap skills.
func isBootstrapName(name string) bool {
	for _, n := range BootstrapNames() {
		if n == name {
			return true
		}
	}
	return false
}

// writeBootstrap copies the embedded files of one skill into dir/name. When
// overwrite is false, it keeps each file that exists.
func writeBootstrap(dir, name string, overwrite bool) error {
	src := path.Join(bootstrapRoot, name)
	return fs.WalkDir(bootstrapFS, src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, src), "/")
		dst := filepath.Join(dir, name, filepath.FromSlash(rel))
		if d.IsDir() {
			if err := os.MkdirAll(dst, 0o700); err != nil {
				return fmt.Errorf("create bootstrap skill folder %s: %w", dst, err)
			}
			return nil
		}
		if !overwrite {
			if _, err := os.Stat(dst); err == nil {
				return nil
			}
		}
		data, err := bootstrapFS.ReadFile(p)
		if err != nil {
			return err
		}
		if err := writeFileAtomic(dst, data); err != nil {
			return fmt.Errorf("write bootstrap skill file %s: %w", dst, err)
		}
		return nil
	})
}

// writeFileAtomic writes data to a temp file in the same folder and renames
// it, so a reader never sees a half-written skill.
func writeFileAtomic(dst string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-"+filepath.Base(dst)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, dst); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
