package cron_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/skills"
	"github.com/dnwandana/tether-agent/internal/tools"
)

var bg = context.Background()

// reply is one scripted model response: text, or tool calls.
type reply struct {
	text  string
	calls []provider.ToolCall
}

func say(s string) reply { return reply{text: s} }

func call(name, args string) reply {
	return reply{calls: []provider.ToolCall{{ID: "c1", Name: name, Args: args}}}
}

// scriptedProv is a fake provider that records each request.
type scriptedProv struct {
	name     string
	mu       sync.Mutex
	script   []reply
	requests []provider.Request
}

func (p *scriptedProv) Name() string  { return p.name }
func (p *scriptedProv) Model() string { return "test-model" }

func (p *scriptedProv) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	p.mu.Lock()
	req.Messages = append([]provider.Message(nil), req.Messages...)
	p.requests = append(p.requests, req)
	r := say("(script ended)")
	if len(p.script) > 0 {
		r, p.script = p.script[0], p.script[1:]
	}
	p.mu.Unlock()
	ch := make(chan provider.Event, 4+len(r.calls))
	if r.text != "" {
		ch <- provider.Event{Type: provider.EventText, Delta: r.text}
	}
	finish := "stop"
	for _, c := range r.calls {
		ch <- provider.Event{Type: provider.EventToolCall, ToolCall: &c}
		finish = "tool_calls"
	}
	ch <- provider.Event{Type: provider.EventDone, FinishReason: finish, Usage: &provider.Usage{}}
	close(ch)
	return ch, nil
}

func (p *scriptedProv) reqs() []provider.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]provider.Request(nil), p.requests...)
}

// stubTool is a tool with a name and a toolset. A nil run returns "ok".
type stubTool struct {
	name, set string
	run       func(ctx context.Context, env *tools.Env, args json.RawMessage) string
}

