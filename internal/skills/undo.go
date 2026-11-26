package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	// ErrCopyStamp means the stamp of the row is not the stamp of the request.
	ErrCopyStamp = errors.New("skills: the stamp does not match the copy")
	// ErrCopyChanged means the skill changed after the copy.
	ErrCopyChanged = errors.New("skills: the skill changed after this copy")
	// ErrCopyUndone means the copy is undone already.
	ErrCopyUndone = errors.New("skills: the copy is undone already")
)

// UndoCopy reverts the change that the row id records. It changes nothing when
// the skill changed after the row.
func (m *Manager) UndoCopy(o Origin, id int64, stamp string) (Copy, error) {
	unlock, err := m.lock()
	if err != nil {
		return Copy{}, err
	}
	defer unlock()
	if m.Copies == nil {
		return Copy{}, errors.New("skills: no copy history")
	}
	row, err := m.Copies.GetCopy(id)
	if err != nil {
		return Copy{}, err
	}
	if row.Stamp != stamp {
		return Copy{}, ErrCopyStamp
	}
	if m.copyUndone(row) {
		return Copy{}, ErrCopyUndone
	}
	return m.undoRow(o, row)
}

// RedoCopy reverts the undo of the row id. The row id is the row of the
// notice, not the row of the undo.
func (m *Manager) RedoCopy(o Origin, id int64, stamp string) (Copy, error) {
	unlock, err := m.lock()
	if err != nil {
		return Copy{}, err
	}
	defer unlock()
	if m.Copies == nil {
		return Copy{}, errors.New("skills: no copy history")
	}
	row, err := m.Copies.GetCopy(id)
	if err != nil {
		return Copy{}, err
	}
	if row.Stamp != stamp {
		return Copy{}, ErrCopyStamp
	}
	if !m.copyUndone(row) {
		return Copy{}, fmt.Errorf("skills: copy %d is not undone", id)
	}
	undo, err := m.Copies.GetCopy(row.UndoneBy)
	if err != nil {
		return Copy{}, err
	}
	return m.undoRow(o, undo)
}

// copyUndone reports whether the undo of row is in force. An undo that is
// undone itself (a redo) puts the row back in force.
// CopyUndone reports whether row is undone now: an Undo reversed it, and no Redo did it again.
func (m *Manager) CopyUndone(row Copy) bool { return m.copyUndone(row) }

func (m *Manager) copyUndone(row Copy) bool { return m.copyUndoneDepth(row, 0) }

func (m *Manager) copyUndoneDepth(row Copy, depth int) bool {
	if row.UndoneBy == 0 || depth >= 64 {
		return false
	}
	undo, err := m.Copies.GetCopy(row.UndoneBy)
	if err != nil {
		return true
	}
	return !m.copyUndoneDepth(undo, depth+1)
}

// undoRow reverts one row. The caller holds the locks.
func (m *Manager) undoRow(o Origin, row Copy) (Copy, error) {
	cur := filepath.Join(m.Dir, filepath.FromSlash(row.RelDir))
	if row.RelDir == "" || !within(m.Dir, cur) || cur == filepath.Clean(m.Dir) {
		return Copy{}, fmt.Errorf("skills: the copy %d has a folder outside the skills folder", row.ID)
	}
	have := ""
	if _, err := os.Lstat(cur); err == nil {
		h, _, err := ContentHash(cur)
		if err != nil {
			return Copy{}, err
		}
		have = h
	}
	if have != row.HashAfter {
		return Copy{}, ErrCopyChanged
	}
	var nested []string
	if have != "" {
		list, _, err := scanDir(cur, false)
		if err != nil {
			return Copy{}, err
		}
		var names []string
		for _, n := range list {
			if n.Dir == cur {
				continue
			}
			names = append(names, n.Name)
			if r, err := filepath.Rel(cur, n.Dir); err == nil {
				nested = append(nested, filepath.ToSlash(r))
			}
		}
		if row.Action == "create" && len(names) > 0 {
			return Copy{}, fmt.Errorf("the folder of skill %s also holds the skills %s; delete those first", row.Name, strings.Join(names, ", "))
		}
	}
	if row.Action == "delete" {
		if have != "" {
			return Copy{}, ErrCopyChanged
		}
		if _, ok, err := m.find(row.Name); err != nil {
			return Copy{}, err
		} else if ok {
			return Copy{}, ErrCopyChanged
		}
	}
	src := historyCopyDir(m.Dir, row.ID)
	if row.Action != "create" {
		if _, err := os.Stat(src); err != nil {
			return Copy{}, fmt.Errorf("skills: the copy folder of row %d is gone: %w", row.ID, err)
		}
	}

	inverse := row.Action
	switch row.Action {
	case "create":
		inverse = "delete"
	case "delete":
		inverse = "create"
	}
	undo, err := m.Copies.AddCopy(Copy{Name: row.Name, RelDir: row.RelDir, At: time.Now(), Action: inverse, Source: o.Source, SessionID: o.SessionID})
	if err != nil {
		return Copy{}, fmt.Errorf("cannot record the undo of skill %s: %w", row.Name, err)
	}
	tmp := filepath.Join(filepath.Dir(cur), fmt.Sprintf(".%s.undo-%d", filepath.Base(cur), undo.ID))
	hashDir := cur
	fail := func(err error) (Copy, error) {
		os.RemoveAll(tmp)
		return Copy{}, err
	}
	switch row.Action {
	case "create":
		if err := copyTree(cur, historyCopyDir(m.Dir, undo.ID), nil); err != nil {
			os.RemoveAll(historyCopyDir(m.Dir, undo.ID))
			return Copy{}, fmt.Errorf("cannot copy skill %s before the undo: %w", row.Name, err)
		}
		if err := os.RemoveAll(cur); err != nil {
			return Copy{}, err
		}
		hashDir = ""
	case "delete":
		if err := os.MkdirAll(filepath.Dir(cur), 0o755); err != nil {
			return Copy{}, err
		}
		if err := copyTree(src, tmp, nil); err != nil {
			return fail(err)
		}
		if err := os.Rename(tmp, cur); err != nil {
			return fail(err)
		}
	default:
		if err := copyTree(cur, historyCopyDir(m.Dir, undo.ID), nested); err != nil {
			os.RemoveAll(historyCopyDir(m.Dir, undo.ID))
			return Copy{}, fmt.Errorf("cannot copy skill %s before the undo: %w", row.Name, err)
		}
		if err := copyTree(src, tmp, nil); err != nil {
			return fail(err)
		}
		for _, n := range nested {
			dst := filepath.Join(tmp, filepath.FromSlash(n))
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return fail(err)
			}
			if err := os.RemoveAll(dst); err != nil {
				return fail(err)
			}
			if err := os.Rename(filepath.Join(cur, filepath.FromSlash(n)), dst); err != nil {
				return fail(err)
			}
		}
		if err := os.RemoveAll(cur); err != nil {
			return fail(err)
		}
		if err := os.Rename(tmp, cur); err != nil {
			return fail(err)
		}
	}
	if inverse != "create" {
		undo.Path = filepath.ToSlash(filepath.Join(historyDir, fmt.Sprint(undo.ID)))
	}
	undo = m.finishCopy(undo, hashDir)
	_ = m.Copies.MarkCopyUndone(row.ID, undo.ID)
	return undo, nil
}
