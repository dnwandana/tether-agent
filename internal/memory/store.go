// Package memory holds the agent notes (memory.md) and the user facts
// (user_profile.md).
package memory

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"
)

// Separator is the text between two entries: a line that holds only "§".
const Separator = "\n§\n"

// The targets of an Op.
const (
	TargetMemory = "memory"
	TargetUser   = "user"
)

// The actions of an Op.
const (
	ActionAdd     = "add"
	ActionReplace = "replace"
	ActionRemove  = "remove"
)

// The file names in the memory folder.
const (
	memoryFile  = "memory.md"
	profileFile = "user_profile.md"
	lockFile    = ".memory.lock"
)

// Store reads and changes the two memory files. A mutex and a file lock
// serialize the writes, so the background review, the main run, and a
// second process can write at the same time.
type Store struct {
	dir       string
	memLimit  int
	userLimit int
	mu        sync.Mutex
}

// Op is one change to a memory file.
type Op struct {
	// Action is "add", "replace", or "remove".
	Action string
	// Target is "memory" (memory.md) or "user" (user_profile.md).
	Target string
	// Content is the new entry for "add" and "replace".
	Content string
	// OldText is a unique part of the entry for "replace" and "remove".
	OldText string
}

// Open returns a Store for the files in dir and creates dir when it does
// not exist. memLimit and userLimit are the limits in characters (runes) of
// memory.md and user_profile.md, separators included.
func Open(dir string, memLimit, userLimit int) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("memory: cannot create the folder %s: %w", dir, err)
	}
	return &Store{dir: dir, memLimit: memLimit, userLimit: userLimit}, nil
}

// Memory returns the entries of memory.md, read from disk on each call.
func (s *Store) Memory() string { return strings.Join(s.Entries(TargetMemory), Separator) }

// Profile returns the entries of user_profile.md, read from disk on each call.
func (s *Store) Profile() string { return strings.Join(s.Entries(TargetUser), Separator) }

// Entries returns the entries of the target file, read from disk. An
// unknown target or a missing file gives no entries.
func (s *Store) Entries(target string) []string {
	path, _, err := s.file(target)
	if err != nil {
		return nil
	}
	entries, _ := readEntries(path)
	return entries
}

// Apply makes the change of op and returns a short summary. The error text
// is for the model: it names the cause and the next step.
func (s *Store) Apply(op Op) (string, error) {
	path, limit, err := s.file(op.Target)
	if err != nil {
		return "", err
	}
	content := strings.TrimSpace(op.Content)
	oldText := strings.TrimSpace(op.OldText)
	switch op.Action {
	case ActionAdd:
		if content == "" {
			return "", errors.New("content is required for add")
		}
	case ActionReplace:
		if oldText == "" {
			return "", errors.New("old_text is required for replace")
		}
		if content == "" {
			return "", errors.New("content is required for replace; to delete an entry, use remove")
		}
	case ActionRemove:
		if oldText == "" {
			return "", errors.New("old_text is required for remove")
		}
	default:
		return "", fmt.Errorf("unknown action %q; use add, replace, or remove", op.Action)
	}
	if hasSeparatorLine(content) {
		return "", errors.New("content must not have a line that holds only §, because § separates the entries")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lock()
	if err != nil {
		return "", err
	}
	defer unlock()

	entries, err := readEntries(path)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", path, err)
	}

	var msg string
	switch op.Action {
	case ActionAdd:
		for _, e := range entries {
			if e == content {
				return s.summary(op.Target, entries, limit, "The entry already exists; nothing changed."), nil
			}
		}
		next := append(entries[:len(entries):len(entries)], content)
		if n := size(next); n > limit {
			return "", fmt.Errorf("%s is at %d/%d characters, and this entry (%d characters) puts it over the limit. "+
				"Merge, replace, or remove an entry first. The current entries are:\n%s",
				op.Target, size(entries), limit, utf8.RuneCountInString(content), numbered(entries))
		}
		entries, msg = next, "Entry added."
	case ActionReplace, ActionRemove:
		i, err := find(entries, oldText)
		if err != nil {
			return "", err
		}
		if op.Action == ActionRemove {
			entries = append(entries[:i:i], entries[i+1:]...)
			msg = "Entry removed."
			break
		}
		next := append([]string(nil), entries...)
		next[i] = content
		if n := size(next); n > limit {
			return "", fmt.Errorf("the replacement puts %s at %d/%d characters. Make the new content shorter, or remove other entries first",
				op.Target, n, limit)
		}
		entries, msg = next, "Entry replaced."
	}
	if err := writeAtomic(path, strings.Join(entries, Separator)); err != nil {
		return "", err
	}
	return s.summary(op.Target, entries, limit, msg), nil
}

// file returns the path and the limit of target.
func (s *Store) file(target string) (string, int, error) {
	switch target {
	case TargetMemory:
		return filepath.Join(s.dir, memoryFile), s.memLimit, nil
	case TargetUser:
		return filepath.Join(s.dir, profileFile), s.userLimit, nil
	}
	return "", 0, fmt.Errorf("unknown target %q; use memory or user", target)
}

// lock takes the file lock that serializes writes across processes.
func (s *Store) lock() (func(), error) {
	f, err := os.OpenFile(filepath.Join(s.dir, lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cannot open the memory lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("cannot lock the memory files: %w", err)
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

// find returns the index of the one entry that contains oldText. Two
// entries with the same text count as one match (Hermes).
func find(entries []string, oldText string) (int, error) {
	idx := -1
	var hits []string
	for i, e := range entries {
		if !strings.Contains(e, oldText) {
			continue
		}
		if idx < 0 {
			idx = i
		}
		hits = append(hits, e)
	}
	if idx < 0 {
		return 0, fmt.Errorf("no entry matches %q", oldText)
	}
	for _, h := range hits[1:] {
		if h != hits[0] {
			return 0, fmt.Errorf("%q matches %d entries; use a longer, unique part of the entry. The matches are:\n%s",
				oldText, len(hits), numbered(previews(hits)))
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
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), Separator) {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out, nil
}

// writeAtomic writes text to path through a temp file and a rename.
func writeAtomic(path, text string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".mem-*.tmp")
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
