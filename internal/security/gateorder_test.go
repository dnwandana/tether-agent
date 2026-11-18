package security

import (
	"context"
	"strings"
	"testing"
)

const blockEnd = "Do not retry it with a different spelling or another tool. Tell the owner what you tried to run."

func TestCouldNotReadPatternID(t *testing.T) {
	if CouldNotReadPatternID != "could_not_read" {
		t.Fatal(CouldNotReadPatternID)
	}
}

func TestHumanOnlyAsksEveryTime(t *testing.T) {
	calls := 0
	for _, mode := range []string{"manual", "smart", "off"} {
		g := newGate(t, mode, "approve")
		g.Judge = fixedJudge{risky: false, n: &calls}
		if err := g.SetMode(ctx, "cli", ModeFull); err != nil {
			t.Fatal(err)
		}
		g.approveSession("s", "human_only_password_change")
		p := &countingPrompter{answer: ScopeSession}
		for want := 1; want <= 2; want++ {
			if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Scope: "cli", Command: "passwd bob"}, p); err != nil || p.n != want {
				t.Fatal(mode, err, p.n)
			}
			r := p.last
			if !r.HumanOnly || !r.NoSession || !r.NoAlways || r.PatternID != "human_only_password_change" ||
				r.Description != "password change" {
				t.Fatalf("%s: %+v", mode, r)
			}
		}
	}
	if calls != 0 {
		t.Fatal("the smart judge saw a human-only command")
	}
}

func TestHumanOnlyBlocksWithNoOwner(t *testing.T) {
	g := newGate(t, "off", "approve")
	want := "BLOCKED (human-only): password change. This action needs the owner to tap Approve each time, " +
		"and no owner can answer here. " + blockEnd
	p := &countingPrompter{answer: ScopeOnce}
	for name, req := range map[string]ApprovalRequest{
		"cron":       {SessionID: "c", Kind: "cron", Command: "passwd bob"},
		"cron child": {SessionID: "c2", Kind: "subagent", RootKind: "cron", Command: "passwd bob"},
		"member":     {SessionID: "m", Command: "passwd bob", Member: true},
	} {
		if reason := blockedReason(t, g.Check(ctx, req, p)); reason != want || p.n != 0 {
			t.Fatal(name, reason, p.n)
		}
	}
	if reason := blockedReason(t, g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "passwd bob"}, nil)); reason != want {
		t.Fatal(reason)
	}
}

func TestRaisedRuleIsHumanOnly(t *testing.T) {
	t.Cleanup(func() { SetHumanOnlyRules(nil) })
	SetHumanOnlyRules([]string{"git_force_push"})
	g := newGate(t, "off", "approve")
	p := &countingPrompter{answer: ScopeOnce}
	cmd := "git push --force origin main"
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: cmd}, p); err != nil || p.n != 1 ||
		!p.last.HumanOnly || p.last.PatternID != "git_force_push" {
		t.Fatalf("%v %d %+v", err, p.n, p.last)
	}
	if reason := blockedReason(t, g.Check(ctx, ApprovalRequest{SessionID: "c", Kind: "cron", Command: cmd}, p)); !strings.HasPrefix(reason, "BLOCKED (human-only): ") {
		t.Fatal(reason)
	}
	SetHumanOnlyRules(nil)
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: cmd}, p); err != nil || p.n != 1 {
		t.Fatal("mode off runs the rule again after the raise is gone", err, p.n)
	}
}

func TestCheckScriptHumanOnly(t *testing.T) {
	g := newGate(t, "off", "approve")
	p := &countingPrompter{answer: ScopeSession}
	code := `import subprocess; subprocess.run(["passwd", "bob"])`
	for want := 1; want <= 2; want++ {
		if err := g.CheckScript(ctx, scriptReq("s", code), p); err != nil || p.n != want {
			t.Fatal(err, p.n)
		}
		if r := p.last; !r.HumanOnly || !r.NoSession || !r.NoAlways || r.PatternID != "human_only_password_change" {
			t.Fatalf("%+v", r)
		}
	}
	cron := ApprovalRequest{SessionID: "c", Kind: "cron", Command: code}
	if reason := blockedReason(t, g.CheckScript(ctx, cron, nil)); !strings.HasPrefix(reason, "BLOCKED (human-only): ") ||
		!strings.HasSuffix(reason, blockEnd) {
		t.Fatal(reason)
	}
}

