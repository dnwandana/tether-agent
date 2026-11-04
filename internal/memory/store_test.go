package memory

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir(), 2200, 1375)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func apply(t *testing.T, s *Store, op Op) Result {
	t.Helper()
	res, err := s.Apply(op)
	if err != nil {
		t.Fatalf("%+v: %v", op, err)
	}
	return res
}

// noteOf returns the note topic of s, and stops the test when the read fails.
func noteOf(t *testing.T, s *Store, topic string) note {
	t.Helper()
	n, err := s.readNote(topic)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func applyErr(t *testing.T, s *Store, op Op) string {
	t.Helper()
	_, err := s.Apply(op)
	if err == nil {
		t.Fatalf("%+v must fail", op)
	}
	return err.Error()
}

func TestAddUsesSectionSeparator(t *testing.T) {
	s := open(t)
	apply(t, s, Op{Action: "add", Target: "memory", Content: "VPS = hetzner"})
	apply(t, s, Op{Action: "add", Target: "memory", Content: "deploy via rsync"})
	if s.Memory() != "VPS = hetzner\n§\ndeploy via rsync" {
		t.Fatalf("%q", s.Memory())
	}
	b, err := os.ReadFile(filepath.Join(s.dir, "memory.md"))
	if err != nil || string(b) != "VPS = hetzner\n§\ndeploy via rsync" {
		t.Fatalf("%q %v", b, err)
	}
	if s.Profile() != "" {
		t.Fatalf("%q", s.Profile())
	}
}

func TestOpenCreatesFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "memory")
	s, err := Open(dir, 2200, 1375)
	if err != nil {
		t.Fatal(err)
	}
	apply(t, s, Op{Action: "add", Target: "user", Content: "The user uses Go"})
	if _, err := os.Stat(filepath.Join(dir, "user_profile.md")); err != nil {
		t.Fatal(err)
	}
}

// Spec tests 1 and 24b.
func TestAddOverLimitSpillsToInbox(t *testing.T) {
	s := open(t)
	apply(t, s, Op{Action: "add", Target: "memory", Content: strings.Repeat("a", 2100)})
	res := apply(t, s, Op{Action: "add", Target: "memory", Content: strings.Repeat("b", 200)})
	want := "memory is full (2100/2200 characters), so the entry went to note inbox-memory (1 entries).\n" + searchHint
	if res.Text != want || res.Tier1Changed || res.Note != "inbox-memory" {
		t.Fatalf("%+v", res)
	}
	if len(s.Entries("memory")) != 1 {
		t.Fatal("a spill must not change memory.md")
	}
	if n := noteOf(t, s, "inbox-memory"); n.Type != "memory" || len(n.Entries) != 1 || n.Entries[0] != strings.Repeat("b", 200) {
		t.Fatalf("%+v", n)
	}
}

