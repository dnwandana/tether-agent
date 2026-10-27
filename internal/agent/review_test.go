package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/ctxmgr"
	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// agentAfterTurn returns an Agent with the memory tool, run_command (a fake), and echo,
// a session after one turn, and the last request of that turn.
func agentAfterTurn(t *testing.T) (*Agent, *sessions.Session, provider.Request, namedTool) {
	t.Helper()
	m, err := memory.Open(t.TempDir(), 2200, 1375)
	if err != nil {
		t.Fatal(err)
	}
	cmd := newNamed("run_command", tools.SetTerminal)
	fp := scripted(toolCalls(tc("c1", "echo", `{"s":"x"}`)), text("Here is the answer."))
	a, sess := newAgent(t, fp, echoTool{}, tools.MemoryTool{Store: m}, cmd)
	a.Memory = m
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "I prefer Go. Run x.", Toolsets: all(), System: "SYS", Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	parentReq := fp.requests[len(fp.requests)-1]
	fp.requests = nil
	return a, sess, parentReq, cmd
}

func names(ts []provider.ToolSchema) []string {
	out := make([]string, len(ts))
	for i, s := range ts {
		out[i] = s.Name
	}
	return out
}

// reviewView returns the parent view of the session.
func reviewView(t *testing.T, a *Agent, sess *sessions.Session) []provider.Message {
	t.Helper()
	v, err := a.View(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestReviewUsesParentToolsAndSystem(t *testing.T) {
	a, sess, parentReq, _ := agentAfterTurn(t)
	fp := a.Prov.(*fakeProv)
	fp.script = []step{toolCalls(tc("r1", "memory", `{"action":"add","target":"user","content":"The user prefers Go"}`)), text("done")}
	var notified []string
	saved, err := a.RunReview(ctx, ReviewRequest{Session: sess, View: reviewView(t, a, sess), System: parentReq.System, Tools: parentReq.Tools,
		Memory: true, Notify: func(s string) { notified = append(notified, s) }})
	req := fp.requests[0]
	if err != nil || req.System != parentReq.System || !reflect.DeepEqual(names(req.Tools), names(parentReq.Tools)) {
		t.Fatal("cache-hit shape", err)
	}
	if !reflect.DeepEqual(req.Tools, parentReq.Tools) {
		t.Fatal("the tool schemas must be byte-identical")
	}
	// The request is the parent view plus the review prompt.
	n := len(parentReq.Messages)
	if len(req.Messages) != n+2 || !reflect.DeepEqual(req.Messages[:n], parentReq.Messages) {
		t.Fatal(len(req.Messages), n)
	}
	if last := req.Messages[n+1]; last.Role != provider.RoleUser || !strings.HasPrefix(last.Content, MemoryReviewPrompt) {
		t.Fatal(last.Content)
	}
	if len(saved) != 1 || saved[0] != "💾 User profile updated" || !reflect.DeepEqual(notified, saved) {
		t.Fatal(saved, notified)
	}
	if !strings.Contains(a.Memory.Profile(), "The user prefers Go") {
		t.Fatal("the review writes to memory")
	}
}

func TestReviewDispatchWhitelist(t *testing.T) {
	a, sess, parentReq, cmd := agentAfterTurn(t)
	fp := a.Prov.(*fakeProv)
	fp.script = []step{toolCalls(tc("r1", "run_command", `{"command":"rm -rf /tmp/x"}`)), text("done")}
	saved, err := a.RunReview(ctx, ReviewRequest{Session: sess, View: reviewView(t, a, sess), System: parentReq.System, Tools: parentReq.Tools, Skills: true})
	if err != nil || len(saved) != 0 {
		t.Fatal(saved, err)
	}
	if got := lastToolContent(fp.requests[1]); got != "review runs may only use memory, skill_view, skill_manage" {
		t.Fatal(got)
	}
	if cmd.seen.SessionID != "" {
		t.Fatal("the command must never run")
	}
}

func TestMainSessionUntouched(t *testing.T) {
	a, sess, parentReq, _ := agentAfterTurn(t)
	fp := a.Prov.(*fakeProv)
	fp.script = []step{toolCalls(tc("r1", "memory", `{"action":"add","target":"memory","content":"VPS = hetzner"}`)), text("done")}
	before := len(messages(t, a, sess.ID))
	if _, err := a.RunReview(ctx, ReviewRequest{Session: sess, View: reviewView(t, a, sess), System: parentReq.System, Tools: parentReq.Tools, Memory: true, Skills: true}); err != nil {
		t.Fatal(err)
	}
	if after := len(messages(t, a, sess.ID)); after != before {
		t.Fatal("the parent session changed", before, after)
	}
	rows := childSessions(t, a, sess.ID)
	if len(rows) != 1 || rows[0].Kind != "review" {
		t.Fatal(rows)
	}
	// The review session holds only its own messages, not a copy of the parent view.
	if n := len(messages(t, a, rows[0].ID)); n != 4 {
		t.Fatal("review messages", n)
	}
	if !strings.HasPrefix(fp.requests[0].Messages[len(fp.requests[0].Messages)-1].Content, CombinedReviewPrompt) {
		t.Fatal("memory and skills use the combined prompt")
	}
}

func TestReviewFocusRange(t *testing.T) {
	a, sess, parentReq, _ := agentAfterTurn(t)
	fp := a.Prov.(*fakeProv)
	fp.script = []step{text("Nothing to save.")}
	if _, err := a.RunReview(ctx, ReviewRequest{Session: sess, View: reviewView(t, a, sess), System: parentReq.System, Tools: parentReq.Tools,
		Memory: true, FocusRange: &[2]int{3, 60}}); err != nil {
		t.Fatal(err)
	}
	last := fp.requests[0].Messages[len(fp.requests[0].Messages)-1].Content
	if !strings.Contains(last, "messages 4 to 60") {
		t.Fatal(last)
	}
}

func TestSkillNotices(t *testing.T) {
	cases := map[string]string{
		`{"action":"create","name":"deploy-checklist"}`: "🧩 Skill created: deploy-checklist",
		`{"action":"patch","name":"deploy-checklist"}`:  "🧩 Skill updated: deploy-checklist",
		`{"action":"write_file","name":"deploy"}`:       "🧩 Skill updated: deploy",
		`{"action":"delete","name":"old"}`:              "🧩 Skill deleted: old",
	}
	for args, want := range cases {
		if got := reviewNotice(tc("c", "skill_manage", args), tools.Result{Text: "ok"}); got != want {
			t.Errorf("%s: %q, want %q", args, got, want)
		}
	}
	if got := reviewNotice(tc("c", "memory", `{"action":"add","target":"memory","content":"x"}`), tools.Result{Text: "ok"}); got != "💾 Memory updated" {
		t.Fatal(got)
	}
	if got := reviewNotice(tc("c", "memory", `{"action":"add","target":"memory"}`), tools.Result{Text: "bad", IsError: true}); got != "" {
		t.Fatal("a failed write gives no notice")
	}
	if got := reviewNotice(tc("c", "skill_view", `{"name":"x"}`), tools.Result{Text: "ok"}); got != "" {
		t.Fatal(got)
	}
}

func TestNoticesHeldUntilRelease(t *testing.T) {
	var sent []string
	notify, release := HoldNotices(func(s string) { sent = append(sent, s) })
	notify("💾 Memory updated")
	if len(sent) != 0 {
		t.Fatal("held before release")
	}
	release()
	if len(sent) != 1 || sent[0] != "💾 Memory updated" {
		t.Fatal(sent)
	}
	notify("🧩 Skill created: deploy-checklist")
	if len(sent) != 2 {
		t.Fatal("after release: send at once")
	}
	release()
	if len(sent) != 2 {
		t.Fatal("a second release sends nothing again")
	}
}

func TestNoticesKeepOrderUnderConcurrency(t *testing.T) {
	var (
		mu   sync.Mutex
		sent []string
	)
	notify, release := HoldNotices(func(s string) { mu.Lock(); sent = append(sent, s); mu.Unlock() })
	notify("a")
	notify("b")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); release() }()
	notify("c")
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 3 || sent[0] != "a" || sent[1] != "b" {
		t.Fatal(sent)
	}
}

