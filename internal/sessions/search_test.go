package sessions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// seedSession creates a session with n messages that alternate between the
// user and the assistant. Message 37 (1-based) holds "hetzner cx22 volume".
func seedSession(t *testing.T, s *Store, n int) *Session {
	t.Helper()
	ctx := context.Background()
	sess, err := s.Create(ctx, Session{Key: "cli:user", Source: "cli", Title: "infra talk"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		role := provider.RoleUser
		if i%2 == 0 {
			role = provider.RoleAssistant
		}
		text := fmt.Sprintf("message %d about the weather", i)
		if i == 37 {
			text = "the hetzner cx22 volume is full"
		}
		if _, err := s.Append(ctx, sess.ID, provider.Message{Role: role, Content: text}, MessageMeta{}); err != nil {
			t.Fatal(err)
		}
	}
	return sess
}

func TestSearchFindsOldPhraseAndScrolls(t *testing.T) {
	ctx := context.Background()
	s := New(openTestDB(t))
	sess := seedSession(t, s, 200)
	hits, err := s.Search(ctx, `"hetzner cx22"`, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].SessionID != sess.ID || !strings.Contains(hits[0].Snippet, "[hetzner") {
		t.Fatalf("%+v", hits)
	}
	if hits[0].Title != "infra talk" || hits[0].Date.IsZero() || hits[0].MessageID == 0 {
		t.Fatalf("%+v", hits[0])
	}
	ms, err := s.Around(ctx, sess.ID, hits[0].MessageID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 21 || ms[10].ID != hits[0].MessageID {
		t.Fatal(len(ms))
	}
	for i := 1; i < len(ms); i++ {
		if ms[i].ID <= ms[i-1].ID {
			t.Fatal("Around must return the messages in id order")
		}
	}
}

func TestAroundAtSessionEdges(t *testing.T) {
	ctx := context.Background()
	s := New(openTestDB(t))
	sess := seedSession(t, s, 30)
	all, _ := s.Messages(ctx, sess.ID)
	ms, err := s.Around(ctx, sess.ID, all[2].ID, 10)
	if err != nil || len(ms) != 13 || ms[0].ID != all[0].ID {
		t.Fatal(len(ms), err)
	}
	ms, err = s.Around(ctx, sess.ID, all[29].ID, 5)
	if err != nil || len(ms) != 6 {
		t.Fatal(len(ms), err)
	}
}

func TestAroundOtherSessionMessageIsError(t *testing.T) {
	ctx := context.Background()
	s := New(openTestDB(t))
	a := seedSession(t, s, 3)
	b := seedSession(t, s, 3)
	bm, _ := s.Messages(ctx, b.ID)
	if _, err := s.Around(ctx, a.ID, bm[0].ID, 5); err == nil {
		t.Fatal("a message of another session must be an error")
	}
}

func TestSearchNewestFirstAndLimit(t *testing.T) {
	ctx := context.Background()
	s := New(openTestDB(t))
	seedSession(t, s, 20)
	hits, err := s.Search(ctx, "weather", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 5 {
		t.Fatal(len(hits))
	}
	for i := 1; i < len(hits); i++ {
		if hits[i].MessageID >= hits[i-1].MessageID {
			t.Fatal("hits must be newest first")
		}
	}
}

func TestObservedMessagesSearchable(t *testing.T) {
	ctx := context.Background()
	s := New(openTestDB(t))
	sess, _ := s.Create(ctx, Session{Key: "tg:group:1"})
	if _, err := s.Append(ctx, sess.ID, provider.Message{Role: provider.RoleUser, Content: "[ana|7]\nthe zanzibar plan"}, MessageMeta{Observed: true}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search(ctx, "zanzibar", 5)
	if err != nil || len(hits) != 1 {
		t.Fatal(hits, err)
	}
}

func TestArchivedSessionsSearchable(t *testing.T) {
	ctx := context.Background()
	s := New(openTestDB(t))
	sess, _ := s.Create(ctx, Session{Key: "cli:user", Archived: true})
	s.Append(ctx, sess.ID, provider.Message{Role: provider.RoleUser, Content: "quokka facts"}, MessageMeta{})
	if hits, err := s.Search(ctx, "quokka", 5); err != nil || len(hits) != 1 {
		t.Fatal(hits, err)
	}
}

func TestBadFTSQueryIsHelpfulError(t *testing.T) {
	s := New(openTestDB(t))
	seedSession(t, s, 3)
	for _, q := range []string{`"unbalanced`, `a AND`, `foo:bar`, `(x`, `c++`} {
		_, err := s.Search(context.Background(), q, 5)
		if err == nil || !strings.Contains(err.Error(), "FTS5 query syntax") || !errors.Is(err, ErrQuerySyntax) {
			t.Errorf("%s: %v", q, err)
		}
	}
}
