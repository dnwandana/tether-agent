package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// The kv keys of the home channel.
const (
	homeChatKey   = "telegram.home_chat"
	homeThreadKey = "telegram.home_thread"
)

// typingEvery is the time between two typing actions. Telegram shows the action for
// about 5 s.
const typingEvery = 4 * time.Second

// StartHelp is the reply to /start for a user who may use the bot.
const StartHelp = "👋 **Tether Agent is ready**\nSend a message to start a conversation.\n\n" +
	"/new · start a new session\n/sessions · see your sessions\n/stop · stop the current task\n/help · see all commands"

// startButtons are the buttons of the /start reply.
var startButtons = [][]gateway.Button{{
	{Text: "📋 Sessions", Data: "/sessions"},
	{Text: "🧠 Model", Data: "/model"},
	{Text: "📊 Usage", Data: "/usage"},
}}

// noPendingApproval is the reply to /approve and /deny when no command waits. It is a
// Tether Agent text.
const noPendingApproval = "💤 **No command waits for approval**"

// Channel is the Telegram channel of the gateway. It implements
// gateway.Channel with the name "telegram".
type Channel struct {
	Cfg   config.TelegramConfig
	C     *Client
	DB    *db.DB
	Pair  *Pairing
	Inbox *Inbox
	Out   *Sender
	// Media decides which local files of an answer go to the chat (MEDIA tags and bare
	// paths). Nil sends no such file.
	Media *MediaPolicy
	// Bot is the bot user from getMe.
	Bot User
	Log *slog.Logger
	// UI holds the approval buttons and the ask_user prompts. Nil means that no prompt is
	// shown.
	UI *Interactions

	// turnCtx is the context of the update handlers and the turns. Nil uses the context of
	// Run. SetTurnContext sets it.
	turnCtx context.Context

	// typingEvery is the time between two typing actions. Zero uses 4 s. Tests change it.
	typingEvery time.Duration

	albums AlbumBuffer

	typingMu sync.Mutex
	typing   map[string]*typingLoop

	// active holds the renderer of the running turn of each session key, for Progress.
	activeMu sync.Mutex
	active   map[string]*tgRenderer

	// richFailed is set at the first failed rich message. Later failures log at the debug
	// level.
	richFailed atomic.Bool
}

var _ gateway.Channel = (*Channel)(nil)

// Name returns "telegram".
func (ch *Channel) Name() string { return "telegram" }

// Run registers the bot commands and polls for updates until ctx ends.
func (ch *Channel) Run(ctx context.Context, gw *gateway.Gateway) error {
	if ch.UI != nil && ch.UI.Authorized == nil {
		ch.UI.Authorized = ch.callbackAuthorized
	}
	p := ch.poller()
	if err := p.RegisterCommands(ctx); err != nil {
		ch.logger().Warn("telegram: cannot register the bot commands", "err", err.Error())
	}
	ch.logger().Info("telegram channel started", "bot", ch.Bot.Username)
	return p.Run(ctx, func(ctx context.Context, u Update) { ch.handleUpdate(ctx, gw, u) })
}

// SetTurnContext sets the context of the update handlers and the turns. Then the context
// of Run stops only the poll, and the running turns can end at a gateway stop. Call it
// before Run.
func (ch *Channel) SetTurnContext(ctx context.Context) { ch.turnCtx = ctx }

// poller returns the poller of Run.
func (ch *Channel) poller() *Poller {
	return &Poller{C: ch.C, DB: ch.DB, Log: ch.Log, HandleCtx: ch.turnCtx}
}

