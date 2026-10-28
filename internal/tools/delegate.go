package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// DelegateDepthKey is the Env.Values key of the depth of a run: 1 for a child of a
// top-level run. It is the same key as agent.ChildDepthKey. A missing value is depth 0.
const DelegateDepthKey = "delegate_depth"

// turnToolsKey is the Env.Values key of the tools of the current turn.
const turnToolsKey = "turn_tools"

// The defaults of the delegation limits, for a config value that is not positive.
const (
	defaultMaxChildren   = 5
	defaultMaxSpawnDepth = 1
	defaultMaxIterations = 50
	defaultChildTimeoutS = 600
)

// turnTools holds the toolsets and the visible tool names of one turn.
type turnTools struct {
	sets, names map[string]bool
}

// SetTurnTools records the allowed toolsets and the visible tool names of the current turn
// in env. The agent loop calls it at the start of each turn. delegate_task gives the
// toolsets to the children, and execute_code uses the names.
func SetTurnTools(env *Env, sets, names map[string]bool) {
	if env == nil {
		return
	}
	valuesMu.Lock()
	defer valuesMu.Unlock()
	if env.Values == nil {
		env.Values = map[string]any{}
	}
	env.Values[turnToolsKey] = turnTools{sets: sets, names: names}
}

// TurnTools returns the toolsets and the visible tool names that SetTurnTools recorded.
// Both are nil when no turn recorded them.
func TurnTools(env *Env) (sets, names map[string]bool) {
	if env == nil {
		return nil, nil
	}
	valuesMu.Lock()
	defer valuesMu.Unlock()
	tt, _ := env.Values[turnToolsKey].(turnTools)
	return tt.sets, tt.names
}

// delegateDepth returns the delegation depth of the run of env.
func delegateDepth(env *Env) int {
	if env == nil {
		return 0
	}
	valuesMu.Lock()
	defer valuesMu.Unlock()
	d, _ := env.Values[DelegateDepthKey].(int)
	return d
}

// ChildSpecLite describes one child of delegate_task. It mirrors agent.ChildSpec, because
// tools cannot import agent.
type ChildSpecLite struct {
	Goal, Context string
	// Role is "leaf" or "orchestrator".
	Role string
	// Toolsets can only narrow the toolsets of the parent turn. Empty keeps them.
	Toolsets []string
	// Depth is the depth of the parent run: 0 for a top-level run.
	Depth int
	// Name names the child in the progress lines and the approval prompts, for example "2/3".
	Name string
	// OnToolStart receives each tool start of the child. It can be nil.
	OnToolStart func(name, preview string)
	// ParentEnv is the Env of the delegate_task call.
	ParentEnv *Env
}

// ChildResultLite is the result of one child. It mirrors agent.ChildResult.
type ChildResultLite struct {
	// Status is "ok", "error", "timeout", or "cancelled".
	Status    string
	Summary   string
	ToolCalls int
	DurationS float64
	Error     string
}

// ChildRunner runs one child and waits for its result. The app gives an adapter over
// agent.Agent.RunChild.
type ChildRunner interface {
	RunChild(ctx context.Context, spec ChildSpecLite) ChildResultLite
}

// BackgroundStarter starts the children of a background batch and returns at once. When
// the batch ends, a synthetic user turn on the session key of env gives the results.
type BackgroundStarter interface {
	StartBackground(ctx context.Context, env *Env, specs []ChildSpecLite) (batchID string, err error)
}

// DelegateTool is the delegate_task tool. The limits come from the config
// (delegation block). The model cannot change them. A value that is not positive uses the
// default.
type DelegateTool struct {
	Runner ChildRunner
	// Background starts the background batches. Nil turns background mode off.
	Background BackgroundStarter
	// MaxConcurrent is delegation.max_concurrent_children: the maximum tasks of one call.
	MaxConcurrent int
	// MaxDepth is delegation.max_spawn_depth.
	MaxDepth int
	// MaxIterations and TimeoutS are the limits of each child. The description states them.
	MaxIterations, TimeoutS int
}

