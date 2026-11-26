// Package memory holds the facts of the agent (memory.md), the facts of the user
// (user_profile.md), the rules of the owner (rules.md), and the memory notes
// (notes/<topic>.md).
package memory

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/textmatch"
)

// Separator is the text between two entries: a line that holds only "§".
const Separator = "\n§\n"

// The targets of an Op.
const (
	TargetMemory = "memory"
	TargetUser   = "user"
	TargetRule   = "rule"
)

// The actions of an Op.
const (
	ActionAdd     = "add"
	ActionReplace = "replace"
	ActionRemove  = "remove"
	ActionMove    = "move"
)

// The file names in the memory folder.
const (
	memoryFile  = "memory.md"
	profileFile = "user_profile.md"
	ruleFile    = "rules.md"
)

// LockFileName is the name of the lock file in the memory folder. The
// backup takes the same lock, so that it copies no half-written file.
const LockFileName = ".memory.lock"

// Store reads and changes the three memory files. A mutex and a file lock
// serialize the writes, so the background review, the main run, and a
// second process can write at the same time.
type Store struct {
	dir       string
	memLimit  int
	userLimit int
	ruleLimit int
	// EntryMaxChars is the limit in runes of one entry. 0 means maxEntryRunes.
	EntryMaxChars int
	// LockWait is the longest wait for the file lock. 0 means the package value lockWait.
	LockWait time.Duration
	// ReadMaxChars caps a Read result in runes. 0 means MaxReadChars.
	ReadMaxChars int
	// SearchMaxHits is the largest number of hits of a search. 0 means maxSearchHits.
	SearchMaxHits int
	// History gets one row for each change. It can be nil.
	History History
	// Threat returns the refusal text of the memory threat scan for a text, or "".
	// Undo and Redo use it on the text that they put back. It can be nil.
	Threat func(text string) string
	mu     sync.Mutex
	// now gives the time of a note write. Tests set it.
	now func() time.Time
}

// Op is one change to a memory file.
type Op struct {
	// Action is "add", "replace", "remove", or "move".
	Action string
	// Target is "memory" (memory.md), "user" (user_profile.md), or "rule" (rules.md).
	Target string
	// Content is the new entry for "add" and "replace".
	Content string
	// OldText is a unique part of the entry for "replace" and "remove".
	OldText string
	// Topic is the note for add, replace, and remove. A blank topic means
	// no topic.
	Topic string
	// Exact makes OldText match only a tier-1 entry that is equal to it.
	// A remove by entry ID sets it, so a blocked entry that is a part of
	// another entry does not give two matches.
	Exact bool
	// Mask, when set, returns the line that replaces a blocked tier-1 entry
	// in an error text, so the error does not show the raw entry.
	Mask func(entry string) (line string, blocked bool)
	// Source is the origin of the change. See the Source constants.
	Source string
	// SessionID is the session that made the change, or "".
	SessionID string
	// Quote is the words of the owner that the change rests on, or "".
	Quote string
	// QuoteOK is true when the quote check of the caller passed.
	QuoteOK bool
	// Outside lists the outside sources that the run had read when it made the change.
	Outside []string
	// To is the direction of a move: "note" (from Target to the note Topic) or
	// "file" (from the note Topic to Target).
	To string
	// Until is a date YYYY-MM-DD. The store adds the line "until <date>" to the entry.
	Until string
}

// Result is the outcome of Apply.
type Result struct {
	// Text is the result text for the model.
	Text string
	// Tier1Changed is true when memory.md or user_profile.md changed.
	Tier1Changed bool
	// Note is the topic of the note that changed, or "".
	Note string
	// EditIDs holds the ids of the history rows of the change, in write order.
	// It is nil when nothing changed or History is nil.
	EditIDs []int64
}

// maxEntryRunes is the limit in characters (runes) of one entry, in both
// tiers. It is the fallback when Store.EntryMaxChars is 0.
const maxEntryRunes = 4000

