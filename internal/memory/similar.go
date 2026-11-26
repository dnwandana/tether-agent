package memory

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dnwandana/tether-agent/internal/textmatch"
)

// similarHead is the first line of the similar entries list in an add result.
const similarHead = "Similar entries (check that they are still true; replace or remove the old one if not):"

// similarShow is the count of runes of an entry that the list shows.
const similarShow = 100

// SimilarEntry is one entry that is like a new entry. ID is the entry ID of
// the full entry. Text is the first 100 runes of the entry.
type SimilarEntry struct{ ID, Text string }

// SimilarEntries returns at most max entries that share words with newEntry,
// most shared words first. An entry that equals newEntry is not in the list.
// Entries with the same count keep their order.
func SimilarEntries(entries []string, newEntry string, max int) []SimilarEntry {
	if max <= 0 {
		return nil
	}
	nw := textmatch.Words(newEntry)
	type hit struct {
		e      string
		shared int
	}
	var hits []hit
	for _, e := range entries {
		if e == newEntry {
			continue
		}
		if shared, ok := textmatch.Similar(nw, textmatch.Words(e)); ok {
			hits = append(hits, hit{e, shared})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].shared > hits[j].shared })
	if len(hits) > max {
		hits = hits[:max]
	}
	var out []SimilarEntry
	for _, h := range hits {
		text := h.e
		if r := []rune(text); len(r) > similarShow {
			text = string(r[:similarShow]) + "…"
		}
		out = append(out, SimilarEntry{ID: EntryID(h.e), Text: text})
	}
	return out
}

// similarLines returns the lines for the add result, with a newline first, or
// "" when no entry is similar. It lists at most 2 entries.
func similarLines(entries []string, newEntry string) string {
	list := SimilarEntries(entries, newEntry, 2)
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n" + similarHead)
	for _, e := range list {
		fmt.Fprintf(&b, "\n- %s \"%s\"", e.ID, e.Text)
	}
	return b.String()
}
