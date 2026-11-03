package memory

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
)

// The limits of a search.
const (
	maxSearchHits  = 8
	maxSearchNotes = 2000
	maxSearchBytes = 16 << 20
	maxQueryWords  = 16
	snippetRunes   = 200
)

// searchSource is a tier-1 file or a note that a search reads.
type searchSource struct {
	name     string // "memory.md", "user_profile.md", or the topic
	isNote   bool
	typ      string
	modified time.Time
	entries  []string
	matched  int
	hits     int
	// best is the entry for the snippet: the first entry with the most distinct query
	// words.
	best string
}

// Search returns the entries of tier 1 and of the notes that match the words
// of query, best first. It takes no lock.
func (s *Store) Search(query string) (string, error) {
	words := queryWords(query)
	if len(words) == 0 {
		return "", errors.New("query is required for search; give one or more words")
	}

	// The search takes no lock: writeAtomic renames a file, so the search
	// reads the old file or the new file.
	var srcs []searchSource
	for _, t := range []struct{ target, file string }{
		{TargetMemory, memoryFile},
		{TargetUser, profileFile},
	} {
		src := searchSource{name: t.file, entries: s.Entries(t.target)}
		if fi, err := os.Lstat(filepath.Join(s.dir, t.file)); err == nil && fi.Mode().IsRegular() {
			src.modified = fi.ModTime()
		}
		srcs = append(srcs, src)
	}

	names, skipped, err := s.listNotes()
	if errors.Is(err, errNotesNotFolder) {
		// A search acts as if no notes exist when notes is not a folder.
		names, skipped, err = nil, 0, nil
	}
	if err != nil {
		return "", err
	}
	notes, noteSkipped, stop := s.scanNotes(names)
	srcs = append(srcs, notes...)
	skipped += noteSkipped

	var hits []searchSource
	for i := range srcs {
		score(&srcs[i], words)
		if srcs[i].matched > 0 && len(srcs[i].entries) > 0 {
			hits = append(hits, srcs[i])
		}
	}
	slices.SortStableFunc(hits, func(a, b searchSource) int {
		if a.matched != b.matched {
			return b.matched - a.matched
		}
		if a.hits != b.hits {
			return b.hits - a.hits
		}
		if c := b.modified.Compare(a.modified); c != 0 {
			return c
		}
		return strings.Compare(a.name, b.name)
	})
	if len(hits) > maxSearchHits {
		hits = hits[:maxSearchHits]
	}

	q := strings.TrimSpace(query)
	var lines []string
	switch {
	case len(hits) > 0:
		lines = append(lines, fmt.Sprintf("%d matches for %q:", len(hits), q))
		for i, h := range hits {
			head := h.name
			if h.isNote {
				typ := ""
				if h.typ != "" {
					typ = "type " + h.typ + ", "
				}
				head = fmt.Sprintf("note %s (%smodified %s, %d entries)",
					h.name, typ, h.modified.UTC().Format(time.RFC3339), len(h.entries))
			}
			lines = append(lines,
				fmt.Sprintf("%d. %s", i+1, head),
				"   "+snippet(h.best, words))
		}
	case len(names) > 0:
		lines = append(lines, fmt.Sprintf("No entry matches %q. The notes are: %s.", q, joinCapped(names, 40)))
	default:
		lines = append(lines, fmt.Sprintf("No entry matches %q; there are no notes yet.", q))
	}
	if stop != "" {
		lines = append(lines, "The search stopped at "+stop+"; some notes were not searched.")
	}
	if skipped > 0 {
		lines = append(lines, fmt.Sprintf("%d files in memory/notes were skipped: not a note name, larger than 1 MiB, or binary.", skipped))
	}
	return strings.Join(lines, "\n"), nil
}

// queryWords returns the lowercase words of query with no duplicates, at most
// maxQueryWords. A word is a run of letters and digits.
func queryWords(query string) []string {
	var words []string
	for _, f := range strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		w := strings.ToLower(f)
		if slices.Contains(words, w) {
			continue
		}
		words = append(words, w)
		if len(words) == maxQueryWords {
			break
		}
	}
	return words
}

// scanNotes reads the notes in names, in order, until a limit. stop is
// "2000 notes", "16 MiB", or "" when the scan read all names. skipped counts
// the notes that are too large, binary, or not readable, so that one bad
// note does not stop the search.
func (s *Store) scanNotes(names []string) (srcs []searchSource, skipped int, stop string) {
	skipped, stop = s.eachNote(names, func(n note) {
		srcs = append(srcs, searchSource{
			name:     n.Topic,
			isNote:   true,
			typ:      n.Type,
			modified: n.Modified,
			entries:  n.Entries,
		})
	})
	return srcs, skipped, stop
}

