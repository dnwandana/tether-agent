package sessions

import (
	"context"
	"testing"

	"github.com/dnwandana/tether-agent/internal/provider"
)

func TestSearchScope(t *testing.T) {
	ctx := context.Background()
	s := New(openTestDB(t))
	add := func(sess Session, text string) *Session {
		t.Helper()
		created, err := s.Create(ctx, sess)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Append(ctx, created.ID, provider.Message{Role: provider.RoleUser, Content: text}, MessageMeta{}); err != nil {
			t.Fatal(err)
		}
		return created
	}
	alice := add(Session{Key: "telegram:dm:100", Source: "telegram"}, "secret plan alpha")
	add(Session{Key: "telegram:dm:100:7", Source: "telegram"}, "secret plan topic")
	add(Session{Source: "telegram", ParentID: alice.ID, Kind: "subagent"}, "secret plan child")
	bob := add(Session{Key: "telegram:dm:200", Source: "telegram"}, "secret plan bravo")
	add(Session{Source: "telegram", ParentID: bob.ID, Kind: "subagent"}, "secret plan bob child")
	add(Session{Key: "telegram:dm:1000", Source: "telegram"}, "secret plan prefix trap")
	add(Session{Key: "cli:user", Source: "cli"}, "secret plan local")

	count := func(sc Scope) int {
		t.Helper()
		hits, err := s.Search(ctx, "secret", 20, sc)
		if err != nil {
			t.Fatal(err)
		}
		return len(hits)
	}
	if n := count(Scope{All: true}); n != 7 {
		t.Fatalf("all: %d", n)
	}
	aliceScope := Scope{Keys: []string{"telegram:dm:100"}, Prefixes: []string{"telegram:dm:100:"}}
	if n := count(aliceScope); n != 3 {
		t.Fatalf("alice sees her DM, her topic, and her fork: %d", n)
	}
	if n := count(Scope{}); n != 0 {
		t.Fatalf("an empty scope sees nothing: %d", n)
	}
	if ok, err := s.InScope(ctx, bob.ID, aliceScope); err != nil || ok {
		t.Fatal("bob is not in the scope of alice", ok, err)
	}
	child, _ := s.Create(ctx, Session{ParentID: alice.ID, Kind: "review"})
	grandchild, _ := s.Create(ctx, Session{ParentID: child.ID, Kind: "subagent"})
	if ok, err := s.InScope(ctx, grandchild.ID, aliceScope); err != nil || !ok {
		t.Fatal("a fork of a fork of alice is in her scope", ok, err)
	}
	if key, _ := s.RootKey(ctx, grandchild.ID); key != "telegram:dm:100" {
		t.Fatal(key)
	}
}
