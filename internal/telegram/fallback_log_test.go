package telegram

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/gateway"
)

const fallbackMsg = `msg="telegram: Telegram rejected the MarkdownV2 text, sent it as plain text"`

// captureLog returns a logger that writes text lines at the Debug level and above to the
// returned buffer. The lines have no time, so a check of the text does not depend on the clock.
func captureLog(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	opts := &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}
	return slog.New(slog.NewTextHandler(buf, opts)), buf
}

// logLines returns the lines in buf that are not empty.
func logLines(buf *bytes.Buffer) []string {
	var out []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// warnLines returns the fallback Warn lines in buf.
func warnLines(buf *bytes.Buffer) []string {
	var out []string
	for _, l := range logLines(buf) {
		if strings.Contains(l, "level=WARN") && strings.Contains(l, fallbackMsg) {
			out = append(out, l)
		}
	}
	return out
}

// rejectMarkdown makes the fake answer HTTP 400 to each request with MarkdownV2.
func rejectMarkdown(method string, body map[string]any) *APIError {
	if body["parse_mode"] == "MarkdownV2" {
		return &APIError{Code: 400, Description: "Bad Request: can't parse entities: unclosed"}
	}
	return nil
}

// mustContain fails the test when line does not contain each of want.
func mustContain(t *testing.T, line string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(line, w) {
			t.Fatalf("log line %q does not contain %q", line, w)
		}
	}
}

func TestSendTextFallbackLogsWarn(t *testing.T) {
	h := harness(t, &tgProv{}, nil)
	log, buf := captureLog(t)
	h.ch.Log = log
	h.api.setReply(rejectMarkdown)
	if _, err := h.ch.sendText(ctx, -100, "140", "**Done** 1.5", sendOpts{}); err != nil {
		t.Fatal(err)
	}
	lines := logLines(buf)
	if len(lines) != 1 {
		t.Fatalf("want one log line, got %q", lines)
	}
	mustContain(t, lines[0], "level=WARN", fallbackMsg, "op=send", "chat=-100", "thread=140", "can't parse entities")
	if strings.Contains(buf.String(), "part=") || strings.Contains(buf.String(), "Done") {
		t.Fatalf("log must not have a part or the text: %q", buf.String())
	}
}

func TestSendTextFallbackLogsOnlyRejectedPart(t *testing.T) {
	h := harness(t, &tgProv{}, nil)
	log, buf := captureLog(t)
	h.ch.Log = log
	n := 0
	h.api.setReply(func(method string, body map[string]any) *APIError {
		if method == "sendMessage" && body["parse_mode"] == "MarkdownV2" {
			n++
			if n == 2 {
				return &APIError{Code: 400, Description: "Bad Request: can't parse entities: unclosed"}
			}
		}
		return nil
	})
	if _, err := h.ch.sendText(ctx, -100, "", strings.Repeat("a", 5000), sendOpts{}); err != nil {
		t.Fatal(err)
	}
	if got := h.api.count("sendMessage"); got != 3 {
		t.Fatal("want two parts and one retry:", h.api.methods())
	}
	lines := warnLines(buf)
	if len(lines) != 1 {
		t.Fatalf("want one Warn line, got %q", logLines(buf))
	}
	mustContain(t, lines[0], "op=send", "part=2/2")
	if strings.Contains(lines[0], "thread=") {
		t.Fatalf("an empty thread must not be logged: %q", lines[0])
	}
}

func TestSendTextNoWarnWithoutFallback(t *testing.T) {
	h := harness(t, &tgProv{}, nil)
	log, buf := captureLog(t)
	h.ch.Log = log
	if _, err := h.ch.sendText(ctx, -100, "140", "**Done** 1.5", sendOpts{}); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("want an empty log, got %q", buf.String())
	}
}

func TestEditReplyFallbackLogsWarn(t *testing.T) {
	h := harness(t, &tgProv{}, nil)
	log, buf := captureLog(t)
	h.ch.Log = log
	h.api.setReply(func(method string, body map[string]any) *APIError {
		if method == "editMessageText" {
			return rejectMarkdown(method, body)
		}
		return nil
	})
	r := replyRenderer(h)
	r.editMsgID = 5
	r.AckReply(gateway.Reply{Text: "**x**", Edit: true})
	lines := warnLines(buf)
	if len(lines) != 1 {
		t.Fatalf("want one Warn line, got %q", logLines(buf))
	}
	mustContain(t, lines[0], "op=edit_reply", "chat=7")
	if strings.Contains(lines[0], "thread=") || strings.Contains(lines[0], "part=") {
		t.Fatalf("want no thread and no part: %q", lines[0])
	}
}

func TestEditReplyNotModifiedNoWarn(t *testing.T) {
	h := harness(t, &tgProv{}, nil)
	log, buf := captureLog(t)
	h.ch.Log = log
	h.api.setReply(func(method string, body map[string]any) *APIError {
		if method == "editMessageText" {
			return &APIError{Code: 400, Description: "Bad Request: message is not modified"}
		}
		return nil
	})
	r := replyRenderer(h)
	r.editMsgID = 5
	r.AckReply(gateway.Reply{Text: "**x**", Edit: true})
	if strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("want no Warn line, got %q", buf.String())
	}
}

func TestStreamFallbackLogsWarn(t *testing.T) {
	s, api, _ := streamer(t, false)
	log, buf := captureLog(t)
	s.Log = log
	api.setReply(rejectMarkdown)
	s.Delta(ctx, "Working on it")
	if err := s.Finish(ctx, "**Done**"); err != nil {
		t.Fatal(err)
	}
	lines := warnLines(buf)
	if len(lines) != 1 {
		t.Fatalf("want one Warn line, got %q", logLines(buf))
	}
	mustContain(t, lines[0], "op=edit_final", "chat=42")
	if strings.Contains(buf.String(), "Done") {
		t.Fatalf("log must not have the text: %q", buf.String())
	}

	s2, api2, _ := streamer(t, false)
	log2, buf2 := captureLog(t)
	s2.Log = log2
	api2.setReply(rejectMarkdown)
	if err := s2.Finish(ctx, "**Done** 1.5"); err != nil {
		t.Fatal(err)
	}
	lines = warnLines(buf2)
	if len(lines) != 1 {
		t.Fatalf("want one Warn line, got %q", logLines(buf2))
	}
	mustContain(t, lines[0], "op=send_part", "chat=42")
	if strings.Contains(lines[0], "part=") || strings.Contains(buf2.String(), "Done") {
		t.Fatalf("want no part field and no text: %q", buf2.String())
	}
}

func TestPromptFallbackLogsWarn(t *testing.T) {
	it, api := interactions(t)
	log, buf := captureLog(t)
	it.Log = log
	api.setReply(rejectMarkdown)
	p := it.add(approvalPrefix, 42, "")
	if err := it.send(ctx, p, "**x** 1.5", nil); err != nil {
		t.Fatal(err)
	}
	lines := warnLines(buf)
	if len(lines) != 1 {
		t.Fatalf("want one Warn line, got %q", logLines(buf))
	}
	mustContain(t, lines[0], "op=prompt_send", "chat=42")
	it.editMarkdown(ctx, p, "**y**")
	lines = warnLines(buf)
	if len(lines) != 2 {
		t.Fatalf("want two Warn lines, got %q", logLines(buf))
	}
	mustContain(t, lines[1], "op=prompt_edit", "chat=42")
	if strings.Contains(buf.String(), "1.5") {
		t.Fatalf("log must not have the text: %q", buf.String())
	}
}
