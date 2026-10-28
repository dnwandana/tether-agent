package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// ChildBlockedTools are the tools that a child never sees: Hermes DELEGATE_BLOCKED_TOOLS
// plus cronjob.
var ChildBlockedTools = []string{"delegate_task", "clarify", "memory", "execute_code", "cronjob"}

// The Env.Values keys of a child run.
const (
	// ChildNameKey holds the name of the child, for example "1/3". The approval prompt
	// shows it.
	ChildNameKey = "child_name"
	// ChildDepthKey holds the depth of the child run: 1 for a child of a top-level run.
	ChildDepthKey = "delegate_depth"
)

// The values of ChildResult.Status.
const (
	ChildOK        = "ok"
	ChildError     = "error"
	ChildTimeout   = "timeout"
	ChildCancelled = "cancelled"
)

// The kinds of a forked session.
const (
	KindSubagent = "subagent"
	KindReview   = "review"
)

// titleMax is the maximum number of characters of the title of a forked session.
const titleMax = 60

// ChildSpec describes one child run.
type ChildSpec struct {
	Goal, Context string
	// Role is "leaf" or "orchestrator". An orchestrator keeps delegate_task while its
	// depth is below delegation.max_spawn_depth.
	Role string
	// Toolsets can only narrow ParentToolsets. Empty keeps the parent sets.
	Toolsets       []string
	ParentSession  *sessions.Session
	ParentToolsets map[string]bool
	// Depth is the depth of the parent run: 0 for a top-level run.
	Depth int
	// Name names the child in the progress lines and the approval prompts, for example "1/3".
	Name string
	Sink Sink
	// ParentEnv gives the work dir, the data dir, the approval gate, and the logger of the
	// parent. It can be nil. The child gets its own Values map.
	ParentEnv *tools.Env
}

// ChildResult is the result of one child run. The parent sees only this result, never the
// tool calls of the child.
type ChildResult struct {
	// Status is ChildOK, ChildError, ChildTimeout, or ChildCancelled.
	Status    string
	Summary   string
	ToolCalls int
	DurationS float64
	Error     string
}

// RunChild runs one child: a fresh conversation in its own session, with a focused system
// prompt, the narrowed toolsets, and the limits of the config. The model
// cannot change the limits.
func (a *Agent) RunChild(ctx context.Context, spec ChildSpec) (res ChildResult) {
	start := time.Now()
	defer func() { res.DurationS = max(time.Since(start).Seconds(), 1e-6) }()
	if spec.ParentSession == nil {
		return ChildResult{Status: ChildError, Error: "the child has no parent session"}
	}
	cfg := a.config()
	limits := delegationLimits(cfg)

	override := spec.ParentSession.ModelOverride
	if dp := strings.TrimSpace(cfg.Agent.DelegationProvider); dp != "" {
		override = dp
	}
	sess, err := a.forkSession(ctx, spec.ParentSession, KindSubagent, "subagent: "+spec.Goal, override)
	if err != nil {
		return ChildResult{Status: ChildError, Error: err.Error()}
	}

	orchestrator := spec.Role == "orchestrator" && spec.Depth+1 < limits.MaxSpawnDepth
	remove := ChildBlockedTools
	system := ChildSystemPrompt(spec.Goal, spec.Context)
	if orchestrator {
		remove = without(ChildBlockedTools, "delegate_task")
		system += "\n" + orchestratorNote(spec.Depth+1, limits.MaxSpawnDepth)
	}
	parentSets := make([]string, 0, len(spec.ParentToolsets))
	for name, on := range spec.ParentToolsets {
		if on {
			parentSets = append(parentSets, name)
		}
	}
	// spec.Toolsets can only narrow the parent sets. An empty spec.Toolsets keeps them all.
	sets := tools.Resolve(parentSets, spec.Toolsets, nil)

	env := childEnv(spec.ParentEnv, sess)
	defer releaseApprovals(env)
	env.Values[ChildNameKey] = spec.Name
	env.Values[ChildDepthKey] = spec.Depth + 1

	cctx, cancel := context.WithTimeout(ctx, time.Duration(limits.ChildTimeoutS)*time.Second)
	defer cancel()
	tr, err := a.RunTurn(cctx, Turn{
		Session:     sess,
		UserText:    spec.Goal,
		Toolsets:    sets,
		RemoveTools: remove,
		Env:         env,
		Sink:        spec.Sink,
		System:      system,
		MaxTurns:    limits.MaxIterations,
	})
	partial, calls, lastTool := a.childActivity(context.WithoutCancel(ctx), sess.ID)
	res.ToolCalls = calls
	switch {
	case err == nil:
		res.Status, res.Summary = ChildOK, tr.Final
		if strings.TrimSpace(tr.Final) == "" {
			res.Status, res.Error = ChildError, "the child ended without a final answer"
		}
	case ctx.Err() != nil:
		res.Status, res.Summary, res.Error = ChildCancelled, partial, "the child was cancelled"
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		res.Status = ChildTimeout
		res.Error = fmt.Sprintf("the child timed out after %ds", limits.ChildTimeoutS)
		activity := fmt.Sprintf("[activity: %d tool calls before the timeout", calls)
		if lastTool != "" {
			activity += "; last tool: " + lastTool
		}
		res.Summary = joinNote(partial, activity+"]")
	default:
		res.Status, res.Summary, res.Error = ChildError, firstNonEmpty(tr.Final, partial), err.Error()
	}
	return res
}

