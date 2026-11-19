package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/tools"
)

var pypiQuestion = tools.SecretQuestion{Name: "PYPI_TOKEN", Commands: []string{"twine upload", "uv publish"},
	Reason: "publish tether-tools 0.4.1 to PyPI"}

const pypiCard = "🔑 Secret needed: PYPI_TOKEN\nFor: twine upload, uv publish\n" +
	"Reason: publish tether-tools 0.4.1 to PyPI\n\n" +
	"Reply to this message with the value. I delete your reply and save the value to .env. The model never sees it.\n" +
	"Waits 5 min."

func TestSendForceReply(t *testing.T) {
	api := newFakeBot(t)
	if _, err := api.client().SendForceReply(ctx, 7, "card", "Paste the value"); err != nil {
		t.Fatal(err)
	}
	c := api.lastCall("sendMessage")
	mk, _ := c.Body["reply_markup"].(map[string]any)
	if c.Body["text"] != "card" || c.Body["parse_mode"] != nil || mk["force_reply"] != true ||
		mk["selective"] != true || mk["input_field_placeholder"] != "Paste the value" {
		t.Fatalf("%+v", c.Body)
	}
}

func TestSecretCardTextAndTimeout(t *testing.T) {
	api := newFakeBot(t)
	d := openTestDB(t)
	sc := &SecretCards{C: api.client(), DB: d, Timeout: 50 * time.Millisecond}
	saved, err := sc.Ask(ctx, 7, "7", pypiQuestion)
	if saved || err != nil {
		t.Fatal(saved, err)
	}
	c := api.lastCall("sendMessage")
	if c.Body["text"] != pypiCard || c.Body["chat_id"] != float64(7) {
		t.Fatalf("%+v", c.Body)
	}
	// The end of the wait clears the kv row.
	if v, _, err := d.KVGet(ctx, "secret_card:7"); err != nil || v != "" {
		t.Fatalf("%q %v", v, err)
	}
	// No reason: no Reason line.
	q := pypiQuestion
	q.Reason = ""
	_, _ = sc.Ask(ctx, 7, "7", q)
	if txt, _ := api.lastCall("sendMessage").Body["text"].(string); strings.Contains(txt, "Reason:") {
		t.Fatal(txt)
	}
}

func TestSecretCardKeepsKVRowWhileItWaits(t *testing.T) {
	api := newFakeBot(t)
	d := openTestDB(t)
	sc := &SecretCards{C: api.client(), DB: d, Timeout: time.Second}
	go sc.Ask(ctx, 7, "7", pypiQuestion)
	deadline := time.Now().Add(2 * time.Second)
	for {
		v, _, _ := d.KVGet(ctx, "secret_card:7")
		if strings.HasPrefix(v, "PYPI_TOKEN\t") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no kv row: %q", v)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSecretCard403(t *testing.T) {
	api := newFakeBot(t)
	api.setReply(func(method string, _ map[string]any) *APIError {
		return &APIError{Code: 403, Description: "Forbidden: bot can't initiate conversation with a user"}
	})
	sc := &SecretCards{C: api.client()}
	if saved, err := sc.Ask(ctx, 7, "7", pypiQuestion); saved || !errors.Is(err, ErrNoPrivateChat) {
		t.Fatal(saved, err)
	}
	if ErrNoPrivateChat.Error() != "I cannot write to the owner in a private chat. Ask the owner to start a private chat with me, then try again." {
		t.Fatal(ErrNoPrivateChat)
	}
}

func TestChatUIAskSecret(t *testing.T) {
	api := newFakeBot(t)
	it := &Interactions{C: api.client()}
	ui := it.For(-100, "7", "42") // a turn in a group topic
	_, err := ui.AskSecret(ctx, pypiQuestion)
	if err == nil || err.Error() != "I have no private chat with the owner. Ask the owner to send /secrets set PYPI_TOKEN." {
		t.Fatal(err)
	}
	it.Secrets = &SecretCards{C: api.client(), Timeout: 50 * time.Millisecond}
	it.OwnerChat = func(_ context.Context, userID string) (int64, bool) {
		if userID != "42" {
			t.Errorf("user %q", userID)
		}
		return 42, true
	}
	if _, err := ui.AskSecret(ctx, pypiQuestion); err != nil {
		t.Fatal(err)
	}
	// The card goes to the private chat of the owner, never to the group.
	for _, c := range api.all() {
		if c.Body["chat_id"] == float64(-100) {
			t.Fatalf("a send to the group: %+v", c)
		}
	}
	if api.lastCall("sendMessage").Body["chat_id"] != float64(42) {
		t.Fatal(api.methods())
	}
	var _ tools.SecretAsker = ui
}
