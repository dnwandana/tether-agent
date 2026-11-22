package telegram

import (
	"context"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
)

const (
	signInText = "🔑 New dashboard sign-in · Chrome on Android · 10.8.0.5 · code 4821\n" +
		"Compare the code with the code on the device. Allow only a device that you hold."
	signInAllowed = "✅ Dashboard sign-in allowed · Chrome on Android · 10.8.0.5"
	signInDenied  = "❌ Dashboard sign-in denied · Chrome on Android · 10.8.0.5"
	signInClosed  = "⌛ Dashboard sign-in closed · Chrome on Android · 10.8.0.5"
)

func signInChannel(t *testing.T) (*Channel, *fakeBot) {
	t.Helper()
	api := newFakeBot(t)
	return &Channel{C: api.client(), Cfg: config.TelegramConfig{AllowedUsers: []string{"5", "*", "-100", "7", "5"}}}, api
}

// answers records the calls of an answer func.
type answers struct {
	n     atomic.Int32
	allow atomic.Bool
}

func (a *answers) f(allow bool) { a.n.Add(1); a.allow.Store(allow) }

// cardData returns the callback data of the two buttons of the first card.
func cardData(t *testing.T, api *fakeBot) (deny, allow string) {
	t.Helper()
	texts, data := keyboardOf(api, 0)
	if !reflect.DeepEqual(texts, []string{"❌ Deny", "✅ Allow"}) || keyboardRows(api) != 2 ||
		!strings.HasPrefix(data[0], "si:") || !strings.HasSuffix(data[0], ":d") || !strings.HasSuffix(data[1], ":a") {
		t.Fatal(texts, data)
	}
	return data[0], data[1]
}

func TestSignInCardGoesToEachOwnerChat(t *testing.T) {
	ch, api := signInChannel(t)
	var a answers
	if err := ch.SendSignInCard(context.Background(), "Chrome on Android", "10.8.0.5", "4821", time.Now().Add(5*time.Minute), a.f); err != nil {
		t.Fatal(err)
	}
	if got := api.textsOf("sendMessage"); !reflect.DeepEqual(got, []string{signInText, signInText}) {
		t.Fatalf("%q", got)
	}
	var chats []float64
	for _, c := range api.all() {
		if c.Method == "sendMessage" {
			chats = append(chats, c.Body["chat_id"].(float64))
			if c.Body["parse_mode"] != nil {
				t.Fatal("the card must be plain text")
			}
		}
	}
	if !reflect.DeepEqual(chats, []float64{5, 7}) {
		t.Fatalf("one card for each private owner chat, no duplicates: %v", chats)
	}
	cardData(t, api)
}

func TestSignInCardFirstTapWins(t *testing.T) {
	ch, api := signInChannel(t)
	var a answers
	_ = ch.SendSignInCard(context.Background(), "Chrome on Android", "10.8.0.5", "4821", time.Now().Add(5*time.Minute), a.f)
	deny, allow := cardData(t, api)
	// A user that is not an owner, and an owner in another chat, change nothing.
	cq := pressFrom(9, 9, allow)
	ch.handleUpdate(context.Background(), nil, Update{CallbackQuery: &cq})
	cq = pressFrom(-100, 5, allow)
	ch.handleUpdate(context.Background(), nil, Update{CallbackQuery: &cq})
	// Owner 7 taps in the chat of owner 5.
	cq = pressFrom(5, 7, allow)
	ch.handleUpdate(context.Background(), nil, Update{CallbackQuery: &cq})
	if a.n.Load() != 0 {
		t.Fatal("a tap from outside the private owner chat must not answer")
	}
	tap(ch, 5, allow)
	if a.n.Load() != 1 || !a.allow.Load() {
		t.Fatal(a.n.Load(), a.allow.Load())
	}
	if got := api.textsOf("editMessageText"); !reflect.DeepEqual(got, []string{signInAllowed, signInAllowed}) {
		t.Fatalf("each card shows the result: %q", got)
	}
	if lastEditKeyboard(api) != nil {
		t.Fatal("the result has no buttons")
	}
	cq = pressFrom(7, 7, deny)
	ch.handleUpdate(context.Background(), nil, Update{CallbackQuery: &cq})
	if a.n.Load() != 1 || !a.allow.Load() {
		t.Fatal("the second tap must change nothing")
	}
	if got := api.lastCall("answerCallbackQuery").Body["text"]; got != resolvedText {
		t.Fatalf("%v", got)
	}
}

func TestSignInCardDeny(t *testing.T) {
	ch, api := signInChannel(t)
	var a answers
	_ = ch.SendSignInCard(context.Background(), "Chrome on Android", "10.8.0.5", "4821", time.Now().Add(5*time.Minute), a.f)
	deny, _ := cardData(t, api)
	cq := pressFrom(7, 7, deny)
	ch.handleUpdate(context.Background(), nil, Update{CallbackQuery: &cq})
	if a.n.Load() != 1 || a.allow.Load() {
		t.Fatal(a.n.Load(), a.allow.Load())
	}
	if got := api.lastCall("editMessageText").Body["text"]; got != signInDenied {
		t.Fatalf("%v", got)
	}
	if got := api.lastCall("answerCallbackQuery").Body["text"]; got != "❌ Denied" {
		t.Fatalf("%v", got)
	}
}

