package cron

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// ErrNoTarget is the error for a job that has no delivery target.
var ErrNoTarget = errors.New("no delivery target: run /sethome in a Telegram chat, or set deliver")

// Target is a chat and an optional topic. internal/app maps it to gateway.Target, so that
// cron does not import gateway.
type Target struct {
	Source, ChatID, ThreadID string
}

// RunOutcome is the result of one job run. Send false means that the run delivers nothing.
type RunOutcome struct {
	Text      string
	Send      bool
	Status    string
	SessionID string
}

// Deliverer sends the outcome of a job run. internal/app sets the function fields, so cron
// does not import gateway or telegram.
type Deliverer struct {
	// Send sends text and files to a target.
	Send func(ctx context.Context, to Target, text string, files []string) error
	// Home returns the Telegram home channel. With forCron, the thread is cron_thread when
	// it is set. The bool is false when no home channel is set.
	Home func(ctx context.Context, forCron bool) (Target, bool)
	// Redact removes secrets from the text before it goes out. Nil sends the text as it is.
	Redact func(string) string
	// Log gets the delivery failures. Nil means slog.Default().
	Log *slog.Logger
}

// resolveTarget returns the target for the job j. An empty deliver value or
// "origin" sends to the Telegram chat and topic of j.Origin. A job from another origin, for
// example the CLI, goes to the home channel. "local" gives local true, and the run saves only.
// "telegram:<chat>[:<thread>]" is an explicit target.
func resolveTarget(ctx context.Context, j Job, home func(context.Context, bool) (Target, bool)) (Target, bool, error) {
	switch d := strings.TrimSpace(j.Deliver); d {
	case "local":
		return Target{}, true, nil
	case "", "origin":
		if j.Origin.Source == "telegram" && j.Origin.ChatID != "" {
			return Target{Source: "telegram", ChatID: j.Origin.ChatID, ThreadID: j.Origin.ThreadID}, false, nil
		}
		if home != nil {
			if t, ok := home(ctx, true); ok && t.ChatID != "" {
				if t.Source == "" {
					t.Source = "telegram"
				}
				return t, false, nil
			}
		}
		return Target{}, false, ErrNoTarget
	default:
		if err := validateDeliver(d); err != nil {
			return Target{}, false, err
		}
		rest := strings.TrimPrefix(d, "telegram:")
		chat, thread, _ := strings.Cut(rest, ":")
		return Target{Source: "telegram", ChatID: chat, ThreadID: thread}, false, nil
	}
}

// Deliver sends o.Text for the job j. It does nothing when o.Send is false or the job is
// local. A failure is logged and returned, so the caller can save last_status
// 'delivery_failed'. Deliver never panics.
func (d *Deliverer) Deliver(ctx context.Context, j Job, o RunOutcome) (err error) {
	if !o.Send {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("cron: deliver job %s: send failed: %v", j.ID, r)
		}
		if err != nil {
			d.log().Error("cron delivery failed", "job", j.ID, "name", j.Name, "err", err)
		}
	}()
	t, local, err := resolveTarget(ctx, j, d.Home)
	if err != nil {
		return fmt.Errorf("cron: deliver job %s: %w", j.ID, err)
	}
	if local {
		return nil
	}
	if d.Send == nil {
		return fmt.Errorf("cron: deliver job %s: no send function is set", j.ID)
	}
	text := o.Text
	if d.Redact != nil {
		text = d.Redact(text)
	}
	if err := d.Send(ctx, t, text, nil); err != nil {
		return fmt.Errorf("cron: deliver job %s to %s:%s: %w", j.ID, t.Source, t.ChatID, err)
	}
	return nil
}

func (d *Deliverer) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}
