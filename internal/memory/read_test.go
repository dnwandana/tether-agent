package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const readFront = "---\ntype: memory\nmodified: 2026-11-04T09:12:44Z\n---\n"

// writeReadFile writes a file with the name name in the notes folder of s.
func writeReadFile(t *testing.T, s *Store, name, text string) {
	t.Helper()
	if err := os.MkdirAll(s.notesPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.notesPath(), name), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, s *Store, topic string, from, maxChars int) string {
	t.Helper()
	out, err := s.Read(topic, from, maxChars)
	if err != nil {
		t.Fatalf("Read(%q): %v", topic, err)
	}
	return out
}

func mustReadErr(t *testing.T, s *Store, topic string, from, maxChars int) string {
	t.Helper()
	_, err := s.Read(topic, from, maxChars)
	if err == nil {
		t.Fatalf("Read(%q) must fail", topic)
	}
	return err.Error()
}

// fiveEntries writes the note rpc with five entries of 100 runes each.
func fiveEntries(t *testing.T, s *Store) {
	t.Helper()
	var es []string
	for i := 1; i <= 5; i++ {
		es = append(es, fmt.Sprintf("e%d", i)+strings.Repeat("a", 98))
	}
	writeReadFile(t, s, "rpc.md", readFront+strings.Join(es, Separator))
}

func TestReadHeaderAndEntries(t *testing.T) {
	s := open(t)
	writeReadFile(t, s, "rpc.md", readFront+
		"QuickNode free plan: 10M credits/month, 15 req/s\n§\nHelius RPC: 1M credits/day; mainnet URL in .env")
	want := "note rpc (type memory, modified 2026-11-04T09:12:44Z), 2 entries:\n" +
		"1. QuickNode free plan: 10M credits/month, 15 req/s\n" +
		"2. Helius RPC: 1M credits/day; mainnet URL in .env"
	if got := mustRead(t, s, "rpc", 1, 0); got != want {
		t.Fatalf("%q", got)
	}
	if got := mustRead(t, s, "RPC", 1, 0); got != want {
		t.Fatalf("the topic is not normalized: %q", got)
	}
	writeReadFile(t, s, "plain.md", "a\n§\nb")
	if got := mustRead(t, s, "plain", 1, 0); !strings.HasPrefix(got, "note plain (modified ") {
		t.Fatalf("%q", got)
	}
}

func TestReadMissingNote(t *testing.T) {
	s := open(t)
	if got := mustReadErr(t, s, "rpc", 1, 0); got != `no note "rpc"; there are no notes yet.` {
		t.Fatalf("%q", got)
	}
	writeReadFile(t, s, "b.md", "x")
	writeReadFile(t, s, "a.md", "y")
	if got := mustReadErr(t, s, "RPC", 1, 0); got != `no note "rpc". The notes are: a, b.` {
		t.Fatalf("%q", got)
	}
}

func TestReadTopicErrors(t *testing.T) {
	s := open(t)
	for _, topic := range []string{"!!!", "日本"} {
		want := fmt.Sprintf("the topic %q has no letters a-z or digits; use a topic from the topic list", topic)
		if got := mustReadErr(t, s, topic, 1, 0); got != want {
			t.Fatalf("%q", got)
		}
	}
	if got := mustReadErr(t, s, "  ", 1, 0); got != "topic is required for read" {
		t.Fatalf("%q", got)
	}
}

func TestReadFrom(t *testing.T) {
	s := open(t)
	fiveEntries(t, s)
	out := mustRead(t, s, "rpc", 4, 0)
	if !strings.Contains(out, ", 5 entries:\n4. e4") || !strings.Contains(out, "\n5. e5") || strings.Contains(out, "3. e3") {
		t.Fatal(out)
	}
	if out := mustRead(t, s, "rpc", 0, 0); !strings.Contains(out, "\n1. e1") {
		t.Fatal(out)
	}
	if got := mustReadErr(t, s, "rpc", 6, 0); got != "note rpc has 5 entries; from must be 1 to 5" {
		t.Fatalf("%q", got)
	}
}

