package telegram

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/config"
)

const (
	// maxMessageLen is the Telegram message limit in UTF-16 code units.
	maxMessageLen = 4096
	// numberRoom is the space that a " \(12/34\)" part suffix needs.
	numberRoom = 16
	// maxStreamInterval is the longest edit interval after flood errors.
	maxStreamInterval = 10 * time.Second
	// maxFloodStrikes is the number of flood errors in a row that stop the
	// edits for the rest of the run (Hermes _MAX_FLOOD_STRIKES).
	maxFloodStrikes = 3
	// minNewMessageChars is the smallest visible text for a new message
	// with the cursor. A shorter text waits, so a lone cursor never stays
	// on screen (Hermes _MIN_NEW_MSG_CHARS).
	minNewMessageChars = 4
)

// draftSeq gives each streamed answer segment its own draft id. Telegram
// animates a draft when the same non-zero id comes again.
var draftSeq atomic.Int64

// Streamer streams one answer into a Telegram chat. It is a Go
// port of Hermes gateway/stream_consumer.py.
//
// During the stream, the text goes into one message that is edited in place
// as plain text with the cursor, or into a native draft in a DM. Boundary and
// Finish close the message with the final MarkdownV2 text. The agent sink
// goroutine calls the methods. A timer sends a pending edit when no new text
// arrives, so the methods use a lock.
type Streamer struct {
	C        *Client
	ChatID   int64
	ThreadID string
	IsDM     bool
	Cfg      config.TelegramStreaming
	Now      func() time.Time
	// OnFirstMessage is called each time a new answer message appears. The
	// channel uses it to call Status.Reset, so the next tool line starts a
	// new status message below the answer.
	OnFirstMessage func()
	Log            *slog.Logger

	mu       sync.Mutex
	started  bool
	finished bool
	draft    bool  // stream into a native draft
	draftID  int64 // the draft id of the current segment
	editsOff bool  // no more stream sends: one final send per segment
	interval time.Duration
	strikes  int

	// The current segment.
	acc        string    // the raw text of the segment
	committed  string    // the clean text in closed messages of the segment
	msgID      int       // the open message, 0 before its first send
	msgCreated time.Time // when the open message appeared
	lastSent   string    // the text that the open message or draft shows
	pending    int       // new characters since the last flush
	lastEdit   time.Time

	timer  *time.Timer
	runCtx context.Context
}

// Delta adds streamed text.
func (s *Streamer) Delta(ctx context.Context, text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init(ctx)
	if s.finished {
		return
	}
	s.acc += text
	s.pending += utf8.RuneCountInString(text)
	if s.editsOff {
		return
	}
	now := s.now()
	threshold := s.Cfg.BufferThreshold
	if now.Sub(s.lastEdit) >= s.interval || (threshold > 0 && s.pending >= threshold) {
		s.streamFlush(ctx)
		return
	}
	s.armTimer(s.lastEdit.Add(s.interval).Sub(now))
}

// Boundary closes the current message at a tool boundary, so the next text
// starts a new message below the tool status.
func (s *Streamer) Boundary(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init(ctx)
	if s.finished {
		return
	}
	s.stopTimer()
	if s.view() != "" || s.msgID != 0 {
		if err := s.finalize(ctx); err != nil {
			s.logger().Warn("telegram stream segment send failed", "err", err.Error())
		}
		s.lastEdit = s.now()
	}
	s.resetSegment()
}

// Finish sends the final text of the last segment. An empty final uses the
// streamed text. The final text must not hold the text of earlier segments,
// and its MEDIA tags must be taken out already. Calls after Finish do
// nothing.
func (s *Streamer) Finish(ctx context.Context, final string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init(ctx)
	if s.finished {
		return nil
	}
	s.finished = true
	s.stopTimer()
	if final != "" {
		s.acc = final
	}
	return s.finalize(ctx)
}