func TestReviewReleasesApprovalSession(t *testing.T) {
	a, sess, parentReq, _ := agentAfterTurn(t)
	a.Prov.(*fakeProv).script = []step{text("nothing to save")}
	gate := &resetGate{}
	if _, err := a.RunReview(ctx, ReviewRequest{Session: sess, System: parentReq.System, Memory: true, ParentEnv: &tools.Env{Gate: gate}}); err != nil {
		t.Fatal(err)
	}
	kids := childSessions(t, a, sess.ID)
	if len(kids) != 1 || len(gate.reset) != 1 || gate.reset[0] != kids[0].ID {
		t.Fatal(gate.reset)
	}
}

func TestCancelledBackgroundStopsRunningReview(t *testing.T) {
	a, sess, parentReq, _ := agentAfterTurn(t)
	fp := a.Prov.(*fakeProv)
	fp.script = []step{{stall: true}}
	bg, stopBg := context.WithCancel(context.Background())
	defer stopBg()
	a.Background = bg
	// The end of the caller context must not stop the review.
	caller, endCaller := context.WithCancel(context.Background())
	a.startReview(caller, ReviewRequest{Session: sess, View: reviewView(t, a, sess), System: parentReq.System, Memory: true})
	endCaller()
	waitUntil(t, func() bool { return fp.reqCount() == 1 })

	start := time.Now()
	if a.WaitReviews(50 * time.Millisecond) {
		t.Fatal("WaitReviews must return false while a review runs")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("WaitReviews took %v; it must keep its bound", d)
	}
	stopBg()
	if !a.WaitReviews(5 * time.Second) {
		t.Fatal("a cancelled Background must stop the running review")
	}
}

