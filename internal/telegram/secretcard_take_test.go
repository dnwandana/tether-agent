package telegram

import (
	"bytes"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/gateway"
)

// secretHarness is a harness with the owner 7 and a secret card store that records saves.
type secretHarness struct {
	*tgHarness
	mu    sync.Mutex
	saved map[string]string
}

func newSecretHarness(t *testing.T) *secretHarness {
	t.Helper()
	h := &secretHarness{tgHarness: harness(t, &tgProv{}, func(c *config.TelegramConfig) {
		c.AllowedUsers = []string{"7"}
	}), saved: map[string]string{}}
	h.ch.Secrets = &SecretCards{C: h.ch.C, DB: h.ch.DB, Timeout: 2 * time.Second, Save: func(name, value string) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.saved[name] = value
		return nil
	}}
	return h
}

// openCard starts a card for PYPI_TOKEN in the chat 7 and waits until it is sent.
func (h *secretHarness) openCard(t *testing.T) chan bool {
	t.Helper()
	res := make(chan bool, 1)
	go func() {
		saved, _ := h.ch.Secrets.Ask(ctx, 7, "7", pypiQuestion)
		res <- saved
	}()
	deadline := time.Now().Add(2 * time.Second)
	for h.api.count("sendMessage") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no card")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return res
}

func TestTakeSecretPlainText(t *testing.T) {
	h := newSecretHarness(t)
	var logs bytes.Buffer
	h.ch.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	res := h.openCard(t)
	h.update(dmText(7, "pypi-AgEIcHlwaS5vcmcCJDU0ZjM4"))
	h.waitIdle()
	if !<-res || h.saved["PYPI_TOKEN"] != "pypi-AgEIcHlwaS5vcmcCJDU0ZjM4" {
		t.Fatal(h.saved)
	}
	if h.api.count("deleteMessage") != 1 || h.prov.count() != 0 {
		t.Fatal("the value must be deleted and must not start a turn", h.api.methods())
	}
	if got := h.api.lastText(); got != "✅ Saved PYPI_TOKEN. I deleted your message." {
		t.Fatal(got)
	}
	if strings.Contains(logs.String(), "pypi-AgEI") {
		t.Fatal("a log line holds the value")
	}
}

// A plain text that the owner pastes after a restart is taken and deleted. Nothing is saved.
func TestTakeSecretAfterRestart(t *testing.T) {
	h := newSecretHarness(t)
	end := time.Now().Add(4 * time.Minute).UnixMilli()
	if err := h.ch.DB.KVSet(ctx, "secret_card:7", "PYPI_TOKEN\t555\t"+strconv.FormatInt(end, 10)); err != nil {
		t.Fatal(err)
	}
	h.update(dmText(7, "pypi-AgEIcHlwaS5vcmcCJDU0ZjM4"))
	h.waitIdle()
	if h.api.count("deleteMessage") != 1 || h.prov.count() != 0 || len(h.saved) != 0 {
		t.Fatal(h.api.methods(), h.saved)
	}
	if got := h.api.lastText(); got != "This request ended. I deleted your message." {
		t.Fatal(got)
	}
}

// A reply to an ended card is taken with no memory and no kv row.
func TestTakeSecretReplyToEndedCard(t *testing.T) {
	h := newSecretHarness(t)
	m := dmText(7, "pypi-AgEIcHlwaS5vcmcCJDU0ZjM4")
	m.ReplyToMessage = &Message{MessageID: 9, From: &User{ID: 99, IsBot: true}, Chat: m.Chat, Text: pypiCard}
	h.update(m)
	h.update(m) // a second delivery of the same update
	h.waitIdle()
	if h.api.count("deleteMessage") != 2 || h.prov.count() != 0 || len(h.saved) != 0 {
		t.Fatal(h.api.methods())
	}
}

func TestTakeSecretNotTaken(t *testing.T) {
	h := newSecretHarness(t)
	res := h.openCard(t)
	h.update(dmText(7, "/help"))                 // a command
	h.update(dmText(8, "pypi-other-user-value")) // another private chat
	g := group("pypi-AgEIcHlwaS5vcmcCJDU0ZjM4")  // a group reply to a card
	g.ReplyToMessage = &Message{MessageID: 9, From: &User{ID: 99, IsBot: true}, Chat: g.Chat, Text: pypiCard}
	h.update(g)
	h.waitIdle()
	if h.api.count("deleteMessage") != 0 || len(h.saved) != 0 {
		t.Fatal(h.api.methods(), h.saved)
	}
	h.update(dmText(7, "pypi-AgEIcHlwaS5vcmcCJDU0ZjM4"))
	h.waitIdle()
	if !<-res {
		t.Fatal("the card did not get the value")
	}
}

