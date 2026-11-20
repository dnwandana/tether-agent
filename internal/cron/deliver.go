package cron

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
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
	// DeliveryHeld means that model_guard held the text. The owner card waits.
	DeliveryHeld = "held"
	// DeliveryDropped means that the owner dropped the held text.
	DeliveryDropped = "dropped"
	// DeliveryExpired means that the held text waited 24 h with no tap.
	DeliveryExpired = "expired"
	// DeliveryRepeat means that the error key is the key of the last alert that went out. Nothing is sent.
	DeliveryRepeat = "repeat"
)

// partialAnswerMark joins the failure alert of a timed-out run to the partial answer of the run.
const partialAnswerMark = ". The partial answer follows."

// Note is the data for the owner note about a masked delivery. Original is the text after
// Redact, with no masks of the scan. HeldID is the held_sends row that keeps Original.
type Note struct {
	Job         Job
	Target      Target
	TargetTitle string
	Findings    []security.Finding
	Original    string
	HeldID      string
	// NoSound sends the note with no sound.
	NoSound bool
}

// TargetInfo describes a target for the guard and the owner cards. Kind is "group", "topic",
// or "private chat of a member".
type TargetInfo struct {
	Kind, Title string
}

// HeldCardInfo is the data of the owner card for a held delivery.
type HeldCardInfo struct {
	JobName, TargetTitle, TargetKind, Reason string
	Sources                                  []string
}

// RunOutcome is the result of one job run. Deliver sends Text only when Send is true and
// Silent is false. Sources, Hosts, ToolCounts, and Handles come from the agent turn and feed
// the scan and the guard of the delivery.
type RunOutcome struct {
	Text      string
	Send      bool
	Status    string // "ok" | "error" (Runner); "stopped" (scheduler)
	SessionID string
	// Silent is true when the agent answered with a silence marker. Send is then false.
	Silent bool
	// Pause is true when this run pauses the job. Deliver then also sends for a local job.
	Pause bool
	// Sources, Hosts, and ToolCounts are the outside facts of the agent turn.
	Sources, Hosts []string
	ToolCounts     map[string]int
	// Handles holds the handle values of the run for the scan of the delivery. They stay in memory.
	Handles []string
	// RunID is the cron_runs row of the run. It is 0 when the row does not exist yet.
	RunID int64
	// Skipped is true when the run did not call the model: a watch with no change, the baseline,
	// a wakeAgent false, or the watch limit. The run writes no cron_runs row.
	Skipped bool
	// Manual is true for a run that the owner started with Run now.
	Manual bool
	// ErrKey is the key of a failed run. The quiet repeat compares it with the key of the last alert.
	ErrKey string
	// SessionIDs holds each session of the run. A retry gives two. SessionID is the last one.
	SessionIDs []string
	// CostUSD is the cost of the sessions of the run. Nil means that no call has a price.
	CostUSD *float64
	// WatchHash and WatchOutput are the hash and the cut stdout of a watch script that ran with exit 0.
	WatchHash, WatchOutput string
	// Repeat is true when the alert has the same error key as the last alert, so it is not sent.
	Repeat bool
	// RecoveredLine is the "works again" line. It goes into the sent text only.
	RecoveredLine string
	// CostLine is the line about a costly run. It goes into the sent text only.
	CostLine string
	// NoSound is true in the cron.quiet_hours window. The send makes no sound.
	NoSound bool
}

