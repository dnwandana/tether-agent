package telegram

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
)

// streamer returns a Streamer for a group chat (or a DM) on a fake Bot API
// and a fake clock, with the default streaming config.
func streamer(t *testing.T, isDM bool) (*Streamer, *fakeBot, *fakeClock) {
	t.Helper()
	api := newFakeBot(t)
	clk := newFakeClock()
	s := &Streamer{C: api.client(), ChatID: 42, IsDM: isDM, Cfg: config.Default().Telegram.Streaming, Now: clk.Now}
	// No test fires a real timer for a later cursor strip.
	s.AfterFunc = func(time.Duration, func()) {}
	t.Cleanup(func() { _ = s.Finish(ctx, "") })
	return s, api, clk
}

func (b *fakeBot) edits() int { return b.count("editMessageText") }

func TestEditCadence(t *testing.T) {
	s, api, clk := streamer(t, false)
	for _, d := range []string{"Hello", "b", "c"} {
		s.Delta(ctx, d) // < 24 new chars and < 0.8 s → only the first send
	}
	if api.count("sendMessage") != 1 || api.edits() != 0 {
		t.Fatal(api.methods())
	}
	clk.Advance(900 * time.Millisecond)
	s.Delta(ctx, "d")
	if api.edits() != 1 || !strings.HasSuffix(api.lastText(), " ▉") || api.lastText() != "Hellobcd ▉" {
		t.Fatal(api.methods(), api.lastText())
	}
	if c := api.lastCall("editMessageText"); c.Body["parse_mode"] != nil {
		t.Fatal("stream edits are plain text:", c.Body)
	}
}

func TestEditAfter24NewChars(t *testing.T) {
	s, api, _ := streamer(t, false)
	s.Delta(ctx, "Hello")
	s.Delta(ctx, strings.Repeat("x", 23))
	if api.edits() != 0 {
		t.Fatal(api.methods())
	}
	s.Delta(ctx, "y")
	if api.edits() != 1 {
		t.Fatal(api.methods())
	}
}

func TestIdleTimerFlushesPendingText(t *testing.T) {
	api := newFakeBot(t)
	cfg := config.Default().Telegram.Streaming
	cfg.EditIntervalS = 0.03
	s := &Streamer{C: api.client(), ChatID: 1, Cfg: cfg}
	defer s.Finish(ctx, "")
	s.Delta(ctx, "Hello")
	s.Delta(ctx, " there")
	deadline := time.Now().Add(2 * time.Second)
	for api.edits() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if api.lastText() != "Hello there ▉" {
		t.Fatal("pending text was not flushed by the timer:", api.methods(), api.lastText())
	}
}

func TestFinalIsMarkdownV2WithoutCursor(t *testing.T) {
	s, api, _ := streamer(t, false)
	s.Delta(ctx, "Working on it")
	if err := s.Finish(ctx, "**Done**"); err != nil {
		t.Fatal(err)
	}
	c := api.lastCall("editMessageText")
	if c.Body["text"] != "*Done*" || c.Body["parse_mode"] != "MarkdownV2" {
		t.Fatal(c.Body)
	}
	if strings.Contains(api.lastText(), "▉") {
		t.Fatal("cursor in final text")
	}
}

func TestFinishWithoutDeltasSendsMarkdownV2(t *testing.T) {
	s, api, _ := streamer(t, false)
	if err := s.Finish(ctx, "**Done** 1.5"); err != nil {
		t.Fatal(err)
	}
	c := api.lastCall("sendMessage")
	if c.Body["text"] != `*Done* 1\.5` || c.Body["parse_mode"] != "MarkdownV2" || api.edits() != 0 {
		t.Fatal(api.methods(), c.Body)
	}
}

func TestFinishUsesStreamedTextWhenFinalEmpty(t *testing.T) {
	s, api, _ := streamer(t, false)
	s.Delta(ctx, "Hello world")
	if err := s.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if api.lastText() != "Hello world" {
		t.Fatal(api.lastText())
	}
}

func TestParseErrorFallsBackToPlain(t *testing.T) {
	s, api, _ := streamer(t, false)
	api.setReply(func(method string, body map[string]any) *APIError {
		if body["parse_mode"] == "MarkdownV2" {
			return &APIError{Code: 400, Description: "Bad Request: can't parse entities: unclosed"}
		}
		return nil
	})
	s.Delta(ctx, "Working on it")
	if err := s.Finish(ctx, "**Done**"); err != nil {
		t.Fatal(err)
	}
	c := api.lastCall("editMessageText")
	if c.Body["text"] != "**Done**" || c.Body["parse_mode"] != nil {
		t.Fatal("edit must retry with the raw text:", c.Body)
	}

	// A send strips the markup.
	s2, api2, _ := streamer(t, false)
	api2.setReply(api.reply)
	if err := s2.Finish(ctx, "**Done** 1.5"); err != nil {
		t.Fatal(err)
	}
	c = api2.lastCall("sendMessage")
	if c.Body["text"] != "Done 1.5" || c.Body["parse_mode"] != nil {
		t.Fatal("send must strip the markup:", c.Body)
	}
}

func TestNotModifiedFinalIsSuccess(t *testing.T) {
	s, api, _ := streamer(t, false)
	s.Delta(ctx, "Done")
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 400, Description: "Bad Request: message is not modified"}
		}
		return nil
	})
	if err := s.Finish(ctx, "Done"); err != nil {
		t.Fatal(err)
	}
	if api.count("sendMessage") != 1 {
		t.Fatal(api.methods())
	}
}

func TestLongAnswerSplitAndNumbered(t *testing.T) {
	s, api, _ := streamer(t, false)
	para := strings.Repeat("word ", 199) + "end\n"
	final := strings.Repeat(para, 10)[:9000]
	if err := s.Finish(ctx, final); err != nil {
		t.Fatal(err)
	}
	sends := api.textsOf("sendMessage")
	if len(sends) != 3 {
		t.Fatal(len(sends))
	}
	for i, txt := range sends {
		suffix := []string{`\(1/3\)`, `\(2/3\)`, `\(3/3\)`}[i]
		if !strings.HasSuffix(txt, suffix) || UTF16Len(txt) > 4096 {
			t.Fatal(i, UTF16Len(txt), txt[len(txt)-20:])
		}
	}
	// The split cuts at a newline and drops it, so join the parts with one.
	texts := make([]string, len(sends))
	for i, txt := range sends {
		texts[i] = txt[:strings.LastIndex(txt, ` \(`)]
	}
	if StripMarkdownV2(strings.Join(texts, "\n")) != final {
		t.Fatal("text lost in the split")
	}
}

