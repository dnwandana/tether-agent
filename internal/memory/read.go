package memory

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxReadChars is the largest result of Read in characters (runes).
const MaxReadChars = 32000

// maxTopics is the largest number of topics that Topics returns.
const maxTopics = 2000

// Read returns the note topic as a header line and numbered entries, from
// the entry from. The result has at most min(maxChars, MaxReadChars) runes;
// maxChars <= 0 means MaxReadChars. It takes no lock.
func (s *Store) Read(topic string, from, maxChars int) (string, error) {
	if strings.TrimSpace(topic) == "" {
		return "", errors.New("topic is required for read")
	}
	t := normalizeTopic(topic)
	if t == "" {
		return "", fmt.Errorf("the topic %q has no letters a-z or digits; use a topic from the topic list", strings.TrimSpace(topic))
	}
	names, _, err := s.listNotes()
	if err != nil && !errors.Is(err, errNotesNotFolder) {
		return "", fmt.Errorf("cannot list memory/notes: %w", err)
	}
	// A symlink in notes/ is not in names, so Read never follows it.
	if !slices.Contains(names, t) {
		return "", noNote(t, names)
	}
	n, err := s.readNote(t)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", noNote(t, names)
	case unreadableNote(t, err) != nil:
		return "", unreadableNote(t, err)
	case err != nil:
		// readNote names the note in its error.
		return "", err
	}

	count := len(n.Entries)
	limit := MaxReadChars
	if maxChars > 0 && maxChars < MaxReadChars {
		limit = maxChars
	}
	var b strings.Builder
	fmt.Fprintf(&b, "note %s (", t)
	if n.Type != "" {
		fmt.Fprintf(&b, "type %s, ", n.Type)
	}
	fmt.Fprintf(&b, "modified %s), %d entries:", n.Modified.UTC().Format(time.RFC3339), count)
	if count == 0 {
		return clampRunes(b.String(), limit), nil
	}
	from = max(from, 1)
	if from > count {
		return "", fmt.Errorf("note %s has %d entries; from must be 1 to %d", t, count, count)
	}

	total := utf8.RuneCountInString(b.String())
	for i := from; i <= count; i++ {
		line := "\n" + fmt.Sprintf("%d. %s", i, n.Entries[i-1])
		runes := utf8.RuneCountInString(line)
		if i == count && total+runes <= limit {
			b.WriteString(line)
			break
		}
		// An entry is shown in full only when the cut line after it also
		// fits, because the next entry can be too long.
		after := utf8.RuneCountInString(cutLine(limit, i, count, true))
		if i < count && total+runes+after <= limit {
			b.WriteString(line)
			total += runes
			continue
		}
		k := i - 1
		if i == from {
			// The first shown entry does not fit, so cut inside it, at a rune
			// boundary.
			cut := cutLine(limit, from, count, from < count)
			keep := max(0, limit-total-utf8.RuneCountInString(cut))
			b.WriteString(string([]rune(line)[:min(keep, runes)]))
			k = from
		}
		b.WriteString(cutLine(limit, k, count, k < count))
		break
	}
	// A limit that is shorter than the header and the cut line cuts them too.
	return clampRunes(b.String(), limit), nil
}

// clampRunes returns the first limit runes of s.
func clampRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:max(0, limit)])
}

// cutLine returns the last line of a Read result that is cut after entry k.
func cutLine(limit, k, count int, again bool) string {
	s := "\n" + fmt.Sprintf("… output cut at %d characters after entry %d; the note has %d entries.", limit, k, count)
	if again {
		s += fmt.Sprintf(" Read again with from %d.", k+1)
	}
	return s
}

// noNote returns the error for a topic that is not in names.
func noNote(topic string, names []string) error {
	if len(names) == 0 {
		//lint:ignore ST1005 the text is a fixed user message with a full sentence.
		return fmt.Errorf("no note %q; there are no notes yet.", topic)
	}
	//lint:ignore ST1005 the text is a fixed user message with full sentences.
	return fmt.Errorf("no note %q. The notes are: %s.", topic, joinCapped(names, 40))
}

// Topics returns the note topics, new first by file mtime, then by name, at
// most 2000. It opens no note, so a prompt build reads no note text.
func (s *Store) Topics() ([]string, error) {
	root, err := s.openNotes()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer root.Close()
	d, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("cannot list the memory notes: %w", err)
	}
	des, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		return nil, fmt.Errorf("cannot list the memory notes: %w", err)
	}
	type entry struct {
		topic string
		mtime time.Time
	}
	var es []entry
	for _, de := range des {
		if !IsNoteName(de.Name()) {
			continue
		}
		fi, err := root.Lstat(de.Name())
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		es = append(es, entry{strings.TrimSuffix(de.Name(), ".md"), fi.ModTime()})
	}
	slices.SortFunc(es, func(a, b entry) int {
		if c := b.mtime.Compare(a.mtime); c != 0 {
			return c
		}
		return strings.Compare(a.topic, b.topic)
	})
	var out []string
	for _, e := range es[:min(len(es), maxTopics)] {
		out = append(out, e.topic)
	}
	return out, nil
}
