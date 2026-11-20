package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/security"
)

func TestHeldOriginalsPutTake(t *testing.T) {
	h := &HeldOriginals{}
	id := h.Put(-100, "7", "full text", []string{"/tmp/a.csv"})
	if len(id) != 8 || strings.Trim(id, "0123456789abcdef") != "" {
		t.Fatalf("id %q", id)
	}
	chat, thread, text, files, ok := h.Take(id)
	if !ok || chat != -100 || thread != "7" || text != "full text" || len(files) != 1 || files[0] != "/tmp/a.csv" {
		t.Fatal(chat, thread, text, files, ok)
	}
	if _, _, _, _, ok := h.Take(id); ok {
		t.Fatal("a second Take must find nothing")
	}
}

func TestHeldOriginalsLimitAndExpiry(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	h := &HeldOriginals{now: func() time.Time { return now }}
	first := h.Put(1, "", "t0", nil)
	for i := 1; i < 33; i++ {
		h.Put(1, "", fmt.Sprintf("t%d", i), nil)
	}
	if _, _, _, _, ok := h.Take(first); ok {
		t.Fatal("the 33rd entry must drop the oldest one")
	}
	id := h.Put(1, "", "late", nil)
	now = now.Add(10*time.Minute + time.Second)
	if _, _, _, _, ok := h.Take(id); ok {
		t.Fatal("an entry older than 10 min is gone")
	}
}

func TestMaskedNoteText(t *testing.T) {
	f := []security.Finding{{Kind: "iban", Label: "[IBAN GB82••••5432]"}, {Kind: "phone", Label: "[phone +94•••••4567]"}}
	want := "🛡️ I masked 2 items in my answer in \"Home ops\":\n• IBAN GB82••••5432\n• phone +94•••••4567"
	if got := maskedNote("Home ops", f, nil); got != want {
		t.Fatalf("got %q", got)
	}
	want = "🛡️ I masked 1 item in my answer in \"Ops\\_team\":\n• file report.csv"
	if got := maskedNote("Ops_team", nil, []string{"/srv/out/report.csv"}); got != want {
		t.Fatalf("got %q", got)
	}
}

// heldChannel returns a channel whose owner is the user 5, with a fake bot.
func heldChannel(t *testing.T) (*Channel, *fakeBot) {
	t.Helper()
	api := newFakeBot(t)
	ch := &Channel{C: api.client(), Cfg: config.TelegramConfig{AllowedUsers: []string{"5"}}, Held: &HeldOriginals{},
		Out: &Sender{C: api.client()}}
	return ch, api
}

func TestNoteMaskedAndPostTap(t *testing.T) {
	ch, api := heldChannel(t)
	orig := "key tok-1234567890abcdef and IBAN GB82 WEST 1234 5698 7654 32"
	f := []security.Finding{
		{Kind: "secret", Start: 4, End: 24, Label: "[secret]"},
		{Kind: "iban", Start: 34, End: len(orig), Label: "[IBAN GB82••••5432]"},
	}
	ch.noteMasked(context.Background(), 5, -100, "7", "Home ops", orig, f, nil, false)
	texts, data := keyboardOf(api, 0)
	if len(data) != 1 || texts[0] != "✅ Post the full answer there" || !strings.HasPrefix(data[0], "ob:") {
		t.Fatal(texts, data)
	}
	if c := api.lastCall("sendMessage"); fmt.Sprint(c.Body["chat_id"]) != "5" {
		t.Fatalf("the note goes to the owner chat: %v", c.Body)
	}
	// A tap from another chat changes nothing.
	ch.handleUpdate(context.Background(), nil, Update{CallbackQuery: new(press(6, data[0]))})
	if api.count("sendMessage") != 1 {
		t.Fatal("a tap in another chat must not post")
	}
	ch.handleUpdate(context.Background(), nil, Update{CallbackQuery: new(press(5, data[0]))})
	post := api.lastCall("sendMessage")
	if fmt.Sprint(post.Body["chat_id"]) != "-100" || fmt.Sprint(post.Body["message_thread_id"]) != "7" {
		t.Fatalf("%v", post.Body)
	}
	if s := fmt.Sprint(post.Body["text"]); strings.Contains(s, "tok-1234567890abcdef") || !strings.Contains(s, "GB82 WEST") {
		t.Fatalf("the post keeps the IBAN and masks the secret: %q", s)
	}
	edit := api.lastCall("editMessageText")
	if edit.Body["text"] != "Posted." || edit.Body["reply_markup"] != nil {
		t.Fatalf("%v", edit.Body)
	}
	ch.handleUpdate(context.Background(), nil, Update{CallbackQuery: new(press(5, data[0]))})
	if got := api.lastCall("answerCallbackQuery").Body["text"]; got != "This text is gone. Ask me again." {
		t.Fatalf("%q", got)
	}
}

// Run gives the channel a store for the originals, so a masked answer gets the owner note.
func TestRunSetsHeldOriginals(t *testing.T) {
	api := newFakeBot(t)
	ch := &Channel{C: api.client(), DB: openTestDB(t), Log: slog.New(slog.DiscardHandler)}
	c, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ch.Run(c, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if ch.Held == nil {
		t.Fatal("Run must set Held")
	}
	keep := &HeldOriginals{}
	ch.Held = keep
	_ = ch.Run(c, nil)
	if ch.Held != keep {
		t.Fatal("Run must keep a Held that is set")
	}
}