func TestStreamOverflowSplitsAtSafeLimit(t *testing.T) {
	// Hermes TestUtf16OverflowDetection: 2,200 rockets are 4,400 UTF-16 units.
	s, api, clk := streamer(t, false)
	s.Delta(ctx, "Start")
	clk.Advance(time.Second)
	s.Delta(ctx, strings.Repeat("🚀", 2200))
	for _, c := range api.all() {
		if txt, _ := c.Body["text"].(string); UTF16Len(txt) > 4096 {
			t.Fatal("message over the limit:", c.Method, UTF16Len(txt))
		}
	}
	if api.edits() != 1 || api.count("sendMessage") != 2 {
		t.Fatal(api.methods())
	}
	if err := s.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	// The first message holds the start; the second holds the rest.
	first := api.textsOf("editMessageText")[0]
	last := api.lastText()
	total := strings.Count(first, "🚀") + strings.Count(last, "🚀")
	if !strings.HasPrefix(first, "Start") || total != 2200 || strings.Contains(last, "▉") {
		t.Fatal(total, api.methods())
	}
}

func TestFreshSendOverflowNumbered(t *testing.T) {
	// No message yet and the text is over the stream limit: Hermes sends
	// numbered parts at once.
	s, api, _ := streamer(t, false)
	s.Delta(ctx, strings.Repeat("a", 5000))
	sends := api.textsOf("sendMessage")
	if len(sends) != 2 || !strings.HasSuffix(sends[0], " (1/2)") || !strings.HasSuffix(sends[1], " (2/2)") {
		t.Fatal(len(sends))
	}
	if err := s.Finish(ctx, strings.Repeat("a", 5000)); err != nil {
		t.Fatal(err)
	}
	if api.count("sendMessage") != 2 || api.edits() != 0 {
		t.Fatal("delivered text must not be sent again:", api.methods())
	}
}

func TestFreshFinalAfter60s(t *testing.T) {
	s, api, clk := streamer(t, false)
	s.Delta(ctx, "Thinking hard")
	preview := api.lastCall("sendMessage")
	clk.Advance(61 * time.Second)
	if err := s.Finish(ctx, "The answer."); err != nil {
		t.Fatal(err)
	}
	if api.count("sendMessage") != 2 || api.edits() != 0 || api.count("deleteMessage") != 1 {
		t.Fatal(api.methods())
	}
	if api.lastCall("deleteMessage").Body["message_id"] != float64(101) || preview.Body["text"] != "Thinking hard ▉" {
		t.Fatal(api.all())
	}
	if api.lastCall("sendMessage").Body["text"] != `The answer\.` {
		t.Fatal(api.lastText())
	}
}

func TestShortPreviewEditsInPlace(t *testing.T) {
	s, api, clk := streamer(t, false)
	s.Delta(ctx, "Thinking hard")
	clk.Advance(30 * time.Second)
	_ = s.Finish(ctx, "The answer.")
	if api.count("sendMessage") != 1 || api.edits() != 1 || api.count("deleteMessage") != 0 {
		t.Fatal(api.methods())
	}

	// fresh_final_after_s 0 turns the feature off.
	s2, api2, clk2 := streamer(t, false)
	s2.Cfg.FreshFinalAfterS = 0
	s2.Delta(ctx, "Thinking hard")
	clk2.Advance(time.Hour)
	_ = s2.Finish(ctx, "The answer.")
	if api2.count("deleteMessage") != 0 || api2.edits() != 1 {
		t.Fatal(api2.methods())
	}
}

func TestFreshFinalSendFailureFallsBackToEdit(t *testing.T) {
	s, api, clk := streamer(t, false)
	s.Delta(ctx, "Thinking hard")
	clk.Advance(61 * time.Second)
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "sendMessage" {
			return &APIError{Code: 400, Description: "Bad Request: chat not found"}
		}
		return nil
	})
	if err := s.Finish(ctx, "The answer."); err != nil {
		t.Fatal(err)
	}
	if api.edits() != 1 || api.count("deleteMessage") != 0 {
		t.Fatal(api.methods())
	}
}

func TestFloodThreeStrikesFinalOnly(t *testing.T) {
	s, api, clk := streamer(t, false)
	s.Delta(ctx, "Hello")
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 429, Description: "Too Many Requests: retry after 30", RetryAfter: 30}
		}
		return nil
	})
	// The interval doubles after each strike: 0.8 s, 1.6 s, 3.2 s.
	for _, wait := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		clk.Advance(wait)
		s.Delta(ctx, " more")
	}
	strikes := api.edits()
	if strikes != 3 { // 3 strikes, and no strip under flood
		t.Fatal(api.methods())
	}
	for i := 0; i < 5; i++ {
		clk.Advance(20 * time.Second)
		s.Delta(ctx, " text")
	}
	if api.edits() != strikes {
		t.Fatal("edits after three strikes:", api.methods())
	}
	if err := s.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	sends := api.textsOf("sendMessage")
	if len(sends) != 2 || sends[1] != "more more more text text text text text" {
		t.Fatal("want one final send with the unseen tail:", sends)
	}
}

func TestFloodStrikeIntervalDoubles(t *testing.T) {
	s, api, clk := streamer(t, false)
	s.Delta(ctx, "Hello")
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 429, Description: "Too Many Requests", RetryAfter: 30}
		}
		return nil
	})
	clk.Advance(time.Second)
	s.Delta(ctx, " a") // strike 1, interval 1.6 s
	api.setReply(nil)
	clk.Advance(time.Second)
	s.Delta(ctx, " b") // 1 s < 1.6 s → no edit
	if api.edits() != 1 {
		t.Fatal(api.methods())
	}
	clk.Advance(time.Second)
	s.Delta(ctx, " c")
	if api.edits() != 2 || api.lastText() != "Hello a b c ▉" {
		t.Fatal(api.methods(), api.lastText())
	}
}

func TestEditFailureSendsOnlyUnsentTail(t *testing.T) {
	s, api, clk := streamer(t, false)
	s.Delta(ctx, "Hello")
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 400, Description: "Bad Request: message can't be edited"}
		}
		return nil
	})
	clk.Advance(time.Second)
	s.Delta(ctx, " world")
	if err := s.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	sends := api.textsOf("sendMessage")
	if len(sends) != 2 || sends[0] != "Hello ▉" || sends[1] != "world" {
		t.Fatal(sends)
	}
}

