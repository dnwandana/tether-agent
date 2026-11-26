package memory

import (
	"context"
	"errors"
	"testing"
	"time"
)

func at(day int) func() time.Time {
	return func() time.Time { return time.Date(2026, 11, day, 10, 0, 0, 0, time.Local) }
}

const blockedText = "The owner deleted this fact on 2026-11-20. Do not save it again unless the owner says it again."

func TestBlockMatch(t *testing.T) {
	long := "owner prefers dark mode in every editor"
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"vps ovh", "vps ovh", true},
		{"", "", false},
		{"vps ovh", "vps ovh is old", false}, // the shorter text has under 20 runes
		{long, "note " + long + " always", true},
		{"note " + long + " always", long, true},
		{long, "x" + long, false}, // not on a word boundary
	} {
		if got := blockMatch(c.a, c.b); got != c.want {
			t.Errorf("blockMatch(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestOwnerRemoveAddsBlock(t *testing.T) {
	s, h := openHist(t, 2200, 1375, 1000)
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "VPS = ovh"})
	apply(t, s, Op{Action: "remove", Target: TargetMemory, OldText: "ovh", Source: SourceOwner})
	if len(h.blocks) != 1 || h.blocks[0].Norm != "vps ovh" || h.blocks[0].Text != "VPS = ovh" || h.blocks[0].EditID != 2 {
		t.Fatalf("%+v", h.blocks)
	}
}

func TestOtherRemovesAddNoBlock(t *testing.T) {
	s, h := openHist(t, 2200, 1375, 1000)
	for _, src := range []string{SourceReview, SourceTidy, SourceChat, SourceCron} {
		apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "VPS = ovh"})
		apply(t, s, Op{Action: "remove", Target: TargetMemory, OldText: "ovh", Source: src})
	}
	if len(h.blocks) != 0 {
		t.Fatalf("%+v", h.blocks)
	}
}

// The remove row of an owner spill replace has a group id, so it adds no block.
func TestOwnerSpillReplaceAddsNoBlock(t *testing.T) {
	s, h := openHist(t, 10, 10, 10)
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "abc"})
	apply(t, s, Op{Action: "replace", Target: TargetMemory, OldText: "abc", Content: "zzzzzzzzzzz", Source: SourceOwner})
	if len(h.blocks) != 0 {
		t.Fatalf("%+v", h.blocks)
	}
}

// An undo row that takes text out adds a block. A put-back row and a group row do not.
func TestUndoRowsAndBlocks(t *testing.T) {
	s, h := openHist(t, 2200, 1375, 1000)
	id := s.record(context.Background(), Edit{Action: "undo", Source: SourceOwner, Target: TargetMemory, OldText: "Owner likes tea"})
	s.record(context.Background(), Edit{Action: "undo", Source: SourceOwner, Target: TargetMemory, NewText: "Owner likes milk"})
	s.record(context.Background(), Edit{Action: "undo", Source: SourceOwner, Target: TargetMemory, OldText: "Owner likes coffee", GroupID: 1})
	if len(h.blocks) != 1 || h.blocks[0].EditID != id || h.blocks[0].Norm != "owner likes tea" {
		t.Fatalf("%+v", h.blocks)
	}
}

func TestBlockedWritesRefused(t *testing.T) {
	s, _ := openHist(t, 2200, 1375, 1000)
	s.now = at(20)
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "VPS = ovh"})
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "Owner uses fish"})
	apply(t, s, Op{Action: "remove", Target: TargetMemory, OldText: "ovh", Source: SourceOwner})
	s.now = at(26)
	for _, op := range []Op{
		{Action: "add", Target: TargetMemory, Content: "vps: OVH", Source: SourceReview},
		{Action: "add", Target: TargetUser, Content: "VPS = ovh", Source: SourceTidy},
		{Action: "add", Target: TargetMemory, Content: "VPS = ovh", Source: SourceCron},
		{Action: "add", Target: TargetMemory, Content: "VPS = ovh", Source: SourceChat},
		{Action: "replace", Target: TargetMemory, OldText: "fish", Content: "VPS = ovh", Source: SourceReview},
	} {
		_, err := s.ApplyContext(context.Background(), op)
		if !errors.Is(err, ErrBlocked) || err.Error() != blockedText {
			t.Fatalf("%+v: %v", op, err)
		}
	}
	if got := s.Entries(TargetMemory); len(got) != 1 || got[0] != "Owner uses fish" {
		t.Fatal(got)
	}
	// The owner can add the text again.
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "VPS = ovh", Source: SourceOwner})
}

