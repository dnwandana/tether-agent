package memory

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
)

// The refusals of Undo and Redo.
var (
	ErrStamp   = errors.New("memory: the stamp does not match the edit; the button is too old")
	ErrChanged = errors.New("memory: the entry changed after this edit, so nothing changed")
	ErrUndone  = errors.New("memory: the edit is undone already")
	// ErrNotUndone is the refusal of a Redo when the edit is open, for example after a Redo of
	// another row of its group.
	ErrNotUndone = errors.New("memory: the edit is not undone; there is nothing to redo")
	ErrThreat    = errors.New("memory: the memory threat scan blocks the text, so nothing changed")
)

const (
	actionUndo = "undo"
	actionRedo = "redo"
)

// Undo reverses the history row id, and the second row of its group. The stamp
// must match the row. It writes one new row for each row that it reverses.
func (s *Store) Undo(ctx context.Context, id int64, stamp string) (Result, error) {
	return s.reverse(ctx, id, stamp, false)
}

// Redo does the change of the history row id again after an Undo. The stamp
// must match the row.
func (s *Store) Redo(ctx context.Context, id int64, stamp string) (Result, error) {
	return s.reverse(ctx, id, stamp, true)
}

// place is one memory file or note that Undo or Redo reads and writes.
type place struct {
	target  string
	entries []string
	write   func([]string) error
	changed bool
}

