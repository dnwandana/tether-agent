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
	_, err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s", Kind: "cron"}, mcpCall("gh", "search", "read"), p)
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
	if _, err := g.CheckMCP(ctx, req, call, p); err != nil || len(p.reqs) != 1 {
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
	if _, err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s"}, mcpCall("gh", "frob", ""), p); err != nil || len(p.reqs) != 1 || p.reqs[0].Class != "write" {
		t.Fatal(err, p.reqs)
	}
}

func TestCheckMCPSessionGrant(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := mcpAnswer(ScopeSession)
	req := ApprovalRequest{SessionID: "s", Kind: "chat"}
	for range 2 {
		if _, err := g.CheckMCP(ctx, req, mcpCall("gh", "create_issue", "write"), p); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.reqs) != 1 {
		t.Fatal("a Session answer covers the same tool and config:", len(p.reqs))
	}
	// A changed server config ends the grant.
	other := mcpCall("gh", "create_issue", "write")
	other.ConfigHash = "fedcba9876543210"
	if _, err := g.CheckMCP(ctx, req, other, p); err != nil || len(p.reqs) != 2 {
		t.Fatal(err, len(p.reqs))
	}
	if rows := logRows(t, g); !hasRow(rows, "grant", DecisionAllowed) {
		t.Fatalf("%+v", rows)
	}
}

func TestCheckMCPAlwaysRow(t *testing.T) {
	d := openGateDB(t)
	g := &Gate{Mode: "manual", CronMode: "deny", Timeout: 5 * time.Second, DB: d}
	if _, err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s"}, mcpCall("gh", "create_issue", "write"), mcpAnswer(ScopeAlways)); err != nil {
		t.Fatal(err)
	}
	rows, err := g.ListAlways(ctx)
	if err != nil || len(rows) != 1 || rows[0].PatternID != "mcp:gh.create_issue" || rows[0].Workdir != "" {
		t.Fatalf("%+v %v", rows, err)
	}
	// A second process reads the row.
	g2 := &Gate{Mode: "manual", CronMode: "deny", Timeout: 5 * time.Second, DB: d}
	p := mcpAnswer(ScopeDeny)
	if _, err := g2.CheckMCP(ctx, ApprovalRequest{SessionID: "other"}, mcpCall("gh", "create_issue", "write"), p); err != nil || len(p.reqs) != 0 {
		t.Fatal(err, p.reqs)
	}
	changed := mcpCall("gh", "create_issue", "write")
	changed.ConfigHash = "fedcba9876543210"
	if _, err := g2.CheckMCP(ctx, ApprovalRequest{SessionID: "other"}, changed, p); err == nil || len(p.reqs) != 1 {
		t.Fatal("a changed config hash asks again:", err, len(p.reqs))
	}
}

func TestCheckMCPSendAsksEachTime(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := mcpAnswer(ScopeSession)
	for range 2 {
		if _, err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s"}, mcpCall("gmail", "send_email", "send"), p); err != nil {
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
			if _, err := g.CheckMCP(ctx, req, call, p); err != nil {
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
			if got := blockedReason(t, mcpErr(g.CheckMCP(ctx, r, call, p))); got != want {
				t.Fatalf("%s %s: %q", mode, name, got)
			}
		}
		if got := blockedReason(t, mcpErr(g.CheckMCP(ctx, req, call, nil))); got != want {
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
		if _, err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s", Kind: "cron", Member: true}, c, p); err != nil {
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
	if _, err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s", Scope: "telegram:5:0"}, mcpCall("gh", "create_issue", "write"), p); err != nil || len(p.reqs) != 0 {
		t.Fatal(err, p.reqs)
	}
	if !hasRow(logRows(t, g), "mode-full", DecisionAllowed) {
		t.Fatal("no mode-full row")
	}
	// Another topic keeps the config mode.
	if _, err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s", Scope: "telegram:5:7"}, mcpCall("gh", "create_issue", "write"), p); err == nil || len(p.reqs) != 1 {
		t.Fatal(err, p.reqs)
	}
}

