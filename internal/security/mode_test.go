package security

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
)

func TestModeConsts(t *testing.T) {
	if ModeReadOnly != "read-only" || ModeGuarded != "guarded" || ModeFull != "full" {
		t.Fatal(ModeReadOnly, ModeGuarded, ModeFull)
	}
}

func TestModeStore(t *testing.T) {
	g := newGate(t, "manual", "deny")
	const scope = "telegram:42:7"
	if m := g.ModeOf(ctx, scope); m != "" {
		t.Fatalf("no row: %q", m)
	}
	for _, mode := range []string{ModeReadOnly, ModeFull} {
		if err := g.SetMode(ctx, scope, mode); err != nil {
			t.Fatal(err)
		}
		if m := g.ModeOf(ctx, scope); m != mode {
			t.Fatalf("got %q, want %q", m, mode)
		}
	}
	var at int64
	if err := g.DB.QueryRowContext(ctx, "SELECT set_at FROM chat_modes WHERE scope=?", scope).Scan(&at); err != nil ||
		at < time.Now().Add(-time.Minute).UnixMilli() {
		t.Fatal(at, err)
	}
	// Another scope keeps no row.
	if m := g.ModeOf(ctx, "telegram:42:0"); m != "" {
		t.Fatal(m)
	}
	// Bad values change nothing.
	for _, bad := range [][2]string{{scope, "bogus"}, {scope, ""}, {"", ModeFull}} {
		if err := g.SetMode(ctx, bad[0], bad[1]); err == nil {
			t.Fatal("SetMode accepted", bad)
		}
	}
	if m := g.ModeOf(ctx, scope); m != ModeFull {
		t.Fatal(m)
	}
	// guarded deletes the row.
	if err := g.SetMode(ctx, scope, ModeGuarded); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := g.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM chat_modes").Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if m := g.ModeOf(ctx, scope); m != "" {
		t.Fatal(m)
	}
	// An empty scope and a gate with no state.db read nothing.
	if m := g.ModeOf(ctx, ""); m != "" {
		t.Fatal(m)
	}
	if m := (&Gate{}).ModeOf(ctx, "cli"); m != "" {
		t.Fatal(m)
	}
	if err := (&Gate{}).SetMode(ctx, "cli", ModeFull); err == nil {
		t.Fatal("a gate with no state.db saved a mode")
	}
}

func TestModeOfUnknownValueAndQueryError(t *testing.T) {
	g := newGate(t, "manual", "deny")
	if _, err := g.DB.ExecContext(ctx, "INSERT INTO chat_modes(scope, mode, set_at) VALUES ('cli', 'bogus', 1)"); err != nil {
		t.Fatal(err)
	}
	if m := g.ModeOf(ctx, "cli"); m != "" {
		t.Fatalf("an unknown mode counts as guarded: %q", m)
	}
	if _, err := g.DB.ExecContext(ctx, "DROP TABLE chat_modes"); err != nil {
		t.Fatal(err)
	}
	if m := g.ModeOf(ctx, "cli"); m != "" {
		t.Fatalf("a query error gives guarded: %q", m)
	}
}

// Two processes, for example tether chat and the gateway, each have their own Gate on one
// state.db. A SetMode of one is seen by the ModeOf of the other at the next decision.
func TestModeSeenByOtherGate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	open := func() *Gate {
		d, err := db.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		return &Gate{Mode: "manual", DB: d}
	}
	chat, gw := open(), open()
	if err := chat.SetMode(ctx, "cli", ModeFull); err != nil {
		t.Fatal(err)
	}
	if m := gw.ModeOf(ctx, "cli"); m != ModeFull {
		t.Fatalf("the other gate read %q", m)
	}
	if err := gw.SetMode(ctx, "cli", ModeGuarded); err != nil {
		t.Fatal(err)
	}
	if m := chat.ModeOf(ctx, "cli"); m != "" {
		t.Fatalf("the other gate read %q", m)
	}
}

// ScopeModes lists only the scopes with a read-only or full row, by scope, and closes its rows.
func TestScopeModes(t *testing.T) {
	g := &Gate{DB: openGateDB(t)}
	before := time.Now().Add(-time.Second)
	for scope, mode := range map[string]string{"telegram:5:0": ModeReadOnly, "dashboard": ModeFull, "cli": ModeGuarded} {
		if err := g.SetMode(ctx, scope, mode); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.DB.ExecContext(ctx, "INSERT INTO chat_modes(scope, mode, set_at) VALUES ('telegram:9:0', 'weird', 5)"); err != nil {
		t.Fatal(err)
	}
	got, err := g.ScopeModes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Scope != "dashboard" || got[0].Mode != ModeFull ||
		got[1].Scope != "telegram:5:0" || got[1].Mode != ModeReadOnly {
		t.Fatalf("%+v", got)
	}
	for _, r := range got {
		if r.SetAt.Before(before) || r.SetAt.After(time.Now().Add(time.Second)) {
			t.Fatalf("set_at of %s: %v", r.Scope, r.SetAt)
		}
	}
	// The rows are closed: a second query on the one connection does not wait.
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if m, err := g.ModeCheck(qctx, "dashboard"); err != nil || m != ModeFull {
		t.Fatal(m, err)
	}
	if rows, err := (&Gate{}).ScopeModes(ctx); err != nil || rows != nil {
		t.Fatal(rows, err)
	}
}
