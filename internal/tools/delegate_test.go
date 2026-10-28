package tools

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// slowRunner is a ChildRunner that sleeps for d in each child. It records the specs, the
// highest number of children that ran at the same time, and the tool starts in script.
type slowRunner struct {
	d time.Duration
	// script holds the tool starts that each child reports, by child name.
	script map[string][][2]string

	mu        sync.Mutex
	active    int
	maxActive int
	specs     []ChildSpecLite
}

func (r *slowRunner) RunChild(ctx context.Context, spec ChildSpecLite) ChildResultLite {
	r.mu.Lock()
	r.active++
	r.maxActive = max(r.maxActive, r.active)
	r.specs = append(r.specs, spec)
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.active--
		r.mu.Unlock()
	}()
	for _, ts := range r.script[spec.Name] {
		if spec.OnToolStart != nil {
			spec.OnToolStart(ts[0], ts[1])
		}
	}
	select {
	case <-time.After(r.d):
		return ChildResultLite{Status: "ok", Summary: "did " + spec.Goal, ToolCalls: 2, DurationS: r.d.Seconds()}
	case <-ctx.Done():
		return ChildResultLite{Status: "cancelled", Error: "the child was cancelled"}
	}
}

// specByGoal returns the recorded spec with the goal.
func (r *slowRunner) specByGoal(goal string) ChildSpecLite {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.specs {
		if s.Goal == goal {
			return s
		}
	}
	return ChildSpecLite{}
}

// envDepth returns an Env of a run at depth d.
func envDepth(d int) *Env {
	e := &Env{SessionID: "s1", SessionKey: "cli:test", Values: map[string]any{}}
	if d > 0 {
		e.Values[DelegateDepthKey] = d
	}
	return e
}

func delegateTool(r ChildRunner) DelegateTool {
	return DelegateTool{Runner: r, MaxConcurrent: 5, MaxDepth: 1, MaxIterations: 50, TimeoutS: 600}
}

// delegateOut is the result JSON of a foreground call.
type delegateOut struct {
	Results []struct {
		Task      int     `json:"task"`
		Status    string  `json:"status"`
		Summary   string  `json:"summary"`
		ToolCalls int     `json:"tool_calls"`
		DurationS float64 `json:"duration_s"`
		Error     string  `json:"error"`
	} `json:"results"`
}

func parseDelegate(t *testing.T, out string) delegateOut {
	t.Helper()
	var d delegateOut
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("%q: %v", out, err)
	}
	return d
}

func TestBatchOf5RunsInParallel(t *testing.T) {
	r := &slowRunner{d: 200 * time.Millisecond}
	start := time.Now()
	out := runToolEnv(t, delegateTool(r),
		`{"tasks":[{"goal":"a"},{"goal":"b"},{"goal":"c"},{"goal":"d"},{"goal":"e"}]}`, envDepth(0))
	if time.Since(start) > 400*time.Millisecond || strings.Count(out, `"status":"ok"`) != 5 || r.maxActive != 5 {
		t.Fatal(time.Since(start), r.maxActive, out)
	}
	d := parseDelegate(t, out)
	for i, res := range d.Results {
		if res.Task != i+1 || res.Summary != "did "+string(rune('a'+i)) || res.ToolCalls != 2 {
			t.Fatalf("the results keep the task order: %+v", d.Results)
		}
	}
	if s := r.specByGoal("c"); s.Name != "3/5" || s.Depth != 0 {
		t.Fatalf("%+v", s)
	}
}

func TestBatchConcurrencyLimit(t *testing.T) {
	r := &slowRunner{d: 50 * time.Millisecond}
	tool := delegateTool(r)
	tool.MaxConcurrent = 2
	out := runToolEnv(t, tool, `{"tasks":[{"goal":"a"},{"goal":"b"}]}`, envDepth(0))
	if strings.Count(out, `"status":"ok"`) != 2 || r.maxActive > 2 {
		t.Fatal(out)
	}
}

func TestBatchOf6IsErrorStatingLimit(t *testing.T) {
	out := runToolEnvErr(t, DelegateTool{Runner: &slowRunner{}, MaxConcurrent: 5},
		`{"tasks":[{"goal":"1"},{"goal":"2"},{"goal":"3"},{"goal":"4"},{"goal":"5"},{"goal":"6"}]}`, envDepth(0))
	if !strings.Contains(out, "max 5 tasks per call (delegation.max_concurrent_children)") {
		t.Fatal(out)
	}
}