func TestCheckMCPMemberAsksEachTime(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := mcpAnswer(ScopeSession)
	req := ApprovalRequest{SessionID: "m", Kind: "chat", Member: true}
	for range 2 {
		if _, err := g.CheckMCP(ctx, req, mcpCall("gh", "create_issue", "write"), p); err != nil {
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
		if got := blockedReason(t, mcpErr(g.CheckMCP(ctx, ApprovalRequest{SessionID: "c", Kind: "cron"}, c, p))); got != want {
			t.Fatalf("%q", got)
		}
	}
	g = newGate(t, "manual", "approve")
	if _, err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "c", Kind: "cron"}, mcpCall("gmail", "send_email", "send"), p); err != nil {
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
	if _, err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s"}, mcpCall("gh", "create_issue", "write"), p); err != nil || len(p.reqs) != 1 || n != 0 {
		t.Fatal(err, len(p.reqs), n)
	}
}

func TestCheckMCPNoPrompterBlocks(t *testing.T) {
	g := newGate(t, "manual", "deny")
	blockedReason(t, mcpErr(g.CheckMCP(ctx, ApprovalRequest{SessionID: "s"}, mcpCall("gh", "create_issue", "write"), nil)))
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
	_, err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s", Scope: "cli"}, mcpCall("gh", "create_issue", "write"), p)
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

// mcpErr drops the arguments of a CheckMCP result.
func mcpErr(_ json.RawMessage, err error) error { return err }

// recGuard is a SendGuarder that records its input and gives a fixed answer.
type recGuard struct {
	allow  bool
	reason string
	got    []GuardInput
}

func (g *recGuard) Check(_ context.Context, in GuardInput) (bool, string) {
	g.got = append(g.got, in)
	return g.allow, g.reason
}

const draftBlock = "BLOCKED: this job is draft-only. It can read and write drafts, but it cannot send. Do not try another way."

func cardCall() MCPCall {
	c := mcpCall("gmail", "send_email", "send")
	c.Args = json.RawMessage(`{"to":"vendor@acme.io","body":"pay with 4111 1111 1111 1111"}`)
	return c
}

func TestCheckMCPDraftOnly(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := mcpAnswer(ScopeOnce)
	// An Always row for create_issue must not open a draft-only run.
	if _, err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s"}, mcpCall("gh", "create_issue", "write"), mcpAnswer(ScopeAlways)); err != nil {
		t.Fatal(err)
	}
	g.Mode = "off"
	req := ApprovalRequest{SessionID: "c", Kind: "cron", DraftOnly: true}
	if args, err := g.CheckMCP(ctx, req, mcpCall("gmail", "create_draft", "write"), p); err != nil || args != nil {
		t.Fatal(args, err)
	}
	for _, c := range []MCPCall{mcpCall("gmail", "send_email", "send"), mcpCall("gh", "create_issue", "write"),
		mcpCall("gmail", "send_draft", "write"), mcpCall("bank", "pay", "human-only")} {
		if got := blockedReason(t, mcpErr(g.CheckMCP(ctx, req, c, p))); got != draftBlock {
			t.Fatalf("%s: %q", c.Tool, got)
		}
	}
	if args, err := g.CheckMCP(ctx, req, mcpCall("gh", "search", "read"), p); err != nil || args != nil {
		t.Fatal(args, err)
	}
	if len(p.reqs) != 0 || !hasRow(logRows(t, g), "draft-only", DecisionBlocked) || !hasRow(logRows(t, g), "draft-only", DecisionAllowed) {
		t.Fatal(p.reqs)
	}
}

