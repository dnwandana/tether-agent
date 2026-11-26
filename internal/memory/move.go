package memory

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// archiveTopic returns the note that holds the entries that a tidy run takes out of target.
func archiveTopic(target string) string { return "archive-" + target }

// errTidyTopic is the refusal of a tidy remove that names a note.
const errTidyTopic = "memory tidy removes only entries of memory, user, and rule; it moves each one to note archive-<target>"

// move moves one entry between the tier-1 file op.Target and the note topic.
// With op.To "note", the entry goes from the file to the note. With op.To
// "file", the entry goes from the note to the file. The destination is
// written first, so a failed write keeps two copies and never loses the
// entry. Call it with the lock held.
func (s *Store) move(ctx context.Context, op Op, path string, limit int, entries []string, oldText, topic string) (Result, error) {
	if op.To == "note" {
		i, err := find(entries, oldText, op.Exact, op.Mask)
		if err != nil {
			return Result{}, err
		}
		if i < 0 {
			return Result{}, fmt.Errorf("no entry matches %q in %s", oldText, op.Target)
		}
		return s.moveToNote(ctx, op, path, limit, entries, i, topic)
	}

	n, err := s.openNote(topic)
	if err != nil {
		return Result{}, err
	}
	i, err := find(n.Entries, oldText, false, nil)
	if err != nil {
		return Result{}, err
	}
	if i < 0 {
		return Result{}, fmt.Errorf("no entry matches %q in note %s", oldText, topic)
	}
	entry := n.Entries[i]
	next := entries
	had := slices.Contains(entries, entry)
	if !had {
		next = append(slices.Clip(entries), entry)
		if size(next) > limit {
			if op.Target == TargetRule {
				return Result{}, rulesFull(entries, limit)
			}
			return Result{}, fmt.Errorf("the entry does not fit in %s (%d/%d characters), so nothing moved; "+
				"remove or shorten an entry of %s first", op.Target, size(entries), limit, op.Target)
		}
		if err := writeAtomic(path, strings.Join(next, Separator)); err != nil {
			return Result{}, err
		}
	}
	n.Entries = append(n.Entries[:i:i], n.Entries[i+1:]...)
	if err := s.writeNote(n); err != nil {
		return Result{}, err
	}
	if had {
		// The file did not change. A move row would let Undo take the older file
		// entry out, so the row is a remove from the note. Undo only puts the entry back.
		ids := s.recordIDs(ctx, op, Edit{Target: noteTarget(topic), Idx: i, Action: ActionRemove, OldText: entry})
		return Result{Text: s.summary(op.Target, next, limit, "The file "+op.Target+" had the entry already. Entry removed from note "+topic+"."), Tier1Changed: true, Note: topic, EditIDs: ids}, nil
	}
	ids := s.recordIDs(ctx, op, Edit{Target: noteTarget(topic), Idx: i, Action: ActionMove, Dest: op.Target, OldText: entry, NewText: entry})
	return Result{Text: s.summary(op.Target, next, limit, "Entry moved from note "+topic+"."), Tier1Changed: true, Note: topic, EditIDs: ids}, nil
}

// moveToNote moves entry i of the tier-1 file to the note topic. The note
// write comes first, then the file write. A tidy remove uses it too. Call it
// with the lock held.
func (s *Store) moveToNote(ctx context.Context, op Op, path string, limit int, entries []string, i int, topic string) (Result, error) {
	entry := entries[i]
	name, _, _, err := s.appendToNote(topic, op.Target, entry)
	if err != nil {
		return Result{}, err
	}
	rest := append(entries[:i:i], entries[i+1:]...)
	if err := writeAtomic(path, strings.Join(rest, Separator)); err != nil {
		return Result{}, err
	}
	ids := s.recordIDs(ctx, op, Edit{Target: op.Target, Idx: i, Action: ActionMove, Dest: noteTarget(name), OldText: entry, NewText: entry})
	return Result{Text: s.summary(op.Target, rest, limit, "Entry moved to note "+name+"."), Tier1Changed: true, Note: name, EditIDs: ids}, nil
}