func TestDepthLimit(t *testing.T) {
	r := &slowRunner{}
	out := runToolEnvErr(t, delegateTool(r), `{"goal":"x"}`, envDepth(1))
	if !strings.Contains(out, "Delegation depth limit reached (depth=1, max_spawn_depth=1)") || len(r.specs) != 0 {
		t.Fatal(out)
	}
	tool := delegateTool(r)
	tool.MaxDepth = 2
	runToolEnv(t, tool, `{"goal":"x"}`, envDepth(1))
	if s := r.specByGoal("x"); s.Depth != 1 {
		t.Fatalf("the child gets the depth of the parent run: %+v", s)
	}
}

func TestSingleGoalShape(t *testing.T) {
	r := &slowRunner{}
	env := envDepth(0)
	out := runToolEnv(t, delegateTool(r), `{"goal":"x","context":"ctx","toolsets":["web"],"role":"orchestrator"}`, env)
	d := parseDelegate(t, out)
	if len(d.Results) != 1 || d.Results[0].Task != 1 || d.Results[0].Status != "ok" || d.Results[0].Summary != "did x" {
		t.Fatal(out)
	}
	s := r.specByGoal("x")
	if s.Name != "1/1" || s.Context != "ctx" || !slices.Equal(s.Toolsets, []string{"web"}) || s.Role != "orchestrator" || s.ParentEnv != env {
		t.Fatalf("%+v", s)
	}
}

func TestTaskRoleOverridesTopRole(t *testing.T) {
	r := &slowRunner{}
	runToolEnv(t, delegateTool(r), `{"role":"orchestrator","tasks":[{"goal":"a"},{"goal":"b","role":"leaf"},{"goal":"c","role":"boss"}]}`, envDepth(0))
	if r.specByGoal("a").Role != "orchestrator" || r.specByGoal("b").Role != "leaf" || r.specByGoal("c").Role != "leaf" {
		t.Fatal(r.specs)
	}
}

func TestDelegateBadArguments(t *testing.T) {
	r := &slowRunner{}
	cases := map[string]string{
		`{}`:                                   "Provide either 'goal' (single task) or 'tasks' (batch)",
		`{"goal":"  "}`:                        "Provide either 'goal' (single task) or 'tasks' (batch)",
		`{"tasks":[{"goal":"a"},{"goal":""}]}`: "Task 2 is missing a 'goal'",
	}
	for args, want := range cases {
		if out := runToolEnvErr(t, delegateTool(r), args, envDepth(0)); !strings.Contains(out, want) {
			t.Errorf("%s: %s", args, out)
		}
	}
	if out := runToolEnvErr(t, DelegateTool{}, `{"goal":"x"}`, envDepth(0)); !strings.Contains(out, "not configured") {
		t.Fatal(out)
	}
}

func TestProgressLinesPrefixed(t *testing.T) {
	r := &slowRunner{script: map[string][][2]string{
		"2/3": {{"browser_navigate", "https://example.com"}},
		"3/3": {{"terminal", ""}},
	}}
	env := envDepth(0)
	var mu sync.Mutex
	var lines []string
	env.Progress = func(line string) { mu.Lock(); lines = append(lines, line); mu.Unlock() }
	runToolEnv(t, delegateTool(r), `{"tasks":[{"goal":"a"},{"goal":"b"},{"goal":"c"}]}`, env)
	slices.Sort(lines)
	want := []string{`🔀 [2/3] browser_navigate "https://example.com"`, `🔀 [3/3] terminal`}
	if !slices.Equal(lines, want) {
		t.Fatalf("%q", lines)
	}
}

func TestStopCancelsEveryChild(t *testing.T) {
	r := &slowRunner{d: time.Minute}
	cctx, cancel := context.WithCancel(ctx)
	time.AfterFunc(50*time.Millisecond, cancel)
	reg := NewRegistry()
	reg.Register(delegateTool(r))
	res := reg.Dispatch(cctx, "delegate_task", json.RawMessage(`{"tasks":[{"goal":"a"},{"goal":"b"}]}`), envDepth(0), vis("delegate_task"))
	if strings.Count(res.Text, `"status":"cancelled"`) != 2 {
		t.Fatal(res.Text)
	}
}

