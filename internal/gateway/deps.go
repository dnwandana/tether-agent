package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/ctxmgr"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/skills"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// Deps holds the shared parts that the channels and the commands use. The app package
// builds it, because gateway must not import app.
type Deps struct {
	Cfg   *config.Config
	Store *sessions.Store
	Agent *agent.Agent
	// Providers holds every provider entry by name. /model accepts only these names.
	Providers map[string]provider.Provider
	// Models keeps the model lists of the provider entries for /model. A nil cache
	// calls the provider each time.
	Models *provider.ModelCache
	Log    *slog.Logger
	// NewEnv returns the tool context of a session on a channel. Its Values map must be the
	// same for a session across turns. Nil gives a context with the session ids only.
	NewEnv func(sess *sessions.Session, channel string) *tools.Env
	// ToolLine returns the progress line of a tool call. Nil uses defaultToolLine.
	ToolLine func(tool, preview string) string
	// BeforeNew runs before /new replaces the session of a key. It can be nil.
	BeforeNew func(ctx context.Context, old *sessions.Session)
	// AfterDelete runs after /delete removes a session, for example to free the state of
	// the session in memory. It can be nil.
	AfterDelete func(sessionID string)
	// CancelKey cancels the background work of a key, for example the background
	// delegate_task children. /stop, /new, and /reset call it. It can be nil.
	CancelKey func(key string)
	// CLIAsk is the approval prompt and the clarify prompt of the CLI. CLI.Run makes it
	// read through the REPL input. It can be nil.
	CLIAsk *CLIAsk
	// Goals runs the /goal loop. Nil turns the feature off, so a gateway with no goals works
	// as before.
	Goals *goals.Manager
	// SendNotice sends a goal notice that has no turn, for example after a restart or a
	// wake, with the buttons of r for the user of sess. Nil sends the text only.
	SendNotice func(ctx context.Context, key string, sess *sessions.Session, r Reply) error
	// Skills loads the skills for the skill commands. The app sets it to the Load method of
	// the skill catalog. Nil turns the skill commands off. It reads the skill folders, so
	// the gateway does not call it in accept, under Gateway.mu, or at startup.
	Skills func() ([]skills.Skill, []skills.Warning, error)
}

// TurnInput is the input of one agent turn that a channel starts.
type TurnInput struct {
	Text    string
	Images  []provider.Image
	Channel string
	Sink    agent.Sink
	Steer   *agent.SteerQueue
	// Notify receives the notices of the background reviews of the turn. Wrap the send
	// function with agent.HoldNotices, so that a notice waits for the answer. It can be nil.
	Notify func(text string)
	// OnSteerRead runs after the agent reads the steer texts up to seq. It can be nil.
	OnSteerRead func(seq uint64)
	// Slash is true when the owner typed Text as a slash command that is not a built-in.
	// Turn then runs the skill of the command, when the channel has one. A goal text, a
	// steer, or a system text has Slash false, also when it starts with "/".
	Slash bool
}

// Turn runs one agent turn for sess with the toolsets of the channel. After a turn with
// an answer, it sets the session title from the user text when the title is empty.
func (d *Deps) Turn(ctx context.Context, sess *sessions.Session, in TurnInput) (agent.TurnResult, error) {
	log := d.logger()
	t := agent.Turn{
		Session:     sess,
		UserText:    in.Text,
		Images:      in.Images,
		Toolsets:    d.Toolsets(in.Channel),
		Sink:        in.Sink,
		Steer:       in.Steer,
		Notify:      in.Notify,
		OnSteerRead: in.OnSteerRead,
	}
	if d.NewEnv != nil {
		t.Env = d.NewEnv(sess, in.Channel)
	}
	t.UserText = d.userText(sess, in, t.Env)
	// The progress lines of the delegate_task children go through the CLI sink. Its lock
	// keeps each line apart from the streamed answer text. The CLI does not group the
	// lines, so it does not use the batch number. A channel with its own progress
	// function, for example Telegram, keeps that function.
	if cs, ok := in.Sink.(*CLISink); ok && t.Env != nil && t.Env.Progress == nil {
		t.Env.Progress = func(_ uint64, child, tool, preview string) {
			cs.Notice(tools.ChildProgressLine(child, tool, preview))
		}
	}
	log.Info("turn start", "session", sess.ID, "channel", in.Channel)
	start := time.Now()
	res, err := d.Agent.RunTurn(ctx, t)
	attrs := []any{"session", sess.ID, "channel", in.Channel, "tool_rounds", res.ToolRounds,
		"input_tokens", res.Usage.InputTokens + res.Usage.CacheReadTokens,
		"cache_read_tokens", res.Usage.CacheReadTokens, "output_tokens", res.Usage.OutputTokens,
		"first_token_ms", res.Usage.FirstTokenMS, "total_ms", time.Since(start).Milliseconds()}
	if err != nil {
		attrs = append(attrs, "err", err.Error())
	}
	log.Info("turn end", attrs...)
	if err == nil && res.Final != "" && d.Store != nil {
		if terr := d.Store.SetTitleIfEmpty(context.WithoutCancel(ctx), sess.ID, in.Text); terr != nil {
			log.Warn("cannot set the session title", "session", sess.ID, "err", terr.Error())
		}
	}
	return res, err
}

