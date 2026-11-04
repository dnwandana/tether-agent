package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/tools"
)

// ToolEmoji maps a tool name to the emoji of its status line. The values come
// from the Hermes tool registry (agent/display.py get_tool_emoji). Where
// Tether Agent has a different name for the same tool, the Hermes tool is in
// the comment. Other tools use defaultToolEmoji.
var ToolEmoji = map[string]string{
	"terminal":                "💻",
	"read_file":               "📖",
	"write_file":              "✍️",
	"patch":                   "🔧",
	"web_search":              "🔍",
	"web_fetch":               "📄", // web_extract
	"clarify":                 "❓",
	"memory":                  "🧠",
	"session_search":          "🔍",
	"skills_list":             "📚",
	"skill_view":              "📚",
	"skill_manage":            "📝",
	"delegate_task":           "🔀",
	"cronjob":                 "⏰",
	"execute_code":            "🐍",
	"view_image":              "👁️", // vision_analyze
	"text_to_speech":          "🔊",
	"generate_image":          "🎨",
	"browser_navigate":        "🌐",
	"browser_click":           "👆",
	"browser_fill":            "⌨️", // browser_type
	"browser_read":            "📸",  // browser_snapshot
	"browser_take_screenshot": "👁️", // browser_vision
	"browser_run_js":          "🖥️", // browser_console
	"todo_list":               "📋",  // todo
}

const (
	// defaultToolEmoji is the emoji of a tool that is not in ToolEmoji.
	defaultToolEmoji = "🔧"
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
	// maxClosedEditTries is the number of tries for the last edit of a
	// closed bubble, the first try included.
	maxClosedEditTries = 3
)