func TestDescriptionStatesLimits(t *testing.T) {
	s := DelegateTool{MaxConcurrent: 5, MaxDepth: 1, MaxIterations: 50, TimeoutS: 600}.Schema()
	for _, want := range []string{"up to 5", "max_spawn_depth=1", "50 tool rounds", "600 s"} {
		if !strings.Contains(s.Description, want) {
			t.Errorf("the description lacks %q: %s", want, s.Description)
		}
	}
	if !json.Valid(s.Parameters) || !strings.Contains(string(s.Parameters), "up to 5") {
		t.Fatal(string(s.Parameters))
	}
	tool := DelegateTool{}
	if tool.Name() != "delegate_task" || tool.Toolset() != SetDelegation || tool.ParallelSafe() {
		t.Fatal("name, toolset, or parallel flag")
	}
}

func TestTurnToolsRoundTrip(t *testing.T) {
	env := &Env{}
	if sets, names := TurnTools(env); sets != nil || names != nil {
		t.Fatal("no turn tools yet")
	}
	SetTurnTools(env, map[string]bool{"web": true}, map[string]bool{"web_fetch": true})
	sets, names := TurnTools(env)
	if !sets["web"] || !names["web_fetch"] {
		t.Fatal(sets, names)
	}
}

// fakeStarter is a BackgroundStarter that records the specs.
type fakeStarter struct {
	env   *Env
	specs []ChildSpecLite
	err   error
}

func (f *fakeStarter) StartBackground(_ context.Context, env *Env, specs []ChildSpecLite) (string, error) {
	f.env, f.specs = env, specs
	return "bg_1", f.err
}

func TestDelegateBackgroundFlagReturnsBatchID(t *testing.T) {
	r, bg := &slowRunner{}, &fakeStarter{}
	tool := delegateTool(r)
	tool.Background = bg
	env := envDepth(0)
	out := runToolEnv(t, tool, `{"tasks":[{"goal":"a"},{"goal":"b"},{"goal":"c"}],"background":true}`, env)
	if out != `{"batch_id":"bg_1","children":["1/3","2/3","3/3"]}` {
		t.Fatal(out)
	}
	if len(r.specs) != 0 || bg.env != env || len(bg.specs) != 3 || bg.specs[1].Goal != "b" {
		t.Fatal("the starter runs the children, not the foreground runner")
	}
	out = runToolEnv(t, tool, `{"goal":"x","background":true}`, env)
	if out != `{"batch_id":"bg_1","children":["1/1"]}` {
		t.Fatal(out)
	}
}

func TestDelegateBackgroundErrors(t *testing.T) {
	tool := delegateTool(&slowRunner{})
	if out := runToolEnvErr(t, tool, `{"goal":"x","background":true}`, envDepth(0)); !strings.Contains(out, "not available") {
		t.Fatal(out)
	}
	tool.Background = &fakeStarter{}
	if out := runToolEnvErr(t, tool, `{"goal":"x","background":true}`, &Env{}); !strings.Contains(out, "needs a session key") {
		t.Fatal(out)
	}
	cronEnv := envDepth(0)
	cronEnv.Kind = "cron"
	if out := runToolEnvErr(t, tool, `{"goal":"x","background":true}`, cronEnv); !strings.Contains(out, "not available in a cron job") {
		t.Fatal(out)
	}
	// An orchestrator child of a cron run also ends before a background batch.
	cronChild := envDepth(0)
	cronChild.Kind, cronChild.RootKind = "subagent", "cron"
	if out := runToolEnvErr(t, tool, `{"goal":"x","background":true}`, cronChild); !strings.Contains(out, "not available in a cron job") {
		t.Fatal(out)
	}
	tool.Background = &fakeStarter{err: errors.New("db closed")}
	if out := runToolEnvErr(t, tool, `{"goal":"x","background":true}`, envDepth(0)); !strings.Contains(out, "db closed") {
		t.Fatal(out)
	}
	if out := runToolEnvErr(t, tool, `{"tasks":[{"goal":"1"},{"goal":"2"},{"goal":"3"},{"goal":"4"},{"goal":"5"},{"goal":"6"}],"background":true}`, envDepth(0)); !strings.Contains(out, "max 5 tasks") {
		t.Fatal("background batches have the same limit:", out)
	}
}
