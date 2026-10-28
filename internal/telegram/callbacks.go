package telegram

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// The callback data prefixes of the buttons.
const (
	approvalPrefix = "ap"
	askPrefix      = "ask"
)

// maxCommandPreview is the longest command in an approval message (Hermes).
const maxCommandPreview = 3800

// The first lines of an approval message after the answer. These are Tether Agent texts.
// The approval scopes follow Hermes.
const (
	labelOnce    = "✅ **Allowed once**"
	labelSession = "✅ **Allowed in this session**"
	labelAlways  = "✅ **Always allowed**"
	labelDenied  = "❌ **Denied**"
	labelTimeout = "⌛ **Denied: no answer**"
)

// The answers to a button press that cannot act.
const (
	resolvedText     = "✔️ This prompt has an answer already."
	notAuthorized    = "⛔ Only the owner can answer this prompt."
	wrongChat        = "⛔ This button is for another chat."
	invalidCallback  = "⚠️ This button is too old. Send the command again."
	askDoneButton    = "Done"
	askNoAnswerLabel = "⏱ No answer."
)

// Interactions shows the approval buttons and the ask_user prompts in Telegram chats. It
// keeps the pending prompts until a button press, a text, or the end of the context
// answers them. It is safe for concurrent use.
type Interactions struct {
	C   *Client
	Log *slog.Logger
	// Authorized reports whether the user of a button press may answer. Nil allows every
	// user. Channel.Run sets it to the authorization check of the channel.
	Authorized func(ctx context.Context, cq CallbackQuery) bool
	// ApprovalTimeout is the time that the approval gate waits for an answer. The approval
	// message shows it. Zero leaves out the line about the time.
	ApprovalTimeout time.Duration

	mu      sync.Mutex
	pending map[string]*prompt
	seq     int64
}

// prompt is one pending question.
type prompt struct {
	id       string
	kind     string // approvalPrefix or askPrefix
	chatID   int64
	threadID string
	seq      int64 // the order of creation, for the FIFO of /approve
	msgID    int
	// text and parseMode are the sent text, for the edits of a multi-select keyboard.
	text, parseMode string
	choices         []string
	multi           bool
	picked          []bool
	// free is true for a question with no choices: the next text in the chat answers it.
	free  bool
	reply chan promptReply
}

type promptReply struct {
	scope  security.Scope
	answer tools.Answer
}

// ChatUI is the prompt UI of one chat and topic. It implements security.Prompter and
// tools.Asker, so App.NewEnv can give it to the tools of a Telegram session.
type ChatUI struct {
	it       *Interactions
	ChatID   int64
	ThreadID string
}

var (
	_ security.Prompter = (*ChatUI)(nil)
	_ tools.Asker       = (*ChatUI)(nil)
)

// For returns the prompt UI of a chat and a topic.
func (it *Interactions) For(chatID int64, threadID string) *ChatUI {
	return &ChatUI{it: it, ChatID: chatID, ThreadID: threadID}
}

// PromptApproval asks in the chat of the UI.
func (c *ChatUI) PromptApproval(ctx context.Context, req security.ApprovalRequest) (security.Scope, error) {
	return c.it.promptApproval(ctx, c.ChatID, c.ThreadID, req)
}

// Ask asks in the chat of the UI.
func (c *ChatUI) Ask(ctx context.Context, q tools.Question) (tools.Answer, error) {
	return c.it.ask(ctx, c.ChatID, c.ThreadID, q)
}