// reverse is the common part of Undo and Redo.
func (s *Store) reverse(ctx context.Context, id int64, stamp string, redo bool) (Result, error) {
	if s.History == nil {
		return Result{}, errors.New("memory: the history is not available")
	}
	e, err := s.History.Get(ctx, id)
	if err != nil {
		return Result{}, fmt.Errorf("memory: cannot read edit %d: %w", id, err)
	}
	if e.Stamp != stamp {
		return Result{}, ErrStamp
	}
	if e.Action == actionUndo || e.Action == actionRedo {
		return Result{}, fmt.Errorf("memory: edit %d is an undo or a redo; undo or redo the first edit", id)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lock()
	if err != nil {
		return Result{}, err
	}
	defer unlock()

	rows, err := s.group(ctx, e)
	if err != nil {
		return Result{}, err
	}
	done, err := s.undone(ctx, rows[0])
	if err != nil {
		return Result{}, err
	}
	if !redo && done {
		return Result{}, ErrUndone
	}
	if redo && !done {
		return Result{}, fmt.Errorf("edit %d: %w", id, ErrNotUndone)
	}
	// Undo reverses the second row first. Redo goes in the original order.
	order := slices.Clone(rows)
	if !redo {
		slices.Reverse(order)
	}

	places := map[string]*place{}
	var touched []*place
	get := func(target string) (*place, error) {
		if p, ok := places[target]; ok {
			return p, nil
		}
		entries, write, err := s.readPlace(target)
		if err != nil {
			return nil, err
		}
		p := &place{target: target, entries: entries, write: write}
		places[target] = p
		touched = append(touched, p)
		return p, nil
	}
	for _, r := range order {
		if err := s.step(r, redo, get); err != nil {
			return Result{}, err
		}
	}

	// Every check has passed, so the writes start now.
	var res Result
	for _, p := range touched {
		if !p.changed {
			continue
		}
		if err := p.write(p.entries); err != nil {
			return Result{}, err
		}
		if topic, ok := strings.CutPrefix(p.target, "note:"); ok {
			res.Note = topic
		} else {
			res.Tier1Changed = true
		}
	}

	action := actionUndo
	verb := "undone"
	if redo {
		action, verb = actionRedo, "redone"
	}
	var firstID int64
	for _, r := range order {
		ne := Edit{Target: r.Target, Idx: r.Idx, Dest: r.Dest, Action: action, Source: SourceOwner}
		if redo {
			ne.OldText, ne.NewText = r.OldText, r.NewText
		} else {
			ne.OldText, ne.NewText = r.NewText, r.OldText
		}
		if firstID != 0 {
			ne.GroupID = firstID
		}
		newID := s.record(ctx, ne)
		if newID == 0 {
			continue
		}
		if firstID == 0 {
			firstID = newID
		}
		res.EditIDs = append(res.EditIDs, newID)
		marked := r.ID
		if redo {
			marked = r.UndoneBy
		}
		if err := s.History.MarkUndone(context.WithoutCancel(ctx), marked, newID); err != nil {
			return res, fmt.Errorf("memory: the edit changed, but the history row %d could not be marked: %w", marked, err)
		}
		if !redo && r.Action == ActionRemove {
			if err := s.dropBlocks(ctx, r); err != nil {
				return res, err
			}
		}
	}
	res.Text = fmt.Sprintf("Edit %d %s.", id, verb)
	return res, nil
}

// dropBlocks deletes the block rows of a remove row that Undo reverses. A
// remove row that was redone before has a second block row, at the redo row.
func (s *Store) dropBlocks(ctx context.Context, r Edit) error {
	ctx = context.WithoutCancel(ctx)
	if err := s.History.DeleteBlockOf(ctx, r.ID); err != nil {
		return fmt.Errorf("memory: cannot delete the block of edit %d: %w", r.ID, err)
	}
	if r.UndoneBy == 0 {
		return nil
	}
	u, err := s.History.Get(ctx, r.UndoneBy)
	if err != nil || u.UndoneBy == 0 {
		return nil
	}
	if err := s.History.DeleteBlockOf(ctx, u.UndoneBy); err != nil {
		return fmt.Errorf("memory: cannot delete the block of edit %d: %w", u.UndoneBy, err)
	}
	return nil
}

// step checks and applies the reversal of row r to the places in memory. It
// writes no file.
func (s *Store) step(r Edit, redo bool, get func(string) (*place, error)) error {
	scan := func(text string) error {
		if s.Threat == nil {
			return nil
		}
		if msg := s.Threat(text); msg != "" {
			return fmt.Errorf("%w: %s", ErrThreat, msg)
		}
		return nil
	}
	// take removes the entry equal to text from the place of target.
	take := func(target, text string) error {
		p, err := get(target)
		if err != nil {
			return err
		}
		i := slices.Index(p.entries, text)
		if i < 0 {
			return ErrChanged
		}
		p.entries = slices.Delete(slices.Clone(p.entries), i, i+1)
		p.changed = true
		return nil
	}
	// put inserts text at idx, or at the end when idx is out of range.
	put := func(target, text string, idx int) error {
		p, err := get(target)
		if err != nil {
			return err
		}
		if slices.Contains(p.entries, text) {
			return ErrChanged
		}
		if idx < 0 || idx > len(p.entries) {
			idx = len(p.entries)
		}
		p.entries = slices.Insert(slices.Clone(p.entries), idx, text)
		p.changed = true
		return nil
	}
	swap := func(target, from, to string) error {
		p, err := get(target)
		if err != nil {
			return err
		}
		i := slices.Index(p.entries, from)
		if i < 0 {
			return ErrChanged
		}
		p.entries = slices.Clone(p.entries)
		p.entries[i] = to
		p.changed = true
		return nil
	}
	dest := r.Dest
	if dest == "" {
		dest = r.Target
	}
	if !redo {
		switch r.Action {
		case ActionAdd:
			return take(r.Target, r.NewText)
		case ActionRemove:
			if err := scan(r.OldText); err != nil {
				return err
			}
			return put(r.Target, r.OldText, r.Idx)
		case ActionReplace:
			if err := scan(r.OldText); err != nil {
				return err
			}
			return swap(r.Target, r.NewText, r.OldText)
		case ActionMove, "expire":
			if err := scan(r.OldText); err != nil {
				return err
			}
			if err := take(dest, r.NewText); err != nil {
				return err
			}
			return put(r.Target, r.OldText, r.Idx)
		}
		return fmt.Errorf("memory: cannot undo the action %q", r.Action)
	}
	switch r.Action {
	case ActionAdd:
		if err := scan(r.NewText); err != nil {
			return err
		}
		return put(r.Target, r.NewText, -1)
	case ActionRemove:
		return take(r.Target, r.OldText)
	case ActionReplace:
		if err := scan(r.NewText); err != nil {
			return err
		}
		return swap(r.Target, r.OldText, r.NewText)
	case ActionMove, "expire":
		if err := scan(r.NewText); err != nil {
			return err
		}
		if err := take(r.Target, r.OldText); err != nil {
			return err
		}
		return put(dest, r.NewText, -1)
	}
	return fmt.Errorf("memory: cannot redo the action %q", r.Action)
}

// group returns the rows of the group of e, first row first.
func (s *Store) group(ctx context.Context, e Edit) ([]Edit, error) {
	if e.GroupID != 0 {
		first, err := s.History.Get(ctx, e.GroupID)
		if err != nil {
			return nil, fmt.Errorf("memory: cannot read edit %d: %w", e.GroupID, err)
		}
		return []Edit{first, e}, nil
	}
	if next, err := s.History.Get(ctx, e.ID+1); err == nil && next.GroupID == e.ID {
		return []Edit{e, next}, nil
	}
	return []Edit{e}, nil
}

// undone reports whether the row e has an undo row that is not undone.
// IsUndone reports whether e is undone now: an Undo reversed it, and no Redo did it again.
func (s *Store) IsUndone(ctx context.Context, e Edit) (bool, error) {
	if s.History == nil {
		return e.UndoneBy != 0, nil
	}
	return s.undone(ctx, e)
}

func (s *Store) undone(ctx context.Context, e Edit) (bool, error) {
	if e.UndoneBy == 0 {
		return false, nil
	}
	u, err := s.History.Get(ctx, e.UndoneBy)
	if err != nil {
		return false, fmt.Errorf("memory: cannot read edit %d: %w", e.UndoneBy, err)
	}
	return u.UndoneBy == 0, nil
}

// readPlace returns the entries of memory, user, rule, or note:<topic>, and a
// function that writes them back. A missing note has no entries. A note with
// no entries is deleted by the write.
func (s *Store) readPlace(target string) ([]string, func([]string) error, error) {
	if topic, ok := strings.CutPrefix(target, "note:"); ok {
		n, err := s.readNoteMax(topic, maxChangeBytes)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, nil, err
		}
		if err != nil {
			n = note{Topic: topic}
		}
		return slices.Clone(n.Entries), func(entries []string) error {
			n.Entries = entries
			return s.writeNote(n)
		}, nil
	}
	path, _, err := s.file(target)
	if err != nil {
		return nil, nil, err
	}
	entries, err := readEntries(path)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot read %s: %w", filepath.Base(path), err)
	}
	return entries, func(entries []string) error {
		return writeAtomic(path, strings.Join(entries, Separator))
	}, nil
}
