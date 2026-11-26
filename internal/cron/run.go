package cron

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/skills"
)

// CronPreamble tells the model how a cron run works. It is the Hermes cron hint
// (cron/scheduler_prompt.py) with the DELIVERY and SILENT rules. It goes in the user message
// of the run, so the system prompt of a chat session does not change.
const CronPreamble = "[IMPORTANT: You are running as a scheduled cron job. No user is present, " +
	"so you cannot ask the user questions or get approval for dangerous commands. " +
	"DELIVERY: Your final response will be automatically delivered to the user — do NOT " +
	"try to deliver the output yourself. Just produce your report/output as your final " +
	"response and the system handles the rest. " +
	"SILENT: If there is genuinely nothing new to report, respond with exactly \"[SILENT]\" " +
	"(nothing else) to suppress delivery. [SILENT] is a literal ASCII control token — never " +
	"translate or rephrase it, whatever language the rest of your answer uses. Never combine " +
	"[SILENT] with content — either report your findings normally, or say [SILENT] and nothing more.]"

// cronRemovedTools are the tools that a cron run cannot use: no user is present,
// and a run cannot create cron jobs.
var cronRemovedTools = []string{"cronjob", "clarify"}

// draftOnlyPreamble is the sentence that CronPreamble gets for a draft-only run.
const draftOnlyPreamble = "DRAFT-ONLY: This job can read and write drafts. It cannot send, post, or change " +
	"anything outside this machine. Put what it would send into a draft or into your final response."

// draftOnlyRemovedTools are the tools that can send or change things outside this machine. A
// draft-only run cannot see them.
var draftOnlyRemovedTools = []string{"terminal", "execute_code", "delegate_task", "acp_delegate",
	"browser_click", "browser_fill", "browser_run_js", "browser_cookies"}

// errAgentTimeout is the cause of the context of an agent turn that reached AgentTimeout.
// It tells a time-out from a stop of the gateway.
var errAgentTimeout = errors.New("cron: the agent turn reached its time limit")

// CronTurn is the input of the agent turn of one cron run.
type CronTurn struct {
	// Session is the new session of the run (kind "cron"). Its ModelOverride holds the
	// model pin of the job.
	Session *sessions.Session
	// Text is the user message: the skills, the script output, the prompt, and CronPreamble.
	Text string
	// Toolsets narrows the cron toolsets of the config. Empty keeps them all.
	Toolsets []string
	// RemoveTools holds the tool names that the run cannot see.
	RemoveTools []string
	// Model is the model pin of the job. Session.ModelOverride already applies it.
	Model *ModelPin
	// Workdir is the working folder of the tools. Empty uses the default folder.
	Workdir string
	// JobName is the name of the job.
	JobName string
	// JobPurpose is the raw prompt of the job, not the built text.
	JobPurpose string
	// DraftOnly is true when the run cannot send.
	DraftOnly bool
	// Outside holds the outside sources from before the turn.
	Outside []string
}

// TurnOutput is the result of the agent turn of a cron run. Final is the answer text, also
// the partial text after an error. Sources, Hosts, and ToolCounts are the outside facts of the
// turn for model_guard. Handles holds the values of the handles of the run for the scan of
// the delivery. The values stay in memory.
type TurnOutput struct {
	Final          string
	Sources, Hosts []string
	ToolCounts     map[string]int
	Handles        []string
}