func TestReadCutAtEntryEnd(t *testing.T) {
	s := open(t)
	fiveEntries(t, s)
	out := mustRead(t, s, "rpc", 1, 400)
	if !strings.Contains(out, "\n2. e2") || strings.Contains(out, "3. e3") {
		t.Fatal(out)
	}
	if !strings.HasSuffix(out, "\n… output cut at 400 characters after entry 2; the note has 5 entries. Read again with from 3.") {
		t.Fatal(out)
	}
	if n := utf8.RuneCountInString(out); n > 400 {
		t.Fatalf("%d runes", n)
	}
}

func TestReadFirstEntryLongerThanMax(t *testing.T) {
	s := open(t)
	writeReadFile(t, s, "rpc.md", readFront+strings.Repeat("日本語", 400)+Separator+"second")
	out := mustRead(t, s, "rpc", 1, 300)
	if !utf8.ValidString(out) {
		t.Fatalf("not valid UTF-8: %q", out)
	}
	if !strings.HasPrefix(out, "note rpc (type memory, modified 2026-11-04T09:12:44Z), 2 entries:\n1. 日本語") {
		t.Fatal(out)
	}
	if !strings.HasSuffix(out, "\n… output cut at 300 characters after entry 1; the note has 2 entries. Read again with from 2.") {
		t.Fatal(out)
	}
	if n := utf8.RuneCountInString(out); n > 300 {
		t.Fatalf("%d runes", n)
	}
}

func TestReadMaxCharsCap(t *testing.T) {
	s := open(t)
	var es []string
	for i := range 40 {
		es = append(es, fmt.Sprintf("%02d", i)+strings.Repeat("b", 998))
	}
	writeReadFile(t, s, "big-list.md", strings.Join(es, Separator))
	for _, max := range []int{0, 50000} {
		out := mustRead(t, s, "big-list", 1, max)
		if !strings.Contains(out, "… output cut at 32000 characters after entry ") || utf8.RuneCountInString(out) > MaxReadChars {
			t.Fatalf("maxChars %d: %d runes", max, utf8.RuneCountInString(out))
		}
	}
}

func TestReadLargeNote(t *testing.T) {
	s := open(t)
	writeReadFile(t, s, "big.md", strings.Repeat("a", 2<<20))
	if got := mustReadErr(t, s, "big", 1, 0); got != "note big is larger than 1 MiB, so read and search skip it. Open it with read_file. "+
		"To make it smaller, remove entries with action remove and topic big, and add them again with another topic." {
		t.Fatalf("%q", got)
	}
}

func TestReadFrontmatterOnlyNote(t *testing.T) {
	s := open(t)
	writeReadFile(t, s, "rpc.md", readFront)
	if got := mustRead(t, s, "rpc", 1, 0); got != "note rpc (type memory, modified 2026-11-04T09:12:44Z), 0 entries:" {
		t.Fatalf("%q", got)
	}
	if got, err := s.Topics(); err != nil || !slices.Equal(got, []string{"rpc"}) {
		t.Fatal(got, err)
	}
}

