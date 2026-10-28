package cron

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/skills"
)

// CronPreamble tells the model how a cron run works. It is the Hermes cron hint
// (cron/scheduler.py) without the [SILENT] rule, because an agent job always delivers.
const CronPreamble = "[IMPORTANT: You are running as a scheduled cron job. No user is present, " +
	"so you cannot ask the user questions or get approval for dangerous commands. " +
	"DELIVERY: Your final response will be automatically delivered to the user — do NOT " +
	"try to deliver the output yourself. Just produce your report/output as your final " +
	"response and the system handles the rest.]"

// cronRemovedTools are the tools that a cron run cannot use: no user is present,
// and a run cannot create cron jobs.
var cronRemovedTools = []string{"cronjob", "clarify"}

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
}

// Runner runs one job. cron does not import agent or tools, so internal/app gives the
// agent turn and the skill loader as function fields.
type Runner struct {
	// Store receives the session of each agent run.
	Store *sessions.Store
	// ScriptsDir is the folder of the job scripts. A relative script path resolves in it.
	ScriptsDir string
	// Turn runs the agent turn and returns the final text.
	Turn func(ctx context.Context, t CronTurn) (final string, err error)
	// Skills returns the installed skills. Nil means that no skill is installed.
	Skills func() ([]skills.Skill, []skills.Warning, error)
	// Env is the environment of the scripts. Nil gives the process environment without
	// secrets.
	Env []string
	// SpillDir receives the full stdout of a script above ScriptOutputMaxChars. Empty uses
	// the temporary folder of the system.
	SpillDir string
	// Log gets the run errors. Nil means slog.Default().
	Log *slog.Logger
}

// Run runs the job j once and returns the outcome. A script job (NoAgent) makes no model
// call and creates no session. An agent job creates a fresh session and always delivers.
// A failure gives Status "error" and an alert text that is always sent.
func (r *Runner) Run(ctx context.Context, j Job) RunOutcome {
	if j.NoAgent {
		return r.runScriptJob(ctx, j)
	}
	o, err := r.runAgentJob(ctx, j)
	if err != nil {
		r.log().Error("cron job failed", "job", j.ID, "name", j.Name, "err", err)
		o.Text, o.Send, o.Status = fmt.Sprintf("⚠ Cron job %s failed: %v", j.Name, err), true, "error"
	}
	return o
}

func (r *Runner) runScriptJob(ctx context.Context, j Job) RunOutcome {
	path, err := ResolveScript(j.Script, r.ScriptsDir)
	if err != nil {
		return RunOutcome{Text: fmt.Sprintf("⚠ Cron job %s failed: %v", j.Name, err), Send: true, Status: "error"}
	}
	res := RunScriptSpill(ctx, path, j.Workdir, r.Env, 0, r.SpillDir)
	text, send := ScriptJobMessage(j, res)
	status := "ok"
	if res.TimedOut || res.ExitCode != 0 {
		status = "error"
	}
	return RunOutcome{Text: text, Send: send, Status: status}
}

func (r *Runner) runAgentJob(ctx context.Context, j Job) (RunOutcome, error) {
	if r.Turn == nil {
		return RunOutcome{}, errors.New("cron: no agent turn function is set")
	}
	if r.Store == nil {
		return RunOutcome{}, errors.New("cron: no session store is set")
	}
	sess, err := r.Store.Create(ctx, sessions.Session{
		Key:           "cron:" + j.ID,
		Source:        "cron",
		Kind:          "cron",
		Title:         j.Name,
		ModelOverride: modelOverride(j.Model),
	})
	if err != nil {
		return RunOutcome{}, err
	}
	o := RunOutcome{SessionID: sess.ID}
	text, err := r.buildText(ctx, j)
	if err != nil {
		return o, err
	}
	final, err := r.Turn(ctx, CronTurn{
		Session:     sess,
		Text:        text,
		Toolsets:    j.Toolsets,
		RemoveTools: append([]string(nil), cronRemovedTools...),
		Model:       j.Model,
		Workdir:     j.Workdir,
	})
	if err != nil {
		return o, err
	}
	if strings.TrimSpace(final) == "" {
		final = fmt.Sprintf("⚠ Cron job %s finished, but the model gave no text.", j.Name)
	}
	o.Text, o.Send, o.Status = final, true, "ok"
	return o, nil
}

// buildText returns the user message of an agent job: the skills in order, the script
// output, the prompt, and CronPreamble.
func (r *Runner) buildText(ctx context.Context, j Job) (string, error) {
	var b strings.Builder
	if err := r.writeSkills(&b, j); err != nil {
		return "", err
	}
	if j.Script != "" {
		r.writeScript(ctx, &b, j)
	}
	b.WriteString(j.Prompt)
	b.WriteString("\n\n")
	b.WriteString(CronPreamble)
	return b.String(), nil
}

// writeSkills writes each skill of the job as "[Skill: <name>]\n<body>\n". A skill that is
// not installed is skipped with a notice to the model, so the user learns about it (Hermes).
func (r *Runner) writeSkills(b *strings.Builder, j Job) error {
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

// writeScript runs the script of an agent job and writes its stdout as data. A failure
// goes into the prompt too, so that the model reports it (Hermes).
func (r *Runner) writeScript(ctx context.Context, b *strings.Builder, j Job) {
	path, err := ResolveScript(j.Script, r.ScriptsDir)
	if err != nil {
		fmt.Fprintf(b, "Script error: the data-collection script did not run. Report this to the user.\n%v\n\n", err)
		return
	}
	res := RunScriptSpill(ctx, path, j.Workdir, r.Env, 0, r.SpillDir)
	if res.TimedOut || res.ExitCode != 0 {
		alert, _ := ScriptJobMessage(j, res)
		fmt.Fprintf(b, "Script error: the data-collection script failed. Report this to the user.\n%s\n\n", alert)
		return
	}
	out := strings.TrimRight(res.Stdout, "\n")
	if strings.TrimSpace(out) == "" {
		out = "(the script printed nothing)"
	}
	fmt.Fprintf(b, "Script output:\n%s\n\n", out)
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