// promptApproval sends the approval message with its buttons and waits for the answer.
// After the answer, the message shows the result and loses its buttons. When ctx ends, it
// returns ScopeTimeout and the context error.
func (it *Interactions) promptApproval(ctx context.Context, chatID int64, threadID string, req security.ApprovalRequest) (security.Scope, error) {
	cmd := req.Command
	if len([]rune(cmd)) > maxCommandPreview {
		cmd = string([]rune(cmd)[:maxCommandPreview]) + "..."
	}
	desc := req.Description
	if desc == "" {
		desc = "dangerous command"
	}
	text := "🛡 **Approval needed**\nThe agent wants to run a " + desc
	if req.ChildName != "" {
		text += " (from subagent " + req.ChildName + ")"
	}
	block := "\n```\n" + cmd + "\n```"
	text += "." + block
	if it.ApprovalTimeout > 0 {
		text += "\n_No answer in " + formatWait(it.ApprovalTimeout) + " = deny._"
	}
	p := it.add(approvalPrefix, chatID, threadID)
	kb := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{{Text: "✅ Allow once", CallbackData: callbackData(approvalPrefix, p.id, "once")},
			{Text: "✅ Allow in this session", CallbackData: callbackData(approvalPrefix, p.id, "session")}},
		{{Text: "✅ Always allow", CallbackData: callbackData(approvalPrefix, p.id, "always")},
			{Text: "❌ Deny", CallbackData: callbackData(approvalPrefix, p.id, "deny")}},
	}}
	if err := it.send(ctx, p, text, kb); err != nil {
		it.remove(p.id)
		return security.ScopeDeny, fmt.Errorf("telegram approval: %w", err)
	}
	// After the answer, the message keeps the command, so the chat shows what ran.
	ectx := context.WithoutCancel(ctx)
	select {
	case r := <-p.reply:
		it.editMarkdown(ectx, p, it.approvalLabel(r.scope)+block)
		return r.scope, nil
	case <-ctx.Done():
		if !it.remove(p.id) {
			// An answer came at the same time.
			r := <-p.reply
			it.editMarkdown(ectx, p, it.approvalLabel(r.scope)+block)
			return r.scope, nil
		}
		label := labelDenied
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			label = it.approvalLabel(security.ScopeTimeout)
		}
		it.editMarkdown(ectx, p, label+block)
		return security.ScopeTimeout, ctx.Err()
	}
}

// ask shows the question with one button for each choice. A multi-select question toggles
// a ✓ mark on each pressed choice and has a Done button. With no choices, the next text in
// the chat is the answer. A Question.Timeout above zero limits the wait.
func (it *Interactions) ask(ctx context.Context, chatID int64, threadID string, q tools.Question) (tools.Answer, error) {
	if q.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, q.Timeout)
		defer cancel()
	}
	p := it.add(askPrefix, chatID, threadID)
	text := "❓ " + q.Text
	it.mu.Lock()
	p.choices, p.multi, p.free = slices.Clone(q.Choices), q.Multi && len(q.Choices) > 0, len(q.Choices) == 0
	p.picked = make([]bool, len(q.Choices))
	kb := askKeyboard(p)
	it.mu.Unlock()
	if p.free {
		text += "\n\n(Reply with a message.)"
	}
	if err := it.send(ctx, p, text, kb); err != nil {
		it.remove(p.id)
		return tools.Answer{}, fmt.Errorf("telegram ask: %w", err)
	}
	ectx := context.WithoutCancel(ctx)
	select {
	case r := <-p.reply:
		it.edit(ectx, p, text+"\n\n✅ "+answerSummary(r.answer))
		return r.answer, nil
	case <-ctx.Done():
		if !it.remove(p.id) {
			r := <-p.reply
			it.edit(ectx, p, text+"\n\n✅ "+answerSummary(r.answer))
			return r.answer, nil
		}
		it.edit(ectx, p, text+"\n\n"+askNoAnswerLabel)
		return tools.Answer{}, ctx.Err()
	}
}