// eachNote reads the notes in names, in order, and calls f for each note, until a
// limit: maxSearchNotes notes or maxSearchBytes of entries. stop is "2000 notes",
// "16 MiB", or "" when it read all names. skipped counts the notes that are too large,
// binary, or not readable. A note that is gone after the list is not a skip.
func (s *Store) eachNote(names []string, f func(note)) (skipped int, stop string) {
	total := 0
	for i, name := range names {
		if i >= maxSearchNotes {
			return skipped, "2000 notes"
		}
		if total >= maxSearchBytes {
			return skipped, "16 MiB"
		}
		n, err := s.readNote(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			skipped++
			continue
		}
		total += entriesBytes(n.Entries)
		f(n)
	}
	return skipped, ""
}

// entriesBytes returns the length of the entries joined with Separator, with no join.
func entriesBytes(entries []string) int {
	n := max(len(entries)-1, 0) * len(Separator)
	for _, e := range entries {
		n += len(e)
	}
	return n
}

// score sets src.matched, src.hits, and src.best for words. It lowercases each entry
// one time. A query word has no newline, so the count in each entry is the count in the
// entries joined with newlines.
func score(src *searchSource, words []string) {
	// A query word matches a whole word of the topic name, so "in" does not match
	// "inbox-user". A topic name has only lowercase letters, digits, and "-".
	var topic []string
	if src.isNote {
		topic = strings.Split(src.name, "-")
	}
	counts := make([]int, len(words))
	src.best = ""
	bestCount := -1
	for _, e := range src.entries {
		low := strings.ToLower(e)
		distinct := 0
		for j, w := range words {
			if c := strings.Count(low, w); c > 0 {
				counts[j] += c
				distinct++
			}
		}
		// The best entry is the first entry that has the most distinct query words.
		if distinct > bestCount {
			src.best, bestCount = e, distinct
		}
	}
	src.matched, src.hits = 0, 0
	for j, w := range words {
		c := counts[j]
		t := slices.Contains(topic, w)
		if c > 0 || t {
			src.matched++
		}
		src.hits += c
		if t {
			// A word in the topic name weighs more than a word in the body.
			src.hits += 3
		}
	}
}

// snippet returns entry, or a cut of snippetRunes runes round the first
// match, with each match in "[" and "]". The cut is at rune boundaries.
func snippet(entry string, words []string) string {
	r := []rune(entry)
	// The snippet is one line.
	for i, c := range r {
		if c == '\n' || c == '\r' {
			r[i] = ' '
		}
	}
	// unicode.ToLower maps one rune to one rune, so an index in low is an
	// index in r.
	low := make([]rune, len(r))
	for i, c := range r {
		low[i] = unicode.ToLower(c)
	}
	ws := make([][]rune, 0, len(words))
	for _, w := range words {
		if w != "" {
			ws = append(ws, []rune(strings.ToLower(w)))
		}
	}
	// The longest word comes first, so that a long word wins over a word
	// that is a part of it.
	slices.SortStableFunc(ws, func(a, b []rune) int { return len(b) - len(a) })

	type span struct{ from, to int }
	var spans []span
	for i := 0; i < len(low); {
		found := false
		for _, w := range ws {
			if i+len(w) <= len(low) && slices.Equal(low[i:i+len(w)], w) {
				spans = append(spans, span{i, i + len(w)})
				i += len(w)
				found = true
				break
			}
		}
		if !found {
			i++
		}
	}

	start, end := 0, len(r)
	if len(r) > snippetRunes {
		f := 0
		if len(spans) > 0 {
			f = spans[0].from
		}
		start = max(0, min(f-40, len(r)-snippetRunes))
		end = start + snippetRunes
	}

	var b strings.Builder
	if start > 0 {
		b.WriteString("…")
	}
	k := 0
	for k < len(spans) && spans[k].from < start {
		k++
	}
	for i := start; i < end; i++ {
		if k < len(spans) && spans[k].from == i && spans[k].to <= end {
			b.WriteByte('[')
		}
		b.WriteRune(r[i])
		if k < len(spans) && spans[k].to == i+1 && spans[k].from >= start {
			b.WriteByte(']')
			k++
		}
	}
	if end < len(r) {
		b.WriteString("…")
	}
	return b.String()
}