func TestNoReviewAfterBackgroundEnds(t *testing.T) {
	a, sess, got := reviewAgent(t, scripted())
	bg, stopBg := context.WithCancel(context.Background())
	a.Background = bg
	stopBg()
	a.startReview(ctx, ReviewRequest{Session: sess, View: []provider.Message{}, Memory: true})
	if !a.WaitReviews(time.Second) {
		t.Fatal("no review must run")
	}
	if n := len(got()); n != 0 {
		t.Fatalf("%d reviews started after the end of Background", n)
	}
}

func TestNoReviewInsideReview(t *testing.T) {
	a, sess, parentReq, _ := agentAfterTurn(t)
	sess.Kind = "review"
	_, err := a.RunReview(ctx, ReviewRequest{Session: sess, System: parentReq.System, Memory: true})
	if err == nil || !strings.Contains(err.Error(), "no reviews inside review runs") {
		t.Fatal(err)
	}
}

// reviewAgent returns an Agent with the memory, skill_manage, and echo tools, and a
// recorder of the reviews that the triggers start. The recorder replaces RunReview.
func reviewAgent(t *testing.T, fp *fakeProv) (*Agent, *sessions.Session, func() []ReviewRequest) {
	t.Helper()
	m, err := memory.Open(t.TempDir(), 2200, 1375)
	if err != nil {
		t.Fatal(err)
	}
	a, sess := newAgent(t, fp, echoTool{}, tools.MemoryTool{Store: m}, newNamed("skill_manage", tools.SetSkills))
	a.Memory = m
	var (
		mu  sync.Mutex
		got []ReviewRequest
	)
	a.reviewHook = func(r ReviewRequest) { mu.Lock(); got = append(got, r); mu.Unlock() }
	return a, sess, func() []ReviewRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]ReviewRequest(nil), got...)
	}
}

// userTurn runs one turn with all toolsets.
func userTurn(t *testing.T, a *Agent, sess *sessions.Session, text string) TurnResult {
	t.Helper()
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: text, Toolsets: all(), System: "SYS", Sink: &recSink{}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// waitUntil fails the test when cond is not true within 5 seconds.
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal("the condition is not true after 5 s")
}

func TestMemoryReviewEvery10UserTurns(t *testing.T) {
	a, sess, reviews := reviewAgent(t, scriptedForever(text("ok")))
	var notices []string
	var mu sync.Mutex
	for i := 0; i < 9; i++ {
		res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "hi", Toolsets: all(), System: "SYS", Sink: &recSink{},
			Notify: func(s string) { mu.Lock(); notices = append(notices, s); mu.Unlock() }})
		if err != nil || res.Final != "ok" {
			t.Fatal(res, err)
		}
	}
	time.Sleep(20 * time.Millisecond)
	if n := len(reviews()); n != 0 {
		t.Fatal("not before 10:", n)
	}
	userTurn(t, a, sess, "hi")
	waitUntil(t, func() bool { return len(reviews()) == 1 })
	r := reviews()[0]
	if !r.Memory || r.Skills || r.Session.ID != sess.ID || r.System != "SYS" || len(r.Tools) != 3 {
		t.Fatalf("%+v", r)
	}
	// The view is the full parent view, with the last answer.
	if last := r.View[len(r.View)-1]; last.Role != provider.RoleAssistant || last.Content != "ok" || len(r.View) != 20 {
		t.Fatal(len(r.View), last)
	}
	// The counter starts again after the review.
	for i := 0; i < 9; i++ {
		userTurn(t, a, sess, "hi")
	}
	time.Sleep(20 * time.Millisecond)
	if n := len(reviews()); n != 1 {
		t.Fatal("the counter must reset:", n)
	}
}

