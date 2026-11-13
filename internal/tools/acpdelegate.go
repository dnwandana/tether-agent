package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/acp"
	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
)

// ACPDelegator runs one coding task in an ACP session. *acp.Manager implements it.
type ACPDelegator interface {
	Delegate(ctx context.Context, d acp.Delegation) (acp.Outcome, error)
}

// acpTextMax is the maximum number of characters of the text in the result. The end stays,
// because the summary of the coding agent is usually at the end.
const acpTextMax = 16000

// defaultACPTurnTimeoutS is the time limit of a turn when acp.turn_timeout_s is not positive.
const defaultACPTurnTimeoutS = 1800

type acpDelegateTool struct {
	cfg             config.ACPConfig
	d               ACPDelegator
	approvalTimeout time.Duration
	agents          []string // cfg.EnabledAgents(), sorted
}

type acpDelegateArgs struct {
	Agent            string `json:"agent"`
	Task             string `json:"task"`
	Workdir          string `json:"workdir"`
	Model            string `json:"model"`
	PermissionPreset string `json:"permission_preset"`
	TimeoutS         int    `json:"timeout_s"`
}

type acpDelegateResult struct {
	Agent      string `json:"agent"`
	Session    string `json:"session"`
	StopReason string `json:"stop_reason"`
	DurationS  int    `json:"duration_s"`
	ToolCalls  int    `json:"tool_calls"`
	Text       string `json:"text"`
	Note       string `json:"note,omitempty"`
}

// NewACPDelegateTool returns the acp_delegate tool. approvalTimeout is the time limit of
// one permission question to the user.
func NewACPDelegateTool(cfg config.ACPConfig, d ACPDelegator, approvalTimeout time.Duration) Tool {
	return &acpDelegateTool{cfg: cfg, d: d, approvalTimeout: approvalTimeout, agents: cfg.EnabledAgents()}
}

func (t *acpDelegateTool) Name() string       { return "acp_delegate" }
func (t *acpDelegateTool) Toolset() string    { return SetDelegation }
func (t *acpDelegateTool) ParallelSafe() bool { return false }

// turnTimeoutS returns acp.turn_timeout_s, or the default when it is not positive.
func (t *acpDelegateTool) turnTimeoutS() int {
	if t.cfg.TurnTimeoutS > 0 {
		return t.cfg.TurnTimeoutS
	}
	return defaultACPTurnTimeoutS
}

// Schema returns the same bytes on each call, because the provider prompt cache needs them.
// json.Marshal sorts the map keys, and t.agents is sorted.
func (t *acpDelegateTool) Schema() provider.ToolSchema {
	desc := "Give a coding task to an external coding agent, for example Claude Code, over the Agent " +
		"Client Protocol. The coding agent works in workdir with its own tools. It sees only the task " +
		"text and the files, so put all the facts that it needs in task. Each permission request of the " +
		"coding agent comes to the user as an approval message. The tool waits for the end of the turn " +
		"and returns the final text of the coding agent. A later call in the same session continues " +
		"the same coding agent session. Do not use this tool for a small change that you can make " +
		"yourself."
	agents := t.agents
	if agents == nil {
		agents = []string{}
	}
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agent": map[string]any{"type": "string", "enum": agents,
				"description": "The id of the coding agent."},
			"task": map[string]any{"type": "string", "description": "The full brief for the coding agent: " +
				"the goal, the files, the constraints, and how to check the result."},
			"workdir": map[string]any{"type": "string", "description": "The absolute path of the repository folder. " +
				"It must be inside acp.workdir_allowlist. Required, except in tether chat, where the default is " +
				"the current folder."},
			"model": map[string]any{"type": "string", "description": "The model of the coding agent. " +
				"Default: the model in the config of the agent."},
			"permission_preset": map[string]any{"type": "string", "enum": config.ACPPresets,
				"description": "The config preset of the agent or a stricter one. The order from strict to loose " +
					"is all, risky, auto, folder. Default: the config preset."},
			"timeout_s": map[string]any{"type": "integer", "description": fmt.Sprintf("The time limit of this "+
				"turn in seconds. At most %d (acp.turn_timeout_s).", t.turnTimeoutS())},
		},
		"required": []string{"agent", "task"},
	}
	b, _ := json.Marshal(params)
	return provider.ToolSchema{Name: t.Name(), Description: desc, Parameters: b}
}

