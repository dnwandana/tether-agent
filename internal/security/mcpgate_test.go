package security

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// mcpPrompter answers with fn and records the requests.
type mcpPrompter struct {
	fn   func(ctx context.Context, req ApprovalRequest) (Scope, error)
	reqs []ApprovalRequest
}

func (p *mcpPrompter) PromptApproval(ctx context.Context, req ApprovalRequest) (Scope, error) {
	p.reqs = append(p.reqs, req)
	return p.fn(ctx, req)
}

// mcpAnswer returns a prompter that always gives s.
func mcpAnswer(s Scope) *mcpPrompter {
	return &mcpPrompter{fn: func(context.Context, ApprovalRequest) (Scope, error) { return s, nil }}
}

const testHash = "0123456789abcdef"

// blockTail ends each block text of the gate.
const blockTail = " Do not retry it with a different spelling or another tool. Tell the owner what you tried to run."

func mcpCall(server, tool, class string) MCPCall {
	return MCPCall{Server: server, Tool: tool, Class: class, ConfigHash: testHash, Args: json.RawMessage(`{"title":"x"}`)}
}

// hasRow reports whether rows has a row with the source and the decision.
func hasRow(rows []LogRow, source, decision string) bool {
	for _, r := range rows {
		if r.Source == source && r.Decision == decision {
			return true
		}
	}
	return false
}

func logRows(t *testing.T, g *Gate) []LogRow {
	t.Helper()
	rows, err := g.ListLog(ctx, "", true, 15)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestCheckMCPReadRuns(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := mcpAnswer(ScopeDeny)
	err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s", Kind: "cron"}, mcpCall("gh", "search", "read"), p)
	if err != nil || len(p.reqs) != 0 {
		t.Fatal(err, p.reqs)
	}
	if rows := logRows(t, g); len(rows) != 0 {
		t.Fatalf("a read call writes no log row: %+v", rows)
	}
}

func TestCheckMCPPromptFields(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := mcpAnswer(ScopeOnce)
	call := mcpCall("gh", "create_issue", "write")
	call.Args = json.RawMessage(`{"title":"Bug","body":"Steps"}`)
	req := ApprovalRequest{SessionID: "s", Kind: "chat", Scope: "cli", Summary: "Create an issue."}
	if err := g.CheckMCP(ctx, req, call, p); err != nil || len(p.reqs) != 1 {
		t.Fatal(err, p.reqs)
	}
	r := p.reqs[0]
	if r.Tool != "mcp:gh.create_issue" || r.Server != "gh" || r.ToolName != "create_issue" || r.Class != "write" ||
		r.PatternID != "mcp:gh.create_issue" || r.Description != "MCP write (gh · create_issue)" ||
		r.GrantKey != "mcp:gh.create_issue:"+testHash || r.Command != "body: Steps\ntitle: Bug" ||
		r.NoSession || r.NoAlways || r.Summary != "Create an issue." {
		t.Fatalf("%+v", r)
	}
	if rows := logRows(t, g); !hasRow(rows, "owner", DecisionAllowed) || rows[0].Answer != "once" ||
		rows[0].Tool != "mcp:gh.create_issue" || rows[0].Class != "write" {
		t.Fatalf("%+v", rows)
	}
}

func TestCheckMCPUnknownClassIsWrite(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := mcpAnswer(ScopeOnce)
	if err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s"}, mcpCall("gh", "frob", ""), p); err != nil || len(p.reqs) != 1 || p.reqs[0].Class != "write" {
		t.Fatal(err, p.reqs)
	}
}

func TestCheckMCPSessionGrant(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := mcpAnswer(ScopeSession)
	req := ApprovalRequest{SessionID: "s", Kind: "chat"}
	for range 2 {
		if err := g.CheckMCP(ctx, req, mcpCall("gh", "create_issue", "write"), p); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.reqs) != 1 {
		t.Fatal("a Session answer covers the same tool and config:", len(p.reqs))
	}
	// A changed server config ends the grant.
	other := mcpCall("gh", "create_issue", "write")
	other.ConfigHash = "fedcba9876543210"
	if err := g.CheckMCP(ctx, req, other, p); err != nil || len(p.reqs) != 2 {
		t.Fatal(err, len(p.reqs))
	}
	if rows := logRows(t, g); !hasRow(rows, "grant", DecisionAllowed) {
		t.Fatalf("%+v", rows)
	}
}

