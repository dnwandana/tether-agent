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
	heldNoSendFunc   = "no send function is set"
	heldKindOriginal = "original"
)

// HeldSendStore is the part of cron.HeldStore that the taps of a held card use. MarkRun
// sets the delivery of the cron_runs row of the held send.
type HeldSendStore interface {
	Claim(ctx context.Context, id string) (cron.HeldSend, bool, error)
	Restore(ctx context.Context, h cron.HeldSend) error
	MarkRun(ctx context.Context, runID int64, delivery string) error
}

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
	C     *Client
	Log   *slog.Logger
	Store HeldSendStore
	Send  func(ctx context.Context, h cron.HeldSend) error
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
	if hc.Store == nil {
		answer(resolvedText)
		return
	}
	h, ok, err := hc.Store.Claim(ctx, id)
	if err != nil {
		hc.logger().Warn("telegram: cannot claim a held send", "id", id, "err", err.Error())
		answer(heldReadFailed)
		return
	}
	if !ok {
		answer(resolvedText)
		return
	}
	restore := func() {
		if err := hc.Store.Restore(ctx, h); err != nil {
			hc.logger().Error("telegram: cannot put a held send back", "id", id, "err", err.Error())
		}
	}
	if cq.Message == nil || strconv.FormatInt(cq.Message.Chat.ID, 10) != h.CardChat {
		restore()
		answer(wrongChat)
		return
	}
	mark := func(delivery string) {
		if h.Kind != "hold" || h.RunID <= 0 {
			return
		}
		if err := hc.Store.MarkRun(ctx, h.RunID, delivery); err != nil {
			hc.logger().Warn("telegram: cannot mark the run of a held send", "id", id, "err", err.Error())
		}
	}
	switch arg {
	case "s":
		answer("")
		send := hc.Send
		if send == nil {
			send = func(context.Context, cron.HeldSend) error { return errors.New(heldNoSendFunc) }
		}
		if err := send(ctx, h); err != nil {
			restore()
			hc.edit(ctx, cq, "Not sent: "+err.Error()+". Tap Send to try again.",
				heldKeyboard(h.ID, h.Kind, h.TargetTitle))
			return
		}
		mark(cron.DeliverySent)
		done := "Sent."
		if h.Kind == heldKindOriginal {
			done = "Posted."
		}
		hc.edit(ctx, cq, done, nil)
	case "d":
		answer("")
		mark(cron.DeliveryDropped)
		hc.edit(ctx, cq, "Dropped.", nil)
	default:
		restore()
		answer(invalidCallback)
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
