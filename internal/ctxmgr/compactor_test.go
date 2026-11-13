package ctxmgr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
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
	usage    provider.Usage // the usage of the EventDone
	midErr   error          // when set, the stream sends an EventError in place of EventDone
	requests []provider.Request
}

func (f *fakeSummarizer) Name() string  { return "primary" }
func (f *fakeSummarizer) Model() string { return "test-model" }

func (f *fakeSummarizer) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	reply, err, u, midErr := f.reply, f.err, f.usage, f.midErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	ch := make(chan provider.Event, 2)
	ch <- provider.Event{Type: provider.EventText, Delta: reply}
	if midErr != nil {
		ch <- provider.Event{Type: provider.EventError, Err: midErr}
	} else {
		ch <- provider.Event{Type: provider.EventDone, FinishReason: "stop", Usage: &u}
	}
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

// usageCall is one call of a provider.RecordUsage function.
type usageCall struct {
	sessionID, task string
	prov            provider.Provider
	out             provider.Result
}

// recordTo returns a provider.RecordUsage that appends each call to calls.
func recordTo(calls *[]usageCall) provider.RecordUsage {
	return func(_ context.Context, sessionID, task string, prov provider.Provider, out provider.Result) {
		*calls = append(*calls, usageCall{sessionID, task, prov, out})
	}
}

func TestCompactionRecordsTheSummaryCall(t *testing.T) {
	c, sess, fp := compactorWithSession(t, 80)
	fp.usage = provider.Usage{InputTokens: 1200, CacheReadTokens: 300, OutputTokens: 40}
	var got []usageCall
	c.Record = recordTo(&got)
	if did, err := c.Maybe(ctx, sess, fp, 2000, false); !did || err != nil {
		t.Fatal(did, err)
	}
	if len(got) != 1 || got[0].sessionID != sess.ID || got[0].task != "compaction" || got[0].prov != fp ||
		got[0].out.Text != "S" || got[0].out.Usage.InputTokens != 1200 ||
		got[0].out.Usage.CacheReadTokens != 300 || got[0].out.Usage.OutputTokens != 40 {
		t.Fatalf("%+v", got)
	}
}

// A billed compaction call with an empty answer is recorded.
func TestCompactionRecordsAnEmptySummary(t *testing.T) {
	c, sess, fp := compactorWithSession(t, 80)
	fp.reply = "  "
	fp.usage = provider.Usage{InputTokens: 1200, OutputTokens: 1}
	var got []usageCall
	c.Record = recordTo(&got)
	if did, err := c.Maybe(ctx, sess, fp, 2000, false); did || err != nil {
		t.Fatal(did, err)
	}
	if comps, _ := LoadCompactions(ctx, c.DB, sess.ID); len(comps) != 0 {
		t.Fatal("an empty summary must not be stored")
	}
	if len(got) != 1 || got[0].sessionID != sess.ID || got[0].task != "compaction" ||
		got[0].out.Usage.InputTokens != 1200 {
		t.Fatalf("%+v", got)
	}
}

func TestCompactionFailedCallRecordsNothing(t *testing.T) {
	for name, set := range map[string]func(*fakeSummarizer){
		"stream":  func(f *fakeSummarizer) { f.err = errors.New("summary down") },
		"collect": func(f *fakeSummarizer) { f.midErr = errors.New("stream cut") },
	} {
		t.Run(name, func(t *testing.T) {
			c, sess, fp := compactorWithSession(t, 80)
			set(fp)
			var got []usageCall
			c.Record = recordTo(&got)
			if did, err := c.Maybe(ctx, sess, fp, 2000, false); did || err != nil {
				t.Fatal(did, err)
			}
			if len(got) != 0 {
				t.Fatalf("%+v", got)
			}
		})
	}
}

