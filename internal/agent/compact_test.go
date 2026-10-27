package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/ctxmgr"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// noticeSink records the notices of a turn.
type noticeSink struct {
	recSink
	mu      sync.Mutex
	notices []string
}

func (n *noticeSink) Notice(s string) { n.mu.Lock(); n.notices = append(n.notices, s); n.mu.Unlock() }

// seedHistory appends n messages that alternate user and assistant to the session.
func seedHistory(t *testing.T, a *Agent, sessID string, n int) {
	t.Helper()
	for i := range n {
		role := provider.RoleUser
		if i%2 == 1 {
			role = provider.RoleAssistant
		}
		m := provider.Message{Role: role, Content: fmt.Sprintf("old %03d %s", i, strings.Repeat("z", 50))}
		if _, err := a.Store.Append(ctx, sessID, m, sessions.MessageMeta{}); err != nil {
			t.Fatal(err)
		}
	}
}

// compactingAgent returns an Agent with a compactor, a window of 2,000 tokens, and a
// session with 80 old messages.
func compactingAgent(t *testing.T, fp *fakeProv) (*Agent, *sessions.Session) {
	t.Helper()
	a, sess := newAgent(t, fp, echoTool{})
	a.Cfg.Providers["primary"] = config.Provider{Format: "openai", Model: "test-model", ContextWindow: 2000, Vision: true}
	a.Compactor = &ctxmgr.Compactor{Store: a.Store, DB: a.DB, Cfg: a.Cfg.Context}
	seedHistory(t, a, sess.ID, 80)
	return a, sess
}

func TestRunTurnCompactsBeforeModelCall(t *testing.T) {
	fp := scripted(text("SUM"), text("ok"))
	a, sess := compactingAgent(t, fp)
	builds := 0
	a.Prompts = NewPromptCache(func(_ context.Context, s *sessions.Session, ch string) (string, time.Time) {
		builds++
		return fmt.Sprint("P", builds), time.Time{}
	})
	img := provider.Image{Path: "/tmp/a.png", MIME: "image/png", Data: []byte("png")}
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "new question", Images: []provider.Image{img}, Toolsets: all(), Sink: &recSink{}})
	if err != nil || res.Final != "ok" {
		t.Fatal(res, err)
	}
	if fp.requests[0].System != ctxmgr.SummaryPrompt || len(fp.requests[0].Tools) != 0 {
		t.Fatal("the first call is the summary call")
	}
	req := fp.requests[1]
	if req.System != "P2" || builds != 2 {
		t.Fatal("the compaction rebuilds the prompt:", req.System, builds)
	}
	if req.Messages[3].Content != ctxmgr.SummaryPrefix+"SUM" || len(req.Messages) != 3+1+20 {
		t.Fatal(len(req.Messages), req.Messages[3].Content)
	}
	last := req.Messages[len(req.Messages)-1]
	if !strings.HasPrefix(last.Content, "new question") || len(last.Images) != 1 {
		t.Fatalf("the user message of the turn keeps its images: %+v", last)
	}
	if n := len(messages(t, a, sess.ID)); n != 82 {
		t.Fatal("the transcript does not change", n)
	}
	v, err := a.View(ctx, sess.ID)
	if err != nil || len(v) != 3+1+20+1 || v[3].Content != ctxmgr.SummaryPrefix+"SUM" {
		t.Fatal(len(v), err)
	}
}

func TestSteerPatchAfterCompaction(t *testing.T) {
	fp := scripted(text("SUM"), toolCalls(tc("c1", "echo", `{"s":"x"}`)), text("done"))
	a, sess := compactingAgent(t, fp)
	q := NewSteerQueue()
	// The pre-call drain patches the stored tool message by its id.
	a.beforeCall = func(round int) {
		if round == 2 {
			q.Push("also check y")
		}
	}
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: &recSink{}, Steer: q}); err != nil {
		t.Fatal(err)
	}
	if got := lastToolContent(fp.requests[2]); !strings.Contains(got, "also check y") {
		t.Fatal("the steer text reaches the tool result after a compaction:", got)
	}
	if fp.requests[2].Messages[3].Content != ctxmgr.SummaryPrefix+"SUM" {
		t.Fatal("the request keeps the summary")
	}
}

func TestCompactionFailureTellsUserAfterThree(t *testing.T) {
	fp := scripted(text("a0"), text("a1"), text("a2"))
	a, sess := compactingAgent(t, fp)
	fail := &failingSummary{fakeProv: fp}
	a.Prov, a.Providers["primary"] = fail, fail
	sink := &noticeSink{}
	for i := range 3 {
		if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "q", Sink: sink}); err != nil {
			t.Fatal(i, err)
		}
	}
	if len(sink.notices) != 1 || !strings.Contains(sink.notices[0], "compaction failed 3 times") {
		t.Fatal(sink.notices)
	}
}

// failingSummary fails each summary call and passes the other calls to fakeProv.
type failingSummary struct{ *fakeProv }

func (f *failingSummary) Stream(c context.Context, req provider.Request) (<-chan provider.Event, error) {
	if req.System == ctxmgr.SummaryPrompt {
		return nil, errors.New("summary down")
	}
	return f.fakeProv.Stream(c, req)
}
