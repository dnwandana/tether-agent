package security

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// judgeProvider is a fake provider. It answers each call with reply, or
// fails with err. It keeps the last request.
type judgeProvider struct {
	reply string
	err   error
	usage provider.Usage

	mu   sync.Mutex
	last provider.Request
	dl   bool
}

func (p *judgeProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	p.mu.Lock()
	p.last = req
	_, p.dl = ctx.Deadline()
	p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	ch := make(chan provider.Event, 2)
	ch <- provider.Event{Type: provider.EventText, Delta: p.reply}
	ch <- provider.Event{Type: provider.EventDone, FinishReason: "stop", Usage: &p.usage}
	close(ch)
	return ch, nil
}

func (p *judgeProvider) Name() string  { return "fake" }
func (p *judgeProvider) Model() string { return "fake-model" }

func replyProvider(reply string) *judgeProvider { return &judgeProvider{reply: reply} }

func errProvider() *judgeProvider { return &judgeProvider{err: errors.New("provider down")} }

// streamErrProvider fails inside the stream.
type streamErrProvider struct{ judgeProvider }

func (p *streamErrProvider) Stream(context.Context, provider.Request) (<-chan provider.Event, error) {
	ch := make(chan provider.Event, 1)
	ch <- provider.Event{Type: provider.EventError, Err: errors.New("stream cut")}
	close(ch)
	return ch, nil
}

func TestJudgeLowRiskApproves(t *testing.T) {
	prov := replyProvider("APPROVE: deleting a temp folder the agent created")
	j := ProviderJudge{Prov: prov}
	risky, err := j.Judge(ctx, "rm -rf /tmp/scrape-old", "recursive delete", "s1")
	if err != nil || risky {
		t.Fatal(risky, err)
	}
	// The call has no tools and one user message with the command and the reason.
	if len(prov.last.Tools) != 0 || len(prov.last.Messages) != 1 || prov.last.Messages[0].Role != provider.RoleUser {
		t.Fatalf("%+v", prov.last)
	}
	msg := prov.last.Messages[0].Content
	if !strings.Contains(msg, "Command: rm -rf /tmp/scrape-old") || !strings.Contains(msg, "Flagged reason: recursive delete") {
		t.Fatal(msg)
	}
	if !prov.dl {
		t.Fatal("the judge call has no deadline")
	}
}

func TestJudgeHighRiskAsks(t *testing.T) {
	risky, _ := ProviderJudge{Prov: replyProvider("ESCALATE: deletes user data")}.Judge(ctx, "rm -rf ~/projects", "recursive delete", "s1")
	if !risky {
		t.Fatal()
	}
}

func TestJudgeAnswerForms(t *testing.T) {
	for reply, want := range map[string]bool{
		"APPROVE":                      false,
		"  approve: benign  \n":        false,
		"**APPROVE**: safe":            false,
		"`APPROVE: safe`":              false,
		"APPROVE - safe":               false,
		"APPROVED: safe":               true,
		"DENY: wipes the disk":         true,
		"ESCALATE":                     true,
		"I would APPROVE: this":        true,
		"":                             true,
		"APPROVE: ok\nESCALATE: wait":  true,
		"ESCALATE: unsure\nAPPROVE: x": true,
	} {
		risky, err := ProviderJudge{Prov: replyProvider(reply)}.Judge(ctx, "x", "y", "s1")
		if err != nil || risky != want {
			t.Fatalf("%q: risky=%v err=%v", reply, risky, err)
		}
	}
}

func TestJudgeUnclearOrErrorEscalates(t *testing.T) {
	if r, _ := (ProviderJudge{Prov: replyProvider("maybe")}).Judge(ctx, "x", "y", "s1"); !r {
		t.Fatal("unclear → ask")
	}
	if r, _ := (ProviderJudge{Prov: errProvider()}).Judge(ctx, "x", "y", "s1"); !r {
		t.Fatal("error → ask")
	}
	if r, err := (ProviderJudge{Prov: &streamErrProvider{}}).Judge(ctx, "x", "y", "s1"); !r || err == nil {
		t.Fatal("stream error → ask", err)
	}
	if r, err := (ProviderJudge{}).Judge(ctx, "x", "y", "s1"); !r || err == nil {
		t.Fatal("no provider → ask", err)
	}
}