func TestSegmentBreakAfterEditFailurePreservesTail(t *testing.T) {
	// Hermes #8124: the tail before a tool boundary must reach the user, and
	// the next segment must not repeat the prefix that the user saw.
	s, api, clk := streamer(t, false)
	s.Delta(ctx, "Hello")
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 400, Description: "Bad Request: message can't be edited"}
		}
		return nil
	})
	clk.Advance(time.Second)
	s.Delta(ctx, " world")
	s.Boundary(ctx)
	s.Delta(ctx, "Next segment")
	if err := s.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	sends := api.textsOf("sendMessage")
	if strings.Join(sends, "|") != "Hello ▉|world|Next segment" {
		t.Fatal(sends)
	}
}

func TestSegmentBreakWithFloodPreservesTail(t *testing.T) {
	s, api, clk := streamer(t, false)
	s.Delta(ctx, "Hello")
	clk.Advance(time.Second)
	s.Delta(ctx, " world")
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 429, Description: "Too Many Requests", RetryAfter: 30}
		}
		return nil
	})
	clk.Advance(time.Second)
	s.Delta(ctx, " more")
	s.Boundary(ctx)
	s.Delta(ctx, "Here is the tool result.")
	_ = s.Finish(ctx, "")
	all := strings.Join(api.textsOf("sendMessage"), " ")
	if !strings.Contains(all, "more") || !strings.Contains(all, "Here is the tool result") {
		t.Fatal(api.textsOf("sendMessage"))
	}
}

func TestFallbackFinalSplitsLongContinuation(t *testing.T) {
	s, api, clk := streamer(t, false)
	s.Delta(ctx, "Hello world")
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 400, Description: "Bad Request: message can't be edited"}
		}
		return nil
	})
	clk.Advance(time.Second)
	tail := strings.Repeat("x", 6000)
	s.Delta(ctx, tail)
	if err := s.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	sends := api.textsOf("sendMessage")
	if len(sends) != 3 || !strings.HasPrefix(sends[0], "Hello world") {
		t.Fatal(len(sends))
	}
	got := 0
	for _, txt := range sends[1:] {
		got += strings.Count(txt, "x")
	}
	if got != len(tail) {
		t.Fatal("tail lost:", got)
	}
}

func TestDraftTransportInDM(t *testing.T) {
	s, api, clk := streamer(t, true)
	s.Delta(ctx, "Hello ")
	clk.Advance(time.Second)
	s.Delta(ctx, "world!")
	if err := s.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	drafts := api.all()
	ids := map[any]bool{}
	for _, c := range drafts {
		if c.Method == "sendMessageDraft" {
			ids[c.Body["draft_id"]] = true
			if c.Body["draft_id"] == float64(0) {
				t.Fatal("draft id 0")
			}
		}
	}
	if api.count("sendMessageDraft") != 2 || len(ids) != 1 || api.edits() != 0 {
		t.Fatal(api.methods())
	}
	if api.textsOf("sendMessageDraft")[1] != "Hello world! ▉" {
		t.Fatal(api.textsOf("sendMessageDraft"))
	}
	if api.count("sendMessage") != 1 || api.lastText() != `Hello world\!` {
		t.Fatal(api.methods(), api.lastText())
	}

	// A group always uses edits.
	g, gapi, gclk := streamer(t, false)
	g.Delta(ctx, "Group message")
	gclk.Advance(time.Second)
	g.Delta(ctx, " more")
	_ = g.Finish(ctx, "")
	if gapi.count("sendMessageDraft") != 0 || gapi.edits() == 0 {
		t.Fatal(gapi.methods())
	}

	// transport "edit" never uses drafts, also in a DM.
	e, eapi, _ := streamer(t, true)
	e.Cfg.Transport = "edit"
	e.Delta(ctx, "Hello there")
	_ = e.Finish(ctx, "")
	if eapi.count("sendMessageDraft") != 0 {
		t.Fatal(eapi.methods())
	}
}

func TestDraftFailureFallsBackToEdits(t *testing.T) {
	s, api, clk := streamer(t, true)
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "sendMessageDraft" {
			return &APIError{Code: 400, Description: "Bad Request: method not available"}
		}
		return nil
	})
	s.Delta(ctx, "Hello ")
	clk.Advance(time.Second)
	s.Delta(ctx, "world!")
	_ = s.Finish(ctx, "")
	if api.count("sendMessageDraft") != 1 || api.count("sendMessage") != 1 || api.edits() != 2 {
		t.Fatal(api.methods())
	}
}

func TestDraftBoundaryBumpsDraftID(t *testing.T) {
	s, api, clk := streamer(t, true)
	s.Delta(ctx, "Pre-tool text")
	s.Boundary(ctx)
	clk.Advance(time.Second)
	s.Delta(ctx, "Post-tool text")
	_ = s.Finish(ctx, "")
	var ids []any
	for _, c := range api.all() {
		if c.Method == "sendMessageDraft" {
			ids = append(ids, c.Body["draft_id"])
		}
	}
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatal(ids)
	}
	// Each segment ends as a real message.
	if strings.Join(api.textsOf("sendMessage"), "|") != `Pre\-tool text|Post\-tool text` {
		t.Fatal(api.textsOf("sendMessage"))
	}

	// Two answers get two draft ids.
	s2, api2, _ := streamer(t, true)
	s2.Delta(ctx, "Second reply")
	_ = s2.Finish(ctx, "")
	if api2.lastCall("sendMessageDraft").Body["draft_id"] == ids[1] {
		t.Fatal("draft id reused")
	}
}

func TestBoundaryStartsNewMessage(t *testing.T) {
	s, api, _ := streamer(t, false)
	n := 0
	s.OnFirstMessage = func() { n++ }
	s.Delta(ctx, "First part")
	s.Boundary(ctx)
	s.Delta(ctx, "Second part")
	_ = s.Finish(ctx, "")
	if api.count("sendMessage") != 2 {
		t.Fatal(api.methods())
	}
	// The boundary closes the first message without the cursor.
	if c := api.lastCall("editMessageText"); c.Body["text"] != "First part" {
		t.Fatal(api.all())
	}
	// One call for each new answer message (Hermes on_new_message).
	if n != 2 {
		t.Fatal("OnFirstMessage calls:", n)
	}
}