// Send sends a message to a chat, for cron delivery and other sends with no turn. The text
// is converted to MarkdownV2 and split at the message limit. The MEDIA files of the text and
// msg.Files go after the text.
func (ch *Channel) Send(ctx context.Context, to gateway.Target, msg gateway.Outbound) error {
	chatID, err := strconv.ParseInt(to.ChatID, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram send: the chat id %q is not a number", to.ChatID)
	}
	// msg.Files come from the code, not from the model, so the media policy does not check
	// them.
	text, files := ch.Media.ExtractMedia(msg.Text)
	for _, f := range msg.Files {
		files = append(files, OutFile{Path: f, Kind: mediaKind(f)})
	}
	opts := sendOpts{keyboard: keyboardMarkup(msg.Keyboard)}
	if id, err := strconv.Atoi(msg.ReplyTo); err == nil && id > 0 {
		opts.replyTo = id
	}
	var errs []error
	if strings.TrimSpace(text) != "" {
		if _, err := ch.sendText(ctx, chatID, to.ThreadID, text, opts); err != nil {
			errs = append(errs, err)
		}
	}
	if len(files) > 0 && ch.Out != nil {
		if err := ch.Out.SendFiles(ctx, chatID, to.ThreadID, files); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// handleUpdate handles one update. The order is: the free-text answer hook, the gate, the
// authorization, and then the session key. So an unauthorized user never creates a session
// row. The gateway does not check the authorization again on the busy path, so this
// function must check it before gw.Submit.
func (ch *Channel) handleUpdate(ctx context.Context, gw *gateway.Gateway, u Update) {
	defer func() {
		if v := recover(); v != nil {
			ch.logger().Error("telegram: the update handler panicked", "update", u.UpdateID,
				"panic", fmt.Sprint(v), "stack", string(debug.Stack()))
		}
	}()
	if u.CallbackQuery != nil {
		if strings.HasPrefix(u.CallbackQuery.Data, cmdPrefix+":") {
			ch.commandCallback(ctx, gw, *u.CallbackQuery)
			return
		}
		if ch.UI == nil || !ch.UI.HandleCallback(ctx, *u.CallbackQuery) {
			// An old or unknown button. Stop the spinner of the button.
			_ = ch.C.AnswerCallbackQuery(ctx, u.CallbackQuery.ID, "", false)
		}
		return
	}
	if u.Message == nil {
		return
	}
	m := *u.Message
	if m.From != nil && m.From.IsBot {
		return
	}
	n := Normalize(m)
	if n.Kind == KindChannel {
		return
	}
	// A free-text answer to ask_user. It comes before the gate, because the answer in a
	// group does not mention the bot. Only an authorized user can answer.
	if ch.UI != nil && m.Text != "" && !strings.HasPrefix(m.Text, "/") &&
		Authorize(ch.Cfg, n, ch.pairApproved(ctx)) == AuthAllow &&
		ch.UI.TakeText(m.Chat.ID, n.ThreadID, StripMention(m.Text, ch.Bot.Username)) {
		return
	}
	d := Gate(ch.Cfg, ch.Bot.Username, ch.Bot.ID, m, n)
	if d.Observe {
		ch.observe(ctx, gw, n, d.Text)
		return
	}
	if !d.Accept {
		ch.logger().Debug("telegram: message not accepted", "chat", n.ChatID, "reason", d.Reason)
		return
	}
	switch Authorize(ch.Cfg, n, ch.pairApproved(ctx)) {
	case AuthIgnore:
		ch.logger().Info("telegram: message from an unauthorized user ignored", "chat", n.ChatID, "user", n.UserID)
		return
	case AuthPair:
		ch.pair(ctx, m, n)
		return
	}
	text := CanonicalCommand(d.Text)
	if ch.localCommand(ctx, m, n, text) {
		return
	}
	if m.MediaGroupID != "" {
		ch.albums.Add(m, func(msgs []Message) { ch.dispatch(ctx, gw, n, msgs, text) })
		return
	}
	ch.dispatch(ctx, gw, n, []Message{m}, text)
}

// dispatch builds the inbound message of msgs and gives it to the gateway. text is the
// gated text of the first message, without the bot mention.
func (ch *Channel) dispatch(ctx context.Context, gw *gateway.Gateway, n Normalized, msgs []Message, text string) {
	in := gateway.Inbound{
		Key:      ch.sessionKey(n),
		Source:   "telegram",
		ChatID:   n.ChatID,
		ThreadID: n.ThreadID,
		UserID:   n.UserID,
		UserName: n.UserName,
		Text:     text,
		IsText:   true,
		Target:   gateway.Target{Source: "telegram", ChatID: n.ChatID, ThreadID: n.ThreadID},
	}
	if ch.observeShared(n) {
		// The shared session of an observed chat holds the messages of all users.
		in.UserID = ""
	}
	media := hasMedia(msgs)
	_, _, isCmd := gateway.IsCommand(text)
	isCmd = isCmd && !media
	chatID := msgs[0].Chat.ID
	// The typing action shows from now until the turn of this message takes it over. It
	// also covers the download and the transcription of the media. A steer, a queued
	// message, or a command releases it when the gateway is done with the message.
	release := func() {}
	if !isCmd {
		release = ch.startTyping(ctx, chatID, n.ThreadID)
	}
	submitted := false
	defer func() {
		if !submitted {
			release()
		}
	}()
	if media {
		if ch.Inbox == nil {
			return
		}
		clean := make([]Message, len(msgs))
		for i, m := range msgs {
			m.Caption = StripMention(m.Caption, ch.Bot.Username)
			m.Text = StripMention(m.Text, ch.Bot.Username)
			clean[i] = m
		}
		t, images, files, err := ch.Inbox.Build(ctx, clean)
		if err != nil {
			return
		}
		in.Text, in.Images, in.Files, in.IsText = t, images, files, false
	}
	if !isCmd && ch.observeShared(n) {
		in.Text = attributed(n, in.Text)
	}
	newRenderer := func() gateway.Renderer {
		r := ch.newRenderer(ctx, chatID, n.ThreadID, n.Kind == KindDM)
		r.key, r.handoff, r.userID = in.Key, release, n.UserID
		return r
	}
	// Submit returns when the gateway has taken the message, so the next update of the chat
	// keeps its order and does not wait for the turn.
	submitted = true
	gw.Submit(ctx, in, newRenderer, func(err error) {
		release()
		if err != nil && ctx.Err() == nil {
			ch.logger().Warn("telegram: the message failed", "key", in.Key, "err", err.Error())
		}
	})
}

// observe saves an observed group message to the shared session of the chat or topic, with
// observed = 1. The agent does not answer it (gate step 10).
func (ch *Channel) observe(ctx context.Context, gw *gateway.Gateway, n Normalized, text string) {
	if strings.TrimSpace(n.Text) == "" || gw == nil || gw.Deps == nil || gw.Deps.Store == nil {
		return
	}
	st := gw.Deps.Store
	key := SessionKey(string(n.Kind), n.ChatID, n.ThreadID, "", false, false)
	sess, err := st.ActiveOrCreate(ctx, key, sessions.Session{
		Source: "telegram", ChatID: n.ChatID, ThreadID: n.ThreadID, Kind: "chat",
	})
	if err != nil {
		ch.logger().Warn("telegram: cannot save the observed message", "key", key, "err", err.Error())
		return
	}
	if _, err := st.Append(ctx, sess.ID, provider.Message{Role: provider.RoleUser, Content: text},
		sessions.MessageMeta{Observed: true}); err != nil {
		ch.logger().Warn("telegram: cannot save the observed message", "key", key, "err", err.Error())
	}
}

// pair sends the pairing code to an unknown DM user.
func (ch *Channel) pair(ctx context.Context, m Message, n Normalized) {
	if ch.Pair == nil {
		return
	}
	_, reply, err := ch.Pair.Request(ctx, "telegram", n.UserID, n.UserName)
	if err != nil && !errors.Is(err, ErrPairRateLimited) && !errors.Is(err, ErrTooManyPending) &&
		!errors.Is(err, ErrPairLockedOut) {
		ch.logger().Error("telegram: the pairing request failed", "user", n.UserID, "err", err.Error())
	}
	if reply == "" {
		return
	}
	if _, err := ch.sendText(ctx, m.Chat.ID, n.ThreadID, reply, sendOpts{}); err != nil {
		ch.logger().Warn("telegram: cannot send the pairing code", "user", n.UserID, "err", err.Error())
	}
}

// localCommand runs the commands that only Telegram has: /start, /sethome, /approve, and
// /deny. It returns false for other text. These commands skip the gateway, so they also
// work while a run waits for an approval.
func (ch *Channel) localCommand(ctx context.Context, m Message, n Normalized, text string) bool {
	name, args, ok := gateway.IsCommand(text)
	if !ok {
		return false
	}
	send := func(s string, o sendOpts) {
		if _, err := ch.sendText(ctx, m.Chat.ID, n.ThreadID, s, o); err != nil {
			ch.logger().Warn("telegram: cannot send the command reply", "command", name, "err", err.Error())
		}
	}
	reply := func(s string) { send(s, sendOpts{}) }
	switch name {
	case "start":
		send(StartHelp, sendOpts{keyboard: cmdKeyboard(n.UserID, startButtons)})
	case "sethome":
		if err := SetHome(ctx, ch.DB, n.ChatID, n.ThreadID); err != nil {
			ch.logger().Error("telegram: cannot save the home channel", "err", err.Error())
			reply("⚠️ **Cannot save the home channel**\n" + gateway.EscapeMarkdown(err.Error()) + ". Try again later.")
			return true
		}
		title := m.Chat.Title
		if title == "" {
			title = n.UserName
		}
		if title == "" {
			title = n.ChatID
		}
		reply("🏠 **Home channel: " + gateway.EscapeMarkdown(title) + "**\n" +
			"Cron results and messages from other channels come to this chat.")
	case "approve", "deny":
		reply(ch.resolveText(m.Chat.ID, name, args))
	default:
		return false
	}
	return true
}

// resolveText runs the /approve or /deny text fallback for the pending approvals of the
// chat, and returns the reply (Hermes).
func (ch *Channel) resolveText(chatID int64, name, args string) string {
	fields := strings.Fields(strings.ToLower(args))
	all := false
	scope := security.ScopeOnce
	for _, f := range fields {
		switch f {
		case "all":
			all = true
		case "session", "ses":
			scope = security.ScopeSession
		case "always", "permanent", "permanently":
			scope = security.ScopeAlways
		}
	}
	if name == "deny" {
		scope = security.ScopeDeny
	}
	n := 0
	if ch.UI != nil {
		n = ch.UI.ResolveApprovals(chatID, scope, all)
	}
	return resolveReply(name, scope, n)
}

// sessionKey returns the session key of a message. In a chat with observed
// context, all users share one session for the chat or the topic (Hermes).
func (ch *Channel) sessionKey(n Normalized) string {
	if ch.observeShared(n) {
		return SessionKey(string(n.Kind), n.ChatID, n.ThreadID, "", false, false)
	}
	return SessionKey(string(n.Kind), n.ChatID, n.ThreadID, n.UserID, ch.Cfg.GroupSessionsPerUser, ch.Cfg.ThreadSessionsPerUser)
}

// observeShared reports whether the group chat of n keeps observed context. Such a chat
// uses one shared session, so a later trigger from another user sees the context (Hermes
// _apply_telegram_group_observe_attribution).
func (ch *Channel) observeShared(n Normalized) bool {
	return ch.Cfg.ObserveUnmentionedGroupMessages && n.Kind == KindGroup && observeAllowed(ch.Cfg, n.ChatID)
}

// attributed returns text with the "[name|id]" line of the sender, like an observed message.
func attributed(n Normalized, text string) string {
	user := n.UserID
	if user == "" {
		user = "unknown"
	}
	name := n.UserName
	if name == "" {
		name = user
	}
	return "[" + name + "|" + user + "]\n" + text
}

func (ch *Channel) pairApproved(ctx context.Context) func(string) bool {
	if ch.Pair == nil {
		return nil
	}
	return func(uid string) bool { return ch.Pair.IsApproved(ctx, "telegram", uid) }
}

// commandCallback runs the action of a command button. The action goes through the gateway
// like a typed command, so the busy rule and the ownership check apply. Only the user who
// sent the command that made the button can tap it.
func (ch *Channel) commandCallback(ctx context.Context, gw *gateway.Gateway, cq CallbackQuery) {
	answer := func(text string) {
		if err := ch.C.AnswerCallbackQuery(ctx, cq.ID, text, false); err != nil {
			ch.logger().Debug("telegram: cannot answer the button press", "err", err.Error())
		}
	}
	user, action, ok := decodeCmd(cq.Data)
	if ok && action != "·" && action != "cancel" {
		_, _, ok = gateway.IsCommand(action)
	}
	if !ok || cq.Message == nil {
		answer(invalidCallback)
		return
	}
	if !ch.callbackAuthorized(ctx, cq) {
		answer(notAuthorized)
		return
	}
	if user != strconv.FormatInt(cq.From.ID, 10) {
		answer(otherUser)
		return
	}
	answer("")
	msg := cq.Message
	n := Normalize(Message{Chat: msg.Chat, MessageThreadID: msg.MessageThreadID,
		IsTopicMessage: msg.IsTopicMessage, From: &cq.From})
	newRenderer := func() *tgRenderer {
		r := ch.newRenderer(ctx, msg.Chat.ID, n.ThreadID, n.Kind == KindDM)
		r.key, r.userID, r.editMsgID = ch.sessionKey(n), n.UserID, msg.MessageID
		return r
	}
	switch action {
	case "·":
		return
	case "cancel":
		newRenderer().AckReply(gateway.Reply{Text: "Not deleted.", Edit: true})
		return
	}
	in := gateway.Inbound{
		Key:      ch.sessionKey(n),
		Source:   "telegram",
		ChatID:   n.ChatID,
		ThreadID: n.ThreadID,
		UserID:   n.UserID,
		UserName: n.UserName,
		Text:     CanonicalCommand(action),
		IsText:   true,
		Target:   gateway.Target{Source: "telegram", ChatID: n.ChatID, ThreadID: n.ThreadID},
	}
	if ch.observeShared(n) {
		in.UserID = ""
	}
	gw.Submit(ctx, in, func() gateway.Renderer { return newRenderer() }, func(err error) {
		if err != nil && ctx.Err() == nil {
			ch.logger().Warn("telegram: the button command failed", "key", in.Key, "err", err.Error())
		}
	})
}

// callbackAuthorized reports whether the user who pressed a button may use the bot. A
// button press has no gate, so only the authorization applies (Hermes
// _is_callback_user_authorized).
func (ch *Channel) callbackAuthorized(ctx context.Context, cq CallbackQuery) bool {
	n := Normalized{Kind: KindDM, UserID: strconv.FormatInt(cq.From.ID, 10)}
	if cq.Message != nil {
		n = Normalize(Message{Chat: cq.Message.Chat, MessageThreadID: cq.Message.MessageThreadID,
			IsTopicMessage: cq.Message.IsTopicMessage, From: &cq.From})
	}
	return Authorize(ch.Cfg, n, ch.pairApproved(ctx)) == AuthAllow
}

func hasMedia(msgs []Message) bool {
	for _, m := range msgs {
		if len(m.Photo) > 0 || m.Document != nil || m.Voice != nil {
			return true
		}
	}
	return false
}

// SetHome saves the chat and the topic of the home channel in the kv table.
func SetHome(ctx context.Context, d *db.DB, chatID, threadID string) error {
	if err := d.KVSet(ctx, homeChatKey, chatID); err != nil {
		return err
	}
	return d.KVSet(ctx, homeThreadKey, threadID)
}

// HomeTarget returns the home channel: the chat of /sethome, else telegram.home_chat of the
// config. With forCron, telegram.cron_thread replaces the topic when it is set. The bool is
// false when no home chat is set.
func HomeTarget(ctx context.Context, d *db.DB, cfg config.TelegramConfig, forCron bool) (gateway.Target, bool) {
	t := gateway.Target{Source: "telegram", ChatID: strings.TrimSpace(cfg.HomeChat), ThreadID: strings.TrimSpace(cfg.HomeThread)}
	if d != nil {
		if chat, ok, err := d.KVGet(ctx, homeChatKey); err == nil && ok && chat != "" {
			thread, _, _ := d.KVGet(ctx, homeThreadKey)
			t.ChatID, t.ThreadID = chat, thread
		}
	}
	if t.ChatID == "" {
		return gateway.Target{}, false
	}
	if forCron && strings.TrimSpace(cfg.CronThread) != "" {
		t.ThreadID = strings.TrimSpace(cfg.CronThread)
	}
	return t, true
}

// ChatInfo returns the chat line of the system prompt for a Telegram session: the chat
// type and the topic. It returns "" for a session of another source.
func ChatInfo(cfg config.TelegramConfig, bot User) func(s *sessions.Session) string {
	return func(s *sessions.Session) string {
		if s == nil || s.Source != "telegram" {
			return ""
		}
		parts := strings.Split(s.Key, ":")
		kind := ""
		if len(parts) > 1 {
			kind = parts[1]
		}
		if kind == string(KindDM) {
			info := "Telegram private chat"
			if s.ThreadID != "" {
				info += ", topic " + s.ThreadID
			}
			return info
		}
		info := "Telegram group chat " + s.ChatID
		if s.ThreadID != "" {
			info += ", topic " + s.ThreadID
		}
		if s.UserID != "" && cfg.GroupSessionsPerUser && (s.ThreadID == "" || cfg.ThreadSessionsPerUser) {
			info += ", a private session of user " + s.UserID
		} else {
			info += ", one session shared by the members"
		}
		if bot.Username != "" {
			info += fmt.Sprintf(". Your user id is %d and your mention name is @%s", bot.ID, bot.Username)
		}
		if cfg.ObserveUnmentionedGroupMessages && observeAllowed(cfg, s.ChatID) {
			info += ". Each user message starts with a [name|id] line. Observed group messages " +
				"can come in a separate context-only block before the current message. They are not " +
				"always addressed to you. Treat only the current message as a request. Use the " +
				"observed context only when the current message asks for it"
		}
		return info
	}
}

// sendOpts are the options of sendText.
type sendOpts struct {
	keyboard *InlineKeyboardMarkup
	replyTo  int
}

// sendText sends text as MarkdownV2 parts with "(i/n)" numbers. A part that Telegram
// rejects goes again as plain text. The keyboard goes on the last part and the reply on
// the first part. It returns the id of the last message.
func (ch *Channel) sendText(ctx context.Context, chatID int64, threadID, text string, o sendOpts) (int, error) {
	parts := finalParts(text)
	last := 0
	for i, part := range parts {
		p := SendMessageParams{ChatID: chatID, Text: part, ParseMode: "MarkdownV2"}
		if id, ok := ThreadParam(threadID, false); ok {
			p.MessageThreadID = id
		}
		if i == len(parts)-1 {
			p.ReplyMarkup = o.keyboard
		}
		if i == 0 && o.replyTo > 0 {
			p.ReplyParameters = &ReplyParameters{MessageID: o.replyTo, AllowSendingWithoutReply: true}
		}
		m, err := ch.C.SendMessage(ctx, p)
		if err != nil && isBadRequest(err) {
			p.Text, p.ParseMode = StripMarkdownV2(part), ""
			m, err = ch.C.SendMessage(ctx, p)
		}
		if err != nil {
			return last, err
		}
		last = m.MessageID
	}
	return last, nil
}

// keyboardMarkup converts a gateway keyboard to the Bot API form. An empty keyboard gives nil.
func keyboardMarkup(rows [][]gateway.Button) *InlineKeyboardMarkup {
	if len(rows) == 0 {
		return nil
	}
	kb := &InlineKeyboardMarkup{}
	for _, row := range rows {
		var r []InlineKeyboardButton
		for _, b := range row {
			r = append(r, InlineKeyboardButton{Text: b.Text, CallbackData: b.Data})
		}
		kb.InlineKeyboard = append(kb.InlineKeyboard, r)
	}
	return kb
}

// Progress adds a line to the status message of the running turn of the session key, for
// example the "🔀 [1/3] tool" line of a delegate_task child. With no running turn, it
// does nothing.
func (ch *Channel) Progress(key, line string) {
	ch.activeMu.Lock()
	r := ch.active[key]
	ch.activeMu.Unlock()
	if r != nil {
		r.status.Add(r.ctx, line, "", "")
	}
}

func (ch *Channel) setActive(r *tgRenderer) {
	if r.key == "" {
		return
	}
	ch.activeMu.Lock()
	defer ch.activeMu.Unlock()
	if ch.active == nil {
		ch.active = map[string]*tgRenderer{}
	}
	ch.active[r.key] = r
}

func (ch *Channel) clearActive(r *tgRenderer) {
	ch.activeMu.Lock()
	defer ch.activeMu.Unlock()
	if ch.active[r.key] == r {
		delete(ch.active, r.key)
	}
}

// typingLoop is one typing action loop of a chat. Each user of the loop holds a reference.
type typingLoop struct {
	refs   int
	cancel context.CancelFunc
	done   chan struct{}
}

// startTyping sends the typing action to the chat at once and then every 4 s, until each
// caller called its stop function. Several callers for one chat share one loop. The last
// stop call waits until the loop ended, so no typing action comes after it.
func (ch *Channel) startTyping(ctx context.Context, chatID int64, threadID string) (stop func()) {
	key := strconv.FormatInt(chatID, 10) + ":" + threadID
	ch.typingMu.Lock()
	if ch.typing == nil {
		ch.typing = map[string]*typingLoop{}
	}
	l := ch.typing[key]
	if l == nil {
		lctx, cancel := context.WithCancel(ctx)
		l = &typingLoop{cancel: cancel, done: make(chan struct{})}
		ch.typing[key] = l
		go func() {
			defer close(l.done)
			ch.typingRun(lctx, chatID, threadID)
		}()
	}
	l.refs++
	ch.typingMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			ch.typingMu.Lock()
			l.refs--
			last := l.refs == 0
			if last {
				l.cancel()
				delete(ch.typing, key)
			}
			ch.typingMu.Unlock()
			if last {
				<-l.done
			}
		})
	}
}

