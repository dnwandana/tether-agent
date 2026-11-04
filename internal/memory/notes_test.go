package memory

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// noteFile writes text to notes/<name> of s and returns the path.
func noteFile(t *testing.T, s *Store, name, text string) string {
	t.Helper()
	if err := os.MkdirAll(s.notesPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(s.notesPath(), name)
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Spec test 5, and the 48-byte boundary.
func TestNormalizeTopic(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"rpc", "rpc"},
		{"RPC Providers", "rpc-providers"},
		{"  --Foo__Bar!!baz--  ", "foo-bar-baz"},
		{"Ünïcode Name", "n-code-name"},
		{"!!!", ""},
		{"日本", ""},
		{"", ""},
		{strings.Repeat("a", 48), strings.Repeat("a", 48)},
		{strings.Repeat("a", 49), strings.Repeat("a", 48)},
		{strings.Repeat("a", 47) + " b", strings.Repeat("a", 47)},
		{strings.Repeat("ab-", 16), strings.Repeat("ab-", 15) + "ab"},
	} {
		if got := normalizeTopic(tc.in); got != tc.want {
			t.Errorf("normalizeTopic(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Spec test 17: the name rule.
func TestIsNoteName(t *testing.T) {
	for _, name := range []string{"rpc.md", "inbox-user.md", "a.md", strings.Repeat("a", 48) + ".md"} {
		if !IsNoteName(name) {
			t.Errorf("%q must be a note name", name)
		}
	}
	for _, name := range []string{".x.md", ".md", "a.txt", "RPC.md", "foo bar.md", "a\nb.md", "rpc",
		"a-.md", "-a.md", "a/b.md", "rpc.md.md", strings.Repeat("a", 49) + ".md"} {
		if IsNoteName(name) {
			t.Errorf("%q must not be a note name", name)
		}
	}
}

// Spec test 17: the store sees only regular files with a note name.
func TestListNotesSkipsOtherFiles(t *testing.T) {
	s := open(t)
	noteFile(t, s, "rpc.md", "a")
	noteFile(t, s, "inbox-user.md", "b")
	for _, name := range []string{".x.md", ".md", "a.txt", "RPC.md", "foo bar.md", "a\nb.md"} {
		noteFile(t, s, name, "x")
	}
	if err := os.Mkdir(filepath.Join(s.notesPath(), "dir.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(s.notesPath(), "link.md")); err != nil {
		t.Fatal(err)
	}
	names, skipped, err := s.listNotes()
	if err != nil || !slices.Equal(names, []string{"inbox-user", "rpc"}) || skipped != 8 {
		t.Fatalf("names %q, skipped %d, err %v", names, skipped, err)
	}
	if _, err := s.readNote("link"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a symlink note must read as missing: %v", err)
	}
}

func TestListNotesNoFolder(t *testing.T) {
	s := open(t)
	names, skipped, err := s.listNotes()
	if names != nil || skipped != 0 || err != nil {
		t.Fatalf("names %q, skipped %d, err %v", names, skipped, err)
	}
	if _, err := os.Lstat(s.notesPath()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a list must not create the notes folder")
	}
}

func TestReadNoteFrontmatter(t *testing.T) {
	s := open(t)
	noteFile(t, s, "rpc.md", "---\ntype: memory\nsource: hand\nmodified: 2026-11-04T09:12:44Z\n---\n"+
		"QuickNode free plan: 10M credits/month, 15 req/s\n§\nHelius RPC: 1M credits/day; mainnet URL in .env\n")
	n, err := s.readNote("rpc")
	if err != nil {
		t.Fatal(err)
	}
	if n.Topic != "rpc" || n.Type != "memory" || !slices.Equal(n.Front, []string{"type: memory", "source: hand"}) ||
		!n.Modified.Equal(time.Date(2026, 11, 4, 9, 12, 44, 0, time.UTC)) ||
		!slices.Equal(n.Entries, []string{"QuickNode free plan: 10M credits/month, 15 req/s",
			"Helius RPC: 1M credits/day; mainnet URL in .env"}) {
		t.Fatalf("%+v", n)
	}
}

// Review Focus: a note with CRLF line endings parses the same as LF.
func TestReadNoteCRLF(t *testing.T) {
	s := open(t)
	noteFile(t, s, "rpc.md", "---\r\ntype: user\r\nmodified: 2026-11-04T09:12:44Z\r\n---\r\none\r\n§\r\ntwo\r\n")
	n, err := s.readNote("rpc")
	if err != nil || n.Type != "user" || !slices.Equal(n.Front, []string{"type: user"}) ||
		!n.Modified.Equal(time.Date(2026, 11, 4, 9, 12, 44, 0, time.UTC)) || !slices.Equal(n.Entries, []string{"one", "two"}) {
		t.Fatalf("%+v %v", n, err)
	}
}

// Spec test 15.
func TestReadNoteBrokenFrontmatter(t *testing.T) {
	s := open(t)
	mt := time.Date(2026, 11, 2, 18, 1, 10, 0, time.UTC)
	p := noteFile(t, s, "open.md", "---\ntype: memory\nfact one")
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	n, err := s.readNote("open")
	if err != nil || n.Type != "" || len(n.Front) != 0 || !n.Modified.Equal(mt) ||
		!slices.Equal(n.Entries, []string{"---\ntype: memory\nfact one"}) {
		t.Fatalf("%+v %v", n, err)
	}
	p = noteFile(t, s, "bad.md", "---\ntype: memory\nmodified: yesterday\n---\nfact")
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	n, err = s.readNote("bad")
	if err != nil || n.Type != "memory" || !slices.Equal(n.Front, []string{"type: memory"}) || !n.Modified.Equal(mt) ||
		!slices.Equal(n.Entries, []string{"fact"}) {
		t.Fatalf("%+v %v", n, err)
	}
}

func TestReadNoteLimits(t *testing.T) {
	s := open(t)
	if _, err := s.readNote("gone"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing note: %v", err)
	}
	noteFile(t, s, "max.md", strings.Repeat("a", 1<<20))
	if _, err := s.readNote("max"); err != nil {
		t.Fatalf("1 MiB must read: %v", err)
	}
	noteFile(t, s, "big.md", strings.Repeat("a", 1<<20+1))
	if _, err := s.readNote("big"); !errors.Is(err, errNoteTooLarge) {
		t.Fatalf("1 MiB + 1: %v", err)
	}
	if _, err := s.readNoteMax("big", maxChangeBytes); err != nil {
		t.Fatalf("a change reads 1 MiB + 1: %v", err)
	}
	noteFile(t, s, "huge.md", strings.Repeat("a", 8<<20+1))
	if _, err := s.readNoteMax("huge", maxChangeBytes); !errors.Is(err, errNoteTooLargeToChange) {
		t.Fatalf("8 MiB + 1: %v", err)
	}
	noteFile(t, s, "bin.md", "a\x00b")
	if _, err := s.readNote("bin"); !errors.Is(err, errNoteBinary) {
		t.Fatalf("NUL byte: %v", err)
	}
	noteFile(t, s, "late.md", strings.Repeat("a", 8<<10)+"\x00")
	if _, err := s.readNote("late"); err != nil {
		t.Fatalf("a NUL after 8 KiB is not checked: %v", err)
	}
}

func TestWriteNoteNew(t *testing.T) {
	s := open(t)
	s.now = func() time.Time { return time.Date(2026, 11, 4, 16, 12, 44, 500, time.FixedZone("WIB", 7*3600)) }
	if err := s.writeNote(note{Topic: "rpc", Type: "memory", Entries: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(s.notesPath(), "rpc.md")
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "---\ntype: memory\nmodified: 2026-11-04T09:12:44Z\n---\na\n§\nb\n" {
		t.Fatalf("%q %v", b, err)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode: %v %v", fi.Mode(), err)
	}
	if fi, err := os.Lstat(s.notesPath()); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("folder mode: %v %v", fi.Mode(), err)
	}
}

// Spec test 16.
func TestWriteNoteKeepsFrontmatter(t *testing.T) {
	s := open(t)
	s.now = func() time.Time { return time.Date(2026, 11, 4, 9, 12, 44, 0, time.UTC) }
	p := noteFile(t, s, "rpc.md", "---\ntype: memory\nsource: hand\nmodified: 2020-01-01T00:00:00Z\n---\na\n")
	n, err := s.readNote("rpc")
	if err != nil {
		t.Fatal(err)
	}
	n.Entries = append(n.Entries, "b")
	if err := s.writeNote(n); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "---\ntype: memory\nsource: hand\nmodified: 2026-11-04T09:12:44Z\n---\na\n§\nb\n" {
		t.Fatalf("%q %v", b, err)
	}
}

func TestWriteNoteDeletesEmpty(t *testing.T) {
	s := open(t)
	p := noteFile(t, s, "rpc.md", "a")
	if err := s.writeNote(note{Topic: "rpc"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a note with no entries must be deleted")
	}
	if err := s.writeNote(note{Topic: "rpc"}); err != nil {
		t.Fatalf("a delete of a missing note: %v", err)
	}
}

func TestWriteNoteRefusesBadTopic(t *testing.T) {
	s := open(t)
	if err := s.writeNote(note{Topic: "../x", Entries: []string{"a"}}); err == nil {
		t.Fatal("a topic that is not a note name must fail")
	}
	if _, err := os.Lstat(filepath.Join(s.dir, "x.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("no file outside notes/")
	}
}

// Spec test 24, and a symlink at notes.
func TestNotesNotAFolder(t *testing.T) {
	s := open(t)
	const want = "memory/notes is not a folder; move it away, and then try again"
	check := func() {
		t.Helper()
		err := s.writeNote(note{Topic: "rpc", Type: "memory", Entries: []string{"a"}})
		if err == nil || err.Error() != want || !errors.Is(err, errNotesNotFolder) {
			t.Fatalf("write: %v", err)
		}
		if _, _, err := s.listNotes(); !errors.Is(err, errNotesNotFolder) {
			t.Fatalf("list: %v", err)
		}
		if _, err := s.readNote("rpc"); !errors.Is(err, errNotesNotFolder) {
			t.Fatalf("read: %v", err)
		}
	}
	if err := os.WriteFile(s.notesPath(), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	check()
	if err := os.Remove(s.notesPath()); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "rpc.md"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, s.notesPath()); err != nil {
		t.Fatal(err)
	}
	check()
}

func TestJoinCapped(t *testing.T) {
	for _, tc := range []struct {
		items []string
		max   int
		want  string
	}{
		{nil, 40, ""},
		{[]string{"a"}, 40, "a"},
		{[]string{"a", "b", "c"}, 3, "a, b, c"},
		{[]string{"a", "b", "c"}, 2, "a, b, and 1 more"},
	} {
		if got := joinCapped(tc.items, tc.max); got != tc.want {
			t.Errorf("joinCapped(%q, %d) = %q, want %q", tc.items, tc.max, got, tc.want)
		}
	}
}

func TestOpenSetsLockNameAndClock(t *testing.T) {
	s := open(t)
	if s.now == nil {
		t.Fatal("Open must set now")
	}
	apply(t, s, Op{Action: "add", Target: "memory", Content: "x"})
	if _, err := os.Stat(filepath.Join(s.dir, LockFileName)); err != nil {
		t.Fatal(err)
	}
}

func TestListNotesIgnoresTempFiles(t *testing.T) {
	s := open(t)
	noteFile(t, s, "rpc.md", "a")
	noteFile(t, s, ".mem-123456.tmp", "b")
	names, skipped, err := s.listNotes()
	if err != nil || !slices.Equal(names, []string{"rpc"}) || skipped != 0 {
		t.Fatalf("names %q, skipped %d, err %v", names, skipped, err)
	}
}
