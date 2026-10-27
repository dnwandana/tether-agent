package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// openSessions returns a session store over a new state.db with one session
// of n messages. Message 37 (1-based) holds "hetzner cx22 volume".
func openSessions(t *testing.T, n int) (*sessions.Store, *sessions.Session, []sessions.StoredMessage) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	s := sessions.New(d)
	c := context.Background()
	sess, err := s.Create(c, sessions.Session{Key: "cli:user", Title: "infra talk"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		text := fmt.Sprintf("message %d", i)
		if i == 37 {
			text = "the hetzner cx22 volume is full"
		}
		role := provider.RoleUser
		if i%2 == 0 {
			role = provider.RoleAssistant
		}
		if _, err := s.Append(c, sess.ID, provider.Message{Role: role, Content: text}, sessions.MessageMeta{}); err != nil {
			t.Fatal(err)
		}
	}
	ms, _ := s.Messages(c, sess.ID)
	return s, sess, ms
}

func TestToolThreeShapes(t *testing.T) {
	s, sess, ms := openSessions(t, 60)
	tool := SessionSearchTool{Store: s, MaxChars: 50000}
	hit := ms[36]

	out := runTool(t, tool, `{"query":"\"hetzner cx22\""}`)
	want := fmt.Sprintf("session %s · infra talk · %s · msg %d: ", sess.ID, hit.At.Format("2006-01-02 15:04"), hit.ID)
	if !strings.Contains(out, want) || !strings.Contains(out, "[hetzner cx22]") {
		t.Fatalf("got %q, want %q", out, want)
	}

	out = runTool(t, tool, fmt.Sprintf(`{"session_id":%q}`, sess.ID))
	if !strings.Contains(out, fmt.Sprintf("msg %d [user] message 1\n", ms[0].ID)) ||
		!strings.Contains(out, fmt.Sprintf("msg %d [assistant] message 60", ms[59].ID)) {
		t.Fatal(out)
	}

	out = runTool(t, tool, fmt.Sprintf(`{"session_id":%q,"around_message_id":%d,"window":2}`, sess.ID, hit.ID))
	if strings.Count(out, "\nmsg ") != 5 || !strings.Contains(out, "hetzner") ||
		!strings.Contains(out, fmt.Sprintf("msg %d ", ms[34].ID)) || strings.Contains(out, fmt.Sprintf("msg %d ", ms[33].ID)) {
		t.Fatal(out)
	}
	// The default window is 10.
	out = runTool(t, tool, fmt.Sprintf(`{"session_id":%q,"around_message_id":%d}`, sess.ID, hit.ID))
	if strings.Count(out, "\nmsg ") != 21 {
		t.Fatal(out)
	}
}

func TestToolReadShapeCapped(t *testing.T) {
	s, sess, ms := openSessions(t, 200)
	tool := SessionSearchTool{Store: s, MaxChars: 500}
	out := runTool(t, tool, fmt.Sprintf(`{"session_id":%q}`, sess.ID))
	if len(out) > 700 || !strings.Contains(out, "more messages") || !strings.Contains(out, "around_message_id") {
		t.Fatalf("%d %q", len(out), out)
	}
	if !strings.Contains(out, fmt.Sprintf("msg %d [user] message 1\n", ms[0].ID)) {
		t.Fatal(out)
	}
}

func TestToolSearchErrors(t *testing.T) {
	s, sess, _ := openSessions(t, 3)
	tool := SessionSearchTool{Store: s}
	if out := runToolErr(t, tool, `{"query":"\"open"}`); !strings.Contains(out, "FTS5 query syntax") {
		t.Fatal(out)
	}
	if out := runToolErr(t, tool, `{}`); !strings.Contains(out, "query") {
		t.Fatal(out)
	}
	if out := runToolErr(t, tool, `{"session_id":"nope"}`); !strings.Contains(out, "nope") {
		t.Fatal(out)
	}
	if out := runToolErr(t, tool, fmt.Sprintf(`{"session_id":%q,"around_message_id":999999}`, sess.ID)); !strings.Contains(out, "999999") {
		t.Fatal(out)
	}
	if out := runTool(t, tool, `{"query":"zebra"}`); !strings.Contains(out, "no messages match") {
		t.Fatal(out)
	}
	if !tool.ParallelSafe() || tool.Toolset() != SetMemory || tool.Name() != "session_search" {
		t.Fatal("bad tool metadata")
	}
}