// delegationLimits returns cfg.Delegation, with the defaults in place of values that are
// not positive. MaxSpawnDepth 0 is kept: it turns orchestrators off.
func delegationLimits(cfg *config.Config) config.DelegationConfig {
	l, def := cfg.Delegation, config.Default().Delegation
	if l.MaxIterations <= 0 {
		l.MaxIterations = def.MaxIterations
	}
	if l.ChildTimeoutS <= 0 {
		l.ChildTimeoutS = def.ChildTimeoutS
	}
	return l
}

// forkSession creates the session of a child or a review run. It copies the channel data
// of the parent. It has no session key, so no channel makes it active.
func (a *Agent) forkSession(ctx context.Context, parent *sessions.Session, kind, title, modelOverride string) (*sessions.Session, error) {
	sess, err := a.Store.Create(context.WithoutCancel(ctx), sessions.Session{
		Source:        parent.Source,
		ChatID:        parent.ChatID,
		ThreadID:      parent.ThreadID,
		UserID:        parent.UserID,
		Title:         capRunes(oneLine(title), titleMax),
		ModelOverride: modelOverride,
		ParentID:      parent.ID,
		Kind:          kind,
	})
	if err != nil {
		return nil, fmt.Errorf("agent: create the %s session: %w", kind, err)
	}
	return sess, nil
}

// childEnv returns the tool context of a forked run. It copies the shared fields of the
// parent context, but not Ask (no user questions) and not Values. RootKind keeps the kind
// of the top-level run, so the gate applies approvals.cron_mode to a child of a cron run.
func childEnv(parent *tools.Env, sess *sessions.Session) *tools.Env {
	env := &tools.Env{SessionID: sess.ID, SessionKey: sess.Key, Kind: sess.Kind, Channel: sess.Source, Values: map[string]any{}}
	if parent != nil {
		env.WorkDir, env.DataDir, env.Gate, env.Log, env.Progress = parent.WorkDir, parent.DataDir, parent.Gate, parent.Log, parent.Progress
		env.RootKind = parent.RunRootKind()
		if parent.Channel != "" {
			env.Channel = parent.Channel
		}
	}
	return env
}

// releaseApprovals removes the "This session" answers of the forked run of env from the
// approval gate. The forked session ends with the run, so the gate must not keep them.
func releaseApprovals(env *tools.Env) {
	if r, ok := env.Gate.(tools.SessionResetter); ok {
		r.ResetSession(env.SessionID)
	}
}

