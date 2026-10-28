package gateway

import (
	"context"
	"log/slog"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
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
	Log       *slog.Logger
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
}

// Turn runs one agent turn for sess with the toolsets of the channel. After a turn with
// an answer, it sets the session title from the user text when the title is empty.
func (d *Deps) Turn(ctx context.Context, sess *sessions.Session, in TurnInput) (agent.TurnResult, error) {
	log := d.logger()
	t := agent.Turn{
		Session:  sess,
		UserText: in.Text,
		Images:   in.Images,
		Toolsets: d.Toolsets(in.Channel),
		Sink:     in.Sink,
		Steer:    in.Steer,
		Notify:   in.Notify,
	}
	if d.NewEnv != nil {
		t.Env = d.NewEnv(sess, in.Channel)
	}
	// The progress lines of the delegate_task children go through the CLI sink. Its lock
	// keeps each line apart from the streamed answer text. A channel with its own
	// progress function, for example Telegram, keeps that function.
	if cs, ok := in.Sink.(*CLISink); ok && t.Env != nil && t.Env.Progress == nil {
		t.Env.Progress = cs.Notice
	}
	log.Info("turn start", "session", sess.ID, "channel", in.Channel)
	start := time.Now()
	res, err := d.Agent.RunTurn(ctx, t)
	attrs := []any{"session", sess.ID, "channel", in.Channel, "tool_rounds", res.ToolRounds,
		"input_tokens", res.Usage.InputTokens + res.Usage.CacheReadTokens, "output_tokens", res.Usage.OutputTokens,
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