func (s *Streamer) init(ctx context.Context) {
	s.runCtx = context.WithoutCancel(ctx)
	if s.started {
		return
	}
	s.started = true
	s.interval = time.Duration(s.Cfg.EditIntervalS * float64(time.Second))
	switch strings.ToLower(s.Cfg.Transport) {
	case "auto", "draft":
		s.draft = s.IsDM
	}
	if s.draft {
		s.draftID = draftSeq.Add(1)
	}
}

// view returns the clean text of the open message: the segment text without
// the media tags and without the text in closed messages.
func (s *Streamer) view() string {
	full := cleanForDisplay(s.acc)
	if s.committed == "" {
		return full
	}
	if !strings.HasPrefix(full, s.committed) {
		s.logger().Info("telegram stream text changed before the sent part")
		return full
	}
	return strings.TrimLeft(full[len(s.committed):], "\n")
}

// commit marks the first n bytes of the view as sent in a closed message.
func (s *Streamer) commit(n int) {
	full := cleanForDisplay(s.acc)
	rest := full[len(s.committed):]
	lead := len(rest) - len(strings.TrimLeft(rest, "\n"))
	s.committed = full[:len(s.committed)+lead+n]
}

func (s *Streamer) safeLimit() int {
	return max(500, maxMessageLen-UTF16Len(s.Cfg.Cursor)-100)
}

// streamFlush shows the current text with the cursor. s.mu is held.
func (s *Streamer) streamFlush(ctx context.Context) {
	s.stopTimer()
	s.pending = 0
	defer func() { s.lastEdit = s.now() }()
	safe := s.safeLimit()
	for v := s.view(); UTF16Len(v) > safe; v = s.view() {
		if s.msgID == 0 {
			// No message to edit: send numbered parts now (Hermes).
			parts := NumberParts(SplitUTF16(v, safe-numberRoom), false)
			for _, p := range parts {
				if _, err := s.sendNew(ctx, p, false); err != nil {
					s.logger().Info("telegram stream send failed", "err", err.Error())
					s.editsOff = true
					return
				}
			}
			s.commit(len(v))
			return
		}
		// Close the open message with the first part. The rest goes into
		// a new message.
		chunk := SplitUTF16(v, safe)[0]
		if !s.editStream(ctx, chunk) {
			return
		}
		s.commit(len(chunk))
		s.msgID, s.lastSent, s.msgCreated = 0, "", time.Time{}
	}
	s.sendOrEdit(ctx, s.view()+s.Cfg.Cursor)
}

// sendOrEdit shows text in the open message, the draft, or a new message.
// It is Hermes _send_or_edit without finalize. s.mu is held.
func (s *Streamer) sendOrEdit(ctx context.Context, text string) {
	cursor := s.Cfg.Cursor
	visible := text
	if cursor != "" {
		visible = strings.ReplaceAll(visible, cursor, "")
	}
	visible = strings.TrimSpace(visible)
	if visible == "" || text == s.lastSent {
		return
	}
	if s.msgID == 0 && cursor != "" && strings.Contains(text, cursor) && utf8.RuneCountInString(visible) < minNewMessageChars {
		return
	}
	if s.draft && s.msgID == 0 {
		p := SendMessageDraftParams{ChatID: s.ChatID, DraftID: s.draftID, Text: cutUTF16(text, maxMessageLen)}
		if id, ok := ThreadParam(s.ThreadID, false); ok {
			p.MessageThreadID = id
		}
		err := noWaitClient(s.C).SendMessageDraft(ctx, p)
		if err == nil {
			s.lastSent = text
			return
		}
		// One failure turns drafts off for the run (Hermes).
		s.logger().Info("telegram draft failed, use edits", "err", err.Error())
		s.draft = false
	}
	if s.msgID != 0 {
		s.editStream(ctx, text)
		return
	}
	if _, err := s.sendNew(ctx, text, false); err != nil {
		s.logger().Info("telegram stream send failed, send the final text only", "err", err.Error())
		s.editsOff = true
	}
}

