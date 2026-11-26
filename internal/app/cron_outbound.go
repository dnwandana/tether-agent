package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/db"
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
	// kv is true when the KV table holds the name. A getChat answer is in memory only.
	kv bool
}

type topicEntry struct {
	name string
	at   time.Time
}

type topicKey struct {
	chat   int64
	thread int
}

// maxTitles is the limit of the chat map and of the topic map. A new entry in a full map
// removes the oldest entry.
const maxTitles = 1000

// missTTL is how long names keeps a KV miss. A list of chats with no name then makes no KV
// read for each row. A put clears the miss at once.
const missTTL = time.Minute

// chatTitles keeps the chat names and the topic names of Telegram. The getChat answers, the
// incoming updates, and the KV table fill it. It holds at most maxTitles chats and maxTitles topics.
type chatTitles struct {
	C   *telegram.Client
	TTL time.Duration
	Now func() time.Time
	// DB keeps the names across a restart. Nil keeps them in memory only.
	DB *db.DB
	// Log gets the debug line of a KV error. Nil writes no log line.
	Log *slog.Logger

	mu     sync.Mutex
	m      map[int64]titleEntry
	topics map[topicKey]topicEntry
	// misses holds the time of each KV miss. The key {chat, 0} is a chat miss, and the key
	// {chat, thread} is a topic miss.
	misses   map[topicKey]time.Time
	kvLogged bool
}

// missed reports whether names had a KV miss for k less than missTTL ago. The caller holds c.mu.
func (c *chatTitles) missed(k topicKey) bool {
	at, ok := c.misses[k]
	return ok && c.now().Sub(at) < missTTL
}

// addMiss keeps a KV miss for k. A full map starts again empty. The caller holds c.mu.
func (c *chatTitles) addMiss(k topicKey) {
	if c.misses == nil || len(c.misses) >= maxTitles {
		c.misses = map[topicKey]time.Time{}
	}
	c.misses[k] = c.now()
}

func (c *chatTitles) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// displayName returns the title, else the first name, else the username of chat.
func displayName(chat telegram.Chat) string {
	switch {
	case chat.Title != "":
		return chat.Title
	case chat.FirstName != "":
		return chat.FirstName
	}
	return chat.Username
}

// storeChat keeps e under id. It removes the oldest entry when the map is full and id is new.
// The caller holds c.mu.
func (c *chatTitles) storeChat(id int64, e titleEntry) {
	if c.m == nil {
		c.m = map[int64]titleEntry{}
	}
	if _, ok := c.m[id]; !ok && len(c.m) >= maxTitles {
		var oldest int64
		var at time.Time
		first := true
		for k, v := range c.m {
			if first || v.at.Before(at) {
				oldest, at, first = k, v.at, false
			}
		}
		delete(c.m, oldest)
	}
	c.m[id] = e
}

// storeTopic keeps e under k with the same limit as storeChat. The caller holds c.mu.
func (c *chatTitles) storeTopic(k topicKey, e topicEntry) {
	if c.topics == nil {
		c.topics = map[topicKey]topicEntry{}
	}
	if _, ok := c.topics[k]; !ok && len(c.topics) >= maxTitles {
		var oldest topicKey
		var at time.Time
		first := true
		for key, v := range c.topics {
			if first || v.at.Before(at) {
				oldest, at, first = key, v.at, false
			}
		}
		delete(c.topics, oldest)
	}
	c.topics[k] = e
}

func chatKey(id int64) string { return "tg.chat." + strconv.FormatInt(id, 10) }

func topicKV(id int64, thread int) string {
	return "tg.topic." + strconv.FormatInt(id, 10) + "." + strconv.Itoa(thread)
}

// kvFailed writes one debug line for the first KV error.
func (c *chatTitles) kvFailed() {
	c.mu.Lock()
	first := !c.kvLogged
	c.kvLogged = true
	c.mu.Unlock()
	if first && c.Log != nil {
		c.Log.Debug("telegram names: cannot write the KV table; the names stay in memory")
	}
}

// put keeps the display name of chat and, for a thread id that is not 0, the topic name.
// It skips a chat with no title, no first name, and no username, and an empty topic name.
// It writes a KV key only when the name differs from the cached name. A topic name that is
// not exact can be the old name of a renamed topic, so it only fills a gap: it is kept only
// when neither the memory nor the KV table has a name for the topic.
func (c *chatTitles) put(chat telegram.Chat, threadID int, topic string, exact bool) {
	name := displayName(chat)
	if name == "" {
		return
	}
	ctx := context.Background()
	k := topicKey{chat.ID, threadID}
	fromKV := false
	if threadID != 0 && topic != "" && !exact {
		c.mu.Lock()
		_, cached := c.topics[k]
		c.mu.Unlock()
		if cached {
			topic = ""
		} else if c.DB != nil {
			v, hit, err := c.DB.KVGet(ctx, topicKV(chat.ID, threadID))
			if err == nil && hit && v != "" {
				topic, fromKV = v, true
			}
		}
	}
	now := c.now()
	c.mu.Lock()
	prev, had := c.m[chat.ID]
	writeChat := !had || !prev.kv || displayName(prev.chat) != name
	c.storeChat(chat.ID, titleEntry{chat: chat, at: now, kv: c.DB != nil})
	delete(c.misses, topicKey{chat.ID, 0})
	delete(c.misses, k)
	writeTopic := false
	if threadID != 0 && topic != "" {
		pt, ok := c.topics[k]
		writeTopic = !fromKV && (!ok || pt.name != topic)
		c.storeTopic(k, topicEntry{name: topic, at: now})
	}
	c.mu.Unlock()
	if c.DB == nil {
		return
	}
	if writeChat {
		if err := c.DB.KVSet(ctx, chatKey(chat.ID), name); err != nil {
			c.kvFailed()
		}
	}
	if writeTopic {
		if err := c.DB.KVSet(ctx, topicKV(chat.ID, threadID), topic); err != nil {
			c.kvFailed()
		}
	}
}