// Deliverer sends the outcome of a job run. internal/app sets the function fields, so cron
// does not import gateway or telegram.
type Deliverer struct {
	// Send sends text and files to a target. With noSound, the send makes no sound (Telegram
	// disable_notification).
	Send func(ctx context.Context, to Target, text string, files []string, noSound bool) error
	// Home returns the Telegram home channel. With forCron, the thread is cron_thread when
	// it is set. The bool is false when no home channel is set.
	Home func(ctx context.Context, forCron bool) (Target, bool)
	// Redact removes secrets from the text before it goes out. Nil sends the text as it is.
	Redact func(string) string
	// Log gets the delivery failures. Nil means slog.Default().
	Log *slog.Logger
	// IsPrivate tells whether t is a private chat of the owner. Nil means that every target is
	// private, so only Redact runs.
	IsPrivate func(ctx context.Context, t Target) bool
	// Scan finds sensitive data in the raw text of a delivery to a target that is not private.
	// It returns the masked text and the findings. The handle values are in handles. Nil finds nothing.
	Scan func(text string, handles []string) (masked string, findings []security.Finding)
	// Guard checks a delivery after an outside source. Nil skips the check.
	Guard security.SendGuarder
	// Hold shows the owner a card for a held delivery. An error means that no card went out.
	Hold func(ctx context.Context, h HeldSend, card HeldCardInfo) error
	// Note tells the owner that the scan masked a delivery. Nil sends no note.
	Note func(ctx context.Context, n Note) error
	// TargetInfo gives the kind and the title of a target. Nil gives the kind "chat" and the chat id.
	TargetInfo func(ctx context.Context, t Target) TargetInfo
	// LogDecision writes the decision of the guard to the approval log. Nil writes nothing.
	LogDecision func(ctx context.Context, j Job, decision, reason string)
	// Held keeps the held texts and the originals. Nil means that a hold is a block.
	Held *HeldStore
	// OwnerHome returns the owner's private chat. The alerts of a draft-only job go there. Nil
	// means no owner chat.
	OwnerHome func(ctx context.Context) (Target, bool)
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
// 'delivery_failed'. For a target that is not private, Deliver scans the raw text before
// Redact, asks the guard after an outside source, and returns DeliveryHeld when the guard
// holds the text. Deliver never panics.
func (d *Deliverer) Deliver(ctx context.Context, j Job, o RunOutcome) (delivery string, err error) {
	if o.Repeat && !o.Pause {
		return DeliveryRepeat, nil
	}
	if o.Silent {
		if o.RecoveredLine != "" {
			d.sendLine(ctx, j, o)
		}
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
	if j.DraftOnly {
		if delivery, done, err := d.deliverDraftOnly(ctx, j, o, t, local); done {
			return delivery, err
		}
	}
	if local {
		// resolveTarget returns local before it calls Home, so the pause alert asks Home here.
		if !o.Pause || d.Home == nil || d.Send == nil {
			if o.RecoveredLine != "" {
				d.sendLine(ctx, j, o)
			}
			return DeliveryLocal, nil
		}
		h, ok := d.Home(ctx, true)
		if !ok || h.ChatID == "" {
			if o.RecoveredLine != "" {
				d.sendLine(ctx, j, o)
			}
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
	if d.IsPrivate == nil || d.IsPrivate(ctx, t) {
		return d.send(ctx, j, t, d.redact(withLines(o.Text, o.RecoveredLine, o.CostLine)), o.NoSound)
	}
	// Scan reads the raw text. Redact first would leave 10 characters of a token, and the scan would miss it.
	masked, findings := o.Text, []security.Finding(nil)
	if d.Scan != nil {
		masked, findings = d.Scan(o.Text, o.Handles)
	}
	info := d.targetInfo(ctx, t)
	if d.guarded(o) {
		allow, reason := d.Guard.Check(ctx, security.GuardInput{SessionID: o.SessionID, JobName: j.Name,
			JobPurpose: j.Prompt, Sources: o.Sources, Hosts: o.Hosts, ToolCounts: o.ToolCounts,
			Findings: security.FindingKinds(findings),
			Action:   "deliver the final text to the Telegram " + info.Kind + ` "` + info.Title + `"`})
		if !allow {
			d.logDecision(ctx, j, security.DecisionBlocked, reason)
			if o.RecoveredLine != "" {
				// The line is a fixed text, so it goes out while the result waits for the owner.
				d.sendLine(ctx, j, o)
			}
			return d.hold(ctx, j, o, t, info, reason)
		}
		d.logDecision(ctx, j, security.DecisionAllowed, reason)
	}
	delivery, err = d.send(ctx, j, t, d.redact(withLines(masked, o.RecoveredLine)), o.NoSound)
	if err == nil && len(findings) > 0 {
		d.noteOriginal(ctx, j, o, t, info, findings)
	}
	return delivery, err
}

// deliverDraftOnly applies the delivery rule of a draft-only job. done is false when the
// target is private and the normal flow goes on.
func (d *Deliverer) deliverDraftOnly(ctx context.Context, j Job, o RunOutcome, t Target, local bool) (delivery string, done bool, err error) {
	if !local && d.IsPrivate != nil && d.IsPrivate(ctx, t) {
		return "", false, nil
	}
	if o.Status == "error" || o.Pause {
		// An alert goes to the private chat of the owner, never to a group.
		if local && !o.Pause {
			return DeliveryLocal, true, nil
		}
		var home Target
		ok := false
		if d.OwnerHome != nil {
			home, ok = d.OwnerHome(ctx)
		}
		if !ok || home.ChatID == "" || d.Send == nil {
			if local {
				return DeliveryLocal, true, nil
			}
			return DeliveryFailed, true, fmt.Errorf("cron: deliver job %s: no private chat of the owner for the alert of a draft-only job", j.ID)
		}
		if home.Source == "" {
			home.Source = "telegram"
		}
		delivery, err = d.send(ctx, j, home, d.redact(o.Text), o.NoSound)
		return delivery, true, err
	}
	if local {
		if o.RecoveredLine != "" {
			d.sendLine(ctx, j, o)
		}
		return DeliveryLocal, true, nil
	}
	return DeliveryFailed, true, fmt.Errorf("cron: deliver job %s: %w", j.ID, ErrDraftDeliver)
}

// guarded tells whether the guard checks o. A failure alert and a pause alert are fixed texts.
func (d *Deliverer) guarded(o RunOutcome) bool {
	return d.Guard != nil && len(o.Sources) > 0 && !o.Pause &&
		(o.Status == "ok" || strings.Contains(o.Text, partialAnswerMark))
}

func (d *Deliverer) redact(s string) string {
	if d.Redact == nil {
		return s
	}
	return d.Redact(s)
}

func (d *Deliverer) send(ctx context.Context, j Job, t Target, text string, noSound bool) (string, error) {
	if err := d.Send(ctx, t, text, nil, noSound); err != nil {
		return DeliveryFailed, fmt.Errorf("cron: deliver job %s to %s:%s: %w", j.ID, t.Source, t.ChatID, err)
	}
	return DeliverySent, nil
}

func (d *Deliverer) targetInfo(ctx context.Context, t Target) TargetInfo {
	if d.TargetInfo == nil {
		return TargetInfo{Kind: "chat", Title: t.ChatID}
	}
	return d.TargetInfo(ctx, t)
}

func (d *Deliverer) logDecision(ctx context.Context, j Job, decision, reason string) {
	if d.LogDecision != nil {
		d.LogDecision(ctx, j, decision, reason)
	}
}

// targetKey returns "source:chat[:thread]" for t.
func targetKey(t Target) string {
	k := t.Source + ":" + t.ChatID
	if t.ThreadID != "" {
		k += ":" + t.ThreadID
	}
	return k
}

// hold keeps the text in a held_sends row and shows the owner a card. With no store, no Hold
// function, or a failed Hold, the hold is a block: the row goes away and the delivery fails.
func (d *Deliverer) hold(ctx context.Context, j Job, o RunOutcome, t Target, info TargetInfo, reason string) (string, error) {
	if d.Held == nil || d.Hold == nil {
		return DeliveryFailed, fmt.Errorf("cron: deliver job %s: model_guard held the delivery, and no owner chat can get the card", j.ID)
	}
	id, err := newHeldID()
	if err != nil {
		return DeliveryFailed, fmt.Errorf("cron: deliver job %s: model_guard held the delivery, and the owner card failed: %w", j.ID, err)
	}
	h := HeldSend{ID: id, Kind: "hold", JobID: j.ID, RunID: o.RunID, Target: targetKey(t), TargetTitle: info.Title,
		Text: d.redact(o.Text), Reason: reason, Sources: o.Sources, CreatedAt: time.Now()}
	if err := d.Held.Insert(ctx, h); err != nil {
		return DeliveryFailed, fmt.Errorf("cron: deliver job %s: model_guard held the delivery, and the owner card failed: %w", j.ID, err)
	}
	card := HeldCardInfo{JobName: j.Name, TargetTitle: info.Title, TargetKind: info.Kind, Reason: reason, Sources: o.Sources}
	if err := d.Hold(ctx, h, card); err != nil {
		if _, _, cerr := d.Held.Claim(ctx, id); cerr != nil {
			d.log().Error("cron held send cleanup failed", "job", j.ID, "id", id, "err", cerr)
		}
		return DeliveryFailed, fmt.Errorf("cron: deliver job %s: model_guard held the delivery, and the owner card failed: %w", j.ID, err)
	}
	return DeliveryHeld, nil
}

// noteOriginal tells the owner that the scan masked a delivery. An error only logs.
func (d *Deliverer) noteOriginal(ctx context.Context, j Job, o RunOutcome, t Target, info TargetInfo, findings []security.Finding) {
	if d.Note == nil {
		return
	}
	original := d.redact(o.Text)
	n := Note{Job: j, Target: t, TargetTitle: info.Title, Findings: findings, Original: original, NoSound: o.NoSound}
	if d.Held != nil {
		id, err := newHeldID()
		if err == nil {
			err = d.Held.Insert(ctx, HeldSend{ID: id, Kind: "original", JobID: j.ID, RunID: o.RunID, Target: targetKey(t),
				TargetTitle: info.Title, Text: original, Sources: o.Sources, CreatedAt: time.Now()})
		}
		if err != nil {
			d.log().Error("cron original text not kept", "job", j.ID, "err", err)
		} else {
			n.HeldID = id
		}
	}
	if err := d.Note(ctx, n); err != nil {
		d.log().Error("cron owner note failed", "job", j.ID, "err", err)
	}
}

func (d *Deliverer) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

// withLines returns text and each non-empty line, with a blank line before each line.
func withLines(text string, lines ...string) string {
	for _, l := range lines {
		if l != "" {
			text += "\n\n" + l
		}
	}
	return text
}

// sendLine sends only o.RecoveredLine. A draft-only job sends it to OwnerHome, and a local
// job to the home channel. The line is a fixed text, so it skips the scan and model_guard. A
// failure or a panic only logs, so the run keeps its delivery value.
func (d *Deliverer) sendLine(ctx context.Context, j Job, o RunOutcome) {
	defer func() {
		if r := recover(); r != nil {
			d.log().Error("cron recovered line failed", "job", j.ID, "err", fmt.Sprint(r))
		}
	}()
	var t Target
	ok := false
	switch {
	case d.Send == nil:
	case j.DraftOnly:
		if d.OwnerHome != nil {
			t, ok = d.OwnerHome(ctx)
		}
	default:
		rt, local, err := resolveTarget(ctx, j, d.Home)
		switch {
		case err != nil:
		case local:
			if d.Home != nil {
				t, ok = d.Home(ctx, true)
			}
		default:
			t, ok = rt, true
		}
	}
	if !ok || t.ChatID == "" {
		d.log().Warn("cron recovered line has no target", "job", j.ID, "name", j.Name)
		return
	}
	if t.Source == "" {
		t.Source = "telegram"
	}
	if err := d.Send(ctx, t, o.RecoveredLine, nil, o.NoSound); err != nil {
		d.log().Error("cron recovered line failed", "job", j.ID, "name", j.Name, "err", err)
	}
}
