package sessions

import (
	"context"
	"errors"
	"testing"

	"github.com/dnwandana/tether-agent/internal/provider"
)

func TestForkCopiesAndIsolates(t *testing.T) {
	ctx := context.Background()
	s := New(openTestDB(t))
	orig := seedSession(t, s, 6)
	if err := s.SetModel(ctx, orig.ID, "cheap"); err != nil {
		t.Fatal(err)
	}
	f, err := s.Fork(ctx, orig.ID, "try B")
	if err != nil || f.ParentID != orig.ID || f.Title != "try B" || f.ID == orig.ID {
		t.Fatal(f, err)
	}
	if f.Key != orig.Key || f.Source != orig.Source || f.ModelOverride != "cheap" {
		t.Fatalf("%+v", f)
	}
	if got, err := s.Get(ctx, f.ID); err != nil || got.ParentID != orig.ID || got.Title != "try B" {
		t.Fatal(got, err)
	}
	if _, err := s.Append(ctx, f.ID, provider.Message{Role: provider.RoleUser, Content: "only in fork"}, MessageMeta{}); err != nil {
		t.Fatal(err)
	}
	a, _ := s.Messages(ctx, orig.ID)
	b, _ := s.Messages(ctx, f.ID)
	if len(a) != 6 || len(b) != 7 {
		t.Fatal(len(a), len(b))
	}
	for i := range a {
		if a[i].Msg.Content != b[i].Msg.Content || a[i].Msg.Role != b[i].Msg.Role || a[i].ID == b[i].ID {
			t.Fatalf("message %d: %+v vs %+v", i, a[i], b[i])
		}
	}
}

func TestForkDefaultTitle(t *testing.T) {
	ctx := context.Background()
	s := New(openTestDB(t))
	orig := seedSession(t, s, 2)
	f, err := s.Fork(ctx, orig.ID, "")
	if err != nil || f.Title != "infra talk (fork)" {
		t.Fatal(f, err)
	}
	if _, err := s.Fork(ctx, "nope", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestForkRemapsCompactionRows(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	s := New(d)
	orig := seedSession(t, s, 6)
	msgs, _ := s.Messages(ctx, orig.ID)
	if _, err := d.ExecContext(ctx, `INSERT INTO compactions (session_id, from_message_id, upto_message_id, summary, created_at)
		VALUES (?, ?, ?, 'sum', 5)`, orig.ID, msgs[1].ID, msgs[3].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `UPDATE messages SET compacted = 1 WHERE id = ?`, msgs[2].ID); err != nil {
		t.Fatal(err)
	}
	f, err := s.Fork(ctx, orig.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	fm, _ := s.Messages(ctx, f.ID)
	var from, upto, at int64
	var sum string
	if err := d.QueryRowContext(ctx, `SELECT from_message_id, upto_message_id, summary, created_at FROM compactions
		WHERE session_id = ?`, f.ID).Scan(&from, &upto, &sum, &at); err != nil {
		t.Fatal(err)
	}
	if from != fm[1].ID || upto != fm[3].ID || sum != "sum" || at != 5 {
		t.Fatal(from, upto, sum, at, fm[1].ID, fm[3].ID)
	}
	if !fm[2].Compacted {
		t.Fatal("the compacted flag must be copied")
	}
}
