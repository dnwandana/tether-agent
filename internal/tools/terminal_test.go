package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/security"
)

func TestRunsAndCombinesOutput(t *testing.T) {
	out := runExec(t, &TerminalTool{DefaultTimeout: 120 * time.Second}, `{"command":"echo out; echo err 1>&2; exit 3"}`)
	if !strings.Contains(out, "out") || !strings.Contains(out, "err") || !strings.Contains(out, "exit code: 3") {
		t.Fatal(out)
	}
}

func TestExitZeroHasNoExitCode(t *testing.T) {
	out := runExec(t, &TerminalTool{DefaultTimeout: time.Minute}, `{"command":"echo fine"}`)
	if out != "fine\n" {
		t.Fatalf("%q", out)
	}
}

func TestTimeoutKillsProcessGroup(t *testing.T) {
	start := time.Now()
	out := runExec(t, &TerminalTool{DefaultTimeout: 120 * time.Second}, `{"command":"sleep 30 & sleep 30","timeout_s":1}`)
	if time.Since(start) > 3*time.Second || !strings.Contains(out, "timed out after 1s") {
		t.Fatal(out)
	}
}

func TestLeftoverChildDoesNotBlock(t *testing.T) {
	start := time.Now()
	out := runExec(t, &TerminalTool{DefaultTimeout: time.Minute}, `{"command":"sleep 30 & echo done"}`)
	if time.Since(start) > 10*time.Second || !strings.Contains(out, "done") {
		t.Fatal(out)
	}
}

func TestSecretsStripped(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-should-not-leak")
	out := runExec(t, &TerminalTool{DefaultTimeout: time.Minute}, `{"command":"env"}`)
	if strings.Contains(out, "sk-should-not-leak") {
		t.Fatal("secret leaked to child")
	}
}

func TestPassthroughKeepsSecret(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp-pass")
	tool := &TerminalTool{DefaultTimeout: time.Minute, Passthrough: func(*Env) []string { return []string{"GITHUB_TOKEN"} }}
	out := runExec(t, tool, `{"command":"echo $GITHUB_TOKEN"}`)
	if !strings.Contains(out, "ghp-pass") {
		t.Fatal(out)
	}
}