func TestSignInCardClosesWithTheContext(t *testing.T) {
	ch, api := signInChannel(t)
	var a answers
	ctx, cancel := context.WithCancel(context.Background())
	_ = ch.SendSignInCard(ctx, "Chrome on Android", "10.8.0.5", "4821", time.Now().Add(5*time.Minute), a.f)
	_, allow := cardData(t, api)
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for api.count("editMessageText") < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := api.textsOf("editMessageText"); !reflect.DeepEqual(got, []string{signInClosed, signInClosed}) {
		t.Fatalf("%q", got)
	}
	tap(ch, 5, allow)
	if a.n.Load() != 0 {
		t.Fatal("a closed card must not answer")
	}
	// The expiry closes a card too.
	ch2, api2 := signInChannel(t)
	_ = ch2.SendSignInCard(context.Background(), "Chrome on Android", "10.8.0.5", "4821", time.Now().Add(50*time.Millisecond), a.f)
	deadline = time.Now().Add(2 * time.Second)
	for api2.count("editMessageText") < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := api2.textsOf("editMessageText"); len(got) != 2 || got[0] != signInClosed {
		t.Fatalf("%q", got)
	}
}

func TestSignInCardNeedsAnOwnerChat(t *testing.T) {
	api := newFakeBot(t)
	ch := &Channel{C: api.client(), Cfg: config.TelegramConfig{AllowedUsers: []string{"*"}, AllowAllUsers: true}}
	err := ch.SendSignInCard(context.Background(), "x", "1.2.3.4", "0000", time.Now().Add(time.Minute), func(bool) {})
	if err == nil || err.Error() != "telegram: no private owner chat for the dashboard sign-in card" || api.count("sendMessage") != 0 {
		t.Fatal(err)
	}
}

func TestSignInCardAllowLabelHours(t *testing.T) {
	ch, api := signInChannel(t)
	ch.SignInHours = 24
	_ = ch.SendSignInCard(context.Background(), "Chrome on Android", "10.8.0.5", "4821", time.Now().Add(5*time.Minute), func(bool) {})
	texts, _ := keyboardOf(api, 0)
	if !reflect.DeepEqual(texts, []string{"❌ Deny", "✅ Allow for 24 h"}) {
		t.Fatal(texts)
	}
}

// A tap after the card expired gets the resolved text and does not answer.
func TestSignInCardTapAfterExpiry(t *testing.T) {
	ch, api := signInChannel(t)
	var a answers
	_ = ch.SendSignInCard(context.Background(), "Chrome on Android", "10.8.0.5", "4821", time.Now().Add(5*time.Minute), a.f)
	_, allow := cardData(t, api)
	ch.signInMu.Lock()
	for _, c := range ch.signIns {
		c.expires = time.Now().Add(-time.Second)
	}
	ch.signInMu.Unlock()
	tap(ch, 5, allow)
	if a.n.Load() != 0 {
		t.Fatal("a tap after the expiry must not answer")
	}
	if got := api.lastCall("answerCallbackQuery").Body["text"]; got != resolvedText {
		t.Fatalf("%v", got)
	}
	if got := api.textsOf("editMessageText"); len(got) != 0 {
		t.Fatalf("the card must not be edited: %q", got)
	}
}

// A tap shows the final state of the request. When the dashboard denied the request first, an
// Allow tap shows the denial.
func TestSignInCardTapShowsTheFinalState(t *testing.T) {
	ch, api := signInChannel(t)
	var a answers
	state := func() string { return "denied" }
	_ = ch.SendSignInCardState(context.Background(), "Chrome on Android", "10.8.0.5", "4821", time.Now().Add(5*time.Minute), a.f, state)
	_, allow := cardData(t, api)
	tap(ch, 5, allow)
	if got := api.lastCall("editMessageText").Body["text"]; got != signInDenied {
		t.Fatalf("%v", got)
	}
	if got := api.lastCall("answerCallbackQuery").Body["text"]; got != "❌ Denied" {
		t.Fatalf("%v", got)
	}
}

// When the dashboard answers first, the request context ends. The card then shows that answer,
// not "closed".
func TestSignInCardClosesWithTheOtherAnswer(t *testing.T) {
	ch, api := signInChannel(t)
	var a answers
	ctx, cancel := context.WithCancel(context.Background())
	state := func() string { return "allowed" }
	_ = ch.SendSignInCardState(ctx, "Chrome on Android", "10.8.0.5", "4821", time.Now().Add(5*time.Minute), a.f, state)
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for api.count("editMessageText") < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := api.textsOf("editMessageText"); !reflect.DeepEqual(got, []string{signInAllowed, signInAllowed}) {
		t.Fatalf("%q", got)
	}
}
