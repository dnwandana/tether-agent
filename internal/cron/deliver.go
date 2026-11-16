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

// The delivery values of a run. The scheduler writes one in each cron_runs row.
const (
	DeliverySent   = "sent"   // Send was true, and the send succeeded.
	DeliverySilent = "silent" // The agent answered with a silence marker.
	DeliveryNone   = "none"   // Send was false for another reason, or the scheduler has no Deliverer.
	DeliveryLocal  = "local"  // The job saves only. Also a pause alert of a local job with no home channel.
	DeliveryFailed = "failed" // The send returned an error.
)

// RunOutcome is the result of one job run. Deliver sends Text only when Send is true and
// Silent is false.
type RunOutcome struct {
	Text      string
	Send      bool
	Status    string // "ok" | "error" (Runner); "stopped" (scheduler)
	SessionID string
	// Silent is true when the agent answered with a silence marker. Send is then false.
	Silent bool
	// Pause is true when this run pauses the job. Deliver then also sends for a local job.
	Pause bool
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

// Deliver sends o.Text for the job j and returns the delivery value. A silent outcome gives
// DeliverySilent, and an outcome with Send false gives DeliveryNone. A local job gives
// DeliveryLocal and sends nothing, except a pause alert: that goes to the home channel. A
// failure is logged and returned with DeliveryFailed, so the caller can save last_status
// 'delivery_failed'. Deliver never panics.
func (d *Deliverer) Deliver(ctx context.Context, j Job, o RunOutcome) (delivery string, err error) {
	if o.Silent {
		return DeliverySilent, nil
	}
	if !o.Send {
		return DeliveryNone, nil
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("cron: deliver job %s: send failed: %v", j.ID, r)
		}
		if err != nil {
			delivery = DeliveryFailed
			d.log().Error("cron delivery failed", "job", j.ID, "name", j.Name, "err", err)
		}
	}()
	t, local, err := resolveTarget(ctx, j, d.Home)
	if err != nil {
		return DeliveryFailed, fmt.Errorf("cron: deliver job %s: %w", j.ID, err)
	}
	if local {
		// resolveTarget returns local before it calls Home, so the pause alert asks Home here.
		if !o.Pause || d.Home == nil || d.Send == nil {
			return DeliveryLocal, nil
		}
		h, ok := d.Home(ctx, true)
		if !ok || h.ChatID == "" {
			return DeliveryLocal, nil
		}
		if h.Source == "" {
			h.Source = "telegram"
		}
		t = h
	}
	if d.Send == nil {
		return DeliveryFailed, fmt.Errorf("cron: deliver job %s: no send function is set", j.ID)
	}
	text := o.Text
	if d.Redact != nil {
		text = d.Redact(text)
	}
	if err := d.Send(ctx, t, text, nil); err != nil {
		return DeliveryFailed, fmt.Errorf("cron: deliver job %s to %s:%s: %w", j.ID, t.Source, t.ChatID, err)
	}
	return DeliverySent, nil
}

func (d *Deliverer) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}
