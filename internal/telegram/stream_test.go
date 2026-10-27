package telegram

import (
	"strings"
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
	var joined strings.Builder
	for _, txt := range sends {
		joined.WriteString(txt[:strings.LastIndex(txt, ` \(`)])
	}
	if StripMarkdownV2(joined.String()) != final {
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
	if strikes != 4 { // 3 strikes and one try to remove the cursor
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