func TestCouldNotRead(t *testing.T) {
	const cmd = `ls; echo "x`
	parsed := ReadCommand(cmd)
	if parsed.Read {
		t.Fatal("the test command must be unreadable")
	}
	g := newGate(t, "manual", "approve")
	p := &countingPrompter{answer: ScopeSession}
	for want := 1; want <= 2; want++ {
		if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Command: cmd}, p); err != nil || p.n != want {
			t.Fatal("no grant covers an unread command", err, p.n)
		}
		r := p.last
		if !r.Unread || !r.NoSession || !r.NoAlways || r.PatternID != CouldNotReadPatternID ||
			r.Description != "could not read the command: "+parsed.Why {
			t.Fatalf("%+v", r)
		}
	}
	// cron_mode: approve does not run it.
	reason := blockedReason(t, g.Check(ctx, ApprovalRequest{SessionID: "c", Kind: "cron", Command: cmd}, p))
	if !strings.Contains(reason, "Use write_file for large content.") || !strings.HasSuffix(reason, blockEnd) {
		t.Fatal(reason)
	}
	// mode: off and /mode full run it.
	if err := newGate(t, "off", "deny").Check(ctx, ApprovalRequest{SessionID: "s", Command: cmd}, nil); err != nil {
		t.Fatal(err)
	}
	if err := g.SetMode(ctx, "telegram:1:0", ModeFull); err != nil {
		t.Fatal(err)
	}
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Scope: "telegram:1:0", Command: cmd}, nil); err != nil {
		t.Fatal(err)
	}
	// A matched rule stays, with a note. No grant and no judge allow it.
	calls := 0
	sm := newGate(t, "smart", "deny")
	sm.Judge = fixedJudge{risky: false, n: &calls}
	sm.approveSession("s", "recursive_delete")
	q := &countingPrompter{answer: ScopeDeny}
	reason = blockedReason(t, sm.Check(ctx, ApprovalRequest{SessionID: "s", Command: `rm -rf build; echo "x`}, q))
	if q.n != 1 || calls != 0 || q.last.PatternID != "recursive_delete" || !q.last.Unread ||
		!strings.HasPrefix(q.last.Description, "recursive delete (the gate could not read the full command: ") {
		t.Fatalf("%d %d %+v", q.n, calls, q.last)
	}
	if !strings.Contains(reason, "Use write_file for large content.") || !strings.HasSuffix(reason, blockEnd) {
		t.Fatal(reason)
	}
}

func TestLiveModeFull(t *testing.T) {
	g := newGate(t, "manual", "deny")
	const scope = "telegram:42:7"
	if err := g.SetMode(ctx, scope, ModeFull); err != nil {
		t.Fatal(err)
	}
	p := &countingPrompter{answer: ScopeDeny}
	if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Scope: scope, Command: "rm -rf build"}, p); err != nil || p.n != 0 {
		t.Fatal(err, p.n)
	}
	// The hardline rules still block.
	if reason := blockedReason(t, g.Check(ctx, ApprovalRequest{SessionID: "s", Scope: scope, Command: "rm -rf /"}, p)); !strings.HasPrefix(reason, "BLOCKED (hardline): ") {
		t.Fatal(reason)
	}
	// Another topic, no scope, and a read-only topic follow approvals.mode.
	if err := g.SetMode(ctx, "telegram:42:8", ModeReadOnly); err != nil {
		t.Fatal(err)
	}
	for i, s := range []string{"telegram:42:0", "", "telegram:42:8"} {
		if err := g.Check(ctx, ApprovalRequest{SessionID: "s", Scope: s, Command: "rm -rf build"}, p); err == nil || p.n != i+1 {
			t.Fatal(s, err, p.n)
		}
	}
}

