package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/telegram"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// heldExpiry is the age at which a held row ends.
const heldExpiry = 24 * time.Hour

// errNoOwnerChat means that no private chat of an owner exists for a card or a note.
var errNoOwnerChat = errors.New("no private chat of an owner exists")

// isPrivateTarget reports whether a send to t stays with the owner. A target with no source is local.
func (a *App) isPrivateTarget(ctx context.Context, t cron.Target) bool {
	src := t.Source
	if src == "" {
		src = "local"
	}
	ch := a.ownerChannel()
	return security.IsPrivateTarget(src, t.ChatID, func(id int64) bool {
		return ch.IsOwnerPrivateChat(ctx, id)
	}, a.Cfg.Outbound.PrivateChats)
}

// ownerChannel returns the Telegram channel for the owner rules. The chat role has no channel,
// so it gets a channel with only the config and the database.
func (a *App) ownerChannel() *telegram.Channel {
	if a.Telegram != nil {
		return a.Telegram
	}
	return &telegram.Channel{Cfg: a.Cfg.Telegram, DB: a.DB, Pair: &telegram.Pairing{DB: a.DB}}
}

// deliveryScan returns the scan of a delivery to a target that is not private, or nil when
// outbound.scan is false.
func (a *App) deliveryScan() func(text string, handles []string) (string, []security.Finding) {
	if !a.Cfg.Outbound.Scan {
		return nil
	}
	return func(text string, handles []string) (string, []security.Finding) {
		found := security.Scan(text, a.Redactor, handles)
		return security.RedactFindings(text, found), found
	}
}

// channelOutbound returns the scan of the Telegram channel, or nil when outbound.scan is false.
func (a *App) channelOutbound() func(sid string, chat int64, text string) (string, []security.Finding) {
	if !a.Cfg.Outbound.Scan {
		return nil
	}
	return func(sid string, _ int64, text string) (string, []security.Finding) {
		var handles []string
		if a.Agent != nil && a.Agent.Handles != nil {
			handles = a.Agent.Handles.Values(sid)
		}
		found := security.Scan(text, a.Redactor, handles)
		return security.RedactFindings(text, found), found
	}
}

type titleEntry struct {
	chat telegram.Chat
	at   time.Time
}

// chatTitles keeps the getChat answers for TTL, so that a note or a card makes few calls.
type chatTitles struct {
	C   *telegram.Client
	TTL time.Duration
	Now func() time.Time

	mu sync.Mutex
	m  map[int64]titleEntry
}

func (c *chatTitles) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// get returns the chat from the cache or from getChat. Only a successful answer goes into the cache.
func (c *chatTitles) get(ctx context.Context, id int64) (telegram.Chat, bool) {
	c.mu.Lock()
	e, ok := c.m[id]
	c.mu.Unlock()
	if ok && c.now().Sub(e.at) < c.TTL {
		return e.chat, true
	}
	chat, err := c.C.GetChat(ctx, id)
	if err != nil {
		return telegram.Chat{}, false
	}
	c.mu.Lock()
	if c.m == nil {
		c.m = map[int64]titleEntry{}
	}
	c.m[id] = titleEntry{chat: chat, at: c.now()}
	c.mu.Unlock()
	return chat, true
}

// targetInfo returns the kind and the title of t. A failed getChat gives the chat id as the title.
func targetInfo(ctx context.Context, t cron.Target, c *chatTitles) cron.TargetInfo {
	info := cron.TargetInfo{Title: t.ChatID}
	id, err := strconv.ParseInt(t.ChatID, 10, 64)
	switch {
	case t.ThreadID != "":
		info.Kind = "topic"
	case err == nil && id > 0:
		info.Kind = "private chat of a member"
	default:
		info.Kind = "group"
	}
	if err != nil || c == nil || c.C == nil || t.Source != "telegram" {
		return info
	}
	chat, ok := c.get(ctx, id)
	if !ok {
		return info
	}
	if t.ThreadID == "" {
		switch chat.Type {
		case "private":
			info.Kind = "private chat of a member"
		case "channel":
			info.Kind = "channel"
		default:
			info.Kind = "group"
		}
	}
	for _, s := range []string{chat.Title, chat.FirstName, chat.Username} {
		if s != "" {
			info.Title = s
			break
		}
	}
	return info
}