// HandleCallback handles a button press. It returns false when the data is not of an
// approval or a question. It answers the callback query at once, so the button stops its
// spinner. A press from another chat or from an unauthorized user changes nothing.
func (it *Interactions) HandleCallback(ctx context.Context, cq CallbackQuery) bool {
	kind, id, arg, ok := parseCallback(cq.Data)
	if !ok {
		return false
	}
	answerCB := func(text string) {
		if err := it.C.AnswerCallbackQuery(ctx, cq.ID, text, false); err != nil {
			it.logger().Debug("telegram: cannot answer the button press", "err", err.Error())
		}
	}
	it.mu.Lock()
	p := it.pending[id]
	it.mu.Unlock()
	if p == nil || p.kind != kind {
		answerCB(resolvedText)
		return true
	}
	if cq.Message == nil || cq.Message.Chat.ID != p.chatID {
		it.logger().Warn("telegram: a button press from another chat was refused", "prompt", id)
		answerCB(wrongChat)
		return true
	}
	if it.Authorized != nil && !it.Authorized(ctx, cq) {
		it.logger().Warn("telegram: a button press from an unauthorized user was refused", "user", cq.From.ID)
		answerCB(notAuthorized)
		return true
	}
	if kind == approvalPrefix {
		scope, ok := map[string]security.Scope{
			"once": security.ScopeOnce, "session": security.ScopeSession,
			"always": security.ScopeAlways, "deny": security.ScopeDeny,
		}[arg]
		if !ok {
			answerCB(invalidCallback)
			return true
		}
		// A pop-up shows plain text, so it gets the label without the bold markers.
		answerCB(strings.ReplaceAll(it.approvalLabel(scope), "**", ""))
		if !it.deliver(id, promptReply{scope: scope}) {
			it.logger().Info("telegram: the approval was answered already", "prompt", id)
		}
		return true
	}
	it.askCallback(ctx, p, arg, answerCB)
	return true
}

// askCallback applies a press on a question button.
func (it *Interactions) askCallback(ctx context.Context, p *prompt, arg string, answerCB func(string)) {
	it.mu.Lock()
	if it.pending[p.id] != p {
		it.mu.Unlock()
		answerCB(resolvedText)
		return
	}
	if arg == "done" && p.multi {
		var picked []string
		for i, ok := range p.picked {
			if ok {
				picked = append(picked, p.choices[i])
			}
		}
		it.deliverLocked(p.id, promptReply{answer: tools.Answer{Choices: picked}})
		it.mu.Unlock()
		answerCB("")
		return
	}
	i, err := strconv.Atoi(arg)
	if err != nil || i < 0 || i >= len(p.choices) {
		it.mu.Unlock()
		answerCB(invalidCallback)
		return
	}
	if !p.multi {
		it.deliverLocked(p.id, promptReply{answer: tools.Answer{Choices: []string{p.choices[i]}}})
		it.mu.Unlock()
		answerCB("")
		return
	}
	p.picked[i] = !p.picked[i]
	kb := askKeyboard(p)
	params := EditMessageTextParams{ChatID: p.chatID, MessageID: p.msgID, Text: p.text, ParseMode: p.parseMode, ReplyMarkup: kb}
	it.mu.Unlock()
	answerCB("")
	if _, err := it.C.EditMessageText(ctx, params); err != nil && !isNotModified(err) {
		it.logger().Info("telegram: cannot update the choice marks", "err", err.Error())
	}
}

// ResolveApprovals answers the pending approvals of a chat with scope, for the /approve and
// /deny text fallbacks. It answers the oldest one, or all of them with all. It returns the
// number of answered approvals.
func (it *Interactions) ResolveApprovals(chatID int64, scope security.Scope, all bool) int {
	it.mu.Lock()
	defer it.mu.Unlock()
	var list []*prompt
	for _, p := range it.pending {
		if p.kind == approvalPrefix && p.chatID == chatID {
			list = append(list, p)
		}
	}
	slices.SortFunc(list, func(a, b *prompt) int { return int(a.seq - b.seq) })
	if !all && len(list) > 1 {
		list = list[:1]
	}
	for _, p := range list {
		it.deliverLocked(p.id, promptReply{scope: scope})
	}
	return len(list)
}