// Run checks the call, gives the task to the coding agent, and waits for the end of the turn.
func (t *acpDelegateTool) Run(ctx context.Context, raw json.RawMessage, env *Env) (string, error) {
	var a acpDelegateArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if env.RunRootKind() == "cron" {
		return "", toolErr("acp: acp_delegate runs only in a chat session, not in a cron job.")
	}
	if env == nil || env.SessionID == "" {
		return "", toolErr("acp: acp_delegate needs a chat session. This run has no session.")
	}
	agentCfg, ok := t.cfg.Agents[a.Agent]
	if !ok || !agentCfg.Enabled {
		return "", toolErr("acp: agent %q is unknown or disabled. Enabled agents: %s.", a.Agent, strings.Join(t.agents, ", "))
	}
	if strings.TrimSpace(a.Task) == "" {
		return "", toolErr("acp: give task, the full brief for the coding agent.")
	}
	preset := a.PermissionPreset
	if preset == "" {
		preset = agentCfg.PermissionPreset
	}
	rank := config.ACPPresetRank(preset)
	if rank < 0 {
		return "", toolErr("acp: permission_preset %q is not known. Use one of: %s.", preset, strings.Join(config.ACPPresets, ", "))
	}
	if rank > config.ACPPresetRank(agentCfg.PermissionPreset) {
		return "", toolErr("acp: permission_preset %q is looser than the config preset %q. Use the config preset or a stricter one.",
			preset, agentCfg.PermissionPreset)
	}
	cwd, err := t.workdir(a.Workdir, env)
	if err != nil {
		return "", err
	}
	timeoutS := t.turnTimeoutS()
	if a.TimeoutS > 0 && a.TimeoutS < timeoutS {
		timeoutS = a.TimeoutS
	}

	h := t.hooks(a.Agent, env)
	tctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutS)*time.Second)
	defer cancel()
	start := time.Now()
	out, err := t.d.Delegate(tctx, acp.Delegation{SessionID: env.SessionID, Agent: a.Agent, CWD: cwd,
		Model: strings.TrimSpace(a.Model), Preset: preset, Task: a.Task, Hooks: h})
	if err != nil {
		return "", toolErr("%s", err.Error())
	}
	notes := append([]string(nil), out.Notes...)
	if out.StopReason == "cancelled" && errors.Is(tctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		notes = append(notes, fmt.Sprintf("the turn timed out after %d s", timeoutS))
	}
	res := acpDelegateResult{
		Agent:      a.Agent,
		Session:    out.Session,
		StopReason: out.StopReason,
		DurationS:  int(time.Since(start).Round(time.Second) / time.Second),
		ToolCalls:  out.ToolCalls,
		Text:       cutACPText(out.Text, t.cfg.ResultMaxChars),
		Note:       strings.Join(notes, "; "),
	}
	b, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// hooks builds the hooks of the coding agent from the env of the current turn.
func (t *acpDelegateTool) hooks(agent string, env *Env) acp.Hooks {
	h := acp.Hooks{ApprovalTimeout: t.approvalTimeout, Member: env.Member}
	// The bridge refuses the same paths as the write_file tool.
	home, _ := os.UserHomeDir()
	h.Protected = security.ProtectedPaths(env.DataDir, home)
	if af, ok := env.Gate.(ACPFloor); ok {
		h.Floor = func(command string) (string, error) { return af.ACPFloor(env, command) }
	}
	if env.Progress != nil {
		progress := env.Progress
		h.Progress = func(kind, title string) { progress(0, agent, kind, title) }
	}
	pa, _ := env.Gate.(PermissionAsker)
	h.Approve = func(ctx context.Context, req security.ApprovalRequest) (security.Scope, error) {
		// With no PermissionAsker, a deny makes the bridge select the reject option.
		if pa == nil {
			return security.ScopeDeny, nil
		}
		return pa.AskPermission(ctx, env, req)
	}
	if env.Ask != nil {
		asker := env.Ask
		h.Ask = func(ctx context.Context, text string, choices []string, timeout time.Duration) (string, error) {
			ans, err := asker.Ask(ctx, Question{Text: text, Choices: choices, Timeout: timeout})
			if err != nil || len(ans.Choices) == 0 {
				return "", err
			}
			return ans.Choices[0], nil
		}
	}
	return h
}

// workdir returns the resolved folder of the coding agent. The resolved folder must be in
// acp.workdir_allowlist, so a symlink cannot point the agent to a folder outside it.
func (t *acpDelegateTool) workdir(arg string, env *Env) (string, error) {
	allow := t.cfg.WorkdirAllowlist
	if len(allow) == 0 {
		return "", toolErr("acp: acp.workdir_allowlist is empty. Add the folder of your projects to acp.workdir_allowlist.")
	}
	p := arg
	if p == "" {
		if env.Channel != "cli" || env.WorkDir == "" {
			return "", toolErr("acp: give workdir, the folder of the repository. Allowed: %s", strings.Join(allow, ", "))
		}
		p = env.WorkDir
	}
	if !filepath.IsAbs(p) {
		if env.WorkDir == "" {
			return "", toolErr("acp: workdir %s is outside acp.workdir_allowlist.", p)
		}
		p = filepath.Join(env.WorkDir, p)
	}
	p = filepath.Clean(p)
	wd, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", toolErr("acp: workdir %s does not exist.", p)
	}
	if fi, err := os.Stat(wd); err != nil || !fi.IsDir() {
		return "", toolErr("acp: workdir %s is not a folder.", p)
	}
	for _, entry := range allow {
		real, err := filepath.EvalSymlinks(entry)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(real, wd)
		if err != nil {
			continue
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return wd, nil
		}
	}
	return "", toolErr("acp: workdir %s is outside acp.workdir_allowlist.", p)
}

// cutACPText keeps the last max characters of s and puts the count of the cut
// characters at the start. A max of 0 or less uses acpTextMax.
func cutACPText(s string, max int) string {
	max = positiveOr(max, acpTextMax)
	r := []rune(s)
	n := len(r)
	if n <= max {
		return s
	}
	return fmt.Sprintf("…(%d characters cut) ", n-max) + string(r[n-max:])
}