// EntryLimit returns the limit in runes of one entry.
func (s *Store) EntryLimit() int {
	if s.EntryMaxChars > 0 {
		return s.EntryMaxChars
	}
	return maxEntryRunes
}

// searchHint ends the result of an add to a note. The background review
// sees only the topic names, so it can save one fact again in other words.
const searchHint = "Before you save another fact on the same subject, search the notes, so that a note does not get the same fact twice."

// alreadyExists is the result text of an add that changes nothing.
const alreadyExists = "The entry already exists; nothing changed."

// inboxTopic returns the topic of the note that gets the entries of target
// that have no topic or that do not fit.
func inboxTopic(target string) string { return "inbox-" + target }

// Open returns a Store for the files in dir and creates dir when it does
// not exist. memLimit and userLimit are the limits in characters (runes) of
// memory.md and user_profile.md, separators included. ruleLimit is the limit of rules.md.
func Open(dir string, memLimit, userLimit, ruleLimit int) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("memory: cannot create the folder %s: %w", dir, err)
	}
	return &Store{dir: dir, memLimit: memLimit, userLimit: userLimit, ruleLimit: ruleLimit, now: time.Now}, nil
}

// Memory returns the entries of memory.md, read from disk on each call.
func (s *Store) Memory() string { return strings.Join(s.Entries(TargetMemory), Separator) }

// Profile returns the entries of user_profile.md, read from disk on each call.
func (s *Store) Profile() string { return strings.Join(s.Entries(TargetUser), Separator) }

// Entries returns the entries of the target file, read from disk. An
// unknown target, a missing file, or a read error gives no entries. Use
// ReadEntries to see the read error.
func (s *Store) Entries(target string) []string {
	entries, _ := s.ReadEntries(target)
	return entries
}

// ReadEntries returns the entries of the target file, read from disk. A
// missing file gives no entries and no error. An unknown target or a file
// that cannot be read gives an error.
func (s *Store) ReadEntries(target string) ([]string, error) {
	path, _, err := s.file(target)
	if err != nil {
		return nil, err
	}
	return readEntries(path)
}

// Apply makes the change of op and returns the result. The error text is
// for the model: it names the cause and the next step.
func (s *Store) Apply(op Op) (Result, error) {
	return s.ApplyContext(context.Background(), op)
}

