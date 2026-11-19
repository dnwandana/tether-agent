package telegram

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/cron"
)

// memHeld is a HeldSendStore in memory. Claim takes a row under one lock.
type memHeld struct {
	mu    sync.Mutex
	rows  map[string]cron.HeldSend
	marks []string
}

func (m *memHeld) Claim(_ context.Context, id string) (cron.HeldSend, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.rows[id]
	delete(m.rows, id)
	return h, ok, nil
}

func (m *memHeld) Restore(_ context.Context, h cron.HeldSend) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[h.ID] = h
	return nil
}

func (m *memHeld) MarkRun(_ context.Context, runID int64, delivery string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.marks = append(m.marks, fmt.Sprintf("%d=%s", runID, delivery))
	return nil
}

func heldCards(t *testing.T) (*Channel, *HeldCards, *memHeld, *fakeBot) {
	t.Helper()
	api := newFakeBot(t)
	store := &memHeld{rows: map[string]cron.HeldSend{"h1": {ID: "h1", Kind: "hold", JobID: "j1", RunID: 41,
		Target: "telegram:-100:7", TargetTitle: "Pricing team", Text: "the report", CardChat: "5", CardMsgID: 101}}}
	hc := &HeldCards{Store: store, C: api.client()}
	ch := &Channel{C: api.client(), Cfg: config.TelegramConfig{AllowedUsers: []string{"5"}}, HeldCards: hc}
	return ch, hc, store, api
}

func tap(ch *Channel, chat int64, data string) {
	cq := press(chat, data)
	ch.handleUpdate(context.Background(), nil, Update{CallbackQuery: &cq})
}

func TestHeldCardText(t *testing.T) {
	_, hc, _, api := heldCards(t)
	card := HeldCard{ID: "h1", Kind: "hold", JobName: "competitor-prices", TargetTitle: "Pricing team", TargetKind: "group",
		Reason:  "the run read pastebin.com, and the text holds a secret. The purpose names neither.",
		Sources: []string{"web_fetch", "terminal:curl"}, Text: "**the report**"}
	id, err := hc.Post(context.Background(), 5, card)
	if err != nil || id == 0 {
		t.Fatal(id, err)
	}
	c := api.lastCall("sendMessage")
	want := "⏸️ Held: job \"competitor-prices\"\nTo: \"Pricing team\" (group)\n" +
		"Guard: the run read pastebin.com, and the text holds a secret. The purpose names neither.\n" +
		"Read: web_fetch, terminal:curl\n—\n**the report**"
	if c.Body["text"] != want || c.Body["parse_mode"] != nil {
		t.Fatalf("%q %v", c.Body["text"], c.Body["parse_mode"])
	}
	texts, data := keyboardOf(api, 0)
	if !reflect.DeepEqual(texts, []string{"❌ Drop", "✅ Send to \"Pricing team\""}) ||
		!reflect.DeepEqual(data, []string{"hs:h1:d", "hs:h1:s"}) || keyboardRows(api) != 2 {
		t.Fatal(texts, data)
	}
	card.Text = strings.Repeat("a", 3600)
	if _, err := hc.Post(context.Background(), 5, card); err != nil {
		t.Fatal(err)
	}
	got := api.lastCall("sendMessage").Body["text"].(string)
	if !strings.HasSuffix(got, "—\n"+strings.Repeat("a", 3500)+"\n(cut; the full text goes to the target)") {
		t.Fatalf("%q", got[len(got)-80:])
	}
	note := HeldCard{ID: "o1", Kind: "original", Text: "🛡️ Job \"spend-report\": I masked 1 item in the delivery to \"Family\":\n• card ••••1111"}
	if _, err := hc.Post(context.Background(), 5, note); err != nil {
		t.Fatal(err)
	}
	if got := api.lastCall("sendMessage").Body["text"]; got != note.Text {
		t.Fatalf("%q", got)
	}
	texts, data = keyboardOf(api, 2)
	if !reflect.DeepEqual(texts, []string{"✅ Post the full text there"}) || !reflect.DeepEqual(data, []string{"hs:o1:s"}) {
		t.Fatal(texts, data)
	}
}

func TestHeldCardSendErrorPutsRowBack(t *testing.T) {
	ch, hc, store, api := heldCards(t)
	var sends atomic.Int32
	hc.Send = func(context.Context, cron.HeldSend) error {
		sends.Add(1)
		return &APIError{Code: 403, Description: "Forbidden: bot was blocked by the user"}
	}
	tap(ch, 5, "hs:h1:s")
	edit := api.lastCall("editMessageText")
	if edit.Body["text"] != "Not sent: telegram API error 403: Forbidden: bot was blocked by the user. Tap Send to try again." ||
		edit.Body["reply_markup"] == nil {
		t.Fatalf("%v", edit.Body)
	}
	if _, ok := store.rows["h1"]; !ok || len(store.marks) != 0 {
		t.Fatal("the row must come back, and the run row must not change")
	}
	hc.Send = func(_ context.Context, h cron.HeldSend) error {
		if h.Target != "telegram:-100:7" || h.Text != "the report" || h.Kind != "hold" {
			t.Errorf("%q %q %q", h.Target, h.Text, h.Kind)
		}
		return nil
	}
	tap(ch, 5, "hs:h1:s")
	if e := api.lastCall("editMessageText"); e.Body["text"] != "Sent." || e.Body["reply_markup"] != nil {
		t.Fatalf("%v", e.Body)
	}
	if !reflect.DeepEqual(store.marks, []string{"41=sent"}) || sends.Load() != 1 {
		t.Fatal(store.marks)
	}
}

func TestHeldCardTwoTapsSendOnce(t *testing.T) {
	ch, hc, _, _ := heldCards(t)
	var sends atomic.Int32
	hc.Send = func(context.Context, cron.HeldSend) error { sends.Add(1); return nil }
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { tap(ch, 5, "hs:h1:s") })
	}
	wg.Wait()
	if sends.Load() != 1 {
		t.Fatalf("%d sends", sends.Load())
	}
}

func TestHeldCardDropAndChecks(t *testing.T) {
	ch, hc, store, api := heldCards(t)
	hc.Send = func(context.Context, cron.HeldSend) error { t.Error("no send"); return nil }
	// Not the chat of the card: the row stays.
	tap(ch, 6, "hs:h1:d")
	if _, ok := store.rows["h1"]; !ok || api.lastCall("answerCallbackQuery").Body["text"] != wrongChat {
		t.Fatal("a tap in another chat changes nothing")
	}
	// Not an owner: no claim.
	cq := pressFrom(5, 9, "hs:h1:d")
	ch.handleUpdate(context.Background(), nil, Update{CallbackQuery: &cq})
	if _, ok := store.rows["h1"]; !ok || api.lastCall("answerCallbackQuery").Body["text"] != notAuthorized {
		t.Fatal("a tap of a user who is not an owner changes nothing")
	}
	tap(ch, 5, "hs:h1:d")
	if e := api.lastCall("editMessageText"); e.Body["text"] != "Dropped." || e.Body["reply_markup"] != nil {
		t.Fatalf("%v", e.Body)
	}
	if !reflect.DeepEqual(store.marks, []string{"41=dropped"}) {
		t.Fatal(store.marks)
	}
	tap(ch, 5, "hs:h1:s")
	if got := api.lastCall("answerCallbackQuery").Body["text"]; got != resolvedText {
		t.Fatalf("%q", got)
	}
}
