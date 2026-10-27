package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// ToolEmoji maps a tool name to the emoji of its status line. The values come
// from the Hermes tool registry (agent/display.py get_tool_emoji). Where
// Tether Agent has a different name for the same tool, the Hermes tool is in
// the comment. Other tools use defaultToolEmoji.
var ToolEmoji = map[string]string{
	"run_command":        "💻", // terminal
	"read_file":          "📖",
	"write_file":         "✍️",
	"patch":              "🔧",
	"web_search":         "🔍",
	"web_fetch":          "📄", // web_extract
	"ask_user":           "❓", // clarify
	"memory":             "🧠",
	"session_search":     "🔍",
	"skills_list":        "📚",
	"skill_view":         "📚",
	"skill_manage":       "📝",
	"delegate_task":      "🔀",
	"cronjob":            "⏰",
	"run_script":         "🐍",  // execute_code
	"view_image":         "👁️", // vision_analyze
	"text_to_speech":     "🔊",
	"image_generate":     "🎨",
	"browser_navigate":   "🌐",
	"browser_click":      "👆",
	"browser_fill":       "⌨️", // browser_type
	"browser_read":       "📸",  // browser_snapshot
	"browser_screenshot": "👁️", // browser_vision
	"browser_run_js":     "🖥️", // browser_console
}

const (
	// defaultToolEmoji is the emoji of a tool that is not in ToolEmoji.
	defaultToolEmoji = "🔧"
	// childPrefix starts the tool name of a delegate_task child line.
	childPrefix = "🔀 "
	// previewMax is the longest preview in "all" and "new" mode (Hermes).
	previewMax = 40
	// statusTextLimit is the longest status message: 4,096 less 64 (Hermes).
	statusTextLimit = 4032
	// defaultStatusInterval is the shortest time between two status edits.
	defaultStatusInterval = 1500 * time.Millisecond
	// maxInlineFloodWait is the longest rate-limit wait that a status or
	// stream call does inline. A longer wait fails the call (Hermes).
	maxInlineFloodWait = 5 * time.Second
	// flushTimeout limits a flush that a timer starts.
	flushTimeout = 30 * time.Second
)

// ToolLine returns the status line for one tool call.
//
//   - "all" and "new" give `{emoji} {tool}: "{preview}"`, with the preview
//     cut to 40 characters, or `{emoji} {tool}...` with no preview.
//   - "verbose" gives `{emoji} {tool}({keys})` and the JSON arguments on the
//     next line. Empty arguments give the short form with the full preview.
//
// A child line from delegate_task has a tool that starts with "🔀 [i/n] ".
// It gets no emoji, and the preview follows in quotes.
func ToolLine(mode, tool, preview, argsJSON string) string {
	preview = strings.Join(strings.Fields(preview), " ")
	if strings.HasPrefix(tool, childPrefix) {
		if preview == "" {
			return tool
		}
		return fmt.Sprintf("%s %q", tool, cutPreview(preview))
	}
	emoji := defaultToolEmoji
	if e, ok := ToolEmoji[tool]; ok {
		emoji = e
	}
	if mode == "verbose" {
		if keys, compact, ok := argKeys(argsJSON); ok {
			return fmt.Sprintf("%s %s(%s)\n%s", emoji, tool, strings.Join(keys, ", "), compact)
		}
	} else {
		preview = cutPreview(preview)
	}
	if preview == "" {
		return emoji + " " + tool + "..."
	}
	return fmt.Sprintf("%s %s: \"%s\"", emoji, tool, preview)
}

// cutPreview cuts s to previewMax characters, with "..." at the end of a cut.
func cutPreview(s string) string {
	r := []rune(s)
	if len(r) <= previewMax {
		return s
	}
	return string(r[:previewMax-3]) + "..."
}

// argKeys returns the top-level keys of a JSON object in their order, and
// the compact JSON. ok is false for an empty object or a value that is not an
// object.
func argKeys(argsJSON string) (keys []string, compact string, ok bool) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(argsJSON)); err != nil {
		return nil, "", false
	}
	dec := json.NewDecoder(strings.NewReader(argsJSON))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, "", false
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, "", false
		}
		k, _ := t.(string)
		keys = append(keys, k)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, "", false
		}
	}
	return keys, buf.String(), len(keys) > 0
}