func TestGateSmartUsesJudge(t *testing.T) {
	g := newGate(t, "smart", "deny")
	var calls int
	g.Judge = fixedJudge{risky: false, n: &calls}
	p := &countingPrompter{answer: ScopeDeny}
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf /tmp/x"}, p); err != nil || p.n != 0 || calls != 1 {
		t.Fatal("approving judge", err, p.n, calls)
	}
	// Hermes: a smart approval covers that one command. The next command of the
	// same pattern goes to the judge again, with no prompt.
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf /tmp/y"}, p); err != nil || p.n != 0 || calls != 2 {
		t.Fatal("a smart approval must not hold for the session", err, p.n, calls)
	}
	if g.sessionApproved("s", "delete_in_root_path") {
		t.Fatal("a smart approval saved a session grant")
	}
	// A safe command does not reach the judge.
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "ls"}, p); err != nil || calls != 2 {
		t.Fatal("judge called for a safe command", calls)
	}

	g = newGate(t, "smart", "deny")
	g.Judge = fixedJudge{risky: true}
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf /tmp/x"}, p); err == nil || p.n != 1 {
		t.Fatal("escalating judge must ask", err, p.n)
	}

	g = newGate(t, "smart", "deny")
	g.Judge = fixedJudge{risky: false, err: errors.New("down")}
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf /tmp/x"}, p); err == nil || p.n != 2 {
		t.Fatal("failing judge must ask", err, p.n)
	}
}

func TestGateSmartNeverJudgesHardline(t *testing.T) {
	g := newGate(t, "smart", "deny")
	var calls int
	g.Judge = fixedJudge{risky: false, n: &calls}
	blockedReason(t, g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf /"}, alwaysApprove{}))
	blockedReason(t, g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "echo x | sudo -S ls"}, alwaysApprove{}))
	if calls != 0 {
		t.Fatal("hardline or sudo command reached the judge")
	}
}

func TestGateSmartWithProviderJudge(t *testing.T) {
	g := newGate(t, "smart", "deny")
	g.Judge = ProviderJudge{Prov: replyProvider("APPROVE: temp files")}
	p := &countingPrompter{answer: ScopeDeny}
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf /tmp/x"}, p); err != nil || p.n != 0 {
		t.Fatal(err, p.n)
	}
}

// usageCall is one call of a provider.RecordUsage.
type usageCall struct {
	sessionID, task string
	prov            provider.Provider
	out             provider.Result
}

// recorder returns a provider.RecordUsage that appends each call to calls.
func recorder(calls *[]usageCall) provider.RecordUsage {
	return func(_ context.Context, sessionID, task string, prov provider.Provider, out provider.Result) {
		*calls = append(*calls, usageCall{sessionID, task, prov, out})
	}
}

func TestJudgeRecordsUsage(t *testing.T) {
	prov := replyProvider("APPROVE: temp files")
	// Collect fills FirstTokenMS and TotalMS when they are 0, so the fake usage sets them.
	prov.usage = provider.Usage{InputTokens: 900, OutputTokens: 12, CacheReadTokens: 300, FirstTokenMS: 5, TotalMS: 40}
	var calls []usageCall
	risky, err := ProviderJudge{Prov: prov, Record: recorder(&calls)}.Judge(ctx, "rm -rf /tmp/x", "recursive delete", "s1")
	if err != nil || risky {
		t.Fatal(risky, err)
	}
	if len(calls) != 1 {
		t.Fatalf("%+v", calls)
	}
	c := calls[0]
	if c.sessionID != "s1" || c.task != "approval" || c.prov != prov || c.out.Usage != prov.usage || c.out.Text != "APPROVE: temp files" {
		t.Fatalf("%+v", c)
	}
}

// A billed call with an empty answer is recorded. The empty answer is risky.
func TestJudgeRecordsEmptyAnswer(t *testing.T) {
	var calls []usageCall
	risky, err := ProviderJudge{Prov: replyProvider(""), Record: recorder(&calls)}.Judge(ctx, "x", "y", "s1")
	if err != nil || !risky || len(calls) != 1 || calls[0].task != "approval" || calls[0].sessionID != "s1" {
		t.Fatal(risky, err, calls)
	}
}