type delegateTask struct {
	Goal     string   `json:"goal"`
	Context  string   `json:"context"`
	Toolsets []string `json:"toolsets"`
	Role     string   `json:"role"`
}

type delegateArgs struct {
	delegateTask
	Tasks      []delegateTask `json:"tasks"`
	Background bool           `json:"background"`
}

// delegateEntry is one entry of the result of delegate_task.
type delegateEntry struct {
	Task      int     `json:"task"`
	Status    string  `json:"status"`
	Summary   string  `json:"summary"`
	ToolCalls int     `json:"tool_calls"`
	DurationS float64 `json:"duration_s"`
	Error     string  `json:"error,omitempty"`
}

// Name returns "delegate_task".
func (t DelegateTool) Name() string { return "delegate_task" }

// Toolset returns the delegation toolset.
func (t DelegateTool) Toolset() string { return SetDelegation }

// ParallelSafe returns false. A batch runs its children in parallel itself.
func (t DelegateTool) ParallelSafe() bool { return false }

func (t DelegateTool) maxChildren() int { return positiveOr(t.MaxConcurrent, defaultMaxChildren) }
func (t DelegateTool) maxDepth() int    { return positiveOr(t.MaxDepth, defaultMaxSpawnDepth) }

func positiveOr(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// Schema returns the tool schema. The description states the configured limits, the same
// as the dynamic description of Hermes.
func (t DelegateTool) Schema() provider.ToolSchema {
	n, depth := t.maxChildren(), t.maxDepth()
	iters, timeout := positiveOr(t.MaxIterations, defaultMaxIterations), positiveOr(t.TimeoutS, defaultChildTimeoutS)
	nesting := fmt.Sprintf("Nested delegation is OFF (max_spawn_depth=%d): every child is a leaf and cannot "+
		"delegate further. Raise delegation.max_spawn_depth in config.yaml to enable nesting.", depth)
	roleNote := fmt.Sprintf("Nesting is OFF (max_spawn_depth=%d); 'orchestrator' is silently forced to 'leaf'.", depth)
	if depth >= 2 {
		nesting = fmt.Sprintf("Nested delegation IS enabled (max_spawn_depth=%d): pass role='orchestrator' on a "+
			"child to let it spawn its own workers, up to %d additional level(s) deep.", depth, depth-1)
		roleNote = fmt.Sprintf("Nesting IS enabled (max_spawn_depth=%d): orchestrator children can delegate up to "+
			"%d more level(s) deep.", depth, depth-1)
	}
	desc := "Spawn one or more subagents to work on tasks in isolated contexts. " +
		"Each subagent gets its own conversation and toolsets. Only the final summary is returned; " +
		"intermediate tool results never enter your context window.\n\n" +
		"TWO MODES (one of 'goal' or 'tasks' is required):\n" +
		"1. Single task: provide 'goal' (+ optional context, toolsets).\n" +
		fmt.Sprintf("2. Batch (parallel): provide a 'tasks' array with up to %d items "+
			"(delegation.max_concurrent_children). All run in parallel and the results are returned together. %s\n\n", n, nesting) +
		fmt.Sprintf("LIMITS: each child has at most %d tool rounds and %d s. The limits come from config.yaml.\n\n", iters, timeout) +
		"BACKGROUND: set background to true to start the children and go on at once. The tool returns a batch_id. " +
		"When every child is done, a message '[Background delegation <batch_id> finished]' with the results " +
		"arrives as a new user turn. /stop cancels the running background children.\n\n" +
		"WHEN TO USE delegate_task:\n" +
		"- Reasoning-heavy subtasks (debugging, code review, research synthesis)\n" +
		"- Tasks that would flood your context with intermediate data\n" +
		"- Parallel independent workstreams (research A and B simultaneously)\n\n" +
		"WHEN NOT TO USE:\n" +
		"- Mechanical multi-step work with no reasoning needed: use execute_code\n" +
		"- Single tool call: call the tool directly\n" +
		"- Tasks needing user interaction: subagents cannot use clarify\n\n" +
		"IMPORTANT:\n" +
		"- Subagents have NO memory of your conversation. Pass all relevant info (file paths, error messages, " +
		"constraints) in the 'context' field.\n" +
		"- If the user writes in a language other than English, or asks for a specific language or style, say so in 'context'.\n" +
		"- Subagent summaries are SELF-REPORTS, not verified facts. For operations with external side effects, " +
		"require a verifiable handle (URL, ID, absolute path) and verify it yourself before you tell the user it succeeded.\n" +
		"- Subagents CANNOT call: delegate_task (unless role='orchestrator'), clarify, memory, execute_code, cronjob.\n" +
		"- Results are always returned as an array, one entry per task."
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"goal": map[string]any{"type": "string", "description": "What the subagent should accomplish. Be specific and " +
				"self-contained: the subagent knows nothing about your conversation history."},
			"context": map[string]any{"type": "string", "description": "Background information the subagent needs: file " +
				"paths, error messages, project structure, constraints."},
			"toolsets": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
				"description": "Toolsets for the subagent. They can only narrow your own toolsets. Default: your toolsets. " +
					"Available: " + strings.Join(AllToolsets(), ", ") + "."},
			"role": map[string]any{"type": "string", "enum": []string{"leaf", "orchestrator"},
				"description": "Role of the child. 'leaf' (default) is a focused worker that cannot delegate further. " +
					"'orchestrator' can use delegate_task to spawn its own workers. " + roleNote},
			"tasks": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"goal":     map[string]any{"type": "string", "description": "Task goal"},
						"context":  map[string]any{"type": "string", "description": "Task-specific context"},
						"toolsets": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Toolsets for this task"},
						"role":     map[string]any{"type": "string", "enum": []string{"leaf", "orchestrator"}, "description": "Per-task role override"},
					},
					"required": []string{"goal"},
				},
				"description": fmt.Sprintf("Batch mode: tasks to run in parallel (up to %d, set by "+
					"delegation.max_concurrent_children). When provided, the top-level goal, context, and toolsets are ignored.", n),
			},
			"background": map[string]any{"type": "boolean", "description": "Start the children in the background and " +
				"return a batch_id at once. Default false."},
		},
	}
	b, _ := json.Marshal(params)
	return provider.ToolSchema{Name: "delegate_task", Description: desc, Parameters: b}
}

