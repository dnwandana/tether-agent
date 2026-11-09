package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/acp"
	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/security"
)

// fakeDelegator records each Delegation and the deadline of its ctx.
type fakeDelegator struct {
	mu       sync.Mutex
	calls    []acp.Delegation
	deadline time.Time
	run      func(ctx context.Context, d acp.Delegation) (acp.Outcome, error)
}

func (f *fakeDelegator) Delegate(ctx context.Context, d acp.Delegation) (acp.Outcome, error) {
	f.mu.Lock()
	f.calls = append(f.calls, d)
	f.deadline, _ = ctx.Deadline()
	run := f.run
	f.mu.Unlock()
	if run != nil {
		return run(ctx, d)
	}
	return acp.Outcome{Session: "new", StopReason: "end_turn", Text: "done", ToolCalls: 2}, nil
}

func (f *fakeDelegator) last(t *testing.T) acp.Delegation {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("Delegate was not called")
	}
	return f.calls[len(f.calls)-1]
}

// askGate is an Approver that also implements PermissionAsker.
type askGate struct {
	scope security.Scope
	got   *security.ApprovalRequest
}

func (askGate) CheckCommand(context.Context, *Env, string, string) error { return nil }

func (g askGate) AskPermission(_ context.Context, _ *Env, req security.ApprovalRequest) (security.Scope, error) {
	*g.got = req
	return g.scope, nil
}

// plainGate is an Approver with no AskPermission.
type plainGate struct{}

func (plainGate) CheckCommand(context.Context, *Env, string, string) error { return nil }

func acpConfig(allow ...string) config.ACPConfig {
	return config.ACPConfig{Enabled: true, IdleTimeoutS: 300, MaxSessions: 2, TurnTimeoutS: 1800,
		WorkdirAllowlist: allow,
		Agents: map[string]config.ACPAgent{
			"codex":       {Enabled: true, PermissionPreset: "auto"},
			"claude-code": {Enabled: true, PermissionPreset: "auto"},
			"aider":       {Enabled: false, PermissionPreset: "auto", Command: []string{"aider-acp"}},
		}}
}

func acpEnv(t *testing.T) *Env {
	env := newEnv(t)
	env.SessionID, env.SessionKey, env.Channel = "s1", "telegram:1", "telegram"
	return env
}