// childActivity returns the last answer text, the number of tool calls, and the name of
// the last tool of a forked session.
func (a *Agent) childActivity(ctx context.Context, sessionID string) (lastText string, calls int, lastTool string) {
	msgs, err := a.Store.Messages(ctx, sessionID)
	if err != nil {
		a.logger().Warn("agent: cannot read the child session", "session", sessionID, "err", err.Error())
		return "", 0, ""
	}
	for _, m := range msgs {
		if m.Msg.Role != provider.RoleAssistant {
			continue
		}
		if m.Msg.Content != "" {
			lastText = m.Msg.Content
		}
		calls += len(m.Msg.ToolCalls)
		if n := len(m.Msg.ToolCalls); n > 0 {
			lastTool = m.Msg.ToolCalls[n-1].Name
		}
	}
	return lastText, calls, lastTool
}

// ChildSystemPrompt returns the focused system prompt of a child. It is a port of Hermes
// _build_child_system_prompt.
func ChildSystemPrompt(goal, context string) string {
	parts := []string{
		"You are a focused subagent working on a specific delegated task.",
		"",
		"YOUR TASK:\n" + goal,
	}
	if strings.TrimSpace(context) != "" {
		parts = append(parts, "\nCONTEXT:\n"+context)
	}
	parts = append(parts, "\nComplete this task using the tools available to you. "+
		"When finished, provide a clear, concise summary of:\n"+
		"- What you did\n"+
		"- What you found or accomplished\n"+
		"- Any files you created or modified\n"+
		"- Any issues encountered\n\n"+
		"Important workspace rule: Never assume a repository lives at /workspace/... or any other container-style path "+
		"unless the task/context explicitly gives that path. If no exact local path is provided, discover it first "+
		"before issuing git/workdir-specific commands.\n\n"+
		"Be thorough but concise -- your response is returned to the parent agent as a summary.")
	return strings.Join(parts, "\n")
}

// orchestratorNote returns the delegation block of an orchestrator child (Hermes).
// childDepth is the depth of the child.
func orchestratorNote(childDepth, maxDepth int) string {
	childNote := "Your own children can themselves be orchestrators or leaves, depending on the `role` you pass to " +
		"delegate_task. Default is 'leaf'; pass role='orchestrator' explicitly when a child needs to further " +
		"decompose its work."
	if childDepth+1 >= maxDepth {
		childNote = "Your own children MUST be leaves (cannot delegate further) because they would be at the depth " +
			"floor — you cannot pass role='orchestrator' to your own delegate_task calls."
	}
	return "\n## Subagent Spawning (Orchestrator Role)\n" +
		"You have access to the `delegate_task` tool and CAN spawn your own subagents to parallelize independent work.\n\n" +
		"WHEN to delegate:\n" +
		"- The goal decomposes into 2+ independent subtasks that can run in parallel (e.g. research A and B simultaneously).\n" +
		"- A subtask is reasoning-heavy and would flood your context with intermediate data.\n\n" +
		"WHEN NOT to delegate:\n" +
		"- Single-step mechanical work — do it directly.\n" +
		"- Trivial tasks you can execute in one or two tool calls.\n" +
		"- Re-delegating your entire assigned goal to one worker (that's just pass-through with no value added).\n\n" +
		"Coordinate your workers' results and synthesize them before reporting back to your parent. You are " +
		"responsible for the final summary, not your workers.\n\n" +
		fmt.Sprintf("NOTE: You are at depth %d. The delegation tree is capped at max_spawn_depth=%d. %s", childDepth, maxDepth, childNote)
}

// without returns list without name.
func without(list []string, name string) []string {
	out := make([]string, 0, len(list))
	for _, n := range list {
		if n != name {
			out = append(out, n)
		}
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