func TestCheckMCPAlwaysRow(t *testing.T) {
	d := openGateDB(t)
	g := &Gate{Mode: "manual", CronMode: "deny", Timeout: 5 * time.Second, DB: d}
	if err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s"}, mcpCall("gh", "create_issue", "write"), mcpAnswer(ScopeAlways)); err != nil {
		t.Fatal(err)
	}
	rows, err := g.ListAlways(ctx)
	if err != nil || len(rows) != 1 || rows[0].PatternID != "mcp:gh.create_issue" || rows[0].Workdir != "" {
		t.Fatalf("%+v %v", rows, err)
	}
	// A second process reads the row.
	g2 := &Gate{Mode: "manual", CronMode: "deny", Timeout: 5 * time.Second, DB: d}
	p := mcpAnswer(ScopeDeny)
	if err := g2.CheckMCP(ctx, ApprovalRequest{SessionID: "other"}, mcpCall("gh", "create_issue", "write"), p); err != nil || len(p.reqs) != 0 {
		t.Fatal(err, p.reqs)
	}
	changed := mcpCall("gh", "create_issue", "write")
	changed.ConfigHash = "fedcba9876543210"
	if err := g2.CheckMCP(ctx, ApprovalRequest{SessionID: "other"}, changed, p); err == nil || len(p.reqs) != 1 {
		t.Fatal("a changed config hash asks again:", err, len(p.reqs))
	}
}

func TestCheckMCPSendAsksEachTime(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := mcpAnswer(ScopeSession)
	for range 2 {
		if err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s"}, mcpCall("gmail", "send_email", "send"), p); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.reqs) != 2 || !p.reqs[0].NoSession || !p.reqs[0].NoAlways || p.reqs[0].GrantKey != "" {
		t.Fatalf("%d %+v", len(p.reqs), p.reqs[0])
	}
}

func TestCheckMCPHumanOnly(t *testing.T) {
	call := mcpCall("stripe", "create_refund", "human-only")
	want := "BLOCKED (human-only): MCP human-only (stripe · create_refund). This action needs the owner to tap " +
		"Approve each time, and no owner can answer here." + blockTail
	for _, mode := range []string{"manual", "smart", "off"} {
		g := newGate(t, mode, "approve")
		if err := g.SetMode(ctx, "cli", ModeFull); err != nil {
			t.Fatal(err)
		}
		p := mcpAnswer(ScopeSession)
		req := ApprovalRequest{SessionID: "s", Kind: "chat", Scope: "cli"}
		for range 2 {
			if err := g.CheckMCP(ctx, req, call, p); err != nil {
				t.Fatal(mode, err)
			}
		}
		if len(p.reqs) != 2 || !p.reqs[0].HumanOnly || !p.reqs[0].NoSession || !p.reqs[0].NoAlways {
			t.Fatalf("%s: human-only asks each time: %d %+v", mode, len(p.reqs), p.reqs[0])
		}
		for name, r := range map[string]ApprovalRequest{
			"cron":   {SessionID: "c", Kind: "cron"},
			"child":  {SessionID: "c", Kind: "subagent", RootKind: "cron"},
			"member": {SessionID: "m", Kind: "chat", Member: true},
		} {
			if got := blockedReason(t, g.CheckMCP(ctx, r, call, p)); got != want {
				t.Fatalf("%s %s: %q", mode, name, got)
			}
		}
		if got := blockedReason(t, g.CheckMCP(ctx, req, call, nil)); got != want {
			t.Fatalf("%s no prompter: %q", mode, got)
		}
		if !hasRow(logRows(t, g), "human-only", DecisionBlocked) {
			t.Fatal(mode, "no human-only row")
		}
	}
}

func TestCheckMCPModes(t *testing.T) {
	g := newGate(t, "off", "deny")
	p := mcpAnswer(ScopeDeny)
	for _, c := range []MCPCall{mcpCall("gh", "create_issue", "write"), mcpCall("gmail", "send_email", "send")} {
		if err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s", Kind: "cron", Member: true}, c, p); err != nil {
			t.Fatal("mode off runs write and send, before the member and cron rules:", err)
		}
	}
	if len(p.reqs) != 0 || !hasRow(logRows(t, g), "mode-off", DecisionAllowed) {
		t.Fatal(p.reqs)
	}
	g = newGate(t, "manual", "deny")
	if err := g.SetMode(ctx, "telegram:5:0", ModeFull); err != nil {
		t.Fatal(err)
	}
	if err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s", Scope: "telegram:5:0"}, mcpCall("gh", "create_issue", "write"), p); err != nil || len(p.reqs) != 0 {
		t.Fatal(err, p.reqs)
	}
	if !hasRow(logRows(t, g), "mode-full", DecisionAllowed) {
		t.Fatal("no mode-full row")
	}
	// Another topic keeps the config mode.
	if err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s", Scope: "telegram:5:7"}, mcpCall("gh", "create_issue", "write"), p); err == nil || len(p.reqs) != 1 {
		t.Fatal(err, p.reqs)
	}
}