// TakeText gives text to the oldest open question with no choices in the chat and the
// topic. It returns false when no such question waits, so the text goes to the agent.
func (it *Interactions) TakeText(chatID int64, threadID, text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	it.mu.Lock()
	defer it.mu.Unlock()
	var oldest *prompt
	for _, p := range it.pending {
		if p.kind == askPrefix && p.free && p.chatID == chatID && p.threadID == threadID &&
			(oldest == nil || p.seq < oldest.seq) {
			oldest = p
		}
	}
	if oldest == nil {
		return false
	}
	return it.deliverLocked(oldest.id, promptReply{answer: tools.Answer{Text: strings.TrimSpace(text)}})
}

// add makes a new pending prompt with a random id of 8 bytes.
func (it *Interactions) add(kind string, chatID int64, threadID string) *prompt {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on the supported systems. A time value keeps the id unique.
		copy(b[:], strconv.FormatInt(time.Now().UnixNano(), 16))
	}
	it.mu.Lock()
	defer it.mu.Unlock()
	if it.pending == nil {
		it.pending = map[string]*prompt{}
	}
	it.seq++
	p := &prompt{id: hex.EncodeToString(b[:]), kind: kind, chatID: chatID, threadID: threadID, seq: it.seq,
		reply: make(chan promptReply, 1)}
	it.pending[p.id] = p
	return p
}

// remove removes a pending prompt. It returns false when an answer took it first.
func (it *Interactions) remove(id string) bool {
	it.mu.Lock()
	defer it.mu.Unlock()
	if _, ok := it.pending[id]; !ok {
		return false
	}
	delete(it.pending, id)
	return true
}

func (it *Interactions) deliver(id string, r promptReply) bool {
	it.mu.Lock()
	defer it.mu.Unlock()
	return it.deliverLocked(id, r)
}

// deliverLocked gives r to the waiter of the prompt id and removes the prompt. The caller
// holds it.mu. The reply channel has room for one answer, so the send never blocks.
func (it *Interactions) deliverLocked(id string, r promptReply) bool {
	p, ok := it.pending[id]
	if !ok {
		return false
	}
	delete(it.pending, id)
	p.reply <- r
	return true
}

// send sends the prompt message as MarkdownV2, or as plain text when Telegram rejects the
// markup.
func (it *Interactions) send(ctx context.Context, p *prompt, text string, kb *InlineKeyboardMarkup) error {
	params := SendMessageParams{ChatID: p.chatID, Text: cutUTF16(ToMarkdownV2(text), maxMessageLen), ParseMode: "MarkdownV2", ReplyMarkup: kb}
	if id, ok := ThreadParam(p.threadID, false); ok {
		params.MessageThreadID = id
	}
	m, err := it.C.SendMessage(ctx, params)
	if err != nil && isBadRequest(err) {
		params.Text, params.ParseMode = cutUTF16(text, maxMessageLen), ""
		m, err = it.C.SendMessage(ctx, params)
	}
	if err != nil {
		return err
	}
	it.mu.Lock()
	p.msgID, p.text, p.parseMode = m.MessageID, params.Text, params.ParseMode
	it.mu.Unlock()
	return nil
}

// editMarkdown replaces the text of the prompt message with md as MarkdownV2 and removes
// its buttons. When Telegram rejects the markup, it sends the text without the markup.
func (it *Interactions) editMarkdown(ctx context.Context, p *prompt, md string) {
	it.mu.Lock()
	msgID := p.msgID
	it.mu.Unlock()
	if msgID == 0 {
		return
	}
	formatted := cutUTF16(ToMarkdownV2(md), maxMessageLen)
	params := EditMessageTextParams{ChatID: p.chatID, MessageID: msgID, Text: formatted, ParseMode: "MarkdownV2"}
	_, err := it.C.EditMessageText(ctx, params)
	if err != nil && isBadRequest(err) && !isNotModified(err) {
		params.Text, params.ParseMode = StripMarkdownV2(formatted), ""
		_, err = it.C.EditMessageText(ctx, params)
	}
	if err != nil && !isNotModified(err) {
		it.logger().Info("telegram: cannot edit the prompt message", "err", err.Error())
	}
}