func TestMemoryCounterSurvivesRestart(t *testing.T) {
	a, sess, reviews := reviewAgent(t, scriptedForever(text("ok")))
	seedHistory(t, a, sess.ID, 16) // 8 user turns before this process
	userTurn(t, a, sess, "hi")
	time.Sleep(20 * time.Millisecond)
	if len(reviews()) != 0 {
		t.Fatal("turn 9 must not review")
	}
	userTurn(t, a, sess, "hi")
	waitUntil(t, func() bool { return len(reviews()) == 1 })
}

func TestMemoryToolUseResetsCounter(t *testing.T) {
	a, sess, reviews := reviewAgent(t, scriptedForever(text("ok")))
	for i := 0; i < 9; i++ {
		userTurn(t, a, sess, "hi")
	}
	fp := a.Prov.(*fakeProv)
	fp.script = []step{toolCalls(tc("m1", "memory", `{"action":"add","target":"memory","content":"x"}`)), text("saved")}
	userTurn(t, a, sess, "remember x")
	time.Sleep(20 * time.Millisecond)
	if len(reviews()) != 0 {
		t.Fatal("a turn that used memory resets the memory counter")
	}
}

func TestSkillReviewEvery10ToolRounds(t *testing.T) {
	fp := scripted()
	for i := 0; i < 10; i++ {
		fp.script = append(fp.script, toolCalls(tc(fmt.Sprint("c", i), "echo", `{"s":"x"}`)))
	}
	fp.script = append(fp.script, text("done"))
	a, sess, reviews := reviewAgent(t, fp)
	if res := userTurn(t, a, sess, "work"); res.ToolRounds != 10 {
		t.Fatal(res.ToolRounds)
	}
	waitUntil(t, func() bool { return len(reviews()) == 1 })
	if r := reviews()[0]; !r.Skills || r.Memory {
		t.Fatalf("%+v", r)
	}
}

func TestMemoryAndSkillReviewDueTogetherRunAsOne(t *testing.T) {
	a, sess, reviews := reviewAgent(t, scriptedForever(text("ok")))
	for i := 0; i < 9; i++ {
		userTurn(t, a, sess, "hi")
	}
	fp := a.Prov.(*fakeProv)
	for i := 0; i < 10; i++ {
		fp.script = append(fp.script, toolCalls(tc(fmt.Sprint("c", i), "echo", `{"s":"x"}`)))
	}
	fp.script = append(fp.script, text("done"))
	userTurn(t, a, sess, "work")
	waitUntil(t, func() bool { return len(reviews()) == 1 })
	time.Sleep(20 * time.Millisecond)
	if r := reviews(); len(r) != 1 || !r[0].Memory || !r[0].Skills {
		t.Fatalf("%+v", r)
	}
}

func TestNoReviewWithoutTheTools(t *testing.T) {
	a, sess := newAgent(t, scriptedForever(text("ok")), echoTool{})
	var n int
	a.reviewHook = func(ReviewRequest) { n++ }
	for i := 0; i < 12; i++ {
		userTurn(t, a, sess, "hi")
	}
	time.Sleep(20 * time.Millisecond)
	if n != 0 {
		t.Fatal("no memory tool, so no memory review")
	}
}

func TestNoReviewForCronOrChild(t *testing.T) {
	for _, kind := range []string{"cron", KindSubagent, KindReview} {
		a, _, reviews := reviewAgent(t, scriptedForever(text("ok")))
		sess, err := a.Store.Create(ctx, sessions.Session{Key: "k", Kind: kind})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 10; i++ {
			userTurn(t, a, sess, "hi")
		}
		a.ReviewBeforeNew(ctx, sess)
		time.Sleep(20 * time.Millisecond)
		if n := len(reviews()); n != 0 {
			t.Fatal(kind, n)
		}
	}
}

func TestNoReviewAfterError(t *testing.T) {
	a, sess, reviews := reviewAgent(t, scriptedForever(text("ok")))
	for i := 0; i < 9; i++ {
		userTurn(t, a, sess, "hi")
	}
	a.Prov.(*fakeProv).script = []step{{err: errors.New("boom")}}
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "hi", Toolsets: all(), System: "SYS"}); err == nil {
		t.Fatal("want an error")
	}
	time.Sleep(20 * time.Millisecond)
	if len(reviews()) != 0 {
		t.Fatal("a turn with no answer starts no review")
	}
	userTurn(t, a, sess, "hi")
	waitUntil(t, func() bool { return len(reviews()) == 1 })
}

