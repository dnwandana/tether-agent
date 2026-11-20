package telegram

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/security"
)

// outboundHarness is a harness whose channel masks each send to a chat with a negative id.
// It records the session id of each Outbound call.
func outboundHarness(t *testing.T, steps ...tgStep) (*tgHarness, func() []string) {
	t.Helper()
	h := harness(t, &tgProv{script: steps}, func(c *config.TelegramConfig) { c.AllowAllUsers = true })
	var mu sync.Mutex
	var ids []string
	h.ch.Outbound = func(sid string, _ int64, text string) (string, []security.Finding) {
		mu.Lock()
		ids = append(ids, sid)
		mu.Unlock()
		f := security.Scan(text, nil, nil)
		return security.RedactFindings(text, f), f
	}
	h.ch.PrivateChat = func(chat int64) bool { return chat > 0 }
	return h, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), ids...) }
}

// allTexts returns the text of each send and edit, without the MarkdownV2 markup.
func allTexts(h *tgHarness) string {
	var out []string
	for _, c := range h.api.all() {
		if s, ok := c.Body["text"].(string); ok {
			out = append(out, StripMarkdownV2(s))
		}
	}
	return strings.Join(out, "\n---\n")
}

func TestGroupAnswerIsMasked(t *testing.T) {
	h, ids := outboundHarness(t, tgStep{text: "Pay with 4111 1111 1111 1111 today."})
	h.update(group("how do I pay?"))
	h.waitIdle()
	got := allTexts(h)
	if strings.Contains(got, "4111") || !strings.Contains(got, "[card ••••1111]") {
		t.Fatalf("got:\n%s", got)
	}
	sess := h.session("telegram:group:-100:123")
	if !slices.Contains(ids(), sess.ID) {
		t.Fatalf("the scan did not get the session id %s: %v", sess.ID, ids())
	}
	// The transcript keeps the original answer.
	msgs := h.messages(sess)
	if last := msgs[len(msgs)-1].Msg.Content; !strings.Contains(last, "4111 1111 1111 1111") {
		t.Fatal(last)
	}
}

func TestPrivateAnswerIsNotMasked(t *testing.T) {
	h, ids := outboundHarness(t, tgStep{text: "Pay with 4111 1111 1111 1111 today."})
	h.update(dm("how do I pay?"))
	h.waitIdle()
	if got := allTexts(h); !strings.Contains(got, "4111 1111 1111 1111") {
		t.Fatalf("got:\n%s", got)
	}
	if len(ids()) != 0 {
		t.Fatal("a private chat is never scanned:", ids())
	}
}

func TestChannelSendMasks(t *testing.T) {
	h, _ := outboundHarness(t)
	text := "IBAN GB82 WEST 1234 5698 7654 32"
	if err := h.ch.Send(ctx, gateway.Target{Source: "telegram", ChatID: "-100"}, gateway.Outbound{Text: text}); err != nil {
		t.Fatal(err)
	}
	if got := StripMarkdownV2(h.api.lastText()); got != "IBAN [IBAN GB82••••5432]" {
		t.Fatal(got)
	}
	if err := h.ch.Send(ctx, gateway.Target{Source: "telegram", ChatID: "123"}, gateway.Outbound{Text: text}); err != nil {
		t.Fatal(err)
	}
	if got := StripMarkdownV2(h.api.lastText()); got != text {
		t.Fatal(got)
	}
}