// A nil Record records nothing, and the compaction still runs.
func TestCompactionNilRecord(t *testing.T) {
	c, sess, fp := compactorWithSession(t, 80)
	fp.reply = "  "
	if did, err := c.Maybe(ctx, sess, fp, 2000, false); did || err != nil {
		t.Fatal(did, err)
	}
	fp.reply = "S"
	if did, err := c.Maybe(ctx, sess, fp, 2000, false); !did || err != nil {
		t.Fatal(did, err)
	}
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

func TestMaybeStoredViewReturnsTheCompactionsThatItRead(t *testing.T) {
	cases := []struct {
		name        string
		prior       bool  // compact one time before the check
		more        int   // messages to add after the prior compaction
		protectTail bool  // protect the last 21 messages, so the old summary is in the tail
		sumErr      error // the error of each summary call
		before      int   // failed checks before the check under test
		cancel      bool  // cancel the context of the check
		window      int
		force       bool
		wantDid     bool
		wantErr     bool
		wantLen     int // -1: comps must be nil
	}{
		{name: "no compaction, below the threshold", window: 1_000_000, wantLen: 0},
		{name: "old compaction, below the threshold", prior: true, window: 1_000_000, wantLen: 1},
		{name: "old compaction, no range to compact", prior: true, window: 2000, force: true, wantLen: 1},
		{name: "old summary in the protected tail", prior: true, protectTail: true, window: 2000, force: true, wantLen: 1},
		{name: "summary call fails", prior: true, more: 40, sumErr: errors.New("summary down"), window: 2000, wantLen: 1},
		{name: "third failure in a row", prior: true, more: 40, sumErr: errors.New("summary down"), before: 2, window: 2000, wantErr: true, wantLen: 1},
		{name: "compaction done", window: 2000, wantDid: true, wantLen: -1},
		{name: "read fails", prior: true, cancel: true, window: 2000, wantErr: true, wantLen: -1},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			c, sess, fp := compactorWithSession(t, 80)
			if tt.prior {
				if did, err := c.Maybe(ctx, sess, fp, 2000, false); !did || err != nil {
					t.Fatal("the prior compaction:", did, err)
				}
				appendPlain(t, c.Store, sess.ID, 80, tt.more)
			}
			if tt.protectTail {
				c.Cfg.ProtectFirstN, c.Cfg.ProtectLastN = 0, 21
			}
			fp.err = tt.sumErr
			stored, err := c.Store.Messages(ctx, sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			for range tt.before {
				if did, _, err := c.MaybeStoredView(ctx, sess, fp, tt.window, tt.force, stored, 0); did || err != nil {
					t.Fatal("a failure before the limit:", did, err)
				}
			}
			cctx := ctx
			if tt.cancel {
				var cancel context.CancelFunc
				cctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			did, comps, err := c.MaybeStoredView(cctx, sess, fp, tt.window, tt.force, stored, 0)
			if did != tt.wantDid || (err != nil) != tt.wantErr {
				t.Fatal(did, err)
			}
			if tt.wantErr && !tt.cancel && !errors.Is(err, ErrCompactionFailed) {
				t.Fatal(err)
			}
			if tt.wantLen < 0 {
				if comps != nil {
					t.Fatalf("comps must be nil: %+v", comps)
				}
				return
			}
			want, err := LatestCompaction(ctx, c.DB, sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(comps) != tt.wantLen || !reflect.DeepEqual(comps, want) {
				t.Fatalf("got %+v, want %+v", comps, want)
			}
		})
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

// A compaction that leaves the view over the threshold counts one strike. At two strikes in
// a row, the automatic check stops. A forced compaction still runs, and a view under the
// threshold clears the strikes.
func TestCompactionStopsAfterTwoIneffectiveCompactions(t *testing.T) {
	c, sess, fp := compactorWithSession(t, 80)
	next := 80
	more := func() {
		appendPlain(t, c.Store, sess.ID, next, 40)
		next += 40
	}
	check := func(fixed int, force bool) bool {
		t.Helper()
		stored, err := c.Store.Messages(ctx, sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		did, err := c.MaybeStored(ctx, sess, fp, 4000, force, stored, fixed)
		if err != nil {
			t.Fatal(err)
		}
		return did
	}
	// The fixed part alone (3000 tokens) is over the threshold of 2000, so no compaction
	// can bring the view under it.
	if !check(3000, false) {
		t.Fatal("the first compaction must run")
	}
	more()
	if !check(3000, false) {
		t.Fatal("one strike must not stop the compaction")
	}
	more()
	if check(3000, false) {
		t.Fatal("two strikes in a row must stop the automatic compaction")
	}
	if check(3000, false) {
		t.Fatal("the stop must stay")
	}
	if !check(3000, true) {
		t.Fatal("a forced compaction must still run")
	}
	if check(0, false) {
		t.Fatal("the view is under the threshold")
	}
	more()
	if !check(3000, false) {
		t.Fatal("a view under the threshold must clear the strikes")
	}
}

// deadlineSummarizer records the deadline of the summary call.
type deadlineSummarizer struct {
	*fakeSummarizer
	deadline time.Time
}

func (d *deadlineSummarizer) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	d.deadline, _ = ctx.Deadline()
	return d.fakeSummarizer.Stream(ctx, req)
}

func TestCompactionSummaryTimeoutFromConfig(t *testing.T) {
	for _, tc := range []struct {
		seconds int
		want    time.Duration
	}{{0, 5 * time.Minute}, {10, 10 * time.Second}} {
		c, sess, fp := compactorWithSession(t, 80)
		c.Cfg.SummaryTimeoutS = tc.seconds
		dp := &deadlineSummarizer{fakeSummarizer: fp}
		start := time.Now()
		if did, err := c.Maybe(ctx, sess, dp, 2000, false); !did || err != nil {
			t.Fatal(did, err)
		}
		if got := dp.deadline.Sub(start); got < tc.want-time.Second || got > tc.want+time.Second {
			t.Fatalf("summary_timeout_s %d: the deadline is %v after the start, want %v", tc.seconds, got, tc.want)
		}
	}
}

func TestCompactionSummaryTokensFromConfig(t *testing.T) {
	// Window 2000: the trigger is 1000 tokens, and the ratio 0.2 gives 200 tokens.
	for _, tc := range []struct{ min, max, want int }{
		{0, 0, 2000},
		{100, 150, 150},
		{500, 12000, 500},
	} {
		c, sess, fp := compactorWithSession(t, 80)
		c.Cfg.SummaryMinTokens, c.Cfg.SummaryMaxTokens = tc.min, tc.max
		if did, err := c.Maybe(ctx, sess, fp, 2000, false); !did || err != nil {
			t.Fatal(did, err)
		}
		req := fp.requests[0]
		if req.MaxTokens != tc.want*13/10 || !strings.Contains(req.Messages[0].Content, fmt.Sprintf("Target about %d tokens.", tc.want)) {
			t.Fatalf("min %d, max %d: max tokens %d", tc.min, tc.max, req.MaxTokens)
		}
	}
}

func TestCompactionMaxFailuresFromConfig(t *testing.T) {
	c, sess, fp := compactorWithSession(t, 80)
	fp.err = errors.New("summary down")
	c.Cfg.CompactMaxFailures = 2
	if did, err := c.Maybe(ctx, sess, fp, 2000, false); did || err != nil {
		t.Fatal(did, err)
	}
	did, err := c.Maybe(ctx, sess, fp, 2000, false)
	if did || !errors.Is(err, ErrCompactionFailed) || !strings.Contains(err.Error(), "compaction failed 2 times") {
		t.Fatal(did, err)
	}
	c.Cfg.CompactMaxFailures = 0 // the const: 3
	for i := 1; i <= 2; i++ {
		if did, err := c.Maybe(ctx, sess, fp, 2000, false); did || err != nil {
			t.Fatal(i, did, err)
		}
	}
	if _, err := c.Maybe(ctx, sess, fp, 2000, false); !strings.Contains(fmt.Sprint(err), "compaction failed 3 times") {
		t.Fatal(err)
	}
}

func TestCompactionMaxIneffectiveFromConfig(t *testing.T) {
	c, sess, fp := compactorWithSession(t, 80)
	c.Cfg.CompactMaxIneffective = 1
	var logs bytes.Buffer
	c.Log = slog.New(slog.NewTextHandler(&logs, nil))
	check := func() bool {
		t.Helper()
		stored, err := c.Store.Messages(ctx, sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		did, err := c.MaybeStored(ctx, sess, fp, 4000, false, stored, 3000)
		if err != nil {
			t.Fatal(err)
		}
		return did
	}
	if !check() {
		t.Fatal("the first compaction must run")
	}
	appendPlain(t, c.Store, sess.ID, 80, 40)
	if check() {
		t.Fatal("one strike must stop the automatic compaction with a limit of 1")
	}
	if !strings.Contains(logs.String(), "under the threshold one time in a row") {
		t.Fatal(logs.String())
	}
}

func TestTimesText(t *testing.T) {
	for n, want := range map[int]string{1: "one time", 2: "two times", 3: "3 times", 10: "10 times"} {
		if got := timesText(n); got != want {
			t.Errorf("timesText(%d) = %q, want %q", n, got, want)
		}
	}
}