func TestBoundaryWithoutTextSendsNothing(t *testing.T) {
	s, api, _ := streamer(t, false)
	s.Boundary(ctx)
	s.Boundary(ctx)
	s.Delta(ctx, "Answer")
	_ = s.Finish(ctx, "")
	if api.count("sendMessage") != 1 || api.edits() != 1 {
		t.Fatal(api.methods())
	}
}

func TestOnFirstMessageNotCalledOnEdit(t *testing.T) {
	s, _, clk := streamer(t, false)
	n := 0
	s.OnFirstMessage = func() { n++ }
	s.Delta(ctx, "Hello")
	clk.Advance(time.Second)
	s.Delta(ctx, " world")
	_ = s.Finish(ctx, "")
	if n != 1 {
		t.Fatal(n)
	}
}

func TestStreamHidesMediaTags(t *testing.T) {
	s, api, clk := streamer(t, false)
	s.Delta(ctx, "Here is the chart\nMEDIA:/tmp/chart.png")
	if strings.Contains(api.lastText(), "MEDIA:") || !strings.HasPrefix(api.lastText(), "Here is the chart") {
		t.Fatal(api.lastText())
	}
	clk.Advance(time.Second)
	s.Delta(ctx, "\n[[audio_as_voice]]")
	_ = s.Finish(ctx, "")
	for _, c := range api.all() {
		if txt, _ := c.Body["text"].(string); strings.Contains(txt, "MEDIA:") || strings.Contains(txt, "[[") {
			t.Fatal(c)
		}
	}

	// A media-only answer sends no text.
	m, mapi, _ := streamer(t, false)
	m.Delta(ctx, "MEDIA:/tmp/image.png")
	_ = m.Finish(ctx, "")
	if len(mapi.all()) != 0 {
		t.Fatal(mapi.methods())
	}
}

func TestShortTextWithCursorWaits(t *testing.T) {
	// Hermes: no new message for less than 4 visible characters with the
	// cursor, so a lone cursor never stays on screen.
	s, api, clk := streamer(t, false)
	s.Delta(ctx, "Hi")
	if len(api.all()) != 0 {
		t.Fatal(api.methods())
	}
	clk.Advance(time.Second)
	s.Delta(ctx, " there")
	if api.count("sendMessage") != 1 || api.lastText() != "Hi there ▉" {
		t.Fatal(api.methods())
	}
	// Without a cursor a short text goes at once.
	n, napi, _ := streamer(t, false)
	n.Cfg.Cursor = ""
	n.Delta(ctx, "Hi")
	if napi.count("sendMessage") != 1 {
		t.Fatal(napi.methods())
	}
}

func TestStreamThreadParam(t *testing.T) {
	s, api, _ := streamer(t, false)
	s.ThreadID = "17585"
	s.Delta(ctx, "Hello")
	_ = s.Finish(ctx, "Hello there")
	if api.lastCall("sendMessage").Body["message_thread_id"] != float64(17585) {
		t.Fatal(api.all())
	}
	// The General topic id is left out.
	g, gapi, _ := streamer(t, false)
	g.ThreadID = "1"
	_ = g.Finish(ctx, "Hello")
	if _, ok := gapi.lastCall("sendMessage").Body["message_thread_id"]; ok {
		t.Fatal(gapi.all())
	}
}

func TestCleanForDisplay(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Here is your analysis of the image.", "Here is your analysis of the image."},
		{"Here is the image\nMEDIA:/tmp/hermes/image.png", "Here is the image"},
		{"Audio generated\nMEDIA: /home/user/voice.mp3", "Audio generated"},
		{"Result: `MEDIA:/path/file.png`", "Result:"},
		{`Result: "MEDIA:/path/file.png"`, "Result:"},
		{"Result: 'MEDIA:/path/file.png'", "Result:"},
		{"[[audio_as_voice]]\nMEDIA:/tmp/voice.ogg", ""},
		{"Two files:\nMEDIA:/tmp/a.png\nMEDIA:/tmp/b.jpg", "Two files:"},
		{"I made this image MEDIA:/tmp/art.png for you.", "I made this image  for you."},
		{"The media: files are in /tmp. Use social MEDIA carefully.", "The media: files are in /tmp. Use social MEDIA carefully."},
		{"Unknown MEDIA:/tmp/x.unknownext stays", "Unknown MEDIA:/tmp/x.unknownext stays"},
	}
	for _, c := range cases {
		if got := cleanForDisplay(c.in); got != c.want {
			t.Errorf("cleanForDisplay(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := cleanForDisplay("Before\n\n\nMEDIA:/tmp/file.png\n\n\nAfter"); strings.Contains(got, "\n\n\n") {
		t.Fatal(got)
	}
}

func TestFinalPartsKeepEntities(t *testing.T) {
	check := func(t *testing.T, parts []string) {
		t.Helper()
		for i, p := range parts {
			if n := UTF16Len(p); n > maxMessageLen {
				t.Errorf("part %d has %d units", i, n)
			}
			if err := checkMDV2(p); err != nil {
				t.Errorf("part %d: %v", i, err)
			}
		}
	}
	t.Run("link", func(t *testing.T) {
		parts := finalParts(strings.Repeat("a", 4070) + " see [LinkSwift](https://github.com/x/y) end")
		check(t, parts)
		if len(parts) != 2 || !strings.HasPrefix(parts[1], "[LinkSwift](https://github.com/x/y)") {
			t.Fatalf("got %d parts, part 2 starts with %q", len(parts), head(parts[len(parts)-1], 40))
		}
	})
	t.Run("code block", func(t *testing.T) {
		parts := finalParts(strings.Repeat("line\n", 700) + "```go\n" + strings.Repeat("x := f(1)\n", 300) + "```\n")
		check(t, parts)
		if len(parts) != 2 {
			t.Fatalf("got %d parts", len(parts))
		}
		if !strings.HasSuffix(parts[0], "```"+` \(1/2\)`) || !strings.HasPrefix(parts[1], "```go\n") {
			t.Fatalf("part 1 ends with %q, part 2 starts with %q", tail(parts[0], 20), head(parts[1], 20))
		}
	})
}

// flood429 makes every editMessageText call get a 429 with the given wait.
func flood429(api *fakeBot, retryAfter int) {
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 429, Description: "Too Many Requests: retry after N", RetryAfter: retryAfter}
		}
		return nil
	})
}