func TestLimitCountsSeparators(t *testing.T) {
	s, err := Open(t.TempDir(), 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	apply(t, s, Op{Action: "add", Target: "memory", Content: "aaaa"})
	// "aaaa\n§\nbbb" is 10 runes: it fits. One more rune spills.
	if res := apply(t, s, Op{Action: "add", Target: "memory", Content: "bbbb"}); res.Note != "inbox-memory" || s.Memory() != "aaaa" {
		t.Fatalf("%+v %q", res, s.Memory())
	}
	if res := apply(t, s, Op{Action: "add", Target: "memory", Content: "bbb"}); !res.Tier1Changed || s.Memory() != "aaaa\n§\nbbb" {
		t.Fatalf("%+v %q", res, s.Memory())
	}
}

func TestLimitCountsRunes(t *testing.T) {
	s, err := Open(t.TempDir(), 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	apply(t, s, Op{Action: "add", Target: "memory", Content: "äöüß"})
}

// Spec test 7.
func TestUserLimit1375(t *testing.T) {
	s := open(t)
	res := apply(t, s, Op{Action: "add", Target: "user", Content: strings.Repeat("u", 1400)})
	if res.Text != "user is full (0/1375 characters), so the entry went to note inbox-user (1 entries).\n"+searchHint {
		t.Fatal(res.Text)
	}
	b, err := os.ReadFile(filepath.Join(s.dir, "notes", "inbox-user.md"))
	if err != nil || !strings.HasPrefix(string(b), "---\ntype: user\nmodified: ") || s.Profile() != "" {
		t.Fatalf("%q %v", b, err)
	}
	if res := apply(t, s, Op{Action: "add", Target: "memory", Content: strings.Repeat("m", 1400)}); !res.Tier1Changed {
		t.Fatalf("%+v", res)
	}
}

func TestReplaceRemoveByOldText(t *testing.T) {
	s := open(t)
	apply(t, s, Op{Action: "add", Target: "memory", Content: "VPS = hetzner"})
	apply(t, s, Op{Action: "add", Target: "memory", Content: "deploy via rsync"})
	apply(t, s, Op{Action: "add", Target: "memory", Content: "deploy key in vault"})

	apply(t, s, Op{Action: "replace", Target: "memory", OldText: "hetzner", Content: "VPS = ovh"})
	if got := s.Entries("memory"); got[0] != "VPS = ovh" {
		t.Fatalf("%q", got)
	}
	if err := applyErr(t, s, Op{Action: "replace", Target: "memory", OldText: "nothing", Content: "x"}); !strings.Contains(err, "no entry matches") {
		t.Fatal(err)
	}
	if err := applyErr(t, s, Op{Action: "remove", Target: "memory", OldText: "deploy"}); !strings.Contains(err, "matches 2 entries") {
		t.Fatal(err)
	}
	apply(t, s, Op{Action: "remove", Target: "memory", OldText: "rsync"})
	if got := s.Entries("memory"); len(got) != 2 || got[1] != "deploy key in vault" {
		t.Fatalf("%q", got)
	}
	if err := applyErr(t, s, Op{Action: "replace", Target: "memory", OldText: "ovh", Content: "  "}); !strings.Contains(err, "remove") {
		t.Fatal(err)
	}
}

// Spec test 13.
func TestReplaceOverLimitSpills(t *testing.T) {
	s, err := Open(t.TempDir(), 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	apply(t, s, Op{Action: "add", Target: "memory", Content: "abc"})
	res := apply(t, s, Op{Action: "replace", Target: "memory", OldText: "abc", Content: strings.Repeat("z", 11)})
	want := "The new text does not fit in memory (3/10 characters), so it went to note inbox-memory (1 entries), " +
		"and the old entry left memory. memory: 0 entries, 0% (0/10 characters).\n" + searchHint
	if res.Text != want || !res.Tier1Changed || res.Note != "inbox-memory" {
		t.Fatalf("%+v", res)
	}
	if s.Memory() != "" || noteOf(t, s, "inbox-memory").Entries[0] != strings.Repeat("z", 11) {
		t.Fatalf("%q", s.Memory())
	}
}

func TestDuplicateAddIsNoOp(t *testing.T) {
	s := open(t)
	apply(t, s, Op{Action: "add", Target: "memory", Content: "x = 1"})
	res := apply(t, s, Op{Action: "add", Target: "memory", Content: " x = 1 "})
	if !strings.Contains(res.Text, "already exists") || res.Tier1Changed || res.Note != "" || len(s.Entries("memory")) != 1 {
		t.Fatal(res, s.Entries("memory"))
	}
}

func TestBadOpRejected(t *testing.T) {
	s := open(t)
	for _, op := range []Op{
		{Action: "add", Target: "other", Content: "x"},
		{Action: "read", Target: "memory"},
		{Action: "add", Target: "memory", Content: "   "},
		{Action: "remove", Target: "memory"},
		{Action: "add", Target: "memory", Content: "a\n§\nb"},
		{Action: "add", Target: "memory", Content: "a\n§"},
	} {
		applyErr(t, s, op)
	}
}

func TestReadsFreshFromDisk(t *testing.T) {
	s := open(t)
	apply(t, s, Op{Action: "add", Target: "memory", Content: "one"})
	if err := os.WriteFile(filepath.Join(s.dir, "memory.md"), []byte("one\n§\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s.Memory() != "one\n§\ntwo" || len(s.Entries("memory")) != 2 {
		t.Fatalf("%q", s.Memory())
	}
}

func TestConcurrentApplySerialized(t *testing.T) {
	s, err := Open(t.TempDir(), 100000, 1375)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Apply(Op{Action: "add", Target: "memory", Content: fmt.Sprintf("entry %d", i)}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := s.Entries("memory"); len(got) != 50 {
		t.Fatalf("got %d entries", len(got))
	}
}

func TestTwoStoresOnOneFolder(t *testing.T) {
	// The gateway and a chat process each open a Store on the same folder.
	dir := t.TempDir()
	a, _ := Open(dir, 100000, 1375)
	b, _ := Open(dir, 100000, 1375)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(2)
		go func() { defer wg.Done(); a.Apply(Op{Action: "add", Target: "memory", Content: fmt.Sprintf("a %d", i)}) }()
		go func() { defer wg.Done(); b.Apply(Op{Action: "add", Target: "memory", Content: fmt.Sprintf("b %d", i)}) }()
	}
	wg.Wait()
	if got := a.Entries("memory"); len(got) != 40 {
		t.Fatalf("got %d entries", len(got))
	}
}

// Spec tests 2, 3, and 24b.
func TestAddWithTopic(t *testing.T) {
	s := open(t)
	res := apply(t, s, Op{Action: "add", Target: "memory", Topic: "RPC", Content: "QuickNode free plan: 10M credits/month"})
	if res.Text != "Entry added to note rpc (1 entries).\n"+searchHint || res.Note != "rpc" || res.Tier1Changed {
		t.Fatalf("%+v", res)
	}
	res = apply(t, s, Op{Action: "add", Target: "memory", Topic: "rpc", Content: "Helius RPC: 1M credits/day"})
	if res.Text != "Entry added to note rpc (2 entries).\n"+searchHint {
		t.Fatal(res.Text)
	}
	if got := noteOf(t, s, "rpc").Entries; len(got) != 2 || got[1] != "Helius RPC: 1M credits/day" {
		t.Fatalf("%q", got)
	}
	if s.Memory() != "" {
		t.Fatal("an add with a topic must not change memory.md")
	}
}

// Spec test 24b: a tier-1 add has no hint.
func TestTier1AddHasNoHint(t *testing.T) {
	s := open(t)
	for i, topic := range []string{"", "   "} {
		res := apply(t, s, Op{Action: "add", Target: "memory", Topic: topic, Content: fmt.Sprintf("x = %d", i)})
		if !strings.HasPrefix(res.Text, "Entry added. memory: ") || strings.Contains(res.Text, searchHint) ||
			!res.Tier1Changed || res.Note != "" {
			t.Fatalf("%+v", res)
		}
	}
}

// Spec test 4.
func TestTopicWithNoLetters(t *testing.T) {
	s := open(t)
	for _, tc := range []struct{ target, topic, inbox string }{
		{"memory", "!!!", "inbox-memory"},
		{"user", "日本", "inbox-user"},
	} {
		res := apply(t, s, Op{Action: "add", Target: tc.target, Topic: tc.topic, Content: "fact"})
		want := "Entry added to note " + tc.inbox + " (1 entries).\n" +
			"The topic \"" + tc.topic + "\" has no letters a-z or digits, so the entry went to note " + tc.inbox + ".\n" + searchHint
		if res.Text != want || res.Note != tc.inbox {
			t.Fatalf("%+v", res)
		}
	}
}

// Spec test 6.
func TestAddDedupe(t *testing.T) {
	s, err := Open(t.TempDir(), 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	apply(t, s, Op{Action: "add", Target: "memory", Content: "x = 1"})
	res := apply(t, s, Op{Action: "add", Target: "memory", Topic: "rpc", Content: "x = 1"})
	if !strings.Contains(res.Text, "The entry already exists; nothing changed.") || res.Note != "" || res.Tier1Changed {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "notes", "rpc.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a tier-1 duplicate must not create a note")
	}
	apply(t, s, Op{Action: "add", Target: "memory", Topic: "rpc", Content: "y = 2"})
	res = apply(t, s, Op{Action: "add", Target: "memory", Topic: "rpc", Content: " y = 2 "})
	if res.Text != "The entry already exists; nothing changed." || res.Note != "" || len(noteOf(t, s, "rpc").Entries) != 1 {
		t.Fatalf("%+v", res)
	}
	apply(t, s, Op{Action: "add", Target: "memory", Content: "spilled entry"})
	res = apply(t, s, Op{Action: "add", Target: "memory", Content: "spilled entry"})
	if res.Text != "The entry already exists; nothing changed." || len(noteOf(t, s, "inbox-memory").Entries) != 1 {
		t.Fatalf("%+v", res)
	}
}

// Spec test 24a.
func TestEntryLimit(t *testing.T) {
	s := open(t)
	long := strings.Repeat("é", 4001)
	const want = "the entry has 4001 characters, and the limit for one entry is 4000. Make it shorter, or save it as several entries."
	for _, op := range []Op{
		{Action: "add", Target: "memory", Content: long},
		{Action: "replace", Target: "memory", OldText: "x", Content: long},
	} {
		if err := applyErr(t, s, op); err != want {
			t.Fatal(err)
		}
	}
	apply(t, s, Op{Action: "add", Target: "memory", Content: strings.Repeat("é", 4000)})
}

// Spec test 21, over two stores on one folder.
func TestConcurrentNoteAdds(t *testing.T) {
	dir := t.TempDir()
	a, _ := Open(dir, 2200, 1375)
	b, _ := Open(dir, 2200, 1375)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(2)
		for j, s := range []*Store{a, b} {
			go func() {
				defer wg.Done()
				if _, err := s.Apply(Op{Action: "add", Target: "memory", Topic: "rpc", Content: fmt.Sprintf("store %d entry %d", j, i)}); err != nil {
					t.Error(err)
				}
			}()
		}
	}
	wg.Wait()
	if got := noteOf(t, a, "rpc").Entries; len(got) != 40 {
		t.Fatalf("got %d entries", len(got))
	}
}

func TestAddFailsWhenNotesIsAFile(t *testing.T) {
	s := open(t)
	if err := os.WriteFile(filepath.Join(s.dir, "notes"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyErr(t, s, Op{Action: "add", Target: "memory", Topic: "rpc", Content: "a"}); err != "memory/notes is not a folder; move it away, and then try again" {
		t.Fatal(err)
	}
}

// Spec test 14.
func TestReplaceShrinksOverCapFile(t *testing.T) {
	s, err := Open(t.TempDir(), 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, "memory.md"), []byte("aaaaaaaa\n§\nbbbbbbbb"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := apply(t, s, Op{Action: "replace", Target: "memory", OldText: "aaaa", Content: "a"})
	if res.Text != "Entry replaced. memory: 2 entries, 100% (12/10 characters)." || !res.Tier1Changed || res.Note != "" {
		t.Fatalf("%+v", res)
	}
	// 13 runes: over the cap and longer than the 12 runes of today, so it spills.
	if res := apply(t, s, Op{Action: "replace", Target: "memory", OldText: "bbbb", Content: "bbbbbbbbb"}); res.Note != "inbox-memory" || s.Memory() != "a" {
		t.Fatalf("%+v %q", res, s.Memory())
	}
}

// Spec tests 8 and 12.
func TestReplaceRemoveInNoteFallback(t *testing.T) {
	s := open(t)
	apply(t, s, Op{Action: "add", Target: "memory", Topic: "rpc", Content: "QuickNode free plan"})
	apply(t, s, Op{Action: "add", Target: "memory", Topic: "rpc", Content: "Helius RPC: 1M credits/day"})
	at := time.Date(2026, 11, 4, 9, 12, 44, 0, time.UTC)
	s.now = func() time.Time { return at }
	res := apply(t, s, Op{Action: "replace", Target: "memory", OldText: "QuickNode", Content: "QuickNode paid plan"})
	if res.Text != "Entry replaced in note rpc (2 entries)." || res.Note != "rpc" || res.Tier1Changed {
		t.Fatalf("%+v", res)
	}
	if n := noteOf(t, s, "rpc"); n.Entries[0] != "QuickNode paid plan" || !n.Modified.Equal(at) {
		t.Fatalf("%+v", n)
	}
	// A match in the notes ignores the type of the note.
	if res := apply(t, s, Op{Action: "remove", Target: "user", OldText: "Helius"}); res.Text != "Entry removed from note rpc (1 entries)." {
		t.Fatal(res.Text)
	}
	res = apply(t, s, Op{Action: "remove", Target: "memory", OldText: "paid"})
	if res.Text != "Entry removed from note rpc. The note had no more entries, so the note was deleted." || res.Note != "rpc" {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "notes", "rpc.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("the remove of the last entry must delete the note")
	}
	if err := applyErr(t, s, Op{Action: "remove", Target: "memory", OldText: "nothing"}); err != `no entry matches "nothing" in memory or in the notes` {
		t.Fatal(err)
	}
}

// Spec test 9, and the topic that normalizes to an empty text.
func TestReplaceRemoveWithTopic(t *testing.T) {
	s := open(t)
	apply(t, s, Op{Action: "add", Target: "memory", Content: "deploy via rsync"})
	apply(t, s, Op{Action: "add", Target: "memory", Topic: "ops", Content: "deploy via scp"})
	apply(t, s, Op{Action: "add", Target: "memory", Topic: "rpc", Content: "deploy the rpc node"})
	res := apply(t, s, Op{Action: "replace", Target: "memory", Topic: "Ops", OldText: "deploy", Content: "deploy via sftp"})
	if res.Text != "Entry replaced in note ops (1 entries)." || s.Memory() != "deploy via rsync" ||
		noteOf(t, s, "rpc").Entries[0] != "deploy the rpc node" {
		t.Fatalf("%+v", res)
	}
	if err := applyErr(t, s, Op{Action: "remove", Target: "memory", Topic: "rpc", OldText: "rsync"}); err != `no entry matches "rsync" in note rpc` {
		t.Fatal(err)
	}
	if err := applyErr(t, s, Op{Action: "remove", Target: "memory", Topic: "nope", OldText: "deploy"}); err != `no note "nope". The notes are: ops, rpc.` {
		t.Fatal(err)
	}
	for _, action := range []string{"replace", "remove"} {
		err := applyErr(t, s, Op{Action: action, Target: "memory", Topic: "!!!", OldText: "deploy", Content: "x"})
		if err != `the topic "!!!" has no letters a-z or digits; use a topic from the topic list` {
			t.Fatal(err)
		}
	}
	if err := applyErr(t, open(t), Op{Action: "remove", Target: "memory", Topic: "nope", OldText: "x"}); err != `no note "nope"; there are no notes yet.` {
		t.Fatal(err)
	}
}

// Spec test 10, and two notes with the same text.
func TestNoteMatchesInTwoNotes(t *testing.T) {
	s := open(t)
	apply(t, s, Op{Action: "add", Target: "memory", Topic: "a", Content: "deploy via scp"})
	apply(t, s, Op{Action: "add", Target: "memory", Topic: "b", Content: "deploy via ftp"})
	err := applyErr(t, s, Op{Action: "remove", Target: "memory", OldText: "deploy"})
	if !strings.Contains(err, "a: deploy via scp") || !strings.Contains(err, "b: deploy via ftp") {
		t.Fatal(err)
	}
	apply(t, s, Op{Action: "add", Target: "memory", Topic: "a", Content: "x = 1"})
	apply(t, s, Op{Action: "add", Target: "memory", Topic: "b", Content: "x = 1"})
	if res := apply(t, s, Op{Action: "remove", Target: "memory", OldText: "x = 1"}); res.Text != "Entry removed from note a (1 entries)." {
		t.Fatal(res.Text)
	}
	if got := noteOf(t, s, "b").Entries; len(got) != 2 {
		t.Fatalf("%q", got)
	}
}

// Spec test 11.
func TestTier1AmbiguousSkipsNotes(t *testing.T) {
	s := open(t)
	apply(t, s, Op{Action: "add", Target: "memory", Content: "deploy via rsync"})
	apply(t, s, Op{Action: "add", Target: "memory", Content: "deploy key in vault"})
	apply(t, s, Op{Action: "add", Target: "memory", Topic: "ops", Content: "deploy x"})
	if err := applyErr(t, s, Op{Action: "remove", Target: "memory", OldText: "deploy"}); !strings.Contains(err, `"deploy" matches 2 entries`) {
		t.Fatal(err)
	}
	if len(noteOf(t, s, "ops").Entries) != 1 {
		t.Fatal("an ambiguous tier-1 match must not change a note")
	}
}

// Spec test 23.
func TestNoteMatchListCapped(t *testing.T) {
	s := open(t)
	for i := 1; i <= 12; i++ {
		apply(t, s, Op{Action: "add", Target: "memory", Topic: fmt.Sprintf("n%02d", i), Content: fmt.Sprintf("deploy key %d", i)})
	}
	err := applyErr(t, s, Op{Action: "remove", Target: "memory", OldText: "deploy"})
	if !strings.Contains(err, "n10: deploy key 10") || strings.Contains(err, "n11:") || !strings.Contains(err, "and 2 more") {
		t.Fatal(err)
	}
}

// Spec test 22.
func TestReplaceSpillPartialFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the folder mode")
	}
	dir := t.TempDir()
	s, err := Open(dir, 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	apply(t, s, Op{Action: "add", Target: "memory", Content: "abc"})
	if err := os.Mkdir(filepath.Join(dir, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A read-only memory folder stops the temp file of memory.md, but not of a note.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	op := Op{Action: "replace", Target: "memory", OldText: "abc", Content: strings.Repeat("z", 11)}
	for range 2 {
		err := applyErr(t, s, op)
		if !strings.HasPrefix(err, "the new text is in note inbox-memory, but memory.md could not be written: ") ||
			!strings.HasSuffix(err, ". The old entry is still in memory. Remove it with action remove.") {
			t.Fatal(err)
		}
	}
	if got := noteOf(t, s, "inbox-memory").Entries; len(got) != 1 || s.Memory() != "abc" {
		t.Fatalf("%q %q", got, s.Memory())
	}
}

func TestAddToBinaryNoteGivesNextAction(t *testing.T) {
	s := open(t)
	noteFile(t, s, "rpc.md", "a\x00b")
	_, err := s.Apply(Op{Action: "add", Target: "memory", Topic: "rpc", Content: "x"})
	if err == nil || err.Error() != "note rpc is a binary file; open it with read_file" {
		t.Fatalf("err = %v", err)
	}
}

func TestNoteFallbackSkipsUnreadableNote(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open a file with mode 0")
	}
	s := open(t)
	noteFile(t, s, "a.md", "deploy via scp")
	if err := os.Chmod(filepath.Join(s.notesPath(), "a.md"), 0); err != nil {
		t.Fatal(err)
	}
	apply(t, s, Op{Action: "add", Target: "memory", Topic: "b", Content: "deploy via ftp"})
	apply(t, s, Op{Action: "add", Target: "memory", Topic: "b", Content: "key in vault"})
	if res := apply(t, s, Op{Action: "remove", Target: "memory", OldText: "ftp"}); res.Text != "Entry removed from note b (1 entries)." {
		t.Fatal(res.Text)
	}
	want := `no entry matches "nothing" in memory or in the notes. 1 notes were skipped: larger than 8 MiB, binary, or not readable.`
	if err := applyErr(t, s, Op{Action: "remove", Target: "memory", OldText: "nothing"}); err != want {
		t.Fatal(err)
	}
}

func TestNoteFallbackReportsSkippedNotes(t *testing.T) {
	s := open(t)
	noteFile(t, s, "big.md", strings.Repeat("a", 8<<20+1))
	noteFile(t, s, "bin.md", "deploy\x00")
	noteFile(t, s, "ok.md", "x")
	want := `no entry matches "deploy" in memory or in the notes. 2 notes were skipped: larger than 8 MiB, binary, or not readable.`
	if err := applyErr(t, s, Op{Action: "remove", Target: "memory", OldText: "deploy"}); err != want {
		t.Fatal(err)
	}
}

// noteFiles returns the size of each file in notes/ of s by its name.
func noteFiles(t *testing.T, s *Store) map[string]int64 {
	t.Helper()
	des, err := os.ReadDir(s.notesPath())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, de := range des {
		fi, err := de.Info()
		if err != nil {
			t.Fatal(err)
		}
		out[de.Name()] = fi.Size()
	}
	return out
}

// fullNote writes the note topic with one entry, so that the note file has
// room for free more bytes only.
func fullNote(t *testing.T, s *Store, topic string, free int) {
	t.Helper()
	n := note{Topic: topic, Type: "memory", Entries: []string{"x"}}
	pad := maxNoteBytes - free - len(s.noteText(n))
	n.Entries = []string{"x" + strings.Repeat("a", pad)}
	if err := s.writeNote(n); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(s.notesPath(), topic+".md")); err != nil || fi.Size() != int64(maxNoteBytes-free) {
		t.Fatalf("size %v, err %v", fi.Size(), err)
	}
}

// An add never makes a note larger than 1 MiB. Entries with non-ASCII text
// reach the limit after less than 100 adds.
func TestAddNeverGrowsNotePastLimit(t *testing.T) {
	s, err := Open(t.TempDir(), 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	content := func(i int) string { return fmt.Sprintf("%d ", i) + strings.Repeat("日", 3990) }
	var last Result
	for i := range 90 {
		last = apply(t, s, Op{Action: "add", Target: "memory", Content: content(i)})
	}
	for name, size := range noteFiles(t, s) {
		if size > maxNoteBytes {
			t.Fatalf("%s has %d bytes", name, size)
		}
	}
	if last.Note != "inbox-memory-2" || !strings.Contains(last.Text, "so the entry went to note inbox-memory-2 (") ||
		!strings.Contains(last.Text, "Note inbox-memory is full (1 MiB), so the entry went to note inbox-memory-2.") {
		t.Fatalf("%+v", last)
	}
	first, second := noteOf(t, s, "inbox-memory"), noteOf(t, s, "inbox-memory-2")
	if len(first.Entries)+len(second.Entries) != 90 || !slices.Contains(second.Front, "continues: inbox-memory") {
		t.Fatalf("%d + %d entries, front %q", len(first.Entries), len(second.Entries), second.Front)
	}
	// An equal entry in any note of the chain is a duplicate.
	for _, i := range []int{0, 89} {
		if res := apply(t, s, Op{Action: "add", Target: "memory", Content: content(i)}); res.Text != alreadyExists {
			t.Fatalf("entry %d: %q", i, res.Text)
		}
	}
	if _, err := s.Read("inbox-memory", 1, 0); err != nil {
		t.Fatal(err)
	}
}

// An add with a topic goes to the first note of the chain that has room.
func TestAddWithTopicGoesToNextNote(t *testing.T) {
	s := open(t)
	fullNote(t, s, "rpc", 8)
	fact := "Helius RPC: 1M credits/day; mainnet URL in .env"
	res := apply(t, s, Op{Action: "add", Target: "memory", Topic: "rpc", Content: fact})
	want := "Entry added to note rpc-2 (1 entries).\n" +
		"Note rpc is full (1 MiB), so the entry went to note rpc-2.\n" + searchHint
	if res.Text != want || res.Note != "rpc-2" {
		t.Fatalf("%+v", res)
	}
	if res := apply(t, s, Op{Action: "add", Target: "memory", Topic: "rpc", Content: fact}); res.Text != alreadyExists {
		t.Fatal(res.Text)
	}
	// A short entry still fits in rpc.
	if res := apply(t, s, Op{Action: "add", Target: "memory", Topic: "rpc", Content: "b"}); res.Note != "rpc" {
		t.Fatalf("%+v", res)
	}
	if got := noteOf(t, s, "rpc-2"); got.Type != "memory" || !slices.Equal(got.Entries, []string{fact}) {
		t.Fatalf("%+v", got)
	}
}

// A note of the model with a continuation name is not a part of the chain.
func TestAddSkipsNoteOfOtherChain(t *testing.T) {
	s := open(t)
	fullNote(t, s, "rpc", 8)
	noteFile(t, s, "rpc-2.md", "---\ntype: memory\n---\nmy own note\n")
	res := apply(t, s, Op{Action: "add", Target: "memory", Topic: "rpc", Content: "my own note"})
	if res.Note != "rpc-3" {
		t.Fatalf("%+v", res)
	}
	if got := noteOf(t, s, "rpc-2").Entries; !slices.Equal(got, []string{"my own note"}) {
		t.Fatalf("%q", got)
	}
}

// Two long topics with the same start get different continuation notes.
func TestContinuationNameOfLongTopic(t *testing.T) {
	a, b := strings.Repeat("a", 46)+"-b", strings.Repeat("a", 46)+"-c"
	for _, topic := range []string{a, b, strings.Repeat("a", 48), "rpc"} {
		for _, k := range []int{2, 10, 1000} {
			name := chainName(topic, k)
			if !IsNoteName(name+".md") || !strings.HasSuffix(name, fmt.Sprintf("-%d", k)) {
				t.Fatalf("chainName(%q, %d) = %q", topic, k, name)
			}
		}
	}
	if chainName("rpc", 1) != "rpc" || chainName("rpc", 2) != "rpc-2" {
		t.Fatal(chainName("rpc", 2))
	}
	s := open(t)
	fullNote(t, s, a, 8)
	fullNote(t, s, b, 8)
	ra := apply(t, s, Op{Action: "add", Target: "memory", Topic: a, Content: "fact of topic a"})
	rb := apply(t, s, Op{Action: "add", Target: "memory", Topic: b, Content: "fact of topic b"})
	if ra.Note == rb.Note || ra.Note != chainName(a, 2) || rb.Note != chainName(b, 3) {
		t.Fatalf("%q %q", ra.Note, rb.Note)
	}
}

// oversizeNote writes inbox-memory.md as an older version wrote it: 280
// entries and more than 1 MiB.
func oversizeNote(t *testing.T, s *Store) {
	t.Helper()
	entries := make([]string, 280)
	for i := range entries {
		entries[i] = fmt.Sprintf("%04d ", i) + strings.Repeat("x", 3990)
	}
	noteFile(t, s, "inbox-memory.md", "---\ntype: memory\n---\n"+strings.Join(entries, Separator)+"\n")
	if fi, err := os.Stat(filepath.Join(s.notesPath(), "inbox-memory.md")); err != nil || fi.Size() <= maxNoteBytes {
		t.Fatalf("size %v, err %v", fi.Size(), err)
	}
}

// A note that is larger than 1 MiB gets new entries in the next note, and
// remove and replace make it smaller.
func TestRepairOversizeNote(t *testing.T) {
	s := open(t)
	oversizeNote(t, s)
	res := apply(t, s, Op{Action: "add", Target: "memory", Topic: "inbox-memory", Content: "new fact"})
	if res.Note != "inbox-memory-2" {
		t.Fatalf("%+v", res)
	}
	if res := apply(t, s, Op{Action: "add", Target: "memory", Topic: "inbox-memory", Content: "0007 " + strings.Repeat("x", 3990)}); res.Text != alreadyExists {
		t.Fatal(res.Text)
	}
	steps := []struct {
		op   Op
		want string
	}{
		{Op{Action: "remove", Target: "memory", Topic: "inbox-memory", OldText: "0007 "}, "Entry removed from note inbox-memory (279 entries)."},
		{Op{Action: "remove", Target: "memory", OldText: "0008 "}, "Entry removed from note inbox-memory (278 entries)."},
		{Op{Action: "replace", Target: "memory", Topic: "inbox-memory", OldText: "0009 ", Content: "0009 " + strings.Repeat("y", 3990)}, "Entry replaced in note inbox-memory (278 entries)."},
		{Op{Action: "replace", Target: "memory", OldText: "0010 ", Content: "short"}, "Entry replaced in note inbox-memory (278 entries)."},
	}
	for _, st := range steps {
		if res := apply(t, s, st.op); res.Text != st.want || res.Note != "inbox-memory" {
			t.Fatalf("%+v: %+v", st.op, res)
		}
	}
	// A replace that makes the note larger is refused.
	grow := Op{Action: "replace", Target: "memory", Topic: "inbox-memory", OldText: "0011 ", Content: "0011 " + strings.Repeat("z", 3995)}
	want := "the new text makes note inbox-memory larger than 1 MiB, so nothing changed. " +
		"Remove the old entry with action remove, and then add the new text with action add and topic inbox-memory. " +
		"The add puts the entry in the next note of the topic when the note is full."
	if err := applyErr(t, s, grow); err != want {
		t.Fatal(err)
	}
}

// A replace does not make a note larger than 1 MiB.
func TestReplaceRefusedPastNoteLimit(t *testing.T) {
	s := open(t)
	fullNote(t, s, "rpc", 8)
	apply(t, s, Op{Action: "add", Target: "memory", Topic: "rpc", Content: "b"})
	err := applyErr(t, s, Op{Action: "replace", Target: "memory", Topic: "rpc", OldText: "b", Content: "a longer text"})
	if !strings.HasPrefix(err, "the new text makes note rpc larger than 1 MiB, so nothing changed.") {
		t.Fatal(err)
	}
	if res := apply(t, s, Op{Action: "replace", Target: "memory", OldText: "b", Content: "c"}); res.Note != "rpc" {
		t.Fatalf("%+v", res)
	}
	for name, size := range noteFiles(t, s) {
		if size > maxNoteBytes {
			t.Fatalf("%s has %d bytes", name, size)
		}
	}
}

// A topic longer than 48 characters is refused, so that two long topics
// never share one note.
func TestLongTopicRefused(t *testing.T) {
	s := open(t)
	long := strings.Repeat("a", 48) + " b"
	want := `the topic "` + long + `" is too long: its note name has 50 characters, and the limit is 48. ` +
		"Use a shorter topic, for example a topic from the topic list."
	for _, op := range []Op{
		{Action: "add", Target: "memory", Topic: long, Content: "fact"},
		{Action: "replace", Target: "memory", Topic: long, OldText: "fact", Content: "new"},
		{Action: "remove", Target: "memory", Topic: long, OldText: "fact"},
	} {
		if err := applyErr(t, s, op); err != want {
			t.Fatalf("%s: %s", op.Action, err)
		}
	}
	if _, err := s.Read(long, 1, 0); err == nil || err.Error() != want {
		t.Fatalf("read: %v", err)
	}
	if _, err := os.Stat(s.notesPath()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("no note must be written")
	}
	// A topic of 48 characters still works.
	if res := apply(t, s, Op{Action: "add", Target: "memory", Topic: strings.Repeat("a", 48), Content: "fact"}); res.Note != strings.Repeat("a", 48) {
		t.Fatalf("%+v", res)
	}
}
