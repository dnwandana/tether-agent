package telegram

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/gateway"
)

func TestEncodeDecodeCmd(t *testing.T) {
	data := encodeCmd("123", "/resume 001a17f9c2e4b8d3a1c")
	if data != "cmd:123:/resume 001a17f9c2e4b8d3a1c" {
		t.Fatal(data)
	}
	user, action, ok := decodeCmd(data)
	if !ok || user != "123" || action != "/resume 001a17f9c2e4b8d3a1c" {
		t.Fatalf("%q %q %v", user, action, ok)
	}
	// The action can hold ":".
	if user, action, ok := decodeCmd("cmd:7:/model zai:glm-4.6"); !ok || user != "7" || action != "/model zai:glm-4.6" {
		t.Fatalf("%q %q %v", user, action, ok)
	}
	for _, bad := range []string{"ap:1:once", "cmd:", "cmd:7", "cmd:7:", "cmd::/new", "cmd:x7:/new"} {
		if _, _, ok := decodeCmd(bad); ok {
			t.Errorf("decodeCmd(%q) ok", bad)
		}
	}
}

func TestEncodeCmdCutsAtUTF8Boundary(t *testing.T) {
	action := "/sessions 2 " + strings.Repeat("日本", 20)
	data := encodeCmd("1234567890123456", action)
	if len(data) > maxCallbackData || !utf8.ValidString(data) {
		t.Fatalf("%d %q", len(data), data)
	}
	user, got, ok := decodeCmd(data)
	if !ok || user != "1234567890123456" || !strings.HasPrefix(got, "/sessions 2 ") || !strings.HasPrefix(action, got) {
		t.Fatalf("%q %q %v", user, got, ok)
	}
	// A cut that ends at a space drops the space.
	data = encodeCmd("1234567890123456", "/sessions 2 "+strings.Repeat("a", 33)+" bcd")
	if len(data) > maxCallbackData || strings.HasSuffix(data, " ") {
		t.Fatalf("%q", data)
	}
}

func TestLongestActionFits(t *testing.T) {
	action := "/delete 001a17f9c2e4b8d3a1c confirm"
	data := encodeCmd("1234567890123456", action)
	if data != "cmd:1234567890123456:"+action || len(data) > maxCallbackData {
		t.Fatalf("%d %q", len(data), data)
	}
}

func TestCmdKeyboard(t *testing.T) {
	if kb := cmdKeyboard("7", nil); kb != nil {
		t.Fatal(kb)
	}
	if kb := cmdKeyboard("", [][]gateway.Button{{{Text: "1", Data: "/new"}}}); kb != nil {
		t.Fatal("a reply without a user must not get buttons:", kb)
	}
	kb := cmdKeyboard("7", [][]gateway.Button{{{Text: "◀ Prev", Data: "/sessions 1"}, {Text: "1/2", Data: "·"}}, {{Text: "🆕 New session", Data: "/new"}}})
	if kb == nil || len(kb.InlineKeyboard) != 2 || len(kb.InlineKeyboard[0]) != 2 {
		t.Fatalf("%+v", kb)
	}
	if b := kb.InlineKeyboard[0][1]; b.Text != "1/2" || b.CallbackData != "cmd:7:·" {
		t.Fatalf("%+v", b)
	}
	if b := kb.InlineKeyboard[1][0]; b.CallbackData != "cmd:7:/new" {
		t.Fatalf("%+v", b)
	}
}

// buttonData returns the callback data of each inline button in the body of a call.
func buttonData(body map[string]any) []string {
	var out []string
	markup, _ := body["reply_markup"].(map[string]any)
	rows, _ := markup["inline_keyboard"].([]any)
	for _, row := range rows {
		btns, _ := row.([]any)
		for _, b := range btns {
			m, _ := b.(map[string]any)
			s, _ := m["callback_data"].(string)
			out = append(out, s)
		}
	}
	return out
}

// withRich makes the harness client answer sendRichMessage with a message, like a Bot API
// with rich messages. The calls go to the record of h.api, and its reply hook applies.
func withRich(t *testing.T, h *tgHarness) {
	t.Helper()
	b := h.api
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendRichMessage") {
			b.serve(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		body := map[string]any{}
		_ = json.Unmarshal(raw, &body)
		b.mu.Lock()
		b.calls = append(b.calls, apiCall{Method: "sendRichMessage", Body: body})
		var apiErr *APIError
		if b.reply != nil {
			apiErr = b.reply("sendRichMessage", body)
		}
		b.nextID++
		id := b.nextID
		b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if apiErr != nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": apiErr.Code, "description": apiErr.Description})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": id, "chat": map[string]any{"id": body["chat_id"]}}})
	}))
	t.Cleanup(srv.Close)
	c := &Client{Token: "T", BaseURL: srv.URL, HC: srv.Client()}
	c.sleep = h.ch.C.sleep
	h.ch.C = c
}

// replyRenderer returns a renderer of a DM with the user 7, like dispatch makes it.
func replyRenderer(h *tgHarness) *tgRenderer {
	r := h.ch.newRenderer(ctx, 7, "", true)
	r.userID = "7"
	return r
}