// threeStrikes streams "Hello" and three more deltas that each get a flood
// error. The interval doubles after each strike: 0.8 s, 1.6 s, 3.2 s.
func threeStrikes(s *Streamer, api *fakeBot, clk *fakeClock, retryAfter int) {
	s.Delta(ctx, "Hello")
	flood429(api, retryAfter)
	for _, wait := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		clk.Advance(wait)
		s.Delta(ctx, " more")
	}
}

func TestNoteFloodSetsFloodUntil(t *testing.T) {
	s, api, clk := streamer(t, false)
	s.Delta(ctx, "Hello")
	flood429(api, 30)
	clk.Advance(time.Second)
	s.Delta(ctx, " more") // strike 1
	s.mu.Lock()
	got := s.floodUntil
	s.mu.Unlock()
	if want := clk.Now().Add(30 * time.Second); !got.Equal(want) {
		t.Fatal(got, want)
	}
}

func TestThirdStrikeMakesNoStripUnderFlood(t *testing.T) {
	s, api, clk := streamer(t, false)
	threeStrikes(s, api, clk, 30)
	if api.edits() != 3 { // the three strikes, and no strip edit
		t.Fatal(api.methods())
	}
}

func TestEditErrorStripsWhenNoFlood(t *testing.T) {
	s, api, clk := streamer(t, false)
	s.Delta(ctx, "Hello")
	failed := false
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" && !failed {
			failed = true
			return &APIError{Code: 400, Description: "Bad Request: message can't be edited"}
		}
		return nil
	})
	clk.Advance(time.Second)
	s.Delta(ctx, " world")
	if api.edits() != 2 || api.lastText() != "Hello" { // the strip of "Hello ▉"
		t.Fatal(api.methods(), api.lastText())
	}
}

func TestStripTryPlainOnly(t *testing.T) {
	api := newFakeBot(t)
	err := stripTry(ctx, api.client(), 42, previewClean{msgID: 101, plain: "Hello"}, nil)
	if err != nil || api.edits() != 1 {
		t.Fatal(err, api.methods())
	}
	c := api.lastCall("editMessageText")
	if c.Body["message_id"] != float64(101) || c.Body["text"] != "Hello" || c.Body["parse_mode"] != nil {
		t.Fatal(c.Body)
	}
}

// mdv2Preview is a preview to clean with a MarkdownV2 text.
func mdv2Preview() previewClean {
	return previewClean{msgID: 101, plain: "Run **make test**", mdv2: ToMarkdownV2("Run **make test**")}
}

func TestStripTrySendsMarkdownV2First(t *testing.T) {
	api := newFakeBot(t)
	pc := mdv2Preview()
	err := stripTry(ctx, api.client(), 42, pc, nil)
	if err != nil || api.edits() != 1 {
		t.Fatal(err, api.methods())
	}
	c := api.lastCall("editMessageText")
	if c.Body["parse_mode"] != "MarkdownV2" || c.Body["text"] != pc.mdv2 {
		t.Fatal(c.Body)
	}
}

func TestStripTryFallsBackToPlainOn400(t *testing.T) {
	api := newFakeBot(t)
	log, buf := captureLog(t)
	api.setReply(func(method string, body map[string]any) *APIError {
		if method == "editMessageText" && body["parse_mode"] != nil {
			return &APIError{Code: 400, Description: "Bad Request: can't parse entities"}
		}
		return nil
	})
	err := stripTry(ctx, api.client(), 42, mdv2Preview(), log)
	if err != nil || api.edits() != 2 {
		t.Fatal(err, api.methods())
	}
	c := api.lastCall("editMessageText")
	if c.Body["text"] != "Run **make test**" || c.Body["parse_mode"] != nil {
		t.Fatal(c.Body)
	}
	if !strings.Contains(buf.String(), fallbackMsg) || !strings.Contains(buf.String(), "op=strip_cursor") {
		t.Fatal(buf.String())
	}
}

func TestStripTryNotModifiedIsSuccess(t *testing.T) {
	api := newFakeBot(t)
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 400, Description: "Bad Request: message is not modified"}
		}
		return nil
	})
	err := stripTry(ctx, api.client(), 42, mdv2Preview(), nil)
	if err != nil || api.edits() != 1 { // no plain retry
		t.Fatal(err, api.methods())
	}
}

func TestStripTryReturnsFloodError(t *testing.T) {
	api := newFakeBot(t)
	flood429(api, 30)
	err := stripTry(ctx, api.client(), 42, previewClean{msgID: 101, plain: "Hello"}, nil)
	if d, ok := floodWait(err); !ok || d != 30*time.Second {
		t.Fatal(err)
	}
}

// fakeTimers holds AfterFunc calls. run moves the fake clock by each wait
// and then calls the function, also for the timers that a function adds.
type fakeTimers struct {
	mu      sync.Mutex
	clk     *fakeClock
	pending []fakeTimer
	waits   []time.Duration
}

type fakeTimer struct {
	d time.Duration
	f func()
}

func (ft *fakeTimers) AfterFunc(d time.Duration, f func()) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.pending = append(ft.pending, fakeTimer{d: d, f: f})
	ft.waits = append(ft.waits, d)
}

// run calls the pending timers in order until none is left.
func (ft *fakeTimers) run() {
	for {
		ft.mu.Lock()
		if len(ft.pending) == 0 {
			ft.mu.Unlock()
			return
		}
		next := ft.pending[0]
		ft.pending = ft.pending[1:]
		ft.mu.Unlock()
		ft.clk.Advance(next.d)
		next.f()
	}
}

// waitList returns a copy of the waits of all AfterFunc calls.
func (ft *fakeTimers) waitList() []time.Duration {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return append([]time.Duration(nil), ft.waits...)
}

// pendingCount returns the number of timers that did not run yet.
func (ft *fakeTimers) pendingCount() int {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return len(ft.pending)
}

// retryStreamer returns a streamer with fake timers and a captured log.
func retryStreamer(t *testing.T) (*Streamer, *fakeBot, *fakeTimers, *bytes.Buffer) {
	t.Helper()
	s, api, clk := streamer(t, false)
	log, buf := captureLog(t)
	ft := &fakeTimers{clk: clk}
	s.Log = log
	s.AfterFunc = ft.AfterFunc
	s.init(ctx)
	return s, api, ft, buf
}

// waitsEqual reports whether the waits are the same as want.
func waitsEqual(got []time.Duration, want ...time.Duration) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

