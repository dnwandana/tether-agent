package telegram

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/tools"
)

func interactions(t *testing.T) (*Interactions, *fakeBot) {
	t.Helper()
	api := newFakeBot(t)
	return &Interactions{C: api.client()}, api
}

// waitSends waits until the fake got n sendMessage calls.
func waitSends(t *testing.T, api *fakeBot, n int) {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for api.count("sendMessage") < n {
		if time.Now().After(end) {
			t.Fatal("no sendMessage", api.methods())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// keyboardOf returns the buttons of the n-th sendMessage call (from 0), as text and data.
func keyboardOf(api *fakeBot, n int) (texts, data []string) {
	i := 0
	for _, c := range api.all() {
		if c.Method != "sendMessage" {
			continue
		}
		if i == n {
			rm, _ := c.Body["reply_markup"].(map[string]any)
			rows, _ := rm["inline_keyboard"].([]any)
			for _, row := range rows {
				for _, b := range row.([]any) {
					bm := b.(map[string]any)
					texts = append(texts, bm["text"].(string))
					data = append(data, bm["callback_data"].(string))
				}
			}
			return texts, data
		}
		i++
	}
	return nil, nil
}

// lastKeyboard returns the buttons of the last editMessageText call.
func lastEditKeyboard(api *fakeBot) []string {
	rm, _ := api.lastCall("editMessageText").Body["reply_markup"].(map[string]any)
	rows, _ := rm["inline_keyboard"].([]any)
	var out []string
	for _, row := range rows {
		for _, b := range row.([]any) {
			out = append(out, b.(map[string]any)["text"].(string))
		}
	}
	return out
}

// press returns a button press with data in chat.
func press(chat int64, data string) CallbackQuery {
	return CallbackQuery{ID: "cb1", From: User{ID: 5}, Message: &Message{MessageID: 101, Chat: Chat{ID: chat}}, Data: data}
}

func TestApprovalButtonsAndEdit(t *testing.T) {
	it, api := interactions(t)
	go func() {
		waitSends(t, api, 1)
		_, data := keyboardOf(api, 0)
		it.HandleCallback(ctx, press(5, data[1]))
	}()
	sc, err := it.For(5, "").PromptApproval(ctx, security.ApprovalRequest{SessionKey: "telegram:dm:5", Command: "rm -rf /tmp/x", Description: "recursive delete"})
	if err != nil || sc != security.ScopeSession {
		t.Fatal(sc, err)
	}
	if api.lastCall("editMessageText").Body["text"] != ToMarkdownV2("✅ **Allowed in this session**\n```\nrm -rf /tmp/x\n```") {
		t.Fatal(api.lastCall("editMessageText").Body)
	}
	_, data := keyboardOf(api, 0)
	if !strings.HasPrefix(data[0], "ap:") || !strings.HasSuffix(data[0], ":once") || len(strings.Split(data[0], ":")[1]) != 16 {
		t.Fatal(data)
	}
	body := api.lastCall("sendMessage").Body
	if body["chat_id"] != float64(5) || !strings.Contains(StripMarkdownV2(body["text"].(string)), "The agent wants to run a recursive delete.") ||
		!strings.Contains(body["text"].(string), "rm -rf /tmp/x") {
		t.Fatal(body)
	}
	if api.count("answerCallbackQuery") != 1 {
		t.Fatal("the button press must be answered", api.methods())
	}
}

func TestApprovalTimeoutEdits(t *testing.T) {
	it, api := interactions(t)
	tctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	sc, err := it.For(5, "").PromptApproval(tctx, security.ApprovalRequest{SessionKey: "telegram:dm:5", Command: "rm -rf /", Description: "x"})
	if sc != security.ScopeTimeout || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(sc, err)
	}
	if api.lastCall("editMessageText").Body["text"] != ToMarkdownV2("⌛ **Denied: no answer**\n```\nrm -rf /\n```") {
		t.Fatal(api.all())
	}
	if it.ResolveApprovals(5, security.ScopeOnce, true) != 0 {
		t.Fatal("a timed out approval must not stay pending")
	}
}

func TestApproveAllFallback(t *testing.T) {
	it, api := interactions(t)
	type res struct {
		sc  security.Scope
		err error
	}
	out := make(chan res, 2)
	for i := 0; i < 2; i++ {
		go func() {
			sc, err := it.For(5, "").PromptApproval(ctx, security.ApprovalRequest{SessionKey: "telegram:dm:5", Command: "rm -rf /x", Description: "d"})
			out <- res{sc, err}
		}()
	}
	waitSends(t, api, 2)
	if n := it.ResolveApprovals(5, security.ScopeOnce, true); n != 2 {
		t.Fatal(n)
	}
	for i := 0; i < 2; i++ {
		if r := <-out; r.sc != security.ScopeOnce || r.err != nil {
			t.Fatal(r)
		}
	}
}

func TestApprovalPromptText(t *testing.T) {
	it, api := interactions(t)
	it.ApprovalTimeout = 10 * time.Minute
	prompt := func(req security.ApprovalRequest) {
		tctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		_, _ = it.For(5, "").PromptApproval(tctx, req)
	}
	prompt(security.ApprovalRequest{Command: "rm -rf ./build", Description: "recursive delete"})
	want := "🛡 **Approval needed**\nThe agent wants to run a recursive delete.\n```\nrm -rf ./build\n```\n_No answer in 10 min = deny._"
	if got := api.lastCall("sendMessage").Body["text"]; got != ToMarkdownV2(want) {
		t.Fatalf("got %q, want %q", got, ToMarkdownV2(want))
	}
	texts, _ := keyboardOf(api, 0)
	if !reflect.DeepEqual(texts, []string{"✅ Allow once", "✅ Allow in this session", "✅ Always allow", "❌ Deny"}) {
		t.Fatal(texts)
	}
	prompt(security.ApprovalRequest{Command: "rm -rf ./build"})
	want = "🛡 **Approval needed**\nThe agent wants to run a dangerous command.\n```\nrm -rf ./build\n```\n_No answer in 10 min = deny._"
	if got := api.lastCall("sendMessage").Body["text"]; got != ToMarkdownV2(want) {
		t.Fatalf("got %q, want %q", got, ToMarkdownV2(want))
	}
	prompt(security.ApprovalRequest{Command: "rm -rf ./build", Description: "recursive delete", ChildName: "researcher"})
	want = "🛡 **Approval needed**\nThe agent wants to run a recursive delete (from subagent researcher).\n```\nrm -rf ./build\n```\n_No answer in 10 min = deny._"
	if got := api.lastCall("sendMessage").Body["text"]; got != ToMarkdownV2(want) {
		t.Fatalf("got %q, want %q", got, ToMarkdownV2(want))
	}
	it.ApprovalTimeout = 0
	prompt(security.ApprovalRequest{Command: "rm -rf ./build", Description: "recursive delete"})
	want = "🛡 **Approval needed**\nThe agent wants to run a recursive delete.\n```\nrm -rf ./build\n```"
	if got := api.lastCall("sendMessage").Body["text"]; got != ToMarkdownV2(want) {
		t.Fatalf("got %q, want %q", got, ToMarkdownV2(want))
	}
}

func TestApprovalEditKeepsCommand(t *testing.T) {
	const block = "\n```\nrm -rf ./build\n```"
	for i, c := range []struct{ label, popup string }{
		{"✅ **Allowed once**", "✅ Allowed once"},
		{"✅ **Allowed in this session**", "✅ Allowed in this session"},
		{"✅ **Always allowed**", "✅ Always allowed"},
		{"❌ **Denied**", "❌ Denied"},
	} {
		it, api := interactions(t)
		go func() {
			waitSends(t, api, 1)
			_, data := keyboardOf(api, 0)
			it.HandleCallback(ctx, press(5, data[i]))
		}()
		if _, err := it.For(5, "").PromptApproval(ctx, security.ApprovalRequest{Command: "rm -rf ./build", Description: "d"}); err != nil {
			t.Fatal(err)
		}
		edit := api.lastCall("editMessageText").Body
		if edit["text"] != ToMarkdownV2(c.label+block) || edit["parse_mode"] != "MarkdownV2" || edit["reply_markup"] != nil {
			t.Fatalf("%d: %v", i, edit)
		}
		if got := api.lastCall("answerCallbackQuery").Body["text"]; got != c.popup {
			t.Fatalf("%d: pop-up %q", i, got)
		}
	}
	it, api := interactions(t)
	it.ApprovalTimeout = 10 * time.Minute
	tctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	_, _ = it.For(5, "").PromptApproval(tctx, security.ApprovalRequest{Command: "rm -rf ./build", Description: "d"})
	if got := api.lastCall("editMessageText").Body["text"]; got != ToMarkdownV2("⌛ **Denied: no answer in 10 min**"+block) {
		t.Fatal(got)
	}
}

func TestFormatWait(t *testing.T) {
	for d, want := range map[time.Duration]string{
		600 * time.Second: "10 min",
		45 * time.Second:  "45 s",
		90 * time.Second:  "1 min",
		time.Minute:       "1 min",
	} {
		if got := formatWait(d); got != want {
			t.Errorf("formatWait(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestResolveReplyTexts(t *testing.T) {
	for _, c := range []struct {
		name  string
		scope security.Scope
		n     int
		want  string
	}{
		{"approve", security.ScopeOnce, 1, "✅ **Allowed once**\nThe agent continues."},
		{"approve", security.ScopeSession, 1, "✅ **Allowed in this session**\nThe agent continues."},
		{"approve", security.ScopeAlways, 1, "✅ **Always allowed**\nThe agent continues."},
		{"approve", security.ScopeOnce, 3, "✅ **Allowed 3 commands**\nThe agent continues."},
		{"approve", security.ScopeSession, 2, "✅ **Allowed 2 commands**\nThe agent continues."},
		{"deny", security.ScopeDeny, 1, "❌ **Denied**\nThe agent continues without the command."},
		{"deny", security.ScopeDeny, 2, "❌ **Denied 2 commands**\nThe agent continues without them."},
		{"approve", security.ScopeOnce, 0, "💤 **No command waits for approval**"},
		{"deny", security.ScopeDeny, 0, "💤 **No command waits for approval**"},
	} {
		if got := resolveReply(c.name, c.scope, c.n); got != c.want {
			t.Errorf("resolveReply(%q, %v, %d) = %q, want %q", c.name, c.scope, c.n, got, c.want)
		}
	}
}

func TestTapErrorTexts(t *testing.T) {
	for got, want := range map[string]string{
		resolvedText:    "✔️ This prompt has an answer already.",
		notAuthorized:   "⛔ Only the owner can answer this prompt.",
		wrongChat:       "⛔ This button is for another chat.",
		invalidCallback: "⚠️ This button is too old. Send the command again.",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestApproveOldestFirst(t *testing.T) {
	it, api := interactions(t)
	first := make(chan security.Scope, 1)
	go func() {
		sc, _ := it.For(5, "").PromptApproval(ctx, security.ApprovalRequest{SessionKey: "telegram:dm:5", Command: "a", Description: "first"})
		first <- sc
	}()
	waitSends(t, api, 1)
	second := make(chan security.Scope, 1)
	go func() {
		sc, _ := it.For(5, "").PromptApproval(ctx, security.ApprovalRequest{SessionKey: "telegram:dm:5", Command: "b", Description: "second"})
		second <- sc
	}()
	waitSends(t, api, 2)
	if it.ResolveApprovals(7, security.ScopeOnce, false) != 0 {
		t.Fatal("an approval of another chat must not resolve")
	}
	if it.ResolveApprovals(5, security.ScopeDeny, false) != 1 || <-first != security.ScopeDeny {
		t.Fatal("the oldest approval must resolve first")
	}
	if it.ResolveApprovals(5, security.ScopeAlways, false) != 1 || <-second != security.ScopeAlways {
		t.Fatal("second")
	}
}

func TestChildNameShown(t *testing.T) {
	it, api := interactions(t)
	tctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	_, _ = it.For(5, "").PromptApproval(tctx, security.ApprovalRequest{SessionKey: "telegram:dm:5", Command: "rm -rf /x", Description: "d", ChildName: "2/5"})
	if !strings.Contains(StripMarkdownV2(api.lastCall("sendMessage").Body["text"].(string)), "(from subagent 2/5)") {
		t.Fatal(api.lastCall("sendMessage").Body)
	}
}

func TestCallbackFromOtherChatRefused(t *testing.T) {
	it, api := interactions(t)
	done := make(chan security.Scope, 1)
	go func() {
		sc, _ := it.For(5, "").PromptApproval(ctx, security.ApprovalRequest{SessionKey: "telegram:dm:5", Command: "rm -rf /x", Description: "d"})
		done <- sc
	}()
	waitSends(t, api, 1)
	_, data := keyboardOf(api, 0)
	if !it.HandleCallback(ctx, press(6, data[0])) {
		t.Fatal("the data is an approval")
	}
	select {
	case <-done:
		t.Fatal("a press from another chat must not approve")
	case <-time.After(30 * time.Millisecond):
	}
	it.Authorized = func(context.Context, CallbackQuery) bool { return false }
	it.HandleCallback(ctx, press(5, data[0]))
	select {
	case <-done:
		t.Fatal("a press from an unauthorized user must not approve")
	case <-time.After(30 * time.Millisecond):
	}
	it.Authorized = nil
	it.HandleCallback(ctx, press(5, data[3]))
	if sc := <-done; sc != security.ScopeDeny {
		t.Fatal(sc)
	}
	if it.HandleCallback(ctx, press(5, "other:1:2")) {
		t.Fatal("unknown data must return false")
	}
}

func TestAskSingleChoice(t *testing.T) {
	it, api := interactions(t)
	go func() {
		waitSends(t, api, 1)
		_, data := keyboardOf(api, 0)
		it.HandleCallback(ctx, press(5, data[1]))
	}()
	ans, err := it.For(5, "").Ask(ctx, tools.Question{Text: "Which?", Choices: []string{"a", "b"}})
	if err != nil || !reflect.DeepEqual(ans.Choices, []string{"b"}) {
		t.Fatal(ans, err)
	}
	if texts, _ := keyboardOf(api, 0); !reflect.DeepEqual(texts, []string{"a", "b"}) {
		t.Fatal(texts)
	}
	if !strings.Contains(api.lastCall("editMessageText").Body["text"].(string), "✅ b") {
		t.Fatal(api.lastCall("editMessageText").Body)
	}
}

func TestAskMultiSelectDone(t *testing.T) {
	it, api := interactions(t)
	go func() {
		waitSends(t, api, 1)
		_, data := keyboardOf(api, 0)
		it.HandleCallback(ctx, press(5, data[0]))
		it.HandleCallback(ctx, press(5, data[2]))
		if got := lastEditKeyboard(api); !reflect.DeepEqual(got, []string{"✓ a", "b", "✓ c", "Done"}) {
			t.Error(got)
		}
		it.HandleCallback(ctx, press(5, data[3]))
	}()
	ans, err := it.For(5, "").Ask(ctx, tools.Question{Text: "Which?", Choices: []string{"a", "b", "c"}, Multi: true})
	if err != nil || !reflect.DeepEqual(ans.Choices, []string{"a", "c"}) {
		t.Fatal(ans, err)
	}
}

func TestAskFreeTextNextMessage(t *testing.T) {
	prov := &tgProv{}
	h := harness(t, prov, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	type res struct {
		a   tools.Answer
		err error
	}
	out := make(chan res, 1)
	go func() {
		a, err := h.ch.UI.For(5, "").Ask(ctx, tools.Question{Text: "Favourite color?"})
		out <- res{a, err}
	}()
	waitSends(t, h.api, 1)
	h.update(dmText(5, "blue"))
	h.waitIdle()
	r := <-out
	if r.err != nil || r.a.Text != "blue" {
		t.Fatal(r)
	}
	if prov.count() != 0 || h.session("telegram:dm:5") != nil {
		t.Fatal("the answer must not go to the agent")
	}
	// The next text goes to the agent again.
	h.update(dmText(5, "thanks"))
	h.waitIdle()
	if prov.count() != 1 {
		t.Fatal(prov.count())
	}
}

func TestApproveCommandInChat(t *testing.T) {
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	out := make(chan security.Scope, 1)
	go func() {
		sc, _ := h.ch.UI.For(5, "").PromptApproval(ctx, security.ApprovalRequest{Command: "rm -rf /x", Description: "d"})
		out <- sc
	}()
	waitSends(t, h.api, 1)
	h.update(dmText(5, "/approve session"))
	h.waitIdle()
	if sc := <-out; sc != security.ScopeSession {
		t.Fatal(sc)
	}
	if !strings.Contains(h.sentTexts(), "✅ Allowed in this session\nThe agent continues.") {
		t.Fatal(h.sentTexts())
	}
	h.update(dmText(5, "/deny"))
	h.waitIdle()
	if !strings.Contains(h.sentTexts(), "💤 No command waits for approval") {
		t.Fatal(h.sentTexts())
	}
}

func TestCallbackUpdateRouted(t *testing.T) {
	h := harness(t, &tgProv{}, func(c *config.TelegramConfig) { c.AllowedUsers = []string{"5"} })
	out := make(chan security.Scope, 1)
	go func() {
		sc, _ := h.ch.UI.For(5, "").PromptApproval(ctx, security.ApprovalRequest{Command: "rm -rf /x", Description: "d"})
		out <- sc
	}()
	waitSends(t, h.api, 1)
	_, data := keyboardOf(h.api, 0)
	cq := press(5, data[0])
	cq.From = User{ID: 9, FirstName: "Stranger"}
	h.ch.handleUpdate(ctx, h.gw, Update{UpdateID: 1, CallbackQuery: &cq})
	select {
	case <-out:
		t.Fatal("an unauthorized user must not approve")
	case <-time.After(30 * time.Millisecond):
	}
	cq.From = User{ID: 5}
	h.ch.handleUpdate(ctx, h.gw, Update{UpdateID: 2, CallbackQuery: &cq})
	if sc := <-out; sc != security.ScopeOnce {
		t.Fatal(sc)
	}
}
