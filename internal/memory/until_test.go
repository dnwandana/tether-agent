package memory

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var nov26 = time.Date(2026, 11, 26, 12, 0, 0, 0, time.Local)

func TestParseUntil(t *testing.T) {
	for _, c := range []struct {
		in, body, day string
		ok            bool
	}{
		{"VPS plan\nuntil 2026-12-31", "VPS plan", "2026-12-31", true},
		{"VPS plan\n  until 2026-12-31  ", "VPS plan", "2026-12-31", true},
		{"until 2026-12-31", "", "2026-12-31", true},
		{"VPS plan", "VPS plan", "", false},
		{"VPS plan\nuntil next week", "VPS plan\nuntil next week", "", false},
		{"until 2026-12-31\nVPS plan", "until 2026-12-31\nVPS plan", "", false},
	} {
		body, day, ok := ParseUntil(c.in)
		if body != c.body || day != c.day || ok != c.ok {
			t.Errorf("ParseUntil(%q) = %q %q %v", c.in, body, day, ok)
		}
	}
}

func TestValidUntil(t *testing.T) {
	for day, want := range map[string]string{
		"2026-11-27": "",
		"2027-11-27": "", // 366 days after today
		"2026-11-26": "the until date 2026-11-26 is not after today (2026-11-26); give a later date, or leave until out",
		"2027-11-28": "the until date 2027-11-28 is more than 366 days after today (2026-11-26)",
		"2026-02-30": `the until date "2026-02-30" is not a valid date; use the form YYYY-MM-DD`,
		"26-12-01":   `the until date "26-12-01" is not a valid date; use the form YYYY-MM-DD`,
	} {
		got := ""
		if err := ValidUntil(day, nov26); err != nil {
			got = err.Error()
		}
		if got != want {
			t.Errorf("%s: %q, want %q", day, got, want)
		}
	}
}

func TestExpiredAndLiveEntries(t *testing.T) {
	if !Expired("x\nuntil 2026-11-25", "2026-11-26") || Expired("x\nuntil 2026-11-26", "2026-11-26") || Expired("x", "2026-11-26") {
		t.Fatal("an entry loads through its date and stops on the next day")
	}
	s := open(t)
	if err := os.WriteFile(filepath.Join(s.dir, "memory.md"), []byte("a1\n§\nb1\nuntil 2026-11-25\n§\nc1\nuntil 2026-11-26"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.LiveEntries(TargetMemory, "2026-11-26")
	if err != nil || !slices.Equal(got, []string{"a1", "c1\nuntil 2026-11-26"}) {
		t.Fatal(got, err)
	}
	if ArchiveNote(TargetUser) != "archive-user" {
		t.Fatal(ArchiveNote(TargetUser))
	}
}

func TestApplyUntil(t *testing.T) {
	s, _ := openHist(t, 2200, 1375, 1000)
	s.now = func() time.Time { return nov26 }
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "Z.ai plan: Pro", Until: "2026-12-31"})
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "Tether 0.25\nuntil 2027-01-31"})
	if got := s.Entries(TargetMemory); !slices.Equal(got, []string{"Z.ai plan: Pro\nuntil 2026-12-31", "Tether 0.25\nuntil 2027-01-31"}) {
		t.Fatal(got)
	}
	for op, want := range map[*Op]string{
		{Action: "add", Target: TargetMemory, Content: "x1", Until: "2026-11-26"}:                   "is not after today",
		{Action: "add", Target: TargetMemory, Content: "x1\nuntil 2020-01-01"}:                      "is not after today",
		{Action: "add", Target: TargetMemory, Content: "x1\nUntil next week"}:                       "is not a valid date",
		{Action: "add", Target: TargetMemory, Content: "x1\nuntil 2026-12-01", Until: "2026-12-02"}: "give the until date once",
		{Action: "remove", Target: TargetMemory, OldText: "x1", Until: "2026-12-01"}:                "until works only for an add or a replace",
		{Action: "add", Target: TargetMemory, Topic: "rpc", Content: "x1", Until: "2026-12-01"}:     "until works only for an add or a replace",
	} {
		if got := applyErr(t, s, *op); !strings.Contains(got, want) {
			t.Errorf("%+v: %q, want %q", *op, got, want)
		}
	}
}

func TestApplyMovesExpired(t *testing.T) {
	s, h := openHist(t, 30, 1375, 1000)
	s.now = func() time.Time { return nov26 }
	old := "b1\nuntil 2026-11-20"
	if err := os.WriteFile(filepath.Join(s.dir, "memory.md"), []byte("a1\n§\n"+old+"\n§\nc1"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The file has 29 of 30 characters, so the add fits only after the move.
	res := apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "d1 is new"})
	if got := s.Entries(TargetMemory); !slices.Equal(got, []string{"a1", "c1", "d1 is new"}) {
		t.Fatal(got)
	}
	if n := noteOf(t, s, "archive-memory"); !slices.Equal(n.Entries, []string{old}) {
		t.Fatalf("%+v", n)
	}
	e := h.edits[0]
	if e.Action != "expire" || e.Source != "expiry" || e.Target != "memory" || e.Idx != 1 ||
		e.Dest != "note:archive-memory" || e.OldText != old || e.NewText != old {
		t.Fatalf("%+v", e)
	}
	if !slices.Equal(res.EditIDs, []int64{2}) || !strings.HasSuffix(res.Text, "\n1 expired entry moved to note archive-memory.") {
		t.Fatalf("%+v", res)
	}
}

// When the archive note has the text already, Undo of the expire row must not take
// the old note entry away.
func TestExpireUndoKeepsOldNoteEntry(t *testing.T) {
	s, h := openHist(t, 2200, 1375, 1000)
	s.now = func() time.Time { return nov26 }
	old := "b1\nuntil 2026-11-20"
	if err := os.WriteFile(filepath.Join(s.dir, "memory.md"), []byte("a1\n§\n"+old), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.writeNote(note{Topic: "archive-memory", Type: "memory", Entries: []string{old}}); err != nil {
		t.Fatal(err)
	}
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "d1 is new"})
	if len(h.edits) == 0 {
		t.Fatal("no row")
	}
	e := h.edits[0]
	if _, err := s.Undo(context.Background(), e.ID, e.Stamp); err != nil {
		t.Fatal(err)
	}
	if n := noteOf(t, s, "archive-memory"); !slices.Equal(n.Entries, []string{old}) {
		t.Fatalf("the note lost the entry: %+v", n)
	}
	if got := s.Entries(TargetMemory); !slices.Contains(got, old) {
		t.Fatal(got)
	}
}
