package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/ctxmgr"
	"github.com/dnwandana/tether-agent/internal/obs"
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
	a.Prompts = NewPromptCache(func(_ context.Context, s *sessions.Session, _ PromptKey, ch string) (string, time.Time) {
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

// countReads sets the compactionRead hook of a. It returns the count of the reads in viewOf.
func countReads(a *Agent) *atomic.Int32 {
	n := &atomic.Int32{}
	a.compactionRead = func() { n.Add(1) }
	return n
}

// threeToolRounds returns a script of 3 tool rounds and a final answer: 4 model calls.
func threeToolRounds() []step {
	return []step{
		toolCalls(tc("c1", "echo", `{"s":"a"}`)),
		toolCalls(tc("c2", "echo", `{"s":"b"}`)),
		toolCalls(tc("c3", "echo", `{"s":"c"}`)),
		text("done"),
	}
}

func TestRoundReusesTheCompactionReadOfTheCheck(t *testing.T) {
	fp := scripted(append([]step{text("SUM")}, threeToolRounds()...)...)
	a, sess := compactingAgent(t, fp)
	if did, err := a.Compactor.Maybe(ctx, sess, fp, 2000, false); !did || err != nil {
		t.Fatal("the old compaction:", did, err)
	}
	// A large window, so the turn does not compact again.
	a.Cfg.Providers["primary"] = config.Provider{Format: "openai", Model: "test-model", ContextWindow: 1_000_000, Vision: true}
	reads := countReads(a)
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: &recSink{}})
	if err != nil || res.Final != "done" {
		t.Fatal(res, err)
	}
	if len(fp.requests) != 1+4 {
		t.Fatal("4 model calls after the summary call:", len(fp.requests))
	}
	for i, req := range fp.requests[1:] {
		if req.Messages[3].Content != ctxmgr.SummaryPrefix+"SUM" {
			t.Fatal("the request keeps the summary:", i)
		}
	}
	if n := reads.Load(); n != 0 {
		t.Fatal("each request uses the read of the check:", n)
	}
}

func TestCompactingRoundReadsTheNewCompaction(t *testing.T) {
	fp := scripted(append([]step{text("SUM")}, threeToolRounds()...)...)
	a, sess := compactingAgent(t, fp)
	reads := countReads(a)
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: &recSink{}})
	if err != nil || res.Final != "done" {
		t.Fatal(res, err)
	}
	if len(fp.requests) != 1+4 || fp.requests[0].System != ctxmgr.SummaryPrompt {
		t.Fatal("one summary call and 4 model calls:", len(fp.requests))
	}
	for i, req := range fp.requests[1:] {
		if req.Messages[3].Content != ctxmgr.SummaryPrefix+"SUM" {
			t.Fatal("the request has the new summary:", i)
		}
	}
	if n := reads.Load(); n != 1 {
		t.Fatal("only the round that compacts reads again:", n)
	}
}

func TestNoCompactorReadsEachRound(t *testing.T) {
	fp := scripted(threeToolRounds()...)
	a, sess := newAgent(t, fp, echoTool{})
	reads := countReads(a)
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: &recSink{}})
	if err != nil || res.Final != "done" || len(fp.requests) != 4 {
		t.Fatal(res, err, len(fp.requests))
	}
	if n := reads.Load(); n != 4 {
		t.Fatal("one read for each round:", n)
	}
}

