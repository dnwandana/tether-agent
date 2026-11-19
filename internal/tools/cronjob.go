package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// cronListLayout is the time format of the next and last runs in replies.
const cronListLayout = "2006-01-02 15:04 MST"

// CronJobTool is the cronjob tool.
type CronJobTool struct {
	Store *cron.Store
	// RunNow runs a job at once. It is nil in the chat role; run then makes
	// the job due now, and the next gateway tick runs it.
	RunNow func(ctx context.Context, id string) error
	// CurrentProvider returns the provider name of the session. create and
	// update pin it when model has no provider. It may be nil.
	CurrentProvider func(env *Env) string
	// Sessions gives the chat and the thread of the session for the origin.
	// When it is nil, or the session row has no chat, the origin comes from
	// the session key.
	Sessions *sessions.Store
	// ScriptsDir is the scripts folder. When it is set, create and update
	// check that the script exists in it.
	ScriptsDir string
	// MissedRuns is cron.missed_runs: "skip" (also "") or "catchup". The
	// result of run without a gateway tells what happens to the run.
	MissedRuns string
	// Tick, GraceMin, and GraceMax are the scheduler values that the result of run without
	// a gateway shows. 0 uses 30 s, 2 min, and 2 h.
	Tick     time.Duration
	GraceMin time.Duration
	GraceMax time.Duration
	// PrivateTarget reports whether the deliver value of a call stays with the owner. Nil
	// skips this check of the tool. The check of the store still runs.
	PrivateTarget func(ctx context.Context, env *Env, deliver string) bool
}

// cronSpan formats d as "2 h", "2 min", or "30 s".
func cronSpan(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%d h", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%d min", d/time.Minute)
	}
	return fmt.Sprintf("%d s", d/time.Second)
}

type cronModelArg struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type cronArgs struct {
	Action    string        `json:"action"`
	JobID     string        `json:"job_id"`
	Name      *string       `json:"name"`
	Prompt    *string       `json:"prompt"`
	Schedule  *string       `json:"schedule"`
	Repeat    *int          `json:"repeat"`
	Deliver   *string       `json:"deliver"`
	Skills    *[]string     `json:"skills"`
	Model     *cronModelArg `json:"model"`
	Script    *string       `json:"script"`
	NoAgent   *bool         `json:"no_agent"`
	DraftOnly *bool         `json:"draft_only"`
	Toolsets  *[]string     `json:"toolsets"`
	Workdir   *string       `json:"workdir"`
	Limit     *int          `json:"limit"`
	Offset    *int          `json:"offset"`
	RunID     *int64        `json:"run_id"`
}

// Name returns "cronjob".
func (CronJobTool) Name() string { return "cronjob" }

// ReadOnlyCall reports whether the call only reads the jobs: the actions list and runs. It
// decodes args with the struct of Run, so it sees the same action.
func (CronJobTool) ReadOnlyCall(args json.RawMessage) bool {
	var a cronArgs
	if err := decodeArgs(args, &a); err != nil {
		return false
	}
	return a.Action == "list" || a.Action == "runs"
}

// Toolset returns the cron toolset.
func (CronJobTool) Toolset() string { return SetCron }

// ParallelSafe returns false, because the tool changes the job table.
func (CronJobTool) ParallelSafe() bool { return false }

