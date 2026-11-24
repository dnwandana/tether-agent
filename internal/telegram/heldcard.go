package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/dnwandana/tether-agent/internal/cron"
)

// heldPrefix is the callback data prefix of the buttons of a held card.
const heldPrefix = "hs"

// heldCardMaxRunes is the longest delivery text in a held card.
const heldCardMaxRunes = 3500

// The texts of a held card.
const (
	heldCutLine      = "(cut; the full text goes to the target)"
	heldDropButton   = "❌ Drop"
	heldPostButton   = "✅ Post the full text there"
	heldReadFailed   = "⚠️ The held text cannot be read now. Try again."
	heldKindOriginal = "original"
)

// HeldCard is one owner card. Kind "hold" is a delivery that model_guard held. Kind
// "original" is the note of a masked cron delivery: Text is then the full note text.
type HeldCard struct {
	ID, JobName, TargetTitle, TargetKind, Reason, Text string
	Sources                                            []string
	Kind                                               string
}

// HeldCards posts the owner cards of the held sends and handles their taps. Send sends a
// held row to its target. It scans a row of kind "hold" and sends a row of kind "original" as is.
type HeldCards struct {
	C   *Client
	Log *slog.Logger
	// Jobs is the store of the held rows and the run rows. Nil answers each tap as resolved.
	Jobs *cron.Store
	Send func(ctx context.Context, h cron.HeldSend) error
	// OnResolved runs after a tap resolves a held send. Nil does nothing.
	OnResolved func(id string)
}

func (hc *HeldCards) logger() *slog.Logger {
	if hc.Log != nil {
		return hc.Log
	}
	return slog.Default()
}

// heldKeyboard returns the buttons of a card. A card of kind "original" has one button.
func heldKeyboard(id, kind, title string) *InlineKeyboardMarkup {
	send := callbackData(heldPrefix, id, "s")
	if kind == heldKindOriginal {
		return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
			{{Text: heldPostButton, CallbackData: send}}}}
	}
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{{Text: heldDropButton, CallbackData: callbackData(heldPrefix, id, "d")}},
		{{Text: "✅ Send to \"" + title + "\"", CallbackData: send}},
	}}
}

// heldCardText builds the plain text of a card.
func heldCardText(card HeldCard) string {
	if card.Kind == heldKindOriginal {
		return card.Text
	}
	text := card.Text
	cut := false
	if r := []rune(text); len(r) > heldCardMaxRunes {
		text = string(r[:heldCardMaxRunes])
		cut = true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "⏸️ Held: job \"%s\"\n", card.JobName)
	fmt.Fprintf(&b, "To: \"%s\" (%s)\n", card.TargetTitle, card.TargetKind)
	fmt.Fprintf(&b, "Guard: %s\n", card.Reason)
	if len(card.Sources) > 0 {
		fmt.Fprintf(&b, "Read: %s\n", strings.Join(card.Sources, ", "))
	}
	b.WriteString("—\n")
	b.WriteString(text)
	if cut {
		b.WriteString("\n")
		b.WriteString(heldCutLine)
	}
	return b.String()
}

// Post sends the card to chatID as plain text with its buttons, and returns the message id.
// The card has no parse mode, because Markdown in a web text can make the send fail.
func (hc *HeldCards) Post(ctx context.Context, chatID int64, card HeldCard) (msgID int, err error) {
	m, err := hc.C.SendMessage(ctx, SendMessageParams{
		ChatID:      chatID,
		Text:        heldCardText(card),
		ReplyMarkup: heldKeyboard(card.ID, card.Kind, card.TargetTitle),
	})
	if err != nil {
		return 0, err
	}
	return m.MessageID, nil
}

// edit replaces the text of the card of a tap. A nil kb removes the buttons.
func (hc *HeldCards) edit(ctx context.Context, cq CallbackQuery, text string, kb *InlineKeyboardMarkup) {
	if cq.Message == nil {
		return
	}
	if _, err := replyClient(hc.C).EditMessageText(ctx, EditMessageTextParams{
		ChatID: cq.Message.Chat.ID, MessageID: cq.Message.MessageID, Text: text, ReplyMarkup: kb}); err != nil {
		hc.logger().Debug("telegram: cannot edit the held card", "err", err.Error())
	}
}

// tap applies a tap with the arg "s" or "d" on the card of the row id. The caller checked
// that an owner tapped.
func (hc *HeldCards) tap(ctx context.Context, cq CallbackQuery, id, arg string) {
	answer := func(text string) {
		if err := hc.C.AnswerCallbackQuery(ctx, cq.ID, text, false); err != nil {
			hc.logger().Debug("telegram: cannot answer the button press", "err", err.Error())
		}
	}
	if hc.Jobs == nil {
		answer(resolvedText)
		return
	}
	h, ok, err := (&cron.HeldStore{DB: hc.Jobs.DB}).Get(ctx, id)
	if err != nil {
		hc.logger().Warn("telegram: cannot read a held send", "id", id, "err", err.Error())
		answer(heldReadFailed)
		return
	}
	if !ok {
		answer(resolvedText)
		return
	}
	if cq.Message == nil || strconv.FormatInt(cq.Message.Chat.ID, 10) != h.CardChat {
		answer(wrongChat)
		return
	}
	var action string
	switch arg {
	case "s":
		action = "post"
	case "d":
		action = "drop"
	default:
		answer(invalidCallback)
		return
	}
	answer("")
	result, err := cron.ResolveHeld(ctx, hc.Jobs, id, action, hc.Send, func(_ cron.HeldSend, result string) {
		hc.edit(ctx, cq, result, nil)
	})
	switch {
	case err == nil:
		if hc.OnResolved != nil {
			hc.OnResolved(id)
		}
	case errors.Is(err, cron.ErrHeldNotSent):
		hc.edit(ctx, cq, result+" Tap Send to try again.", heldKeyboard(h.ID, h.Kind, h.TargetTitle))
	case errors.Is(err, cron.ErrHeldGone):
	default:
		hc.logger().Warn("telegram: cannot finish a held send answer", "id", id, "err", err.Error())
	}
}

// heldCallback handles an "hs:" tap. It returns false for other data.
func (ch *Channel) heldCallback(ctx context.Context, cq CallbackQuery) bool {
	kind, id, arg, ok := parseCallback(cq.Data)
	if !ok || kind != heldPrefix {
		return false
	}
	answer := func(text string) {
		if err := ch.C.AnswerCallbackQuery(ctx, cq.ID, text, false); err != nil {
			ch.logger().Debug("telegram: cannot answer the button press", "err", err.Error())
		}
	}
	if !ch.callbackOwner(ctx, cq) {
		ch.logger().Warn("telegram: a button press from a user that is not an owner was refused", "user", cq.From.ID)
		answer(notAuthorized)
		return true
	}
	if ch.HeldCards == nil {
		answer(resolvedText)
		return true
	}
	ch.HeldCards.tap(ctx, cq, id, arg)
	return true
}
