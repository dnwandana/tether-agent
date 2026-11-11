package telegram

import (
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/gateway"
)

const (
	testSteerAck = "↪️ Got it. I'll do this after the current step."
	testReadAck  = "✅ The agent read your note."
)

// waitDone fails the test when ch does not close within 5 s.
func waitDone(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("the edit did not end")
	}
}

func nowRow() [][]gateway.Button {
	return [][]gateway.Button{{{Text: "⚡ Do this now", Data: "/now 1"}}}
}

func TestAckReplyHandleSendsAndEdits(t *testing.T) {
	h := harness(t, &tgProv{}, nil)
	ah := replyRenderer(h).AckReplyHandle(gateway.Reply{Text: testSteerAck, Buttons: nowRow()})
	if ah == nil {
		t.Fatal("no handle")
	}
	c := h.api.lastCall("sendMessage")
	if h.api.count("sendMessage") != 1 || c.Body["text"] != ToMarkdownV2(testSteerAck) {
		t.Fatalf("%v %+v", h.api.methods(), c.Body)
	}
	if got := buttonData(c.Body); len(got) != 1 || got[0] != "cmd:7:/now 1" {
		t.Fatal(got)
	}
	if ah.MsgID() <= 100 {
		t.Fatal(ah.MsgID())
	}
	waitDone(t, ah.Close(testReadAck))
	e := h.api.lastCall("editMessageText")
	if h.api.count("editMessageText") != 1 || e.Body["message_id"] != float64(ah.MsgID()) ||
		e.Body["text"] != ToMarkdownV2(testReadAck) || e.Body["parse_mode"] != "MarkdownV2" {
		t.Fatalf("%v %+v", h.api.methods(), e.Body)
	}
	if _, ok := e.Body["reply_markup"]; ok {
		t.Fatal("the edit must remove the button")
	}
	// Close runs once.
	waitDone(t, ah.Close("⏹ Not sent. The task stopped."))
	if n := h.api.count("editMessageText"); n != 1 {
		t.Fatal(n)
	}
}

func TestAckHandleCloseWithNoText(t *testing.T) {
	h := harness(t, &tgProv{}, nil)
	ah := replyRenderer(h).AckReplyHandle(gateway.Reply{Text: testSteerAck, Buttons: nowRow()})
	waitDone(t, ah.Close(""))
	waitDone(t, ah.Close(testReadAck))
	if n := h.api.count("editMessageText"); n != 0 {
		t.Fatal(n)
	}
}

func TestAckHandleCloseDoesNotBlock(t *testing.T) {
	h := harness(t, &tgProv{}, nil)
	ah := replyRenderer(h).AckReplyHandle(gateway.Reply{Text: "⏳ Queued. I'll answer after the current task.", Buttons: nowRow()})
	hold := make(chan struct{})
	h.api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			<-hold
		}
		return nil
	})
	start := time.Now()
	done := ah.Close("▶️ The agent works on your message now.")
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatal("Close blocked for", d)
	}
	select {
	case <-done:
		t.Fatal("the channel must stay open while the edit runs")
	case <-time.After(50 * time.Millisecond):
	}
	close(hold)
	waitDone(t, done)
}

func TestAckHandleNotModifiedIsSuccess(t *testing.T) {
	h := harness(t, &tgProv{}, nil)
	ah := replyRenderer(h).AckReplyHandle(gateway.Reply{Text: testSteerAck, Buttons: nowRow()})
	h.api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 400, Description: "Bad Request: message is not modified"}
		}
		return nil
	})
	waitDone(t, ah.Close(testReadAck))
	if n := h.api.count("editMessageText"); n != 1 {
		t.Fatal("no second edit after not modified:", n)
	}
}

func TestAckReplyHandleNil(t *testing.T) {
	h := harness(t, &tgProv{}, nil)
	r := replyRenderer(h)
	r.editMsgID = 55
	if ah := r.AckReplyHandle(gateway.Reply{Text: "**Page 2**", Edit: true}); ah != nil {
		t.Fatal("an edit has no handle")
	}
	if h.api.count("editMessageText") != 1 {
		t.Fatal("the edit must still go out:", h.api.methods())
	}
	h.api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "sendMessage" {
			return &APIError{Code: 403, Description: "Forbidden: bot was blocked by the user"}
		}
		return nil
	})
	if ah := replyRenderer(h).AckReplyHandle(gateway.Reply{Text: testSteerAck, Buttons: nowRow()}); ah != nil {
		t.Fatal("a failed send has no handle")
	}
	hr := harness(t, &tgProv{}, nil)
	withRich(t, hr)
	if ah := replyRenderer(hr).AckReplyHandle(gateway.Reply{Text: "T", Rich: "R"}); ah != nil {
		t.Fatal("a rich reply has no handle")
	}
}

func TestCallbackInboundHasButtonMsgID(t *testing.T) {
	h := harness(t, &tgProv{}, nil)
	n := Normalize(Message{Chat: Chat{ID: 7, Type: "private"}, From: &User{ID: 7, FirstName: "U"}})
	in := h.ch.callbackInbound(ctx, n, "/now 1", 55)
	if in.ButtonMsgID != 55 || in.Text != "/now 1" || !in.IsText || in.Key != h.ch.sessionKey(n) || in.Source != "telegram" {
		t.Fatalf("%+v", in)
	}
	if in.Owner || in.Shared {
		t.Fatalf("no allowlist: %+v", in)
	}
	owner := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"7"} })
	if in := owner.ch.callbackInbound(ctx, n, "/now 1", 55); !in.Owner || in.Shared {
		t.Fatalf("%+v", in)
	}
}
