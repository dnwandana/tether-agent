package learn

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/memory"
)

// historyDB opens a new state.db in a temporary folder. The test closes it at the end.
func historyDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestMemoryHistoryRecordAndGet(t *testing.T) {
	ctx := context.Background()
	h := NewMemoryHistory(historyDB(t))
	at := time.Unix(1764100000, 0)
	in := memory.Edit{At: at, Target: "user", Idx: 2, Action: "replace",
		OldText: "VPS = hetzner", NewText: "VPS = ovh", Source: "review", SessionID: "s1",
		Quote: "my vps is ovh now", Outside: []string{"mcp_docs", "web_fetch"}}
	id, err := h.Record(ctx, in)
	if err != nil || id <= 0 {
		t.Fatal(id, err)
	}
	got, err := h.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{4}$`).MatchString(got.Stamp) {
		t.Fatalf("stamp %q", got.Stamp)
	}
	if !got.At.Equal(at) {
		t.Fatal(got.At)
	}
	in.ID, in.Stamp, got.At = id, got.Stamp, at
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("got %+v, want %+v", got, in)
	}
}

func TestMemoryHistoryStampGroupAndOrder(t *testing.T) {
	ctx := context.Background()
	h := NewMemoryHistory(historyDB(t))
	first, err := h.Record(ctx, memory.Edit{Stamp: "a3f1", Target: "memory", Idx: 0, Action: "remove",
		OldText: "Old plan", Source: "tidy"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.Record(ctx, memory.Edit{Target: "note:inbox-memory", Idx: -1, Action: "add",
		NewText: "Old plan", Source: "tidy", GroupID: first})
	if err != nil || second <= first {
		t.Fatal(first, second, err)
	}
	before := time.Now().Add(-time.Second)
	third, err := h.Record(ctx, memory.Edit{Target: "memory", Idx: 0, Dest: "note:archive-memory",
		Action: "expire", OldText: "Freeze\nuntil 2026-11-01", Source: "expiry"})
	if err != nil || third <= second {
		t.Fatal(second, third, err)
	}
	e1, _ := h.Get(ctx, first)
	e2, _ := h.Get(ctx, second)
	e3, _ := h.Get(ctx, third)
	if e1.Stamp != "a3f1" || e1.GroupID != 0 || e2.GroupID != first || e3.Dest != "note:archive-memory" {
		t.Fatalf("%+v %+v %+v", e1, e2, e3)
	}
	if e3.At.Before(before) || e3.At.After(time.Now().Add(time.Second)) {
		t.Fatalf("a zero At must be the time of the Record call: %v", e3.At)
	}
	recent, err := h.Recent(ctx, 2)
	if err != nil || len(recent) != 2 || recent[0].ID != third || recent[1].ID != second {
		t.Fatal(recent, err)
	}
	if all, _ := h.Recent(ctx, 0); len(all) != 3 {
		t.Fatal("a limit of 0 must use 10", len(all))
	}
}

func TestMemoryHistoryMarkUndone(t *testing.T) {
	ctx := context.Background()
	h := NewMemoryHistory(historyDB(t))
	id, err := h.Record(ctx, memory.Edit{Target: "memory", Action: "add", NewText: "VPS = ovh", Source: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.MarkUndone(ctx, id, 9); err != nil {
		t.Fatal(err)
	}
	if e, _ := h.Get(ctx, id); e.UndoneBy != 9 {
		t.Fatal(e.UndoneBy)
	}
	if err := h.MarkUndone(ctx, id, 0); err != nil {
		t.Fatal(err)
	}
	if e, _ := h.Get(ctx, id); e.UndoneBy != 0 {
		t.Fatal("undoID 0 must clear undone_by", e.UndoneBy)
	}
	if err := h.MarkUndone(ctx, 999, 1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if _, err := h.Get(ctx, 999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
}

func TestMemoryHistoryBlocks(t *testing.T) {
	ctx := context.Background()
	h := NewMemoryHistory(historyDB(t))
	if err := h.AddBlock(ctx, memory.Block{Norm: "the owner hates basic auth", Text: "The owner hates basic auth!", EditID: 7}); err != nil {
		t.Fatal(err)
	}
	if err := h.AddBlock(ctx, memory.Block{Norm: "vps is hetzner", Text: "VPS is Hetzner", EditID: 8}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		norm string
		want bool
	}{
		{"the owner hates basic auth", true},            // equal
		{"note the owner hates basic auth now", true},   // the block is inside, on word boundaries
		{"owner hates basic auth", true},                // 22 runes, inside the block
		{"hates basic auth", false},                     // 16 runes: too short to match inside
		{"the owner hates basic authentication", false}, // not on a word boundary
		{"vps is hetzner", true},                        // equal, also under 20 runes
		{"my vps is hetzner today", false},              // the block has under 20 runes
		{"", false},                                     // an empty text never matches
	} {
		got, err := h.Blocked(ctx, c.norm)
		if err != nil || got != c.want {
			t.Errorf("Blocked(%q) = %v, %v; want %v", c.norm, got, err, c.want)
		}
	}
	if err := h.ClearBlock(ctx, "note the owner hates basic auth now"); err != nil {
		t.Fatal(err)
	}
	if b, _ := h.Blocked(ctx, "the owner hates basic auth"); b {
		t.Fatal("ClearBlock must delete each block that matches")
	}
	if err := h.DeleteBlockOf(ctx, 8); err != nil {
		t.Fatal(err)
	}
	if b, _ := h.Blocked(ctx, "vps is hetzner"); b {
		t.Fatal("DeleteBlockOf must delete the block of the edit")
	}
}