func TestCheckMCPSendFinding(t *testing.T) {
	g := newGate(t, "off", "deny")
	// No finding: mode off runs the send with no prompt.
	p := mcpAnswer(ScopeOnce)
	if args, err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s"}, mcpCall("gmail", "send_email", "send"), p); err != nil || args != nil || len(p.reqs) != 0 {
		t.Fatal(args, err, len(p.reqs))
	}
	// A phone number under an address key is the target, not data.
	phone := mcpCall("sms", "send", "send")
	phone.Args = json.RawMessage(`{"to":"+94 77 123 4567","body":"hi"}`)
	if _, err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s"}, phone, p); err != nil || len(p.reqs) != 0 {
		t.Fatal(err, len(p.reqs))
	}
	// A finding asks, also in mode off.
	p = mcpAnswer(ScopeRedacted)
	args, err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s"}, cardCall(), p)
	if err != nil || len(p.reqs) != 1 {
		t.Fatal(err, len(p.reqs))
	}
	r := p.reqs[0]
	if len(r.Findings) != 1 || r.Findings[0].Kind != "card" || !r.NoSession || !r.NoAlways {
		t.Fatalf("%+v", r)
	}
	var sent map[string]string
	if err := json.Unmarshal(args, &sent); err != nil || strings.Contains(sent["body"], "4111") ||
		!strings.Contains(sent["body"], "[card ••••1111]") || sent["to"] != "vendor@acme.io" {
		t.Fatalf("%s %v", args, err)
	}
	if rows := logRows(t, g); rows[0].Answer != "redacted" || rows[0].Decision != DecisionAllowed {
		t.Fatalf("%+v", rows[0])
	}
	if args, err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s"}, cardCall(), mcpAnswer(ScopeOnce)); err != nil || args != nil {
		t.Fatal(args, err)
	}
	blockedReason(t, mcpErr(g.CheckMCP(ctx, ApprovalRequest{SessionID: "s"}, cardCall(), mcpAnswer(ScopeDeny))))
	// "redacted" for a request with no finding is a deny.
	g.Mode = "manual"
	blockedReason(t, mcpErr(g.CheckMCP(ctx, ApprovalRequest{SessionID: "s", Member: true}, mcpCall("gmail", "send_email", "send"), mcpAnswer(ScopeRedacted))))
}

func TestCheckMCPSendFindingInCron(t *testing.T) {
	g := newGate(t, "manual", "approve")
	want := "BLOCKED: the arguments of gmail.send_email hold sensitive data (card). A cron run cannot send it."
	if got := blockedReason(t, mcpErr(g.CheckMCP(ctx, ApprovalRequest{SessionID: "c", Kind: "cron"}, cardCall(), nil))); got != want {
		t.Fatalf("%q", got)
	}
	if !hasRow(logRows(t, g), "outbound-scan", DecisionBlocked) {
		t.Fatal("no outbound-scan row")
	}
}

func TestCheckMCPGuard(t *testing.T) {
	g := newGate(t, "manual", "approve")
	guard := &recGuard{reason: "the purpose names no email"}
	g.Guard = guard
	req := ApprovalRequest{SessionID: "c", Kind: "cron", JobName: "competitor-prices", JobPurpose: "Check the prices.",
		OutsideSources: []string{"web_fetch"}, OutsideHosts: []string{"pastebin.com"}, ToolCounts: map[string]int{"web_fetch": 2}}
	call := mcpCall("gmail", "send_email", "send")
	call.Args = json.RawMessage(`{"to":"x@y.io","body":"hello"}`)
	want := "BLOCKED: model_guard held gmail.send_email: the purpose names no email"
	if got := blockedReason(t, mcpErr(g.CheckMCP(ctx, req, call, nil))); got != want {
		t.Fatalf("%q", got)
	}
	in := guard.got[0]
	if in.SessionID != "c" || in.JobName != "competitor-prices" || in.JobPurpose != "Check the prices." ||
		in.Sources[0] != "web_fetch" || in.Hosts[0] != "pastebin.com" || in.ToolCounts["web_fetch"] != 2 ||
		in.Action != "call the MCP tool gmail.send_email (class send) with the arguments: body, to" ||
		strings.Contains(fmt.Sprint(in), "hello") {
		t.Fatalf("%+v", in)
	}
	guard.allow = true
	if args, err := g.CheckMCP(ctx, req, call, nil); err != nil || args != nil {
		t.Fatal(args, err)
	}
	rows := logRows(t, g)
	if !hasRow(rows, "model-guard", DecisionBlocked) || !hasRow(rows, "model-guard", DecisionAllowed) || !hasRow(rows, "cron-mode", DecisionAllowed) {
		t.Fatalf("%+v", rows)
	}
	// No guard call with no outside source, outside cron, or with cron_mode deny.
	n := len(guard.got)
	noSrc := req
	noSrc.OutsideSources = nil
	_, _ = g.CheckMCP(ctx, noSrc, call, nil)
	_, _ = g.CheckMCP(ctx, ApprovalRequest{SessionID: "s", OutsideSources: []string{"web_fetch"}}, call, mcpAnswer(ScopeOnce))
	g.CronMode = "deny"
	_, _ = g.CheckMCP(ctx, req, call, nil)
	if len(guard.got) != n {
		t.Fatalf("the guard ran %d more times", len(guard.got)-n)
	}
}