func TestCanceledCompactionCheckReadsAgain(t *testing.T) {
	fp := scripted(text("SUM"))
	a, sess := compactingAgent(t, fp)
	if did, err := a.Compactor.Maybe(ctx, sess, fp, 2000, false); !did || err != nil {
		t.Fatal("the old compaction:", did, err)
	}
	reads := countReads(a)
	stored, err := a.Store.Messages(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	did, comps, read := a.compact(cctx, Turn{Session: sess}, fp, &recSink{}, stored, 0)
	if did || comps != nil || read {
		t.Fatal("a canceled check gives no compactions:", did, comps, read)
	}
	msgs, err := a.requestMessages(ctx, &turnState{sessionID: sess.ID}, stored, nil)
	if err != nil || msgs[3].Content != ctxmgr.SummaryPrefix+"SUM" {
		t.Fatal("the request keeps the summary:", err)
	}
	if n := reads.Load(); n != 1 {
		t.Fatal("viewOf reads the compaction again:", n)
	}
}

// overFixed is a fixed size that keeps the view over the threshold of compactingAgent
// after each compaction.
const overFixed = 5000

func TestCompactionNoticeAfterCompaction(t *testing.T) {
	fp := scripted(text("SUM"))
	a, sess := compactingAgent(t, fp)
	stored := messages(t, a, sess.ID)
	before := ctxmgr.ViewSize(0, 0, 0, ctxmgr.BuildStoredView(stored, nil))
	sink := &noticeSink{}
	did, comps, read := a.compact(ctx, Turn{Session: sess}, fp, sink, stored, 0)
	if !did || comps != nil || read {
		t.Fatal("the check compacts:", did, comps, read)
	}
	view, err := a.Compactor.View(ctx, sess.ID)
	if err != nil || len(view) != 3+1+20 {
		t.Fatal(len(view), err)
	}
	after := ctxmgr.ViewSize(0, 0, 0, view)
	want := fmt.Sprintf(NoteCompacted, 57, obs.HumanTokens(before), obs.HumanTokens(after))
	if len(sink.notices) != 1 || sink.notices[0] != want {
		t.Fatalf("one notice with the numbers of the result:\ngot  %q\nwant %q", sink.notices, want)
	}
	if !strings.HasPrefix(want, "🗜 Context compacted. 57 messages → 1 summary. Context: ~") {
		t.Fatal(want)
	}
	if n := len(messages(t, a, sess.ID)); n != 80 {
		t.Fatal("the notice is not a stored message:", n)
	}
}

func TestNoCompactionNoNotice(t *testing.T) {
	fp := scripted()
	a, sess := compactingAgent(t, fp)
	a.Cfg.Providers["primary"] = config.Provider{Format: "openai", Model: "test-model", ContextWindow: 1_000_000, Vision: true}
	sink := &noticeSink{}
	did, _, read := a.compact(ctx, Turn{Session: sess}, fp, sink, messages(t, a, sess.ID), 0)
	if did || !read || len(sink.notices) != 0 || len(fp.requests) != 0 {
		t.Fatal("a view under the threshold gives no notice:", did, read, sink.notices, len(fp.requests))
	}
}

func TestCompactionStopTellsUserOnce(t *testing.T) {
	fp := scripted(text("SUM"))
	a, sess := compactingAgent(t, fp)
	a.Compactor.Cfg.CompactMaxIneffective = 1
	var logs bytes.Buffer
	a.Log = slog.New(slog.NewTextHandler(&logs, nil))
	stored := messages(t, a, sess.ID)
	sink := &noticeSink{}
	if did, _, _ := a.compact(ctx, Turn{Session: sess}, fp, sink, stored, overFixed); !did {
		t.Fatal("the first check compacts")
	}
	did, comps, read := a.compact(ctx, Turn{Session: sess}, fp, sink, stored, overFixed)
	if did || comps == nil || !read {
		t.Fatal("the stop keeps the read of the check:", did, comps, read)
	}
	if did, _, read := a.compact(ctx, Turn{Session: sess}, fp, sink, stored, overFixed); did || !read {
		t.Fatal("a check after the stop does not compact:", did, read)
	}
	if len(sink.notices) != 2 || !strings.HasPrefix(sink.notices[0], "🗜 Context compacted. 57 messages") ||
		sink.notices[1] != NoteCompactionStoppedOne {
		t.Fatalf("one success notice and one stop warning: %q", sink.notices)
	}
	if strings.Contains(logs.String(), "cannot check the compaction") {
		t.Fatal("the stop is not a check error:", logs.String())
	}
	if len(fp.requests) != 1 {
		t.Fatal("one summary call:", len(fp.requests))
	}
}

func TestCompactionStopWarningUsesTheLimit(t *testing.T) {
	fp := scripted(text("SUM"), text("SUM2"))
	a, sess := compactingAgent(t, fp)
	sink := &noticeSink{}
	if did, _, _ := a.compact(ctx, Turn{Session: sess}, fp, sink, messages(t, a, sess.ID), overFixed); !did {
		t.Fatal("the first check compacts")
	}
	// New messages give the second compaction a range.
	seedHistory(t, a, sess.ID, 40)
	stored := messages(t, a, sess.ID)
	if did, _, _ := a.compact(ctx, Turn{Session: sess}, fp, sink, stored, overFixed); !did {
		t.Fatal("one strike is under the limit of 2, so the second check compacts")
	}
	if did, comps, read := a.compact(ctx, Turn{Session: sess}, fp, sink, stored, overFixed); did || comps == nil || !read {
		t.Fatal("the second strike stops:", did, comps, read)
	}
	if len(sink.notices) != 3 || sink.notices[2] != fmt.Sprintf(NoteCompactionStopped, 2) {
		t.Fatalf("two success notices and the stop warning for 2: %q", sink.notices)
	}
}

func TestCompactNoticeOffKeepsTheStopWarning(t *testing.T) {
	fp := scripted(text("SUM"))
	a, sess := compactingAgent(t, fp)
	a.Cfg.Context.CompactNotice = false
	a.Compactor.Cfg.CompactMaxIneffective = 1
	stored := messages(t, a, sess.ID)
	sink := &noticeSink{}
	if did, _, _ := a.compact(ctx, Turn{Session: sess}, fp, sink, stored, overFixed); !did {
		t.Fatal("the first check compacts")
	}
	if did, _, read := a.compact(ctx, Turn{Session: sess}, fp, sink, stored, overFixed); did || !read {
		t.Fatal("the second check stops:", did, read)
	}
	if len(sink.notices) != 1 || sink.notices[0] != NoteCompactionStoppedOne {
		t.Fatalf("no success notice, but the stop warning: %q", sink.notices)
	}
}