// Runner runs one job. cron does not import agent or tools, so internal/app gives the
// agent turn and the skill loader as function fields.
type Runner struct {
	// Store receives the session of each agent run.
	Store *sessions.Store
	// Jobs is the cron store. The watch limit, the continuity block, and the retry read it.
	// Nil turns off the watch limit.
	Jobs *Store
	// ScriptsDir is the folder of the job scripts. A relative script path resolves in it.
	ScriptsDir string
	// Turn runs the agent turn and returns its output. The output is valid also when err is not nil.
	Turn func(ctx context.Context, t CronTurn) (TurnOutput, error)
	// AgentTimeout is the total time limit of the agent turn of a job. The skills and the
	// data script run before the turn and do not count. 0 means no limit.
	AgentTimeout time.Duration
	// Skills returns the installed skills. Nil means that no skill is installed.
	Skills func() ([]skills.Skill, []skills.Warning, error)
	// SkillRead records a read of a skill that a job prompt loads. The app sets it. Nil
	// records nothing.
	SkillRead func(ctx context.Context, name, sessionID string)
	// Env is the environment of the scripts. Nil gives the process environment without
	// secrets.
	Env []string
	// SpillDir receives the full stdout of a script above the output limit of Script. Empty uses
	// the temporary folder of the system.
	SpillDir string
	// Script holds the limits of each script run: the script of a script job and the data
	// script of an agent job. The zero value uses the defaults.
	Script ScriptLimits
	// Log gets the run errors. Nil means slog.Default().
	Log *slog.Logger
	// Sleep waits d before the retry of an agent run. It returns an error when ctx ends first.
	// Nil uses a timer that stops when ctx ends.
	Sleep func(ctx context.Context, d time.Duration) error
	// Cost returns the cost of the sessions of a run. Nil leaves RunOutcome.CostUSD nil.
	Cost func(ctx context.Context, sessionIDs []string) (*float64, error)
}

const (
	retryWait    = 60 * time.Second // the wait before the second start
	retryMinLeft = 2 * time.Minute  // the AgentTimeout time that a retry needs
	retryLine    = "An earlier start of this run failed after it used tools. Check what it did before you act again."
)

// errKey returns the key of a failed run for the quiet repeat. It has no provider body and
// no digits, so the same failure gives the same key.
func errKey(err error, alert string) string {
	var se *provider.HTTPStatusError
	switch {
	case errors.Is(err, provider.ErrConnection):
		return "connection"
	case errors.As(err, &se):
		return fmt.Sprintf("http %d", se.Code)
	}
	return textKey(alert)
}

// textKey returns alert cut at partialAnswerMark, with the ASCII digits removed.
func textKey(alert string) string {
	if i := strings.Index(alert, partialAnswerMark); i >= 0 {
		alert = alert[:i]
	}
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return -1
		}
		return r
	}, alert)
}

// scriptErrKey returns "script timeout" or "script exit <N>" for a failed script.
func scriptErrKey(res ScriptResult) string {
	if res.TimedOut {
		return "script timeout"
	}
	return fmt.Sprintf("script exit %d", res.ExitCode)
}

