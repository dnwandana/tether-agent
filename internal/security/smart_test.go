package security

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

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
	// Hermes: a smart approval holds for the session, so no second judge call.
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf /tmp/y"}, p); err != nil || calls != 1 {
		t.Fatal("smart approval not kept for the session", err, calls)
	}
	// A safe command does not reach the judge.
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "ls"}, p); err != nil || calls != 1 {
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