func TestCheckMCPRedactedAfterReadOnlyTap(t *testing.T) {
	g := newGate(t, "manual", "deny")
	p := &mcpPrompter{fn: func(ctx context.Context, _ ApprovalRequest) (Scope, error) {
		if err := g.SetMode(ctx, "cli", ModeReadOnly); err != nil {
			return ScopeDeny, err
		}
		return ScopeRedacted, nil
	}}
	err := mcpErr(g.CheckMCP(ctx, ApprovalRequest{SessionID: "s", Scope: "cli"}, cardCall(), p))
	if got := blockedReason(t, err); !strings.Contains(got, "changed to read-only while the approval waited") {
		t.Fatal(got)
	}
}

// TestCheckMCPSendFindingLogPreview checks that no log row of an MCP send with findings keeps
// a card, an IBAN, or a phone number in clear text.
func TestCheckMCPSendFindingLogPreview(t *testing.T) {
	clear := []string{"4111", "1111 1111", "GB82 WEST", "5698 7654", "77 123 4567"}
	check := func(rows []LogRow) {
		t.Helper()
		for _, r := range rows {
			for _, c := range clear {
				if strings.Contains(r.Preview, c) {
					t.Fatalf("the preview keeps %q: %q", c, r.Preview)
				}
			}
		}
	}
	g := newGate(t, "manual", "approve")
	call := mcpCall("gmail", "send_email", "send")
	call.Args = json.RawMessage(`{"to":"vendor@acme.io","body":"pay 4111 1111 1111 1111 or GB82 WEST 1234 5698 7654 32, call +94 77 123 4567"}`)
	if _, err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s"}, call, mcpAnswer(ScopeOnce)); err != nil {
		t.Fatal(err)
	}
	rows := logRows(t, g)
	if len(rows) != 1 || !strings.Contains(rows[0].Preview, "[card ••••1111]") ||
		!strings.Contains(rows[0].Preview, "[IBAN GB82••••5432]") || !strings.Contains(rows[0].Preview, "vendor@acme.io") {
		t.Fatalf("%+v", rows)
	}
	check(rows)
	_ = mcpErr(g.CheckMCP(ctx, ApprovalRequest{SessionID: "s"}, cardCall(), mcpAnswer(ScopeDeny)))
	_ = mcpErr(g.CheckMCP(ctx, ApprovalRequest{SessionID: "c", Kind: "cron"}, cardCall(), nil))
	_, _ = g.CheckMCP(ctx, ApprovalRequest{SessionID: "s"}, cardCall(), mcpAnswer(ScopeRedacted))
	check(logRows(t, g))
	// The cut of a long value can split a card. The digits before the cut are masked too.
	long := mcpCall("gmail", "send_email", "send")
	long.Args = mustJSON(t, map[string]string{"body": strings.Repeat("a", 285) + " 4111 1111 1111 1111"})
	if _, err := g.CheckMCP(ctx, ApprovalRequest{SessionID: "s"}, long, mcpAnswer(ScopeOnce)); err != nil {
		t.Fatal(err)
	}
	rows = logRows(t, g)
	if p := rows[0].Preview; strings.Contains(p, "41") || strings.Contains(p, "11") {
		t.Fatalf("the preview keeps a part of the card: %q", p)
	}
}