// modeFlipPrompter sets the mode of scope to read-only while the prompt waits, then answers.
type modeFlipPrompter struct {
	g      *Gate
	scope  string
	answer Scope
}

func (p modeFlipPrompter) PromptApproval(ctx context.Context, _ ApprovalRequest) (Scope, error) {
	if err := p.g.SetMode(ctx, p.scope, ModeReadOnly); err != nil {
		return ScopeDeny, err
	}
	return p.answer, nil
}

// A /mode read-only change while the prompt waits: the tap must not run the command, and it
// saves no grant.
func TestReadOnlyDuringWaitBlocks(t *testing.T) {
	const scope = "telegram:42:7"
	const want = "BLOCKED: The mode of this topic changed to read-only while the approval waited."
	for _, answer := range []Scope{ScopeOnce, ScopeSession, ScopeAlways} {
		g := newGate(t, "manual", "deny")
		bin, wd := fakeBin(t, "rm"), t.TempDir()
		req := ApprovalRequest{SessionID: "s", Scope: scope, Command: "rm -rf build/", Workdir: wd, Path: bin}
		if reason := blockedReason(t, g.Check(ctx, req, modeFlipPrompter{g: g, scope: scope, answer: answer})); !strings.HasPrefix(reason, want) {
			t.Fatal(answer, reason)
		}
		if g.sessionApproved("s", "recursive_delete") || len(readGrants(t, g)) != 0 {
			t.Fatal(answer, "the tap saved a grant")
		}
		if err := g.SetMode(ctx, scope, ModeGuarded); err != nil {
			t.Fatal(err)
		}
		script := ApprovalRequest{SessionID: "s", Kind: "chat", Scope: scope, Command: "print(1)"}
		if reason := blockedReason(t, g.CheckScript(ctx, script, modeFlipPrompter{g: g, scope: scope, answer: answer})); !strings.HasPrefix(reason, want) {
			t.Fatal(answer, reason)
		}
	}
}

// TestACPReadOnlyDuringWaitDenies checks that a permission request of a coding agent also
// reads the live mode after the tap.
func TestACPReadOnlyDuringWaitDenies(t *testing.T) {
	const scope = "telegram:42:7"
	for _, answer := range []Scope{ScopeOnce, ScopeSession, ScopeAlways} {
		g := newGate(t, "manual", "deny")
		req := ApprovalRequest{SessionID: "s", Scope: scope, Kind: "acp", PatternID: "acp_write", Command: "write a.txt"}
		got, err := g.AskACP(ctx, req, modeFlipPrompter{g: g, scope: scope, answer: answer})
		if err != nil || got != ScopeDeny {
			t.Fatal(answer, got, err)
		}
		if g.sessionApproved("s", "acp_write") {
			t.Fatal(answer, "the tap saved a session answer")
		}
	}
}

func TestBlockTextsEndWithSentence(t *testing.T) {
	g := newGate(t, "manual", "deny")
	for name, err := range map[string]error{
		"hardline":    g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf /"}, nil),
		"owner deny":  g.Check(ctx, ApprovalRequest{SessionID: "s", Command: "rm -rf build"}, &countingPrompter{answer: ScopeDeny}),
		"cron":        g.Check(ctx, ApprovalRequest{SessionID: "c", Kind: "cron", Command: "rm -rf build"}, nil),
		"cron script": g.CheckScript(ctx, ApprovalRequest{SessionID: "c", Kind: "cron", Command: "print(1)"}, nil),
	} {
		if reason := blockedReason(t, err); !strings.HasSuffix(reason, " "+blockEnd) {
			t.Fatal(name, reason)
		}
	}
}