func TestTakeSecretNoTextAndBadValue(t *testing.T) {
	h := newSecretHarness(t)
	res := h.openCard(t)
	photo := dmText(7, "")
	photo.Photo = []PhotoSize{{FileID: "p1", Width: 10, Height: 10}}
	h.update(photo)
	h.waitIdle()
	if got := h.api.lastText(); got != "Send the value as text." {
		t.Fatal(got)
	}
	h.update(dmText(7, "line one\nline two"))
	h.waitIdle()
	if got := h.api.lastText(); got != "The value has a line break or is longer than 4096 bytes. Send it again." {
		t.Fatal(got)
	}
	if len(h.saved) != 0 || h.api.count("deleteMessage") != 2 {
		t.Fatal(h.saved, h.api.methods())
	}
	h.update(dmText(7, "pypi-AgEIcHlwaS5vcmcCJDU0ZjM4"))
	h.waitIdle()
	if !<-res {
		t.Fatal("the card did not stay open")
	}
}

func TestTakeSecretFailedDeleteAndSave(t *testing.T) {
	h := newSecretHarness(t)
	h.api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "deleteMessage" {
			return &APIError{Code: 400, Description: "Bad Request: message can't be deleted"}
		}
		return nil
	})
	h.ch.Secrets.Save = func(string, string) error { return errors.New(".env is locked by another process. Try again.") }
	h.openCard(t)
	h.update(dmText(7, "pypi-AgEIcHlwaS5vcmcCJDU0ZjM4"))
	h.waitIdle()
	texts := h.api.textsOf("sendMessage")
	want := []string{"I could not delete your message. Delete it by hand.",
		"I could not save PYPI_TOKEN: .env is locked by another process. Try again.. Send it again."}
	if got := texts[len(texts)-2:]; !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
}

// A value that starts with "/" but is not a command name is taken. About 1 in 64 base64
// values start with "/".
func TestTakeSecretSlashValue(t *testing.T) {
	h := newSecretHarness(t)
	res := h.openCard(t)
	h.update(dmText(7, "/x9K+abc/def=="))
	h.waitIdle()
	if !<-res || h.saved["PYPI_TOKEN"] != "/x9K+abc/def==" {
		t.Fatal(h.saved)
	}
	if h.api.count("deleteMessage") != 1 || h.prov.count() != 0 {
		t.Fatal("the value must be deleted and must not start a turn", h.api.methods())
	}
}

// A real command passes to the gateway while a card waits, also as a reply to the card.
func TestTakeSecretCommandPasses(t *testing.T) {
	h := newSecretHarness(t)
	res := h.openCard(t)
	h.update(dmText(7, "/stop"))
	h.update(dmText(7, "/stop@tether_bot"))
	reply := dmText(7, "/usage now")
	reply.ReplyToMessage = &Message{MessageID: 101, From: &User{ID: 99, IsBot: true}, Chat: reply.Chat, Text: pypiCard}
	h.update(reply)
	h.waitIdle()
	if h.api.count("deleteMessage") != 0 || len(h.saved) != 0 {
		t.Fatal(h.api.methods(), h.saved)
	}
	h.update(dmText(7, "pypi-AgEIcHlwaS5vcmcCJDU0ZjM4"))
	h.waitIdle()
	if !<-res {
		t.Fatal("the card did not stay open")
	}
}

// An edit of an older message is not a value for a waiting card.
func TestTakeSecretIgnoresEdit(t *testing.T) {
	h := newSecretHarness(t)
	res := h.openCard(t)
	old := dmText(7, "an older message, edited")
	old.MessageID = 3
	h.edit(old)
	h.waitIdle()
	if h.api.count("deleteMessage") != 0 || len(h.saved) != 0 {
		t.Fatal("an edit must not be taken", h.api.methods(), h.saved)
	}
	h.update(dmText(7, "pypi-AgEIcHlwaS5vcmcCJDU0ZjM4"))
	h.waitIdle()
	if !<-res || h.saved["PYPI_TOKEN"] != "pypi-AgEIcHlwaS5vcmcCJDU0ZjM4" {
		t.Fatal(h.saved)
	}
}