const (
	stripStopMsg  = `msg="telegram cursor strip stopped"`
	stripRetryMsg = `msg="telegram cursor strip failed, retry later"`
)

// linesWith returns the log lines in buf that contain msg.
func linesWith(buf *bytes.Buffer, msg string) []string {
	var out []string
	for _, l := range logLines(buf) {
		if strings.Contains(l, msg) {
			out = append(out, l)
		}
	}
	return out
}

func TestStripRetrySucceeds(t *testing.T) {
	s, api, ft, buf := retryStreamer(t)
	s.startStripRetry(previewClean{msgID: 101, plain: "Hello"}, 31*time.Second)
	if api.edits() != 0 {
		t.Fatal(api.methods())
	}
	ft.run()
	if api.edits() != 1 || api.lastText() != "Hello" || !waitsEqual(ft.waitList(), 31*time.Second) {
		t.Fatal(api.methods(), api.lastText(), ft.waitList())
	}
	if len(linesWith(buf, stripStopMsg)) != 0 {
		t.Fatal(buf.String())
	}
}

func TestStripRetryLogsRetryLater(t *testing.T) {
	s, api, ft, buf := retryStreamer(t)
	failed := false
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" && !failed {
			failed = true
			return &APIError{Code: 429, Description: "Too Many Requests: retry after 10", RetryAfter: 10}
		}
		return nil
	})
	s.startStripRetry(previewClean{msgID: 101, plain: "Hello"}, 31*time.Second)
	ft.run()
	if api.edits() != 2 || !waitsEqual(ft.waitList(), 31*time.Second, 11*time.Second) {
		t.Fatal(api.methods(), ft.waitList())
	}
	found := false
	for _, l := range linesWith(buf, stripRetryMsg) {
		if strings.Contains(l, "retry_after_s=11") && strings.Contains(l, "try=2") {
			found = true
		}
	}
	if !found {
		t.Fatal(buf.String())
	}
}

func TestStripRetryStopsAfterThreeTries(t *testing.T) {
	s, api, ft, buf := retryStreamer(t)
	flood429(api, 10)
	s.startStripRetry(previewClean{msgID: 101, plain: "Hello"}, 11*time.Second)
	ft.run()
	if api.edits() != 3 || !waitsEqual(ft.waitList(), 11*time.Second, 11*time.Second, 11*time.Second) {
		t.Fatal(api.methods(), ft.waitList())
	}
	stops := linesWith(buf, stripStopMsg)
	if len(stops) != 1 || !strings.Contains(stops[0], `reason="max tries"`) {
		t.Fatal(buf.String())
	}
}

func TestStripRetryStopsAtMaxWait(t *testing.T) {
	s, api, ft, buf := retryStreamer(t)
	flood429(api, 90)
	s.startStripRetry(previewClean{msgID: 101, plain: "Hello"}, 91*time.Second)
	ft.run()
	if api.edits() != 1 { // the next try at 182 s is after 2 minutes
		t.Fatal(api.methods())
	}
	stops := linesWith(buf, stripStopMsg)
	if len(stops) != 1 || !strings.Contains(stops[0], `reason="max wait"`) {
		t.Fatal(buf.String())
	}
}

func TestStripRetryFirstWaitOverLimit(t *testing.T) {
	s, api, ft, buf := retryStreamer(t)
	s.startStripRetry(previewClean{msgID: 101, plain: "Hello"}, 121*time.Second)
	ft.run()
	if api.edits() != 0 || len(ft.waitList()) != 0 {
		t.Fatal(api.methods(), ft.waitList())
	}
	stops := linesWith(buf, stripStopMsg)
	if len(stops) != 1 || !strings.Contains(stops[0], `reason="max wait"`) {
		t.Fatal(buf.String())
	}
}

func TestStripRetryStopsOnError(t *testing.T) {
	s, api, ft, buf := retryStreamer(t)
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 400, Description: "Bad Request: message to edit not found"}
		}
		return nil
	})
	s.startStripRetry(previewClean{msgID: 101, plain: "Hello"}, 11*time.Second)
	ft.run()
	if api.edits() != 1 {
		t.Fatal(api.methods())
	}
	stops := linesWith(buf, stripStopMsg)
	if len(stops) != 1 || !strings.Contains(stops[0], "reason=error") || !strings.Contains(stops[0], "message to edit not found") {
		t.Fatal(buf.String())
	}
}

// editsAfter returns the editMessageText calls from call n on.
func editsAfter(api *fakeBot, n int) []apiCall {
	var out []apiCall
	for _, c := range api.all()[n:] {
		if c.Method == "editMessageText" {
			out = append(out, c)
		}
	}
	return out
}

func TestCursorGoesAwayAfterFloodWait(t *testing.T) {
	s, api, ft, _ := retryStreamer(t)
	threeStrikes(s, api, ft.clk, 30)
	s.Delta(ctx, " text")
	if err := s.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if api.edits() != 3 { // no edit at finish under flood
		t.Fatal(api.methods())
	}
	api.setReply(nil)
	ft.run()
	c := api.lastCall("editMessageText")
	if c.Body["message_id"] != float64(101) || c.Body["text"] != "Hello" {
		t.Fatal(c.Body)
	}
	if w := ft.waitList(); len(w) == 0 || w[0] != 31*time.Second {
		t.Fatal(w)
	}
}

func TestStripEditsPreviewNotTail(t *testing.T) {
	s, api, ft, _ := retryStreamer(t)
	threeStrikes(s, api, ft.clk, 30)
	s.Delta(ctx, " text")
	if err := s.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	// The tail is the second sendMessage, so it got message id 102.
	if api.count("sendMessage") != 2 {
		t.Fatal(api.methods())
	}
	n := len(api.all())
	api.setReply(nil)
	ft.run()
	after := editsAfter(api, n)
	if len(after) == 0 {
		t.Fatal(api.methods())
	}
	for _, c := range after {
		if c.Body["message_id"] != float64(101) {
			t.Fatal(c.Body)
		}
	}
}

func TestStripAtFinishWhenFloodOver(t *testing.T) {
	s, api, ft, _ := retryStreamer(t)
	threeStrikes(s, api, ft.clk, 30)
	api.setReply(nil)
	ft.clk.Advance(31 * time.Second)
	if err := s.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if api.edits() != 4 || len(ft.waitList()) != 0 { // the strip at finish
		t.Fatal(api.methods(), ft.waitList())
	}
	c := api.lastCall("editMessageText")
	if c.Body["message_id"] != float64(101) || c.Body["text"] != "Hello" {
		t.Fatal(c.Body)
	}
}

