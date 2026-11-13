package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/ctxmgr"
	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// agentAfterTurn returns an Agent with the memory tool, terminal (a fake), and echo,
// a session after one turn, and the last request of that turn.
func agentAfterTurn(t *testing.T) (*Agent, *sessions.Session, provider.Request, namedTool) {
	t.Helper()
	m, err := memory.Open(t.TempDir(), 2200, 1375)
	if err != nil {
		t.Fatal(err)
	}
	cmd := newNamed("terminal", tools.SetTerminal)
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
	// The review run sends the prompt cache key of the parent.
	if req.CacheKey != sess.ID || parentReq.CacheKey != sess.ID {
		t.Fatal(req.CacheKey, parentReq.CacheKey, sess.ID)
	}
	// The request is the parent view plus the review prompt.
	n := len(parentReq.Messages)
	if len(req.Messages) != n+2 || !reflect.DeepEqual(req.Messages[:n], parentReq.Messages) {
		t.Fatal(len(req.Messages), n)
	}
	if last := req.Messages[n+1]; last.Role != provider.RoleUser || !strings.HasPrefix(last.Content, MemoryReviewPrompt) {
		t.Fatal(last.Content)
	}
	if len(saved) != 1 || saved[0] != "💾 User profile updated: `The user prefers Go`" || !reflect.DeepEqual(notified, saved) {
		t.Fatal(saved, notified)
	}
	if !strings.Contains(a.Memory.Profile(), "The user prefers Go") {
		t.Fatal("the review writes to memory")
	}
}

