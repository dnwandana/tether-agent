package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// writeSearchFile writes a file with the name name in the notes folder of s.
func writeSearchFile(t *testing.T, s *Store, name, text string) {
	t.Helper()
	if err := os.MkdirAll(s.notesPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.notesPath(), name), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustSearch(t *testing.T, s *Store, query string) string {
	t.Helper()
	out, err := s.Search(query)
	if err != nil {
		t.Fatalf("Search(%q): %v", query, err)
	}
	return out
}

func TestQueryWords(t *testing.T) {
	if got := queryWords("Quota, quota; QUOTA rpc-node"); !slices.Equal(got, []string{"quota", "rpc", "node"}) {
		t.Fatalf("%q", got)
	}
	var many []string
	for i := range 20 {
		many = append(many, fmt.Sprintf("w%d", i))
	}
	if got := queryWords(strings.Join(many, " ")); len(got) != 16 {
		t.Fatalf("%d words", len(got))
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	_, err := open(t).Search("  ,, !! ")
	if err == nil || err.Error() != "query is required for search; give one or more words" {
		t.Fatalf("%v", err)
	}
}

func TestSearchTierOneWithNoNotesFolder(t *testing.T) {
	s := open(t)
	if err := os.WriteFile(filepath.Join(s.dir, "memory.md"), []byte("QuickNode key is in .env"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := "1 matches for \"quicknode\":\n1. memory.md\n   [QuickNode] key is in .env"
	if got := mustSearch(t, s, "quicknode"); got != want {
		t.Fatalf("%q", got)
	}
	if got := mustSearch(t, s, "zzz"); got != `No entry matches "zzz"; there are no notes yet.` {
		t.Fatalf("%q", got)
	}
}

func TestSearchNoteShape(t *testing.T) {
	s := open(t)
	writeSearchFile(t, s, "rpc.md", "---\ntype: memory\nmodified: 2026-11-04T09:12:44Z\n---\n"+
		"QuickNode free plan: 10M credits/month, 15 req/s\n§\nHelius RPC: 1M credits/day")
	want := "1 matches for \"helius\":\n" +
		"1. note rpc (type memory, modified 2026-11-04T09:12:44Z, 2 entries)\n" +
		"   [Helius] RPC: 1M credits/day"
	if got := mustSearch(t, s, "helius"); got != want {
		t.Fatalf("%q", got)
	}
}

func TestSearchNoHitListsNotes(t *testing.T) {
	s := open(t)
	writeSearchFile(t, s, "rpc.md", "QuickNode plan")
	writeSearchFile(t, s, "inbox-memory.md", "VPS is at Hetzner")
	if got := mustSearch(t, s, "zzz"); got != `No entry matches "zzz". The notes are: inbox-memory, rpc.` {
		t.Fatalf("%q", got)
	}
}

func TestSearchFrontmatterOnlyNoteIsNoHit(t *testing.T) {
	s := open(t)
	writeSearchFile(t, s, "rpc.md", "---\ntype: memory\nmodified: 2026-11-04T09:12:44Z\n---\n")
	if got := mustSearch(t, s, "rpc"); got != `No entry matches "rpc". The notes are: rpc.` {
		t.Fatalf("%q", got)
	}
}

func TestSearchOrder(t *testing.T) {
	s := open(t)
	// matched first: b has both words, a has one word four times.
	writeSearchFile(t, s, "a.md", "quicknode quicknode quicknode quicknode")
	writeSearchFile(t, s, "b.md", "quicknode quota")
	if out := mustSearch(t, s, "quicknode quota"); !strings.Contains(out, "1. note b (") || !strings.Contains(out, "2. note a (") {
		t.Fatal(out)
	}

	s = open(t)
	// hits next: both have both words, d has more occurrences.
	writeSearchFile(t, s, "c.md", "quicknode quota")
	writeSearchFile(t, s, "d.md", "quicknode quota quota")
	if out := mustSearch(t, s, "quicknode quota"); !strings.Contains(out, "1. note d (") {
		t.Fatal(out)
	}

	s = open(t)
	// The topic name adds 3 hits for each query word in it.
	writeSearchFile(t, s, "alpha.md", "the quota resets monthly")
	writeSearchFile(t, s, "quota.md", "the quota resets monthly")
	if out := mustSearch(t, s, "quota"); !strings.Contains(out, "1. note quota (") {
		t.Fatal(out)
	}

	s = open(t)
	// A word only in the topic name is a match, and the snippet is the first entry.
	writeSearchFile(t, s, "rpc.md", "QuickNode free plan: 10M credits/month")
	if out := mustSearch(t, s, "rpc"); !strings.Contains(out, "1. note rpc (") || !strings.Contains(out, "QuickNode free plan") {
		t.Fatal(out)
	}

	s = open(t)
	// modified next: the new note is first, also against the name order.
	writeSearchFile(t, s, "e.md", "---\nmodified: 2026-11-04T09:00:00Z\n---\nquota")
	writeSearchFile(t, s, "f.md", "---\nmodified: 2026-11-05T09:00:00Z\n---\nquota")
	if out := mustSearch(t, s, "quota"); !strings.Contains(out, "1. note f (") {
		t.Fatal(out)
	}
}

func TestSearchSeesDirectEdit(t *testing.T) {
	s := open(t)
	writeSearchFile(t, s, "rpc.md", "old text")
	if out := mustSearch(t, s, "fresh"); !strings.HasPrefix(out, "No entry matches") {
		t.Fatal(out)
	}
	writeSearchFile(t, s, "rpc.md", "fresh text")
	if out := mustSearch(t, s, "fresh"); !strings.Contains(out, "1. note rpc (") {
		t.Fatal(out)
	}
}

func TestSearchEightHits(t *testing.T) {
	s := open(t)
	for i := range 10 {
		writeSearchFile(t, s, fmt.Sprintf("n%d.md", i), "quota")
	}
	out := mustSearch(t, s, "quota")
	if !strings.HasPrefix(out, `8 matches for "quota":`) || !strings.Contains(out, "\n8. note ") || strings.Contains(out, "\n9. note ") {
		t.Fatal(out)
	}
}

func TestSearchSkippedFiles(t *testing.T) {
	s := open(t)
	writeSearchFile(t, s, "rpc.md", "quota")
	writeSearchFile(t, s, "big.md", strings.Repeat("quota ", (2<<20)/6+1))
	writeSearchFile(t, s, "bin.md", "quota\x00")
	writeSearchFile(t, s, "a.txt", "quota")
	out := mustSearch(t, s, "quota")
	if strings.Contains(out, "note big") || strings.Contains(out, "note bin") {
		t.Fatal(out)
	}
	if !strings.HasSuffix(out, "\n3 files in memory/notes were skipped: not a note name, larger than 1 MiB, or binary.") {
		t.Fatal(out)
	}
}

func TestSearchNoteGoneIsNotReported(t *testing.T) {
	s := open(t)
	srcs, skipped, stop := s.scanNotes([]string{"gone"})
	if len(srcs) != 0 || skipped != 0 || stop != "" {
		t.Fatal(srcs, skipped, stop)
	}
}

func TestSearchStopsAt2000Notes(t *testing.T) {
	s := open(t)
	for i := range 2001 {
		writeSearchFile(t, s, fmt.Sprintf("n%04d.md", i), "x")
	}
	if out := mustSearch(t, s, "zzz"); !strings.Contains(out, "\nThe search stopped at 2000 notes; some notes were not searched.") {
		t.Fatal(out)
	}
}

func TestSearchNotesFileActsAsNoNotes(t *testing.T) {
	s := open(t)
	if err := os.WriteFile(s.notesPath(), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := mustSearch(t, s, "zzz"); got != `No entry matches "zzz"; there are no notes yet.` {
		t.Fatalf("%q", got)
	}
}

func TestSnippetMarksEachMatch(t *testing.T) {
	s := open(t)
	writeSearchFile(t, s, "rpc.md", "quota only here\n§\n"+
		"QuickNode free plan: 10M credits/month; the quota resets on the 1st. QuickNode again.")
	out := mustSearch(t, s, "quicknode quota")
	want := "   [QuickNode] free plan: 10M credits/month; the [quota] resets on the 1st. [QuickNode] again."
	if !strings.Contains(out, want) {
		t.Fatal(out)
	}
}

func TestSnippetCutIs200RunesAtRuneBoundary(t *testing.T) {
	for _, entry := range []string{
		strings.Repeat("x", 300) + " quota " + strings.Repeat("y", 300),
		strings.Repeat("日本", 150) + " quota " + strings.Repeat("語", 300),
	} {
		got := snippet(entry, []string{"quota"})
		if !utf8.ValidString(got) {
			t.Fatalf("not valid UTF-8: %q", got)
		}
		if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") || !strings.Contains(got, "[quota]") {
			t.Fatalf("%q", got)
		}
		inner := strings.NewReplacer("…", "", "[", "", "]", "").Replace(got)
		if n := utf8.RuneCountInString(inner); n != 200 {
			t.Fatalf("%d runes: %q", n, got)
		}
	}
	if got := snippet("Short QUOTA entry", []string{"quota"}); got != "Short [QUOTA] entry" {
		t.Fatalf("%q", got)
	}
}

func TestSearchSkipsUnreadableNote(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open a file with mode 0")
	}
	s := open(t)
	writeSearchFile(t, s, "rpc.md", "quota")
	writeSearchFile(t, s, "closed.md", "quota")
	if err := os.Chmod(filepath.Join(s.notesPath(), "closed.md"), 0); err != nil {
		t.Fatal(err)
	}
	out := mustSearch(t, s, "quota")
	if !strings.Contains(out, "note rpc") || strings.Contains(out, "note closed") {
		t.Fatal(out)
	}
	if !strings.HasSuffix(out, "\n1 files in memory/notes were skipped: not a note name, larger than 1 MiB, or binary.") {
		t.Fatal(out)
	}
}