// ToolLine returns the status line for one tool call.
//
//   - "all" and "new" give `{emoji} {tool}: "{preview}"`, with the preview
//     cut to 40 characters, or `{emoji} {tool}...` with no preview.
//   - "verbose" gives `{emoji} {tool}({keys})` and the JSON arguments on the
//     next line. Empty arguments give the short form with the full preview.
func ToolLine(mode, tool, preview, argsJSON string) string {
	preview = oneLine(preview)
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

// oneLine replaces each run of white space in s with one space.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
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

// Source is the origin of a tool call. The zero value is the main agent of
// the turn. A delegate_task child has the number of its batch and its name.
type Source struct {
	Batch uint64
	Child string
}

// groupKey identifies one line of the open bubble in "all" mode.
type groupKey struct {
	src  Source
	tool string
}

// statusGroup is the line of one group key and the number of its calls.
type statusGroup struct {
	idx   int // the index in Status.lines
	count int
}

// closedEdit is the last text of a closed bubble. The edit that closed the
// bubble failed for a short time, so the timer tries it again.
type closedEdit struct {
	msgID int
	text  string
	tries int
}

// queuedSend is a new message that waits in the send queue. When deletable is
// true, the sent message ID goes into Status.ids for the cleanup.
type queuedSend struct {
	text      string
	silent    bool
	deletable bool
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
	// FlushTimer starts the timer of a later flush and returns its stop function. Nil
	// means time.AfterFunc. Tests give a fake timer.
	FlushTimer func(d time.Duration, f func()) (stop func() bool)

	mu         sync.Mutex
	lines      []string                 // the lines of the open bubble
	msgID      int                      // the open bubble, 0 before its first send
	shown      string                   // the text that the open bubble shows
	groups     map[groupKey]statusGroup // the lines of the open bubble in "all" mode
	lastKey    groupKey                 // the key of the last call, for "new" mode
	lastLine   string                   // the line of the last call, with no count
	noEdit     bool                     // set after a permanent edit error: send each line alone
	splitting  bool                     // set while split closes the full bubble
	waiting    []closedEdit             // the edits of closed bubbles to try again, oldest first
	queue      []queuedSend             // the new messages that wait for a send, oldest first
	lastEdit   time.Time
	floodUntil time.Time
	holdUntil  time.Time   // the end of the last pace hold of a first send
	editHeld   bool        // the last forced edit got a pace hold and sent nothing
	ids        []int       // every status message, for the cleanup
	timer      func() bool // the stop function of the pending timer, nil when no timer waits
	timerGen   uint64      // the generation of the pending timer, for onTimer
	flushCtx   context.Context
	closed     bool
}

// Add adds the line for one tool call from src. The main agent gets the
// ToolLine form. A delegate_task child gets `🔀 [i/n] tool "preview"` in all
// modes, with the preview cut to 40 characters and no arguments. In "all"
// mode, each source and tool has one line in the open bubble. A new call
// replaces that line with its own preview and the count "(×N)". "new" mode
// adds a line only when the source or the tool changes, and "off" mode does
// nothing. The todo_list call of the main agent gets no line, because the plan
// messages show the list.
func (s *Status) Add(ctx context.Context, src Source, tool, preview, args string) {
	if src == (Source{}) && tool == "todo_list" {
		return
	}
	mode := s.mode()
	if mode == "off" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	key := groupKey{src: src, tool: tool}
	if mode == "new" && key == s.lastKey {
		return
	}
	s.lastKey = key
	s.flushCtx = context.WithoutCancel(ctx)
	var line string
	if src.Child != "" {
		line = tools.ChildProgressLine(src.Child, tool, cutPreview(oneLine(preview)))
	} else {
		line = ToolLine(mode, tool, preview, args)
	}
	line = cutUTF16(line, statusTextLimit)

	if s.noEdit {
		// No line can change: send each new line once, with no groups.
		if mode != "verbose" && line == s.lastLine {
			return
		}
		s.lastLine = line
		s.sendLine(ctx, line)
		return
	}
	s.lastLine = line
	if g, ok := s.groups[key]; ok && mode == "all" {
		old := s.lines[g.idx]
		s.lines[g.idx] = fmt.Sprintf("%s (×%d)", line, g.count+1)
		if s.fits(s.lines) {
			g.count++
			s.groups[key] = g
			s.flush(ctx, false)
			return
		}
		// The update does not fit. Close the bubble with its old text, and
		// start the line again in a new bubble.
		s.lines[g.idx] = old
		if s.split(ctx) {
			return
		}
	} else if len(s.lines) > 0 && !s.fits(append(slices.Clone(s.lines), line)) {
		if s.split(ctx) {
			return
		}
	}
	if mode == "all" {
		if s.groups == nil {
			s.groups = map[groupKey]statusGroup{}
		}
		s.groups[key] = statusGroup{idx: len(s.lines), count: 1}
	}
	s.lines = append(s.lines, line)
	s.flush(ctx, false)
}

// SetGoalLine sends the goal line of the turn as its own silent message in each mode. The
// cleanup deletes it, except in "off" mode.
func (s *Status) SetGoalLine(ctx context.Context, line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.flushCtx = context.WithoutCancel(ctx)
	s.enqueue(ctx, cutUTF16(line, statusTextLimit), true, s.mode() != "off")
}

// SetPlan sends the plan message of one todo_list call of the main agent. before is the
// list before the call, and after is the list after the call. The message closes the open
// bubble, so the next tool line goes out below it. "off" mode sends no plan message.
func (s *Status) SetPlan(ctx context.Context, before, after []tools.TodoItem) {
	if s.mode() == "off" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	text, done := planMessage(before, after)
	if text == "" {
		return
	}
	s.flushCtx = context.WithoutCancel(ctx)
	s.closeBubble(ctx)
	// The next tool line is not a repeat of the line above the plan message.
	s.lastLine, s.lastKey = "", groupKey{}
	// The cleanup keeps the done list.
	s.enqueue(ctx, text, true, !done)
}

// textOf returns the text of a bubble with lines, at most statusTextLimit UTF-16 units.
// s.mu is held.
func (s *Status) textOf(lines []string) string {
	text, _ := s.compose(lines)
	return cutUTF16(text, statusTextLimit)
}

// fits reports whether the text of a bubble with lines fits in statusTextLimit with no
// cut. s.mu is held.
func (s *Status) fits(lines []string) bool {
	_, ok := s.compose(lines)
	return ok
}

// compose returns the text of a bubble with lines before the cut. A bubble shows tool
// lines only. ok is true when the text fits in statusTextLimit. s.mu is held.
func (s *Status) compose(lines []string) (text string, ok bool) {
	text = strings.Join(lines, "\n")
	return text, UTF16Len(text) <= statusTextLimit
}

// planMarker returns the marker of an item line for status.
func planMarker(status string) string {
	switch status {
	case "completed":
		return "[x]"
	case "in_progress":
		return "[>]"
	case "cancelled":
		return "[~]"
	default:
		return "[ ]"
	}
}

// split closes the full bubble with its last lines, and opens a new one. It
// returns true when the flush found a permanent edit error. Then the flush
// sent the line of the call alone, and the caller must stop. s.mu is held.
func (s *Status) split(ctx context.Context) bool {
	if s.msgID == 0 {
		// The flush holds a bubble that waits for its first send, so the
		// lines of the bubble must go into the send queue.
		s.closeBubble(ctx)
		return false
	}
	s.splitting = true
	s.flush(ctx, true)
	s.splitting = false
	s.keepFailedEdit()
	s.openBubble()
	return s.noEdit
}

// Reset closes the open bubble, so the next tool line starts a new message
// below the streamed answer. The closed bubble gets its pending lines first.
// A bubble that waits for its first send goes into the send queue.
func (s *Status) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	ctx := context.Background()
	if s.flushCtx != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(s.flushCtx, flushTimeout)
		defer cancel()
	}
	s.closeBubble(ctx)
	s.lastLine, s.lastKey = "", groupKey{}
}