// Status is the tool progress message of one run. All lines go
// into one message that is edited in place, at most once per Interval. A
// timer sends a pending edit when no new line arrives. The methods are safe
// for concurrent use.
type Status struct {
	C        *Client
	ChatID   int64
	ThreadID string
	Mode     string
	Interval time.Duration
	Now      func() time.Time
	Cleanup  bool
	Log      *slog.Logger

	mu         sync.Mutex
	lines      []string // the lines of the open bubble
	msgID      int      // the open bubble, 0 before its first send
	shown      string   // the text that the open bubble shows
	lastTool   string
	lastLine   string
	repeat     int
	noEdit     bool // set after a permanent edit error: send each line alone
	lastEdit   time.Time
	floodUntil time.Time
	ids        []int // every status message, for the cleanup
	timer      *time.Timer
	flushCtx   context.Context
	closed     bool
}

// Add adds the line for one tool call. Identical consecutive lines collapse
// to one line with "(×N)". "new" mode adds a line only when the tool name
// changes, and "off" mode does nothing.
func (s *Status) Add(ctx context.Context, tool, preview, args string) {
	mode := s.Mode
	if mode == "" {
		mode = "all"
	}
	if mode == "off" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if mode == "new" && tool == s.lastTool {
		return
	}
	s.lastTool = tool
	s.flushCtx = context.WithoutCancel(ctx)
	line := cutUTF16(ToolLine(mode, tool, preview, args), statusTextLimit)

	if mode != "verbose" && line == s.lastLine && len(s.lines) > 0 {
		s.repeat++
		s.lines[len(s.lines)-1] = fmt.Sprintf("%s (×%d)", line, s.repeat+1)
		if s.noEdit {
			return
		}
		s.flush(ctx, false)
		return
	}
	s.lastLine, s.repeat = line, 0
	if s.noEdit {
		s.sendLine(ctx, line)
		return
	}
	if len(s.lines) > 0 && UTF16Len(strings.Join(s.lines, "\n")+"\n"+line) > statusTextLimit {
		// Close the full bubble with its last lines, and start a new one.
		s.flush(ctx, true)
		s.openBubble()
	}
	s.lines = append(s.lines, line)
	s.flush(ctx, false)
}

// Reset closes the open bubble, so the next tool line starts a new message
// below the streamed answer. The closed bubble gets its pending lines first.
func (s *Status) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if s.msgID != 0 && !s.noEdit && s.flushCtx != nil {
		ctx, cancel := context.WithTimeout(s.flushCtx, flushTimeout)
		s.flush(ctx, true)
		cancel()
	}
	s.openBubble()
	s.lastLine, s.repeat = "", 0
}

// Close sends the pending lines. It deletes all status messages when Cleanup
// and success are both true. Add does nothing after Close.
func (s *Status) Close(ctx context.Context, success bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.stopTimer()
	if !s.noEdit && len(s.lines) > 0 {
		s.flush(ctx, true)
	}
	if s.Cleanup && success {
		for _, id := range s.ids {
			if err := s.C.DeleteMessage(ctx, s.ChatID, id); err != nil {
				s.logger().Info("telegram status delete failed", "err", err.Error())
			}
		}
	}
}

func (s *Status) openBubble() {
	s.stopTimer()
	s.lines, s.msgID, s.shown = nil, 0, ""
}

