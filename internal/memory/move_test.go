package memory

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"
)

func TestMoveToNote(t *testing.T) {
	s, h := openHist(t, 2200, 1375, 1000)
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "a1"})
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "Tether 0.25 release notes"})
	res := apply(t, s, Op{Action: ActionMove, Target: TargetMemory, Topic: "releases", OldText: "Tether 0.25", To: "note", Source: SourceChat})
	if got := s.Entries(TargetMemory); !slices.Equal(got, []string{"a1"}) {
		t.Fatal(got)
	}
	if n := noteOf(t, s, "releases"); !slices.Equal(n.Entries, []string{"Tether 0.25 release notes"}) {
		t.Fatalf("%+v", n)
	}
	e := h.edits[2]
	if e.Action != "move" || e.Target != "memory" || e.Idx != 1 || e.Dest != "note:releases" ||
		e.OldText != "Tether 0.25 release notes" || e.NewText != e.OldText || !slices.Equal(res.EditIDs, []int64{3}) {
		t.Fatalf("%+v %+v", e, res)
	}
	if !strings.HasPrefix(res.Text, "Entry moved to note releases. memory: 1 entries") {
		t.Fatal(res.Text)
	}
}

func TestMoveToFile(t *testing.T) {
	s, h := openHist(t, 2200, 1375, 1000)
	apply(t, s, Op{Action: "add", Target: TargetRule, Topic: "", Content: "Rules start here"})
	apply(t, s, Op{Action: "add", Target: TargetUser, Topic: "inbox-user", Content: "Do not send a file unless I ask"})
	res := apply(t, s, Op{Action: ActionMove, Target: TargetRule, Topic: "inbox-user", OldText: "send a file", To: "file", Source: SourceTidy})
	if got := s.Entries(TargetRule); !slices.Equal(got, []string{"Rules start here", "Do not send a file unless I ask"}) {
		t.Fatal(got)
	}
	if _, err := s.readNote("inbox-user"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("the empty note must be deleted:", err)
	}
	e := h.edits[2]
	if e.Action != "move" || e.Target != "note:inbox-user" || e.Idx != 0 || e.Dest != "rule" {
		t.Fatalf("%+v", e)
	}
	if !strings.HasPrefix(res.Text, "Entry moved from note inbox-user. rule: 2 entries") {
		t.Fatal(res.Text)
	}
}

func TestMoveToFullFileRefused(t *testing.T) {
	s, _ := openHist(t, 10, 1375, 10)
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "abcdefgh"})
	apply(t, s, Op{Action: "add", Target: TargetMemory, Topic: "old", Content: "xyz"})
	want := "the entry does not fit in memory (8/10 characters), so nothing moved; remove or shorten an entry of memory first"
	if got := applyErr(t, s, Op{Action: ActionMove, Target: TargetMemory, Topic: "old", OldText: "xyz", To: "file"}); got != want {
		t.Fatal(got)
	}
	apply(t, s, Op{Action: "add", Target: TargetRule, Content: "abcdefgh"})
	if got := applyErr(t, s, Op{Action: ActionMove, Target: TargetRule, Topic: "old", OldText: "xyz", To: "file"}); !strings.HasPrefix(got, "The owner rules are full (8/10).") {
		t.Fatal(got)
	}
	if n := noteOf(t, s, "old"); !slices.Equal(n.Entries, []string{"xyz"}) {
		t.Fatalf("%+v", n)
	}
}

func TestMoveRefusals(t *testing.T) {
	s := open(t)
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "a1"})
	for op, want := range map[*Op]string{
		{Action: ActionMove, Target: TargetMemory, Topic: "x", To: "note"}:                                    "old_text is required for move",
		{Action: ActionMove, Target: TargetMemory, OldText: "a1", To: "note"}:                                 "topic is required for move",
		{Action: ActionMove, Target: TargetMemory, Topic: "x", OldText: "a1", To: "elsewhere"}:                `to must be "note" or "file" for move`,
		{Action: ActionMove, Target: TargetMemory, Topic: "x", OldText: "a1", To: "note", Source: SourceCron}: "the move action is not open to cron runs",
		{Action: ActionMove, Target: TargetMemory, Topic: "x", OldText: "zz", To: "note"}:                     `no entry matches "zz" in memory`,
		{Action: "list", Target: TargetMemory}:                                                                `unknown action "list"; use add, replace, remove, or move`,
	} {
		if got := applyErr(t, s, *op); got != want {
			t.Errorf("%+v: %q, want %q", *op, got, want)
		}
	}
}

// A tidy remove never drops text: it moves the entry to note archive-<target>.
func TestTidyRemoveIsMove(t *testing.T) {
	s, h := openHist(t, 2200, 1375, 1000)
	apply(t, s, Op{Action: "add", Target: TargetUser, Content: "Terse communicator"})
	res := apply(t, s, Op{Action: "remove", Target: TargetUser, OldText: "Terse", Source: SourceTidy})
	if len(s.Entries(TargetUser)) != 0 {
		t.Fatal(s.Entries(TargetUser))
	}
	if n := noteOf(t, s, "archive-user"); !slices.Equal(n.Entries, []string{"Terse communicator"}) {
		t.Fatalf("%+v", n)
	}
	if e := h.edits[1]; e.Action != "move" || e.Source != "tidy" || e.Dest != "note:archive-user" || !slices.Equal(res.EditIDs, []int64{2}) {
		t.Fatalf("%+v", e)
	}
	want := "memory tidy removes only entries of memory, user, and rule; it moves each one to note archive-<target>"
	if got := applyErr(t, s, Op{Action: "remove", Target: TargetUser, Topic: "archive-user", OldText: "Terse", Source: SourceTidy}); got != want {
		t.Fatal(got)
	}
}

// A move to the file of an entry that the file has already writes no file. Undo of
// its row must keep the file entry and put the entry back into the note.
func TestMoveToFileDuplicateUndo(t *testing.T) {
	s, h := openHist(t, 2200, 1375, 1000)
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "Owner likes tea"})
	apply(t, s, Op{Action: "add", Target: TargetMemory, Topic: "drinks", Content: "Owner likes tea"})
	if err := s.writeNote(note{Topic: "drinks", Type: "memory", Entries: []string{"Owner likes tea"}}); err != nil {
		t.Fatal(err)
	}
	res := apply(t, s, Op{Action: ActionMove, Target: TargetMemory, Topic: "drinks", OldText: "likes tea", To: "file", Source: SourceChat})
	if !strings.Contains(res.Text, "already") {
		t.Fatal(res.Text)
	}
	id := res.EditIDs[0]
	if _, err := s.Undo(context.Background(), id, h.edits[id-1].Stamp); err != nil {
		t.Fatal(err)
	}
	if got := s.Entries(TargetMemory); !slices.Equal(got, []string{"Owner likes tea"}) {
		t.Fatal("the file lost the entry:", got)
	}
	if n := noteOf(t, s, "drinks"); !slices.Equal(n.Entries, []string{"Owner likes tea"}) {
		t.Fatalf("%+v", n)
	}
}