// Close closes the open bubble and sends the queue one time. It deletes all
// status messages when Cleanup and success are both true. Add does nothing
// after Close. The timer still tries the failed edits of closed bubbles, so
// the final counts can arrive later.
func (s *Status) Close(ctx context.Context, success bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	cleanup := s.Cleanup && success
	if cleanup {
		// Do not send a message that the cleanup deletes next.
		s.dropDeletable()
	}
	s.closeBubble(ctx)
	s.stopTimer()
	// After Close, sendQueue drops an entry on a network error or a 5xx. An entry
	// that waits for a flood wait stays, and the timer sends it later.
	if wait := s.sendQueue(ctx); wait > 0 {
		s.armTimer(wait)
	}
	if cleanup {
		// A deleted message needs no edit.
		s.waiting = nil
		for _, id := range s.ids {
			if err := s.C.DeleteMessage(ctx, s.ChatID, id); err != nil {
				s.logger().Info("telegram status delete failed", "err", err.Error())
			}
		}
	} else if len(s.waiting) > 0 {
		s.armTimer(s.editWait())
	}
}

// dropDeletable removes the queue entries that the cleanup deletes. The other
// entries keep their order. A bubble that waits for its first send loses its
// lines, because the cleanup deletes it too. s.mu is held.
func (s *Status) dropDeletable() {
	s.queue = slices.DeleteFunc(s.queue, func(e queuedSend) bool { return e.deletable })
	if s.msgID == 0 {
		s.lines, s.groups = nil, nil
	}
}

// DropCleanupEntries drops the queued messages that the cleanup of a successful turn
// deletes, so they do not go out before the final answer.
func (s *Status) DropCleanupEntries(success bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Cleanup && success {
		s.dropDeletable()
	}
}