// userText returns the user text of the turn. A marked slash text that names a skill of
// the channel gives the skill invocation text, and the env names of the skill go into
// env. Any other text stays as it is.
func (d *Deps) userText(sess *sessions.Session, in TurnInput, env *tools.Env) string {
	if !in.Slash {
		return in.Text
	}
	name, args, ok := IsCommand(in.Text)
	if !ok {
		return in.Text
	}
	// This is the only read of the skill folders in the turn.
	sc, ok := ResolveSkill(d.SkillCommandsFor(in.Channel), name)
	if !ok {
		return in.Text
	}
	body := skills.SubstituteTemplateVars(sc.Skill.Body, sc.Skill.Dir, sess.ID)
	body = d.cutSkillBody(body)
	if env != nil {
		tools.AddPassthrough(env, sc.Skill.RequiredEnv)
	}
	d.logger().Info("skill command", "session", sess.ID, "skill", sc.Skill.Name, "channel", in.Channel)
	return tools.SkillInvocationText(sc.Skill, body, args)
}

// cutSkillBody cuts a skill body to the limit of a tool output, like a skill_view result.
// The full body goes to a spill file. When the spill write fails, the body is cut with no
// copy.
func (d *Deps) cutSkillBody(body string) string {
	max := config.Default().Context.ToolOutputMaxChars
	if d.Cfg != nil {
		max = d.Cfg.Context.ToolOutputMaxChars
	}
	out, err := ctxmgr.Truncate(body, max, d.Agent.SpillDir)
	if err == nil {
		return out
	}
	d.logger().Warn("skill command: cannot write the spill file; the skill body is cut without a copy",
		"dir", d.Agent.SpillDir, "err", err.Error())
	return cutBodyNoSpill(ctxmgr.SanitizeUTF8(body), max)
}

// cutBodyNoSpill keeps the head (60%) and the tail (30%) of s, with a marker and no spill
// path. The marker is the one of a tool output that the agent cannot spill.
func cutBodyNoSpill(s string, max int) string {
	r := []rune(s)
	head, tail := max*6/10, max*3/10
	return string(r[:head]) +
		fmt.Sprintf("\n[… %d chars omitted. The full output could not be saved.]\n", len(r)-head-tail) +
		string(r[len(r)-tail:])
}

// Toolsets returns the allowed toolsets of the channel from the toolsets config.
func (d *Deps) Toolsets(channel string) map[string]bool {
	if d.Cfg == nil {
		def := config.Default()
		return tools.Resolve(def.Toolsets[channel], nil, nil)
	}
	return tools.Resolve(d.Cfg.Toolsets[channel], nil, nil)
}

func (d *Deps) toolLine(tool, preview string) string {
	if d != nil && d.ToolLine != nil {
		return d.ToolLine(tool, preview)
	}
	return defaultToolLine(tool, preview)
}

func (d *Deps) logger() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

// defaultToolLine returns `🔧 tool: "preview"`, or `🔧 tool...` with no preview.
func defaultToolLine(tool, preview string) string {
	if preview == "" {
		return "🔧 " + tool + "..."
	}
	return "🔧 " + tool + `: "` + preview + `"`
}