// typingRun sends the typing action until ctx ends.
func (ch *Channel) typingRun(ctx context.Context, chatID int64, threadID string) {
	every := ch.typingEvery
	if every <= 0 {
		every = typingEvery
	}
	thread, _ := ThreadParam(threadID, true)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		// A failed typing action is not important, so the error is only logged.
		actx, cancel := context.WithTimeout(ctx, every)
		if err := ch.C.SendChatAction(actx, chatID, thread, "typing"); err != nil && ctx.Err() == nil {
			ch.logger().Debug("telegram: typing action failed", "err", err.Error())
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (ch *Channel) logger() *slog.Logger {
	if ch.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return ch.Log
}

// tgRenderer shows one turn in a Telegram chat. The status message shows the tool lines,
// the streamer shows the answer, and the files of the answer go out after the text.
type tgRenderer struct {
	ch       *Channel
	ctx      context.Context
	key      string // the session key of the turn
	chatID   int64
	threadID string
	status   *Status
	stream   *Streamer
	// userID is the Telegram user ID of the sender. The command buttons of a reply carry it.
	userID string
	// editMsgID is the message of the pressed button, or 0. A reply with Edit set replaces
	// that message.
	editMsgID int

	// handoff releases the typing action of the inbound message, when the turn takes it
	// over. It can be nil.
	handoff func()

	mu sync.Mutex
	// stopTyping ends the typing loop of the turn. It is set at the first event of the turn.
	stopTyping func()
	done       bool
	// cur is the streamed text of the open segment, and prev is the text of the closed
	// segment before it.
	cur, prev strings.Builder
}

var (
	_ gateway.Renderer      = (*tgRenderer)(nil)
	_ gateway.ReplyRenderer = (*tgRenderer)(nil)
)

func (ch *Channel) newRenderer(ctx context.Context, chatID int64, threadID string, isDM bool) *tgRenderer {
	rctx := context.WithoutCancel(ctx)
	st := &Status{C: ch.C, ChatID: chatID, ThreadID: threadID, Mode: ch.Cfg.ToolProgress,
		Cleanup: ch.Cfg.CleanupProgress, Log: ch.Log}
	sm := &Streamer{C: ch.C, ChatID: chatID, ThreadID: threadID, IsDM: isDM, Cfg: ch.Cfg.Streaming,
		OnFirstMessage: st.Reset, Log: ch.Log}
	return &tgRenderer{ch: ch, ctx: rctx, chatID: chatID, threadID: threadID, status: st, stream: sm}
}

// begin starts the typing loop at the first event of the turn. A renderer that only sends
// an ack never starts it.
func (r *tgRenderer) begin() bool {
	r.mu.Lock()
	if r.done {
		r.mu.Unlock()
		return false
	}
	first := r.stopTyping == nil
	if first {
		r.stopTyping = r.ch.startTyping(r.ctx, r.chatID, r.threadID)
	}
	r.mu.Unlock()
	if first {
		r.ch.setActive(r)
		if r.handoff != nil {
			r.handoff()
		}
	}
	return true
}

func (r *tgRenderer) Delta(text string) {
	if !r.begin() {
		return
	}
	r.mu.Lock()
	r.cur.WriteString(text)
	r.mu.Unlock()
	r.stream.Delta(r.ctx, text)
}

func (r *tgRenderer) ToolStart(name, preview string) {
	if r.begin() {
		r.status.Add(r.ctx, name, preview, "")
	}
}

func (r *tgRenderer) ToolEnd(string, bool) {}

func (r *tgRenderer) Boundary() {
	if !r.begin() {
		return
	}
	r.mu.Lock()
	if r.cur.Len() > 0 {
		r.prev.Reset()
		r.prev.WriteString(r.cur.String())
		r.cur.Reset()
	}
	r.mu.Unlock()
	r.stream.Boundary(r.ctx)
}

// Notice sends a status text as its own message.
func (r *tgRenderer) Notice(text string) {
	r.begin()
	r.Ack(text)
}

// Ack sends text at once as its own message.
func (r *tgRenderer) Ack(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if _, err := r.ch.sendText(r.ctx, r.chatID, r.threadID, text, sendOpts{}); err != nil {
		r.ch.logger().Warn("telegram: cannot send the reply", "chat", r.chatID, "err", err.Error())
	}
}

// AckReply sends a command reply with its buttons. A reply with Edit set replaces the
// message of the pressed button, when there is one. Rich goes as a rich message, and Text is
// the fallback when Telegram refuses it.
func (r *tgRenderer) AckReply(rep gateway.Reply) {
	kb := cmdKeyboard(r.userID, rep.Buttons)
	if rep.Edit && r.editMsgID > 0 && r.editReply(rep, kb) {
		return
	}
	if rep.Rich != "" {
		p := SendRichMessageParams{ChatID: r.chatID, RichMessage: InputRichMessage{Markdown: rep.Rich}, ReplyMarkup: kb}
		if id, ok := ThreadParam(r.threadID, false); ok {
			p.MessageThreadID = id
		}
		_, err := r.ch.C.SendRichMessage(r.ctx, p)
		if err == nil {
			return
		}
		r.ch.richFailure(err)
	}
	if strings.TrimSpace(rep.Text) == "" {
		return
	}
	if _, err := r.ch.sendText(r.ctx, r.chatID, r.threadID, rep.Text, sendOpts{keyboard: kb}); err != nil {
		r.ch.logger().Warn("telegram: cannot send the reply", "chat", r.chatID, "err", err.Error())
	}
}

// editReply replaces the message of the pressed button with rep. An edit to the same text
// is a success. It returns false when Telegram refuses the edit, for example because the
// message is too old. Then the caller sends a new message.
func (r *tgRenderer) editReply(rep gateway.Reply, kb *InlineKeyboardMarkup) bool {
	p := EditMessageTextParams{ChatID: r.chatID, MessageID: r.editMsgID, ReplyMarkup: kb}
	var richErr error
	if rep.Rich != "" {
		p.RichMessage = &InputRichMessage{Markdown: rep.Rich}
		_, err := r.ch.C.EditMessageText(r.ctx, p)
		if err == nil || isNotModified(err) {
			return true
		}
		richErr, p.RichMessage = err, nil
	}
	if strings.TrimSpace(rep.Text) == "" {
		return false
	}
	parts := finalParts(rep.Text)
	if len(parts) > 1 {
		// One message cannot hold the text.
		return false
	}
	p.Text, p.ParseMode = parts[0], "MarkdownV2"
	_, err := r.ch.C.EditMessageText(r.ctx, p)
	if err != nil && isBadRequest(err) && !isNotModified(err) {
		p.Text, p.ParseMode = StripMarkdownV2(parts[0]), ""
		_, err = r.ch.C.EditMessageText(r.ctx, p)
	}
	if err != nil && !isNotModified(err) {
		r.ch.logger().Debug("telegram: cannot edit the reply, send a new message", "chat", r.chatID, "err", err.Error())
		return false
	}
	// The text edit worked, so the rich edit failed because of the rich message.
	if richErr != nil {
		r.ch.richFailure(richErr)
	}
	return true
}

// richFailure logs a failed rich message. Only the first failure logs at the warn level, so
// a Bot API server without rich messages does not fill the log.
func (ch *Channel) richFailure(err error) {
	if ch.richFailed.CompareAndSwap(false, true) {
		ch.logger().Warn("telegram: the rich message failed, send the text reply", "err", err.Error())
		return
	}
	ch.logger().Debug("telegram: the rich message failed, send the text reply", "err", err.Error())
}

// Done stops the typing loop, sends the final text, closes the status message, and sends
// the files of the answer.
func (r *tgRenderer) Done(final string, err error) {
	r.mu.Lock()
	if r.done {
		r.mu.Unlock()
		return
	}
	r.done = true
	stop := r.stopTyping
	cur, prev := r.cur.String(), r.prev.String()
	r.mu.Unlock()
	r.ch.clearActive(r)
	// Stop the typing action first, so it does not show again after the answer.
	if r.handoff != nil {
		r.handoff()
	}
	if stop != nil {
		stop()
	}
	// After a tool round with no new text, the final can be the text of the closed segment,
	// which the chat shows already.
	if cur == "" && prev != "" {
		if rest, ok := strings.CutPrefix(final, prev); ok {
			final = strings.TrimSpace(rest)
		}
	}
	if final == "" {
		final = cur
	}
	clean, files := r.ch.Media.ExtractMedia(final)
	if err != nil && !errors.Is(err, context.Canceled) {
		clean = strings.TrimSpace(clean + "\n\nerror: " + err.Error())
	}
	// An empty text makes the streamer use the streamed text without the media tags.
	if ferr := r.stream.Finish(r.ctx, clean); ferr != nil {
		r.ch.logger().Warn("telegram: cannot send the answer", "chat", r.chatID, "err", ferr.Error())
	}
	r.status.Close(r.ctx, err == nil)
	if len(files) > 0 && r.ch.Out != nil {
		if serr := r.ch.Out.SendFiles(r.ctx, r.chatID, r.threadID, files); serr != nil {
			r.ch.logger().Warn("telegram: cannot send the files of the answer", "chat", r.chatID, "err", serr.Error())
		}
	}
}