func TestTopicsOrder(t *testing.T) {
	s := open(t)
	if got, err := s.Topics(); err != nil || got != nil {
		t.Fatal(got, err)
	}
	base := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	for name, mt := range map[string]time.Time{"a.md": base, "b.md": base.Add(time.Hour), "c.md": base} {
		writeReadFile(t, s, name, "x")
		if err := os.Chtimes(filepath.Join(s.notesPath(), name), mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	writeReadFile(t, s, "RPC.md", "x")
	writeReadFile(t, s, "x.txt", "x")
	if err := os.Mkdir(filepath.Join(s.notesPath(), "sub.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(s.notesPath(), "a.md"), filepath.Join(s.notesPath(), "link.md")); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Topics(); err != nil || !slices.Equal(got, []string{"b", "a", "c"}) {
		t.Fatal(got, err)
	}
}

func TestTopicsOpensNoFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can open a file with mode 0")
	}
	s := open(t)
	writeReadFile(t, s, "closed.md", "x")
	if err := os.Chmod(filepath.Join(s.notesPath(), "closed.md"), 0); err != nil {
		t.Fatal(err)
	}
	writeReadFile(t, s, "bin.md", "x\x00")
	got, err := s.Topics()
	if err != nil || !slices.Contains(got, "closed") || !slices.Contains(got, "bin") {
		t.Fatal(got, err)
	}
}

func TestTopicsMax2000(t *testing.T) {
	s := open(t)
	for i := range 2001 {
		writeReadFile(t, s, fmt.Sprintf("n%04d.md", i), "x")
	}
	if got, err := s.Topics(); err != nil || len(got) != 2000 {
		t.Fatal(len(got), err)
	}
}

func TestNotesSymlinkGivesNoTopics(t *testing.T) {
	s := open(t)
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "a.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, s.notesPath()); err != nil {
		t.Fatal(err)
	}
	got, err := s.Topics()
	if got != nil || !errors.Is(err, errNotesNotFolder) {
		t.Fatal(got, err)
	}
	if got := mustReadErr(t, s, "a", 1, 0); got != `no note "a"; there are no notes yet.` {
		t.Fatalf("%q", got)
	}
}

func TestReadBinaryNote(t *testing.T) {
	s := open(t)
	writeReadFile(t, s, "rpc.md", "a\x00b")
	if got := mustReadErr(t, s, "rpc", 1, 0); got != "note rpc is a binary file; open it with read_file" {
		t.Fatalf("%q", got)
	}
}

func TestReadUnreadableNoteWrapsOnce(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open a file with mode 0")
	}
	s := open(t)
	writeReadFile(t, s, "closed.md", "x")
	if err := os.Chmod(filepath.Join(s.notesPath(), "closed.md"), 0); err != nil {
		t.Fatal(err)
	}
	got := mustReadErr(t, s, "closed", 1, 0)
	if !strings.HasPrefix(got, "cannot read note closed: ") || strings.Count(got, "cannot read note") != 1 {
		t.Fatalf("%q", got)
	}
}

// The first shown entry is shown in full when the entry and its own cut line
// fit, also when the longest cut line of the note does not fit.
func TestReadFirstEntryFitsWithItsCutLine(t *testing.T) {
	s := open(t)
	var es []string
	for i := 1; i <= 10; i++ {
		es = append(es, fmt.Sprintf("e%02d", i)+strings.Repeat("a", 97))
	}
	writeReadFile(t, s, "rpc.md", readFront+strings.Join(es, Separator))
	head := "note rpc (type memory, modified 2026-11-04T09:12:44Z), 10 entries:\n1. " + es[0]
	cut := "\n… output cut at 265 characters after entry 1; the note has 10 entries. Read again with from 2."
	want := head + cut
	if n := utf8.RuneCountInString(want); n != 265 {
		t.Fatalf("the test text has %d runes", n)
	}
	if got := mustRead(t, s, "rpc", 1, 265); got != want {
		t.Fatalf("%q", got)
	}
}

func TestReadOneEntryLimit200(t *testing.T) {
	s := open(t)
	e := strings.Repeat("a", 100)
	writeReadFile(t, s, "rpc.md", readFront+e)
	want := "note rpc (type memory, modified 2026-11-04T09:12:44Z), 1 entries:\n1. " + e
	if got := mustRead(t, s, "rpc", 1, 200); got != want {
		t.Fatalf("%q", got)
	}
}

func TestReadNeverOverLimit(t *testing.T) {
	s := open(t)
	fiveEntries(t, s)
	for _, limit := range []int{1, 50, 100, 150, 200} {
		out := mustRead(t, s, "rpc", 1, limit)
		if n := utf8.RuneCountInString(out); n > limit || !utf8.ValidString(out) {
			t.Fatalf("limit %d: %d runes: %q", limit, n, out)
		}
	}
	writeReadFile(t, s, "empty.md", readFront)
	if out := mustRead(t, s, "empty", 1, 20); utf8.RuneCountInString(out) > 20 {
		t.Fatalf("%q", out)
	}
}