// Run checks the limits and runs the children. A foreground call waits for every child.
func (t DelegateTool) Run(ctx context.Context, raw json.RawMessage, env *Env) (string, error) {
	var a delegateArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if t.Runner == nil && (!a.Background || t.Background == nil) {
		return "", toolErr("delegation is not configured in this process")
	}
	depth, maxDepth := delegateDepth(env), t.maxDepth()
	if depth >= maxDepth {
		return "", toolErr("Delegation depth limit reached (depth=%d, max_spawn_depth=%d). Raise "+
			"delegation.max_spawn_depth in config.yaml if deeper nesting is necessary.", depth, maxDepth)
	}
	specs, err := t.specs(a, depth, env)
	if err != nil {
		return "", err
	}
	if a.Background {
		return t.runBackground(ctx, env, specs)
	}
	return FormatDelegateResults(t.runForeground(ctx, env, specs)), nil
}

// specs returns the child specs of the call. The children are named "i/n".
func (t DelegateTool) specs(a delegateArgs, depth int, env *Env) ([]ChildSpecLite, error) {
	tasks := a.Tasks
	if len(tasks) > 0 {
		if n, limit := len(tasks), t.maxChildren(); n > limit {
			return nil, toolErr("Too many tasks: %d provided, but max %d tasks per call (delegation.max_concurrent_children). "+
				"Reduce the task count, split the tasks into more delegate_task calls, or raise "+
				"delegation.max_concurrent_children in config.yaml.", n, limit)
		}
		for i, task := range tasks {
			if strings.TrimSpace(task.Goal) == "" {
				return nil, toolErr("Task %d is missing a 'goal'.", i+1)
			}
		}
	} else {
		if strings.TrimSpace(a.Goal) == "" {
			return nil, toolErr("Provide either 'goal' (single task) or 'tasks' (batch).")
		}
		tasks = []delegateTask{a.delegateTask}
	}
	specs := make([]ChildSpecLite, len(tasks))
	for i, task := range tasks {
		role := task.Role
		if role == "" {
			role = a.Role
		}
		specs[i] = ChildSpecLite{
			Goal:      task.Goal,
			Context:   task.Context,
			Role:      normalizeRole(role),
			Toolsets:  task.Toolsets,
			Depth:     depth,
			Name:      fmt.Sprintf("%d/%d", i+1, len(tasks)),
			ParentEnv: env,
		}
	}
	return specs, nil
}

