package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir(), 2200, 1375)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func apply(t *testing.T, s *Store, op Op) string {
	t.Helper()
	sum, err := s.Apply(op)
	if err != nil {
		t.Fatalf("%+v: %v", op, err)
	}
	return sum
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

func TestAddOverLimitListsEntries(t *testing.T) {
	s := open(t)
	apply(t, s, Op{Action: "add", Target: "memory", Content: strings.Repeat("a", 2100)})
	err := applyErr(t, s, Op{Action: "add", Target: "memory", Content: strings.Repeat("b", 200)})
	if !strings.Contains(err, "2200") || !strings.Contains(err, "1. aaaa") {
		t.Fatal(err)
	}
	if len(s.Entries("memory")) != 1 {
		t.Fatal("a rejected add must not change the file")
	}
}

func TestLimitCountsSeparators(t *testing.T) {
	s, err := Open(t.TempDir(), 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	apply(t, s, Op{Action: "add", Target: "memory", Content: "aaaa"})
	// "aaaa\n§\nbbb" is 10 runes: it fits. One more rune does not.
	applyErr(t, s, Op{Action: "add", Target: "memory", Content: "bbbb"})
	apply(t, s, Op{Action: "add", Target: "memory", Content: "bbb"})
}

func TestLimitCountsRunes(t *testing.T) {
	s, err := Open(t.TempDir(), 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	apply(t, s, Op{Action: "add", Target: "memory", Content: "äöüß"})
}

func TestUserLimit1375(t *testing.T) {
	s := open(t)
	err := applyErr(t, s, Op{Action: "add", Target: "user", Content: strings.Repeat("u", 1400)})
	if !strings.Contains(err, "1375") {
		t.Fatal(err)
	}
	apply(t, s, Op{Action: "add", Target: "memory", Content: strings.Repeat("m", 1400)})
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

func TestReplaceOverLimitRejected(t *testing.T) {
	s, err := Open(t.TempDir(), 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	apply(t, s, Op{Action: "add", Target: "memory", Content: "abc"})
	applyErr(t, s, Op{Action: "replace", Target: "memory", OldText: "abc", Content: strings.Repeat("z", 11)})
	if s.Memory() != "abc" {
		t.Fatalf("%q", s.Memory())
	}
}

func TestDuplicateAddIsNoOp(t *testing.T) {
	s := open(t)
	apply(t, s, Op{Action: "add", Target: "memory", Content: "x = 1"})
	sum := apply(t, s, Op{Action: "add", Target: "memory", Content: " x = 1 "})
	if !strings.Contains(sum, "already exists") || len(s.Entries("memory")) != 1 {
		t.Fatal(sum, s.Entries("memory"))
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
