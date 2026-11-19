package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// secretCardHead is the first part of each secret card.
const secretCardHead = "🔑 Secret needed: "

// secretCardTimeout is the wait of a card when SecretCards.Timeout is zero.
const secretCardTimeout = 5 * time.Minute

// ErrNoPrivateChat is the error when the bot cannot write to the owner in a private chat.
//
//lint:ignore ST1005 the text is a fixed user message with full sentences.
var ErrNoPrivateChat = errors.New("I cannot write to the owner in a private chat. Ask the owner to start a private chat with me, then try again.")

// SecretCards sends the ForceReply cards of request_secret and /secrets set, and waits for
// the value. It is safe for concurrent use.
type SecretCards struct {
	C *Client
	// DB keeps the open card of each chat in kv, so a restart can still take a pasted value.
	// Nil keeps the cards in memory only.
	DB  *db.DB
	Log *slog.Logger
	// Timeout is the wait of a card. Zero uses 5 min.
	Timeout time.Duration
	// Save saves a value to .env. Nil makes each save fail with "no save function".
	Save func(name, value string) error

	mu    sync.Mutex
	cards map[int64]*secretCard // by chat id
	// rows holds the end time of the kv row of each chat that Take read or that a card wrote.
	// A zero time means that the chat has no row.
	rows map[int64]time.Time
	// skills holds the slugs of the skill commands of the bot menu.
	skills map[string]bool
}

// secretCard is one waiting card.
type secretCard struct {
	name    string
	ownerID string
	msgID   int
	// done gets the result of the card one time: nil after a save, or the save error.
	done chan error
	// lastErr is the text of the last failed save. Ask returns it at the timeout.
	lastErr string
}

// cardText returns the text of the card of q.
func cardText(q tools.SecretQuestion) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s\n", secretCardHead, q.Name)
	if len(q.Commands) > 0 {
		fmt.Fprintf(&b, "For: %s\n", strings.Join(q.Commands, ", "))
	}
	if q.Reason != "" {
		fmt.Fprintf(&b, "Reason: %s\n", q.Reason)
	}
	b.WriteString("\nReply to this message with the value. I delete your reply and save the value to .env. The model never sees it.\nWaits 5 min.")
	return b.String()
}

func cardKey(chatID int64) string { return "secret_card:" + strconv.FormatInt(chatID, 10) }

// saveCard writes the open card to kv.
func (sc *SecretCards) saveCard(ctx context.Context, chatID int64, name string, msgID int, end time.Time) {
	if sc.DB == nil {
		return
	}
	v := fmt.Sprintf("%s\t%d\t%d", name, msgID, end.UnixMilli())
	err := sc.DB.KVSet(ctx, cardKey(chatID), v)
	if err != nil && sc.Log != nil {
		sc.Log.Warn("secret card row not saved", "err", err)
	}
	sc.cacheRow(chatID, end, err == nil)
}

// cacheRow sets the cached end time of the kv row of a chat. When the kv write failed, it
// removes the cached value, so the next rowEnd reads the row again.
func (sc *SecretCards) cacheRow(chatID int64, end time.Time, written bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if !written {
		delete(sc.rows, chatID)
		return
	}
	if sc.rows == nil {
		sc.rows = map[int64]time.Time{}
	}
	sc.rows[chatID] = end
}

// clearCard marks the card of a chat as closed in kv. It does not use the context of the
// card, because that context can be done already.
func (sc *SecretCards) clearCard(chatID int64) {
	if sc.DB == nil {
		return
	}
	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := sc.DB.KVSet(cctx, cardKey(chatID), "")
	if err != nil && sc.Log != nil {
		sc.Log.Warn("secret card row not cleared", "err", err)
	}
	sc.cacheRow(chatID, time.Time{}, err == nil)
}

// dropRow clears the kv row of a chat after a take through the row, so the next message of
// the owner reaches the agent. A waiting card in memory owns the row, so the row then stays.
func (sc *SecretCards) dropRow(chatID int64) {
	sc.mu.Lock()
	live := sc.cards[chatID] != nil
	sc.mu.Unlock()
	if !live {
		sc.clearCard(chatID)
	}
}

// setSkillCommands keeps the slugs of the skill commands of the bot menu. While a card
// waits, a skill command passes to the gateway like a command of the command table.
func (sc *SecretCards) setSkillCommands(cmds []gateway.SkillCommand) {
	skills := make(map[string]bool, len(cmds))
	for _, c := range cmds {
		skills[c.Slug] = true
	}
	sc.mu.Lock()
	sc.skills = skills
	sc.mu.Unlock()
}

