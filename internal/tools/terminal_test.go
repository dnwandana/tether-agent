package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

func (allowAll) CheckCommand(context.Context, *Env, string) error { return nil }

// denyAll is an Approver that blocks each command with reason and records the commands.
type denyAll struct {
	reason string
	seen   *[]string
}

func (d denyAll) CheckCommand(_ context.Context, _ *Env, command string) error {
	if d.seen != nil {
		*d.seen = append(*d.seen, command)
	}
	return &security.BlockedError{Reason: d.reason}
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
	err := g.CheckCommand(ctx, env, "rm -rf /tmp/x")
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
	if err := g.CheckCommand(ctx, env, "ls -la"); err != nil || len(p.reqs) != 1 {
		t.Fatal(err, p.reqs)
	}
}

func TestGateApproverPassesRootKind(t *testing.T) {
	p := &recPrompter{scope: security.ScopeDeny}
	g := GateApprover{Gate: &security.Gate{Mode: "manual", CronMode: "approve"}, Prompter: p}
	env := &Env{SessionID: "c1", Kind: "subagent", RootKind: "cron"}
	if err := g.CheckCommand(ctx, env, "rm -rf /tmp/x"); err != nil || len(p.reqs) != 0 {
		t.Fatal("a child of a cron run follows cron_mode:", err, p.reqs)
	}
}

func TestGateApproverResetSession(t *testing.T) {
	p := &recPrompter{scope: security.ScopeSession}
	g := GateApprover{Gate: &security.Gate{Mode: "manual"}, Prompter: p}
	env := &Env{SessionID: "s1"}
	if err := g.CheckCommand(ctx, env, "rm -rf /tmp/x"); err != nil {
		t.Fatal(err)
	}
	g.ResetSession("s1")
	if err := g.CheckCommand(ctx, env, "rm -rf /tmp/x"); err != nil || len(p.reqs) != 2 {
		t.Fatal("the reset must remove the session answer:", err, len(p.reqs))
	}
	(GateApprover{}).ResetSession("s1")
}

func TestGateApproverSessionScopeHoldsForSession(t *testing.T) {
	p := &recPrompter{scope: security.ScopeSession}
	g := GateApprover{Gate: &security.Gate{Mode: "manual"}, Prompter: p}
	env := &Env{SessionID: "s1"}
	for range 2 {
		if err := g.CheckCommand(ctx, env, "rm -rf /tmp/x"); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.reqs) != 1 {
		t.Fatalf("asked %d times", len(p.reqs))
	}
}

func TestGateApproverNoPrompterBlocksDangerous(t *testing.T) {
	g := GateApprover{Gate: &security.Gate{Mode: "manual"}}
	if err := g.CheckCommand(ctx, &Env{SessionID: "s1"}, "rm -rf /tmp/x"); err == nil || !strings.Contains(err.Error(), "no user is present") {
		t.Fatal(err)
	}
	if err := g.CheckCommand(ctx, &Env{SessionID: "s1"}, "echo hi"); err != nil {
		t.Fatal(err)
	}
}

func TestGateApproverNilGateFailsClosed(t *testing.T) {
	if err := (GateApprover{}).CheckCommand(ctx, &Env{}, "echo hi"); err == nil || !strings.Contains(err.Error(), "approval gate missing") {
		t.Fatal(err)
	}
}

func TestExecThroughRealGateCronDeny(t *testing.T) {
	env := &Env{SessionID: "s1", Kind: "cron", Gate: GateApprover{Gate: &security.Gate{Mode: "manual", CronMode: "deny"}}}
	out := runToolEnvErr(t, &TerminalTool{DefaultTimeout: time.Minute}, `{"command":"rm -rf /tmp/tether-none"}`, env)
	if !strings.Contains(out, "cron jobs run without a user") {
		t.Fatal(out)
	}
}