// edit replaces the text of the prompt message with plain text and removes its buttons.
func (it *Interactions) edit(ctx context.Context, p *prompt, text string) {
	it.mu.Lock()
	msgID := p.msgID
	it.mu.Unlock()
	if msgID == 0 {
		return
	}
	params := EditMessageTextParams{ChatID: p.chatID, MessageID: msgID, Text: cutUTF16(text, maxMessageLen)}
	if _, err := it.C.EditMessageText(ctx, params); err != nil && !isNotModified(err) {
		it.logger().Info("telegram: cannot edit the prompt message", "err", err.Error())
	}
}

func (it *Interactions) logger() *slog.Logger {
	if it.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return it.Log
}

// askKeyboard returns the buttons of a question: one row for each choice, and Done for a
// multi-select question. The caller holds it.mu.
func askKeyboard(p *prompt) *InlineKeyboardMarkup {
	if len(p.choices) == 0 {
		return nil
	}
	kb := &InlineKeyboardMarkup{}
	for i, c := range p.choices {
		label := c
		if p.multi && p.picked[i] {
			label = "✓ " + c
		}
		kb.InlineKeyboard = append(kb.InlineKeyboard, []InlineKeyboardButton{
			{Text: label, CallbackData: callbackData(askPrefix, p.id, strconv.Itoa(i))}})
	}
	if p.multi {
		kb.InlineKeyboard = append(kb.InlineKeyboard, []InlineKeyboardButton{
			{Text: askDoneButton, CallbackData: callbackData(askPrefix, p.id, "done")}})
	}
	return kb
}

func callbackData(kind, id, arg string) string { return kind + ":" + id + ":" + arg }

// parseCallback splits "<kind>:<id>:<arg>" callback data.
func parseCallback(data string) (kind, id, arg string, ok bool) {
	parts := strings.SplitN(data, ":", 3)
	if len(parts) != 3 || (parts[0] != approvalPrefix && parts[0] != askPrefix) || parts[1] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// approvalLabel returns the first line of an approval message after the answer. The
// timeout line names the wait when ApprovalTimeout is set.
func (it *Interactions) approvalLabel(s security.Scope) string {
	switch s {
	case security.ScopeOnce:
		return labelOnce
	case security.ScopeSession:
		return labelSession
	case security.ScopeAlways:
		return labelAlways
	case security.ScopeTimeout:
		if it.ApprovalTimeout > 0 {
			return "⌛ **Denied: no answer in " + formatWait(it.ApprovalTimeout) + "**"
		}
		return labelTimeout
	}
	return labelDenied
}

// formatWait returns a wait as whole minutes ("10 min"), or as seconds ("45 s") under a
// minute.
func formatWait(d time.Duration) string {
	if d < time.Minute {
		return strconv.Itoa(int(d/time.Second)) + " s"
	}
	return strconv.Itoa(int(d/time.Minute)) + " min"
}

// answerSummary returns the answer of a question as one line.
func answerSummary(a tools.Answer) string {
	if len(a.Choices) > 0 {
		return strings.Join(a.Choices, ", ")
	}
	if a.Text == "" {
		return "(nothing selected)"
	}
	return a.Text
}

// resolveReply returns the reply to /approve or /deny that answered n approvals. These are
// Tether Agent texts. The scopes and the oldest-first order follow Hermes.
func resolveReply(name string, scope security.Scope, n int) string {
	switch {
	case n == 0:
		return noPendingApproval
	case name == "deny" && n == 1:
		return labelDenied + "\nThe agent continues without the command."
	case name == "deny":
		return fmt.Sprintf("❌ **Denied %d commands**\nThe agent continues without them.", n)
	case n > 1:
		return fmt.Sprintf("✅ **Allowed %d commands**\nThe agent continues.", n)
	}
	label := labelOnce
	switch scope {
	case security.ScopeSession:
		label = labelSession
	case security.ScopeAlways:
		label = labelAlways
	}
	return label + "\nThe agent continues."
}