// normalizeRole returns "orchestrator" or "leaf". An unknown role is a leaf (Hermes).
func normalizeRole(r string) string {
	if strings.EqualFold(strings.TrimSpace(r), "orchestrator") {
		return "orchestrator"
	}
	return "leaf"
}

// runForeground runs the children with at most MaxConcurrent at the same time, and returns
// the results in task order. The progress lines of the children go to env.Progress one at a
// time.
func (t DelegateTool) runForeground(ctx context.Context, env *Env, specs []ChildSpecLite) []ChildResultLite {
	var progressMu sync.Mutex
	results := make([]ChildResultLite, len(specs))
	sem := make(chan struct{}, t.maxChildren())
	var wg sync.WaitGroup
	for i := range specs {
		spec := specs[i]
		if env != nil && env.Progress != nil {
			progress := env.Progress
			spec.OnToolStart = func(name, preview string) {
				progressMu.Lock()
				defer progressMu.Unlock()
				progress(ChildProgressLine(spec.Name, name, preview))
			}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = ChildResultLite{Status: "cancelled", Error: "the child was cancelled before it started"}
				return
			}
			defer func() { <-sem }()
			results[i] = t.Runner.RunChild(ctx, spec)
		}()
	}
	wg.Wait()
	return results
}

// runBackground starts the batch and returns its id and the child names at once.
func (t DelegateTool) runBackground(ctx context.Context, env *Env, specs []ChildSpecLite) (string, error) {
	if t.Background == nil {
		return "", toolErr("background delegation is not available in this process. Run the tasks without background")
	}
	if env == nil || env.SessionKey == "" {
		return "", toolErr("background delegation needs a session key, and this run has none. Run the tasks without background")
	}
	if env.RunRootKind() == "cron" {
		return "", toolErr("background delegation is not available in a cron job, because the job ends before the children. Run the tasks without background")
	}
	id, err := t.Background.StartBackground(ctx, env, specs)
	if err != nil {
		return "", toolErr("cannot start the background delegation: %v", err)
	}
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.Name
	}
	b, _ := json.Marshal(struct {
		BatchID  string   `json:"batch_id"`
		Children []string `json:"children"`
	}{id, names})
	return string(b), nil
}

// ChildProgressLine returns the tool progress line of one tool start of a child, for
// example `🔀 [2/3] browser_navigate "https://example.com"`.
func ChildProgressLine(child, tool, preview string) string {
	line := "🔀 [" + child + "] " + tool
	if preview != "" {
		line += ` "` + preview + `"`
	}
	return line
}

// FormatDelegateResults returns the result JSON of delegate_task:
// {"results":[{"task":1,"status":"ok","summary":"…","tool_calls":N,"duration_s":D}]}.
func FormatDelegateResults(results []ChildResultLite) string {
	entries := make([]delegateEntry, len(results))
	for i, r := range results {
		entries[i] = delegateEntry{
			Task:      i + 1,
			Status:    r.Status,
			Summary:   r.Summary,
			ToolCalls: r.ToolCalls,
			DurationS: math.Round(r.DurationS*100) / 100,
			Error:     r.Error,
		}
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(struct {
		Results []delegateEntry `json:"results"`
	}{entries})
	return strings.TrimSuffix(b.String(), "\n")
}
