package sessions

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
)

func TestAppendStoresOutside(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	s := New(d)
	sess, err := s.Create(ctx, Session{Key: "cli:t", Source: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Append(ctx, sess.ID, provider.Message{Role: provider.RoleTool, Content: "page text", ToolName: "web_fetch"},
		MessageMeta{Outside: []string{"mcp_docs", "web_fetch"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, sess.ID, provider.Message{Role: provider.RoleUser, Content: "hi"}, MessageMeta{}); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := d.QueryRowContext(ctx, `SELECT outside FROM messages WHERE id = ?`, id).Scan(&raw); err != nil || raw != "mcp_docs,web_fetch" {
		t.Fatal(raw, err)
	}
	msgs, err := s.Messages(ctx, sess.ID)
	if err != nil || len(msgs) != 2 {
		t.Fatal(msgs, err)
	}
	if !reflect.DeepEqual(msgs[0].Meta.Outside, []string{"mcp_docs", "web_fetch"}) || msgs[1].Meta.Outside != nil {
		t.Fatal(msgs[0].Meta.Outside, msgs[1].Meta.Outside)
	}
	page, err := s.MessagesPage(ctx, sess.ID, 0, 0, 10)
	if err != nil || len(page) != 2 || len(page[0].Meta.Outside) != 2 {
		t.Fatal(page, err)
	}
}

func TestForkCopiesOutside(t *testing.T) {
	ctx := context.Background()
	s := New(openTestDB(t))
	sess, _ := s.Create(ctx, Session{Key: "cli:t", Source: "cli"})
	if _, err := s.Append(ctx, sess.ID, provider.Message{Role: provider.RoleUser, Content: "read the docs"},
		MessageMeta{Outside: []string{"delegate_task"}}); err != nil {
		t.Fatal(err)
	}
	f, err := s.Fork(ctx, sess.ID, "copy")
	if err != nil {
		t.Fatal(err)
	}
	msgs, _ := s.Messages(ctx, f.ID)
	if len(msgs) != 1 || !reflect.DeepEqual(msgs[0].Meta.Outside, []string{"delegate_task"}) {
		t.Fatal(msgs)
	}
}

func TestPruneDeletesOldTidySessions(t *testing.T) {
	d := openTestDB(t)
	s := New(d)
	day := 24 * time.Hour
	old := agedSession(t, d, s, KindTidy, "telegram", 91*day)
	got, err := s.PruneAutomatic(context.Background(), retNow.Add(-90*day))
	if err != nil || len(got) != 1 || got[0] != old.ID {
		t.Fatalf("pruned %v, want [%s]: %v", got, old.ID, err)
	}
}