func TestAckReplyButtons(t *testing.T) {
	h := harness(t, &tgProv{}, nil)
	r := replyRenderer(h)
	r.AckReply(gateway.Reply{Text: "📋 **Your sessions**", Buttons: [][]gateway.Button{{{Text: "1", Data: "/resume X"}}}})
	if n := h.api.count("sendMessage"); n != 1 {
		t.Fatal(h.api.methods())
	}
	c := h.api.lastCall("sendMessage")
	if c.Body["parse_mode"] != "MarkdownV2" || c.Body["text"] != ToMarkdownV2("📋 **Your sessions**") {
		t.Fatalf("%+v", c.Body)
	}
	if got := buttonData(c.Body); len(got) != 1 || got[0] != "cmd:7:/resume X" {
		t.Fatal(got)
	}
	// An empty reply sends nothing.
	r.AckReply(gateway.Reply{Text: "  "})
	if n := h.api.count("sendMessage"); n != 1 {
		t.Fatal(h.api.methods())
	}
}

func TestAckReplyRichAndFallback(t *testing.T) {
	h := harness(t, &tgProv{}, nil)
	withRich(t, h)
	r := replyRenderer(h)
	rep := gateway.Reply{Text: "T", Rich: "R", Buttons: [][]gateway.Button{{{Text: "7 days", Data: "/usage 7d"}}}}
	r.AckReply(rep)
	if h.api.count("sendRichMessage") != 1 || h.api.count("sendMessage") != 0 {
		t.Fatal(h.api.methods())
	}
	c := h.api.lastCall("sendRichMessage")
	rich, _ := c.Body["rich_message"].(map[string]any)
	if rich["markdown"] != "R" {
		t.Fatalf("%+v", c.Body)
	}
	if got := buttonData(c.Body); len(got) != 1 || got[0] != "cmd:7:/usage 7d" {
		t.Fatal(got)
	}
	if h.ch.richFailed.Load() {
		t.Fatal("a sent rich message is not a failure")
	}

	h.api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "sendRichMessage" {
			return &APIError{Code: 400, Description: "Bad Request: method not found"}
		}
		return nil
	})
	r.AckReply(rep)
	if h.api.count("sendRichMessage") != 2 || h.api.count("sendMessage") != 1 {
		t.Fatal(h.api.methods())
	}
	c = h.api.lastCall("sendMessage")
	if c.Body["text"] != "T" {
		t.Fatalf("%+v", c.Body)
	}
	if got := buttonData(c.Body); len(got) != 1 || got[0] != "cmd:7:/usage 7d" {
		t.Fatal(got)
	}
	if !h.ch.richFailed.Load() {
		t.Fatal("the first rich failure must be recorded")
	}
}

func TestAckReplyEdit(t *testing.T) {
	h := harness(t, &tgProv{}, nil)
	r := replyRenderer(h)
	r.editMsgID = 55
	btns := [][]gateway.Button{{{Text: "◀ Prev", Data: "/sessions 1"}}}

	r.AckReply(gateway.Reply{Text: "**Page 2**", Buttons: btns, Edit: true})
	if h.api.count("editMessageText") != 1 || h.api.count("sendMessage") != 0 {
		t.Fatal(h.api.methods())
	}
	c := h.api.lastCall("editMessageText")
	if c.Body["message_id"] != float64(55) || c.Body["text"] != ToMarkdownV2("**Page 2**") || c.Body["parse_mode"] != "MarkdownV2" {
		t.Fatalf("%+v", c.Body)
	}
	if got := buttonData(c.Body); len(got) != 1 || got[0] != "cmd:7:/sessions 1" {
		t.Fatal(got)
	}

	// A rich edit sends rich_message and no text.
	r.AckReply(gateway.Reply{Text: "T", Rich: "R", Buttons: btns, Edit: true})
	c = h.api.lastCall("editMessageText")
	rich, _ := c.Body["rich_message"].(map[string]any)
	if h.api.count("editMessageText") != 2 || rich["markdown"] != "R" || c.Body["text"] != nil {
		t.Fatalf("%v %+v", h.api.methods(), c.Body)
	}

	// Edit without a button message sends a new message.
	r2 := replyRenderer(h)
	r2.AckReply(gateway.Reply{Text: "new", Edit: true})
	if h.api.count("sendMessage") != 1 || h.api.count("editMessageText") != 2 {
		t.Fatal(h.api.methods())
	}

	// A message that is too old to edit gets a new message.
	h.api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 400, Description: "Bad Request: message can't be edited"}
		}
		return nil
	})
	r.AckReply(gateway.Reply{Text: "old", Buttons: btns, Edit: true})
	if h.api.count("sendMessage") != 2 {
		t.Fatal(h.api.methods())
	}
	if c := h.api.lastCall("sendMessage"); c.Body["text"] != "old" || len(buttonData(c.Body)) != 1 {
		t.Fatalf("%+v", c.Body)
	}

	// The same page again is not a failure.
	h.api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 400, Description: "Bad Request: message is not modified"}
		}
		return nil
	})
	r.AckReply(gateway.Reply{Text: "same", Buttons: btns, Edit: true})
	if h.api.count("sendMessage") != 2 {
		t.Fatal(h.api.methods())
	}
}
