package memory

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
	"time"
)

// The limits of the memory notes.
const (
	// maxTopicBytes is the maximum length of a topic. A topic has only ASCII.
	maxTopicBytes = 48
	// maxNoteBytes is the maximum size of a note that the store reads.
	maxNoteBytes = 1 << 20
	// binaryCheckBytes is the length of the start of a note that the
	// store examines for a NUL byte.
	binaryCheckBytes = 8 << 10
)

// errNotesNotFolder tells that notes is a symlink or a file. The store never
// follows a symlink at notes, so that it never reads or writes outside the
// memory folder.
var errNotesNotFolder = errors.New("memory/notes is not a folder; move it away, and then try again")

// The errors of readNote for a note that the store does not read. Callers
// add the topic and their own text.
var (
	errNoteTooLarge = errors.New("the note is larger than 1 MiB")
	errNoteBinary   = errors.New("the note has a NUL byte, so it is not a text file")
)

// unreadableNote returns the text for the model when err is errNoteTooLarge
// or errNoteBinary, and nil for other errors. Read, the note fallback, and
// the append share it, so that the model gets one text and one next action.
func unreadableNote(topic string, err error) error {
	switch {
	case errors.Is(err, errNoteTooLarge):
		return fmt.Errorf("note %s is larger than 1 MiB; open it with read_file, or split it", topic)
	case errors.Is(err, errNoteBinary):
		return fmt.Errorf("note %s is a binary file; open it with read_file", topic)
	}
	return nil
}

// note is one file notes/<topic>.md.
type note struct {
	// Topic is the file name with no ".md".
	Topic string
	// Front holds the raw frontmatter lines between the "---" lines, with
	// no "modified:" line. A write keeps them in their order.
	Front []string
	// Type is the value of the "type:" line, or empty.
	Type string
	// Modified is the "modified:" value, else the mtime of the file.
	Modified time.Time
	// Entries are the entries of the body.
	Entries []string
}

// normalizeTopic returns the topic of a name from the model. The result has
// only [a-z0-9-], and has no "-" at the start or at the end.
func normalizeTopic(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	t := strings.Trim(b.String(), "-")
	if len(t) > maxTopicBytes {
		t = t[:maxTopicBytes]
	}
	return strings.TrimRight(t, "-")
}

// IsNoteName reports whether name is the file name of a note: the name ends
// in ".md", and its stem is not empty and does not change in normalizeTopic.
// The store and the backup both use this rule.
func IsNoteName(name string) bool {
	stem, ok := strings.CutSuffix(name, ".md")
	return ok && stem != "" && stem == normalizeTopic(stem)
}

// joinCapped joins items with ", ". With more than max items, it shows the
// first max items and the number of the other items.
func joinCapped(items []string, max int) string {
	if len(items) <= max {
		return strings.Join(items, ", ")
	}
	more := fmt.Sprintf("%d more", len(items)-max)
	if max <= 0 {
		return more
	}
	return strings.Join(items[:max], ", ") + ", and " + more
}

// notesPath returns the path of the notes folder.
func (s *Store) notesPath() string { return filepath.Join(s.dir, "notes") }

// notesDir examines the notes folder with os.Lstat. It returns an error
// that wraps fs.ErrNotExist when the folder is missing, and
// errNotesNotFolder when it is a symlink or a file.
func (s *Store) notesDir() error {
	fi, err := os.Lstat(s.notesPath())
	if errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err != nil {
		return fmt.Errorf("cannot read the memory notes folder: %w", err)
	}
	if !fi.IsDir() {
		return errNotesNotFolder
	}
	return nil
}

// listNotes returns the topics of the regular files in notes/ that have a
// note name, in name order. skipped counts the other entries of the folder.
// A missing folder gives no names and no error.
func (s *Store) listNotes() (names []string, skipped int, err error) {
	if err := s.notesDir(); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	des, err := os.ReadDir(s.notesPath())
	if err != nil {
		return nil, 0, fmt.Errorf("cannot list the memory notes: %w", err)
	}
	for _, de := range des {
		// The type of a DirEntry does not follow a symlink.
		if de.Type().IsRegular() && IsNoteName(de.Name()) {
			names = append(names, strings.TrimSuffix(de.Name(), ".md"))
			continue
		}
		// A temp file of writeAtomic is a part of a write, not a skipped file.
		if isTempName(de.Name()) {
			continue
		}
		skipped++
	}
	return names, skipped, nil
}