// ApplyContext is Apply with a context. The history rows ignore the cancel of ctx.
func (s *Store) ApplyContext(ctx context.Context, op Op) (Result, error) {
	path, limit, err := s.file(op.Target)
	if err != nil {
		return Result{}, err
	}
	tidyRemove := op.Action == ActionRemove && op.Source == SourceTidy
	if tidyRemove && strings.TrimSpace(op.Topic) != "" {
		return Result{}, errors.New(errTidyTopic)
	}
	if op.Target == TargetRule && op.Action != ActionMove && strings.TrimSpace(op.Topic) != "" {
		return Result{}, errors.New("the target rule takes no topic, because a rule in a note never loads; remove the topic")
	}
	content := strings.TrimSpace(op.Content)
	oldText := strings.TrimSpace(op.OldText)
	if content, err = s.checkUntil(op, content); err != nil {
		return Result{}, err
	}
	switch op.Action {
	case ActionAdd:
		if content == "" {
			return Result{}, errors.New("content is required for add")
		}
	case ActionReplace:
		if oldText == "" {
			return Result{}, errors.New("old_text is required for replace")
		}
		if content == "" {
			return Result{}, errors.New("content is required for replace; to delete an entry, use remove")
		}
	case ActionRemove:
		if oldText == "" {
			return Result{}, errors.New("old_text is required for remove")
		}
	case ActionMove:
		if oldText == "" {
			return Result{}, errors.New("old_text is required for move")
		}
		if strings.TrimSpace(op.Topic) == "" {
			return Result{}, errors.New("topic is required for move")
		}
		if op.To != "note" && op.To != "file" {
			return Result{}, errors.New(`to must be "note" or "file" for move`)
		}
		if op.Source == SourceCron {
			return Result{}, errors.New("the move action is not open to cron runs")
		}
	default:
		return Result{}, fmt.Errorf("unknown action %q; use add, replace, remove, or move", op.Action)
	}
	if hasSeparatorLine(content) {
		return Result{}, errors.New("content must not have a line that holds only §, because § separates the entries")
	}
	if n := utf8.RuneCountInString(content); op.Action != ActionRemove && n > s.EntryLimit() {
		//lint:ignore ST1005 the text is a fixed message for the model with full sentences.
		return Result{}, fmt.Errorf("the entry has %d characters, and the limit for one entry is %d. "+
			"Make it shorter, or save it as several entries.", n, s.EntryLimit())
	}

	blockOp := op
	blockOp.Content = content
	if err := s.CheckBlocked(ctx, blockOp); err != nil {
		return Result{}, err
	}

	topic := ""
	if strings.TrimSpace(op.Topic) != "" {
		t, err := topicOf(op.Topic)
		if err != nil {
			return Result{}, err
		}
		if IsHeldNote(t) && op.Source != SourceOwner {
			return Result{}, errors.New("note " + HeldNote + " holds memory writes that wait for the owner; the memory tool cannot change it")
		}
		// A wide search in place of a narrow one can change the wrong entry.
		if op.Action != ActionAdd {
			if topic = t; topic == "" {
				return Result{}, fmt.Errorf("the topic %q has no letters a-z or digits; use a topic from the topic list", op.Topic)
			}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lock()
	if err != nil {
		return Result{}, err
	}
	defer unlock()

	entries, err := readEntries(path)
	if err != nil {
		return Result{}, fmt.Errorf("cannot read %s: %w", path, err)
	}

	entries, expiredLine, err := s.expire(ctx, op, path, entries)
	if err != nil {
		return Result{}, err
	}
	var res Result
	switch {
	case op.Action == ActionAdd:
		res, err = s.add(ctx, op, path, limit, entries, content)
	case op.Action == ActionMove:
		res, err = s.move(ctx, op, path, limit, entries, oldText, topic)
	case tidyRemove:
		var i int
		if i, err = find(entries, oldText, op.Exact, op.Mask); err == nil && i < 0 {
			err = fmt.Errorf("no entry matches %q in %s", oldText, op.Target)
		}
		if err == nil {
			res, err = s.moveToNote(ctx, op, path, limit, entries, i, archiveTopic(op.Target))
		}
	default:
		res, err = s.change(ctx, op, path, limit, entries, content, oldText, topic)
	}
	if err == nil && expiredLine != "" {
		res.Text += "\n" + expiredLine
		res.Tier1Changed = true
	}
	if err == nil && s.History != nil && op.Source == SourceChat && op.QuoteOK &&
		(op.Action == ActionAdd || op.Action == ActionReplace) {
		if cerr := s.History.ClearBlock(context.WithoutCancel(ctx), textmatch.Normalize(blockBody(content))); cerr != nil {
			slog.Warn("memory: cannot clear the block", "err", cerr)
		}
	}
	return res, err
}

// change makes a replace or a remove. With a topic, it looks only in that
// note. With no topic, it looks in tier 1 of op.Target first, and then in
// the notes. Call it with the lock held.
func (s *Store) change(ctx context.Context, op Op, path string, limit int, entries []string, content, oldText, topic string) (Result, error) {
	var n note
	var i int
	var err error
	if topic != "" {
		if n, err = s.openNote(topic); err != nil {
			return Result{}, err
		}
		if i, err = find(n.Entries, oldText, false, nil); err != nil {
			return Result{}, err
		}
		if i < 0 {
			return Result{}, fmt.Errorf("no entry matches %q in note %s", oldText, topic)
		}
		return s.changeNote(ctx, op, n, i, content)
	}
	// An ambiguous tier-1 match returns its error before any note is read.
	if i, err = find(entries, oldText, op.Exact, op.Mask); err != nil {
		return Result{}, err
	}
	if i < 0 && op.Target == TargetRule && !op.Exact {
		return Result{}, fmt.Errorf("no entry matches %q in rule", oldText)
	}
	if i < 0 && op.Exact {
		// The error does not quote oldText, because it can be a blocked entry.
		return Result{}, fmt.Errorf("no entry in %s is equal to the given entry; read the memory again", op.Target)
	}
	if i < 0 {
		var (
			skipped int
			stop    string
		)
		if n, i, skipped, stop, err = s.findInNotes(oldText); err != nil {
			return Result{}, err
		}
		if n.Topic == "" && (skipped > 0 || stop != "") {
			// The notes that were not read can hold the entry, so the model must know them.
			msg := fmt.Sprintf("no entry matches %q in %s or in the notes.", oldText, op.Target)
			if skipped > 0 {
				msg += fmt.Sprintf(" %d notes were skipped: larger than 8 MiB, binary, or not readable.", skipped)
			}
			if stop != "" {
				msg += " The read stopped at " + stop + "; some notes were not read. Give a topic."
			}
			return Result{}, errors.New(msg)
		}
		if n.Topic == "" {
			return Result{}, fmt.Errorf("no entry matches %q in %s or in the notes", oldText, op.Target)
		}
		return s.changeNote(ctx, op, n, i, content)
	}

	if op.Action == ActionRemove {
		old := entries[i]
		entries = append(entries[:i:i], entries[i+1:]...)
		if err := writeAtomic(path, strings.Join(entries, Separator)); err != nil {
			return Result{}, err
		}
		ids := s.recordIDs(ctx, op, Edit{Target: op.Target, Idx: i, Action: ActionRemove, OldText: old})
		return Result{Text: s.summary(op.Target, entries, limit, "Entry removed."), Tier1Changed: true, EditIDs: ids}, nil
	}
	next := append([]string(nil), entries...)
	next[i] = content
	// A replace that is not longer than the file of today also changes tier
	// 1, so that a file over its cap can get smaller.
	if n := size(next); n <= limit || n <= size(entries) {
		if err := writeAtomic(path, strings.Join(next, Separator)); err != nil {
			return Result{}, err
		}
		ids := s.recordIDs(ctx, op, Edit{Target: op.Target, Idx: i, Action: ActionReplace, OldText: entries[i], NewText: content})
		return Result{Text: s.summary(op.Target, next, limit, "Entry replaced."), Tier1Changed: true, EditIDs: ids}, nil
	}

	if op.Target == TargetRule {
		return Result{}, rulesFull(entries, limit)
	}
	// The spill writes the inbox before tier 1. The dedupe of the inbox stops
	// a second copy when the model tries again after a failed tier-1 write.
	inbox, count, _, err := s.appendToNote(inboxTopic(op.Target), op.Target, content)
	if err != nil {
		return Result{}, err
	}
	oldEntry := entries[i]
	// The inbox row comes first, as the inbox write did. The remove row joins it by GroupID.
	addID := s.record(ctx, withOp(op, Edit{Target: noteTarget(inbox), Idx: -1, Action: ActionAdd, NewText: content}))
	remaining := append(entries[:i:i], entries[i+1:]...)
	if err := writeAtomic(path, strings.Join(remaining, Separator)); err != nil {
		cause := err
		if u := errors.Unwrap(err); u != nil {
			cause = u
		}
		//lint:ignore ST1005 the text is a fixed message for the model with full sentences.
		return Result{}, fmt.Errorf("the new text is in note %s, but %s could not be written: %v. "+
			"The old entry is still in %s. Remove it with action remove.", inbox, filepath.Base(path), cause, op.Target)
	}
	msg := fmt.Sprintf("The new text does not fit in %s (%d/%d characters), so it went to note %s (%d entries), and the old entry left %s.",
		op.Target, size(entries), limit, inbox, count, op.Target)
	var ids []int64
	if addID != 0 {
		ids = append(ids, addID)
		if rmID := s.record(ctx, withOp(op, Edit{Target: op.Target, Idx: i, Action: ActionRemove, OldText: oldEntry, GroupID: addID})); rmID != 0 {
			ids = append(ids, rmID)
		}
	}
	return Result{Text: s.summary(op.Target, remaining, limit, msg) + "\n" + searchHint, Tier1Changed: true, Note: inbox, EditIDs: ids}, nil
}

// changeNote replaces or removes entry i of n and writes the note. A note
// with no more entries is deleted. Call it with the lock held.
func (s *Store) changeNote(ctx context.Context, op Op, n note, i int, content string) (Result, error) {
	var text string
	old := n.Entries[i]
	if op.Action == ActionRemove {
		n.Entries = append(n.Entries[:i:i], n.Entries[i+1:]...)
		text = fmt.Sprintf("Entry removed from note %s (%d entries).", n.Topic, len(n.Entries))
		if len(n.Entries) == 0 {
			text = fmt.Sprintf("Entry removed from note %s. The note had no more entries, so the note was deleted.", n.Topic)
		}
	} else {
		// A note from an older version can be larger than maxNoteBytes. A
		// replace that does not make the entry longer can change it, so that
		// the model can make the note smaller.
		grows := len(content) > len(n.Entries[i])
		n.Entries[i] = content
		if grows && len(s.noteText(n)) > maxNoteBytes {
			//lint:ignore ST1005 the text is a fixed message for the model with full sentences.
			return Result{}, fmt.Errorf("the new text makes note %s larger than 1 MiB, so nothing changed. "+
				"Remove the old entry with action remove, and then add the new text with action add and topic %s. "+
				"The add puts the entry in the next note of the topic when the note is full.", n.Topic, n.Topic)
		}
		text = fmt.Sprintf("Entry replaced in note %s (%d entries).", n.Topic, len(n.Entries))
	}
	if err := s.writeNote(n); err != nil {
		return Result{}, err
	}
	e := Edit{Target: noteTarget(n.Topic), Idx: i, Action: op.Action, OldText: old}
	if op.Action != ActionRemove {
		e.NewText = content
	}
	return Result{Text: text, Note: n.Topic, EditIDs: s.recordIDs(ctx, op, e)}, nil
}

// openNote reads the note of topic for a replace or a remove, with the limit
// maxChangeBytes. The error of a missing note lists the notes, so that the
// model can select one.
func (s *Store) openNote(topic string) (note, error) {
	n, err := s.readNoteMax(topic, maxChangeBytes)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		names, _, lerr := s.listNotes()
		if lerr != nil {
			return note{}, lerr
		}
		if len(names) == 0 {
			//lint:ignore ST1005 the text is a fixed message for the model with full sentences.
			return note{}, fmt.Errorf("no note %q; there are no notes yet.", topic)
		}
		//lint:ignore ST1005 the text is a fixed message for the model with full sentences.
		return note{}, fmt.Errorf("no note %q. The notes are: %s.", topic, joinCapped(names, 40))
	case unreadableNote(topic, err) != nil:
		return note{}, unreadableNote(topic, err)
	}
	return n, err
}

// findInNotes returns the note and the index of the one note entry that
// contains oldText. It reads the notes in name order, with the note and byte
// limits of a search (eachNote) and the size limit maxChangeBytes. Two entries
// with the same text count as one match, and the first one is the match. No
// match gives a note with an empty topic. A note that is too large, binary, or
// not readable is skipped, and skipped counts these notes, so that one bad note
// does not stop a change in the other notes. stop is the limit that stopped the
// read, or "".
func (s *Store) findInNotes(oldText string) (note, int, int, string, error) {
	names, _, err := s.listNotes()
	if errors.Is(err, errNotesNotFolder) {
		return note{}, 0, 0, "", nil
	}
	if err != nil {
		return note{}, 0, 0, "", err
	}
	type match struct {
		n note
		i int
	}
	var hits []match
	names = slices.DeleteFunc(slices.Clone(names), IsHeldNote)
	skipped, stop := s.eachNote(names, maxChangeBytes, func(n note) {
		for i, e := range n.Entries {
			if strings.Contains(e, oldText) {
				hits = append(hits, match{n, i})
			}
		}
	})
	if len(hits) == 0 {
		return note{}, 0, skipped, stop, nil
	}
	first := hits[0].n.Entries[hits[0].i]
	same := true
	for _, h := range hits[1:] {
		if h.n.Entries[h.i] != first {
			same = false
			break
		}
	}
	if same {
		return hits[0].n, hits[0].i, skipped, stop, nil
	}
	shown := hits[:min(len(hits), 10)]
	texts := make([]string, len(shown))
	for j, h := range shown {
		texts[j] = h.n.Entries[h.i]
	}
	lines := make([]string, len(shown))
	for j, p := range previews(texts) {
		lines[j] = shown[j].n.Topic + ": " + p
	}
	if len(hits) > 10 {
		lines = append(lines, fmt.Sprintf("and %d more", len(hits)-10))
	}
	return note{}, 0, skipped, stop, fmt.Errorf("%q matches %d entries in the notes; use a longer, unique part of the entry, or give a topic. The matches are:\n%s",
		oldText, len(hits), strings.Join(lines, "\n"))
}

// add appends content to tier 1 of op.Target or to a note. An entry with no
// topic that does not fit in the cap goes to the inbox note, so an add is
// never refused because of the cap. Call it with the lock held.
func (s *Store) add(ctx context.Context, op Op, path string, limit int, entries []string, content string) (Result, error) {
	topic, topicLine := "", ""
	if strings.TrimSpace(op.Topic) != "" {
		topic = normalizeTopic(op.Topic)
		if topic == "" {
			topic = inboxTopic(op.Target)
			topicLine = fmt.Sprintf("The topic %q has no letters a-z or digits, so the entry went to note %s.", op.Topic, topic)
		}
	}
	if slices.Contains(entries, content) {
		return Result{Text: s.summary(op.Target, entries, limit, alreadyExists)}, nil
	}
	spill := false
	if topic == "" {
		next := append(slices.Clip(entries), content)
		if size(next) <= limit {
			if err := writeAtomic(path, strings.Join(next, Separator)); err != nil {
				return Result{}, err
			}
			ids := s.recordIDs(ctx, op, Edit{Target: op.Target, Idx: -1, Action: ActionAdd, NewText: content})
			return Result{Text: s.summary(op.Target, next, limit, "Entry added.") + similarLines(entries, content), Tier1Changed: true, EditIDs: ids}, nil
		}
		if op.Target == TargetRule {
			return Result{}, rulesFull(entries, limit)
		}
		topic, spill = inboxTopic(op.Target), true
	}
	name, count, added, err := s.appendToNote(topic, op.Target, content)
	if err != nil {
		return Result{}, err
	}
	if !added {
		return Result{Text: alreadyExists}, nil
	}
	first := fmt.Sprintf("Entry added to note %s (%d entries).", name, count)
	if spill {
		first = fmt.Sprintf("%s is full (%d/%d characters), so the entry went to note %s (%d entries).",
			op.Target, size(entries), limit, name, count)
	}
	lines := []string{first}
	if name != topic {
		lines = append(lines, fmt.Sprintf("Note %s is full (1 MiB), so the entry went to note %s.", topic, name))
	}
	if topicLine != "" {
		lines = append(lines, topicLine)
	}
	lines = append(lines, searchHint)
	ids := s.recordIDs(ctx, op, Edit{Target: noteTarget(name), Idx: -1, Action: ActionAdd, NewText: content})
	return Result{Text: strings.Join(lines, "\n"), Note: name, EditIDs: ids}, nil
}

// appendToNote appends content to the first note of the chain of topic that
// has room for it: topic, then its continuation notes (see chainName). A
// missing note topic has room. When no note has room, it makes the next
// continuation note. Thus an add never makes a note larger than
// maxNoteBytes. A new note gets typ as its type. When a note of the chain
// has an equal entry, no note changes, and added is false. name is the note
// that has the entry, and count is its entry count after the call. Call it
// with the lock held.
func (s *Store) appendToNote(topic, typ, content string) (name string, count int, added bool, err error) {
	names, _, err := s.listNotes()
	if err != nil {
		return "", 0, false, err
	}
	// ks holds 1 and the numbers of the continuation notes that exist, in
	// order.
	ks := []int{1}
	for _, nm := range names {
		i := strings.LastIndexByte(nm, '-')
		if i < 0 {
			continue
		}
		if k, err := strconv.Atoi(nm[i+1:]); err == nil && k > 1 && chainName(topic, k) == nm {
			ks = append(ks, k)
		}
	}
	slices.Sort(ks)

	var dst *note
	for _, k := range ks {
		n, err := s.readNoteMax(chainName(topic, k), maxChangeBytes)
		switch {
		case k == 1 && errors.Is(err, fs.ErrNotExist):
			dst = &note{Topic: topic, Type: typ, Entries: []string{content}}
			continue
		case k > 1 && err != nil:
			// The note is gone, or it is not a text note that the store reads.
			continue
		case errors.Is(err, errNoteTooLargeToChange):
			// The note is full, and the store does not read it.
			continue
		case unreadableNote(topic, err) != nil:
			return "", 0, false, unreadableNote(topic, err)
		case err != nil:
			return "", 0, false, err
		}
		if k > 1 && n.continues() != topic {
			// A note of the model or of another topic has this name.
			continue
		}
		if slices.Contains(n.Entries, content) {
			return n.Topic, len(n.Entries), false, nil
		}
		if dst == nil {
			n.Entries = append(n.Entries, content)
			if len(s.noteText(n)) <= maxNoteBytes {
				dst = &n
			}
		}
	}
	if dst == nil {
		k := 2
		for slices.Contains(names, chainName(topic, k)) {
			k++
		}
		dst = &note{Topic: chainName(topic, k), Type: typ, Entries: []string{content},
			Front: []string{"type: " + typ, continuesKey + " " + topic}}
	}
	if err := s.writeNote(*dst); err != nil {
		return "", 0, false, err
	}
	return dst.Topic, len(dst.Entries), true, nil
}

// AppendHeldNote adds one entry to the chain of HeldNote, with note type held. It is the
// internal path for a held write, so the refusal of ApplyContext does not apply. It writes no
// history row.
func (s *Store) AppendHeldNote(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("the held text is empty")
	}
	if hasSeparatorLine(text) {
		return errors.New("the held text must not have a line that holds only §, because § separates the entries")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	_, _, _, err = s.appendToNote(HeldNote, "held", text)
	return err
}

// file returns the path and the limit of target.
func (s *Store) file(target string) (string, int, error) {
	switch target {
	case TargetMemory:
		return filepath.Join(s.dir, memoryFile), s.memLimit, nil
	case TargetUser:
		return filepath.Join(s.dir, profileFile), s.userLimit, nil
	case TargetRule:
		return filepath.Join(s.dir, ruleFile), s.ruleLimit, nil
	}
	return "", 0, fmt.Errorf("unknown target %q; use memory, user, or rule", target)
}

// rulesFull returns the error for a rule that does not fit. A rule never
// goes to a note, because a note does not load into the prompt.
func rulesFull(entries []string, limit int) error {
	//lint:ignore ST1005 the text is a fixed message for the model with full sentences.
	return fmt.Errorf("The owner rules are full (%d/%d). Ask the owner which rule to remove, "+
		"or ask the owner to raise memory.rules_char_limit.", size(entries), limit)
}

// lockWait is the maximum time that a change waits for the file lock. It is the
// fallback when Store.LockWait is 0. Another process holds the lock only for a
// change or for the copies of a backup.
var lockWait = 30 * time.Second

// lockPoll is the time between two tries of the file lock.
const lockPoll = 50 * time.Millisecond

// lock takes the file lock that serializes writes across processes. A blocked flock
// cannot stop, so it tries without a wait until the wait ends.
func (s *Store) lock() (func(), error) {
	f, err := os.OpenFile(filepath.Join(s.dir, LockFileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cannot open the memory lock file: %w", err)
	}
	wait := s.LockWait
	if wait <= 0 {
		wait = lockWait
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, fmt.Errorf("cannot lock the memory files: %w", err)
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, fmt.Errorf("the memory files stayed locked for %s by another process, for example a backup; try again later", wait)
		}
		time.Sleep(lockPoll)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// summary returns the result text of a change.
func (s *Store) summary(target string, entries []string, limit int, msg string) string {
	pct := 0
	if limit > 0 {
		pct = min(100, size(entries)*100/limit)
	}
	return fmt.Sprintf("%s %s: %d entries, %d%% (%d/%d characters).", msg, target, len(entries), pct, size(entries), limit)
}

// find returns the index of the one entry that contains oldText, or -1
// when no entry matches. Two entries with the same text count as one match
// (Hermes). With exact, an entry matches only when it is equal to oldText.
// A non-nil mask replaces each blocked entry in the list of matches.
func find(entries []string, oldText string, exact bool, mask func(string) (string, bool)) (int, error) {
	idx := -1
	var hits []string
	for i, e := range entries {
		if exact && e != oldText || !exact && !strings.Contains(e, oldText) {
			continue
		}
		if idx < 0 {
			idx = i
		}
		hits = append(hits, e)
	}
	if idx < 0 {
		return -1, nil
	}
	for _, h := range hits[1:] {
		if h != hits[0] {
			shown := previews(hits)
			if mask != nil {
				for j, e := range hits {
					if line, blocked := mask(e); blocked {
						shown[j] = line
					}
				}
			}
			return 0, fmt.Errorf("%q matches %d entries; use a longer, unique part of the entry. The matches are:\n%s",
				oldText, len(hits), numbered(shown))
		}
	}
	return idx, nil
}

// previews returns each entry cut to 80 characters.
func previews(entries []string) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		if utf8.RuneCountInString(e) > 80 {
			e = string([]rune(e)[:80]) + "..."
		}
		out[i] = e
	}
	return out
}

// numbered returns the entries as a numbered list.
func numbered(entries []string) string {
	if len(entries) == 0 {
		return "(no entries)"
	}
	var b strings.Builder
	for i, e := range entries {
		fmt.Fprintf(&b, "%d. %s\n", i+1, e)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// hasSeparatorLine reports whether s has a line that holds only "§".
func hasSeparatorLine(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "§" {
			return true
		}
	}
	return false
}

// size returns the length in runes of the file text of entries.
func size(entries []string) int {
	return utf8.RuneCountInString(strings.Join(entries, Separator))
}

// readEntries returns the entries of the file at path. A missing file has
// no entries.
func readEntries(path string) ([]string, error) {
	// memory.md and user_profile.md go into the system prompt, so the store reads
	// them only as regular files in the memory folder, the same as the notes.
	b, err := readRegularPath(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if errors.Is(err, errNotRegular) {
		return nil, fmt.Errorf("%s is not a regular file (for example a symlink); move it away, and then try again", filepath.Base(path))
	}
	if err != nil {
		return nil, err
	}
	return splitEntries(string(b)), nil
}

// splitEntries returns the entries of a file text. It accepts CRLF line
// endings and drops the empty parts.
func splitEntries(text string) []string {
	var out []string
	for e := range strings.SplitSeq(strings.ReplaceAll(text, "\r\n", "\n"), Separator) {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// tempPattern is the name pattern of the temp files of writeAtomic.
const tempPattern = ".mem-*.tmp"

// writeAtomic writes text to path through a temp file and a rename.
func writeAtomic(path, text string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, tempPattern)
	if err != nil {
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	if _, err := tmp.WriteString(text); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	return nil
}