// flush shows the open bubble text. Without force, an edit waits for the
// interval and for a flood wait, and a timer sends it later. s.mu is held.
func (s *Status) flush(ctx context.Context, force bool) {
	text := strings.Join(s.lines, "\n")
	if text == "" || text == s.shown {
		s.stopTimer()
		return
	}
	now := s.now()
	if s.msgID == 0 {
		m, err := s.send(ctx, text)
		if err != nil {
			s.logger().Info("telegram status send failed", "err", err.Error())
			return
		}
		s.msgID, s.shown, s.lastEdit = m.MessageID, text, now
		s.ids = append(s.ids, m.MessageID)
		s.stopTimer()
		return
	}
	if !force {
		wait := max(s.lastEdit.Add(s.interval()).Sub(now), s.floodUntil.Sub(now))
		if wait > 0 {
			s.armTimer(wait)
			return
		}
	}
	_, err := noWaitClient(s.C).EditMessageText(ctx, EditMessageTextParams{ChatID: s.ChatID, MessageID: s.msgID, Text: text})
	s.lastEdit = now
	switch {
	case err == nil || isNotModified(err):
		s.shown = text
		s.stopTimer()
	case isTransient(err):
		// Keep the edits. The next line or the timer tries again.
		s.logger().Info("telegram status edit failed, retry later", "err", err.Error())
		s.armTimer(s.interval())
	default:
		if wait, ok := floodWait(err); ok {
			s.floodUntil = now.Add(max(wait, s.interval()))
			s.logger().Info("telegram status flood control, back off", "wait_s", wait.Seconds())
			s.armTimer(s.floodUntil.Sub(now))
			return
		}
		// A permanent error: the message cannot be edited. Send each new
		// line as its own message (Hermes).
		s.logger().Info("telegram status edit failed, send each line", "err", err.Error())
		s.noEdit = true
		s.stopTimer()
		s.sendLine(ctx, s.lines[len(s.lines)-1])
	}
}

func (s *Status) sendLine(ctx context.Context, line string) {
	m, err := s.send(ctx, line)
	if err != nil {
		s.logger().Info("telegram status send failed", "err", err.Error())
		return
	}
	s.ids = append(s.ids, m.MessageID)
}

func (s *Status) send(ctx context.Context, text string) (Message, error) {
	p := SendMessageParams{ChatID: s.ChatID, Text: text}
	if id, ok := ThreadParam(s.ThreadID, false); ok {
		p.MessageThreadID = id
	}
	return noWaitClient(s.C).SendMessage(ctx, p)
}

// armTimer starts the timer for a later flush. s.mu is held.
func (s *Status) armTimer(d time.Duration) {
	if s.timer != nil {
		return
	}
	s.timer = time.AfterFunc(d, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.timer = nil
		if s.closed || s.flushCtx == nil {
			return
		}
		ctx, cancel := context.WithTimeout(s.flushCtx, flushTimeout)
		defer cancel()
		s.flush(ctx, false)
	})
}

func (s *Status) stopTimer() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

func (s *Status) interval() time.Duration {
	if s.Interval <= 0 {
		return defaultStatusInterval
	}
	return s.Interval
}

func (s *Status) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s *Status) logger() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}

// floodError is a rate-limit reply with a wait that is too long to do inline.
type floodError struct{ RetryAfter time.Duration }

func (e *floodError) Error() string {
	return fmt.Sprintf("telegram flood control: retry after %s", e.RetryAfter)
}

// noWaitClient returns a copy of c for status and stream calls. On a
// rate-limit reply it waits once inline when the wait is 5 s or less. Else
// the call fails at once with a *floodError, so the caller can back off
// without a block (Hermes edit_message).
func noWaitClient(c *Client) *Client {
	cc := *c
	waited := false
	cc.sleep = func(ctx context.Context, d time.Duration) error {
		if waited || d > maxInlineFloodWait {
			return &floodError{RetryAfter: d}
		}
		waited = true
		return c.wait(ctx, d)
	}
	return &cc
}

// floodWait reports whether err is a rate-limit error, and the wait.
func floodWait(err error) (time.Duration, bool) {
	var fe *floodError
	if errors.As(err, &fe) {
		return fe.RetryAfter, true
	}
	var ae *APIError
	if errors.As(err, &ae) && ae.Code == 429 {
		return time.Duration(ae.RetryAfter) * time.Second, true
	}
	return 0, false
}

// isNotModified reports whether err says that the new text equals the old
// text. That is not a failure.
func isNotModified(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && strings.Contains(strings.ToLower(ae.Description), "not modified")
}

// isTransient reports whether err is a network error and not a reply from
// Telegram. The next call can work.
func isTransient(err error) bool {
	var ae *APIError
	var fe *floodError
	return !errors.As(err, &ae) && !errors.As(err, &fe)
}

// cutUTF16 cuts s to at most limit UTF-16 code units, at a rune border.
func cutUTF16(s string, limit int) string {
	if UTF16Len(s) <= limit {
		return s
	}
	return SplitUTF16(s, limit)[0]
}