func TestFinishDoesNotWait(t *testing.T) {
	s, api, ft, _ := retryStreamer(t)
	threeStrikes(s, api, ft.clk, 30)
	if err := s.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if api.edits() != 3 || ft.pendingCount() != 1 { // the retry has not run
		t.Fatal(api.methods(), ft.pendingCount())
	}
}

func TestNoCursorTailOnlyMakesNoStrip(t *testing.T) {
	s, api, ft, _ := retryStreamer(t)
	s.Cfg.Cursor = ""
	threeStrikes(s, api, ft.clk, 30)
	s.Delta(ctx, " text")
	if err := s.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	ft.run()
	if api.edits() != 3 || len(ft.waitList()) != 0 {
		t.Fatal(api.methods(), ft.waitList())
	}
}

func TestDraftFinishStartsNoStripRetry(t *testing.T) {
	s, api, clk := streamer(t, true)
	ft := &fakeTimers{clk: clk}
	s.AfterFunc = ft.AfterFunc
	s.Delta(ctx, "Hello there")
	if err := s.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	ft.run()
	if api.edits() != 0 || len(ft.waitList()) != 0 {
		t.Fatal(api.methods(), ft.waitList())
	}
}

func TestStripRetryDoesNotRaceWithStreamer(t *testing.T) {
	s, api, clk := streamer(t, false)
	var wg sync.WaitGroup
	s.AfterFunc = func(_ time.Duration, f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f()
		}()
	}
	threeStrikes(s, api, clk, 30)
	s.Delta(ctx, " text")
	api.setReply(nil)
	s.Boundary(ctx)
	// The retry goroutine runs while the next segment streams.
	s.Delta(ctx, "Next part")
	if err := s.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	found := false
	for _, c := range api.all() {
		if c.Method == "editMessageText" && c.Body["message_id"] == float64(101) && c.Body["text"] == "Hello" {
			found = true
		}
	}
	if !found {
		t.Fatal(api.all())
	}
}

// fullResend streams "Hello there", makes three flood strikes, and finishes
// with a final text that does not start with the preview text. Each edit gets
// a 429 with a wait of 30 s. extra, when it is not nil, gives the reply to
// the other calls.
func fullResend(s *Streamer, api *fakeBot, ft *fakeTimers, extra func(method string) *APIError) error {
	s.Delta(ctx, "Hello there")
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 429, Description: "Too Many Requests: retry after 30", RetryAfter: 30}
		}
		if extra != nil {
			return extra(method)
		}
		return nil
	})
	for _, wait := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		ft.clk.Advance(wait)
		s.Delta(ctx, " more")
	}
	return s.Finish(ctx, "Other text")
}

func TestFullResendDeletesPreview(t *testing.T) {
	s, api, ft, _ := retryStreamer(t)
	if err := fullResend(s, api, ft, nil); err != nil {
		t.Fatal(err)
	}
	if got := api.lastCall("sendMessage").Body["text"]; got != "Other text" {
		t.Fatal(got)
	}
	if api.count("deleteMessage") != 1 || api.lastCall("deleteMessage").Body["message_id"] != float64(101) {
		t.Fatal(api.methods())
	}
	ft.run()
	if api.edits() != 3 { // no strip after the delete
		t.Fatal(api.methods())
	}
}

func TestTailOnlyKeepsPreview(t *testing.T) {
	s, api, ft, _ := retryStreamer(t)
	threeStrikes(s, api, ft.clk, 30)
	s.Delta(ctx, " text")
	if err := s.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	api.setReply(nil)
	ft.run()
	if api.count("deleteMessage") != 0 {
		t.Fatal(api.methods())
	}
	c := api.lastCall("editMessageText")
	if c.Body["message_id"] != float64(101) || c.Body["text"] != "Hello" {
		t.Fatal(c.Body)
	}
}

