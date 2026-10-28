package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// namedTool is a tool with a given name and toolset. It records the env values of its
// last call.
type namedTool struct {
	name, set string
	mu        *sync.Mutex
	seen      *tools.Env
}

func newNamed(name, set string) namedTool {
	return namedTool{name: name, set: set, mu: &sync.Mutex{}, seen: &tools.Env{}}
}

func (n namedTool) Name() string       { return n.name }
func (n namedTool) Toolset() string    { return n.set }
func (n namedTool) ParallelSafe() bool { return false }
func (n namedTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{Name: n.name, Description: n.name, Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (n namedTool) Run(_ context.Context, _ json.RawMessage, env *tools.Env) (string, error) {
	n.mu.Lock()
	*n.seen = *env
	n.mu.Unlock()
	return n.name + " ran", nil
}

// newAgentWithHistory returns an Agent and a parent session with n messages.
func newAgentWithHistory(t *testing.T, fp *fakeProv, n int) (*Agent, *sessions.Session) {
	t.Helper()
	a, parent := newAgent(t, fp, echoTool{})
	seedHistory(t, a, parent.ID, n)
	return a, parent
}

// newAgentAllTools returns an Agent with echo and every tool that a child must not see.
func newAgentAllTools(t *testing.T, fp *fakeProv) (*Agent, *sessions.Session) {
	t.Helper()
	sets := map[string]string{"delegate_task": tools.SetDelegation, "clarify": tools.SetAsk, "memory": tools.SetMemory,
		"execute_code": tools.SetCodeExecution, "cronjob": tools.SetCron}
	var ts []tools.Tool
	for name, set := range sets {
		ts = append(ts, newNamed(name, set))
	}
	return newAgent(t, fp, append(ts, echoTool{})...)
}

// schemaNames returns the tool names of req.
func schemaNames(req provider.Request) map[string]bool {
	out := map[string]bool{}
	for _, s := range req.Tools {
		out[s.Name] = true
	}
	return out
}

// childSessions returns the sessions whose parent is parentID.
func childSessions(t *testing.T, a *Agent, parentID string) []sessions.Session {
	t.Helper()
	rows, err := a.DB.QueryContext(ctx, `SELECT id FROM sessions WHERE parent_id = ? ORDER BY id`, parentID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	var out []sessions.Session
	for _, id := range ids {
		s, err := a.Store.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, *s)
	}
	return out
}

func TestChildFreshConversationAndOwnSession(t *testing.T) {
	fp := scripted(text("child done"))
	a, parent := newAgentWithHistory(t, fp, 10)
	r := a.RunChild(ctx, ChildSpec{Goal: "scrape mens", Context: "site: example.com", ParentSession: parent, ParentToolsets: all(), Name: "1/1"})
	if r.Status != "ok" || r.Summary != "child done" || r.Error != "" || r.DurationS <= 0 {
		t.Fatalf("%+v", r)
	}
	req := fp.requests[0]
	if len(req.Messages) != 1 || req.Messages[0].Content != "scrape mens" {
		t.Fatal("no parent history")
	}
	if !strings.Contains(req.System, "YOUR TASK:\nscrape mens") || !strings.Contains(req.System, "CONTEXT:\nsite: example.com") {
		t.Fatal(req.System)
	}
	rows := childSessions(t, a, parent.ID)
	if len(rows) != 1 || rows[0].Kind != "subagent" || rows[0].Source != parent.Source {
		t.Fatal(rows)
	}
	if n := len(messages(t, a, parent.ID)); n != 10 {
		t.Fatal("the parent session does not change", n)
	}
}

func TestBlockedToolsHidden(t *testing.T) {
	fp := scripted(text("ok"))
	a, parent := newAgentAllTools(t, fp)
	a.RunChild(ctx, ChildSpec{Goal: "x", ParentSession: parent, ParentToolsets: all()})
	names := schemaNames(fp.requests[0])
	for _, n := range ChildBlockedTools {
		if names[n] {
			t.Fatal(n)
		}
	}
	if !names["echo"] {
		t.Fatal("the other tools stay visible")
	}
}

func TestOrchestratorKeepsDelegateOnlyBelowDepth(t *testing.T) {
	for _, tc := range []struct {
		max, depth int
		role       string
		want       bool
	}{{1, 0, "orchestrator", false}, {2, 0, "orchestrator", true}, {2, 1, "orchestrator", false}, {2, 0, "leaf", false}} {
		fp := scripted(text("ok"))
		a, parent := newAgentAllTools(t, fp)
		a.Cfg.Delegation.MaxSpawnDepth = tc.max
		a.RunChild(ctx, ChildSpec{Goal: "x", Role: tc.role, Depth: tc.depth, ParentSession: parent, ParentToolsets: all()})
		req := fp.requests[0]
		if got := schemaNames(req)["delegate_task"]; got != tc.want {
			t.Fatalf("%+v: delegate_task visible = %v", tc, got)
		}
		if got := strings.Contains(req.System, "Orchestrator Role"); got != tc.want {
			t.Fatalf("%+v: orchestrator note = %v", tc, got)
		}
		if schemaNames(req)["memory"] || schemaNames(req)["clarify"] {
			t.Fatal("an orchestrator keeps the other blocks")
		}
	}
}

func TestChildToolsetsOnlyNarrow(t *testing.T) {
	fp := scripted(text("ok"), text("ok"))
	a, parent := newAgent(t, fp, echoTool{}, newNamed("web_x", tools.SetWeb))
	parentSets := tools.Resolve([]string{tools.SetWeb}, nil, nil)
	a.RunChild(ctx, ChildSpec{Goal: "x", Toolsets: []string{tools.SetTerminal}, ParentSession: parent, ParentToolsets: parentSets})
	if names := schemaNames(fp.requests[0]); names["echo"] || names["web_x"] {
		t.Fatal("terminal is not in the parent sets", names)
	}
	a.RunChild(ctx, ChildSpec{Goal: "x", Toolsets: []string{tools.SetWeb, tools.SetTerminal}, ParentSession: parent, ParentToolsets: parentSets})
	if names := schemaNames(fp.requests[1]); names["echo"] || !names["web_x"] {
		t.Fatal(names)
	}
}

func TestChildTimeoutReturnsPartial(t *testing.T) {
	fp := scripted(step{text: "partial work", calls: []provider.ToolCall{tc("c1", "slow", `{}`)}})
	a, parent := newAgent(t, fp, slowTool{name: "slow", d: 30 * time.Second})
	a.Cfg.Delegation.ChildTimeoutS = 1
	start := time.Now()
	r := a.RunChild(ctx, ChildSpec{Goal: "x", ParentSession: parent, ParentToolsets: all()})
	if time.Since(start) > 5*time.Second {
		t.Fatal("the timeout must end the child")
	}
	if r.Status != "timeout" || !strings.Contains(r.Summary, "partial work") || !strings.Contains(r.Summary, "[activity:") || r.ToolCalls != 1 {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(r.Error, "timed out after 1s") {
		t.Fatal(r.Error)
	}
}

func TestChildCancelled(t *testing.T) {
	fp := scripted(step{text: "partial", calls: []provider.ToolCall{tc("c1", "slow", `{}`)}})
	a, parent := newAgent(t, fp, slowTool{name: "slow", d: 30 * time.Second})
	cctx, cancel := context.WithCancel(ctx)
	time.AfterFunc(100*time.Millisecond, cancel)
	if r := a.RunChild(cctx, ChildSpec{Goal: "x", ParentSession: parent, ParentToolsets: all()}); r.Status != "cancelled" {
		t.Fatalf("%+v", r)
	}
}

func TestChildMaxIterations50(t *testing.T) {
	fp := scriptedForever(toolCalls(tc("c", "echo", `{"s":"x"}`)))
	a, parent := newAgent(t, fp, echoTool{})
	a.Cfg.Agent.MaxTurns = 3
	r := a.RunChild(ctx, ChildSpec{Goal: "x", ParentSession: parent, ParentToolsets: all()})
	if len(fp.requests) != a.Cfg.Delegation.MaxIterations || a.Cfg.Delegation.MaxIterations != 50 {
		t.Fatal(len(fp.requests))
	}
	if r.ToolCalls != 50 || !strings.Contains(r.Summary, NoteStopped) {
		t.Fatalf("%+v", r)
	}
}

func TestChildEnvAndProvider(t *testing.T) {
	prim := scripted()
	cheap := &fakeProv{name: "cheap", model: "cheap-m", script: []step{toolCalls(tc("c1", "rec", `{}`)), text("ok")}}
	rec := newNamed("rec", tools.SetFile)
	a, parent := newAgent(t, prim, rec)
	a.Providers["cheap"] = cheap
	a.Cfg.Agent.DelegationProvider = "cheap"
	parentEnv := &tools.Env{DataDir: "/data", WorkDir: "/work", Values: map[string]any{"x": 1}}
	r := a.RunChild(ctx, ChildSpec{Goal: "x", ParentSession: parent, ParentToolsets: all(), Name: "2/3", Depth: 0, ParentEnv: parentEnv})
	if r.Status != "ok" || len(prim.requests) != 0 || len(cheap.requests) != 2 {
		t.Fatalf("the delegation provider runs the child: %+v", r)
	}
	seen := rec.seen
	if seen.Values[ChildNameKey] != "2/3" || seen.Values[ChildDepthKey] != 1 || seen.DataDir != "/data" || seen.WorkDir != "/work" {
		t.Fatalf("%+v", seen)
	}
	if seen.Kind != "subagent" || seen.SessionID == parent.ID || seen.Values["x"] != nil {
		t.Fatalf("the child has its own session and values: %+v", seen)
	}
}

func TestChildEnvKeepsRootKind(t *testing.T) {
	cronEnv := &tools.Env{Kind: "cron"}
	child := childEnv(cronEnv, &sessions.Session{ID: "c1", Kind: KindSubagent})
	if child.Kind != KindSubagent || child.RootKind != "cron" {
		t.Fatalf("%+v", child)
	}
	grand := childEnv(child, &sessions.Session{ID: "c2", Kind: KindSubagent})
	if grand.RootKind != "cron" {
		t.Fatalf("a grandchild keeps the root kind: %+v", grand)
	}
	if top := childEnv(&tools.Env{Kind: "chat"}, &sessions.Session{ID: "c3", Kind: KindSubagent}); top.RootKind != "chat" {
		t.Fatalf("%+v", top)
	}
	if none := childEnv(nil, &sessions.Session{ID: "c4", Kind: KindSubagent}); none.RootKind != "" {
		t.Fatalf("%+v", none)
	}
}

// resetGate is a tools.Approver that records the ResetSession calls.
type resetGate struct {
	mu    sync.Mutex
	reset []string
}

func (g *resetGate) CheckCommand(context.Context, *tools.Env, string) error { return nil }

func (g *resetGate) ResetSession(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reset = append(g.reset, id)
}

func TestChildReleasesApprovalSession(t *testing.T) {
	a, parent := newAgent(t, scripted(text("done")), echoTool{})
	gate := &resetGate{}
	r := a.RunChild(ctx, ChildSpec{Goal: "x", ParentSession: parent, ParentToolsets: all(), ParentEnv: &tools.Env{Gate: gate}})
	kids := childSessions(t, a, parent.ID)
	if r.Status != ChildOK || len(kids) != 1 || len(gate.reset) != 1 || gate.reset[0] != kids[0].ID {
		t.Fatalf("%+v %v", r, gate.reset)
	}
}

func TestChildSystemPromptPort(t *testing.T) {
	p := ChildSystemPrompt("fix the build", "")
	if !strings.HasPrefix(p, "You are a focused subagent working on a specific delegated task.") ||
		strings.Contains(p, "CONTEXT:") || !strings.Contains(p, "- Any files you created or modified") {
		t.Fatal(p)
	}
}
