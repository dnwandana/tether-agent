package sessions

import (
	"context"
	"testing"

	"github.com/dnwandana/tether-agent/internal/provider"
)

func TestAppendStoresMember(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	s := New(d)
	sess, err := s.Create(ctx, Session{Key: "telegram:-100:7", Source: "telegram"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Append(ctx, sess.ID, provider.Message{Role: provider.RoleUser, Content: "always restart nginx"},
		MessageMeta{Member: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, sess.ID, provider.Message{Role: provider.RoleUser, Content: "hi"}, MessageMeta{}); err != nil {
		t.Fatal(err)
	}
	var raw int
	if err := d.QueryRowContext(ctx, `SELECT member FROM messages WHERE id = ?`, id).Scan(&raw); err != nil || raw != 1 {
		t.Fatal(raw, err)
	}
	msgs, err := s.Messages(ctx, sess.ID)
	if err != nil || len(msgs) != 2 {
		t.Fatal(msgs, err)
	}
	if !msgs[0].Meta.Member || msgs[1].Meta.Member {
		t.Fatal(msgs[0].Meta.Member, msgs[1].Meta.Member)
	}
	page, err := s.MessagesPage(ctx, sess.ID, 0, 0, 10)
	if err != nil || len(page) != 2 || !page[0].Meta.Member || page[1].Meta.Member {
		t.Fatal(page, err)
	}
}

func TestForkCopiesMember(t *testing.T) {
	ctx := context.Background()
	s := New(openTestDB(t))
	sess, _ := s.Create(ctx, Session{Key: "telegram:-100:7", Source: "telegram"})
	if _, err := s.Append(ctx, sess.ID, provider.Message{Role: provider.RoleUser, Content: "always restart nginx"},
		MessageMeta{Member: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, sess.ID, provider.Message{Role: provider.RoleUser, Content: "hi"}, MessageMeta{}); err != nil {
		t.Fatal(err)
	}
	f, err := s.Fork(ctx, sess.ID, "copy")
	if err != nil {
		t.Fatal(err)
	}
	msgs, _ := s.Messages(ctx, f.ID)
	if len(msgs) != 2 || !msgs[0].Meta.Member || msgs[1].Meta.Member {
		t.Fatal(msgs)
	}
}