func TestJudgeFailedCallRecordsNothing(t *testing.T) {
	var calls []usageCall
	for _, j := range []ProviderJudge{
		{Prov: errProvider(), Record: recorder(&calls)},
		{Prov: &streamErrProvider{}, Record: recorder(&calls)},
		{Record: recorder(&calls)},
	} {
		if r, err := j.Judge(ctx, "x", "y", "s1"); !r || err == nil {
			t.Fatal(r, err)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("a failed call must record nothing: %+v", calls)
	}
}

func TestJudgeNilRecord(t *testing.T) {
	if r, err := (ProviderJudge{Prov: replyProvider("APPROVE")}).Judge(ctx, "x", "y", "s1"); r || err != nil {
		t.Fatal(r, err)
	}
}

func TestGateSmartPassesSessionID(t *testing.T) {
	g := newGate(t, "smart", "deny")
	var got string
	g.Judge = fixedJudge{risky: false, session: &got}
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s-42", Command: "rm -rf /tmp/x"}, &countingPrompter{answer: ScopeDeny}); err != nil {
		t.Fatal(err)
	}
	if got != "s-42" {
		t.Fatalf("the judge got session %q", got)
	}
}

// scriptJudge is a Judge and a ScriptJudge. JudgeScript gives a fixed verdict, counts
// the calls in n, and keeps the last script, description, and session ID in got.
type scriptJudge struct {
	risky bool
	n     *int
	got   *[3]string
}

func (scriptJudge) Judge(context.Context, string, string, string) (bool, error) { return true, nil }

func (j scriptJudge) JudgeScript(_ context.Context, script, description, sessionID string) (bool, error) {
	*j.n++
	if j.got != nil {
		*j.got = [3]string{script, description, sessionID}
	}
	return j.risky, nil
}

func TestGateSmartJudgesEachScript(t *testing.T) {
	g := newGate(t, "smart", "deny")
	var calls int
	var got [3]string
	g.Judge = scriptJudge{n: &calls, got: &got}
	p := &countingPrompter{answer: ScopeDeny}
	// APPROVE runs this script only. The next script goes to the judge again.
	for want := 1; want <= 2; want++ {
		if err := g.CheckScript(ctx, ApprovalRequest{SessionID: "s", Command: "print(1)"}, p); err != nil || p.n != 0 || calls != want {
			t.Fatal(err, p.n, calls)
		}
	}
	if got != [3]string{"print(1)", "run a Python script on the host", "s"} {
		t.Fatalf("%q", got)
	}
	g.Judge = scriptJudge{risky: true, n: &calls}
	if err := g.CheckScript(ctx, ApprovalRequest{SessionID: "s", Command: "print(1)"}, p); err == nil || p.n != 1 {
		t.Fatal("an escalating judge must ask", err, p.n)
	}
}

func TestGateSmartLongScriptGoesToTheUser(t *testing.T) {
	if SmartScriptMaxChars != 8000 {
		t.Fatal(SmartScriptMaxChars)
	}
	g := newGate(t, "smart", "deny")
	var calls int
	g.Judge = scriptJudge{n: &calls}
	p := &countingPrompter{answer: ScopeOnce}
	long := strings.Repeat("x", SmartScriptMaxChars+1)
	if err := g.CheckScript(ctx, ApprovalRequest{SessionID: "s", Command: long}, p); err != nil || p.n != 1 || calls != 0 {
		t.Fatal("a script over 8000 characters must go to the user", err, p.n, calls)
	}
	// The limit counts characters, not bytes.
	wide := strings.Repeat("é", SmartScriptMaxChars)
	if err := g.CheckScript(ctx, ApprovalRequest{SessionID: "s", Command: wide}, p); err != nil || p.n != 1 || calls != 1 {
		t.Fatal(err, p.n, calls)
	}
}

func TestGateSmartScriptNeedsScriptJudge(t *testing.T) {
	g := newGate(t, "smart", "deny")
	var calls int
	g.Judge = fixedJudge{risky: false, n: &calls}
	p := &countingPrompter{answer: ScopeOnce}
	if err := g.CheckScript(ctx, ApprovalRequest{SessionID: "s", Command: "print(1)"}, p); err != nil || p.n != 1 || calls != 0 {
		t.Fatal(err, p.n, calls)
	}
}