func TestReviewSendsOneSummary(t *testing.T) {
	fp := scripted(text("ok"))
	a, sess, _ := reviewAgent(t, fp)
	userTurn(t, a, sess, "hi")
	fp.script = []step{toolCalls(
		tc("r1", "skill_manage", `{"action":"patch","name":"a"}`),
		tc("r2", "skill_manage", `{"action":"write_file","name":"a","file":"references/x.md","content":"x"}`),
		tc("r3", "skill_manage", `{"action":"patch","name":"b"}`),
		tc("r4", "memory", `{"action":"add","target":"memory","content":"x"}`),
	), text("done")}
	var notified []string
	saved, err := a.RunReview(ctx, ReviewRequest{Session: sess, System: "SYS", Memory: true, Skills: true,
		Notify: func(s string) { notified = append(notified, s) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 3 {
		t.Fatal("one notice for each distinct change:", saved)
	}
	want := "🧩 Skills updated: a, b\n💾 Memory updated: `x`"
	if len(notified) != 1 || notified[0] != want {
		t.Fatalf("want one notify call with %q, got %q", want, notified)
	}
}

func TestReviewFailureStillNotifies(t *testing.T) {
	fp := scripted(text("ok"))
	a, sess, _ := reviewAgent(t, fp)
	userTurn(t, a, sess, "hi")
	fp.script = []step{toolCalls(tc("r1", "skill_manage", `{"action":"patch","name":"a"}`)), {err: errors.New("boom")}}
	var notified []string
	saved, err := a.RunReview(ctx, ReviewRequest{Session: sess, System: "SYS", Skills: true,
		Notify: func(s string) { notified = append(notified, s) }})
	if err == nil {
		t.Fatal("want the error of the provider")
	}
	if len(saved) != 1 || len(notified) != 1 || notified[0] != "🧩 Skill updated: a" {
		t.Fatal("a failed review sends the changes that it saved:", saved, notified)
	}
}

func TestReviewDispatchWhitelist(t *testing.T) {
	a, sess, parentReq, cmd := agentAfterTurn(t)
	fp := a.Prov.(*fakeProv)
	fp.script = []step{toolCalls(tc("r1", "terminal", `{"command":"rm -rf /tmp/x"}`)), text("done")}
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
	if got := reviewNotice(tc("c", "memory", `{"action":"add","target":"memory","content":"x"}`), tools.Result{Text: "ok"}); got != "💾 Memory updated: `x`" {
		t.Fatal(got)
	}
	if got := reviewNotice(tc("c", "memory", `{"action":"add","target":"memory"}`), tools.Result{Text: "bad", IsError: true}); got != "" {
		t.Fatal("a failed write gives no notice")
	}
	if got := reviewNotice(tc("c", "skill_view", `{"name":"x"}`), tools.Result{Text: "ok"}); got != "" {
		t.Fatal(got)
	}
}

// TestMemoryNotices runs the real memory tool through Registry.Dispatch, so the notice
// follows the result texts of the store and not a copy of them.
func TestMemoryNotices(t *testing.T) {
	// long is too large for tier 1 with the limits of the test (2200 and 1375), but not
	// too large for one entry.
	long := strings.Repeat("y", 2300)
	// mid fits in tier 1, but it is longer than the old cut of 120 runes.
	mid := strings.Repeat("é", 500)
	cases := []struct {
		name  string
		setup []string // the memory calls before the call under test
		args  string
		want  string
	}{
		{"add to memory", nil,
			`{"action":"add","target":"memory","content":"VPS = hetzner"}`, "💾 Memory updated: `VPS = hetzner`"},
		{"add to user", nil,
			`{"action":"add","target":"user","content":"The user prefers Go"}`, "💾 User profile updated: `The user prefers Go`"},
		{"replace in memory", []string{`{"action":"add","target":"memory","content":"VPS = hetzner"}`},
			`{"action":"replace","target":"memory","old_text":"hetzner","content":"VPS = ovh"}`, "💾 Memory updated: `VPS = ovh`"},
		{"remove from user", []string{`{"action":"add","target":"user","content":"The user prefers Go"}`},
			`{"action":"remove","target":"user","old_text":"Go"}`, "💾 User profile updated (removed): `Go`"},
		{"add with a topic", nil,
			`{"action":"add","target":"memory","topic":"rpc","content":"QuickNode free plan"}`, "💾 Memory note updated: rpc"},
		{"add spill", nil,
			`{"action":"add","target":"memory","content":"` + long + `"}`, "💾 Memory note updated: inbox-memory"},
		{"add spill to user", nil,
			`{"action":"add","target":"user","content":"` + long + `"}`, "💾 Memory note updated: inbox-user"},
		{"add with a topic that has no letters", nil,
			`{"action":"add","target":"memory","topic":"!!!","content":"QuickNode free plan"}`, "💾 Memory note updated: inbox-memory"},
		{"replace in a note", []string{`{"action":"add","target":"memory","topic":"rpc","content":"QuickNode free plan"}`},
			`{"action":"replace","target":"memory","topic":"rpc","old_text":"QuickNode","content":"QuickNode paid plan"}`, "💾 Memory note updated: rpc"},
		{"replace in a note with no topic", []string{`{"action":"add","target":"memory","topic":"rpc","content":"QuickNode free plan"}`},
			`{"action":"replace","target":"memory","old_text":"QuickNode","content":"QuickNode paid plan"}`, "💾 Memory note updated: rpc"},
		{"remove in a note", []string{
			`{"action":"add","target":"memory","topic":"rpc","content":"QuickNode free plan"}`,
			`{"action":"add","target":"memory","topic":"rpc","content":"Helius RPC: 1M credits/day"}`},
			`{"action":"remove","target":"memory","topic":"rpc","old_text":"QuickNode"}`, "💾 Memory note updated: rpc"},
		{"remove the last entry of a note", []string{`{"action":"add","target":"memory","topic":"rpc","content":"QuickNode free plan"}`},
			`{"action":"remove","target":"memory","old_text":"QuickNode"}`, "💾 Memory note updated: rpc"},
		{"replace spill", []string{`{"action":"add","target":"user","content":"The user prefers Go"}`},
			`{"action":"replace","target":"user","old_text":"Go","content":"` + long + `"}`, "💾 Memory note updated: inbox-user"},
		{"replace spill in memory", []string{`{"action":"add","target":"memory","content":"VPS = hetzner"}`},
			`{"action":"replace","target":"memory","old_text":"hetzner","content":"` + long + `"}`, "💾 Memory note updated: inbox-memory"},
		{"already exists in memory", []string{`{"action":"add","target":"memory","content":"VPS = hetzner"}`},
			`{"action":"add","target":"memory","content":"VPS = hetzner"}`, ""},
		{"already exists in a note", []string{`{"action":"add","target":"memory","topic":"rpc","content":"QuickNode free plan"}`},
			`{"action":"add","target":"memory","topic":"rpc","content":"QuickNode free plan"}`, ""},
		{"already exists in the inbox", []string{`{"action":"add","target":"memory","content":"` + long + `"}`},
			`{"action":"add","target":"memory","content":"` + long + `"}`, ""},
		{"search", []string{`{"action":"add","target":"memory","topic":"rpc","content":"QuickNode free plan"}`},
			`{"action":"search","query":"QuickNode"}`, ""},
		{"read", []string{`{"action":"add","target":"memory","topic":"rpc","content":"QuickNode free plan"}`},
			`{"action":"read","topic":"rpc"}`, ""},
		{"add 500 runes to memory", nil,
			`{"action":"add","target":"memory","content":"` + mid + `"}`, "💾 Memory updated: `" + mid + "`"},
		{"add 500 runes to the user profile", nil,
			`{"action":"add","target":"user","content":"` + mid + `"}`, "💾 User profile updated: `" + mid + "`"},
		{"add 500 runes to a note", nil,
			`{"action":"add","target":"memory","topic":"rpc","content":"` + mid + `"}`, "💾 Memory note updated: rpc"},
		{"add a hidden rune", nil,
			`{"action":"add","target":"memory","content":"a\u200bb"}`, ""},
		{"failed add", nil,
			`{"action":"add","target":"memory"}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := memory.Open(t.TempDir(), 2200, 1375)
			if err != nil {
				t.Fatal(err)
			}
			reg := tools.NewRegistry()
			reg.Register(tools.MemoryTool{Store: m})
			run := func(args string) tools.Result {
				return reg.Dispatch(ctx, "memory", json.RawMessage(args), nil, map[string]bool{"memory": true})
			}
			for _, s := range c.setup {
				if res := run(s); res.IsError {
					t.Fatalf("setup %s: %s", s, res.Text)
				}
			}
			res := run(c.args)
			if got := reviewNotice(tc("c", "memory", c.args), res); got != c.want {
				t.Errorf("notice %q, want %q; the result is %q", got, c.want, res.Text)
			}
		})
	}
}

func TestMemoryUseRule(t *testing.T) {
	cases := map[string]bool{
		`{"action":"add","target":"memory"}`:   true,
		`{"action":"replace","target":"user"}`: true,
		`{"action":"remove"}`:                  true,
		`{"action":"search","query":"rpc"}`:    false,
		`{"action":"read","topic":"rpc"}`:      false,
		`{}`:                                   true,
		`{"action":`:                           true,
	}
	for args, want := range cases {
		if got := memoryUse(tc("c", "memory", args)); got != want {
			t.Errorf("%s: %v, want %v", args, got, want)
		}
	}
	if memoryUse(tc("c", "skill_manage", `{"action":"create"}`)) {
		t.Fatal("only a memory call is memory use")
	}
}

func TestReviewSummary(t *testing.T) {
	cases := []struct {
		name  string
		saved []string
		want  string
	}{
		{"none", nil, ""},
		{"one skill", []string{"🧩 Skill updated: vps-services"}, "🧩 Skill updated: vps-services"},
		{"one memory", []string{"💾 Memory updated"}, "💾 Memory updated"},
		{"plural and memory",
			[]string{"🧩 Skill updated: a", "🧩 Skill updated: b", "💾 Memory updated"},
			"🧩 Skills updated: a, b\n💾 Memory updated"},
		{"order of first notice",
			[]string{"💾 User profile updated", "🧩 Skill created: c", "🧩 Skill updated: a", "🧩 Skill updated: b"},
			"💾 User profile updated\n🧩 Skill created: c\n🧩 Skills updated: a, b"},
		{"singular and plural deleted",
			[]string{"🧩 Skill updated: a", "🧩 Skill deleted: d", "🧩 Skill deleted: e"},
			"🧩 Skill updated: a\n🧩 Skills deleted: d, e"},
		{"created then updated",
			[]string{"🧩 Skill created: x", "🧩 Skill updated: x"},
			"🧩 Skill created: x\n🧩 Skill updated: x"},
		{"group after memory keeps its first place",
			[]string{"🧩 Skill updated: a", "💾 Memory updated", "🧩 Skill updated: b"},
			"🧩 Skills updated: a, b\n💾 Memory updated"},
		{"four updates (the screenshot)",
			[]string{"🧩 Skill updated: crypto-gainer-wallet-reports", "🧩 Skill updated: solana-launch-sniping",
				"🧩 Skill updated: subagent-driven-development", "🧩 Skill updated: vps-services"},
			"🧩 Skills updated: crypto-gainer-wallet-reports, solana-launch-sniping, subagent-driven-development, vps-services"},
	}
	for _, c := range cases {
		if got := reviewSummary(c.saved); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
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

func TestReviewLimitSummary(t *testing.T) {
	a, sess, parentReq, _ := agentAfterTurn(t)
	fp := a.Prov.(*fakeProv)
	steps := make([]step, 0, reviewMaxTurns+1)
	for i := range reviewMaxTurns {
		steps = append(steps, toolCalls(tc(fmt.Sprintf("r%d", i+1), "memory", `{"action":"add","target":"memory","content":"VPS = hetzner"}`)))
	}
	fp.script = append(steps, text("Saved the VPS fact."))
	if _, err := a.RunReview(ctx, ReviewRequest{Session: sess, View: reviewView(t, a, sess), System: parentReq.System, Tools: parentReq.Tools, Memory: true}); err != nil {
		t.Fatal("a review at the limit must end without an error:", err)
	}
	if len(fp.requests) != reviewMaxTurns+1 {
		t.Fatal(len(fp.requests))
	}
	last := fp.requests[reviewMaxTurns]
	if m := last.Messages[len(last.Messages)-1]; m.Role != provider.RoleUser || m.Content != NoteSummaryRequest {
		t.Fatalf("%+v", m)
	}
	if last.System != parentReq.System || !reflect.DeepEqual(last.Tools, parentReq.Tools) {
		t.Fatal("the summary request of a review keeps the parent shape")
	}
	rows := childSessions(t, a, sess.ID)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	msgs := messages(t, a, rows[0].ID)
	if hasNudge(msgs) {
		t.Fatal("the nudge must not be saved")
	}
	if end := msgs[len(msgs)-1].Msg; end.Role != provider.RoleAssistant || end.Content != "Saved the VPS fact." {
		t.Fatalf("%+v", end)
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
	checkMemoryReset(t, a, sess, reviews)
}

// memoryCount returns the user turns that the memory review of the session counted.
func memoryCount(a *Agent, id string) int {
	return a.Reviews.shape(id).userTurns
}

// checkMemoryReset fails the test when the last turn did not reset the memory counter.
// The turn is turn 10, so a counter that did not reset starts a review. RunTurn calls
// goReview before it returns, so WaitReviews also waits for that review.
func checkMemoryReset(t *testing.T, a *Agent, sess *sessions.Session, reviews func() []ReviewRequest) {
	t.Helper()
	if !a.WaitReviews(5 * time.Second) {
		t.Fatal("a review did not end")
	}
	if n := len(reviews()); n != 0 {
		t.Fatal("the turn must reset the memory counter, but a review started:", n)
	}
	if n := memoryCount(a, sess.ID); n != 0 {
		t.Fatal("the memory counter after the reset:", n)
	}
	userTurn(t, a, sess, "hi")
	if n := memoryCount(a, sess.ID); n != 1 {
		t.Fatal("the memory counter after the next turn:", n)
	}
}

func TestMemorySearchDoesNotResetCounter(t *testing.T) {
	a, sess, reviews := reviewAgent(t, scriptedForever(text("ok")))
	for range 9 {
		userTurn(t, a, sess, "hi")
	}
	fp := a.Prov.(*fakeProv)
	fp.script = []step{toolCalls(tc("m1", "memory", `{"action":"search","query":"rpc"}`),
		tc("m2", "memory", `{"action":"read","topic":"rpc"}`)), text("found")}
	userTurn(t, a, sess, "find rpc")
	waitUntil(t, func() bool { return len(reviews()) == 1 })
}

func TestFailedMemoryAddResetsCounter(t *testing.T) {
	a, sess, reviews := reviewAgent(t, scriptedForever(text("ok")))
	for range 9 {
		userTurn(t, a, sess, "hi")
	}
	fp := a.Prov.(*fakeProv)
	fp.script = []step{toolCalls(tc("m1", "memory", `{"action":"add","target":"memory"}`)), text("failed")}
	userTurn(t, a, sess, "remember")
	checkMemoryReset(t, a, sess, reviews)
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
	if notices[0] != "💾 User profile updated: `likes Go`" || !strings.Contains(a.Memory.Profile(), "likes Go") {
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

func TestNoticeQuote(t *testing.T) {
	long := strings.Repeat("é", 500)
	cases := []struct{ action, text, want string }{
		{"add", "", ""},
		{"add", "  a\n  b ", ": `a b`"},
		{"add", "run `rm`", ": `run 'rm'`"},
		{"remove", "Go", " (removed): `Go`"},
		// A tier-1 write goes into the system prompt, so the notice shows the full text.
		{"add", long, ": `" + long + "`"},
		// A hidden rune shows as a mark.
		{"add", "VPS\u200b = ovh", ": `VPS<U+200B> = ovh`"},
		{"remove", "a\u202eb", " (removed): `a<U+202E>b`"},
		{"add", "tag\U000E0041", ": `tag<U+E0041>`"},
	}
	for _, c := range cases {
		if got := noticeQuote(c.action, c.text); got != c.want {
			t.Errorf("%q %q: %q, want %q", c.action, c.text, got, c.want)
		}
	}
}

func TestReviewPromptMarksToolTextAsData(t *testing.T) {
	for _, p := range []string{reviewPrompt(true, false, nil), reviewPrompt(false, true, nil), reviewPrompt(true, true, &[2]int{0, 4})} {
		if !strings.Contains(p, reviewProvenanceNote) || !strings.HasSuffix(p, reviewToolNote) {
			t.Fatal(p)
		}
	}
}

func TestNoReviewSessionWhenTheProviderIsDown(t *testing.T) {
	a, sess, parentReq, _ := agentAfterTurn(t)
	health := provider.NewHealth(nil)
	chain := provider.NewChain([]provider.ChainEntry{{Name: "primary", P: failProv{"primary", "m"}}}, health, slog.New(slog.DiscardHandler))
	if _, err := chain.Stream(ctx, provider.Request{}); err == nil {
		t.Fatal("the call must fail")
	}
	a.Prov = chain
	_, err := a.RunReview(ctx, ReviewRequest{Session: sess, System: parentReq.System, Memory: true})
	if !errors.Is(err, ErrReviewProviderDown) {
		t.Fatal(err)
	}
	if kids := childSessions(t, a, sess.ID); len(kids) != 0 {
		t.Fatal("a review with no provider must not make a session:", len(kids))
	}
}

// A due review does not start while each provider entry is down, and it stays due. It
// starts after the next turn when the provider works again.
func TestDueReviewWaitsWhileTheProviderIsDown(t *testing.T) {
	a, sess, reviews := reviewAgent(t, scriptedForever(text("ok")))
	for range 9 {
		userTurn(t, a, sess, "hi")
	}
	up := a.Prov
	chain := provider.NewChain([]provider.ChainEntry{{Name: "primary", P: failProv{"primary", "m"}}},
		provider.NewHealth(nil), slog.New(slog.DiscardHandler))
	if _, err := chain.Stream(ctx, provider.Request{}); err == nil {
		t.Fatal("the call must fail")
	}
	a.Prov = chain
	req := provider.Request{System: "SYS", Tools: []provider.ToolSchema{{Name: "memory"}}}
	a.MaybeStartReviews(ctx, sess, req, TurnResult{Final: "ok"}, nil)
	time.Sleep(20 * time.Millisecond)
	if n := len(reviews()); n != 0 {
		t.Fatal("no review while the provider is down:", n)
	}
	a.Prov = up
	a.MaybeStartReviews(ctx, sess, req, TurnResult{Final: "ok"}, nil)
	waitUntil(t, func() bool { return len(reviews()) == 1 })
}

func TestBackgroundReviewsRunAtMostTwoAtATime(t *testing.T) {
	a := &Agent{}
	var running, peak atomic.Int32
	release := make(chan struct{})
	for range 5 {
		a.goReview(ctx, func(context.Context) {
			n := running.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			<-release
			running.Add(-1)
		})
	}
	waitUntil(t, func() bool { return running.Load() == maxConcurrentReviews })
	time.Sleep(50 * time.Millisecond)
	if p := peak.Load(); p != maxConcurrentReviews {
		t.Fatal("peak", p)
	}
	close(release)
	if !a.WaitReviews(5 * time.Second) {
		t.Fatal("the waiting reviews must run after the first ones end")
	}
}

func TestReviewRoundsFromConfig(t *testing.T) {
	a, sess, parentReq, _ := agentAfterTurn(t)
	a.Cfg.Review.MaxRounds = 2
	fp := a.Prov.(*fakeProv)
	fp.script = []step{
		toolCalls(tc("r1", "memory", `{"action":"add","target":"memory","content":"VPS = hetzner"}`)),
		toolCalls(tc("r2", "memory", `{"action":"add","target":"memory","content":"DNS = cloudflare"}`)),
		text("Saved two facts."),
	}
	if _, err := a.RunReview(ctx, ReviewRequest{Session: sess, View: reviewView(t, a, sess), System: parentReq.System, Tools: parentReq.Tools, Memory: true}); err != nil {
		t.Fatal(err)
	}
	if len(fp.requests) != 3 {
		t.Fatal("2 rounds and the summary call:", len(fp.requests))
	}
	last := fp.requests[2]
	if m := last.Messages[len(last.Messages)-1]; m.Role != provider.RoleUser || m.Content != NoteSummaryRequest {
		t.Fatalf("%+v", m)
	}
}

func TestReviewConcurrencyFromConfig(t *testing.T) {
	cfg := config.Default()
	cfg.Review.MaxConcurrent = 3
	a := &Agent{Cfg: &cfg}
	var running, peak atomic.Int32
	release := make(chan struct{})
	for range 6 {
		a.goReview(ctx, func(context.Context) {
			n := running.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			<-release
			running.Add(-1)
		})
	}
	waitUntil(t, func() bool { return running.Load() == 3 })
	time.Sleep(50 * time.Millisecond)
	if p := peak.Load(); p != 3 {
		t.Fatal("peak", p)
	}
	close(release)
	if !a.WaitReviews(5 * time.Second) {
		t.Fatal("the waiting reviews must run after the first ones end")
	}
}

func TestReviewMinUserTurnsFromConfig(t *testing.T) {
	a, sess, reviews := reviewAgent(t, scriptedForever(text("ok")))
	a.Cfg.Review.MinUserTurns = 3
	seedHistory(t, a, sess.ID, 4) // 2 user turns
	a.ReviewOnCompaction(ctx, sess, reviewView(t, a, sess), 1, 3)
	time.Sleep(20 * time.Millisecond)
	if len(reviews()) != 0 {
		t.Fatal("2 user turns: no review")
	}
	seedHistory(t, a, sess.ID, 2) // 3 user turns
	a.ReviewOnCompaction(ctx, sess, reviewView(t, a, sess), 1, 3)
	waitUntil(t, func() bool { return len(reviews()) == 1 })
}

func TestReviewLimitsZeroKeepConsts(t *testing.T) {
	a := &Agent{Cfg: &config.Config{}}
	if a.reviewRounds() != reviewMaxTurns || a.reviewConcurrency() != maxConcurrentReviews || a.reviewMinTurns() != reviewMinUserTurns {
		t.Fatal(a.reviewRounds(), a.reviewConcurrency(), a.reviewMinTurns())
	}
}