// Drain sends the queue, oldest first, and then a bubble that waits for its
// first send. Thus the tool lines before an answer message go out above it. It
// waits for each flood wait without s.mu, so the timer and the setters do not
// block. It returns when the queue is empty, when ctx ends, or after
// flushTimeout. The timer sends the entries that are left.
func (s *Status) Drain(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	deadline := s.now().Add(flushTimeout)
	for {
		s.mu.Lock()
		wait := s.sendQueue(ctx)
		if wait == 0 {
			wait = s.sendHeld(ctx)
		}
		if wait > 0 && !s.closed {
			s.armTimer(wait)
		}
		s.mu.Unlock()
		if wait <= 0 {
			return
		}
		left := deadline.Sub(s.now())
		if left <= 0 {
			return
		}
		t := time.NewTimer(min(wait, left))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// sendHeld sends a bubble that waits for its first send. It returns the wait
// before the next try during a flood wait, else 0. s.mu is held.
func (s *Status) sendHeld(ctx context.Context) time.Duration {
	if s.msgID != 0 || len(s.lines) == 0 || s.noEdit || s.closed {
		return 0
	}
	s.flush(ctx, true)
	if s.msgID == 0 {
		now := s.now()
		return max(s.floodUntil.Sub(now), s.holdUntil.Sub(now), 0)
	}
	return 0
}

// keepFailedEdit runs after the forced flush of a bubble that closes. When
// the edit failed for a short time or for a flood wait, it keeps the text
// for the timer. A permanent error set noEdit, and a send error left no
// message to edit. s.mu is held.
func (s *Status) keepFailedEdit() {
	text := s.textOf(s.lines)
	held := s.editHeld
	s.editHeld = false
	if s.msgID == 0 || s.noEdit || text == s.shown {
		return
	}
	// A held edit sent no request, so it is not a try.
	tries := 1
	if held {
		tries = 0
	}
	s.waiting = append(s.waiting, closedEdit{msgID: s.msgID, text: text, tries: tries})
}

func (s *Status) openBubble() {
	s.stopTimer()
	s.lines, s.msgID, s.shown, s.groups = nil, 0, "", nil
	if len(s.waiting) > 0 || len(s.queue) > 0 {
		s.armTimer(s.editWait())
	}
}

// editClosed tries the waiting edits of closed bubbles, oldest first. It
// returns false when an edit must wait for the interval or for a flood wait.
// Then the timer runs again later. s.mu is held.
func (s *Status) editClosed(ctx context.Context) bool {
	for len(s.waiting) > 0 {
		if wait := s.editWait(); wait > 0 {
			s.armTimer(wait)
			return false
		}
		e := &s.waiting[0]
		now := s.now()
		_, err := interimClient(s.C).EditMessageText(ctx, EditMessageTextParams{ChatID: s.ChatID, MessageID: e.msgID, Text: e.text})
		// A pace hold sent no request, so it is not a try and sets no lastEdit.
		if hold, ok := isPaceHold(err); ok {
			s.logger().Debug("telegram status pace hold", "kind", "closed_edit", "wait_s", hold.Seconds())
			s.armTimer(hold)
			return false
		}
		e.tries++
		s.lastEdit = now
		if err != nil && !isNotModified(err) {
			wait, flood := floodWait(err)
			if flood {
				s.floodUntil = now.Add(max(wait, s.interval()))
			}
			switch {
			case !flood && !isTransient(err):
				// The message cannot be edited. Only this bubble keeps its
				// old text, and the open bubble is not affected.
				s.logger().Info("telegram status edit of a closed bubble failed, drop it", "err", err.Error())
			case e.tries >= maxClosedEditTries:
				s.logger().Info("telegram status edit of a closed bubble failed too many times, drop it",
					"err", err.Error(), "tries", e.tries)
			default:
				s.logger().Info("telegram status edit of a closed bubble failed, retry later", "err", err.Error())
				continue
			}
		}
		s.waiting = s.waiting[1:]
	}
	return true
}

// stopIdleTimer stops the timer when no closed bubble waits for an edit and
// no new message waits in the send queue. s.mu is held.
func (s *Status) stopIdleTimer() {
	if len(s.waiting) == 0 && len(s.queue) == 0 {
		s.stopTimer()
	}
}

// editWait returns the time until the next edit can go out. s.mu is held.
func (s *Status) editWait() time.Duration {
	now := s.now()
	return max(s.lastEdit.Add(s.interval()).Sub(now), s.floodUntil.Sub(now))
}

// flush shows the open bubble text. Without force, an edit waits for the
// interval and for a flood wait, and a timer sends it later. s.mu is held.
func (s *Status) flush(ctx context.Context, force bool) {
	text := s.textOf(s.lines)
	// A bubble with no tool line has no text, so it never goes out.
	if text == "" || text == s.shown {
		s.stopIdleTimer()
		return
	}
	now := s.now()
	if s.msgID == 0 {
		// A new bubble must not go out above a message that waits in the queue.
		if len(s.queue) > 0 || now.Before(s.floodUntil) {
			s.armTimer(max(s.floodUntil.Sub(now), 0))
			return
		}
		m, err := s.send(ctx, text, false)
		if hold, ok := isPaceHold(err); ok {
			s.logger().Debug("telegram status pace hold", "kind", "send", "wait_s", hold.Seconds())
			s.holdUntil = now.Add(hold)
			s.armTimer(hold)
			return
		}
		if err != nil {
			s.logger().Info("telegram status send failed", "err", err.Error())
			if wait, ok := floodWait(err); ok {
				s.floodUntil = now.Add(max(wait, s.interval()))
				s.armTimer(s.floodUntil.Sub(now))
			}
			return
		}
		s.msgID, s.shown, s.lastEdit = m.MessageID, text, now
		s.ids = append(s.ids, m.MessageID)
		s.stopIdleTimer()
		return
	}
	if !force {
		// The waiting edits of closed bubbles go first. The timer sends
		// them, and then this edit.
		if wait := s.editWait(); wait > 0 || len(s.waiting) > 0 {
			s.armTimer(max(wait, 0))
			return
		}
	}
	_, err := interimClient(s.C).EditMessageText(ctx, EditMessageTextParams{ChatID: s.ChatID, MessageID: s.msgID, Text: text})
	if hold, ok := isPaceHold(err); ok {
		s.logger().Debug("telegram status pace hold", "kind", "edit", "wait_s", hold.Seconds())
		s.editHeld = force
		s.armTimer(hold)
		return
	}
	s.editHeld = false
	s.lastEdit = now
	switch {
	case err == nil || isNotModified(err):
		s.shown = text
		s.stopIdleTimer()
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
		s.stopIdleTimer()
		if line := s.noEditLine(); line != "" {
			s.sendLine(ctx, line)
		}
	}
}

// noEditLine returns the line to send when edits stop. In "all" mode it is
// the group line of the last call, so the count stays visible. The last line
// of the bubble can be an earlier group. During a split, the group line in the
// full bubble does not have the new call yet, so the line of the call goes
// out. A line that the bubble shows already is not sent again. s.mu is held.
func (s *Status) noEditLine() string {
	if s.mode() != "all" || s.splitting {
		return s.lastLine
	}
	g, ok := s.groups[s.lastKey]
	if !ok {
		return s.lastLine
	}
	line := s.lines[g.idx]
	for shown := range strings.SplitSeq(s.shown, "\n") {
		if shown == line {
			return ""
		}
	}
	return line
}

// sendLine sends line as its own message. The line goes through the send
// queue, so it does not go out above a message that waits. s.mu is held.
func (s *Status) sendLine(ctx context.Context, line string) {
	s.enqueue(ctx, line, false, true)
}

// enqueue puts a new message at the end of the send queue. The message goes
// out at once when no other message waits and no flood wait runs. s.mu is
// held.
func (s *Status) enqueue(ctx context.Context, text string, silent, deletable bool) {
	s.queue = append(s.queue, queuedSend{text: text, silent: silent, deletable: deletable})
	if len(s.queue) > 1 {
		// The timer runs for the head of the queue.
		return
	}
	if wait := s.sendQueue(ctx); wait > 0 {
		s.armTimer(wait)
	}
}

// sendQueue sends the waiting messages, oldest first. It returns 0 when the
// queue is empty, else the wait before the next try. s.mu is held.
func (s *Status) sendQueue(ctx context.Context) time.Duration {
	for len(s.queue) > 0 {
		now := s.now()
		if now.Before(s.floodUntil) {
			return s.floodUntil.Sub(now)
		}
		e := s.queue[0]
		m, err := s.send(ctx, e.text, e.silent)
		if hold, ok := isPaceHold(err); ok {
			// The entry stays at the head, and floodUntil does not change.
			s.logger().Debug("telegram status pace hold", "kind", "send", "wait_s", hold.Seconds())
			return hold
		}
		if err == nil {
			if e.deletable {
				s.ids = append(s.ids, m.MessageID)
			}
			s.queue = s.queue[1:]
			continue
		}
		if wait, ok := floodWait(err); ok {
			// The entry stays at the head, also after Close.
			s.floodUntil = now.Add(max(wait, s.interval()))
			s.logger().Info("telegram status flood control, back off", "wait_s", wait.Seconds())
			return s.floodUntil.Sub(now)
		}
		var ae *APIError
		retry := !errors.As(err, &ae) || ae.Code >= 500
		// After Close, no new event can come, so a closed Status must not
		// try a failed send for ever.
		if retry && !s.closed {
			s.logger().Info("telegram status send failed, retry later", "err", err.Error())
			return s.interval()
		}
		s.logger().Info("telegram status send failed, drop it", "err", err.Error())
		s.queue = s.queue[1:]
	}
	return 0
}

// closeBubble closes the open bubble and opens a new one. A sent bubble gets
// its pending lines, but no forced edit during a flood wait. A bubble that
// waits for its first send goes into the send queue, so its lines stay.
// s.mu is held.
func (s *Status) closeBubble(ctx context.Context) {
	s.editHeld = false
	switch {
	case s.msgID != 0 && !s.noEdit:
		if !s.now().Before(s.floodUntil) {
			s.flush(ctx, true)
		}
		s.keepFailedEdit()
	case s.msgID == 0 && len(s.lines) > 0:
		s.enqueue(ctx, s.textOf(s.lines), false, true)
	}
	s.openBubble()
}

// send sends text as a new message. A silent message makes no sound.
func (s *Status) send(ctx context.Context, text string, silent bool) (Message, error) {
	p := SendMessageParams{ChatID: s.ChatID, Text: text, DisableNotification: silent}
	if id, ok := ThreadParam(s.ThreadID, false); ok {
		p.MessageThreadID = id
	}
	return queueClient(s.C).SendMessage(ctx, p)
}

// armTimer starts the timer for a later flush. The callback gets the
// generation of this timer. s.mu is held.
func (s *Status) armTimer(d time.Duration) {
	if s.timer != nil {
		return
	}
	s.timerGen++
	gen := s.timerGen
	f := func() { s.onTimer(gen) }
	if s.FlushTimer != nil {
		s.timer = s.FlushTimer(d, f)
		return
	}
	s.timer = time.AfterFunc(d, f).Stop
}

// onTimer sends the edits that waited for the interval or for a flood wait.
// gen is the generation of the timer that fired. A timer can fire while
// stopTimer stops it, and then its callback waits for s.mu. When gen is not
// the current generation, that timer was stopped, so the callback does
// nothing. It does not touch s.timer, which can be the handle of a newer
// timer.
func (s *Status) onTimer(gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.timerGen {
		return
	}
	s.timer = nil
	if s.flushCtx == nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.flushCtx, flushTimeout)
	defer cancel()
	// The queue goes first, so the new messages keep the order of the events. The
	// edits and the open bubble wait for the next timer.
	if wait := s.sendQueue(ctx); wait > 0 {
		s.armTimer(wait)
		return
	}
	// After Close, and after a permanent edit error, only the closed
	// bubbles get edits.
	if !s.editClosed(ctx) || s.closed || s.noEdit {
		return
	}
	s.flush(ctx, false)
}

// stopTimer stops the pending timer. It changes the generation, so a
// callback of that timer that fired already does nothing. s.mu is held.
func (s *Status) stopTimer() {
	if s.timer != nil {
		s.timer()
		s.timer = nil
		s.timerGen++
	}
}

// mode returns Mode, with "all" for an empty value.
func (s *Status) mode() string {
	if s.Mode == "" {
		return "all"
	}
	return s.Mode
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
	cc.inlineWaited = new(bool)
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
	var ph *paceHold
	return !errors.As(err, &ae) && !errors.As(err, &fe) && !errors.As(err, &ph)
}

// cutUTF16 cuts s to at most limit UTF-16 code units, at a rune border.
func cutUTF16(s string, limit int) string {
	if UTF16Len(s) <= limit {
		return s
	}
	return SplitUTF16(s, limit)[0]
}