// cronTargetInfo is targetInfo with the client of the Telegram channel and a cache of 1 h.
func (a *App) cronTargetInfo(ctx context.Context, t cron.Target) cron.TargetInfo {
	return targetInfo(ctx, t, a.titles)
}

// cronNoteText returns the owner note of a delivery that the scan masked.
func cronNoteText(n cron.Note) string {
	title := n.TargetTitle
	if title == "" {
		title = n.Target.ChatID
	}
	noun := "items"
	if len(n.Findings) == 1 {
		noun = "item"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🛡️ Job \"%s\": I masked %d %s in the delivery to \"%s\":", n.Job.Name, len(n.Findings), noun, title)
	for _, f := range n.Findings {
		b.WriteString("\n• ")
		if strings.HasPrefix(f.Label, "[") && strings.HasSuffix(f.Label, "]") {
			b.WriteString(strings.TrimSuffix(strings.TrimPrefix(f.Label, "["), "]"))
		} else {
			b.WriteString(f.Kind)
			b.WriteString(" ")
			b.WriteString(f.Label)
		}
	}
	return b.String()
}

// ownerChatFor returns the owner chat for a card or a note. It logs a warning that names the job
// when no chat exists.
func (a *App) ownerChatFor(ctx context.Context, job string) (int64, error) {
	if a.Telegram != nil {
		if id, ok := a.Telegram.HomeOwnerChat(ctx); ok {
			return id, nil
		}
	}
	a.Log.Warn("cron: no private chat of an owner exists, so the delivery of the job is blocked", "job", job)
	return 0, fmt.Errorf("cron: job %s: %w", job, errNoOwnerChat)
}

// sendCronNote sends the note to the owner's private chat, with the button that posts the original.
func (a *App) sendCronNote(ctx context.Context, n cron.Note) error {
	chat, err := a.ownerChatFor(ctx, n.Job.Name)
	if err != nil {
		return err
	}
	p := telegram.SendMessageParams{ChatID: chat, Text: cronNoteText(n)}
	if n.HeldID != "" {
		p.ReplyMarkup = &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{{Text: "✅ Post the full text there", CallbackData: "hs:" + n.HeldID + ":s"}}}}
	}
	m, err := a.Telegram.C.SendMessage(ctx, p)
	if err != nil {
		return fmt.Errorf("cron: send the note of job %s: %w", n.Job.Name, err)
	}
	if n.HeldID != "" {
		return a.Sched.Deliver.Held.SetCard(ctx, n.HeldID, strconv.FormatInt(chat, 10), m.MessageID)
	}
	return nil
}

// holdDelivery posts the held card to the owner's private chat and saves the card id in the row.
func (a *App) holdDelivery(ctx context.Context, h cron.HeldSend, card cron.HeldCardInfo) error {
	chat, err := a.ownerChatFor(ctx, card.JobName)
	if err != nil {
		return err
	}
	id, err := a.heldCards.Post(ctx, chat, telegram.HeldCard{ID: h.ID, JobName: card.JobName,
		TargetTitle: card.TargetTitle, TargetKind: card.TargetKind, Reason: card.Reason, Text: h.Text,
		Sources: card.Sources, Kind: "hold"})
	if err != nil {
		return fmt.Errorf("cron: post the held card of job %s: %w", card.JobName, err)
	}
	return a.Sched.Deliver.Held.SetCard(ctx, h.ID, strconv.FormatInt(chat, 10), id)
}

// heldSendStore gives telegram.HeldCards the held rows and the run row of each one.
type heldSendStore struct {
	held *cron.HeldStore
	jobs *cron.Store
}

func (s heldSendStore) Claim(ctx context.Context, id string) (cron.HeldSend, bool, error) {
	return s.held.Claim(ctx, id)
}