// isTempName reports whether name is the name of a temp file of writeAtomic.
func isTempName(name string) bool {
	ok, _ := filepath.Match(tempPattern, name)
	return ok
}

// readNote reads the note of topic. A missing note, a topic that is not a
// note name, and a file that is not regular give an error that wraps
// fs.ErrNotExist. The size check comes before the open, so that the store
// never reads a large file.
func (s *Store) readNote(topic string) (note, error) {
	if err := s.notesDir(); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return note{}, fmt.Errorf("no note %s: %w", topic, fs.ErrNotExist)
		}
		return note{}, err
	}
	if !IsNoteName(topic + ".md") {
		return note{}, fmt.Errorf("no note %q: %w", topic, fs.ErrNotExist)
	}
	path := filepath.Join(s.notesPath(), topic+".md")
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && !fi.Mode().IsRegular()) {
		return note{}, fmt.Errorf("no note %s: %w", topic, fs.ErrNotExist)
	}
	if err != nil {
		return note{}, fmt.Errorf("cannot read note %s: %w", topic, err)
	}
	if fi.Size() > maxNoteBytes {
		return note{}, errNoteTooLarge
	}
	// O_NOFOLLOW stops a symlink that replaced the file after the Lstat.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return note{}, fmt.Errorf("no note %s: %w", topic, fs.ErrNotExist)
	}
	if err != nil {
		return note{}, fmt.Errorf("cannot read note %s: %w", topic, err)
	}
	defer f.Close()
	// The limit stops a file that grew after the Lstat.
	b, err := io.ReadAll(io.LimitReader(f, maxNoteBytes+1))
	if err != nil {
		return note{}, fmt.Errorf("cannot read note %s: %w", topic, err)
	}
	if len(b) > maxNoteBytes {
		return note{}, errNoteTooLarge
	}
	if bytes.IndexByte(b[:min(len(b), binaryCheckBytes)], 0) >= 0 {
		return note{}, errNoteBinary
	}

	n := note{Topic: topic, Modified: fi.ModTime()}
	body := strings.ReplaceAll(string(b), "\r\n", "\n")
	lines := strings.Split(body, "\n")
	if lines[0] == "---" {
		for j := 1; j < len(lines); j++ {
			if lines[j] != "---" {
				continue
			}
			for _, line := range lines[1:j] {
				if v, ok := strings.CutPrefix(line, "modified:"); ok {
					if t, err := time.Parse(time.RFC3339, strings.TrimSpace(v)); err == nil {
						n.Modified = t
					}
					continue
				}
				n.Front = append(n.Front, line)
				if v, ok := strings.CutPrefix(line, "type:"); ok {
					n.Type = strings.TrimSpace(v)
				}
			}
			body = strings.Join(lines[j+1:], "\n")
			break
		}
	}
	n.Entries = splitEntries(body)
	return n, nil
}

// writeNote writes n to notes/<topic>.md and sets modified to the time of
// now. A note with no entries is deleted. Call it with the lock held.
func (s *Store) writeNote(n note) error {
	if !IsNoteName(n.Topic + ".md") {
		return fmt.Errorf("the topic %q is not a note name", n.Topic)
	}
	if err := s.notesDir(); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.Mkdir(s.notesPath(), 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("cannot create the memory notes folder: %w", err)
		}
		// A second process can make the folder, or a file, at the same time.
		if err := s.notesDir(); err != nil {
			return err
		}
	}
	path := filepath.Join(s.notesPath(), n.Topic+".md")
	if len(n.Entries) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("cannot delete note %s: %w", n.Topic, err)
		}
		return nil
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	front := n.Front
	if len(front) == 0 && n.Type != "" {
		front = []string{"type: " + n.Type}
	}
	var b strings.Builder
	b.WriteString("---\n")
	for _, line := range front {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("modified: ")
	b.WriteString(now().UTC().Truncate(time.Second).Format(time.RFC3339))
	b.WriteString("\n---\n")
	b.WriteString(strings.Join(n.Entries, Separator))
	b.WriteByte('\n')
	return writeAtomic(path, b.String())
}
