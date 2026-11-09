package acp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
)

// openTestDB opens a new state.db in a temporary folder. The test closes it at the end.
func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestStorePutGet(t *testing.T) {
	d := openTestDB(t)
	s := NewStore(d)
	ctx := context.Background()
	want := Record{SessionID: "s1", Agent: "claude-code", ACPSessionID: "acp-1", CWD: "/home/user/projects/app",
		CreatedAt: time.UnixMilli(1762560000000), UpdatedAt: time.UnixMilli(1762560005000)}
	if err := s.Put(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(ctx, "s1", "claude-code")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if got.SessionID != want.SessionID || got.Agent != want.Agent || got.ACPSessionID != want.ACPSessionID ||
		got.CWD != want.CWD || !got.CreatedAt.Equal(want.CreatedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("Get = %+v, want %+v", got, want)
	}
	// The table holds Unix ms.
	var created, updated int64
	if err := d.QueryRowContext(ctx, `SELECT created_at, updated_at FROM acp_sessions
		WHERE session_id = 's1' AND agent = 'claude-code'`).Scan(&created, &updated); err != nil {
		t.Fatal(err)
	}
	if created != 1762560000000 || updated != 1762560005000 {
		t.Fatal(created, updated)
	}
}

func TestStoreGetMissing(t *testing.T) {
	s := NewStore(openTestDB(t))
	got, ok, err := s.Get(context.Background(), "s1", "codex")
	if err != nil || ok || got != (Record{}) {
		t.Fatalf("a missing row must give (Record{}, false, nil): %+v, %v, %v", got, ok, err)
	}
}

func TestStorePutReplaces(t *testing.T) {
	d := openTestDB(t)
	s := NewStore(d)
	ctx := context.Background()
	at := time.UnixMilli(1762560000000)
	for _, r := range []Record{
		{SessionID: "s1", Agent: "claude-code", ACPSessionID: "acp-1", CWD: "/home/user/a", CreatedAt: at, UpdatedAt: at},
		{SessionID: "s1", Agent: "codex", ACPSessionID: "cx-1", CWD: "/home/user/a", CreatedAt: at, UpdatedAt: at},
		{SessionID: "s1", Agent: "claude-code", ACPSessionID: "acp-2", CWD: "/home/user/b", CreatedAt: at, UpdatedAt: at},
	} {
		if err := s.Put(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := s.Get(ctx, "s1", "claude-code")
	if err != nil || !ok || got.ACPSessionID != "acp-2" || got.CWD != "/home/user/b" {
		t.Fatalf("Put must replace the old row: %+v, %v, %v", got, ok, err)
	}
	if cx, ok, _ := s.Get(ctx, "s1", "codex"); !ok || cx.ACPSessionID != "cx-1" {
		t.Fatalf("another agent keeps its row: %+v", cx)
	}
	var n int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM acp_sessions`).Scan(&n); err != nil || n != 2 {
		t.Fatal(n, err)
	}
}

func TestStorePutZeroTimes(t *testing.T) {
	s := NewStore(openTestDB(t))
	ctx := context.Background()
	before := time.Now().UnixMilli()
	if err := s.Put(ctx, Record{SessionID: "s1", Agent: "claude-code", ACPSessionID: "acp-1", CWD: "/home/user/a"}); err != nil {
		t.Fatal(err)
	}
	after := time.Now().UnixMilli()
	got, _, err := s.Get(ctx, "s1", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	for _, ts := range []time.Time{got.CreatedAt, got.UpdatedAt} {
		if ms := ts.UnixMilli(); ms < before || ms > after {
			t.Fatalf("a zero time must become the current time: %d not in [%d, %d]", ms, before, after)
		}
	}
}

func TestStoreDelete(t *testing.T) {
	s := NewStore(openTestDB(t))
	ctx := context.Background()
	for _, agent := range []string{"claude-code", "codex"} {
		if err := s.Put(ctx, Record{SessionID: "s1", Agent: agent, ACPSessionID: "id-" + agent, CWD: "/home/user/a"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Delete(ctx, "s1", "claude-code"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Get(ctx, "s1", "claude-code"); ok || err != nil {
		t.Fatal("the row must be gone:", ok, err)
	}
	if _, ok, _ := s.Get(ctx, "s1", "codex"); !ok {
		t.Fatal("Delete must keep the row of another agent")
	}
	if err := s.Delete(ctx, "s1", "claude-code"); err != nil {
		t.Fatal("a delete of a missing row is not an error:", err)
	}
}
