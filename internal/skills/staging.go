package skills

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// staleStaging is the age after which a staging folder is left over from a
// crash and the next run removes it.
const staleStaging = time.Hour

// Staging is one install or update run's private folder under
// skills/.hub/staging. It is on the same disk as the skills, so the final
// move is an atomic rename.
type Staging struct {
	Root string
}

// Repo returns the folder for the clone or the copy of the local folder.
func (s *Staging) Repo() string { return filepath.Join(s.Root, "repo") }

// Skill returns the folder for the selected skill, which the guard scans.
func (s *Staging) Skill() string { return filepath.Join(s.Root, "skill") }

// Old returns the folder that holds the old skill during an update swap.
func (s *Staging) Old() string { return filepath.Join(s.Root, "old") }

// Remove deletes the staging folder. A nil receiver does nothing, so callers
// can defer it before they know that the folder exists.
func (s *Staging) Remove() error {
	if s == nil || s.Root == "" {
		return nil
	}
	return os.RemoveAll(s.Root)
}

// stagingDir returns skills/.hub/staging.
func stagingDir(skillsDir string) string {
	return filepath.Join(skillsDir, ".hub", "staging")
}

// NewStaging removes stale staging folders, then makes a new folder with a
// random name, so two runs never collide.
func NewStaging(skillsDir string, now time.Time) (*Staging, error) {
	if err := CleanStale(skillsDir, now, staleStaging); err != nil {
		return nil, err
	}
	hub := filepath.Join(skillsDir, ".hub")
	parent := stagingDir(skillsDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, fmt.Errorf("make staging folder: %w", err)
	}
	// MkdirAll does not change a folder that exists, and the umask can
	// remove bits, so set the private mode on each .hub folder.
	for _, dir := range []string{hub, parent} {
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, fmt.Errorf("set mode of %s: %w", dir, err)
		}
	}
	root, err := os.MkdirTemp(parent, "")
	if err != nil {
		return nil, fmt.Errorf("make staging folder: %w", err)
	}
	return &Staging{Root: root}, nil
}

// CleanStale removes each folder directly in skills/.hub/staging whose mod
// time is older than now-maxAge. A missing staging folder is not an error.
func CleanStale(skillsDir string, now time.Time, maxAge time.Duration) error {
	dir := stagingDir(skillsDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read staging folder: %w", err)
	}
	cutoff := now.Add(-maxAge)
	var errs []error
	for _, e := range entries {
		// ReadDir does not follow symlinks, so a symlink is never a folder here.
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			errs = append(errs, fmt.Errorf("remove stale staging folder: %w", err))
		}
	}
	return errors.Join(errs...)
}

// CopyTree copies the folder src to dst, which must not exist. It skips each
// folder named .git. It copies a symlink as a symlink and never reads through
// it, so the symlink check sees each link. It keeps the permission bits,
// because the guard checks executable bits.
func CopyTree(src, dst string) error {
	rootInfo, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("copy %s: %w", src, err)
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("copy %s: not a folder", src)
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("copy to %s: the folder already exists", dst)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("copy to %s: %w", dst, err)
	}

	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		mode := d.Type()
		switch {
		case mode&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.IsDir():
			if rel != "." && d.Name() == ".git" {
				return filepath.SkipDir
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			return copyDir(target, info.Mode().Perm())
		case mode.IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			return copyFile(path, target, info.Mode().Perm())
		default:
			return fmt.Errorf("copy %s: unsupported file type %s", path, mode)
		}
	})
}

// copyDir makes one folder. The owner bits stay open during the copy so the
// copy can write the folder's children.
func copyDir(path string, perm fs.FileMode) error {
	if err := os.Mkdir(path, perm|0o700); err != nil {
		return err
	}
	return os.Chmod(path, perm|0o700)
}

// copyFile copies one regular file and sets perm after the write, because
// the umask can remove bits at create time.
func copyFile(src, dst string, perm fs.FileMode) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Chmod(perm)
}