// names returns the display name of the chat and, for a thread id that is not 0, the topic
// name. It reads the memory cache first, then the KV table, and puts a KV hit into memory.
// ok is false when the chat name is not known. It never calls getChat.
func (c *chatTitles) names(ctx context.Context, chatID int64, threadID int) (chat, topic string, ok bool) {
	ck := topicKey{chatID, 0}
	c.mu.Lock()
	e, found := c.m[chatID]
	skip := c.missed(ck)
	c.mu.Unlock()
	if found {
		chat = displayName(e.chat)
	}
	if chat == "" && c.DB != nil && !skip {
		v, hit, err := c.DB.KVGet(ctx, chatKey(chatID))
		c.mu.Lock()
		if err == nil && hit && v != "" {
			chat = v
			if _, again := c.m[chatID]; !again {
				c.storeChat(chatID, titleEntry{chat: telegram.Chat{ID: chatID, Title: v}, kv: true})
			}
		} else if err == nil {
			c.addMiss(ck)
		}
		c.mu.Unlock()
	}
	if chat == "" {
		return "", "", false
	}
	if threadID == 0 {
		return chat, "", true
	}
	k := topicKey{chatID, threadID}
	c.mu.Lock()
	te, found := c.topics[k]
	skip = c.missed(k)
	c.mu.Unlock()
	if found {
		return chat, te.name, true
	}
	if c.DB != nil && !skip {
		v, hit, err := c.DB.KVGet(ctx, topicKV(chatID, threadID))
		c.mu.Lock()
		defer c.mu.Unlock()
		if err == nil && hit && v != "" {
			c.storeTopic(k, topicEntry{name: v})
			return chat, v, true
		}
		if err == nil {
			c.addMiss(k)
		}
	}
	return chat, "", true
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
	c.storeChat(id, titleEntry{chat: chat, at: c.now()})
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
	p := telegram.SendMessageParams{ChatID: chat, Text: cronNoteText(n), DisableNotification: n.NoSound}
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
	if err := a.Sched.Deliver.Held.SetCard(ctx, h.ID, strconv.FormatInt(chat, 10), id); err != nil {
		return err
	}
	a.publishHeld(h.ID)
	return nil
}

// errHeldTelegramOff is the answer of a dashboard Post when Telegram is off.
//
//lint:ignore ST1005 The dashboard shows this text to the owner as it is.
var errHeldTelegramOff = errors.New("Telegram is off, so the text cannot be posted. Drop it, or turn Telegram on.")

// editHeldCard replaces the text of the Telegram card of h with result and removes the
// buttons. It does nothing when Telegram is off or the row has no card.
func (a *App) editHeldCard(h cron.HeldSend, result string) {
	if h.CardMsgID == 0 || a.Telegram == nil {
		return
	}
	chat, err := strconv.ParseInt(h.CardChat, 10, 64)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := a.Telegram.C.EditMessageText(ctx, telegram.EditMessageTextParams{
		ChatID: chat, MessageID: h.CardMsgID, Text: result}); err != nil {
		a.Log.Warn("cron: cannot edit the card of a held send", "job", h.JobID, "err", err.Error())
	}
}

// resolveHeld posts or drops a held send for the dashboard. It skips the card-chat check of
// the Telegram tap, and it edits the Telegram card of the row.
func (a *App) resolveHeld(ctx context.Context, id, action string) (string, error) {
	if action == "post" && a.Telegram == nil {
		return "", errHeldTelegramOff
	}
	result, err := cron.ResolveHeld(ctx, a.Cron, id, action, a.sendHeld, a.editHeldCard)
	if result != "" {
		a.publishHeld(id)
	}
	return result, err
}

// publishHeld tells the open dashboard pages that the held send id changed. It does
// nothing when the dashboard is off.
func (a *App) publishHeld(id string) {
	a.publishDashboard("held", map[string]any{"id": id})
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

// sweepHeld ends each held send older than 24 h and edits its card to "Expired.". It also
// sweeps the held memory writes and the old notice cards, and it runs the curator check.
func (a *App) sweepHeld(ctx context.Context) {
	a.sweepLearn(ctx)
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
		a.editHeldCard(h, "Expired.")
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