// Schema returns the tool schema for the model. The text is ported from
// Hermes tools/cronjob_tools.py, without context_from.
func (CronJobTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{
		Name: "cronjob",
		Description: `Manage scheduled cron jobs with a single compressed tool.

Use action='create' to schedule a new job from a prompt or one or more skills.
Use action='list' to inspect jobs.
Use action='update', 'pause', 'resume', 'remove', or 'run' to manage an existing job.
Use action='runs' with job_id to see the last runs of a job: the status, the delivery, the duration, and the output.

To stop a job the user no longer wants: first action='list' to find the job_id, then action='remove' with that job_id. Never guess job IDs — always list first.

Jobs run in a fresh session with no current-chat context, so prompts must be self-contained.
If skills are provided on create, the future cron run loads those skills in order, then follows the prompt as the task instruction.
On update, passing skills=[] clears attached skills.

NOTE: The agent's final response is auto-delivered to the target. When a run finds nothing new, the agent answers [SILENT], and nothing is sent. A failed run always sends an alert, except with deliver='local'. Put the primary user-facing content in the final response. Cron jobs run autonomously with no user present — they cannot ask questions or request clarification.

SCRIPT JOBS (no_agent=true): the scheduler runs the script with no model call and sends its stdout word for word. Empty stdout sends nothing, so make the script quiet when there is nothing to report. A failure (non-zero exit or timeout) always alerts.

Important safety rule: cron-run sessions should not recursively schedule more cron jobs.`,
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "action": {"type": "string", "enum": ["create", "list", "update", "pause", "resume", "remove", "run", "runs"], "description": "One of: create, list, update, pause, resume, remove, run, runs. When action=create, the 'schedule' field and a 'prompt' (or no_agent with a script) are REQUIRED."},
    "job_id": {"type": "string", "description": "Required for update/pause/resume/remove/run/runs. Take it from action='list'."},
    "prompt": {"type": "string", "description": "For create: the full self-contained prompt. If skills are also provided, this becomes the task instruction paired with those skills."},
    "schedule": {"type": "string", "description": "REQUIRED for action=create. For create/update: '30m', 'every 2h', '0 9 * * *', or ISO timestamp. Examples: '30m' (every 30 minutes), 'every 2h' (every 2 hours), '0 9 * * *' (daily at 9am), '2026-06-01T09:00:00' (one-shot). The time zone is the host time zone."},
    "name": {"type": "string", "description": "Optional human-friendly name"},
    "repeat": {"type": "integer", "description": "Optional repeat count. Omit for defaults (once for one-shot, forever for recurring)."},
    "deliver": {"type": "string", "description": "Omit this parameter to auto-deliver back to the current chat and topic (recommended). Only set it when the user asks to deliver somewhere else. Values: 'origin' (same as omitting), 'local' (no delivery, save only), or telegram:<chat_id>[:<thread_id>] for a specific destination, for example 'telegram:-1001234567890:17585'. WARNING: 'telegram:<chat_id>' without :<thread_id> loses topic targeting."},
    "skills": {"type": "array", "items": {"type": "string"}, "description": "Optional ordered list of skill names to load before executing the cron prompt. On update, pass an empty array to clear attached skills."},
    "model": {
      "type": "object",
      "description": "Optional per-job model override. If provider is omitted, the current main provider is pinned at creation time so the job stays stable.",
      "properties": {
        "provider": {"type": "string", "description": "Provider name from the providers config. Omit to use and pin the current provider."},
        "model": {"type": "string", "description": "Model name"}
      },
      "required": ["model"]
    },
    "script": {"type": "string", "description": "Optional path to a script. A relative path resolves under ~/.tether/scripts/. .sh and .bash files run with bash, other files with python3. In an agent job, the script runs first and its stdout goes into the prompt as data. With no_agent=true, the script IS the job: empty stdout sends nothing; a failure always alerts. On update, pass an empty string to clear."},
    "no_agent": {"type": "boolean", "description": "Default false: the agent runs the prompt each tick. Set true to skip the model: the scheduler runs the script and delivers its stdout word for word, with zero tokens. Requires script."},
    "draft_only": {"type": "boolean", "description": "Default false. Set true to make the job draft-only: it can read the web and read MCP data, and it can write drafts, but it cannot send. The run has no terminal, execute_code, browser actions, or delegate_task, and only MCP read tools and draft tools. A draft-only job delivers only to a private chat or local, and it cannot use no_agent. On update, pass false to clear."},
    "toolsets": {"type": "array", "items": {"type": "string"}, "description": "Optional list of toolset names to restrict the job's agent to (e.g. [\"web\", \"terminal\", \"file\"]). On update, pass an empty array to clear."},
    "workdir": {"type": "string", "description": "Optional absolute path of an existing folder to run the job from. On update, pass an empty string to clear."},
    "limit": {"type": "integer", "description": "For runs: the number of runs to show, newest first. Default 10, maximum 50."},
    "offset": {"type": "integer", "description": "For runs: the number of newest runs to skip. Use it to see older runs. Default 0."},
    "run_id": {"type": "integer", "description": "For runs: one run id from the runs list. The result shows the full stored output of that run."}
  },
  "required": ["action"]
}`),
	}
}

// Run does the action of the call.
func (t CronJobTool) Run(ctx context.Context, raw json.RawMessage, env *Env) (string, error) {
	var a cronArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if t.Store == nil {
		return "", toolErr("cron is not available in this session")
	}
	a.JobID = strings.TrimSpace(a.JobID)
	// A caller sees and changes only the jobs of its own chat (callerScope).
	sc, err := callerScope(ctx, t.Sessions, t.Store, env)
	if err != nil {
		return "", err
	}
	if a.Deliver != nil {
		if err := t.checkDeliver(ctx, *a.Deliver, sc, env); err != nil {
			return "", err
		}
	}
	switch a.Action {
	case "create":
		return t.create(ctx, a, env)
	case "list":
		return t.list(ctx, sc)
	}
	if a.JobID == "" {
		switch a.Action {
		case "update", "pause", "resume", "remove", "run", "runs":
			return "", toolErr("job_id is required for %s; call action='list' to get the job ids", a.Action)
		}
	}
	switch a.Action {
	case "update", "pause", "resume", "remove", "run", "runs":
		j, err := t.Store.Get(ctx, a.JobID)
		if err == nil && !sc.Has(j.Origin.SessionKey) {
			// A job of another chat gives the same error as a job that does not exist.
			err = fmt.Errorf("%w (%s)", cron.ErrNotFound, a.JobID)
		}
		if err != nil {
			return "", cronErr(err)
		}
	}
	switch a.Action {
	case "update":
		return t.update(ctx, a, env)
	case "pause", "resume":
		on := a.Action == "resume"
		if err := t.Store.SetEnabled(ctx, a.JobID, on); err != nil {
			return "", cronErr(err)
		}
		j, err := t.Store.Get(ctx, a.JobID)
		if err != nil {
			return "", cronErr(err)
		}
		if on {
			return fmt.Sprintf("resumed job %s (%s); next run %s", j.ID, j.Name, t.timeText(j.NextRun)), nil
		}
		return fmt.Sprintf("paused job %s (%s)", j.ID, j.Name), nil
	case "remove":
		j, err := t.Store.Get(ctx, a.JobID)
		if err != nil {
			return "", cronErr(err)
		}
		if err := t.Store.Remove(ctx, a.JobID); err != nil {
			return "", cronErr(err)
		}
		return fmt.Sprintf("removed job %s (%s)", j.ID, j.Name), nil
	case "run":
		return t.run(ctx, a.JobID)
	case "runs":
		return t.runs(ctx, a)
	}
	return "", toolErr("unknown action %q; use create, list, update, pause, resume, remove, run, or runs", a.Action)
}

func (t CronJobTool) create(ctx context.Context, a cronArgs, env *Env) (string, error) {
	j := cron.Job{Enabled: true}
	setStr := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	setStr(&j.Name, a.Name)
	setStr(&j.Prompt, a.Prompt)
	setStr(&j.Schedule, a.Schedule)
	setStr(&j.Deliver, a.Deliver)
	setStr(&j.Script, a.Script)
	setStr(&j.Workdir, a.Workdir)
	if a.NoAgent != nil {
		j.NoAgent = *a.NoAgent
	}
	if a.DraftOnly != nil {
		j.DraftOnly = *a.DraftOnly
	}
	if a.Repeat != nil {
		j.RepeatLeft = *a.Repeat
	}
	if a.Skills != nil {
		j.Skills = *a.Skills
	}
	if a.Toolsets != nil {
		j.Toolsets = *a.Toolsets
	}
	j.Model = t.modelPin(a.Model, env)
	if err := t.checkScript(j.Script); err != nil {
		return "", err
	}
	if err := t.checkDraftDeliver(ctx, env, j.DraftOnly, j.Deliver); err != nil {
		return "", err
	}
	if env != nil {
		j.Origin = t.origin(ctx, env)
		j.CreatedBySession = env.SessionID
	}
	created, err := t.Store.Create(ctx, j)
	if err != nil {
		return "", cronErr(err)
	}
	return "created job " + created.ID + "\n" + t.jobLine(created), nil
}

func (t CronJobTool) update(ctx context.Context, a cronArgs, env *Env) (string, error) {
	p := cron.JobPatch{
		Name: a.Name, Prompt: a.Prompt, Schedule: a.Schedule, Deliver: a.Deliver, Script: a.Script,
		Workdir: a.Workdir, NoAgent: a.NoAgent, DraftOnly: a.DraftOnly, Skills: a.Skills, Toolsets: a.Toolsets, RepeatLeft: a.Repeat,
	}
	if a.Model != nil {
		pin := t.modelPin(a.Model, env)
		if pin == nil {
			pin = &cron.ModelPin{}
		}
		p.Model = pin
	}
	if p == (cron.JobPatch{}) {
		return "", toolErr("update has no fields to change; pass at least one of name, prompt, schedule, repeat, deliver, skills, model, script, no_agent, draft_only, toolsets, or workdir")
	}
	if a.Script != nil {
		if err := t.checkScript(*a.Script); err != nil {
			return "", err
		}
	}
	if a.DraftOnly != nil || a.Deliver != nil {
		old, err := t.Store.Get(ctx, a.JobID)
		if err != nil {
			return "", cronErr(err)
		}
		draftOnly, deliver := old.DraftOnly, old.Deliver
		if a.DraftOnly != nil {
			draftOnly = *a.DraftOnly
		}
		if a.Deliver != nil {
			deliver = *a.Deliver
		}
		if err := t.checkDraftDeliver(ctx, env, draftOnly, deliver); err != nil {
			return "", err
		}
	}
	j, err := t.Store.Update(ctx, a.JobID, p)
	if err != nil {
		return "", cronErr(err)
	}
	return "updated job " + j.ID + "\n" + t.jobLine(j), nil
}

func (t CronJobTool) run(ctx context.Context, id string) (string, error) {
	j, err := t.Store.Get(ctx, id)
	if err != nil {
		return "", cronErr(err)
	}
	if t.RunNow != nil {
		if err := t.RunNow(ctx, id); err != nil {
			return "", toolErr("cannot run job %s: %v", id, err)
		}
		return fmt.Sprintf("started job %s (%s); the result goes to its delivery target, unless the run answers [SILENT]", j.ID, j.Name), nil
	}
	if !j.Enabled {
		return "", toolErr("job %s is paused; resume it first", id)
	}
	now := time.Now()
	if t.Store.Now != nil {
		now = t.Store.Now()
	}
	if _, err := t.Store.Update(ctx, id, cron.JobPatch{NextRun: &now}); err != nil {
		return "", cronErr(err)
	}
	tick, graceMin, graceMax := t.Tick, t.GraceMin, t.GraceMax
	if tick <= 0 {
		tick = cron.DefaultTick
	}
	if graceMin <= 0 {
		graceMin = 2 * time.Minute
	}
	if graceMax <= 0 {
		graceMax = 2 * time.Hour
	}
	out := fmt.Sprintf("job %s (%s) is due now; the gateway runs it on the next tick (within %s). ",
		j.ID, j.Name, cronSpan(tick))
	if t.MissedRuns == cron.MissedCatchup || t.oneShot(j) {
		return out + "When the gateway is not running, the job runs when it starts", nil
	}
	return out + "When the gateway is not running, the job runs at the gateway start only if the start " +
		"is in the grace window (half the period of the schedule, from " + cronSpan(graceMin) + " to " +
		cronSpan(graceMax) + "). After that window, " +
		"cron.missed_runs: skip skips this run, and the job runs at its next scheduled time", nil
}

// Limits of the runs action.
const (
	cronRunsDefaultLimit = 10
	cronRunsMaxLimit     = 50
	cronRunsListRunes    = 300
)

// runs returns the last runs of the job, newest first, or the one run of run_id with its full output.
func (t CronJobTool) runs(ctx context.Context, a cronArgs) (string, error) {
	j, err := t.Store.Get(ctx, a.JobID)
	if err != nil {
		return "", cronErr(err)
	}
	if a.RunID != nil {
		r, err := t.Store.Run(ctx, j.ID, *a.RunID)
		if err != nil {
			return "", cronErr(err)
		}
		return t.runText(r, r.Output), nil
	}
	limit := cronRunsDefaultLimit
	if a.Limit != nil && *a.Limit > 0 {
		limit = min(*a.Limit, cronRunsMaxLimit)
	}
	offset := 0
	if a.Offset != nil && *a.Offset > 0 {
		offset = *a.Offset
	}
	// One row more than the limit tells whether older runs exist.
	rs, err := t.Store.Runs(ctx, j.ID, limit+1, offset)
	if err != nil {
		return "", cronErr(err)
	}
	if len(rs) == 0 {
		out := fmt.Sprintf("no runs recorded for job %s (%s)", j.ID, j.Name)
		if t.Store.RunHistory == 0 {
			out += "; cron.run_history is 0, so no runs are kept"
		}
		return out, nil
	}
	more := len(rs) > limit
	if more {
		rs = rs[:limit]
	}
	var b strings.Builder
	// The header names the offset, not a run id, so the model does not take it for a run_id.
	fmt.Fprintf(&b, "%d runs of job %s (%s), newest first", len(rs), j.ID, j.Name)
	if offset > 0 {
		fmt.Fprintf(&b, ", after the newest %d", offset)
	}
	b.WriteString(":\n")
	for _, r := range rs {
		b.WriteString(t.runText(r, cron.TruncateRunes(r.Output, cronRunsListRunes)))
		b.WriteByte('\n')
	}
	if more {
		fmt.Fprintf(&b, "More runs exist: use offset %d.", offset+len(rs))
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// runText returns the head line of r and then the output, with an indent of two spaces on each line.
// The session id is the full id, so the model can pass it to session_search.
func (t CronJobTool) runText(r cron.Run, output string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- run %d · %s · %s · %s · %s", r.ID, t.timeText(r.StartedAt), r.Status, r.Delivery,
		r.FinishedAt.Sub(r.StartedAt).Round(time.Second))
	if r.SessionID != "" {
		fmt.Fprintf(&b, " · session %s", r.SessionID)
	}
	if r.Delivery == cron.DeliveryFailed {
		fmt.Fprintf(&b, " · delivery error: %s", r.DeliveryErr)
	}
	switch r.Delivery {
	case cron.DeliveryHeld:
		b.WriteString(" · held: waits for the owner to tap Send or Drop")
	case cron.DeliveryDropped:
		b.WriteString(" · held, then dropped by the owner")
	case cron.DeliveryExpired:
		b.WriteString(" · held, then expired after 24 h with no tap")
	}
	if output == "" {
		output = "(no output)"
	}
	for line := range strings.SplitSeq(output, "\n") {
		b.WriteString("\n  ")
		b.WriteString(line)
	}
	return b.String()
}

// oneShot reports whether the schedule of j runs one time only. A one-shot run
// that comes late always runs, because it has no next run.
func (t CronJobTool) oneShot(j cron.Job) bool {
	sched, err := cron.Parse(j.Schedule, time.Time{}, t.Store.Loc)
	return err == nil && sched.OneShot()
}

func (t CronJobTool) list(ctx context.Context, sc sessions.Scope) (string, error) {
	all, err := t.Store.List(ctx)
	if err != nil {
		return "", cronErr(err)
	}
	var jobs []cron.Job
	for _, j := range all {
		if sc.Has(j.Origin.SessionKey) {
			jobs = append(jobs, j)
		}
	}
	if len(jobs) == 0 {
		return "no cron jobs", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d cron jobs:\n", len(jobs))
	for _, j := range jobs {
		b.WriteString(t.jobLine(j))
	}
	return b.String(), nil
}

// jobLine returns a short description of j that starts with its exact id.
func (t CronJobTool) jobLine(j cron.Job) string {
	state := "enabled"
	if !j.Enabled {
		state = "paused"
	}
	switch {
	case j.FailStreak == 1:
		state += " (1 failure in a row)"
	case j.FailStreak > 1:
		state += fmt.Sprintf(" (%d failures in a row)", j.FailStreak)
	}
	kind := "agent"
	if j.NoAgent {
		kind = "script"
	} else if j.DraftOnly {
		kind = "agent, draft-only"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "- %s · %s · %s · %s · schedule %q · next run %s", j.ID, j.Name, state, kind, j.Schedule, t.timeText(j.NextRun))
	if j.RepeatLeft > 0 {
		fmt.Fprintf(&b, " · %d runs left", j.RepeatLeft)
	}
	deliver := j.Deliver
	if deliver == "" {
		deliver = "origin"
	}
	fmt.Fprintf(&b, " · deliver %s", deliver)
	if !j.LastRun.IsZero() {
		fmt.Fprintf(&b, " · last run %s %s", t.timeText(j.LastRun), j.LastStatus)
	}
	if j.Script != "" {
		fmt.Fprintf(&b, " · script %s", j.Script)
	}
	if len(j.Skills) > 0 {
		fmt.Fprintf(&b, " · skills %s", strings.Join(j.Skills, ","))
	}
	if j.Model != nil {
		fmt.Fprintf(&b, " · model %s/%s", j.Model.Provider, j.Model.Model)
	}
	b.WriteString("\n")
	return b.String()
}

// timeText formats tm in the time zone of the store, or "none".
func (t CronJobTool) timeText(tm time.Time) string {
	if tm.IsZero() {
		return "none"
	}
	loc := time.UTC
	if t.Store != nil && t.Store.Loc != nil {
		loc = t.Store.Loc
	}
	return tm.In(loc).Format(cronListLayout)
}

// modelPin returns the model pin of the argument. A model without a
// provider pins the current provider of the session (Hermes).
func (t CronJobTool) modelPin(m *cronModelArg, env *Env) *cron.ModelPin {
	if m == nil {
		return nil
	}
	pin := cron.ModelPin{Provider: strings.TrimSpace(m.Provider), Model: strings.TrimSpace(m.Model)}
	if pin.Model != "" && pin.Provider == "" && t.CurrentProvider != nil && env != nil {
		pin.Provider = t.CurrentProvider(env)
	}
	if pin == (cron.ModelPin{}) {
		return nil
	}
	return &pin
}

// checkScript checks that a non-empty script exists in ScriptsDir.
func (t CronJobTool) checkScript(script string) error {
	if t.ScriptsDir == "" || strings.TrimSpace(script) == "" {
		return nil
	}
	if _, err := cron.ResolveScript(script, t.ScriptsDir); err != nil {
		return toolErr("%v", err)
	}
	return nil
}

// origin returns the chat and the thread of the session of env. The session
// row wins over the session key, because a group key can end with a user id.
// A fork has no key, so the key comes from the session that it was forked from.
func (t CronJobTool) origin(ctx context.Context, env *Env) cron.Origin {
	key := env.SessionKey
	if t.Sessions != nil && env.SessionID != "" {
		if k, err := t.Sessions.RootKey(ctx, env.SessionID); err == nil {
			key = k
		}
	}
	o := originFromKey(key)
	if t.Sessions == nil || env.SessionID == "" {
		return o
	}
	sess, err := t.Sessions.Get(ctx, env.SessionID)
	if err != nil || sess.ChatID == "" {
		return o
	}
	src := sess.Source
	if src == "" {
		src = o.Source
	}
	return cron.Origin{Source: src, ChatID: sess.ChatID, ThreadID: sess.ThreadID, SessionKey: key}
}

// checkDraftDeliver refuses a draft-only job with a deliver value that is not private.
// A nil PrivateTarget skips the check, and the store check still runs.
func (t CronJobTool) checkDraftDeliver(ctx context.Context, env *Env, draftOnly bool, deliver string) error {
	if !draftOnly || t.PrivateTarget == nil {
		return nil
	}
	if !t.PrivateTarget(ctx, env, deliver) {
		return toolErr("%s", cron.ErrDraftDeliver.Error())
	}
	return nil
}

// checkDeliver refuses an explicit delivery target outside the chat of a caller that
// does not have the full scope. Such a caller can deliver to "origin", to "local", or
// to telegram:<chat> of its own chat, with any thread.
func (t CronJobTool) checkDeliver(ctx context.Context, deliver string, sc sessions.Scope, env *Env) error {
	d := strings.TrimSpace(deliver)
	if sc.All || d == "" || d == "origin" || d == "local" {
		return nil
	}
	chat, _, _ := strings.Cut(strings.TrimPrefix(d, "telegram:"), ":")
	if env != nil && strings.HasPrefix(d, "telegram:") && chat != "" && chat == t.origin(ctx, env).ChatID {
		return nil
	}
	return toolErr("deliver %q is outside this chat; use 'origin', 'local', or telegram:<chat_id> of this chat", deliver)
}

// originFromKey parses a session key: telegram:dm:<chat>[:<thread>] or
// telegram:group:<chat>[:<thread>[:<user>]]. Other keys give only the source,
// for example "cli".
func originFromKey(key string) cron.Origin {
	o := cron.Origin{SessionKey: key}
	parts := strings.Split(key, ":")
	if key == "" {
		return o
	}
	o.Source = parts[0]
	if parts[0] != "telegram" || len(parts) < 3 {
		return o
	}
	o.ChatID = parts[2]
	if len(parts) >= 4 {
		o.ThreadID = parts[3]
	}
	return o
}

// cronErr returns the error of the cron store for the model. The store
// errors name the cause and the next step.
func cronErr(err error) error {
	return toolErr("%v", err)
}