// realDir resolves the symlinks of dir. t.TempDir can be under a symlink, for example /var
// on macOS.
func realDir(t *testing.T, dir string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func acpArgs(workdir, extra string) string {
	s := `{"agent":"claude-code","task":"fix the bug"`
	if workdir != "" {
		s += `,"workdir":"` + workdir + `"`
	}
	return s + extra + `}`
}

func TestACPDelegateSchema(t *testing.T) {
	tool := NewACPDelegateTool(acpConfig(), &fakeDelegator{}, time.Minute)
	if tool.Name() != "acp_delegate" || tool.Toolset() != SetDelegation || tool.ParallelSafe() {
		t.Fatalf("name %q, toolset %q, parallel %v", tool.Name(), tool.Toolset(), tool.ParallelSafe())
	}
	sc := tool.Schema()
	var s struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(sc.Parameters, &s); err != nil {
		t.Fatal(err)
	}
	if got := s.Properties["agent"].Enum; !reflect.DeepEqual(got, []string{"claude-code", "codex"}) {
		t.Fatalf("agent enum = %q", got)
	}
	if got := s.Properties["permission_preset"].Enum; !reflect.DeepEqual(got, []string{"all", "risky", "auto", "folder"}) {
		t.Fatalf("preset enum = %q", got)
	}
	for _, k := range []string{"task", "workdir", "model", "timeout_s"} {
		if _, ok := s.Properties[k]; !ok {
			t.Fatalf("no property %q", k)
		}
	}
	if !reflect.DeepEqual(s.Required, []string{"agent", "task"}) {
		t.Fatalf("required = %q", s.Required)
	}
	// The provider prompt cache needs the same bytes on each turn.
	for range 20 {
		if again := tool.Schema(); string(again.Parameters) != string(sc.Parameters) || again.Description != sc.Description {
			t.Fatal("the schema bytes change between calls")
		}
	}
}

func TestACPDelegateChecks(t *testing.T) {
	allow := t.TempDir()
	file := filepath.Join(allow, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	missing := filepath.Join(allow, "nope")
	cases := []struct {
		name string
		cfg  config.ACPConfig
		env  func(*Env)
		args string
		want string
	}{
		{"cron", acpConfig(allow), func(e *Env) { e.Kind = "cron" }, acpArgs(allow, ""),
			"acp: acp_delegate runs only in a chat session, not in a cron job."},
		{"child of cron", acpConfig(allow), func(e *Env) { e.Kind, e.RootKind = "subagent", "cron" }, acpArgs(allow, ""),
			"acp: acp_delegate runs only in a chat session, not in a cron job."},
		{"no session", acpConfig(allow), func(e *Env) { e.SessionID = "" }, acpArgs(allow, ""),
			"acp: acp_delegate needs a chat session. This run has no session."},
		{"unknown agent", acpConfig(allow), nil, `{"agent":"gemini","task":"x","workdir":"` + allow + `"}`,
			`acp: agent "gemini" is unknown or disabled. Enabled agents: claude-code, codex.`},
		{"disabled agent", acpConfig(allow), nil, `{"agent":"aider","task":"x","workdir":"` + allow + `"}`,
			`acp: agent "aider" is unknown or disabled. Enabled agents: claude-code, codex.`},
		{"empty task", acpConfig(allow), nil, `{"agent":"claude-code","task":"  ","workdir":"` + allow + `"}`,
			"acp: give task, the full brief for the coding agent."},
		{"looser preset", acpConfig(allow), nil, acpArgs(allow, `,"permission_preset":"folder"`),
			`acp: permission_preset "folder" is looser than the config preset "auto". Use the config preset or a stricter one.`},
		{"unknown preset", acpConfig(allow), nil, acpArgs(allow, `,"permission_preset":"yolo"`),
			`acp: permission_preset "yolo" is not known. Use one of: all, risky, auto, folder.`},
		{"empty allowlist", acpConfig(), nil, acpArgs(allow, ""),
			"acp: acp.workdir_allowlist is empty. Add the folder of your projects to acp.workdir_allowlist."},
		{"no workdir", acpConfig(allow), nil, acpArgs("", ""),
			"acp: give workdir, the folder of the repository. Allowed: " + allow},
		{"outside", acpConfig(allow), nil, acpArgs(outside, ""),
			"acp: workdir " + outside + " is outside acp.workdir_allowlist."},
		{"a file", acpConfig(allow), nil, acpArgs(file, ""), "acp: workdir " + file + " is not a folder."},
		{"missing", acpConfig(allow), nil, acpArgs(missing, ""), "acp: workdir " + missing + " does not exist."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fd := &fakeDelegator{}
			env := acpEnv(t)
			if c.env != nil {
				c.env(env)
			}
			got := runToolEnvErr(t, NewACPDelegateTool(c.cfg, fd, time.Minute), c.args, env)
			if got != c.want {
				t.Fatalf("got  %q\nwant %q", got, c.want)
			}
			if len(fd.calls) != 0 {
				t.Fatal("a refused call must not reach Delegate")
			}
		})
	}
}

// A workdir symlink from the allowlist to a folder outside it is refused. An allowlist entry
// that is a symlink works.
func TestACPDelegateWorkdirSymlinks(t *testing.T) {
	base := t.TempDir()
	allow, outside := filepath.Join(base, "projects"), filepath.Join(base, "secret")
	repo := filepath.Join(allow, "repo")
	for _, d := range []string{repo, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	escape, alink := filepath.Join(allow, "escape"), filepath.Join(base, "alink")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(allow, alink); err != nil {
		t.Fatal(err)
	}
	fd := &fakeDelegator{}
	got := runToolEnvErr(t, NewACPDelegateTool(acpConfig(allow), fd, time.Minute), acpArgs(escape, ""), acpEnv(t))
	if want := "acp: workdir " + escape + " is outside acp.workdir_allowlist."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	tool := NewACPDelegateTool(acpConfig(alink), fd, time.Minute)
	for _, wd := range []string{repo, filepath.Join(alink, "repo")} {
		runToolEnv(t, tool, acpArgs(wd, ""), acpEnv(t))
		if cwd := fd.last(t).CWD; cwd != realDir(t, repo) {
			t.Fatalf("workdir %s: CWD = %q, want %q", wd, cwd, realDir(t, repo))
		}
	}

	// tether chat uses its own folder when workdir is missing, and a relative workdir is
	// inside that folder.
	env := acpEnv(t)
	env.Channel, env.WorkDir = "cli", repo
	runToolEnv(t, tool, acpArgs("", ""), env)
	if cwd := fd.last(t).CWD; cwd != realDir(t, repo) {
		t.Fatalf("CLI default CWD = %q", cwd)
	}
	env.WorkDir = allow
	runToolEnv(t, tool, acpArgs("repo", ""), env)
	if cwd := fd.last(t).CWD; cwd != realDir(t, repo) {
		t.Fatalf("relative workdir CWD = %q", cwd)
	}
}

func TestACPDelegateCallAndResult(t *testing.T) {
	allow := t.TempDir()
	var asked []Question
	var gotReq security.ApprovalRequest
	var progress []string
	var hookErr error
	fd := &fakeDelegator{run: func(ctx context.Context, d acp.Delegation) (acp.Outcome, error) {
		d.Hooks.Progress("edit", "edit a.go")
		if scope, err := d.Hooks.Approve(ctx, security.ApprovalRequest{PatternID: "acp:claude-code:x:edit", NoAlways: true}); scope != security.ScopeSession || err != nil {
			hookErr = fmt.Errorf("approve = %v, %v", scope, err)
		}
		if choice, err := d.Hooks.Ask(ctx, "Allow?", []string{"Allow", "Reject"}, d.Hooks.ApprovalTimeout); choice != "Reject" || err != nil {
			hookErr = fmt.Errorf("ask = %q, %v", choice, err)
		}
		return acp.Outcome{Session: "resumed", StopReason: "end_turn", Text: strings.Repeat("a", 16010) + "END", ToolCalls: 18,
			Notes: []string{"mode acceptEdits, not auto: the model does not support auto"}}, nil
	}}
	env := acpEnv(t)
	env.Progress = func(batch uint64, child, tool, preview string) {
		progress = append(progress, fmt.Sprintf("%d %s %s %s", batch, child, tool, preview))
	}
	env.Gate = askGate{scope: security.ScopeSession, got: &gotReq}
	env.Ask = fixedAsker{ans: Answer{Choices: []string{"Reject"}}, seen: &asked}
	tool := NewACPDelegateTool(acpConfig(allow), fd, 10*time.Minute)
	out := runToolEnv(t, tool, acpArgs(allow, `,"model":" opus ","permission_preset":"risky","timeout_s":5000`), env)
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	d := fd.last(t)
	if d.SessionID != "s1" || d.Agent != "claude-code" || d.CWD != realDir(t, allow) || d.Model != "opus" ||
		d.Preset != "risky" || d.Task != "fix the bug" || d.Hooks.ApprovalTimeout != 10*time.Minute {
		t.Fatalf("delegation = %+v", d)
	}
	if left := time.Until(fd.deadline); left > 1800*time.Second || left < 1790*time.Second {
		t.Fatalf("timeout_s 5000 must use acp.turn_timeout_s 1800; time left %v", left)
	}
	if !reflect.DeepEqual(progress, []string{"0 claude-code edit edit a.go"}) {
		t.Fatalf("progress = %q", progress)
	}
	if gotReq.PatternID != "acp:claude-code:x:edit" {
		t.Fatalf("approval request = %+v", gotReq)
	}
	if len(asked) != 1 || asked[0].Text != "Allow?" || asked[0].Timeout != 10*time.Minute ||
		!reflect.DeepEqual(asked[0].Choices, []string{"Allow", "Reject"}) {
		t.Fatalf("questions = %+v", asked)
	}
	var res struct {
		Agent      string `json:"agent"`
		Session    string `json:"session"`
		StopReason string `json:"stop_reason"`
		DurationS  *int   `json:"duration_s"`
		ToolCalls  int    `json:"tool_calls"`
		Text       string `json:"text"`
		Note       string `json:"note"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	wantText := "…(13 characters cut) " + strings.Repeat("a", 15997) + "END"
	if res.Agent != "claude-code" || res.Session != "resumed" || res.StopReason != "end_turn" || res.DurationS == nil ||
		res.ToolCalls != 18 || res.Text != wantText || res.Note != "mode acceptEdits, not auto: the model does not support auto" {
		t.Fatalf("result = %+v", res)
	}
	if strings.Contains(runToolEnv(t, NewACPDelegateTool(acpConfig(allow), &fakeDelegator{}, time.Minute), acpArgs(allow, ""), acpEnv(t)), `"note"`) {
		t.Fatal("a result with no notes must have no note key")
	}
}

func TestACPDelegateHookFallbacks(t *testing.T) {
	allow := t.TempDir()
	for _, gate := range []Approver{nil, plainGate{}} {
		var scope security.Scope
		var err error
		var d acp.Delegation
		fd := &fakeDelegator{run: func(ctx context.Context, got acp.Delegation) (acp.Outcome, error) {
			d = got
			scope, err = got.Hooks.Approve(ctx, security.ApprovalRequest{})
			return acp.Outcome{Session: "new", StopReason: "end_turn"}, nil
		}}
		env := acpEnv(t)
		env.Gate = gate
		runToolEnv(t, NewACPDelegateTool(acpConfig(allow), fd, time.Minute), acpArgs(allow, ""), env)
		if scope != security.ScopeDeny || err != nil {
			t.Fatalf("gate %T: Approve = %v, %v; want ScopeDeny, nil", gate, scope, err)
		}
		if d.Hooks.Ask != nil || d.Hooks.Progress != nil {
			t.Fatal("no env.Ask and no env.Progress must give nil hooks")
		}
	}
	// A free-text answer is no choice.
	var choice string
	fd := &fakeDelegator{run: func(ctx context.Context, d acp.Delegation) (acp.Outcome, error) {
		choice, _ = d.Hooks.Ask(ctx, "?", []string{"Allow", "Reject"}, time.Minute)
		return acp.Outcome{Session: "new", StopReason: "end_turn"}, nil
	}}
	env := acpEnv(t)
	env.Ask = fixedAsker{ans: Answer{Text: "maybe"}}
	runToolEnv(t, NewACPDelegateTool(acpConfig(allow), fd, time.Minute), acpArgs(allow, ""), env)
	if choice != "" {
		t.Fatalf("free text gave choice %q", choice)
	}
}

func TestACPDelegateTimeoutNote(t *testing.T) {
	allow := t.TempDir()
	fd := &fakeDelegator{run: func(ctx context.Context, d acp.Delegation) (acp.Outcome, error) {
		<-ctx.Done()
		return acp.Outcome{Session: "reused", StopReason: "cancelled",
			Notes: []string{"the earlier context of the coding agent is lost"}}, nil
	}}
	out := runToolEnv(t, NewACPDelegateTool(acpConfig(allow), fd, time.Minute), acpArgs(allow, `,"timeout_s":1`), acpEnv(t))
	var res struct {
		StopReason string `json:"stop_reason"`
		Note       string `json:"note"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.StopReason != "cancelled" || res.Note != "the earlier context of the coding agent is lost; the turn timed out after 1 s" {
		t.Fatalf("result = %+v", res)
	}
}

func TestACPDelegateErrorPassThrough(t *testing.T) {
	allow := t.TempDir()
	fd := &fakeDelegator{run: func(context.Context, acp.Delegation) (acp.Outcome, error) {
		return acp.Outcome{}, acp.ErrSessionLocked
	}}
	got := runToolEnvErr(t, NewACPDelegateTool(acpConfig(allow), fd, time.Minute), acpArgs(allow, ""), acpEnv(t))
	if got != acp.ErrSessionLocked.Error() {
		t.Fatalf("got %q", got)
	}
}

func TestACPDelegatePreviewAndNeeds(t *testing.T) {
	if got := ArgPreview("acp_delegate", `{"agent":"claude-code","workdir":"/w","task":"Fix the login bug"}`); got != "Fix the login bug" {
		t.Fatalf("preview = %q", got)
	}
	if got := toolNeeds("acp_delegate"); got != "acp.enabled, an agent, and acp.workdir_allowlist." {
		t.Fatalf("needs = %q", got)
	}
}