func TestGroupAnswerHoldsFileWithFinding(t *testing.T) {
	dir := t.TempDir()
	report := filepath.Join(dir, "report.txt")
	if err := os.WriteFile(report, []byte("Call +94 77 123 4567\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	clean := filepath.Join(dir, "clean.txt")
	if err := os.WriteFile(clean, []byte("all good\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, _ := outboundHarness(t, tgStep{text: "Here.\nMEDIA:" + report + "\nMEDIA:" + clean})
	h.update(group("send the report"))
	h.waitIdle()
	if n := h.api.count("sendDocument"); n != 1 {
		t.Fatalf("only the clean file goes to the group: %d sends", n)
	}
}

func TestRendererMasksPreviews(t *testing.T) {
	h, _ := outboundHarness(t)
	r := h.ch.newRenderer(ctx, -100, "", false)
	r.SetSession("s1")
	if got := r.maskOut("echo +94 77 123 4567"); got != "echo [phone +94•••••4567]" {
		t.Fatal(got)
	}
	dm := h.ch.newRenderer(ctx, 123, "", true)
	if got := dm.maskOut("echo +94 77 123 4567"); got != "echo +94 77 123 4567" {
		t.Fatal(got)
	}
	var _ gateway.SessionRenderer = r
}

func TestSendRawSkipsScan(t *testing.T) {
	h, ids := outboundHarness(t)
	text := "IBAN GB82 WEST 1234 5698 7654 32"
	if err := h.ch.Send(ctx, gateway.Target{Source: "telegram", ChatID: "-100"}, gateway.Outbound{Text: text, Raw: true}); err != nil {
		t.Fatal(err)
	}
	if got := StripMarkdownV2(h.api.lastText()); got != text {
		t.Fatal(got)
	}
	if len(ids()) != 0 {
		t.Fatal("Raw calls no Outbound:", ids())
	}
}

// The owner note of a masked send goes out only after the send. A send that fails gives no
// note and no button for a message that never went out.
func TestChannelSendNotesOwnerOnlyAfterSend(t *testing.T) {
	h, _ := outboundHarness(t)
	h.ch.Cfg.AllowedUsers = []string{"5"}
	h.ch.Held = &HeldOriginals{}
	h.api.setReply(func(method string, body map[string]any) *APIError {
		if method == "sendMessage" && fmt.Sprint(body["chat_id"]) == "-100" {
			return &APIError{Code: 403, Description: "Forbidden: bot was kicked from the group chat"}
		}
		return nil
	})
	text := "IBAN GB82 WEST 1234 5698 7654 32"
	if err := h.ch.Send(ctx, gateway.Target{Source: "telegram", ChatID: "-100"}, gateway.Outbound{Text: text}); err == nil {
		t.Fatal("the send must fail")
	}
	notes := func() int {
		n := 0
		for _, c := range h.api.all() {
			if c.Method == "sendMessage" && fmt.Sprint(c.Body["chat_id"]) == "5" {
				n++
			}
		}
		return n
	}
	if n := notes(); n != 0 {
		t.Fatalf("a failed send must not note the owner: %d notes", n)
	}
	if err := h.ch.Send(ctx, gateway.Target{Source: "telegram", ChatID: "-101"}, gateway.Outbound{Text: text}); err != nil {
		t.Fatal(err)
	}
	if n := notes(); n != 1 {
		t.Fatalf("a sent message notes the owner once: %d notes", n)
	}
}

// The owner note of a masked answer goes out only after the answer. A failed answer gives no
// note and no button for a message that never went out.
func TestRendererNotesOwnerOnlyAfterSend(t *testing.T) {
	h, _ := outboundHarness(t)
	h.ch.Cfg.AllowedUsers = []string{"5"}
	h.ch.Held = &HeldOriginals{}
	h.api.setReply(func(method string, body map[string]any) *APIError {
		if method == "sendMessage" && fmt.Sprint(body["chat_id"]) == "-100" {
			return &APIError{Code: 403, Description: "Forbidden: bot was kicked from the group chat"}
		}
		return nil
	})
	r := h.ch.newRenderer(ctx, -100, "", false)
	r.SetSession("s1")
	r.Done("IBAN GB82 WEST 1234 5698 7654 32", nil)
	for _, c := range h.api.all() {
		if c.Method == "sendMessage" && fmt.Sprint(c.Body["chat_id"]) == "5" {
			t.Fatal("a failed answer must not note the owner")
		}
	}
}

// A group answer over the message limit with a card in its first part goes out one time. The
// stream masks the final text itself, so the closed part matches the final text.
func TestGroupLongAnswerWithCardNoDuplicate(t *testing.T) {
	h, _ := outboundHarness(t)
	r := h.ch.newRenderer(ctx, -100, "", false)
	r.SetSession("s1")
	first := "START Pay with 4111 1111 1111 1111 today.\n" + strings.Repeat("word ", 900)
	second := "\n" + strings.Repeat("more ", 200) + "END"
	r.Delta(first)
	r.Delta(second)
	r.Done(first+second, nil)
	// The fake bot gives the ids 101, 102, ... to the sends in order.
	final := map[int]string{}
	next := 100
	for _, c := range h.api.all() {
		s, _ := c.Body["text"].(string)
		if strings.Contains(s, "4111") {
			t.Fatalf("a send shows the card: %q", s)
		}
		switch c.Method {
		case "sendMessage":
			next++
			final[next] = s
		case "editMessageText":
			id, _ := c.Body["message_id"].(float64)
			final[int(id)] = s
		}
	}
	starts, ends := 0, 0
	for _, s := range final {
		if strings.Contains(s, "START") {
			starts++
		}
		if strings.Contains(s, "END") {
			ends++
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("the answer must show one time: %d messages with the start, %d with the end", starts, ends)
	}
}

// The owner note of a masked send has no sound only when the send has no sound.
func TestMaskedNoteFollowsNoSound(t *testing.T) {
	for _, quiet := range []bool{false, true} {
		h, _ := outboundHarness(t)
		h.ch.Cfg.AllowedUsers = []string{"5"}
		h.ch.Held = &HeldOriginals{}
		text := "IBAN GB82 WEST 1234 5698 7654 32"
		if err := h.ch.Send(ctx, gateway.Target{Source: "telegram", ChatID: "-100"},
			gateway.Outbound{Text: text, NoSound: quiet}); err != nil {
			t.Fatal(err)
		}
		note := h.api.lastCall("sendMessage")
		if fmt.Sprint(note.Body["chat_id"]) != "5" || (note.Body["disable_notification"] == true) != quiet {
			t.Fatalf("quiet %v: %v", quiet, note.Body)
		}
	}
}