func (s heldSendStore) Restore(ctx context.Context, h cron.HeldSend) error {
	return s.held.Restore(ctx, h)
}

func (s heldSendStore) MarkRun(ctx context.Context, runID int64, delivery string) error {
	return s.jobs.UpdateRunDelivery(ctx, runID, delivery)
}

// sendHeld sends a held row to its target. A row of kind "original" skips the channel scan.
func (a *App) sendHeld(ctx context.Context, h cron.HeldSend) error {
	src, rest, _ := strings.Cut(h.Target, ":")
	chat, thread, _ := strings.Cut(rest, ":")
	return a.Telegram.Send(ctx, gateway.Target{Source: src, ChatID: chat, ThreadID: thread},
		gateway.Outbound{Text: h.Text, Raw: h.Kind == "original"})
}

// logDeliveryDecision writes the model_guard decision of a delivery to approval_log.
func (a *App) logDeliveryDecision(ctx context.Context, j cron.Job, decision, reason string) {
	scope := ""
	if j.Origin.Source == "telegram" && j.Origin.ChatID != "" {
		scope = gateway.ScopeFor("telegram", j.Origin.ChatID, j.Origin.ThreadID)
	}
	a.Gate.RecordDecision(ctx, security.ApprovalRequest{Kind: "cron", Tool: "cron_delivery", Command: reason,
		JobName: j.Name, LogScope: scope}, decision, "model-guard", "")
}

// sweepHeld ends each held row older than 24 h and edits its card to "Expired.".
func (a *App) sweepHeld(ctx context.Context) {
	held := a.Sched.Deliver.Held
	rows, err := held.ClaimExpired(ctx, time.Now().Add(-heldExpiry))
	if err != nil {
		a.Log.Warn("cron: cannot sweep the held sends", "err", err.Error())
		return
	}
	for _, h := range rows {
		if h.Kind == "hold" && h.RunID != 0 {
			if err := a.Cron.UpdateRunDelivery(ctx, h.RunID, cron.DeliveryExpired); err != nil {
				a.Log.Warn("cron: cannot mark the run of an expired held send", "job", h.JobID, "err", err.Error())
			}
		}
		if h.CardMsgID == 0 || a.Telegram == nil {
			continue
		}
		chat, err := strconv.ParseInt(h.CardChat, 10, 64)
		if err != nil {
			continue
		}
		if _, err := a.Telegram.C.EditMessageText(ctx, telegram.EditMessageTextParams{
			ChatID: chat, MessageID: h.CardMsgID, Text: "Expired."}); err != nil {
			a.Log.Warn("cron: cannot edit the card of an expired held send", "job", h.JobID, "err", err.Error())
		}
	}
}

// draftPrivateTarget reports whether the deliver value of a cronjob call stays with the owner.
func (a *App) draftPrivateTarget(ctx context.Context, env *tools.Env, deliver string) bool {
	switch d := strings.TrimSpace(deliver); {
	case d == "local":
		return true
	case strings.HasPrefix(d, "telegram:"):
		chat, thread, _ := strings.Cut(strings.TrimPrefix(d, "telegram:"), ":")
		return a.Cron.IsPrivate != nil && a.Cron.IsPrivate(ctx, cron.Target{Source: "telegram", ChatID: chat, ThreadID: thread})
	}
	// An empty value and "origin" use the chat of the session. The run checks the home target again.
	if env != nil && env.Channel == "telegram" {
		sess, err := a.Store.Get(ctx, env.SessionID)
		if err != nil || sess == nil {
			return false
		}
		return a.Cron.IsPrivate != nil && a.Cron.IsPrivate(ctx, cron.Target{Source: "telegram", ChatID: sess.ChatID, ThreadID: sess.ThreadID})
	}
	src := ""
	if env != nil {
		src = env.Channel
	}
	return a.Cron.IsPrivate != nil && a.Cron.IsPrivate(ctx, cron.Target{Source: src})
}
