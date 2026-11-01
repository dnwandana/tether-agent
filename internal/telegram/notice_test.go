package telegram

import (
	"reflect"
	"testing"

	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

const noticeText = "⏸ Goal paused — the gateway was down for 2h. Use /goal resume to keep going, or /goal clear to stop."

func TestSendNoticeEncodesTheUserOfTheSession(t *testing.T) {
	api := newFakeBot(t)
	ch := &Channel{C: api.client()}
	sess := &sessions.Session{Key: "telegram:dm:7", Source: "telegram", ChatID: "7", UserID: "7"}
	if err := ch.SendNotice(ctx, sess.Key, sess, gateway.Reply{Text: noticeText, Buttons: gateway.StopButtons()}); err != nil {
		t.Fatal(err)
	}
	call := api.lastCall("sendMessage")
	if id, _ := call.Body["chat_id"].(float64); id != 7 {
		t.Fatalf("%v", call.Body)
	}
	if got := StripMarkdownV2(call.Body["text"].(string)); got != noticeText {
		t.Fatalf("%q", got)
	}
	texts, data := keyboardOf(api, 0)
	if !reflect.DeepEqual(texts, []string{"Resume", "Clear"}) ||
		!reflect.DeepEqual(data, []string{"cmd:7:/goal resume", "cmd:7:/goal clear"}) {
		t.Fatal(texts, data)
	}
	for _, d := range data {
		if len(d) > maxCallbackData {
			t.Fatalf("%q is longer than 64 bytes", d)
		}
	}
}

func TestSendNoticeWithNoUserHasNoButtons(t *testing.T) {
	api := newFakeBot(t)
	ch := &Channel{C: api.client()}
	sess := &sessions.Session{Key: "telegram:group:-100", Source: "telegram", ChatID: "-100", ThreadID: "5"}
	if err := ch.SendNotice(ctx, sess.Key, sess, gateway.Reply{Text: noticeText, Buttons: gateway.StopButtons()}); err != nil {
		t.Fatal(err)
	}
	call := api.lastCall("sendMessage")
	if _, ok := call.Body["reply_markup"]; ok {
		t.Fatalf("no user ID gives no buttons: %v", call.Body)
	}
	if thread, _ := call.Body["message_thread_id"].(float64); thread != 5 {
		t.Fatalf("%v", call.Body)
	}
}

func TestSendNoticeRefusesABadChat(t *testing.T) {
	api := newFakeBot(t)
	ch := &Channel{C: api.client()}
	sess := &sessions.Session{Key: "telegram:dm:x", ChatID: "x", UserID: "7"}
	if err := ch.SendNotice(ctx, sess.Key, sess, gateway.Reply{Text: noticeText}); err == nil {
		t.Fatal("a chat ID that is not a number must fail")
	}
	if err := ch.SendNotice(ctx, "k", nil, gateway.Reply{Text: noticeText}); err == nil {
		t.Fatal("a nil session must fail")
	}
	if len(api.all()) != 0 {
		t.Fatal(api.methods())
	}
}