func TestCheckMCPMemberAsksEachTime(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := mcpAnswer(ScopeSession)
	req := ApprovalRequest{SessionID: "m", Kind: "chat", Member: true}
	for range 2 {
		if err := g.CheckMCP(ctx, req, mcpCall("gh", "create_issue", "write"), p); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.reqs) != 2 || !p.reqs[0].NoSession || !p.reqs[0].NoAlways {
		t.Fatalf("%d %+v", len(p.reqs), p.reqs[0])
	}
}

func TestCheckMCPCron(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := mcpAnswer(ScopeOnce)
	for _, c := range []MCPCall{mcpCall("gh", "create_issue", "write"), mcpCall("gmail", "send_email", "send")} {
		want := fmt.Sprintf("BLOCKED: %s call %s.%s in a cron run. Set approvals.cron_mode: approve to allow MCP "+
			"write and send calls in cron jobs.", c.Class, c.Server, c.Tool) + blockTail
		if got := blockedReason(t, g.CheckMCP(ctx, ApprovalRequest{SessionID: "c", Kind: "cron"}, c, p)); got != want {
			t.Fatalf("%q", got)
		}
	}
	g = newGate(t, "manual", "approve")
	if err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "c", Kind: "cron"}, mcpCall("gmail", "send_email", "send"), p); err != nil {
		t.Fatal(err)
	}
	if len(p.reqs) != 0 || !hasRow(logRows(t, g), "cron-mode", DecisionAllowed) {
		t.Fatal(p.reqs)
	}
}

func TestCheckMCPNoSmartJudge(t *testing.T) {
	n := 0
	g := newGate(t, "smart", "deny")
	g.Judge = fixedJudge{n: &n}
	p := mcpAnswer(ScopeOnce)
	if err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s"}, mcpCall("gh", "create_issue", "write"), p); err != nil || len(p.reqs) != 1 || n != 0 {
		t.Fatal(err, len(p.reqs), n)
	}
}

func TestCheckMCPNoPrompterBlocks(t *testing.T) {
	g := newGate(t, "manual", "deny")
	blockedReason(t, g.CheckMCP(ctx, ApprovalRequest{SessionID: "s"}, mcpCall("gh", "create_issue", "write"), nil))
	if !hasRow(logRows(t, g), "no-prompter", DecisionBlocked) {
		t.Fatal("no no-prompter row")
	}
}

func TestCheckMCPReadOnlyAfterTap(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := &mcpPrompter{fn: func(ctx context.Context, _ ApprovalRequest) (Scope, error) {
		if err := g.SetMode(ctx, "cli", ModeReadOnly); err != nil {
			return ScopeDeny, err
		}
		return ScopeOnce, nil
	}}
	err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s", Scope: "cli"}, mcpCall("gh", "create_issue", "write"), p)
	if got := blockedReason(t, err); !strings.Contains(got, "The mode of this topic changed to read-only while the approval waited.") {
		t.Fatal(got)
	}
}

func TestFormatMCPArgs(t *testing.T) {
	long := strings.Repeat("a", 5000)
	args := json.RawMessage(`{"to":"ana@example.com","body":"` + long + `","bcc":"boss@example.com","amount":12,` +
		`"meta":{"a":[1, 2]},"note":"line1\nline2 hunter2-secret"}`)
	want := strings.Join([]string{
		"amount: 12",
		"bcc: boss@example.com",
		"body: " + strings.Repeat("a", 299) + "…",
		`meta: {"a":[1,2]}`,
		"note: line1 line2 ***",
		"to: ana@example.com",
	}, "\n")
	if got := FormatMCPArgs(args, NewRedactor([]string{"hunter2-secret"})); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	// The lines stop at 3000 characters. The block names the keys that it does not show.
	var b strings.Builder
	b.WriteString("{")
	for i := range 12 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"k%02d":"%s"`, i, strings.Repeat("x", 400))
	}
	b.WriteString("}")
	lines := strings.Split(FormatMCPArgs(json.RawMessage(b.String()), nil), "\n")
	if len(lines) != 10 || !strings.HasPrefix(lines[8], "k08: x") || lines[9] != "Not shown: k09, k10, k11" {
		t.Fatalf("%d %q", len(lines), lines[len(lines)-1])
	}
	for in, want := range map[string]string{
		"":                       "(no arguments)",
		"null":                   "(no arguments)",
		"{}":                     "(no arguments)",
		"[1, 2]":                 "arguments: [1,2]",
		`{"k":"hunter2-secret"}`: "k: hunter2-secret",
	} {
		if got := FormatMCPArgs(json.RawMessage(in), nil); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}
