package skills

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// defaultHistoryKeep is the number of copies that stay for each skill when
// Manager.HistoryKeep is 0 or less.
const defaultHistoryKeep = 5

// historyDir is the folder in the skills folder that holds the copies.
const historyDir = ".history"

// Origin tells where a skill change comes from. Source is chat, review, cron,
// child, or owner. SessionID is the session of the change, or "".
type Origin struct {
	Source, SessionID string
}

// Copy is the record of one skill change and of the copy of the skill folder
// before the change.
type Copy struct {
	ID        int64
	Stamp     string
	Name      string
	RelDir    string
	At        time.Time
	Action    string
	Source    string
	SessionID string
	// Path is the copy folder under the skills folder, or "" for a create.
	Path      string
	HashAfter string
	UndoneBy  int64
}

// CopyRecorder stores the records of the copies. The app gives the Manager an
// implementation on state.db.
type CopyRecorder interface {
	// AddCopy stores c and returns it with its ID and Stamp.
	AddCopy(c Copy) (Copy, error)
	// FinishCopy sets the hash of the skill folder after the change.
	FinishCopy(id int64, hashAfter string) error
	GetCopy(id int64) (Copy, error)
	ListCopies(name string, limit int) ([]Copy, error)
	MarkCopyUndone(id, undoID int64) error
	// PruneCopies deletes the rows of name after the newest keep rows and
	// returns the deleted rows.
	PruneCopies(name string, keep int) ([]Copy, error)
}

// LockSkills takes a blocking exclusive flock on <dir>/.lock and returns the
// function that releases it. It makes dir when dir does not exist. The lock
// works between processes, as Manager.mu works inside one process.
func LockSkills(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("cannot create the skills folder %s: %w", dir, err)
	}
	path := filepath.Join(dir, ".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cannot open the skills lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("cannot lock %s: %w", path, err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// historyCopyDir returns the folder of the copy with the row id.
func historyCopyDir(dir string, id int64) string {
	return filepath.Join(dir, historyDir, strconv.FormatInt(id, 10))
}

// historyKeep returns the number of copies to keep for each skill.
func (m *Manager) historyKeep() int {
	if m.HistoryKeep > 0 {
		return m.HistoryKeep
	}
	return defaultHistoryKeep
}

// beginCopy stores the row of a change and, for each action other than create,
// copies the skill folder src. It returns a zero Copy when Copies is nil. A
// failed copy removes its folder and keeps no row open for the caller.
func (m *Manager) beginCopy(o Origin, name, action, src string) (Copy, error) {
	if m.Copies == nil {
		return Copy{}, nil
	}
	rel := name
	if action != "create" {
		r, err := filepath.Rel(m.Dir, src)
		if err != nil {
			return Copy{}, fmt.Errorf("cannot find the skill folder: %w", err)
		}
		rel = filepath.ToSlash(r)
	}
	c, err := m.Copies.AddCopy(Copy{Name: name, RelDir: rel, At: time.Now(), Action: action, Source: o.Source, SessionID: o.SessionID})
	if err != nil {
		return Copy{}, fmt.Errorf("cannot record the copy of skill %s: %w", name, err)
	}
	if action == "create" {
		return c, nil
	}
	nested, _, err := scanDir(src, false)
	if err != nil {
		return Copy{}, err
	}
	var skip []string
	for _, n := range nested {
		if n.Dir == src {
			continue
		}
		if r, err := filepath.Rel(src, n.Dir); err == nil {
			skip = append(skip, filepath.ToSlash(r))
		}
	}
	if err := copyTree(src, historyCopyDir(m.Dir, c.ID), skip); err != nil {
		os.RemoveAll(historyCopyDir(m.Dir, c.ID))
		return Copy{}, fmt.Errorf("cannot copy skill %s before the change: %w", name, err)
	}
	return c, nil
}

// abortCopy removes the folder of a copy after a failed change. The row stays.
func (m *Manager) abortCopy(c Copy) {
	if c.ID > 0 {
		os.RemoveAll(historyCopyDir(m.Dir, c.ID))
	}
}

// finishCopy sets the hash after the change and prunes the old copies. It
// returns c with its HashAfter. An error of the recorder does not fail the
// change, because the change is on disk.
func (m *Manager) finishCopy(c Copy, hashDir string) Copy {
	if c.ID == 0 {
		return c
	}
	if hashDir != "" {
		if h, _, err := ContentHash(hashDir); err == nil {
			c.HashAfter = h
		}
	}
	_ = m.Copies.FinishCopy(c.ID, c.HashAfter)
	removed, _ := m.Copies.PruneCopies(c.Name, m.historyKeep())
	for _, r := range removed {
		os.RemoveAll(historyCopyDir(m.Dir, r.ID))
	}
	return c
}

// copyTree copies the folder src to dst. It leaves out the folders in skip
// (slash paths under src). Regular files keep their mode, and a symlink is
// copied as a symlink.
func copyTree(src, dst string, skip []string) error {
	skipSet := map[string]bool{}
	for _, s := range skip {
		skipSet[s] = true
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			if skipSet[filepath.ToSlash(rel)] {
				return fs.SkipDir
			}
			return os.MkdirAll(out, 0o755)
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, out)
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			return copyFile(p, out, info.Mode().Perm())
		}
		return nil
	})
}
