package telegram

import (
	"reflect"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/gateway"
)

const ibanText = "IBAN GB82 WEST 1234 5698 7654 32"

func TestAckReplyMasksGroup(t *testing.T) {
	h, _ := outboundHarness(t)
	r := h.ch.newRenderer(ctx, -100, "123", true)
	r.userID = "7"
	r.AckReply(gateway.Reply{Text: ibanText,
		Buttons: [][]gateway.Button{{{Text: "✓ OK", Data: "/notice ok 1.a3f1"}}}})
	if got := allTexts(h); strings.Contains(got, "GB82 WEST 1234") || got == "" {
		t.Fatalf("got:\n%s", got)
	}
	want := []string{"cmd:7:/notice ok 1.a3f1"}
	if got := buttonData(h.api.lastCall("sendMessage").Body); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestEditReplyMasksGroup(t *testing.T) {
	h, _ := outboundHarness(t)
	r := h.ch.newRenderer(ctx, -100, "123", true)
	r.userID = "7"
	r.editMsgID = 55
	r.AckReply(gateway.Reply{Text: ibanText, Edit: true})
	if n := h.api.count("editMessageText"); n != 1 {
		t.Fatalf("%d edits", n)
	}
	if got := allTexts(h); strings.Contains(got, "GB82 WEST 1234") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestAckReplyPrivateNotMasked(t *testing.T) {
	h, _ := outboundHarness(t)
	r := h.ch.newRenderer(ctx, 7, "", true)
	r.userID = "7"
	r.AckReply(gateway.Reply{Text: ibanText})
	if got := allTexts(h); !strings.Contains(got, "GB82 WEST 1234") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestSendOwnerReplyHomeChat(t *testing.T) {
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"7"} })
	err := h.ch.SendOwnerReply(ctx, gateway.Reply{Text: "🗂 **Skill curator**",
		Buttons: [][]gateway.Button{{{Text: "✓ OK", Data: "/notice ok 3.beef"}}}})
	if err != nil {
		t.Fatal(err)
	}
	c := h.api.lastCall("sendMessage")
	if c.Body["chat_id"] != float64(7) {
		t.Fatalf("chat_id %v", c.Body["chat_id"])
	}
	if got, want := buttonData(c.Body), []string{"cmd:7:/notice ok 3.beef"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSendOwnerReplyNoOwner(t *testing.T) {
	h := harness(t, &tgProv{}, nil)
	err := h.ch.SendOwnerReply(ctx, gateway.Reply{Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "no owner chat") {
		t.Fatalf("err %v", err)
	}
	err = h.ch.SendPrivateReply(ctx, "", gateway.Reply{Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "no owner chat") {
		t.Fatalf("err %v", err)
	}
}

func TestSendPrivateReply(t *testing.T) {
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"7"} })
	err := h.ch.SendPrivateReply(ctx, "7", gateway.Reply{Text: "hi",
		Buttons: [][]gateway.Button{{{Text: "✓ OK", Data: "/notice ok 3.beef"}}}})
	if err != nil {
		t.Fatal(err)
	}
	c := h.api.lastCall("sendMessage")
	if c.Body["chat_id"] != float64(7) {
		t.Fatalf("chat_id %v", c.Body["chat_id"])
	}
	if got, want := buttonData(c.Body), []string{"cmd:7:/notice ok 3.beef"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestChatHasMember(t *testing.T) {
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"7"} })
	h.api.setResult(func(method string, body map[string]any) any {
		switch method {
		case "getChatMemberCount":
			if body["chat_id"] == float64(-200) {
				return 2
			}
			return 3
		case "getChatMember":
			return map[string]any{"status": "member", "user": map[string]any{"id": 7}}
		}
		return nil
	})
	if !h.ch.ChatHasMember(ctx, -100) {
		t.Fatal("3 users, 1 owner: want a member")
	}
	if !h.ch.ChatHasMember(ctx, -100) || h.api.count("getChatMemberCount") != 1 {
		t.Fatalf("the second call must use the cache: %d calls", h.api.count("getChatMemberCount"))
	}
	if h.ch.ChatHasMember(ctx, -200) {
		t.Fatal("chat -200 has the owner only")
	}
	before := len(h.api.all())
	if h.ch.ChatHasMember(ctx, 7) || len(h.api.all()) != before {
		t.Fatal("a private chat gives false with no API call")
	}
	h.api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "getChatMemberCount" {
			return &APIError{Code: 400, Description: "Bad Request: boom"}
		}
		return nil
	})
	if !h.ch.ChatHasMember(ctx, -300) {
		t.Fatal("an API error must give true")
	}

	all := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowAllUsers = true })
	if all.ch.ChatHasMember(ctx, -100) || len(all.api.all()) != 0 {
		t.Fatal("allow_all_users gives false with no API call")
	}
}

func TestRefreshCommandsSetsMenu(t *testing.T) {
	h := harness(t, &tgProv{}, nil)
	if err := h.ch.RefreshCommands(ctx); err != nil {
		t.Fatal(err)
	}
	if n := h.api.count("setMyCommands"); n != 1 {
		t.Fatalf("%d setMyCommands calls", n)
	}
}

// A card for the turn of a member goes to the home owner chat, so its buttons belong to the
// owner of that chat, not to the member.
func TestSendPrivateReplyBindsOwnerChat(t *testing.T) {
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"7"} })
	err := h.ch.SendPrivateReply(ctx, "99", gateway.Reply{Text: "hi",
		Buttons: [][]gateway.Button{{{Text: "✓ OK", Data: "/notice ok 3.beef"}}}})
	if err != nil {
		t.Fatal(err)
	}
	c := h.api.lastCall("sendMessage")
	if c.Body["chat_id"] != float64(7) {
		t.Fatalf("chat_id %v", c.Body["chat_id"])
	}
	if got, want := buttonData(c.Body), []string{"cmd:7:/notice ok 3.beef"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