// Ask sends the card of q to the chat and waits for the value. It returns true after the
// value is saved. A second Ask for the same chat replaces the first card, and the first Ask
// returns (false, nil).
func (sc *SecretCards) Ask(ctx context.Context, chatID int64, ownerID string, q tools.SecretQuestion) (saved bool, err error) {
	msg, err := sc.C.replyClientSend(ctx, chatID, cardText(q))
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Code == 403 {
			return false, ErrNoPrivateChat
		}
		return false, err
	}
	timeout := sc.Timeout
	if timeout <= 0 {
		timeout = secretCardTimeout
	}
	card := &secretCard{name: q.Name, ownerID: ownerID, msgID: msg.MessageID, done: make(chan error, 1)}
	sc.mu.Lock()
	if sc.cards == nil {
		sc.cards = map[int64]*secretCard{}
	}
	old := sc.cards[chatID]
	sc.cards[chatID] = card
	sc.mu.Unlock()
	if old != nil {
		// The replaced card ends with no save.
		select {
		case old.done <- errReplaced:
		default:
		}
	}
	sc.saveCard(ctx, chatID, q.Name, card.msgID, time.Now().Add(timeout))
	defer func() {
		sc.mu.Lock()
		own := sc.cards[chatID] == card
		if own {
			delete(sc.cards, chatID)
		}
		sc.mu.Unlock()
		if own {
			sc.clearCard(chatID)
		}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case res := <-card.done:
			if res == nil {
				return true, nil
			}
			if errors.Is(res, errReplaced) {
				return false, nil
			}
			// A failed save keeps the card open until the timeout.
			sc.mu.Lock()
			card.lastErr = res.Error()
			sc.mu.Unlock()
		case <-timer.C:
			sc.mu.Lock()
			last := card.lastErr
			sc.mu.Unlock()
			if last != "" {
				return false, errors.New(last)
			}
			return false, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

// errReplaced ends a card that a newer card for the same chat replaced.
var errReplaced = errors.New("card replaced")

// replyClientSend sends a card as a direct reply to the owner.
func (c *Client) replyClientSend(ctx context.Context, chatID int64, text string) (Message, error) {
	return replyClient(c).SendForceReply(ctx, chatID, text, "Paste the value")
}

// AskSecret sends the card of q to the owner's private chat and waits for the value. The
// value goes to .env through the save function of the channel and never comes back.
func (c *ChatUI) AskSecret(ctx context.Context, q tools.SecretQuestion) (bool, error) {
	if c.it.Secrets == nil || c.it.OwnerChat == nil {
		return false, noOwnerChatError(q.Name)
	}
	chatID, ok := c.it.OwnerChat(ctx, c.UserID)
	if !ok {
		return false, noOwnerChatError(q.Name)
	}
	return c.it.Secrets.Ask(ctx, chatID, strconv.FormatInt(chatID, 10), q)
}

func noOwnerChatError(name string) error {
	//lint:ignore ST1005 the text is a fixed user message with full sentences.
	return fmt.Errorf("I have no private chat with the owner. Ask the owner to send /secrets set %s.", name)
}

// rowEnd returns the end time of the kv row of a chat and the name in it. It reads the row
// one time for each chat and keeps the result in memory.
func (sc *SecretCards) rowEnd(ctx context.Context, chatID int64) (string, time.Time) {
	sc.mu.Lock()
	end, seen := sc.rows[chatID]
	sc.mu.Unlock()
	if seen && end.IsZero() {
		return "", time.Time{}
	}
	if sc.DB == nil {
		return "", time.Time{}
	}
	v, ok, err := sc.DB.KVGet(ctx, cardKey(chatID))
	var name string
	end = time.Time{}
	if err == nil && ok && v != "" {
		parts := strings.Split(v, "\t")
		if len(parts) == 3 {
			if ms, perr := strconv.ParseInt(parts[2], 10, 64); perr == nil {
				name, end = parts[0], time.UnixMilli(ms)
			}
		}
	}
	if err == nil {
		sc.mu.Lock()
		if sc.rows == nil {
			sc.rows = map[int64]time.Time{}
		}
		sc.rows[chatID] = end
		sc.mu.Unlock()
	}
	return name, end
}

// waits reports whether a card waits in the chat: in memory, or through a kv row that did
// not end.
func (sc *SecretCards) waits(ctx context.Context, chatID int64) bool {
	sc.mu.Lock()
	live := sc.cards[chatID] != nil
	sc.mu.Unlock()
	if live {
		return true
	}
	_, end := sc.rowEnd(ctx, chatID)
	return end.After(time.Now())
}

// commandPasses reports whether text is a command that goes to the gateway and not to a
// card. While a card waits, only a known command passes, because a secret value can have
// the form of a command, for example "/Zm9vYmFy0123".
func (sc *SecretCards) commandPasses(ctx context.Context, chatID int64, text string) bool {
	if !strings.HasPrefix(strings.TrimSpace(text), "/") {
		return false
	}
	if !sc.waits(ctx, chatID) {
		return isCommandName(text)
	}
	name, _, ok := gateway.IsCommand(CanonicalCommand(strings.TrimSpace(text)))
	if !ok {
		return false
	}
	if _, found := gateway.Commands()[name]; found || localCommandNames[name] {
		return true
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.skills[name] || sc.skills[strings.ReplaceAll(name, "_", "-")]
}

// isCardReply reports whether m is a reply to a secret card of the bot.
func isCardReply(m Message) bool {
	return m.ReplyToMessage != nil && m.ReplyToMessage.From != nil && m.ReplyToMessage.From.IsBot &&
		strings.HasPrefix(m.ReplyToMessage.Text, secretCardHead)
}

// Take reports whether m belongs to a secret card, and handles it: it deletes m, saves a
// value for a waiting card, and replies with the result. A taken message never reaches the
// agent. A command, a group message, and a message of another user are not taken.
func (sc *SecretCards) Take(ctx context.Context, m Message) bool {
	if m.Chat.Type != "private" || m.From == nil {
		return false
	}
	chatID := m.Chat.ID
	if sc.commandPasses(ctx, chatID, m.Text) {
		return false
	}
	sender := strconv.FormatInt(m.From.ID, 10)
	cardReply := isCardReply(m)

	sc.mu.Lock()
	card := sc.cards[chatID]
	sc.mu.Unlock()
	if card != nil && card.ownerID != sender {
		card = nil
	}
	name := ""
	taken := card != nil
	if card != nil {
		name = card.name
		// A reply to an older card is not a value for the waiting card.
		if cardReply && m.ReplyToMessage.MessageID != card.msgID {
			card = nil
		}
	}
	if !taken && cardReply {
		taken = true
	}
	if !taken {
		rowName, end := sc.rowEnd(ctx, chatID)
		if !end.After(time.Now()) {
			return false
		}
		taken, name = true, rowName
		// The row takes one message only. A later message reaches the agent.
		sc.dropRow(chatID)
	}
	if cardReply && name == "" {
		name = strings.TrimSpace(strings.SplitN(strings.TrimPrefix(m.ReplyToMessage.Text, secretCardHead), "\n", 2)[0])
	}
	if sc.Log != nil {
		sc.Log.Info("telegram: a secret reply was taken", "chat", chatID, "name", name)
	}

	c := replyClient(sc.C)
	send := func(text string, reply *ReplyParameters) {
		_, _ = c.SendMessage(ctx, SendMessageParams{ChatID: chatID, Text: text, ReplyParameters: reply})
	}
	deleted := c.DeleteMessage(ctx, chatID, m.MessageID) == nil
	if !deleted {
		send("I could not delete your message. Delete it by hand.", nil)
	}
	if card == nil {
		if deleted {
			send("This request ended. I deleted your message.", nil)
		}
		return true
	}
	if m.Text == "" {
		send("Send the value as text.", nil)
		return true
	}
	value := strings.TrimSpace(m.Text)
	if err := security.CheckSecretValue(value); err != nil {
		send(err.Error(), nil)
		return true
	}
	save := sc.Save
	if save == nil {
		save = func(string, string) error { return errors.New("no save function") }
	}
	if err := save(card.name, value); err != nil {
		send(fmt.Sprintf("I could not save %s: %s. Send it again.", card.name, err.Error()), nil)
		select {
		case card.done <- err:
		default:
		}
		return true
	}
	text := "✅ Saved " + card.name + "."
	if deleted {
		text += " I deleted your message."
	}
	send(text, &ReplyParameters{MessageID: card.msgID, AllowSendingWithoutReply: true})
	select {
	case card.done <- nil:
	default:
	}
	return true
}

// isCommandName reports whether text starts with a Telegram command: "/", a name of the
// characters A-Z, a-z, 0-9, and "_", an optional "@botname", and then a space or the end.
// A secret value can start with "/", for example a base64 value. Such a value has other
// characters, so it is not a command.
func isCommandName(text string) bool {
	text = strings.TrimSpace(text)
	rest, ok := strings.CutPrefix(text, "/")
	if !ok {
		return false
	}
	head := rest
	if i := strings.IndexAny(rest, " \t\n\r"); i >= 0 {
		head = rest[:i]
	}
	name, bot, hasBot := strings.Cut(head, "@")
	return commandWord(name) && (!hasBot || commandWord(bot))
}

// commandWord reports whether s is not empty and holds only A-Z, a-z, 0-9, and "_".
func commandWord(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}