func TestBlockContainment(t *testing.T) {
	s, _ := openHist(t, 2200, 1375, 1000)
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "Owner prefers dark mode in every editor"})
	apply(t, s, Op{Action: "remove", Target: TargetMemory, OldText: "dark mode", Source: SourceOwner})
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "VPS = ovh"})
	apply(t, s, Op{Action: "remove", Target: TargetMemory, OldText: "ovh", Source: SourceOwner})
	if _, err := s.ApplyContext(context.Background(), Op{Action: "add", Target: TargetUser,
		Content: "Note: owner prefers dark mode in every editor, always.", Source: SourceReview}); !errors.Is(err, ErrBlocked) {
		t.Fatal(err)
	}
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "VPS = ovh is old", Source: SourceReview})
}

// A chat write with a valid owner quote is not blocked, and it clears the block.
func TestQuotedChatClearsBlock(t *testing.T) {
	s, h := openHist(t, 2200, 1375, 1000)
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "VPS = ovh"})
	apply(t, s, Op{Action: "remove", Target: TargetMemory, OldText: "ovh", Source: SourceOwner})
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "VPS = ovh", Source: SourceChat, QuoteOK: true})
	if len(h.cleared) != 1 || h.cleared[0] != "vps ovh" || len(h.blocks) != 0 {
		t.Fatalf("%+v %+v", h.cleared, h.blocks)
	}
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "vps: OVH", Source: SourceReview})
}

// With no owner row in the recent history, the text names no date.
func TestBlockedTextWithNoDate(t *testing.T) {
	s, h := openHist(t, 2200, 1375, 1000)
	h.blocks = []Block{{Norm: "vps ovh", Text: "VPS = ovh"}}
	_, err := s.ApplyContext(context.Background(), Op{Action: "add", Target: TargetMemory, Content: "VPS = ovh", Source: SourceReview})
	want := "The owner deleted this fact on an earlier day. Do not save it again unless the owner says it again."
	if !errors.Is(err, ErrBlocked) || err.Error() != want {
		t.Fatal(err)
	}
}

// A review add with the until field of a blocked text is refused. The until line
// is not part of the fact.
func TestBlockedWriteWithUntilRefused(t *testing.T) {
	s, _ := openHist(t, 2200, 1375, 1000)
	s.now = at(20)
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "VPS = ovh"})
	apply(t, s, Op{Action: "remove", Target: TargetMemory, OldText: "ovh", Source: SourceOwner})
	s.now = at(26)
	for _, op := range []Op{
		{Action: "add", Target: TargetMemory, Content: "VPS = ovh", Until: "2026-12-30", Source: SourceReview},
		{Action: "add", Target: TargetMemory, Content: "VPS = ovh\nuntil 2026-12-30", Source: SourceReview},
	} {
		if _, err := s.ApplyContext(context.Background(), op); !errors.Is(err, ErrBlocked) {
			t.Fatalf("%+v: %v", op, err)
		}
	}
}

// A quoted chat write with the until field clears the block of its body.
func TestQuotedChatWithUntilClearsBlock(t *testing.T) {
	s, h := openHist(t, 2200, 1375, 1000)
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "VPS = ovh"})
	apply(t, s, Op{Action: "remove", Target: TargetMemory, OldText: "ovh", Source: SourceOwner})
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "VPS = ovh", Until: "2026-12-30", Source: SourceChat, QuoteOK: true})
	if len(h.cleared) != 1 || h.cleared[0] != "vps ovh" || len(h.blocks) != 0 {
		t.Fatalf("%+v %+v", h.cleared, h.blocks)
	}
}

// The block of a deleted entry with an until line holds the body only.
func TestRemoveOfUntilEntryBlocksBody(t *testing.T) {
	s, h := openHist(t, 2200, 1375, 1000)
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "VPS = ovh", Until: "2026-12-30"})
	apply(t, s, Op{Action: "remove", Target: TargetMemory, OldText: "ovh", Source: SourceOwner})
	if len(h.blocks) != 1 || h.blocks[0].Norm != "vps ovh" {
		t.Fatalf("%+v", h.blocks)
	}
	if _, err := s.ApplyContext(context.Background(), Op{Action: "add", Target: TargetMemory, Content: "VPS = ovh", Source: SourceReview}); !errors.Is(err, ErrBlocked) {
		t.Fatal(err)
	}
}