func TestCompactionReviewNeeds6UserTurnsAndIsBackground(t *testing.T) {
	a, sess, reviews := reviewAgent(t, scriptedForever(text("ok")))
	seedHistory(t, a, sess.ID, 10) // 5 user turns
	view := reviewView(t, a, sess)
	a.ReviewOnCompaction(ctx, sess, view, 3, 8)
	time.Sleep(20 * time.Millisecond)
	if len(reviews()) != 0 {
		t.Fatal("5 user turns: no review")
	}
	seedHistory(t, a, sess.ID, 2) // 6 user turns
	view = reviewView(t, a, sess)
	a.ReviewOnCompaction(ctx, sess, view, 3, 8)
	waitUntil(t, func() bool { return len(reviews()) == 1 })
	r := reviews()[0]
	if !r.Memory || !r.Skills || r.FocusRange == nil || *r.FocusRange != [2]int{3, 8} || len(r.View) != 12 {
		t.Fatalf("%+v", r)
	}
	// A slow review does not block the compaction: Maybe returns first.
	release := make(chan struct{})
	done := make(chan struct{})
	a.reviewHook = func(ReviewRequest) { <-release; close(done) }
	a.Compactor = &ctxmgr.Compactor{Store: a.Store, DB: a.DB, Cfg: a.Cfg.Context, OnCompact: a.ReviewOnCompaction}
	seedHistory(t, a, sess.ID, 40)
	fp := a.Prov.(*fakeProv)
	fp.script = []step{text("SUMMARY")}
	did, err := a.Compactor.Maybe(ctx, sess, fp, 2000, true)
	if err != nil || !did {
		t.Fatal(did, err)
	}
	select {
	case <-done:
		t.Fatal("the review must not finish before Maybe returns")
	default:
	}
	close(release)
	<-done
}

func TestReviewBeforeNewIsBackground(t *testing.T) {
	a, sess, _ := reviewAgent(t, scriptedForever(text("ok")))
	for i := 0; i < 6; i++ {
		userTurn(t, a, sess, "hi")
	}
	release := make(chan struct{})
	got := make(chan ReviewRequest, 1)
	a.reviewHook = func(r ReviewRequest) { <-release; got <- r }
	a.ReviewBeforeNew(ctx, sess) // returns at once
	close(release)
	r := <-got
	if !r.Memory || !r.Skills || r.FocusRange != nil || r.System != "SYS" || len(r.View) != 12 {
		t.Fatalf("%+v", r)
	}
}

func TestTriggeredReviewRunsAndNotifies(t *testing.T) {
	a, sess, _ := reviewAgent(t, scriptedForever(text("ok")))
	a.reviewHook = nil
	for i := 0; i < 9; i++ {
		userTurn(t, a, sess, "hi")
	}
	fp := a.Prov.(*fakeProv)
	fp.script = []step{text("answer"), toolCalls(tc("r1", "memory", `{"action":"add","target":"user","content":"likes Go"}`)), text("done")}
	var mu sync.Mutex
	var notices []string
	notify, release := HoldNotices(func(s string) { mu.Lock(); notices = append(notices, s); mu.Unlock() })
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "hi", Toolsets: all(), System: "SYS", Notify: notify}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return len(childSessions(t, a, sess.ID)) == 1 })
	release()
	waitUntil(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(notices) == 1 })
	if notices[0] != "💾 User profile updated" || !strings.Contains(a.Memory.Profile(), "likes Go") {
		t.Fatal(notices)
	}
}

func TestCompactNowForcesAndCounts(t *testing.T) {
	fp := scripted(text("SUM"))
	a, sess := newAgent(t, fp, echoTool{})
	a.Compactor = &ctxmgr.Compactor{Store: a.Store, DB: a.DB, Cfg: a.Cfg.Context}
	seedHistory(t, a, sess.ID, 10)
	if n, err := a.CompactNow(ctx, sess); err != nil || n != 0 {
		t.Fatal("too short:", n, err)
	}
	seedHistory(t, a, sess.ID, 20) // 30 messages, far below the threshold
	n, err := a.CompactNow(ctx, sess)
	if err != nil || n != 7 {
		t.Fatal(n, err)
	}
	if v, _ := a.View(ctx, sess.ID); len(v) != 3+1+20 {
		t.Fatal(len(v))
	}
	fp.script = []step{{err: errors.New("boom")}}
	seedHistory(t, a, sess.ID, 4)
	if _, err := a.CompactNow(ctx, sess); err == nil {
		t.Fatal("a failed summary must be an error")
	}
	a.Compactor = nil
	if _, err := a.CompactNow(ctx, sess); err == nil {
		t.Fatal("no compactor")
	}
}
