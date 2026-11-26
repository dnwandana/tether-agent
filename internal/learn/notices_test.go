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

// noticesDB opens a new state.db in a temporary folder. The test closes it at the end.
func noticesDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestNoticesCreateGetSetState(t *testing.T) {
	ctx := context.Background()
	n := NewNotices(noticesDB(t))
	t0 := time.Unix(1764100000, 0)
	n.now = func() time.Time { return t0 }
	items := []NoticeItem{{Kind: "memory", ID: 5}, {Kind: "skill", ID: 2}, {Kind: "archive", ID: 3}}
	c, err := n.Create(ctx, "telegram:-100:7", "💾 Memory updated (add)\nNow: `VPS = ovh`", items)
	if err != nil || c.ID <= 0 || c.State != "open" || !c.At.Equal(t0) {
		t.Fatalf("%+v %v", c, err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{4}$`).MatchString(c.Stamp) {
		t.Fatalf("stamp %q", c.Stamp)
	}
	got, err := n.Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	got.At = c.At
	if !reflect.DeepEqual(got, c) || !reflect.DeepEqual(got.Items, items) {
		t.Fatalf("got %+v, want %+v", got, c)
	}
	if err := n.SetState(ctx, c.ID, "undone"); err != nil {
		t.Fatal(err)
	}
	if got, _ := n.Get(ctx, c.ID); got.State != "undone" {
		t.Fatal(got.State)
	}
	if err := n.SetState(ctx, c.ID, "gone"); err == nil {
		t.Fatal("an unknown state must be refused")
	}
	if err := n.SetState(ctx, 999, "kept"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if _, err := n.Get(ctx, 999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
}

func TestNoticesExpireOpen(t *testing.T) {
	ctx := context.Background()
	n := NewNotices(noticesDB(t))
	day0 := time.Unix(1764100000, 0)
	n.now = func() time.Time { return day0 }
	old, _ := n.Create(ctx, "cli:t", "old", []NoticeItem{{Kind: "memory", ID: 1}})
	undone, _ := n.Create(ctx, "cli:t", "undone", []NoticeItem{{Kind: "memory", ID: 2}})
	if err := n.SetState(ctx, undone.ID, "undone"); err != nil {
		t.Fatal(err)
	}
	n.now = func() time.Time { return day0.AddDate(0, 0, 40) }
	fresh, _ := n.Create(ctx, "cli:t", "fresh", nil)
	count, err := n.ExpireOpen(ctx, day0.AddDate(0, 0, 31))
	if err != nil || count != 1 {
		t.Fatal(count, err)
	}
	for id, want := range map[int64]string{old.ID: "kept", undone.ID: "undone", fresh.ID: "open"} {
		if got, _ := n.Get(ctx, id); got.State != want {
			t.Errorf("notice %d: state %q, want %q", id, got.State, want)
		}
	}
	if got, _ := n.Get(ctx, fresh.ID); len(got.Items) != 0 {
		t.Fatal(got.Items)
	}
}

func TestHeldAddGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	h := NewHeld(noticesDB(t))
	t0 := time.Unix(1764100000, 0)
	h.now = func() time.Time { return t0 }
	op := memory.Op{Action: "replace", Target: "memory", OldText: "Z.ai base URL",
		Content: "Z.ai coding plan base URL: https://api.z.ai/api/coding/paas/v4",
		Source:  "chat", SessionID: "s1", Quote: "use the coding plan url", QuoteOK: true,
		Until: "2026-12-31", Outside: []string{"web_fetch"},
		Mask: func(string) (string, bool) { return "", false }}
	got, err := h.Add(ctx, HeldWrite{Op: op, MatchedEntry: "Z.ai base URL: https://api.z.ai/api/paas/v4",
		Source: "chat", SessionID: "s1", SessionKey: "telegram:-100:7", Sources: []string{"web_fetch", "mcp_docs"}, Hosts: []string{"docs.z.ai", "api.z.ai"}})
	if err != nil || got.ID <= 0 || got.State != "held" || !got.At.Equal(t0) || !got.ExpiresAt.Equal(t0.Add(24*time.Hour)) {
		t.Fatalf("%+v %v", got, err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{4}$`).MatchString(got.Stamp) {
		t.Fatalf("stamp %q", got.Stamp)
	}
	back, err := h.Get(ctx, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := got
	want.Op.Mask = nil
	back.At, back.ExpiresAt = want.At, want.ExpiresAt
	if !reflect.DeepEqual(back, want) {
		t.Fatalf("got %+v, want %+v", back, want)
	}
	if _, err := h.Get(ctx, 999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
}

func TestHeldListSetStateDue(t *testing.T) {
	ctx := context.Background()
	h := NewHeld(noticesDB(t))
	t0 := time.Unix(1764100000, 0)
	h.now = func() time.Time { return t0 }
	add := func(content string) HeldWrite {
		w, err := h.Add(ctx, HeldWrite{Op: memory.Op{Action: "add", Target: "memory", Content: content, Source: "review"},
			Source: "review", SessionID: "r1", SessionKey: "cli:t", Sources: []string{"web_search"}})
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	a, b := add("fact one"), add("fact two")
	h.now = func() time.Time { return t0.Add(2 * time.Hour) }
	c := add("fact three")
	if err := h.SetState(ctx, b.ID, "saved"); err != nil {
		t.Fatal(err)
	}
	if err := h.SetState(ctx, a.ID, "lost"); err == nil {
		t.Fatal("an unknown state must be refused")
	}
	held, err := h.List(ctx, "held")
	if err != nil || len(held) != 2 || held[0].ID != a.ID || held[1].ID != c.ID {
		t.Fatal(held, err)
	}
	if all, _ := h.List(ctx, ""); len(all) != 3 {
		t.Fatal("an empty state must list all rows", len(all))
	}
	due, err := h.Due(ctx, t0.Add(25*time.Hour))
	if err != nil || len(due) != 1 || due[0].ID != a.ID {
		t.Fatal(due, err)
	}
	if due, _ := h.Due(ctx, t0.Add(26*time.Hour)); len(due) != 2 {
		t.Fatal(len(due))
	}
}
