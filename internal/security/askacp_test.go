package security

import (
	"context"
	"errors"
	"testing"
	"time"
)

// acpReq is a permission request of a coding agent, as the ACP bridge builds it.
func acpReq(pattern string) ApprovalRequest {
	return ApprovalRequest{SessionID: "s1", SessionKey: "telegram:dm:5", Kind: "chat", RootKind: "chat",
		Command: "go test ./...", PatternID: pattern, Description: "claude-code wants to run a command",
		Summary: "Run the tests", NoAlways: true}
}

func TestAskACPReturnContract(t *testing.T) {
	sendErr := errors.New("telegram approval: send failed")
	for _, c := range []struct {
		name      string
		p         Prompter
		wantScope Scope
		wantErr   error
	}{
		{"once", &countingPrompter{answer: ScopeOnce}, ScopeOnce, nil},
		{"session", &countingPrompter{answer: ScopeSession}, ScopeSession, nil},
		{"always becomes session", &countingPrompter{answer: ScopeAlways}, ScopeSession, nil},
		{"deny", &countingPrompter{answer: ScopeDeny}, ScopeDeny, nil},
		{"prompter answers timeout", &countingPrompter{answer: ScopeTimeout}, ScopeTimeout, nil},
		{"prompter error", &countingPrompter{err: sendErr}, ScopeDeny, sendErr},
		{"no prompter", nil, ScopeDeny, nil},
	} {
		g := newGate(t, "manual", "deny")
		got, err := g.AskACP(ctx, acpReq("acp:claude-code:a1:execute:go"), c.p)
		if got != c.wantScope || !errors.Is(err, c.wantErr) || (c.wantErr == nil && err != nil) {
			t.Errorf("%s: got %v, %v, want %v, %v", c.name, got, err, c.wantScope, c.wantErr)
		}
	}
}

func TestAskACPOwnTimeoutIsNotAnError(t *testing.T) {
	g := newGate(t, "manual", "deny")
	g.Timeout = 30 * time.Millisecond
	got, err := g.AskACP(ctx, acpReq("acp:claude-code:a1:edit"), neverAnswers{})
	if got != ScopeTimeout || err != nil {
		t.Fatal(got, err)
	}
}

func TestAskACPParentContextEnd(t *testing.T) {
	g := newGate(t, "manual", "deny")
	cctx, cancel := context.WithCancel(ctx)
	time.AfterFunc(20*time.Millisecond, cancel)
	got, err := g.AskACP(cctx, acpReq("acp:claude-code:a1:edit"), neverAnswers{})
	if got != ScopeTimeout || !errors.Is(err, context.Canceled) {
		t.Fatal(got, err)
	}
}

// The danger patterns, approvals.mode off, and approvals.cron_mode do not apply.
func TestAskACPAlwaysAsks(t *testing.T) {
	for _, mode := range []string{"manual", "smart", "off"} {
		g := newGate(t, mode, "approve")
		p := &countingPrompter{answer: ScopeDeny}
		req := acpReq("acp:claude-code:a1:execute:ls")
		req.Command = "ls"
		if got, _ := g.AskACP(ctx, req, p); got != ScopeDeny || p.n != 1 {
			t.Fatalf("mode %s: got %v after %d prompts", mode, got, p.n)
		}
	}
}

func TestAskACPPrompterSeesNoAlways(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := &countingPrompter{answer: ScopeOnce}
	req := acpReq("acp:claude-code:a1:read")
	req.NoAlways = false
	_, _ = g.AskACP(ctx, req, p)
	if !p.last.NoAlways {
		t.Fatal("AskACP must never show an always answer")
	}
}

func TestAskACPSessionMemory(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := &countingPrompter{answer: ScopeAlways}
	if got, _ := g.AskACP(ctx, acpReq("acp:claude-code:a1:execute:go"), p); got != ScopeSession {
		t.Fatal(got)
	}
	if got, err := g.AskACP(ctx, acpReq("acp:claude-code:a1:execute:go"), p); got != ScopeSession || err != nil || p.n != 1 {
		t.Fatalf("a saved pattern must not ask again: %v %v %d", got, err, p.n)
	}
	p.answer = ScopeOnce
	if got, _ := g.AskACP(ctx, acpReq("acp:claude-code:a1:execute:rm"), p); got != ScopeOnce || p.n != 2 {
		t.Fatal("another pattern must ask", got, p.n)
	}
	other := acpReq("acp:claude-code:a1:execute:go")
	other.SessionID = "s2"
	if _, _ = g.AskACP(ctx, other, p); p.n != 3 {
		t.Fatal("another session must ask", p.n)
	}
	var n int
	if err := g.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM approvals_always").Scan(&n); err != nil || n != 0 {
		t.Fatalf("AskACP must not write approvals_always: %d %v", n, err)
	}
	g.ResetSession("s1")
	if _, _ = g.AskACP(ctx, acpReq("acp:claude-code:a1:execute:go"), p); p.n != 4 {
		t.Fatal("ResetSession must clear the answer", p.n)
	}
}

func TestAskACPNoSession(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := &countingPrompter{answer: ScopeSession}
	// A plain request saves the pattern for the session.
	if got, _ := g.AskACP(ctx, acpReq("acp:claude-code:a1:execute:go"), p); got != ScopeSession {
		t.Fatal(got)
	}
	// A compound command has the same pattern, but NoSession asks each time and saves nothing.
	req := acpReq("acp:claude-code:a1:execute:go")
	req.Command, req.NoSession = "go test ./... && rm -rf build", true
	for i := 2; i <= 3; i++ {
		got, err := g.AskACP(ctx, req, p)
		if got != ScopeOnce || err != nil || p.n != i {
			t.Fatalf("call %d: got %v %v after %d prompts", i, got, err, p.n)
		}
	}
	p.answer = ScopeAlways
	if got, _ := g.AskACP(ctx, req, p); got != ScopeOnce {
		t.Fatal("always must become once with NoSession", got)
	}
	plan := acpReq("acp:claude-code:a1:switch_mode")
	plan.NoSession = true
	p.answer = ScopeSession
	_, _ = g.AskACP(ctx, plan, p)
	plan.NoSession = false
	before := p.n
	if _, _ = g.AskACP(ctx, plan, p); p.n != before+1 {
		t.Fatal("a NoSession answer must not go into the session memory")
	}
}
