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

func TestOnCompactRunsAfterTheSummaryIsSaved(t *testing.T) {
	c, sess, fp := compactorWithSession(t, 80)
	saved := -1
	c.OnCompact = func(context.Context, *sessions.Session, []provider.Message, int, int) {
		comps, err := LoadCompactions(ctx, c.DB, sess.ID)
		if err != nil {
			t.Error(err)
		}
		saved = len(comps)
	}
	if did, err := c.Maybe(ctx, sess, fp, 2000, false); !did || err != nil {
		t.Fatal(did, err)
	}
	if saved != 1 {
		t.Fatal("OnCompact must run after the save and before Maybe returns:", saved)
	}
}

func TestOnCompactRunsOnceForTheSavedRange(t *testing.T) {
	// One failure keeps the failure count. maxFailures failures reset it.
	for _, failures := range []int{1, maxFailures} {
		t.Run(fmt.Sprint(failures), func(t *testing.T) { onCompactAfterFailures(t, failures) })
	}
}

// onCompactAfterFailures fails the summary n times, adds 20 messages, and then compacts.
// It checks that OnCompact ran one time, for the range of the saved compaction.
func onCompactAfterFailures(t *testing.T, n int) {
	c, sess, fp := compactorWithSession(t, 80)
	type call struct {
		view       []provider.Message
		start, end int
	}
	var (
		mu    sync.Mutex
		calls []call
	)
	c.OnCompact = func(_ context.Context, _ *sessions.Session, view []provider.Message, start, end int) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, call{view, start, end})
	}
	fp.err = errors.New("summary down")
	for i := 1; i <= n; i++ {
		if did, _ := c.Maybe(ctx, sess, fp, 2000, false); did {
			t.Fatal(i, "a failed summary must not compact")
		}
	}
	// The session grows before the next attempt, so the range moves.
	appendPlain(t, c.Store, sess.ID, 80, 20)
	fp.err = nil
	if did, err := c.Maybe(ctx, sess, fp, 2000, false); !did || err != nil {
		t.Fatal(did, err)
	}
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatal("one review for each compaction that is saved:", len(calls))
	}
	stored, _ := c.Store.Messages(ctx, sess.ID)
	comps, err := LoadCompactions(ctx, c.DB, sess.ID)
	if err != nil || len(comps) != 1 {
		t.Fatalf("%+v %v", comps, err)
	}
	content := map[int64]string{}
	for _, m := range stored {
		content[m.ID] = m.Msg.Content
	}
	g := calls[0]
	if len(g.view) != 100 || g.view[g.start].Content != content[comps[0].FromMessageID] ||
		g.view[g.end-1].Content != content[comps[0].UptoMessageID] {
		t.Fatal("the review must cover the saved range:", len(g.view), g.start, g.end, comps[0].FromMessageID, comps[0].UptoMessageID)
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

func TestLatestCompactionGivesOnlyTheNewest(t *testing.T) {
	c, sess, fp := compactorWithSession(t, 80)
	if latest, err := LatestCompaction(ctx, c.DB, sess.ID); err != nil || len(latest) != 0 {
		t.Fatal(latest, err)
	}
	if did, _ := c.Maybe(ctx, sess, fp, 2000, false); !did {
		t.Fatal("first")
	}
	appendPlain(t, c.Store, sess.ID, 80, 40)
	fp.reply = "S2"
	if did, err := c.Maybe(ctx, sess, fp, 2000, false); !did || err != nil {
		t.Fatal("second", err)
	}
	all, _ := LoadCompactions(ctx, c.DB, sess.ID)
	latest, err := LatestCompaction(ctx, c.DB, sess.ID)
	if err != nil || len(all) != 2 || len(latest) != 1 || latest[0] != all[1] || latest[0].Summary != "S2" {
		t.Fatalf("%+v %+v %v", latest, all, err)
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

func TestMaybeStoredCountsFixedPartsWithNoProviderCount(t *testing.T) {
	c, sess, fp := compactorWithSession(t, 79)
	stored, err := c.Store.Messages(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The messages alone are about 1500 tokens, below the threshold of 2000.
	if did, err := c.MaybeStored(ctx, sess, fp, 4000, false, stored, 0); did || err != nil {
		t.Fatal("the messages alone must not compact", did, err)
	}
	// A large system prompt and large tool schemas move the request over the threshold.
	if did, err := c.MaybeStored(ctx, sess, fp, 4000, false, stored, 1000); !did || err != nil {
		t.Fatal("the system prompt and the schemas count", did, err)
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

const testSnap = TodoSnapshotHeader + "\n- [ ] 3. Run the cron tests 20 times (pending)"

func TestCompactionAddsOneTodoSnapshot(t *testing.T) {
	c, sess, fp := compactorWithSession(t, 80)
	var asked []string
	c.Todo = func(_ context.Context, id string) string { asked = append(asked, id); return testSnap }
	if did, err := c.Maybe(ctx, sess, fp, 1_000_000, true); !did || err != nil {
		t.Fatal(did, err)
	}
	comps, _ := LoadCompactions(ctx, c.DB, sess.ID)
	if len(comps) != 1 || comps[0].Summary != "S\n\n"+testSnap || len(asked) != 1 || asked[0] != sess.ID {
		t.Fatalf("%+v %v", comps, asked)
	}
}

func TestCompactionWithNoOpenItemAddsNothing(t *testing.T) {
	c, sess, fp := compactorWithSession(t, 80)
	c.Todo = func(context.Context, string) string { return "" }
	if did, _ := c.Maybe(ctx, sess, fp, 1_000_000, true); !did {
		t.Fatal("no compaction")
	}
	if comps, _ := LoadCompactions(ctx, c.DB, sess.ID); comps[0].Summary != "S" {
		t.Fatalf("%q", comps[0].Summary)
	}
}

func TestSecondCompactionStripsTheOldSnapshot(t *testing.T) {
	c, sess, fp := compactorWithSession(t, 80)
	c.Todo = func(context.Context, string) string { return testSnap }
	if did, _ := c.Maybe(ctx, sess, fp, 2000, false); !did {
		t.Fatal("first")
	}
	appendPlain(t, c.Store, sess.ID, 80, 40)
	// The summary model copies the old snapshot into its reply.
	fp.reply = "S2\n\n" + TodoSnapshotHeader + "\n- [ ] 1. old item (pending)"
	if did, err := c.Maybe(ctx, sess, fp, 2000, false); !did || err != nil {
		t.Fatal("second", err)
	}
	body := fp.requests[1].Messages[0].Content
	if !strings.Contains(body, "[PREVIOUS SUMMARY]:\nS\n") || strings.Contains(body, TodoSnapshotHeader) {
		t.Fatalf("the old snapshot must not go to the summary model: %q", body)
	}
	view := viewOf(t, c, sess)
	if summaries(view) != 1 {
		t.Fatal(summaries(view))
	}
	for _, m := range view {
		if strings.HasPrefix(m.Content, SummaryPrefix) && (m.Content != SummaryPrefix+"S2\n\n"+testSnap || strings.Count(m.Content, TodoSnapshotHeader) != 1) {
			t.Fatalf("%q", m.Content)
		}
	}
}

func TestStripTodoSnapshot(t *testing.T) {
	for in, want := range map[string]string{
		"S\n\n" + testSnap:                 "S",
		"S":                                "S",
		testSnap:                           "",
		"a\n" + TodoSnapshotHeader + "\nb": "a",
	} {
		if got := StripTodoSnapshot(in); got != want {
			t.Errorf("StripTodoSnapshot(%q) = %q, want %q", in, got, want)
		}
	}
}
