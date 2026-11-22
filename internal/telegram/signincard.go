package telegram

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"time"
)

// signInPrefix is the callback data prefix of the buttons of a dashboard sign-in card.
const signInPrefix = "si"

// The texts of a dashboard sign-in card.
const (
	signInHead     = "🔑 New dashboard sign-in"
	signInAdvice   = "Compare the code with the code on the device. Allow only a device that you hold."
	signInDenyBtn  = "❌ Deny"
	signInAllowBtn = "✅ Allow"
	signInNoChat   = "telegram: no private owner chat for the dashboard sign-in card"
)

// signInCard is one sign-in request with its card in each owner chat.
type signInCard struct {
	label, ip string
	msgs      []sentCard // the card in each owner chat
	answer    func(allow bool)
	state     func() string // the final state of the request; nil when unknown
	expires   time.Time
	closed    bool
	done      chan struct{} // closed when the card is answered or closed
}

// sentCard is one sent card.
type sentCard struct {
	chatID int64
	msgID  int
}

// signInKeyboard returns the buttons of a card. The Deny button is alone on row 1.
func (ch *Channel) signInKeyboard(id string) *InlineKeyboardMarkup {
	allow := signInAllowBtn
	if ch.SignInHours > 0 {
		allow += " for " + strconv.Itoa(ch.SignInHours) + " h"
	}
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{{Text: signInDenyBtn, CallbackData: callbackData(signInPrefix, id, "d")}},
		{{Text: allow, CallbackData: callbackData(signInPrefix, id, "a")}},
	}}
}

// SendSignInCard sends a sign-in card to the private chat of each owner. The first tap
// calls answer once. The card closes when ctx ends or expires comes, and answer is then not
// called. The card is plain text, because the label comes from the browser.
func (ch *Channel) SendSignInCard(ctx context.Context, label, ip, code string, expires time.Time, answer func(allow bool)) error {
	return ch.SendSignInCardState(ctx, label, ip, code, expires, answer, nil)
}

// SendSignInCardState is SendSignInCard with the state of the request. Another notifier can
// answer first, so after a tap, or when ctx ends, the card shows the state that state gives:
// allowed, denied, or else closed. A nil state gives the tap result and "closed".
func (ch *Channel) SendSignInCardState(ctx context.Context, label, ip, code string, expires time.Time,
	answer func(allow bool), state func() string) error {
	chats := ch.ownerPrivateChats(ctx)
	if len(chats) == 0 {
		return errors.New(signInNoChat)
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	id := hex.EncodeToString(b[:])
	card := &signInCard{label: label, ip: ip, expires: expires, answer: answer, state: state, done: make(chan struct{})}
	ch.signInMu.Lock()
	if ch.signIns == nil {
		ch.signIns = map[string]*signInCard{}
	}
	ch.signIns[id] = card
	ch.signInMu.Unlock()

	text := signInHead + " · " + label + " · " + ip + " · code " + code + "\n" + signInAdvice
	kb := ch.signInKeyboard(id)
	var errs []error
	for _, chat := range chats {
		m, err := replyClient(ch.C).SendMessage(ctx, SendMessageParams{ChatID: chat, Text: text, ReplyMarkup: kb})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ch.signInMu.Lock()
		card.msgs = append(card.msgs, sentCard{chatID: chat, msgID: m.MessageID})
		ch.signInMu.Unlock()
	}
	ch.signInMu.Lock()
	sent := len(card.msgs)
	if sent == 0 {
		delete(ch.signIns, id)
	}
	ch.signInMu.Unlock()
	if sent == 0 {
		return errors.Join(errs...)
	}
	go func() {
		timer := time.NewTimer(time.Until(expires))
		defer timer.Stop()
		select {
		case <-card.done:
			return
		case <-ctx.Done():
		case <-timer.C:
		}
		ch.signInMu.Lock()
		if card.closed {
			ch.signInMu.Unlock()
			return
		}
		card.closed = true
		close(card.done)
		delete(ch.signIns, id)
		ch.signInMu.Unlock()
		result, _ := signInResult("")
		if card.state != nil {
			result, _ = signInResult(card.state())
		}
		ch.closeSignIn(context.WithoutCancel(ctx), card, result+" · "+card.label+" · "+card.ip)
	}()
	return nil
}

// ownerPrivateChats returns the numeric, positive allowed_users entries and the approved
// paired users, in that order, with no duplicates.
func (ch *Channel) ownerPrivateChats(ctx context.Context) []int64 {
	var ids []int64
	for _, s := range trimmed(ch.Cfg.AllowedUsers) {
		if id, err := strconv.ParseInt(s, 10, 64); err == nil && id > 0 && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	for _, id := range ch.pairedUserIDs(ctx) {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// signInCallback handles an "si:" tap. It returns false for other data.
func (ch *Channel) signInCallback(ctx context.Context, cq CallbackQuery) bool {
	kind, id, arg, ok := parseCallback(cq.Data)
	if !ok || kind != signInPrefix {
		return false
	}
	answerCB := func(text string) {
		if err := ch.C.AnswerCallbackQuery(ctx, cq.ID, text, false); err != nil {
			ch.logger().Debug("telegram: cannot answer the button press", "err", err.Error())
		}
	}
	// Only the owner in the own private chat can answer.
	if cq.Message == nil || cq.Message.Chat.ID != cq.From.ID || !ch.IsOwnerPrivateChat(ctx, cq.Message.Chat.ID) {
		ch.logger().Warn("telegram: a sign-in card tap from outside a private owner chat was refused", "user", cq.From.ID)
		answerCB(notAuthorized)
		return true
	}
	if arg != "a" && arg != "d" {
		answerCB(invalidCallback)
		return true
	}
	ch.signInMu.Lock()
	card := ch.signIns[id]
	if card == nil || card.closed || (!card.expires.IsZero() && !time.Now().Before(card.expires)) {
		ch.signInMu.Unlock()
		answerCB(resolvedText)
		return true
	}
	if !slices.ContainsFunc(card.msgs, func(m sentCard) bool { return m.chatID == cq.Message.Chat.ID }) {
		ch.signInMu.Unlock()
		answerCB(wrongChat)
		return true
	}
	card.closed = true
	close(card.done)
	delete(ch.signIns, id)
	ch.signInMu.Unlock()

	allow := arg == "a"
	if card.answer != nil {
		card.answer(allow)
	}
	final := "denied"
	if allow {
		final = "allowed"
	}
	// An earlier answer of the dashboard wins over this tap.
	if card.state != nil {
		final = card.state()
	}
	result, popup := signInResult(final)
	answerCB(popup)
	ch.closeSignIn(ctx, card, result+" · "+card.label+" · "+card.ip)
	return true
}

// signInResult returns the card text and the popup text for the state of a request.
func signInResult(state string) (result, popup string) {
	switch state {
	case "allowed":
		return "✅ Dashboard sign-in allowed", "✅ Allowed"
	case "denied":
		return "❌ Dashboard sign-in denied", "❌ Denied"
	}
	return "⌛ Dashboard sign-in closed", resolvedText
}

// closeSignIn edits each card of card to text with no buttons.
func (ch *Channel) closeSignIn(ctx context.Context, card *signInCard, text string) {
	ch.signInMu.Lock()
	msgs := slices.Clone(card.msgs)
	ch.signInMu.Unlock()
	for _, m := range msgs {
		if _, err := replyClient(ch.C).EditMessageText(ctx, EditMessageTextParams{
			ChatID: m.chatID, MessageID: m.msgID, Text: text}); err != nil {
			ch.logger().Debug("telegram: cannot edit the sign-in card", "err", err.Error())
		}
	}
}