// editStream edits the open message with plain text. A flood error doubles
// the interval. The third flood error in a row, or another error, stops the
// stream edits. s.mu is held.
func (s *Streamer) editStream(ctx context.Context, text string) bool {
	_, err := noWaitClient(s.C).EditMessageText(ctx, EditMessageTextParams{ChatID: s.ChatID, MessageID: s.msgID, Text: text})
	if err == nil || isNotModified(err) {
		s.lastSent = text
		s.strikes = 0
		return true
	}
	if _, ok := floodWait(err); ok {
		s.strikes++
		s.interval = min(2*s.interval, maxStreamInterval)
		s.logger().Info("telegram stream flood control, back off", "strike", s.strikes, "interval_s", s.interval.Seconds())
		if s.strikes < maxFloodStrikes {
			return false
		}
	}
	s.logger().Info("telegram stream edit failed, send the final text only", "err", err.Error())
	s.editsOff = true
	s.stripCursor(ctx)
	return false
}

// stripCursor removes the cursor from the open message. It is best effort.
func (s *Streamer) stripCursor(ctx context.Context) {
	if s.msgID == 0 || s.Cfg.Cursor == "" || !strings.HasSuffix(s.lastSent, s.Cfg.Cursor) {
		return
	}
	text := strings.TrimSuffix(s.lastSent, s.Cfg.Cursor)
	if strings.TrimSpace(text) == "" {
		return
	}
	_, err := noWaitClient(s.C).EditMessageText(ctx, EditMessageTextParams{ChatID: s.ChatID, MessageID: s.msgID, Text: text})
	if err == nil {
		s.lastSent = text
	}
}

// visiblePrefix returns the text that the user sees in the open message.
func (s *Streamer) visiblePrefix() string {
	if s.msgID == 0 {
		return ""
	}
	return cleanForDisplay(strings.TrimSuffix(s.lastSent, s.Cfg.Cursor))
}

// continuation returns the part of text that the open message does not show.
func (s *Streamer) continuation(text string) string {
	if p := s.visiblePrefix(); p != "" && strings.HasPrefix(text, p) {
		return strings.TrimLeft(text[len(p):], " \n")
	}
	return text
}

// finalize closes the segment with its final MarkdownV2 text. s.mu is held.
func (s *Streamer) finalize(ctx context.Context) error {
	text := s.view()
	if strings.TrimSpace(text) == "" {
		s.stripCursor(ctx)
		return nil
	}
	if s.editsOff {
		// After an edit failure, send only the text that the user did not
		// see (Hermes _send_fallback_final).
		cont := s.continuation(text)
		if strings.TrimSpace(cont) == "" {
			s.stripCursor(ctx)
			return nil
		}
		return s.sendFinal(ctx, cont)
	}
	if s.msgID == 0 {
		return s.sendFinal(ctx, text)
	}
	if s.freshFinalDue() {
		old := s.msgID
		if err := s.sendFinal(ctx, text); err == nil {
			if err := s.C.DeleteMessage(ctx, s.ChatID, old); err != nil {
				s.logger().Info("telegram preview delete failed", "err", err.Error())
			}
			return nil
		}
		s.msgID = old
	}
	return s.editFinal(ctx, text)
}

// freshFinalDue reports whether the open message is old enough that the
// final text goes into a new message, so its time shows the end of the run.
func (s *Streamer) freshFinalDue() bool {
	after := time.Duration(s.Cfg.FreshFinalAfterS * float64(time.Second))
	return after > 0 && s.msgID != 0 && !s.msgCreated.IsZero() && s.now().Sub(s.msgCreated) >= after
}

// finalParts converts text to MarkdownV2 and splits it at the message limit,
// with escaped part numbers.
func finalParts(text string) []string {
	formatted := ToMarkdownV2(text)
	if UTF16Len(formatted) <= maxMessageLen {
		return []string{formatted}
	}
	return NumberParts(SplitUTF16(formatted, maxMessageLen-numberRoom), true)
}

