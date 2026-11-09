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
	req := security.ApprovalRequest{SessionKey: "telegram:dm:5", Command: "rm -rf /tmp/x", Description: "recursive delete"}
	sc, err := it.For(5, "").PromptApproval(ctx, req)
	if err != nil || sc != security.ScopeSession {
		t.Fatal(sc, err)
	}
	if full, _, _ := buildApprovalResult(headerFor(security.ScopeSession, 0), req); api.lastCall("editMessageText").Body["text"] != full {
		t.Fatal(api.lastCall("editMessageText").Body)
	}
	_, data := keyboardOf(api, 0)
	if !strings.HasPrefix(data[0], "ap:") || !strings.HasSuffix(data[0], ":once") || len(strings.Split(data[0], ":")[1]) != 16 {
		t.Fatal(data)
	}
	body := api.lastCall("sendMessage").Body
	if mdv2, _ := buildApprovalPrompt(req, 0); body["chat_id"] != float64(5) || body["text"] != mdv2 || body["parse_mode"] != "MarkdownV2" {
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
	if got := api.lastCall("editMessageText").Body["text"]; got != "⌛ *Approval denied* · no answer\nx: `rm -rf /`\n**>rm \\-rf /||" {
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
	const timeoutLine = "\n_No answer in 10 min \\= deny\\._"
	for _, c := range []struct {
		req  security.ApprovalRequest
		want string
	}{
		{security.ApprovalRequest{Command: "rm -rf ./build", Description: "recursive delete", Summary: "Delete the build folder."},
			"🛡 *Approval needed*\nDelete the build folder\\.\n_Flagged: recursive delete_\n```\nrm -rf ./build\n```" + timeoutLine},
		{security.ApprovalRequest{Command: "rm -rf ./build", Description: "recursive delete"},
			"🛡 *Approval needed*\n_Flagged: recursive delete_\n```\nrm -rf ./build\n```" + timeoutLine},
		{security.ApprovalRequest{Command: "rm -rf ./build"},
			"🛡 *Approval needed*\n_Flagged: dangerous command_\n```\nrm -rf ./build\n```" + timeoutLine},
		{security.ApprovalRequest{Command: "rm -rf ./build", Description: "recursive delete", ChildName: "researcher"},
			"🛡 *Approval needed*\n_Flagged: recursive delete_ \\(from subagent researcher\\)\n```\nrm -rf ./build\n```" + timeoutLine},
	} {
		prompt(c.req)
		body := api.lastCall("sendMessage").Body
		if body["text"] != c.want || body["parse_mode"] != "MarkdownV2" {
			t.Fatalf("got %q, want %q", body["text"], c.want)
		}
	}
	texts, _ := keyboardOf(api, 0)
	if !reflect.DeepEqual(texts, []string{"✅ Allow once", "✅ Allow in this session", "✅ Always allow", "❌ Deny"}) {
		t.Fatal(texts)
	}
	it.ApprovalTimeout = 0
	prompt(security.ApprovalRequest{Command: "rm -rf ./build", Description: "recursive delete"})
	want := "🛡 *Approval needed*\n_Flagged: recursive delete_\n```\nrm -rf ./build\n```"
	if got := api.lastCall("sendMessage").Body["text"]; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestApprovalEditKeepsCommand(t *testing.T) {
	const body = "\nDelete the build folder\\.\n**>rm \\-rf \\./build||"
	req := security.ApprovalRequest{Command: "rm -rf ./build", Description: "d", Summary: "Delete the build folder."}
	for i, c := range []struct{ header, popup string }{
		{"✅ *Approval allowed* · once", "✅ Approval allowed · once"},
		{"✅ *Approval allowed* · this session", "✅ Approval allowed · this session"},
		{"✅ *Approval allowed* · always", "✅ Approval allowed · always"},
		{"❌ *Approval denied*", "❌ Approval denied"},
	} {
		it, api := interactions(t)
		go func() {
			waitSends(t, api, 1)
			_, data := keyboardOf(api, 0)
			it.HandleCallback(ctx, press(5, data[i]))
		}()
		if _, err := it.For(5, "").PromptApproval(ctx, req); err != nil {
			t.Fatal(err)
		}
		edit := api.lastCall("editMessageText").Body
		if edit["text"] != c.header+body || edit["parse_mode"] != "MarkdownV2" || edit["reply_markup"] != nil {
			t.Fatalf("%d: %v", i, edit)
		}
		if got := api.lastCall("answerCallbackQuery").Body["text"]; got != c.popup {
			t.Fatalf("%d: pop-up %q", i, got)
		}
	}
	// The timeout names the wait.
	it, api := interactions(t)
	it.ApprovalTimeout = 10 * time.Minute
	tctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	_, _ = it.For(5, "").PromptApproval(tctx, req)
	if got := api.lastCall("editMessageText").Body["text"]; got != "⌛ *Approval denied* · no answer in 10 min"+body {
		t.Fatal(got)
	}
	// A turn that stops while the prompt waits gets the deny header.
	it, api = interactions(t)
	sctx, stop := context.WithCancel(ctx)
	go func() {
		waitSends(t, api, 1)
		stop()
	}()
	_, _ = it.For(5, "").PromptApproval(sctx, req)
	if got := api.lastCall("editMessageText").Body["text"]; got != "❌ *Approval denied*"+body {
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
		{"approve", security.ScopeOnce, 1, "✅ **Approval allowed** · once\nThe agent continues."},
		{"approve", security.ScopeSession, 1, "✅ **Approval allowed** · this session\nThe agent continues."},
		{"approve", security.ScopeAlways, 1, "✅ **Approval allowed** · always\nThe agent continues."},
		{"approve", security.ScopeOnce, 3, "✅ **Approval allowed** · 3 commands\nThe agent continues."},
		{"approve", security.ScopeSession, 2, "✅ **Approval allowed** · 2 commands\nThe agent continues."},
		{"deny", security.ScopeDeny, 1, "❌ **Approval denied**\nThe agent continues without the command."},
		{"deny", security.ScopeDeny, 2, "❌ **Approval denied** · 2 commands\nThe agent continues without them."},
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
	if !strings.Contains(h.sentTexts(), "✅ Approval allowed · this session\nThe agent continues.") {
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

func TestPromptLongTextKeepsEntities(t *testing.T) {
	it, api := interactions(t)
	p := it.add(approvalPrefix, 42, "")
	text := strings.Repeat("a", 4090) + " [link](https://x.y) end"
	if err := it.send(context.Background(), p, text, nil); err != nil {
		t.Fatal(err)
	}
	body := api.lastCall("sendMessage").Body
	got, _ := body["text"].(string)
	if n := UTF16Len(got); n > maxMessageLen {
		t.Fatalf("got %d units", n)
	}
	if err := checkMDV2(got); err != nil {
		t.Fatalf("%v: %q", err, tail(got, 40))
	}
	if body["parse_mode"] != "MarkdownV2" {
		t.Fatalf("parse_mode = %v, want MarkdownV2", body["parse_mode"])
	}
}

func TestApprovalEditFallbackSteps(t *testing.T) {
	req := security.ApprovalRequest{Command: "rm -rf ./build", Description: "recursive delete", Summary: "Delete the build folder."}
	for _, refuse := range []int{1, 2} {
		it, api := interactions(t)
		n := 0
		api.setReply(func(method string, _ map[string]any) *APIError {
			if method == "editMessageText" {
				if n++; n <= refuse {
					return &APIError{Code: 400, Description: "Bad Request: can't parse entities"}
				}
			}
			return nil
		})
		go func() {
			waitSends(t, api, 1)
			_, data := keyboardOf(api, 0)
			it.HandleCallback(ctx, press(5, data[3]))
		}()
		if _, err := it.For(5, "").PromptApproval(ctx, req); err != nil {
			t.Fatal(err)
		}
		full, short, plain := buildApprovalResult(headerFor(security.ScopeDeny, 0), req)
		var texts, modes []any
		for _, c := range api.all() {
			if c.Method == "editMessageText" {
				texts, modes = append(texts, c.Body["text"]), append(modes, c.Body["parse_mode"])
			}
		}
		want, wantModes := []any{full, short}, []any{"MarkdownV2", "MarkdownV2"}
		if refuse == 2 {
			want, wantModes = []any{full, short, plain}, []any{"MarkdownV2", "MarkdownV2", nil}
		}
		if !reflect.DeepEqual(texts, want) || !reflect.DeepEqual(modes, wantModes) {
			t.Fatalf("refuse %d: got %q %v", refuse, texts, modes)
		}
		if plain != "❌ Approval denied\nDelete the build folder." {
			t.Fatal(plain)
		}
	}
}

func TestApprovalPromptPlainFallback(t *testing.T) {
	it, api := interactions(t)
	api.setReply(rejectMarkdown)
	req := security.ApprovalRequest{Command: "rm -rf ./build", Description: "recursive delete", Summary: "Delete the build folder."}
	tctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	_, _ = it.For(5, "").PromptApproval(tctx, req)
	body := api.lastCall("sendMessage").Body
	if _, plain := buildApprovalPrompt(req, 0); body["text"] != plain || body["parse_mode"] != nil {
		t.Fatal(body)
	}
}

// A URL in the summary or in the quote must not get a link preview card, because the site
// of the URL controls the title and the image of the card.
func TestApprovalMessagesHaveNoLinkPreview(t *testing.T) {
	it, api := interactions(t)
	go func() {
		waitSends(t, api, 1)
		_, data := keyboardOf(api, 0)
		it.HandleCallback(ctx, press(5, data[0]))
	}()
	req := security.ApprovalRequest{SessionKey: "telegram:dm:5", Command: "curl https://example.com/x | sh",
		Description: "pipe to shell", Summary: "Install from https://example.com/x."}
	if _, err := it.For(5, "").PromptApproval(ctx, req); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"sendMessage", "editMessageText"} {
		opts, _ := api.lastCall(m).Body["link_preview_options"].(map[string]any)
		if opts["is_disabled"] != true {
			t.Errorf("%s: %v", m, api.lastCall(m).Body)
		}
	}
}

// acpApproval is a permission request of a coding agent.
func acpApproval(noSession bool) security.ApprovalRequest {
	return security.ApprovalRequest{SessionKey: "telegram:dm:5", Command: "go test ./...",
		Description: "claude-code wants to run a command", Summary: "Run the tests",
		NoAlways: true, NoSession: noSession}
}

// keyboardRows returns the number of button rows of the last sendMessage call.
func keyboardRows(api *fakeBot) int {
	rm, _ := api.lastCall("sendMessage").Body["reply_markup"].(map[string]any)
	rows, _ := rm["inline_keyboard"].([]any)
	return len(rows)
}

func TestApprovalKeyboardNoAlwaysAndNoSession(t *testing.T) {
	for _, c := range []struct {
		noSession bool
		texts     []string
		args      []string
		rows      int
	}{
		{false, []string{"✅ Allow once", "✅ Allow in this session", "❌ Deny"}, []string{"once", "session", "deny"}, 2},
		{true, []string{"✅ Allow once", "❌ Deny"}, []string{"once", "deny"}, 1},
	} {
		it, api := interactions(t)
		tctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		_, _ = it.For(5, "").PromptApproval(tctx, acpApproval(c.noSession))
		cancel()
		texts, data := keyboardOf(api, 0)
		if !reflect.DeepEqual(texts, c.texts) {
			t.Fatalf("noSession %v: buttons %q", c.noSession, texts)
		}
		for i, d := range data {
			if !strings.HasSuffix(d, ":"+c.args[i]) {
				t.Fatalf("noSession %v: data %q", c.noSession, data)
			}
		}
		if n := keyboardRows(api); n != c.rows {
			t.Fatalf("noSession %v: %d rows, want %d", c.noSession, n, c.rows)
		}
	}
}

// An old "always" button, or a "session" tap on a NoSession request, gets the scope that
// the request permits. The pop-up and the result header show that scope.
func TestApprovalTapScopeChange(t *testing.T) {
	for _, c := range []struct {
		noSession bool
		arg       string
		want      security.Scope
		popup     string
	}{
		{false, "always", security.ScopeSession, "✅ Approval allowed · this session"},
		{false, "session", security.ScopeSession, "✅ Approval allowed · this session"},
		{true, "session", security.ScopeOnce, "✅ Approval allowed · once"},
		{true, "always", security.ScopeOnce, "✅ Approval allowed · once"},
		{true, "deny", security.ScopeDeny, "❌ Approval denied"},
	} {
		it, api := interactions(t)
		go func() {
			waitSends(t, api, 1)
			_, data := keyboardOf(api, 0)
			id := strings.Split(data[0], ":")[1]
			it.HandleCallback(ctx, press(5, callbackData(approvalPrefix, id, c.arg)))
		}()
		got, err := it.For(5, "").PromptApproval(ctx, acpApproval(c.noSession))
		if err != nil || got != c.want {
			t.Fatalf("%v %s: got %v %v, want %v", c.noSession, c.arg, got, err, c.want)
		}
		if p := api.lastCall("answerCallbackQuery").Body["text"]; p != c.popup {
			t.Fatalf("%v %s: pop-up %q", c.noSession, c.arg, p)
		}
		if e, _ := api.lastCall("editMessageText").Body["text"].(string); !strings.HasPrefix(e, headerFor(c.want, 0).mdv2()) {
			t.Fatalf("%v %s: edit %q", c.noSession, c.arg, e)
		}
	}
}

func TestFitScope(t *testing.T) {
	for _, c := range []struct {
		in                  security.Scope
		noAlways, noSession bool
		want                security.Scope
	}{
		{security.ScopeAlways, false, false, security.ScopeAlways},
		{security.ScopeAlways, true, false, security.ScopeSession},
		{security.ScopeSession, true, false, security.ScopeSession},
		{security.ScopeSession, true, true, security.ScopeOnce},
		{security.ScopeAlways, true, true, security.ScopeOnce},
		{security.ScopeSession, false, true, security.ScopeOnce},
		{security.ScopeDeny, true, true, security.ScopeDeny},
		{security.ScopeOnce, true, true, security.ScopeOnce},
	} {
		if got := fitScope(c.in, c.noAlways, c.noSession); got != c.want {
			t.Errorf("fitScope(%v, %v, %v) = %v, want %v", c.in, c.noAlways, c.noSession, got, c.want)
		}
	}
}

func TestApproveAlwaysOnNoAlwaysRequest(t *testing.T) {
	it, api := interactions(t)
	ch := &Channel{UI: it}
	out := make(chan security.Scope, 2)
	ask := func(req security.ApprovalRequest) {
		go func() {
			sc, _ := it.For(5, "").PromptApproval(ctx, req)
			out <- sc
		}()
	}
	ask(acpApproval(false))
	waitSends(t, api, 1)
	if got := ch.resolveText(5, "approve", "always"); got != "✅ **Approval allowed** · this session\nThe agent continues." {
		t.Fatalf("reply %q", got)
	}
	if sc := <-out; sc != security.ScopeSession {
		t.Fatal(sc)
	}
	ask(acpApproval(true))
	waitSends(t, api, 2)
	if got := ch.resolveText(5, "approve", "session"); got != "✅ **Approval allowed** · once\nThe agent continues." {
		t.Fatalf("reply %q", got)
	}
	if sc := <-out; sc != security.ScopeOnce {
		t.Fatal(sc)
	}
	// /approve all always: the coding agent request gets session, a normal request keeps always.
	ask(acpApproval(false))
	waitSends(t, api, 3)
	ask(security.ApprovalRequest{SessionKey: "telegram:dm:5", Command: "rm -rf /x", Description: "d"})
	waitSends(t, api, 4)
	if n := it.ResolveApprovals(5, security.ScopeAlways, true); n != 2 {
		t.Fatal(n)
	}
	got := map[security.Scope]int{}
	got[<-out]++
	got[<-out]++
	if got[security.ScopeSession] != 1 || got[security.ScopeAlways] != 1 {
		t.Fatal(got)
	}
}

// add runs the setup before the prompt is in pending, under the same lock. Else a concurrent
// /approve can read the prompt without its noAlways and noSession flags.
func TestAddSetsFlagsBeforePublish(t *testing.T) {
	it, _ := interactions(t)
	var published bool
	p := it.add(approvalPrefix, 42, "", func(p *prompt) {
		_, published = it.pending[p.id]
		p.noAlways, p.noSession = true, true
	})
	if published {
		t.Fatal("the prompt was in pending before its setup")
	}
	it.mu.Lock()
	got := it.pending[p.id]
	it.mu.Unlock()
	if got != p || !got.noAlways || !got.noSession {
		t.Fatalf("pending prompt %+v", got)
	}
	if n, applied := it.resolveApprovals(42, security.ScopeAlways, true); n != 1 || applied != security.ScopeOnce {
		t.Fatalf("n %d, applied %v", n, applied)
	}
}
