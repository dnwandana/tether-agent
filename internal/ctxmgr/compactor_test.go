package ctxmgr

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

var ctx = context.Background()

// fakeSummarizer is a provider that replies with reply, or fails with err. It records
// each request.
type fakeSummarizer struct {
	mu       sync.Mutex
	reply    string
	err      error
	requests []provider.Request
}

func (f *fakeSummarizer) Name() string  { return "primary" }
func (f *fakeSummarizer) Model() string { return "test-model" }

func (f *fakeSummarizer) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	reply, err := f.reply, f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	ch := make(chan provider.Event, 2)
	ch <- provider.Event{Type: provider.EventText, Delta: reply}
	ch <- provider.Event{Type: provider.EventDone, FinishReason: "stop", Usage: &provider.Usage{}}
	close(ch)
	return ch, nil
}

func (f *fakeSummarizer) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// appendPlain appends n messages that alternate user and assistant, each with about 60
// characters.
func appendPlain(t *testing.T, st *sessions.Store, sessID string, from, n int) []int64 {
	t.Helper()
	ids := make([]int64, n)
	for i := range n {
		role := provider.RoleUser
		if (from+i)%2 == 1 {
			role = provider.RoleAssistant
		}
		id, err := st.Append(ctx, sessID, provider.Message{Role: role, Content: fmt.Sprintf("message %03d %s", from+i, strings.Repeat("z", 48))}, sessions.MessageMeta{})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	return ids
}

// compactorWithSession returns a Compactor, a session with n messages, and a summarizer
// that replies "S".
func compactorWithSession(t *testing.T, n int) (*Compactor, *sessions.Session, *fakeSummarizer) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	st := sessions.New(d)
	sess, err := st.Create(ctx, sessions.Session{Key: "cli:test", Source: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	appendPlain(t, st, sess.ID, 0, n)
	c := &Compactor{Store: st, DB: d, Cfg: config.Default().Context}
	return c, sess, &fakeSummarizer{reply: "S"}
}

// viewOf returns the model view of the session.
func viewOf(t *testing.T, c *Compactor, sess *sessions.Session) []provider.Message {
	t.Helper()
	v, err := c.View(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]provider.Message, len(v))
	for i, sv := range v {
		out[i] = sv.Msg
	}
	return out
}

func summaries(view []provider.Message) int {
	n := 0
	for _, m := range view {
		if strings.HasPrefix(m.Content, SummaryPrefix) {
			n++
		}
	}
	return n
}

func TestCompactsAbove50PercentKeepsHeadTail(t *testing.T) {
	c, sess, fp := compactorWithSession(t, 80) // 80 messages, fake summarizer replies "S"
	did, err := c.Maybe(ctx, sess, fp, 2000 /* small window */, false)
	if err != nil || !did {
		t.Fatal(did, err)
	}
	view := viewOf(t, c, sess)
	if view[3].Content != "[conversation summary]\nS" || len(view) != 3+1+20 {
		t.Fatal(len(view))
	}
	if !strings.HasPrefix(view[2].Content, "message 002") || !strings.HasPrefix(view[4].Content, "message 060") {
		t.Fatal(view[2].Content, view[4].Content)
	}
	stored, _ := c.Store.Messages(ctx, sess.ID)
	if len(stored) != 80 {
		t.Fatal("transcript unchanged")
	}
	comps, err := LoadCompactions(ctx, c.DB, sess.ID)
	if err != nil || len(comps) != 1 || comps[0].FromMessageID != stored[3].ID || comps[0].UptoMessageID != stored[59].ID {
		t.Fatalf("%+v %v", comps, err)
	}
	// The summary call sends the range only, with the summary prompt and no tools.
	req := fp.requests[0]
	if req.System != SummaryPrompt || len(req.Tools) != 0 || len(req.Messages) != 1 || req.MaxTokens <= 0 {
		t.Fatalf("%+v", req)
	}
	body := req.Messages[0].Content
	if !strings.Contains(body, "message 003") || !strings.Contains(body, "message 059") ||
		strings.Contains(body, "message 002") || strings.Contains(body, "message 060") {
		t.Fatal(body)
	}
}

func TestBelowThresholdNoop(t *testing.T) {
	c, sess, fp := compactorWithSession(t, 80)
	did, err := c.Maybe(ctx, sess, fp, 1_000_000, false)
	if err != nil || did || fp.calls() != 0 {
		t.Fatal(did, err, fp.calls())
	}
}

func TestForceCompacts(t *testing.T) {
	c, sess, fp := compactorWithSession(t, 80)
	did, err := c.Maybe(ctx, sess, fp, 1_000_000, true)
	if err != nil || !did || summaries(viewOf(t, c, sess)) != 1 {
		t.Fatal(did, err)
	}
}

func TestTooFewMessagesNoop(t *testing.T) {
	c, sess, fp := compactorWithSession(t, 22)
	did, err := c.Maybe(ctx, sess, fp, 100, true)
	if err != nil || did || fp.calls() != 0 {
		t.Fatal(did, err)
	}
}

func TestFailureThreeTimesTellsUser(t *testing.T) {
	c, sess, fp := compactorWithSession(t, 80)
	fp.err = errors.New("summary down")
	for i := 1; i <= 2; i++ {
		did, err := c.Maybe(ctx, sess, fp, 2000, false)
		if did || err != nil {
			t.Fatal(i, did, err)
		}
	}
	did, err := c.Maybe(ctx, sess, fp, 2000, false)
	if did || err == nil || !strings.Contains(err.Error(), "compaction failed 3 times") || !errors.Is(err, ErrCompactionFailed) {
		t.Fatal(did, err)
	}
	// A success resets the count.
	fp.err = nil
	if did, err := c.Maybe(ctx, sess, fp, 2000, false); !did || err != nil {
		t.Fatal(did, err)
	}
	if summaries(viewOf(t, c, sess)) != 1 {
		t.Fatal("one summary")
	}
}

func TestEmptySummaryIsFailure(t *testing.T) {
	c, sess, fp := compactorWithSession(t, 80)
	fp.reply = "  "
	if did, err := c.Maybe(ctx, sess, fp, 2000, false); did || err != nil {
		t.Fatal(did, err)
	}
	if comps, _ := LoadCompactions(ctx, c.DB, sess.ID); len(comps) != 0 {
		t.Fatal("an empty summary must not be stored")
	}
}

func TestOnCompactHookCalledWithFullView(t *testing.T) {
	c, sess, fp := compactorWithSession(t, 80)
	type call struct {
		view       []provider.Message
		start, end int
	}
	got := make(chan call, 1)
	c.OnCompact = func(_ context.Context, s *sessions.Session, view []provider.Message, start, end int) {
		got <- call{view, start, end}
	}
	if did, err := c.Maybe(ctx, sess, fp, 2000, false); !did || err != nil {
		t.Fatal(did, err)
	}
	select {
	case g := <-got:
		if len(g.view) != 80 || g.start != 3 || g.end != 60 || !strings.HasPrefix(g.view[79].Content, "message 079") {
			t.Fatal(len(g.view), g.start, g.end)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnCompact was not called")
	}
}

func TestOnCompactDoesNotBlock(t *testing.T) {
	c, sess, fp := compactorWithSession(t, 80)
	release := make(chan struct{})
	defer close(release)
	c.OnCompact = func(context.Context, *sessions.Session, []provider.Message, int, int) { <-release }
	done := make(chan struct{})
	go func() {
		c.Maybe(ctx, sess, fp, 2000, false)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a slow hook must not block the compaction")
	}
}

func TestSecondCompactionSummarizesPreviousSummary(t *testing.T) {
	c, sess, fp := compactorWithSession(t, 80)
	if did, _ := c.Maybe(ctx, sess, fp, 2000, false); !did {
		t.Fatal("first")
	}
	first, _ := LoadCompactions(ctx, c.DB, sess.ID)
	appendPlain(t, c.Store, sess.ID, 80, 40)
	fp.reply = "S2"
	if did, err := c.Maybe(ctx, sess, fp, 2000, false); !did || err != nil {
		t.Fatal("second", err)
	}
	view := viewOf(t, c, sess)
	if summaries(view) != 1 || view[3].Content != SummaryPrefix+"S2" || len(view) != 3+1+20 {
		t.Fatal(len(view), summaries(view))
	}
	if body := fp.requests[1].Messages[0].Content; !strings.Contains(body, "PREVIOUS SUMMARY") || !strings.Contains(body, "\nS\n") {
		t.Fatal(body)
	}
	all, _ := LoadCompactions(ctx, c.DB, sess.ID)
	if len(all) != 2 || all[1].FromMessageID != first[0].FromMessageID {
		t.Fatalf("%+v", all)
	}
}

func TestStaleProviderCountIgnoredAfterCompaction(t *testing.T) {
	c, sess, fp := compactorWithSession(t, 79)
	// The last response reports a prompt over the threshold.
	if _, err := c.Store.Append(ctx, sess.ID, provider.Message{Role: provider.RoleAssistant, Content: "big"},
		sessions.MessageMeta{Usage: provider.Usage{InputTokens: 1400, CacheReadTokens: 100}}); err != nil {
		t.Fatal(err)
	}
	if did, _ := c.Maybe(ctx, sess, fp, 2000, false); !did {
		t.Fatal("the provider count is over the threshold")
	}
	// The count is from before the compaction, so the next check estimates the view.
	if did, err := c.Maybe(ctx, sess, fp, 2000, false); did || err != nil {
		t.Fatal("no loop of compactions", did, err)
	}
}

func TestBuildViewWithoutCompactionIsStored(t *testing.T) {
	stored := []sessions.StoredMessage{{ID: 1, Msg: provider.Message{Content: "a"}}, {ID: 2, Msg: provider.Message{Content: "b"}}}
	v := BuildView(stored, nil)
	if len(v) != 2 || v[1].Content != "b" {
		t.Fatal(v)
	}
	v = BuildView(stored, []Compaction{{ID: 1, FromMessageID: 2, UptoMessageID: 2, Summary: "x"}})
	if len(v) != 2 || v[1].Content != SummaryPrefix+"x" || v[1].Role != provider.RoleUser {
		t.Fatal(v)
	}
}