func TestBackgroundReturnsPidAndLog(t *testing.T) {
	spill := t.TempDir()
	out := runExec(t, &TerminalTool{DefaultTimeout: time.Minute, SpillDir: spill}, `{"command":"echo bg-ok","background":true}`)
	var r struct {
		PID     int    `json:"pid"`
		LogPath string `json:"log_path"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err, out)
	}
	if r.PID == 0 || !strings.HasPrefix(filepath.Base(r.LogPath), "bg-") || filepath.Dir(r.LogPath) != spill {
		t.Fatal(out)
	}
	eventually(t, func() bool { b, _ := os.ReadFile(r.LogPath); return strings.Contains(string(b), "bg-ok") })
}

func TestBackgroundNeedsSpillDir(t *testing.T) {
	out := runToolEnvErr(t, &TerminalTool{DefaultTimeout: time.Minute}, `{"command":"true","background":true}`, &Env{Gate: allowAll{}})
	if !strings.Contains(out, "spill") {
		t.Fatal(out)
	}
}

func TestWorkdir(t *testing.T) {
	dir := t.TempDir()
	want, _ := filepath.EvalSymlinks(dir)
	out := runExec(t, &TerminalTool{DefaultTimeout: time.Minute}, `{"command":"pwd -P","workdir":`+jsonString(dir)+`}`)
	if strings.TrimSpace(out) != want {
		t.Fatalf("%q != %q", out, want)
	}
}

func TestRelativeWorkdirUsesEnvWorkDir(t *testing.T) {
	env := newEnv(t)
	if err := os.Mkdir(filepath.Join(env.WorkDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(filepath.Join(env.WorkDir, "sub"))
	env.Gate = allowAll{}
	out := runToolEnv(t, &TerminalTool{DefaultTimeout: time.Minute}, `{"command":"pwd -P","workdir":"sub"}`, env)
	if strings.TrimSpace(out) != want {
		t.Fatalf("%q != %q", out, want)
	}
}

func TestMissingWorkdirIsError(t *testing.T) {
	out := runExecErr(t, &TerminalTool{DefaultTimeout: time.Minute}, `{"command":"pwd","workdir":"/no/such/dir"}`)
	if !strings.Contains(out, "/no/such/dir") {
		t.Fatal(out)
	}
}

func TestEmptyCommandIsError(t *testing.T) {
	out := runExecErr(t, &TerminalTool{DefaultTimeout: time.Minute}, `{"command":"  "}`)
	if !strings.Contains(out, "command is required") {
		t.Fatal(out)
	}
}

func TestBinaryOutputStaysValidUTF8(t *testing.T) {
	out := runExec(t, &TerminalTool{DefaultTimeout: time.Minute}, `{"command":"printf 'a\\377\\376b\\342\\202'; head -c 4096 /dev/urandom"}`)
	if !utf8.ValidString(out) || !strings.HasPrefix(out, "a") {
		t.Fatalf("%q", out[:10])
	}
	if _, err := json.Marshal(out); err != nil {
		t.Fatal(err)
	}
}

func TestOutputCapKeepsHeadAndTail(t *testing.T) {
	tool := &TerminalTool{DefaultTimeout: time.Minute, MaxOutputBytes: 1000}
	out := runExec(t, tool, `{"command":"echo START; head -c 100000 /dev/zero | tr '\\0' x; echo; echo END"}`)
	if !strings.HasPrefix(out, "START") || !strings.Contains(out, "END") || !strings.Contains(out, "bytes of output omitted") || len(out) > 1200 {
		t.Fatalf("%d %q", len(out), out)
	}
}

// jsonString returns s as a JSON string literal.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// allowAll is an Approver that lets each command run.
type allowAll struct{}

func (allowAll) CheckCommand(context.Context, *Env, string, string) error { return nil }

func (allowAll) CheckScript(context.Context, *Env, string, string) error { return nil }

// denyAll is an Approver that blocks each command with reason and records the commands.
type denyAll struct {
	reason string
	seen   *[]string
}

func (d denyAll) CheckCommand(_ context.Context, _ *Env, command, _ string) error {
	if d.seen != nil {
		*d.seen = append(*d.seen, command)
	}
	return &security.BlockedError{Reason: d.reason}
}

func (d denyAll) CheckScript(ctx context.Context, env *Env, code, summary string) error {
	return d.CheckCommand(ctx, env, code, summary)
}

// runExec runs tool with a new Env that lets each command run.
func runExec(t *testing.T, tool Tool, args string) string {
	t.Helper()
	env := newEnv(t)
	env.Gate = allowAll{}
	return runToolEnv(t, tool, args, env)
}

// runExecErr runs tool with a new Env that lets each command run, and expects an error.
func runExecErr(t *testing.T, tool Tool, args string) string {
	t.Helper()
	env := newEnv(t)
	env.Gate = allowAll{}
	return runToolEnvErr(t, tool, args, env)
}

func TestExecBlockedByGateReturnsReason(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	env := &Env{Gate: denyAll{reason: "BLOCKED by user: recursive delete"}}
	out := runToolEnvErr(t, &TerminalTool{DefaultTimeout: time.Minute}, `{"command":"rm -rf /tmp/x; touch `+marker+`"}`, env)
	if !strings.Contains(out, "BLOCKED by user: recursive delete") {
		t.Fatal(out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the blocked command ran")
	}
}

func TestExecBackgroundBlockedByGate(t *testing.T) {
	spill := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	var seen []string
	env := &Env{Gate: denyAll{reason: "BLOCKED: no", seen: &seen}}
	out := runToolEnvErr(t, &TerminalTool{DefaultTimeout: time.Minute, SpillDir: spill},
		`{"command":"touch `+marker+`","background":true}`, env)
	if !strings.Contains(out, "BLOCKED: no") || len(seen) != 1 || seen[0] != "touch "+marker {
		t.Fatal(out, seen)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the blocked background command ran")
	}
	if entries, _ := os.ReadDir(spill); len(entries) != 0 {
		t.Fatal("a log file was made for a blocked command")
	}
}

func TestExecNoGateFailsClosed(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	for _, env := range []*Env{{}, nil} {
		res := dispatch(t, &TerminalTool{DefaultTimeout: time.Minute}, `{"command":"touch `+marker+`"}`, env)
		if !res.IsError || !strings.Contains(res.Text, "approval gate missing") {
			t.Fatal(res)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the command ran with no gate")
	}
}

// recPrompter records the approval requests and gives scope.
type recPrompter struct {
	scope security.Scope
	reqs  []security.ApprovalRequest
}

func (p *recPrompter) PromptApproval(_ context.Context, req security.ApprovalRequest) (security.Scope, error) {
	p.reqs = append(p.reqs, req)
	return p.scope, nil
}

func TestGateApproverBuildsRequest(t *testing.T) {
	p := &recPrompter{scope: security.ScopeDeny}
	g := GateApprover{Gate: &security.Gate{Mode: "manual"}, Prompter: p}
	env := &Env{SessionID: "s1", SessionKey: "cli:ana", Kind: "subagent", Values: map[string]any{"child_name": "2/3"}}
	err := g.CheckCommand(ctx, env, "rm -rf /tmp/x", "")
	var be *security.BlockedError
	if !errors.As(err, &be) || !strings.Contains(be.Reason, "BLOCKED by user") {
		t.Fatal(err)
	}
	if len(p.reqs) != 1 {
		t.Fatal(p.reqs)
	}
	r := p.reqs[0]
	if r.SessionID != "s1" || r.SessionKey != "cli:ana" || r.Kind != "subagent" || r.ChildName != "2/3" || r.Command != "rm -rf /tmp/x" || r.PatternID == "" {
		t.Fatalf("%+v", r)
	}
	// A safe command needs no question.
	if err := g.CheckCommand(ctx, env, "ls -la", ""); err != nil || len(p.reqs) != 1 {
		t.Fatal(err, p.reqs)
	}
}

func TestGateApproverPassesSummary(t *testing.T) {
	p := &recPrompter{scope: security.ScopeDeny}
	g := GateApprover{Gate: &security.Gate{Mode: "manual"}, Prompter: p}
	err := g.CheckCommand(ctx, &Env{SessionID: "s1"}, "rm -rf /tmp/x", "Delete the old build folder.")
	if len(p.reqs) != 1 || p.reqs[0].Summary != "Delete the old build folder." {
		t.Fatalf("%+v", p.reqs)
	}
	// The gate does not read the summary. The text for the model keeps the pattern name.
	if err == nil || !strings.Contains(err.Error(), "BLOCKED by user: delete in root path.") ||
		strings.Contains(err.Error(), "old build folder") {
		t.Fatal(err)
	}
	// An empty summary gives an empty field.
	_ = g.CheckCommand(ctx, &Env{SessionID: "s1"}, "rm -rf /tmp/x", "")
	if len(p.reqs) != 2 || p.reqs[1].Summary != "" {
		t.Fatalf("%+v", p.reqs)
	}
}

func TestGateApproverPassesRootKind(t *testing.T) {
	p := &recPrompter{scope: security.ScopeDeny}
	g := GateApprover{Gate: &security.Gate{Mode: "manual", CronMode: "approve"}, Prompter: p}
	env := &Env{SessionID: "c1", Kind: "subagent", RootKind: "cron"}
	if err := g.CheckCommand(ctx, env, "rm -rf /tmp/x", ""); err != nil || len(p.reqs) != 0 {
		t.Fatal("a child of a cron run follows cron_mode:", err, p.reqs)
	}
}

func TestGateApproverResetSession(t *testing.T) {
	p := &recPrompter{scope: security.ScopeSession}
	g := GateApprover{Gate: &security.Gate{Mode: "manual"}, Prompter: p}
	env := &Env{SessionID: "s1"}
	if err := g.CheckCommand(ctx, env, "rm -rf /tmp/x", ""); err != nil {
		t.Fatal(err)
	}
	g.ResetSession("s1")
	if err := g.CheckCommand(ctx, env, "rm -rf /tmp/x", ""); err != nil || len(p.reqs) != 2 {
		t.Fatal("the reset must remove the session answer:", err, len(p.reqs))
	}
	(GateApprover{}).ResetSession("s1")
}

func TestGateApproverSessionScopeHoldsForSession(t *testing.T) {
	p := &recPrompter{scope: security.ScopeSession}
	g := GateApprover{Gate: &security.Gate{Mode: "manual"}, Prompter: p}
	env := &Env{SessionID: "s1"}
	for range 2 {
		if err := g.CheckCommand(ctx, env, "rm -rf /tmp/x", ""); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.reqs) != 1 {
		t.Fatalf("asked %d times", len(p.reqs))
	}
}

func TestGateApproverNoPrompterBlocksDangerous(t *testing.T) {
	g := GateApprover{Gate: &security.Gate{Mode: "manual"}}
	if err := g.CheckCommand(ctx, &Env{SessionID: "s1"}, "rm -rf /tmp/x", ""); err == nil || !strings.Contains(err.Error(), "no user is present") {
		t.Fatal(err)
	}
	if err := g.CheckCommand(ctx, &Env{SessionID: "s1"}, "echo hi", ""); err != nil {
		t.Fatal(err)
	}
}

func TestGateApproverNilGateFailsClosed(t *testing.T) {
	if err := (GateApprover{}).CheckCommand(ctx, &Env{}, "echo hi", ""); err == nil || !strings.Contains(err.Error(), "approval gate missing") {
		t.Fatal(err)
	}
}

var _ PermissionAsker = GateApprover{}

// acpRequest is a permission request as the ACP bridge builds it.
func acpRequest() security.ApprovalRequest {
	return security.ApprovalRequest{
		SessionID:   "from-the-caller",
		Summary:     "Edit internal/acp/client.go",
		Description: "claude-code wants to edit a file",
		Command:     "internal/acp/client.go\n- return nil\n+ return c.close(ctx)",
		PatternID:   "acp:claude-code:acp-sess-1:edit",
		NoAlways:    true,
	}
}

func TestGateApproverAskPermissionBuildsRequest(t *testing.T) {
	p := &recPrompter{scope: security.ScopeOnce}
	// Mode "off" must not skip the question: AskACP ignores approvals.mode.
	var a Approver = GateApprover{Gate: &security.Gate{Mode: "off"}, Prompter: p}
	pa, ok := a.(PermissionAsker)
	if !ok {
		t.Fatal("GateApprover must implement PermissionAsker")
	}
	env := &Env{SessionID: "s1", SessionKey: "telegram:dm:5", Kind: "chat", RootKind: "",
		Values: map[string]any{"child_name": "2/3"}}
	scope, err := pa.AskPermission(ctx, env, acpRequest())
	if err != nil || scope != security.ScopeOnce {
		t.Fatal(scope, err)
	}
	if len(p.reqs) != 1 {
		t.Fatalf("asked %d times, want 1", len(p.reqs))
	}
	r, want := p.reqs[0], acpRequest()
	if r.SessionID != "s1" || r.SessionKey != "telegram:dm:5" || r.Kind != "chat" || r.RootKind != "" {
		t.Fatalf("the env fields are wrong: %+v", r)
	}
	if r.Summary != want.Summary || r.Description != want.Description || r.Command != want.Command ||
		r.PatternID != want.PatternID || !r.NoAlways || r.NoSession {
		t.Fatalf("the request fields must pass unchanged: %+v", r)
	}
	if r.ChildName != "" {
		t.Fatalf("a coding agent is not a delegate_task child, ChildName = %q", r.ChildName)
	}
}

func TestGateApproverAskPermissionSessionMemory(t *testing.T) {
	p := &recPrompter{scope: security.ScopeSession}
	g := GateApprover{Gate: &security.Gate{Mode: "manual"}, Prompter: p}
	env := &Env{SessionID: "s1"}
	for range 2 {
		scope, err := g.AskPermission(ctx, env, acpRequest())
		if err != nil || scope != security.ScopeSession {
			t.Fatal(scope, err)
		}
	}
	if len(p.reqs) != 1 {
		t.Fatalf("the session answer must hold, asked %d times", len(p.reqs))
	}
	// Another Tether session has no grant.
	if _, err := g.AskPermission(ctx, &Env{SessionID: "s2"}, acpRequest()); err != nil || len(p.reqs) != 2 {
		t.Fatal(err, len(p.reqs))
	}
}

func TestGateApproverAskPermissionNilGate(t *testing.T) {
	scope, err := (GateApprover{}).AskPermission(ctx, &Env{SessionID: "s1"}, acpRequest())
	if scope != security.ScopeDeny || err == nil || !strings.Contains(err.Error(), "approval gate missing") {
		t.Fatal(scope, err)
	}
}

func TestGateApproverAskPermissionNoPrompter(t *testing.T) {
	g := GateApprover{Gate: &security.Gate{Mode: "manual"}}
	for _, env := range []*Env{{SessionID: "s1"}, nil} {
		scope, err := g.AskPermission(ctx, env, acpRequest())
		if scope != security.ScopeDeny || err != nil {
			t.Fatal(scope, err)
		}
	}
}

func TestExecThroughRealGateCronDeny(t *testing.T) {
	env := &Env{SessionID: "s1", Kind: "cron", Gate: GateApprover{Gate: &security.Gate{Mode: "manual", CronMode: "deny"}}}
	out := runToolEnvErr(t, &TerminalTool{DefaultTimeout: time.Minute}, `{"command":"rm -rf /tmp/tether-none"}`, env)
	if !strings.Contains(out, "cron jobs run without a user") {
		t.Fatal(out)
	}
}

func TestTerminalChildEnvInGateway(t *testing.T) {
	t.Setenv("TETHER_GATEWAY", "")
	tt := &TerminalTool{InGateway: true}
	has := func(env []string) bool { return slices.Contains(env, GatewayEnv) }
	if !has(tt.childEnv(&Env{Kind: "chat"})) {
		t.Fatal("a chat turn in the gateway must give the child TETHER_GATEWAY=1")
	}
	if has(tt.childEnv(&Env{Kind: "cron"})) || has(tt.childEnv(&Env{Kind: "child", RootKind: "cron"})) {
		t.Fatal("a cron run keeps the usual environment")
	}
	if has((&TerminalTool{}).childEnv(&Env{Kind: "chat"})) {
		t.Fatal("outside the gateway, no TETHER_GATEWAY")
	}
}

// recApprover blocks each command and records the summary that it gets.
type recApprover struct{ summaries *[]string }

func (r recApprover) CheckCommand(_ context.Context, _ *Env, _, summary string) error {
	*r.summaries = append(*r.summaries, summary)
	return &security.BlockedError{Reason: "BLOCKED"}
}

func (r recApprover) CheckScript(ctx context.Context, env *Env, code, summary string) error {
	return r.CheckCommand(ctx, env, code, summary)
}

func TestGateApproverCheckScriptBuildsRequest(t *testing.T) {
	var _ ScriptApprover = GateApprover{}
	p := &recPrompter{scope: security.ScopeDeny}
	g := GateApprover{Gate: &security.Gate{Mode: "manual"}, Prompter: p}
	env := &Env{SessionID: "s1", SessionKey: "cli:ana", Kind: "subagent", Values: map[string]any{"child_name": "2/3"}}
	err := g.CheckScript(ctx, env, "print(1)", "Print one.")
	var be *security.BlockedError
	if !errors.As(err, &be) || !strings.Contains(be.Reason, "BLOCKED by user") {
		t.Fatal(err)
	}
	if len(p.reqs) != 1 {
		t.Fatal(p.reqs)
	}
	r := p.reqs[0]
	if r.SessionID != "s1" || r.SessionKey != "cli:ana" || r.Kind != "subagent" || r.ChildName != "2/3" ||
		r.Command != "print(1)" || r.Summary != "Print one." || r.PatternID != security.ScriptPatternID || !r.NoAlways {
		t.Fatalf("%+v", r)
	}
	if err := (GateApprover{}).CheckScript(ctx, env, "print(1)", ""); err == nil || !strings.Contains(err.Error(), "approval gate missing") {
		t.Fatal(err)
	}
}

func TestTerminalPassesCleanSummary(t *testing.T) {
	var got []string
	env := newEnv(t)
	env.Gate = recApprover{summaries: &got}
	tool := &TerminalTool{DefaultTimeout: time.Minute}
	runToolEnvErr(t, tool, `{"command":"ls","description":"  List\tthe\nfiles.\u202E "}`, env)
	runToolEnvErr(t, tool, `{"command":"ls"}`, env)
	if !slices.Equal(got, []string{"List the files.", ""}) {
		t.Fatalf("%q", got)
	}
}