// A take through the kv row after a restart clears the row. The next plain message of the
// owner reaches the agent.
func TestTakeSecretAfterRestartClearsRow(t *testing.T) {
	h := newSecretHarness(t)
	end := time.Now().Add(4 * time.Minute).UnixMilli()
	if err := h.ch.DB.KVSet(ctx, "secret_card:7", "PYPI_TOKEN\t555\t"+strconv.FormatInt(end, 10)); err != nil {
		t.Fatal(err)
	}
	h.update(dmText(7, "pypi-AgEIcHlwaS5vcmcCJDU0ZjM4"))
	h.waitIdle()
	if v, _, err := h.ch.DB.KVGet(ctx, "secret_card:7"); err != nil || v != "" {
		t.Fatalf("the row must be cleared: %q %v", v, err)
	}
	h.update(dmText(7, "hello again"))
	h.waitIdle()
	if h.api.count("deleteMessage") != 1 || h.prov.count() != 1 {
		t.Fatal("the second message must reach the agent", h.api.methods(), h.prov.count())
	}
}

// saveCard and clearCard keep the cache of the kv rows current.
func TestSecretCardRowCache(t *testing.T) {
	h := newSecretHarness(t)
	sc := h.ch.Secrets
	rowOf := func() (time.Time, bool) {
		sc.mu.Lock()
		defer sc.mu.Unlock()
		end, ok := sc.rows[7]
		return end, ok
	}
	res := h.openCard(t)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if end, ok := rowOf(); ok && end.After(time.Now()) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("saveCard did not set the cache")
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.update(dmText(7, "pypi-AgEIcHlwaS5vcmcCJDU0ZjM4"))
	h.waitIdle()
	if !<-res {
		t.Fatal("no save")
	}
	if end, ok := rowOf(); !ok || !end.IsZero() {
		t.Fatal("clearCard did not set the cache to zero", end, ok)
	}
}

// While a card waits, a value such as "/Zm9vYmFy0123" is taken. It looks like a command, but
// no command has its name.
func TestTakeSecretCommandLikeValue(t *testing.T) {
	h := newSecretHarness(t)
	res := h.openCard(t)
	h.update(dmText(7, "/Zm9vYmFy0123"))
	h.waitIdle()
	if !<-res || h.saved["PYPI_TOKEN"] != "/Zm9vYmFy0123" {
		t.Fatal(h.saved)
	}
	if h.api.count("deleteMessage") != 1 || h.prov.count() != 0 {
		t.Fatal("the value must be deleted and must not start a turn", h.api.methods())
	}
}

// With no waiting card, a text with a command form passes to the gateway as before.
func TestTakeSecretCommandFormNoCard(t *testing.T) {
	h := newSecretHarness(t)
	if h.ch.Secrets.Take(ctx, dmText(7, "/Zm9vYmFy0123")) {
		t.Fatal("a command form with no card must not be taken")
	}
	if h.api.count("deleteMessage") != 0 {
		t.Fatal(h.api.methods())
	}
}

// A local command of the channel and a skill command of the menu pass while a card waits.
func TestTakeSecretLocalAndSkillCommandPass(t *testing.T) {
	h := newSecretHarness(t)
	h.ch.Secrets.setSkillCommands([]gateway.SkillCommand{{Slug: "daily-report"}})
	h.openCard(t)
	for _, text := range []string{"/start", "/sethome", "/reload_mcp", "/daily_report", "/daily-report now"} {
		if h.ch.Secrets.Take(ctx, dmText(7, text)) {
			t.Fatalf("%s must pass", text)
		}
	}
	if h.api.count("deleteMessage") != 0 || len(h.saved) != 0 {
		t.Fatal(h.api.methods(), h.saved)
	}
}

// An edit of a reply to a secret card never reaches the agent. The edit is deleted.
func TestEditedSecretReplyIsDropped(t *testing.T) {
	h := newSecretHarness(t)
	m := dmText(7, "pypi-AgEIcHlwaS5vcmcCJDU0ZjM4")
	m.MessageID = 3
	m.ReplyToMessage = &Message{MessageID: 9, From: &User{ID: 99, IsBot: true}, Chat: m.Chat, Text: pypiCard}
	h.edit(m)
	h.waitIdle()
	if h.prov.count() != 0 || len(h.saved) != 0 {
		t.Fatal("the edit must not reach the agent", h.prov.count(), h.saved)
	}
	if h.api.count("deleteMessage") != 1 {
		t.Fatal("the edit must be deleted", h.api.methods())
	}
}