func TestFailedDeleteFallsBackToStrip(t *testing.T) {
	s, api, ft, buf := retryStreamer(t)
	err := fullResend(s, api, ft, func(method string) *APIError {
		if method == "deleteMessage" {
			return &APIError{Code: 429, Description: "Too Many Requests: retry after 30", RetryAfter: 30}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `msg="telegram preview delete failed"`) {
		t.Fatal(buf.String())
	}
	api.setReply(nil)
	ft.run()
	c := api.lastCall("editMessageText")
	if c.Body["message_id"] != float64(101) || c.Body["text"] != "Hello there" {
		t.Fatal(c.Body)
	}
}

func TestFailedFullResendKeepsPreview(t *testing.T) {
	s, api, ft, _ := retryStreamer(t)
	err := fullResend(s, api, ft, func(method string) *APIError {
		if method == "sendMessage" {
			return &APIError{Code: 403, Description: "Forbidden: bot was blocked by the user"}
		}
		return nil
	})
	if err == nil {
		t.Fatal("want the send error")
	}
	if api.count("deleteMessage") != 0 {
		t.Fatal(api.methods())
	}
	api.setReply(nil)
	ft.run()
	c := api.lastCall("editMessageText")
	if c.Body["message_id"] != float64(101) || c.Body["text"] != "Hello there" {
		t.Fatal(c.Body)
	}
}

// failFinalEdit streams text into one preview (message 101) and finishes
// while every editMessageText gets 429 RetryAfter 30.
func failFinalEdit(s *Streamer, api *fakeBot, text string) error {
	s.Delta(ctx, text)
	flood429(api, 30)
	return s.Finish(ctx, "")
}

func TestFailedFinalEditNothingUnseenSendsNothing(t *testing.T) {
	s, api, _, _ := retryStreamer(t)
	if err := failFinalEdit(s, api, "Run **make test**"); err != nil {
		t.Fatal(err)
	}
	if api.count("sendMessage") != 1 { // the preview only
		t.Fatal(api.methods())
	}
}

func TestFinalEditFloodSkipsStripAtFinish(t *testing.T) {
	s, api, ft, _ := retryStreamer(t)
	if err := failFinalEdit(s, api, "Run **make test**"); err != nil {
		t.Fatal(err)
	}
	if api.edits() != 1 || ft.pendingCount() != 1 { // the failed final edit
		t.Fatal(api.methods(), ft.pendingCount())
	}
	if w := ft.waitList(); w[0] != 31*time.Second {
		t.Fatal(w)
	}
}

func TestStripSendsMarkdownV2WhenNothingUnseen(t *testing.T) {
	s, api, ft, _ := retryStreamer(t)
	if err := failFinalEdit(s, api, "Run **make test**"); err != nil {
		t.Fatal(err)
	}
	api.setReply(nil)
	ft.run()
	c := api.lastCall("editMessageText")
	if c.Body["message_id"] != float64(101) || c.Body["parse_mode"] != "MarkdownV2" || c.Body["text"] != ToMarkdownV2("Run **make test**") {
		t.Fatal(c.Body)
	}
}

func TestMarkdownV2StripFallsBackToPlain(t *testing.T) {
	s, api, ft, _ := retryStreamer(t)
	if err := failFinalEdit(s, api, "Run **make test**"); err != nil {
		t.Fatal(err)
	}
	api.setReply(func(method string, body map[string]any) *APIError {
		if method == "editMessageText" && body["parse_mode"] != nil {
			return &APIError{Code: 400, Description: "Bad Request: can't parse entities"}
		}
		return nil
	})
	n := len(api.all())
	ft.run()
	// The retry makes two edits in the same try: MarkdownV2, then plain.
	after := editsAfter(api, n)
	if len(after) != 2 || after[0].Body["parse_mode"] != "MarkdownV2" {
		t.Fatal(api.methods())
	}
	if c := after[1]; c.Body["text"] != "Run **make test**" || c.Body["parse_mode"] != nil {
		t.Fatal(c.Body)
	}
}

func TestNoCursorNothingUnseenGetsMarkdownV2(t *testing.T) {
	s, api, ft, _ := retryStreamer(t)
	s.Cfg.Cursor = ""
	if err := failFinalEdit(s, api, "Run **make test**"); err != nil {
		t.Fatal(err)
	}
	api.setReply(nil)
	n := len(api.all())
	ft.run()
	after := editsAfter(api, n)
	if len(after) != 1 || after[0].Body["parse_mode"] != "MarkdownV2" || after[0].Body["text"] != ToMarkdownV2("Run **make test**") {
		t.Fatal(api.all())
	}
}

func TestLongFinalNothingUnseenStripsPlain(t *testing.T) {
	s, api, ft, _ := retryStreamer(t)
	text := strings.Repeat("a.", 1950) // 3,900 chars; the escapes make it longer than 4,096
	if err := failFinalEdit(s, api, text); err != nil {
		t.Fatal(err)
	}
	api.setReply(nil)
	ft.run()
	c := api.lastCall("editMessageText")
	if c.Body["text"] != text || c.Body["parse_mode"] != nil || api.count("sendMessage") != 1 {
		t.Fatal(api.methods(), c.Body["parse_mode"])
	}
}

func TestBoundaryLeakStripsEachPreview(t *testing.T) {
	s, api, ft, _ := retryStreamer(t)
	s.Delta(ctx, "First part")
	flood429(api, 30)
	s.Boundary(ctx)             // preview 101, the final edit fails
	ft.clk.Advance(time.Second) // past the edit interval, so the next delta sends at once
	s.Delta(ctx, "Second part")
	if err := s.Finish(ctx, ""); err != nil { // preview 102, the final edit fails
		t.Fatal(err)
	}
	api.setReply(nil)
	n := len(api.all())
	ft.run()
	// Count the retry edits only. The failed final edits have the same text.
	want := map[float64]string{101: ToMarkdownV2("First part"), 102: ToMarkdownV2("Second part")}
	got := map[float64]int{}
	for _, c := range editsAfter(api, n) {
		id, _ := c.Body["message_id"].(float64)
		if c.Body["parse_mode"] == "MarkdownV2" && want[id] != "" && c.Body["text"] == want[id] {
			got[id]++
		}
	}
	if got[101] != 1 || got[102] != 1 {
		t.Fatal(got, api.all())
	}
}

// freshFinalPartFails streams "Hello there" into preview 101, waits 61 s,
// and finishes with a final of two MarkdownV2 parts that starts with "Hello
// there". Part 1 goes out as message 102. Part 2 gets 403 "Forbidden: bot was
// blocked by the user". The final edit of message 101 gets 429 RetryAfter 30.
// failLater says whether each sendMessage after part 2 also gets 403.
func freshFinalPartFails(s *Streamer, api *fakeBot, ft *fakeTimers, failLater bool) error {
	s.Delta(ctx, "Hello there")
	ft.clk.Advance(61 * time.Second)
	sends := 1 // the preview
	api.setReply(func(method string, _ map[string]any) *APIError {
		switch method {
		case "editMessageText":
			return &APIError{Code: 429, Description: "Too Many Requests: retry after 30", RetryAfter: 30}
		case "sendMessage":
			sends++
			if sends == 3 || (sends > 3 && failLater) {
				return &APIError{Code: 403, Description: "Forbidden: bot was blocked by the user"}
			}
		}
		return nil
	})
	return s.Finish(ctx, "Hello there "+strings.Repeat("word. ", 800))
}

func TestFreshFinalFallbackSendsNoDuplicate(t *testing.T) {
	s, api, ft, _ := retryStreamer(t)
	_ = freshFinalPartFails(s, api, ft, false)
	if api.count("deleteMessage") != 0 {
		t.Fatal(api.methods())
	}
	// Only the tail went out after the failed part 2.
	sends := api.textsOf("sendMessage")
	if len(sends) < 4 || strings.HasPrefix(sends[3], "Hello there") {
		t.Fatal(len(sends), api.methods())
	}
	api.setReply(nil)
	ft.run()
	c := api.lastCall("editMessageText")
	if c.Body["message_id"] != float64(101) || c.Body["text"] != "Hello there" {
		t.Fatal(c.Body)
	}
}

func TestFreshFinalFallbackKeepsPreviewOnFailure(t *testing.T) {
	s, api, ft, _ := retryStreamer(t)
	if err := freshFinalPartFails(s, api, ft, true); err == nil {
		t.Fatal("want the send error")
	}
	if api.count("deleteMessage") != 0 {
		t.Fatal(api.methods())
	}
	api.setReply(nil)
	ft.run()
	c := api.lastCall("editMessageText")
	if c.Body["message_id"] != float64(101) || c.Body["text"] != "Hello there" {
		t.Fatal(c.Body)
	}
}