func (s stubTool) Name() string       { return s.name }
func (s stubTool) Toolset() string    { return s.set }
func (s stubTool) ParallelSafe() bool { return false }
func (s stubTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{Name: s.name, Description: s.name, Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (s stubTool) Run(ctx context.Context, args json.RawMessage, env *tools.Env) (string, error) {
	if s.run == nil {
		return "ok", nil
	}
	return s.run(ctx, env, args), nil
}

// gateApprover adapts security.Gate to tools.Approver, the same as internal/app.
type gateApprover struct {
	g *security.Gate
	p security.Prompter
}

func (a gateApprover) CheckCommand(ctx context.Context, env *tools.Env, command string) error {
	return a.g.Check(ctx, security.ApprovalRequest{
		SessionID: env.SessionID, SessionKey: env.SessionKey, Kind: env.Kind, Command: command,
	}, a.p)
}

// failPrompter fails the test when the gate asks a user.
type failPrompter struct{ t *testing.T }

func (f failPrompter) PromptApproval(context.Context, security.ApprovalRequest) (security.Scope, error) {
	f.t.Error("a cron run must never prompt a user")
	return security.ScopeDeny, nil
}

// fixture holds a Runner with a real agent.Agent and a scripted provider.
type fixture struct {
	r     *cron.Runner
	fp    *scriptedProv
	ag    *agent.Agent
	store *sessions.Store
	dir   string
}

// runner returns a Runner whose Turn is the internal/app adapter: it resolves the cron
// toolsets of the config, sets Kind "cron" on the env, and calls agent.RunTurn.
func runner(t *testing.T, script ...reply) (*cron.Runner, *scriptedProv) {
	f := newFixture(t, script...)
	return f.r, f.fp
}

func newFixture(t *testing.T, script ...reply) *fixture {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{"primary": {Format: "openai", Model: "test-model"}}
	fp := &scriptedProv{name: "primary", script: script}
	reg := tools.NewRegistry()
	gate := gateApprover{g: security.NewGate(cfg.Approvals, d), p: failPrompter{t}}
	for _, tool := range []tools.Tool{
		stubTool{name: "cronjob", set: tools.SetCron},
		stubTool{name: "ask_user", set: tools.SetAsk},
		stubTool{name: "web_search", set: tools.SetWeb},
		stubTool{name: "run_command", set: tools.SetTerminal, run: func(ctx context.Context, env *tools.Env, args json.RawMessage) string {
			var a struct{ Command string }
			_ = json.Unmarshal(args, &a)
			if err := env.Gate.CheckCommand(ctx, env, a.Command); err != nil {
				return err.Error()
			}
			return "ran"
		}},
	} {
		reg.Register(tool)
	}
	store := sessions.New(d)
	ag := &agent.Agent{
		Prov: fp, Providers: map[string]provider.Provider{"primary": fp},
		Tools: reg, Store: store, DB: d, Cfg: &cfg, SpillDir: filepath.Join(dir, "spill"),
	}
	scripts := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(scripts, 0o700); err != nil {
		t.Fatal(err)
	}
	r := &cron.Runner{
		Store:      store,
		ScriptsDir: scripts,
		SpillDir:   filepath.Join(dir, "spill"),
		Turn: func(ctx context.Context, ct cron.CronTurn) (string, error) {
			env := &tools.Env{
				SessionID: ct.Session.ID, SessionKey: ct.Session.Key, Kind: "cron", Channel: "cron",
				WorkDir: ct.Workdir, DataDir: dir, Gate: gate,
			}
			res, err := ag.RunTurn(ctx, agent.Turn{
				Session:     ct.Session,
				UserText:    ct.Text,
				Toolsets:    tools.Resolve(cfg.Toolsets["cron"], ct.Toolsets, nil),
				RemoveTools: ct.RemoveTools,
				Env:         env,
			})
			return res.Final, err
		},
	}
	return &fixture{r: r, fp: fp, ag: ag, store: store, dir: dir}
}

// firstUser returns the first user message of req.
func firstUser(t *testing.T, req provider.Request) string {
	t.Helper()
	for _, m := range req.Messages {
		if m.Role == provider.RoleUser {
			return m.Content
		}
	}
	t.Fatal("the request has no user message")
	return ""
}

func schemaNames(req provider.Request) map[string]bool {
	out := map[string]bool{}
	for _, s := range req.Tools {
		out[s.Name] = true
	}
	return out
}

func writeScript(t *testing.T, r *cron.Runner, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.ScriptsDir, name), []byte(body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestAgentJobAlwaysDelivers(t *testing.T) {
	r, fp := runner(t, say("Nothing new today."))
	o := r.Run(bg, cron.Job{ID: "j1", Name: "digest", Prompt: "GitHub trending digest"})
	if !o.Send || o.Text != "Nothing new today." || o.Status != "ok" || o.SessionID == "" {
		t.Fatal(o)
	}
	reqs := fp.reqs()
	for _, m := range reqs[0].Messages {
		if strings.Contains(m.Content, "[SILENT]") {
			t.Fatal("no SILENT rule")
		}
	}
	if strings.Contains(reqs[0].System, "[SILENT]") {
		t.Fatal("no SILENT rule in the system prompt")
	}
	u := firstUser(t, reqs[0])
	if !strings.Contains(u, "GitHub trending digest") || !strings.Contains(u, cron.CronPreamble) {
		t.Fatalf("%q", u)
	}
}

func TestCronHidesCronjobAndAskUser(t *testing.T) {
	r, fp := runner(t, say("done"))
	r.Run(bg, cron.Job{ID: "j1", Name: "n", Prompt: "p"})
	names := schemaNames(fp.reqs()[0])
	if names["cronjob"] || names["ask_user"] || !names["web_search"] || !names["run_command"] {
		t.Fatal(names)
	}
}

func TestJobToolsetsNarrow(t *testing.T) {
	r, fp := runner(t, say("done"))
	r.Run(bg, cron.Job{ID: "j1", Name: "n", Prompt: "p", Toolsets: []string{"web"}})
	names := schemaNames(fp.reqs()[0])
	if len(names) != 1 || !names["web_search"] {
		t.Fatal(names)
	}
}

func TestPreScriptOutputInPrompt(t *testing.T) {
	r, fp := runner(t, say("done"))
	writeScript(t, r, "count.sh", "echo count=5")
	o := r.Run(bg, cron.Job{ID: "j1", Name: "n", Prompt: "Report the count", Script: "count.sh"})
	u := firstUser(t, fp.reqs()[0])
	if !o.Send || !strings.HasPrefix(u, "Script output:\ncount=5\n\nReport the count") {
		t.Fatalf("%q", u)
	}
}

func TestPreScriptFailureInPrompt(t *testing.T) {
	r, fp := runner(t, say("the script failed"))
	writeScript(t, r, "bad.sh", "echo broken >&2; exit 3")
	o := r.Run(bg, cron.Job{ID: "j1", Name: "n", Prompt: "p", Script: "bad.sh"})
	u := firstUser(t, fp.reqs()[0])
	if !o.Send || !strings.HasPrefix(u, "Script error:") || !strings.Contains(u, "exit 3") || !strings.Contains(u, "broken") {
		t.Fatalf("%q", u)
	}
}

func TestSkillsLoadedInOrder(t *testing.T) {
	r, fp := runner(t, say("done"))
	r.Skills = func() ([]skills.Skill, []skills.Warning, error) {
		return []skills.Skill{{Name: "a", Body: "Body of a"}, {Name: "b", Body: "Body of b"}}, nil, nil
	}
	r.Run(bg, cron.Job{ID: "j1", Name: "n", Prompt: "the prompt", Skills: []string{"b", "a"}})
	u := firstUser(t, fp.reqs()[0])
	ib, ia, ip := strings.Index(u, "[Skill: b]\nBody of b\n"), strings.Index(u, "[Skill: a]\nBody of a\n"), strings.Index(u, "the prompt")
	if ib != 0 || ia <= ib || ip <= ia {
		t.Fatalf("%q", u)
	}
}

func TestMissingSkillNotice(t *testing.T) {
	r, fp := runner(t, say("done"))
	r.Skills = func() ([]skills.Skill, []skills.Warning, error) {
		return []skills.Skill{{Name: "a", Body: "Body of a"}}, nil, nil
	}
	r.Run(bg, cron.Job{ID: "j1", Name: "n", Prompt: "p", Skills: []string{"a", "gone"}})
	u := firstUser(t, fp.reqs()[0])
	if !strings.Contains(u, "could not be found and were skipped: gone") || !strings.Contains(u, "[Skill: a]") {
		t.Fatalf("%q", u)
	}
}

func TestFreshSessionPerRun(t *testing.T) {
	f := newFixture(t, say("first answer"), say("second answer"))
	j := cron.Job{ID: "j1", Name: "digest", Prompt: "p"}
	o1, o2 := f.r.Run(bg, j), f.r.Run(bg, j)
	if o1.SessionID == "" || o1.SessionID == o2.SessionID {
		t.Fatal(o1, o2)
	}
	for _, m := range f.fp.reqs()[1].Messages {
		if strings.Contains(m.Content, "first answer") {
			t.Fatal("the second run sees the first run")
		}
	}
	list, err := f.store.List(bg, "cron:j1", 10)
	if err != nil || len(list) != 2 {
		t.Fatal(list, err)
	}
	for _, s := range list {
		if s.Kind != "cron" || s.Source != "cron" || s.Title != "digest" {
			t.Fatal(s.Session)
		}
	}
}

func TestModelPin(t *testing.T) {
	f := newFixture(t)
	cheap := &scriptedProv{name: "cheap", script: []reply{say("from cheap")}}
	f.ag.Providers["cheap"] = cheap
	o := f.r.Run(bg, cron.Job{ID: "j1", Name: "n", Prompt: "p", Model: &cron.ModelPin{Provider: "cheap", Model: "small"}})
	if o.Text != "from cheap" || len(f.fp.reqs()) != 0 || len(cheap.reqs()) != 1 || cheap.reqs()[0].Model != "small" {
		t.Fatal(o, len(f.fp.reqs()), cheap.reqs())
	}
}

func TestScriptJobNoModelCall(t *testing.T) {
	f := newFixture(t)
	writeScript(t, f.r, "rl.sh", "echo 'allsaints: 4120/9000'")
	o := f.r.Run(bg, cron.Job{ID: "j1", Name: "rl", NoAgent: true, Script: "rl.sh"})
	if !o.Send || o.Text != "allsaints: 4120/9000\n" || o.Status != "ok" || o.SessionID != "" || len(f.fp.reqs()) != 0 {
		t.Fatal(o)
	}
	if list, _ := f.store.List(bg, "cron:j1", 10); len(list) != 0 {
		t.Fatal("a script job creates no session", list)
	}
}

func TestScriptJobFailureStatus(t *testing.T) {
	r, _ := runner(t)
	writeScript(t, r, "bad.sh", "exit 2")
	o := r.Run(bg, cron.Job{ID: "j1", Name: "rl", NoAgent: true, Script: "bad.sh"})
	if !o.Send || o.Status != "error" || !strings.Contains(o.Text, "exit 2") {
		t.Fatal(o)
	}
}

func TestDangerousDeniedInCron(t *testing.T) {
	r, fp := runner(t, call("run_command", `{"command":"rm -rf /tmp/x"}`), say("could not delete"))
	o := r.Run(bg, cron.Job{ID: "j1", Name: "n", Prompt: "clean up"})
	reqs := fp.reqs()
	if len(reqs) != 2 || o.Text != "could not delete" {
		t.Fatal(o, len(reqs))
	}
	var tool string
	for _, m := range reqs[1].Messages {
		if m.Role == provider.RoleTool {
			tool = m.Content
		}
	}
	if !strings.Contains(tool, "BLOCKED") || !strings.Contains(tool, "cron jobs run without a user present") {
		t.Fatalf("%q", tool)
	}
}

func TestTurnErrorAlerts(t *testing.T) {
	r, _ := runner(t)
	r.Turn = func(context.Context, cron.CronTurn) (string, error) { return "", errors.New("provider down") }
	o := r.Run(bg, cron.Job{ID: "j1", Name: "digest", Prompt: "p"})
	if !o.Send || o.Status != "error" || !strings.HasPrefix(o.Text, "⚠ Cron job digest failed:") ||
		!strings.Contains(o.Text, "provider down") || o.SessionID == "" {
		t.Fatal(o)
	}
}