// editFinal edits the open message with the first final part and sends the
// other parts. When the edit fails, it sends the text that the user did not
// see. s.mu is held.
func (s *Streamer) editFinal(ctx context.Context, text string) error {
	parts := finalParts(text)
	plain := text
	if len(parts) > 1 {
		plain = StripMarkdownV2(parts[0])
	}
	c := noWaitClient(s.C)
	p := EditMessageTextParams{ChatID: s.ChatID, MessageID: s.msgID, Text: parts[0], ParseMode: "MarkdownV2"}
	_, err := c.EditMessageText(ctx, p)
	if err != nil && !isNotModified(err) && isBadRequest(err) {
		p.Text, p.ParseMode = plain, ""
		_, err = noWaitClient(s.C).EditMessageText(ctx, p)
	}
	if err != nil && !isNotModified(err) {
		s.logger().Info("telegram final edit failed, send the rest", "err", err.Error())
		cont := s.continuation(text)
		s.stripCursor(ctx)
		return s.sendFinal(ctx, cont)
	}
	s.lastSent = parts[0]
	for _, part := range parts[1:] {
		if err := s.sendPart(ctx, part); err != nil {
			return err
		}
	}
	return nil
}

// sendFinal sends text as new MarkdownV2 messages. s.mu is held.
func (s *Streamer) sendFinal(ctx context.Context, text string) error {
	for _, part := range finalParts(text) {
		if err := s.sendPart(ctx, part); err != nil {
			return err
		}
	}
	return nil
}

// sendPart sends one MarkdownV2 part. When Telegram rejects the entities, it
// sends the part again without the markup.
func (s *Streamer) sendPart(ctx context.Context, part string) error {
	_, err := s.sendNew(ctx, part, true)
	if err != nil && isBadRequest(err) {
		p := SendMessageParams{ChatID: s.ChatID, Text: StripMarkdownV2(part)}
		if id, ok := ThreadParam(s.ThreadID, false); ok {
			p.MessageThreadID = id
		}
		var m Message
		if m, err = s.C.SendMessage(ctx, p); err == nil {
			s.opened(m.MessageID, p.Text)
		}
	}
	return err
}

// sendNew sends a new message and makes it the open message. A stream send
// is plain text, and it fails at once on a long flood wait. A final send is
// MarkdownV2, and it waits for the rate limit (Client.Call), because the user
// must get the final text.
func (s *Streamer) sendNew(ctx context.Context, text string, final bool) (Message, error) {
	p := SendMessageParams{ChatID: s.ChatID, Text: text}
	c := s.C
	if final {
		p.ParseMode = "MarkdownV2"
	} else {
		c = noWaitClient(s.C)
	}
	if id, ok := ThreadParam(s.ThreadID, false); ok {
		p.MessageThreadID = id
	}
	m, err := c.SendMessage(ctx, p)
	if err != nil {
		return m, err
	}
	s.opened(m.MessageID, text)
	return m, nil
}

// opened records a new answer message and tells the channel.
func (s *Streamer) opened(id int, text string) {
	s.msgID, s.msgCreated, s.lastSent = id, s.now(), text
	if s.OnFirstMessage != nil {
		s.OnFirstMessage()
	}
}

func (s *Streamer) resetSegment() {
	s.acc, s.committed, s.lastSent = "", "", ""
	s.msgID, s.msgCreated, s.pending = 0, time.Time{}, 0
	if s.draft {
		s.draftID = draftSeq.Add(1)
	}
}

// armTimer starts the timer for a later flush. s.mu is held.
func (s *Streamer) armTimer(d time.Duration) {
	if s.timer != nil {
		return
	}
	s.timer = time.AfterFunc(max(d, 0), func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.timer = nil
		if s.finished || s.editsOff || s.pending == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(s.runCtx, flushTimeout)
		defer cancel()
		s.streamFlush(ctx)
	})
}

func (s *Streamer) stopTimer() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

func (s *Streamer) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s *Streamer) logger() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}

// isBadRequest reports whether Telegram rejected the request with HTTP 400,
// for example for MarkdownV2 entities that do not parse, or for a text that
// the escapes made too long.
func isBadRequest(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Code == 400
}
