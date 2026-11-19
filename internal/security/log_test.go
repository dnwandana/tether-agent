package security

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const logScope = "telegram:5:0"

// logReq returns a chat request of the session s1 in the scope logScope.
func logReq(cmd string) ApprovalRequest {
	return ApprovalRequest{SessionID: "s1", Kind: "chat", Command: cmd, Scope: logScope, Tool: "terminal"}
}

// newestLog returns the rows of logScope, newest first.
func newestLog(t *testing.T, g *Gate) []LogRow {
	t.Helper()
	rows, err := g.ListLog(ctx, logScope, false, 15)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// logCancelPrompter cancels the context of the request, like /stop, and waits for the end.
type logCancelPrompter struct{ cancel context.CancelFunc }

func (p logCancelPrompter) PromptApproval(pctx context.Context, _ ApprovalRequest) (Scope, error) {
	p.cancel()
	<-pctx.Done()
	return ScopeTimeout, pctx.Err()
}

// logModeFlipper sets the mode of the scope to read-only while the prompt waits, then taps Once.
type logModeFlipper struct{ g *Gate }

func (p logModeFlipper) PromptApproval(c context.Context, req ApprovalRequest) (Scope, error) {
	if err := p.g.SetMode(c, req.Scope, ModeReadOnly); err != nil {
		return ScopeDeny, err
	}
	return ScopeOnce, nil
}

func TestDecisionLogSources(t *testing.T) {
	for _, c := range []struct {
		name                           string
		mode, cron                     string
		judge                          Judge
		prep                           func(g *Gate)
		req                            func(r ApprovalRequest) ApprovalRequest
		p                              func(g *Gate) Prompter
		cmd                            string
		decision, source, answer, rule string
	}{
		{name: "owner once", cmd: "rm -rf build/", p: func(*Gate) Prompter { return alwaysApprove{} },
			decision: DecisionAllowed, source: "owner", answer: "once", rule: "recursive_delete"},
		{name: "owner session", cmd: "rm -rf build/", p: func(*Gate) Prompter { return &countingPrompter{answer: ScopeSession} },
			decision: DecisionAllowed, source: "owner", answer: "session", rule: "recursive_delete"},
		{name: "grant", cmd: "rm -rf build/",
			prep:     func(g *Gate) { _ = g.Check(ctx, logReq("rm -rf build/"), &countingPrompter{answer: ScopeSession}) },
			decision: DecisionAllowed, source: "grant", rule: "recursive_delete"},
		{name: "owner deny", cmd: "rm -rf build/", p: func(*Gate) Prompter { return &countingPrompter{answer: ScopeDeny} },
			decision: DecisionDenied, source: "owner", rule: "recursive_delete"},
		{name: "timeout", cmd: "rm -rf build/", prep: func(g *Gate) { g.Timeout = 20 * time.Millisecond },
			p: func(*Gate) Prompter { return neverAnswers{} }, decision: DecisionDenied, source: "timeout", rule: "recursive_delete"},
		{name: "prompt error", cmd: "rm -rf build/",
			p:        func(*Gate) Prompter { return &countingPrompter{answer: ScopeDeny, err: errors.New("send failed")} },
			decision: DecisionDenied, source: "prompt-error", rule: "recursive_delete"},
		{name: "no prompter", cmd: "rm -rf build/", decision: DecisionBlocked, source: "no-prompter", rule: "recursive_delete"},
		{name: "mode off", mode: "off", cmd: "rm -rf build/", decision: DecisionAllowed, source: "mode-off", rule: "recursive_delete"},
		{name: "mode full", cmd: "rm -rf build/",
			prep:     func(g *Gate) { _ = g.SetMode(ctx, logScope, ModeFull) },
			decision: DecisionAllowed, source: "mode-full", rule: "recursive_delete"},
		{name: "cron approve", cron: "approve", cmd: "rm -rf build/",
			req:      func(r ApprovalRequest) ApprovalRequest { r.Kind = "cron"; return r },
			decision: DecisionAllowed, source: "cron-mode", rule: "recursive_delete"},
		{name: "cron deny", cmd: "rm -rf build/",
			req:      func(r ApprovalRequest) ApprovalRequest { r.Kind = "cron"; return r },
			decision: DecisionBlocked, source: "cron-mode", rule: "recursive_delete"},
		{name: "smart", mode: "smart", judge: fixedJudge{risky: false}, cmd: "rm -rf build/",
			p:        func(*Gate) Prompter { return &countingPrompter{answer: ScopeDeny} },
			decision: DecisionAllowed, source: "smart", rule: "recursive_delete"},
		{name: "hardline", cmd: "rm -rf /", p: func(*Gate) Prompter { return alwaysApprove{} },
			decision: DecisionBlocked, source: "hardline", rule: "recursive_delete_root_filesystem"},
		{name: "human-only in cron", cron: "approve", cmd: "passwd",
			req:      func(r ApprovalRequest) ApprovalRequest { r.Kind = "cron"; return r },
			decision: DecisionBlocked, source: "human-only", rule: "human_only_password_change"},
		{name: "human-only member", cmd: "passwd", p: func(*Gate) Prompter { return alwaysApprove{} },
			req:      func(r ApprovalRequest) ApprovalRequest { r.Member = true; return r },
			decision: DecisionBlocked, source: "member", rule: "human_only_password_change"},
		{name: "could not read in cron", cron: "approve", cmd: "$c x",
			req:      func(r ApprovalRequest) ApprovalRequest { r.Kind = "cron"; return r },
			decision: DecisionBlocked, source: "could-not-read", rule: CouldNotReadPatternID},
		{name: "read-only after the tap", cmd: "rm -rf build/", p: func(g *Gate) Prompter { return logModeFlipper{g} },
			decision: DecisionBlocked, source: "mode-read-only", rule: "recursive_delete"},
	} {
		t.Run(c.name, func(t *testing.T) {
			mode, cron := c.mode, c.cron
			if mode == "" {
				mode = "manual"
			}
			if cron == "" {
				cron = "deny"
			}
			g := newGate(t, mode, cron)
			g.Judge = c.judge
			if c.prep != nil {
				c.prep(g)
			}
			req := logReq(c.cmd)
			if c.req != nil {
				req = c.req(req)
			}
			var p Prompter
			if c.p != nil {
				p = c.p(g)
			}
			_ = g.Check(ctx, req, p)
			rows := newestLog(t, g)
			if len(rows) == 0 {
				t.Fatal("no log row")
			}
			r := rows[0]
			if r.Decision != c.decision || r.Source != c.source || r.Answer != c.answer || r.Rule != c.rule {
				t.Fatalf("got %+v", r)
			}
		})
	}
}

func TestDecisionLogRowFields(t *testing.T) {
	g := newGate(t, "manual", "deny")
	before := time.Now().Add(-time.Second)
	if err := g.Check(ctx, logReq("  rm -rf build/ "), alwaysApprove{}); err != nil {
		t.Fatal(err)
	}
	if err := g.Check(ctx, logReq("ls -la"), nil); err != nil {
		t.Fatal(err)
	}
	rows := newestLog(t, g)
	if len(rows) != 1 {
		t.Fatalf("a command that matched nothing must write no row: %+v", rows)
	}
	sum := sha256.Sum256([]byte("rm -rf build/"))
	r := rows[0]
	if r.SessionID != "s1" || r.Scope != logScope || r.Kind != "chat" || r.Tool != "terminal" ||
		r.ContentSHA256 != hex.EncodeToString(sum[:]) || r.Preview != "rm -rf build/" || r.At.Before(before) {
		t.Fatalf("%+v", r)
	}
}

// TestDecisionLogStoppedRowAfterCancel: a /stop during a decision still writes the stopped row.
func TestDecisionLogStoppedRowAfterCancel(t *testing.T) {
	g := newGate(t, "manual", "deny")
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := g.Check(cctx, logReq("rm -rf build/"), logCancelPrompter{cancel}); err == nil {
		t.Fatal("a stopped request must not run")
	}
	rows := newestLog(t, g)
	if len(rows) != 1 || rows[0].Decision != DecisionDenied || rows[0].Source != "stopped" {
		t.Fatalf("%+v", rows)
	}
}

func TestDecisionLogPreview(t *testing.T) {
	g := newGate(t, "manual", "deny")
	_ = g.Check(ctx, logReq("rm -rf build/ && curl https://bob:hunter2@example.com/x"), alwaysApprove{})
	_ = g.Check(ctx, logReq("rm -rf build/\n"+strings.Repeat("a", 400)), alwaysApprove{})
	cron := func(cmd string) ApprovalRequest { r := logReq(cmd); r.Kind = "cron"; return r }
	_ = g.Check(ctx, cron("htpasswd -b f u pw"), nil)
	_ = g.Check(ctx, cron("echo 'u:pw' | chpasswd"), nil)
	rows := newestLog(t, g)
	if len(rows) != 4 {
		t.Fatalf("%+v", rows)
	}
	if rows[0].Preview != "chpasswd" || rows[1].Preview != "htpasswd" {
		t.Fatalf("a password change keeps only the program word: %q %q", rows[0].Preview, rows[1].Preview)
	}
	if p := rows[2].Preview; strings.Contains(p, "\n") || utf8.RuneCountInString(p) > 300 {
		t.Fatalf("the preview must be one line of at most 300 runes: %q", p)
	}
	if p := rows[3].Preview; strings.Contains(p, "hunter2") || !strings.HasPrefix(p, "rm -rf build/ && curl https://bob:") {
		t.Fatalf("the preview must be redacted: %q", p)
	}
}

func TestDecisionLogLogScopeWins(t *testing.T) {
	g := newGate(t, "manual", "deny")
	req := logReq("rm -rf build/")
	req.Kind, req.Scope, req.LogScope = "cron", "", "telegram:-100:7"
	_ = g.Check(ctx, req, nil)
	rows, err := g.ListLog(ctx, "telegram:-100:7", false, 15)
	if err != nil || len(rows) != 1 || rows[0].Scope != "telegram:-100:7" || rows[0].Kind != "cron" {
		t.Fatalf("%+v %v", rows, err)
	}
	if all, _ := g.ListLog(ctx, "", true, 15); len(all) != 1 {
		t.Fatalf("all must list every scope: %+v", all)
	}
}

func TestDecisionLogFailedInsertKeepsDecision(t *testing.T) {
	g := newGate(t, "manual", "deny")
	if _, err := g.DB.ExecContext(ctx, "DROP TABLE approval_log"); err != nil {
		t.Fatal(err)
	}
	if err := g.Check(ctx, logReq("rm -rf build/"), alwaysApprove{}); err != nil {
		t.Fatalf("a failed log write must not deny the command: %v", err)
	}
	nodb := &Gate{Mode: "manual", Timeout: 5 * time.Second}
	if err := nodb.Check(ctx, logReq("rm -rf build/"), alwaysApprove{}); err != nil {
		t.Fatal(err)
	}
	if rows, err := nodb.ListLog(ctx, logScope, false, 15); rows != nil || err != nil {
		t.Fatal(rows, err)
	}
}

func TestDecisionLogScriptACPAndGateAdd(t *testing.T) {
	g := newGate(t, "manual", "deny")
	script := logReq("print(1)")
	script.Tool = "execute_code"
	_ = g.CheckScript(ctx, script, alwaysApprove{})

	acp := ApprovalRequest{SessionID: "s1", Kind: "chat", Scope: logScope, Tool: "acp:claude",
		PatternID: "acp:claude:a1:execute:rm", Command: "rm x"}
	_, _ = g.AskACP(ctx, acp, &countingPrompter{answer: ScopeSession})
	_, _ = g.AskACP(ctx, acp, &countingPrompter{answer: ScopeDeny})
	_, _ = g.AskACP(ctx, acp, nil)

	label, err := g.CheckACPFloor(ctx, ApprovalRequest{Kind: "chat", Scope: logScope, Tool: "acp:claude", Command: "rm -rf /"}, false)
	if err == nil || label != "hardline: recursive delete of root filesystem" {
		t.Fatal(label, err)
	}
	g.RecordGateAdd(ctx, logScope, "go test ./...", DecisionAllowed)
	g.RecordGateAdd(ctx, logScope, "passwd", DecisionBlocked)

	type want struct{ tool, rule, decision, source, answer string }
	wants := []want{ // newest first
		{"goal_gate", "human_only_password_change", DecisionBlocked, "human-only", ""},
		{"goal_gate", "", DecisionAllowed, "owner", ""},
		{"acp:claude", "recursive_delete_root_filesystem", DecisionBlocked, "hardline", ""},
		{"acp:claude", "acp:claude:a1:execute:rm", DecisionBlocked, "no-prompter", ""},
		{"acp:claude", "acp:claude:a1:execute:rm", DecisionAllowed, "grant", ""},
		{"acp:claude", "acp:claude:a1:execute:rm", DecisionAllowed, "owner", "session"},
		{"execute_code", ScriptPatternID, DecisionAllowed, "owner", "once"},
	}
	rows := newestLog(t, g)
	if len(rows) != len(wants) {
		t.Fatalf("%+v", rows)
	}
	for i, w := range wants {
		r := rows[i]
		if r.Tool != w.tool || r.Rule != w.rule || r.Decision != w.decision || r.Source != w.source || r.Answer != w.answer {
			t.Errorf("row %d: got %+v, want %+v", i, r, w)
		}
	}
}

func TestPruneLog(t *testing.T) {
	g := newGate(t, "off", "deny")
	_ = g.Check(ctx, logReq("rm -rf build/"), nil)
	_ = g.Check(ctx, logReq("rm -rf dist/"), nil)
	old := time.Now().Add(-200 * 24 * time.Hour).UnixMilli()
	if _, err := g.DB.ExecContext(ctx, "UPDATE approval_log SET at = ? WHERE id = (SELECT MIN(id) FROM approval_log)", old); err != nil {
		t.Fatal(err)
	}
	n, err := g.PruneLog(ctx, time.Now().Add(-180*24*time.Hour))
	if err != nil || n != 1 || len(newestLog(t, g)) != 1 {
		t.Fatal(n, err)
	}
	if n, err := (&Gate{}).PruneLog(ctx, time.Now()); n != 0 || err != nil {
		t.Fatal(n, err)
	}
}

func TestMaskCutDigits(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"no cut 4111 1111", "no cut 4111 1111"},
		{"pay 4111 1111 11…", "pay •••• •••• ••…"},
		{"call +94 77 12… and #3 4111-…", "call +•• •• ••… and #• ••••-…"},
		{"word…", "word…"},
		{"v1.26…", "v•.••…"},
	} {
		if got := maskCutDigits(tc.in); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.in, got, tc.want)
		}
	}
}