// sleep waits d, or until ctx ends.
func (r *Runner) sleep(ctx context.Context, d time.Duration) error {
	if r.Sleep != nil {
		return r.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run runs the job j once and returns the outcome. A script job (NoAgent) makes no model
// call and creates no session. An agent job creates a fresh session. Its answer is not sent
// when it is a silence marker (IsSilent). A failure gives Status "error" and an alert text
// that is always sent. manual is true for Run now. A manual watch run always wakes the model.
func (r *Runner) Run(ctx context.Context, j Job, manual bool) RunOutcome {
	if j.NoAgent {
		return r.runScriptJob(ctx, j)
	}
	o, err := r.runAgentJob(ctx, j, manual)
	if err != nil {
		r.log().Error("cron job failed", "job", j.ID, "name", j.Name, "err", err)
		o.Text, o.Send, o.Status = fmt.Sprintf("⚠ Cron job %s failed: %v", j.Name, err), true, "error"
		o.ErrKey = errKey(err, o.Text)
	}
	if r.Cost != nil && len(o.SessionIDs) > 0 {
		c, cerr := r.Cost(context.WithoutCancel(ctx), o.SessionIDs)
		if cerr != nil {
			r.log().Warn("cron run cost failed", "job", j.ID, "name", j.Name, "err", cerr)
		} else {
			o.CostUSD = c
		}
	}
	return o
}

func (r *Runner) runScriptJob(ctx context.Context, j Job) RunOutcome {
	path, err := ResolveScript(j.Script, r.ScriptsDir)
	if err != nil {
		text := fmt.Sprintf("⚠ Cron job %s failed: %v", j.Name, err)
		return RunOutcome{Text: text, Send: true, Status: "error", ErrKey: textKey(text)}
	}
	res := RunScriptSpill(ctx, path, j.Workdir, r.Env, r.Script, r.SpillDir)
	text, send := ScriptJobMessage(j, res, r.Script)
	status, key := "ok", ""
	if res.TimedOut || res.ExitCode != 0 {
		status, key = "error", scriptErrKey(res)
	}
	return RunOutcome{Text: text, Send: send, Status: status, ErrKey: key}
}

// watchCapText is the run output of a watch run that the limit of model runs skipped.
const watchCapText = "Skipped: this watch job reached 24 model runs in 24 h."

// scriptRun is the result of the data script of an agent job. err is the ResolveScript error.
type scriptRun struct {
	res ScriptResult
	err error
}

// failed reports whether the script did not run, timed out, or exited with a code other than 0.
func (s scriptRun) failed() bool {
	return s.err != nil || s.res.TimedOut || s.res.ExitCode != 0
}

func (r *Runner) runAgentJob(ctx context.Context, j Job, manual bool) (RunOutcome, error) {
	if r.Turn == nil {
		return RunOutcome{}, errors.New("cron: no agent turn function is set")
	}
	if r.Store == nil {
		return RunOutcome{}, errors.New("cron: no session store is set")
	}
	var sr *scriptRun
	if j.Script != "" {
		sr = &scriptRun{}
		path, err := ResolveScript(j.Script, r.ScriptsDir)
		if err != nil {
			sr.err = err
		} else {
			sr.res = RunScriptSpill(ctx, path, j.Workdir, r.Env, r.Script, r.SpillDir)
		}
	}
	o := RunOutcome{Manual: manual}
	if j.Watch && sr != nil {
		if sr.failed() {
			o.Text, o.Send, o.Status = r.scriptAlert(j, *sr), true, "error"
			if sr.err != nil {
				o.ErrKey = textKey(o.Text)
			} else {
				o.ErrKey = scriptErrKey(sr.res)
			}
			return o, nil
		}
		o.WatchHash, o.WatchOutput = sr.res.RawSHA256, watchText(sr.res.Stdout)
		wake, capped, err := r.watchWakes(ctx, j, sr.res, manual)
		if err != nil {
			return o, err
		}
		if !wake {
			o.Skipped, o.Status = true, "ok"
			if capped {
				o.Text, o.WatchHash, o.WatchOutput = watchCapText, "", ""
			}
			return o, nil
		}
	}
	sess, err := r.Store.Create(ctx, sessions.Session{
		Key:           "cron:" + j.ID,
		Source:        "cron",
		Kind:          "cron",
		Title:         j.Name,
		ModelOverride: modelOverride(j.Model),
		Effort:        j.Effort,
	})
	if err != nil {
		return o, err
	}
	o.SessionID, o.SessionIDs = sess.ID, []string{sess.ID}
	text, scriptFailed, err := r.buildText(ctx, j, sr, sess.ID)
	if err != nil {
		return o, err
	}
	tctx := ctx
	if r.AgentTimeout > 0 {
		var cancel context.CancelFunc
		tctx, cancel = context.WithTimeoutCause(ctx, r.AgentTimeout, errAgentTimeout)
		defer cancel()
	}
	removed := append([]string(nil), cronRemovedTools...)
	if j.DraftOnly {
		removed = append(removed, draftOnlyRemovedTools...)
	}
	turn := func(sess *sessions.Session, text string) (TurnOutput, error) {
		return r.Turn(tctx, CronTurn{
			Session:     sess,
			Text:        text,
			Toolsets:    j.Toolsets,
			RemoveTools: removed,
			Model:       j.Model,
			Workdir:     j.Workdir,
			JobName:     j.Name,
			JobPurpose:  j.Prompt,
			DraftOnly:   j.DraftOnly,
		})
	}
	out, err := turn(sess, text)
	if err != nil && r.canRetry(ctx, tctx, err) {
		r.log().Warn("cron agent turn failed, retrying once", "job", j.ID, "name", j.Name,
			"session", sess.ID, "err", err)
		if retryText, ok := r.prepareRetry(tctx, j, sess.ID, text); ok {
			sess2, cerr := r.Store.Create(ctx, sessions.Session{
				Key:           "cron:" + j.ID,
				Source:        "cron",
				Kind:          "cron",
				Title:         j.Name,
				ModelOverride: modelOverride(j.Model),
				Effort:        j.Effort,
			})
			if cerr != nil {
				return o, cerr
			}
			sess = sess2
			o.SessionID, o.SessionIDs = sess.ID, append(o.SessionIDs, sess.ID)
			out, err = turn(sess, retryText)
		}
	}
	final := out.Final
	o.Sources, o.Hosts, o.ToolCounts, o.Handles = out.Sources, out.Hosts, out.ToolCounts, out.Handles
	if err != nil {
		if errors.Is(context.Cause(tctx), errAgentTimeout) {
			r.log().Warn("cron agent turn timed out", "job", j.ID, "name", j.Name,
				"limit", r.AgentTimeout, "session", sess.ID)
			o.Text = fmt.Sprintf("⚠ Cron job %s failed (timed out after %s)", j.Name, seconds(r.AgentTimeout))
			if strings.TrimSpace(final) != "" {
				o.Text += partialAnswerMark + "\n\n" + final
			}
			o.Send, o.Status, o.ErrKey = true, "error", "timeout"
			return o, nil
		}
		return o, err
	}
	if strings.TrimSpace(final) == "" {
		// An empty answer is a fault of the model or the provider, so it counts as a failure (Hermes).
		o.Text = fmt.Sprintf("⚠ Cron job %s finished, but the model gave no text.", j.Name)
		o.Send, o.Status, o.ErrKey = true, "error", "empty"
		return o, nil
	}
	// The model must report a failed data script, so that run skips the silence check.
	if !scriptFailed && IsSilent(final) {
		r.log().Info("cron job answered [SILENT]; no delivery", "job", j.ID, "name", j.Name)
		o.Text, o.Send, o.Status, o.Silent = final, false, "ok", true
		return o, nil
	}
	o.Text, o.Send, o.Status = final, true, "ok"
	return o, nil
}

// canRetry reports whether a failed first start gets a second start. These conditions must
// all be true:
//   - The gateway did not stop.
//   - The time limit did not end the turn.
//   - The error class is Retry or Timeout (provider.Retryable).
//   - Enough time is left.
//
// Thus a used-up quota gets no second start.
func (r *Runner) canRetry(ctx, tctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(context.Cause(tctx), errAgentTimeout) || !provider.Retryable(err) {
		return false
	}
	if dl, ok := tctx.Deadline(); ok && time.Until(dl) < retryMinLeft {
		return false
	}
	return true
}

// prepareRetry waits, checks that the job still runs, and returns the text of the second
// start. The text has the retry line when the first session used a tool, or when its read
// fails. ok is false when the retry must not start.
func (r *Runner) prepareRetry(tctx context.Context, j Job, firstID, text string) (string, bool) {
	if err := r.sleep(tctx, retryWait); err != nil {
		return "", false
	}
	if r.Jobs != nil {
		cur, err := r.Jobs.Get(tctx, j.ID)
		if err != nil || !cur.Enabled {
			return "", false
		}
	}
	msgs, err := r.Store.Messages(tctx, firstID)
	if err != nil {
		r.log().Warn("cron retry: read the first session failed", "job", j.ID, "session", firstID, "err", err)
		// The tool calls of the first start are not known, so warn the model as if it used tools.
		return retryLine + "\n\n" + text, true
	}
	for _, m := range msgs {
		if m.Msg.Role == provider.RoleTool {
			return retryLine + "\n\n" + text, true
		}
	}
	return text, true
}

// buildText returns the user message of an agent job: the skills in order, the continuity
// block, the script output, the prompt, and CronPreamble. sr is nil for a job with no script. scriptFailed is true
// when the data script failed.
func (r *Runner) buildText(ctx context.Context, j Job, sr *scriptRun, sessionID string) (text string, scriptFailed bool, err error) {
	var b strings.Builder
	if err := r.writeSkills(ctx, &b, j, sessionID); err != nil {
		return "", false, err
	}
	if err := r.writeContinuity(ctx, &b, j); err != nil {
		return "", false, err
	}
	if sr != nil {
		if j.Watch {
			r.writeWatch(&b, j, sr.res)
		} else {
			scriptFailed = r.writeScript(&b, j, *sr)
		}
	}
	b.WriteString(j.Prompt)
	b.WriteString("\n\n")
	b.WriteString(CronPreamble)
	if j.DraftOnly {
		b.WriteString("\n\n")
		b.WriteString(draftOnlyPreamble)
	}
	return b.String(), scriptFailed, nil
}

// writeContinuity writes the output of the last delivered run of a job with continuity, as
// untrusted data (Hermes continuity, cron/job_args.py). With no such run, or with a nil Jobs,
// it writes nothing.
func (r *Runner) writeContinuity(ctx context.Context, b *strings.Builder, j Job) error {
	if !j.Continuity || r.Jobs == nil {
		return nil
	}
	run, ok, err := r.Jobs.LastDelivered(ctx, j.ID)
	if err != nil {
		return fmt.Errorf("cron: read the previous run: %w", err)
	}
	if !ok {
		return nil
	}
	fmt.Fprintf(b, "Your previous run's output (%s)\n%s\n\n",
		run.StartedAt.In(time.Local).Format("2006-01-02 15:04"),
		security.WrapUntrusted(security.UntrustedCronSource, run.Output))
	return nil
}

// scriptAlert returns the alert text of a data script that failed.
func (r *Runner) scriptAlert(j Job, sr scriptRun) string {
	if sr.err != nil {
		return fmt.Sprintf("⚠ Cron job %s failed: %v", j.Name, sr.err)
	}
	alert, _ := ScriptJobMessage(j, sr.res, r.Script)
	return alert
}

// watchWakes applies the decision table of a watch job whose script exited with 0. capped is
// true when the limit of model runs in 24 h skipped the run.
func (r *Runner) watchWakes(ctx context.Context, j Job, res ScriptResult, manual bool) (wake, capped bool, err error) {
	switch g, ok := ParseWakeGate(res.LastLine); {
	case manual:
		return true, false, nil
	case ok:
		wake = g
	case j.WatchHash == "":
		wake = false
	default:
		wake = res.RawSHA256 != j.WatchHash
	}
	if !wake || r.Jobs == nil {
		return wake, false, nil
	}
	n, err := r.Jobs.ModelRunsSince(ctx, j.ID, time.Now().Add(-24*time.Hour))
	if err != nil {
		return false, false, fmt.Errorf("cron: count the watch runs: %w", err)
	}
	if n >= WatchModelRunsPerDay {
		return false, true, nil
	}
	return true, false, nil
}

// watchText returns the stdout of a watch script with no cut marker, cut at
// WatchOutputMaxRunes runes with no mark. It cuts at the first "\n[… " (the marker of
// ctxmgr.TruncateNamed) or "\n[stdout was cut", so the stored text never names a spill file.
func watchText(stdout string) string {
	for _, m := range []string{"\n[… ", "\n[stdout was cut"} {
		if i := strings.Index(stdout, m); i >= 0 {
			stdout = stdout[:i]
		}
	}
	if r := []rune(stdout); len(r) > WatchOutputMaxRunes {
		stdout = string(r[:WatchOutputMaxRunes])
	}
	return stdout
}

// writeWatch writes the Watch change block of a woken watch job. It replaces the Script output
// block, so the model does not get the same text two times.
func (r *Runner) writeWatch(b *strings.Builder, j Job, res ScriptResult) {
	wrap := func(t string) string { return security.WrapUntrusted(security.UntrustedCronSource, t) }
	text := watchText(res.Stdout)
	newText := strings.TrimRight(text, "\n")
	if strings.TrimSpace(newText) == "" {
		newText = "(the script printed nothing)"
	}
	if j.WatchHash != "" {
		const head = "Watch change: the script output changed since the last run.\n"
		switch d := WatchDiff(j.WatchOutput, text); {
		case d == "" && res.RawSHA256 != j.WatchHash:
			// The hash covers the full stdout, so the change is in the part that the cut removed.
			b.WriteString(head)
			b.WriteString(watchCutChange + "\n\n")
		case d == "":
		case d == watchTooLarge:
			b.WriteString(head)
			b.WriteString(watchTooLarge + "\n\n")
		default:
			b.WriteString(head)
			b.WriteString("Diff:\n")
			b.WriteString(wrap(strings.TrimRight(d, "\n")))
			b.WriteString("\n\n")
		}
	}
	b.WriteString("New output:\n")
	b.WriteString(wrap(newText))
	b.WriteString("\n\n")
}

// writeSkills writes each skill of the job as "[Skill: <name>]\n<body>\n". A skill that is
// not installed is skipped with a notice to the model, so the user learns about it (Hermes).
func (r *Runner) writeSkills(ctx context.Context, b *strings.Builder, j Job, sessionID string) error {
	if len(j.Skills) == 0 {
		return nil
	}
	byName := map[string]skills.Skill{}
	if r.Skills != nil {
		all, _, err := r.Skills()
		if err != nil {
			return fmt.Errorf("cron: load skills: %w", err)
		}
		for _, s := range all {
			byName[s.Name] = s
		}
	}
	var missing []string
	var body strings.Builder
	for _, name := range j.Skills {
		s, ok := byName[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		fmt.Fprintf(&body, "[Skill: %s]\n%s\n", name, strings.TrimSpace(s.Body))
		if r.SkillRead != nil {
			r.SkillRead(ctx, name, sessionID)
		}
	}
	if len(missing) > 0 {
		list := strings.Join(missing, ", ")
		r.log().Warn("cron job skills not found", "job", j.ID, "skills", list)
		fmt.Fprintf(b, "[IMPORTANT: The following skill(s) were listed for this job but could not be found "+
			"and were skipped: %s. Start your response with a brief notice so the user is aware, e.g.: "+
			"'⚠️ Skill(s) not found and skipped: %s']\n\n", list, list)
	}
	if body.Len() > 0 {
		b.WriteString(body.String())
		b.WriteString("\n")
	}
	return nil
}

// writeScript writes the stdout of the data script as untrusted data inside the untrusted block. A failure
// goes into the prompt too, so that the model reports it (Hermes). It returns true when it
// wrote a "Script error:" block.
func (r *Runner) writeScript(b *strings.Builder, j Job, sr scriptRun) (failed bool) {
	if sr.err != nil {
		fmt.Fprintf(b, "Script error: the data-collection script did not run. Report this to the user.\n%v\n\n", sr.err)
		return true
	}
	res := sr.res
	if res.TimedOut || res.ExitCode != 0 {
		alert, _ := ScriptJobMessage(j, res, r.Script)
		fmt.Fprintf(b, "Script error: the data-collection script failed. Report this to the user.\n%s\n\n",
			security.WrapUntrusted(security.UntrustedCronSource, alert))
		return true
	}
	out := strings.TrimRight(res.Stdout, "\n")
	if strings.TrimSpace(out) == "" {
		out = "(the script printed nothing)"
	}
	fmt.Fprintf(b, "Script output:\n%s\n\n", security.WrapUntrusted(security.UntrustedCronSource, out))
	return false
}

// modelOverride returns the session model override for a pin: "<provider>[:<model>]".
// A pin with no provider uses the default entry "primary".
func modelOverride(p *ModelPin) string {
	if p == nil || *p == (ModelPin{}) {
		return ""
	}
	name := p.Provider
	if name == "" {
		name = "primary"
	}
	if p.Model == "" {
		return name
	}
	return name + ":" + p.Model
}

func (r *Runner) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}