func TestJudgeScriptPrompt(t *testing.T) {
	prov := replyProvider("APPROVE: prints a number")
	var rec []usageCall
	risky, err := ProviderJudge{Prov: prov, Record: recorder(&rec)}.JudgeScript(ctx, "print(1)", "run a Python script on the host", "s1")
	if err != nil || risky {
		t.Fatal(risky, err)
	}
	if len(prov.last.Tools) != 0 || len(prov.last.Messages) != 1 || prov.last.Messages[0].Role != provider.RoleUser || !prov.dl {
		t.Fatalf("%+v", prov.last)
	}
	msg := prov.last.Messages[0].Content
	if !strings.Contains(msg, "A Python script will run on the host") || !strings.Contains(msg, "print(1)") ||
		!strings.Contains(msg, "Flagged reason: run a Python script on the host") || strings.Contains(msg, "A terminal command was flagged") {
		t.Fatal(msg)
	}
	if len(rec) != 1 || rec[0].task != "approval" || rec[0].sessionID != "s1" {
		t.Fatalf("%+v", rec)
	}
	if r, _ := (ProviderJudge{Prov: replyProvider("ESCALATE: deletes files")}).JudgeScript(ctx, "x", "y", "s1"); !r {
		t.Fatal("ESCALATE must be risky")
	}
	if r, err := (ProviderJudge{}).JudgeScript(ctx, "x", "y", "s1"); !r || err == nil {
		t.Fatal("no provider must be risky", r, err)
	}
	var _ ScriptJudge = ProviderJudge{}
}

// deadlineProvider approves each call and keeps the time that was left on the context.
type deadlineProvider struct{ left time.Duration }

func (p *deadlineProvider) Name() string  { return "fake" }
func (p *deadlineProvider) Model() string { return "fake-model" }
func (p *deadlineProvider) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Event, error) {
	if dl, ok := ctx.Deadline(); ok {
		p.left = time.Until(dl)
	}
	ch := make(chan provider.Event, 2)
	ch <- provider.Event{Type: provider.EventText, Delta: "APPROVE: ok"}
	ch <- provider.Event{Type: provider.EventDone, FinishReason: "stop", Usage: &provider.Usage{}}
	close(ch)
	return ch, nil
}

func TestProviderJudgeTimeoutField(t *testing.T) {
	p := &deadlineProvider{}
	if _, err := (ProviderJudge{Prov: p, Timeout: 2 * time.Second}).Judge(ctx, "x", "y", "s1"); err != nil {
		t.Fatal(err)
	}
	if p.left <= time.Second || p.left > 2*time.Second {
		t.Fatal("Timeout 2s:", p.left)
	}
	if _, err := (ProviderJudge{Prov: p}).Judge(ctx, "x", "y", "s1"); err != nil {
		t.Fatal(err)
	}
	if p.left <= 29*time.Second || p.left > 30*time.Second {
		t.Fatal("a zero Timeout must give 30s:", p.left)
	}
}

func TestGateSmartScriptMaxCharsField(t *testing.T) {
	g := newGate(t, "smart", "deny")
	g.SmartScriptMaxChars = 100
	var calls int
	g.Judge = scriptJudge{n: &calls}
	p := &countingPrompter{answer: ScopeOnce}
	if err := g.CheckScript(ctx, ApprovalRequest{SessionID: "s", Command: strings.Repeat("x", 101)}, p); err != nil || p.n != 1 || calls != 0 {
		t.Fatal("a script over 100 characters must go to the user", err, p.n, calls)
	}
	if err := g.CheckScript(ctx, ApprovalRequest{SessionID: "s", Command: strings.Repeat("é", 100)}, p); err != nil || p.n != 1 || calls != 1 {
		t.Fatal("a script of 100 characters must go to the judge", err, p.n, calls)
	}
}

func TestProtectedSkipsTheJudge(t *testing.T) {
	g := protectedGate(t, "smart")
	var calls int
	g.Judge = fixedJudge{risky: false, n: &calls}
	p := &countingPrompter{answer: ScopeOnce}
	env := filepath.Join(os.Getenv("HOME"), ".tether", ".env")
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "tee " + env}, p); err != nil || p.n != 1 || calls != 0 {
		t.Fatal("a protected write must go to the user, not to the judge", err, p.n, calls)
	}
	if !strings.HasPrefix(p.last.PatternID, "protected_path_write_") {
		t.Fatal(p.last.PatternID)
	}
	// A general rule still goes to the judge.
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf build"}, p); err != nil || p.n != 1 || calls != 1 {
		t.Fatal(err, p.n, calls)
	}
}

func TestProtectedScriptSkipsTheJudge(t *testing.T) {
	g := protectedGate(t, "smart")
	var calls int
	g.Judge = scriptJudge{n: &calls}
	p := &countingPrompter{answer: ScopeOnce}
	env := filepath.Join(os.Getenv("HOME"), ".tether", ".env")
	script := "import os\nos.system('cp x " + env + "')"
	if err := g.CheckScript(ctx, ApprovalRequest{SessionID: "s", Command: script}, p); err != nil || p.n != 1 || calls != 0 {
		t.Fatal("a script with a protected literal match must go to the user", err, p.n, calls)
	}
}
