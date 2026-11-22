package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/media"
	"github.com/dnwandana/tether-agent/internal/security"
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
	// maxStripTries is the number of later tries to remove the cursor after
	// a flood failure. Each try uses the flood budget of the chat, which the
	// next turn also uses.
	maxStripTries = 3
	// maxStripWait is the longest time after the first strip during which
	// a later try can run.
	maxStripWait = 2 * time.Minute
	// stripRetryPad is added to each flood wait. Telegram rounds retry_after
	// to whole seconds, so a try at the exact time can fail again.
	stripRetryPad = time.Second
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
	// BeforeNewMessage runs before the first message of each answer segment.
	// The channel uses it to call Status.Drain, so a plan message that waits
	// goes out above the answer. It runs with the Streamer lock held, so it
	// must not call the Streamer.
	BeforeNewMessage func(ctx context.Context)
	Log              *slog.Logger
	// AfterFunc runs f after d for a later strip of the cursor. Nil means
	// time.AfterFunc. Tests replace it.
	AfterFunc func(d time.Duration, f func())
	// FlushTimer runs f after d for a later flush of the stream. Nil means
	// time.AfterFunc. Tests replace it.
	FlushTimer func(d time.Duration, f func()) (stop func() bool)
	// Mask replaces the sensitive data of a text with labels and returns the findings with
	// their offsets in text. Nil shows the text as it is. The channel sets it for a chat that
	// is not private.
	Mask func(text string) (string, []security.Finding)
	// Partial returns the byte length of the longest suffix of tail that is the start of a
	// known exact value or of its JSON form. Nil holds no partial value.
	Partial func(tail string) int

	mu       sync.Mutex
	started  bool
	finished bool
	draft    bool  // stream into a native draft
	draftID  int64 // the draft id of the current segment
	editsOff bool  // no more stream sends: one final send per segment
	interval time.Duration
	strikes  int
	// floodUntil is the time when the current flood wait ends. A strip
	// before this time cannot work.
	floodUntil time.Time
	// floodOff is true when the third flood error in a row stopped the
	// edits.
	floodOff bool
	// holdUntil is the time before which no stream request goes out.
	holdUntil time.Time
	// held is true when the last stream request got a pace hold.
	held bool

	// The current segment.
	acc        string    // the raw text of the segment
	committed  string    // the clean text in closed messages of the segment
	msgID      int       // the open message, 0 before its first send
	msgCreated time.Time // when the open message appeared
	lastSent   string    // the text that the open message or draft shows
	pending    int       // new characters since the last flush
	lastEdit   time.Time
	drained    bool // BeforeNewMessage ran for the segment
	// parts are the numbered preview parts that did not go out yet. They
	// keep the numbers of the first split.
	parts []previewPart
	// flushAll is true when the segment ends: the mask holds no tail.
	flushAll bool
	// lastFindings are the findings of the last masked view.
	lastFindings []security.Finding

	// stopFlush stops the armed flush timer. Nil means no timer.
	stopFlush func() bool
	// flushGen is the generation of the armed flush timer, for onFlushTimer.
	flushGen uint64
	runCtx   context.Context
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
	if hold := s.holdEnd(); now.Before(hold) {
		s.armTimer(hold.Sub(now))
		return
	}
	threshold := s.Cfg.BufferThreshold
	// The buffer threshold is a debounce rule and not a platform limit, so
	// it must not override a flood backoff (Hermes).
	if now.Sub(s.lastEdit) >= s.interval || (threshold > 0 && s.strikes == 0 && s.pending >= threshold) {
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
	s.flushAll = true
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
	s.flushAll = true
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
// the media tags and without the text in closed messages. With a mask, it
// masks the open segment and holds back a tail that can still grow into a
// match, until the segment ends.
func (s *Streamer) view() string {
	full := media.CleanForDisplay(s.acc)
	if s.Mask != nil {
		return s.maskedView(full)
	}
	if s.committed == "" {
		return full
	}
	if !strings.HasPrefix(full, s.committed) {
		s.logger().Info("telegram stream text changed before the sent part")
		return full
	}
	return strings.TrimLeft(full[len(s.committed):], "\n")
}

// maskedView returns the masked text of the open segment. It keeps the findings
// for commit.
func (s *Streamer) maskedView(full string) string {
	if !strings.HasPrefix(full, s.committed) {
		s.logger().Info("telegram stream text changed before the sent part")
		masked, f := s.Mask(full)
		s.lastFindings = f
		return masked
	}
	rest := strings.TrimLeft(full[len(s.committed):], "\n")
	cut := len(rest)
	if !s.flushAll {
		cut = holdCut(rest, s.Partial)
		if cut < len(rest) {
			// holdCut holds at most 64 bytes, so the cut can fall inside a finding that
			// is complete in rest. The mask of rest[:cut] does not match a part of a
			// value, so move the cut to the start of that finding.
			_, all := s.Mask(rest)
			for _, x := range all {
				if x.Start < cut && cut < x.End {
					cut = x.Start
				}
			}
		}
	}
	masked, f := s.Mask(rest[:cut])
	if !s.flushAll && len(f) > 0 && f[len(f)-1].End == cut {
		cut = f[len(f)-1].Start
		masked, f = s.Mask(rest[:cut])
	}
	s.lastFindings = f
	return masked
}

// commit marks the first n bytes of the view as sent in a closed message. With a
// mask, n counts bytes of the masked view, and commit changes it to raw bytes.
func (s *Streamer) commit(n int) {
	if s.Mask != nil {
		n = rawOffset(s.lastFindings, n)
	}
	full := media.CleanForDisplay(s.acc)
	rest := full[len(s.committed):]
	lead := len(rest) - len(strings.TrimLeft(rest, "\n"))
	s.committed = full[:len(s.committed)+lead+n]
}

func (s *Streamer) safeLimit() int {
	return max(500, maxMessageLen-UTF16Len(s.Cfg.Cursor)-100)
}

// previewPart is one numbered preview part that did not go out yet. end is
// the byte offset in the clean raw text where the part ends. It is fixed when
// the part is made, because a later view can have other findings.
type previewPart struct {
	text string
	end  int
}

// viewStart returns the byte offset in full, the clean raw text, where the view
// starts.
func (s *Streamer) viewStart(full string) int {
	// view shows all of full when the text changed. Without a mask, it also shows
	// the leading newlines of the first message.
	if !strings.HasPrefix(full, s.committed) || (s.Mask == nil && s.committed == "") {
		return 0
	}
	rest := full[len(s.committed):]
	return len(s.committed) + len(rest) - len(strings.TrimLeft(rest, "\n"))
}

// rawAt changes n, a byte offset in the view, into the offset in the raw text
// of the view. s.mu is held.
func (s *Streamer) rawAt(n int) int {
	if s.Mask != nil {
		return rawOffset(s.lastFindings, n)
	}
	return n
}

// splitView splits the view v into parts of at most limit UTF-16 units. With a
// mask, a cut inside a label moves to the start of that label. Thus no part
// shows a part of a label, and each cut maps to an exact raw offset. s.mu is
// held.
func (s *Streamer) splitView(v string, limit int) []string {
	if s.Mask == nil || len(s.lastFindings) == 0 {
		return SplitUTF16(v, limit)
	}
	spans := labelSpans(s.lastFindings)
	var parts []string
	for off := 0; off < len(v); {
		rest := v[off:]
		end := off + len(SplitUTF16(rest, limit)[0])
		for _, sp := range spans {
			if sp[0] < end && end < sp[1] {
				end = sp[0]
				if end <= off {
					// The part holds only the label. Keep the full label.
					end = sp[1]
				}
				break
			}
		}
		parts = append(parts, v[off:end])
		off = end
	}
	return parts
}

// streamFlush shows the current text with the cursor. s.mu is held.
func (s *Streamer) streamFlush(ctx context.Context) {
	s.stopTimer()
	s.pending = 0
	s.held = false
	// A held attempt sent no request, so it does not move lastEdit. A flood
	// error moves it.
	defer func() {
		if !s.held {
			s.lastEdit = s.now()
		}
	}()
	if len(s.parts) > 0 {
		s.sendParts(ctx)
		return
	}
	safe := s.safeLimit()
	for v := s.view(); UTF16Len(v) > safe; v = s.view() {
		if s.msgID == 0 {
			// No message to edit: send numbered parts now (Hermes).
			chunks := s.splitView(v, safe-numberRoom)
			base, pos := s.viewStart(media.CleanForDisplay(s.acc)), 0
			for i, text := range NumberParts(chunks, false) {
				pos += len(chunks[i])
				s.parts = append(s.parts, previewPart{text: text, end: base + s.rawAt(pos)})
			}
			s.sendParts(ctx)
			return
		}
		// Close the open message with the first part. The rest goes into
		// a new message.
		chunk := s.splitView(v, safe)[0]
		if !s.editStream(ctx, chunk) {
			return
		}
		s.commit(len(chunk))
		s.msgID, s.lastSent, s.msgCreated = 0, "", time.Time{}
	}
	s.sendOrEdit(ctx, s.view()+s.Cfg.Cursor)
}

// sendParts sends the numbered preview parts in order. Each part is a
// closed message, so commit marks it after its send. A pace hold or a
// flood error stops the loop, and the next flush sends the next part.
// s.mu is held.
func (s *Streamer) sendParts(ctx context.Context) {
	for len(s.parts) > 0 {
		p := s.parts[0]
		if _, err := s.sendNew(ctx, p.text, false); err != nil {
			if s.noteHold(err) || s.floodStrike(err) {
				return
			}
			s.logger().Info("telegram stream send failed", "err", err.Error())
			s.editsOff = true
			return
		}
		s.strikes = 0
		s.parts = s.parts[1:]
		// The end of the part is a raw offset already. A new view can have other
		// findings, so do not map it again.
		if full := media.CleanForDisplay(s.acc); p.end <= len(full) {
			s.committed = full[:p.end]
		}
		s.msgID, s.lastSent, s.msgCreated = 0, "", time.Time{}
	}
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
		s.noteFlood(err)
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
		if s.noteHold(err) || s.floodStrike(err) {
			return
		}
		s.logger().Info("telegram stream send failed, send the final text only", "err", err.Error())
		s.editsOff = true
		return
	}
	s.strikes = 0
}

// editStream edits the open message with plain text. A pace hold waits for
// the hold end. A flood error is a strike. Another error stops the stream
// edits. s.mu is held.
func (s *Streamer) editStream(ctx context.Context, text string) bool {
	_, err := interimClient(s.C).EditMessageText(ctx, EditMessageTextParams{ChatID: s.ChatID, MessageID: s.msgID, Text: text})
	if err == nil || isNotModified(err) {
		s.lastSent = text
		s.strikes = 0
		return true
	}
	if s.noteHold(err) || s.floodStrike(err) {
		return false
	}
	s.logger().Info("telegram stream edit failed, send the final text only", "err", err.Error())
	s.editsOff = true
	// A strip before the flood end cannot work. finalize cleans the preview later.
	if !s.now().Before(s.holdEnd()) {
		s.stripCursor(ctx)
	}
	return false
}

// stripCursor removes the cursor from the open message. It is best effort.
func (s *Streamer) stripCursor(ctx context.Context) {
	pc, ok := s.savePreview("")
	if !ok {
		return
	}
	err := stripTry(ctx, s.C, s.ChatID, pc, s.logger())
	s.noteFlood(err)
	if err == nil {
		s.lastSent = pc.plain
	}
}

// previewClean is a preview message that still needs a clean after a stream
// failure. plain is the preview text without the cursor. mdv2 is the final
// MarkdownV2 text, or empty.
type previewClean struct {
	msgID int
	plain string
	mdv2  string
}

// stripTry makes one strip edit of the preview. It sends the MarkdownV2 text
// first when there is one, and the plain text when Telegram rejects it. It
// reads no Streamer field, so a timer goroutine can call it. It returns nil
// on success or on "not modified", else the error of the last edit.
func stripTry(ctx context.Context, c *Client, chatID int64, pc previewClean, log *slog.Logger) error {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	// One client for both edits, so a try waits inline at most once.
	nc := noWaitClient(c)
	p := EditMessageTextParams{ChatID: chatID, MessageID: pc.msgID, Text: pc.plain}
	if pc.mdv2 != "" {
		p.Text, p.ParseMode = pc.mdv2, "MarkdownV2"
		_, err := nc.EditMessageText(ctx, p)
		// "not modified" is also a 400, so check it first.
		if err == nil || isNotModified(err) {
			return nil
		}
		if !isBadRequest(err) {
			return err
		}
		logPlainFallback(log, "strip_cursor", chatID, "", 0, 0, err)
		p.Text, p.ParseMode = pc.plain, ""
	}
	_, err := nc.EditMessageText(ctx, p)
	if isNotModified(err) {
		return nil
	}
	return err
}

// startStripRetry starts a chain of timers that remove the cursor from the
// preview after a flood failure. The first try runs after wait. The chain
// gets copies of the values that it needs and reads no Streamer field, so it
// does not need s.mu. A turn cancel does not stop it. s.mu is held.
func (s *Streamer) startStripRetry(pc previewClean, wait time.Duration) {
	c, chatID, log, now := s.C, s.ChatID, s.logger(), s.Now
	runCtx := s.runCtx
	if runCtx == nil {
		runCtx = context.Background()
	}
	afterFunc := s.AfterFunc
	if afterFunc == nil {
		afterFunc = func(d time.Duration, f func()) { time.AfterFunc(d, f) }
	}
	clock := func() time.Time {
		if now == nil {
			return time.Now()
		}
		return now()
	}
	deadline := clock().Add(maxStripWait)
	var schedule func(try int, wait time.Duration, last error)
	schedule = func(try int, wait time.Duration, last error) {
		if clock().Add(wait).After(deadline) {
			stripStopped(log, "max wait", last)
			return
		}
		log.Info("telegram cursor strip failed, retry later", "retry_after_s", wait.Seconds(), "try", try)
		afterFunc(wait, func() {
			ctx, cancel := context.WithTimeout(runCtx, flushTimeout)
			defer cancel()
			err := stripTry(ctx, c, chatID, pc, log)
			if err == nil {
				return
			}
			d, ok := stripWait(err)
			switch {
			case !ok:
				stripStopped(log, "error", err)
			case try >= maxStripTries:
				stripStopped(log, "max tries", err)
			default:
				schedule(try+1, d+stripRetryPad, err)
			}
		})
	}
	schedule(1, wait, nil)
}

// stripStopped records that no more tries remove the cursor. err is the last
// error, or nil.
func stripStopped(log *slog.Logger, reason string, err error) {
	if err == nil {
		log.Info("telegram cursor strip stopped", "reason", reason)
		return
	}
	log.Info("telegram cursor strip stopped", "reason", reason, "err", err.Error())
}

// noteFlood records the end of the flood wait when err is a flood error. It
// ignores a *paceHold, because floodWait does not match it. s.mu is held.
func (s *Streamer) noteFlood(err error) {
	if d, ok := floodWait(err); ok {
		s.floodUntil = s.now().Add(d)
		// Telegram rounds retry_after to whole seconds, so the hold gets the
		// same pad as the pace.
		if end := s.floodUntil.Add(stripRetryPad); end.After(s.holdUntil) {
			s.holdUntil = end
		}
	}
}

// holdEnd returns holdUntil after it adds the flood end of the chat from
// the shared pace. s.mu is held.
func (s *Streamer) holdEnd() time.Time {
	if end := s.C.Pace.FloodEnd(s.ChatID); end.After(s.holdUntil) {
		s.holdUntil = end
	}
	return s.holdUntil
}

// noteHold records a pace hold of a stream request. A hold is not a
// strike, because no request went out. The flush timer tries again at the
// hold end. s.mu is held.
func (s *Streamer) noteHold(err error) bool {
	d, ok := isPaceHold(err)
	if !ok {
		return false
	}
	now := s.now()
	if end := now.Add(d); end.After(s.holdUntil) {
		s.holdUntil = end
	}
	s.held = true
	s.logger().Debug("telegram stream held by the chat pace", "wait_s", d.Seconds())
	s.armTimer(s.holdUntil.Sub(now))
	return true
}

// floodStrike records a flood error of a stream request as a strike. The
// interval doubles up to maxStreamInterval, and no request goes out before
// the flood end. The third strike in a row stops the stream edits. s.mu is
// held.
func (s *Streamer) floodStrike(err error) bool {
	d, ok := floodWait(err)
	if !ok {
		return false
	}
	s.noteFlood(err)
	s.strikes++
	s.interval = min(2*s.interval, maxStreamInterval)
	s.logger().Info("telegram stream flood control, back off",
		"strike", s.strikes, "interval_s", s.interval.Seconds(), "retry_after_s", d.Seconds())
	if s.strikes < maxFloodStrikes {
		s.armTimer(s.holdUntil.Sub(s.now()))
		return true
	}
	s.logger().Info("telegram stream edit failed, send the final text only", "err", err.Error(), "flood", true)
	s.editsOff, s.floodOff = true, true
	s.stopTimer()
	return true
}

// savePreview copies the open preview before a send, because a send moves
// s.msgID and s.lastSent to the new message. ok is true when the preview
// shows the cursor or when mdv2 is set. s.mu is held.
func (s *Streamer) savePreview(mdv2 string) (previewClean, bool) {
	cursor := s.Cfg.Cursor
	hasCursor := cursor != "" && strings.HasSuffix(s.lastSent, cursor)
	pc := previewClean{msgID: s.msgID, plain: s.lastSent, mdv2: mdv2}
	if hasCursor {
		pc.plain = strings.TrimSuffix(s.lastSent, cursor)
	}
	ok := s.msgID != 0 && strings.TrimSpace(pc.plain) != "" && (hasCursor || mdv2 != "")
	return pc, ok
}

// cleanPreview removes the cursor from a saved preview at finish. Under flood
// control, or on a flood error, a timer chain tries again later, so the
// method never waits. s.mu is held.
func (s *Streamer) cleanPreview(ctx context.Context, pc previewClean) {
	if now := s.now(); now.Before(s.floodUntil) {
		s.startStripRetry(pc, s.floodUntil.Sub(now)+stripRetryPad)
		return
	}
	// A flood of another call of the chat also starts the timer chain, so
	// the strip does not wait for the flood end with s.mu held.
	if now, end := s.now(), s.C.Pace.FloodEnd(s.ChatID); now.Before(end) {
		s.startStripRetry(pc, end.Sub(now))
		return
	}
	err := stripTry(ctx, s.C, s.ChatID, pc, s.logger())
	s.noteFlood(err)
	if err == nil {
		if s.msgID == pc.msgID {
			s.lastSent = pc.plain
		}
		return
	}
	if d, ok := stripWait(err); ok {
		s.startStripRetry(pc, d+stripRetryPad)
		return
	}
	stripStopped(s.logger(), "error", err)
}

// stripWait reports whether a strip try can try again later, and the wait.
// A pace wait that is longer than the context of one try gives a *paceHold
// and not a flood error.
func stripWait(err error) (time.Duration, bool) {
	if d, ok := floodWait(err); ok {
		return d, true
	}
	return isPaceHold(err)
}

// deleteStale deletes a preview that the full final text replaced. It uses
// noWaitClient, because the chat is under flood control and s.C can wait for
// a long time. s.mu is held.
func (s *Streamer) deleteStale(ctx context.Context, msgID int) error {
	err := noWaitClient(s.C).DeleteMessage(ctx, s.ChatID, msgID)
	s.noteFlood(err)
	if err != nil {
		s.logger().Info("telegram preview delete failed", "err", err.Error())
	}
	return err
}

// visiblePrefix returns the text that the user sees in the open message.
func (s *Streamer) visiblePrefix() string {
	if s.msgID == 0 {
		return ""
	}
	return media.CleanForDisplay(strings.TrimSuffix(s.lastSent, s.Cfg.Cursor))
}

// continuation returns the part of text that the open message does not show.
func (s *Streamer) continuation(text string) string {
	if p := s.visiblePrefix(); p != "" && strings.HasPrefix(text, p) {
		return strings.TrimLeft(text[len(p):], " \n")
	}
	return text
}

// inFlood reports whether the chat is in a flood wait now: the flood end
// of the stream or of the shared pace is later than now. With a nil pace,
// only the flood end of the stream counts. s.mu is held.
func (s *Streamer) inFlood() bool {
	now := s.now()
	return now.Before(s.floodUntil) || now.Before(s.C.Pace.FloodEnd(s.ChatID))
}

// finalize closes the segment with its final MarkdownV2 text. s.mu is held.
func (s *Streamer) finalize(ctx context.Context) error {
	text := s.view()
	if strings.TrimSpace(text) == "" {
		if pc, ok := s.savePreview(""); ok {
			s.cleanPreview(ctx, pc)
		}
		return nil
	}
	// After a flood stop, or in a flood wait of the chat, the final text must
	// wait for the flood end in all cases. An edit of the preview then costs
	// no extra time and gives one formatted answer.
	afterFlood := s.floodOff || s.inFlood()
	if s.editsOff && !s.floodOff {
		return s.sendRest(ctx, text, s.continuation(text))
	}
	if s.msgID == 0 {
		return s.sendFinal(ctx, text)
	}
	if s.freshFinalDue() {
		old, oldSent, oldCreated := s.msgID, s.lastSent, s.msgCreated
		if err := s.sendFinal(ctx, text); err == nil {
			if err := s.C.DeleteMessage(ctx, s.ChatID, old); err != nil {
				s.logger().Info("telegram preview delete failed", "err", err.Error())
			}
			return nil
		}
		// A send of more than one part can fail after part 1, and opened has
		// then moved all three fields to part 1. Restore the preview.
		s.msgID, s.lastSent, s.msgCreated = old, oldSent, oldCreated
	}
	return s.editFinal(ctx, text, afterFlood)
}

// freshFinalDue reports whether the open message is old enough that the
// final text goes into a new message, so its time shows the end of the run.
func (s *Streamer) freshFinalDue() bool {
	after := time.Duration(s.Cfg.FreshFinalAfterS * float64(time.Second))
	return after > 0 && s.msgID != 0 && !s.msgCreated.IsZero() && s.now().Sub(s.msgCreated) >= after
}

// finalParts converts text to MarkdownV2 and splits it at the message limit,
// with escaped part numbers. The split keeps each entity whole.
func finalParts(text string) []string {
	formatted := ToMarkdownV2(text)
	if UTF16Len(formatted) <= maxMessageLen {
		return []string{formatted}
	}
	return NumberParts(SplitMarkdownV2(formatted, maxMessageLen-numberRoom), true)
}

// editFinal edits the open message with the first final part and sends the
// other parts. When the edit fails, it sends the text that the user did not
// see. When afterFlood is true, the edit waits for the flood end of the chat
// and makes one try. s.mu is held.
func (s *Streamer) editFinal(ctx context.Context, text string, afterFlood bool) error {
	parts := finalParts(text)
	plain := text
	if len(parts) > 1 {
		plain = StripMarkdownV2(parts[0])
	}
	c := noWaitClient(s.C)
	if afterFlood {
		// Wait for the flood end, then make one try. A second 429 goes to
		// sendRest at once.
		c = finalEditClient(s.C)
	}
	p := EditMessageTextParams{ChatID: s.ChatID, MessageID: s.msgID, Text: parts[0], ParseMode: "MarkdownV2"}
	_, err := c.EditMessageText(ctx, p)
	s.noteFlood(err)
	if err != nil && !isNotModified(err) && isBadRequest(err) {
		logPlainFallback(s.logger(), "edit_final", s.ChatID, s.ThreadID, 1, len(parts), err)
		p.Text, p.ParseMode = plain, ""
		fc := noWaitClient(s.C)
		if afterFlood {
			fc = finalEditClient(s.C)
		}
		_, err = fc.EditMessageText(ctx, p)
		s.noteFlood(err)
	}
	if err != nil && !isNotModified(err) {
		s.logger().Info("telegram final edit failed, send the rest", "err", err.Error())
		return s.sendRest(ctx, text, s.continuation(text))
	}
	s.lastSent = parts[0]
	for _, part := range parts[1:] {
		if err := s.sendPart(ctx, part); err != nil {
			return err
		}
	}
	return nil
}

// sendRest sends cont, the part of the final text that the user did not see,
// after an edit failure (Hermes _send_fallback_final). It sends no empty
// message. Then it cleans the preview. s.mu is held.
func (s *Streamer) sendRest(ctx context.Context, text, cont string) error {
	// Save the preview first, because sendFinal moves s.msgID to the new
	// message.
	old := s.msgID
	pc, dirty := s.savePreview(s.finalMarkdown(text))
	if strings.TrimSpace(cont) == "" {
		if dirty {
			s.cleanPreview(ctx, pc)
		}
		return nil
	}
	err := s.sendFinal(ctx, cont)
	// Delete the preview only when the full final went out again. Else the
	// preview is the head of the answer, and it stays (Hermes).
	if err == nil && old != 0 && cont == text {
		if s.deleteStale(ctx, old) == nil {
			return nil
		}
	}
	if dirty {
		s.cleanPreview(ctx, pc)
	}
	return err
}

// finalMarkdown returns the final MarkdownV2 text for a strip edit of the
// open message. It is set only when the message shows all of text and the
// text fits in one part. Else it is empty. s.mu is held.
func (s *Streamer) finalMarkdown(text string) string {
	if strings.TrimSpace(s.continuation(text)) != "" {
		return ""
	}
	if parts := finalParts(text); len(parts) == 1 {
		return parts[0]
	}
	return ""
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
		// sendPart does not know the part number, so the line has no part field.
		logPlainFallback(s.logger(), "send_part", s.ChatID, s.ThreadID, 0, 0, err)
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
// is plain text. It holds for the chat pace, and it fails at once on a long
// flood wait. A final send is
// MarkdownV2, and it waits for the rate limit (Client.Call), because the user
// must get the final text.
func (s *Streamer) sendNew(ctx context.Context, text string, final bool) (Message, error) {
	p := SendMessageParams{ChatID: s.ChatID, Text: text}
	c := s.C
	if final {
		p.ParseMode = "MarkdownV2"
	} else {
		c = interimClient(s.C)
	}
	if id, ok := ThreadParam(s.ThreadID, false); ok {
		p.MessageThreadID = id
	}
	if !s.drained {
		s.drained = true
		if s.BeforeNewMessage != nil {
			s.BeforeNewMessage(ctx)
		}
	}
	m, err := c.SendMessage(ctx, p)
	if err != nil {
		if !final {
			s.noteFlood(err)
		}
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
	s.drained = false
	s.parts = nil
	s.flushAll, s.lastFindings = false, nil
	if s.draft {
		s.draftID = draftSeq.Add(1)
	}
}

// armTimer starts the timer for a later flush. s.mu is held.
func (s *Streamer) armTimer(d time.Duration) {
	if s.stopFlush != nil {
		return
	}
	timer := s.FlushTimer
	if timer == nil {
		timer = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	s.flushGen++
	gen := s.flushGen
	s.stopFlush = timer(max(d, 0), func() { s.onFlushTimer(gen) })
}

// onFlushTimer flushes the stream when the open message does not show the
// newest text. It does not read pending, because streamFlush sets pending
// to 0 before the request. gen is the generation of the timer that fired. A
// timer can fire while stopTimer stops it, and then its callback waits for
// s.mu. When gen is not the current generation, that timer was stopped, so
// the callback does nothing. It does not touch stopFlush, which can be the
// handle of a newer timer.
func (s *Streamer) onFlushTimer(gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.flushGen {
		return
	}
	s.stopFlush = nil
	if s.finished || s.editsOff {
		return
	}
	now := s.now()
	if hold := s.holdEnd(); now.Before(hold) {
		s.armTimer(hold.Sub(now))
		return
	}
	if s.view()+s.Cfg.Cursor == s.lastSent {
		return
	}
	ctx, cancel := context.WithTimeout(s.runCtx, flushTimeout)
	defer cancel()
	s.streamFlush(ctx)
}

func (s *Streamer) stopTimer() {
	if s.stopFlush != nil {
		s.stopFlush()
		s.stopFlush = nil
		// A callback of the stopped timer that waits for s.mu must do nothing.
		s.flushGen++
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

// logPlainFallback records that Telegram rejected MarkdownV2 text and that the
// text went out as plain text. part is 1-based. The line has no part field when
// parts is less than 2. Do not give the message text to this function.
func logPlainFallback(log *slog.Logger, op string, chatID int64, threadID string, part, parts int, err error) {
	attrs := []any{"op", op, "chat", chatID}
	if threadID != "" {
		attrs = append(attrs, "thread", threadID)
	}
	if parts >= 2 {
		attrs = append(attrs, "part", fmt.Sprintf("%d/%d", part, parts))
	}
	attrs = append(attrs, "err", err.Error())
	log.Warn("telegram: Telegram rejected the MarkdownV2 text, sent it as plain text", attrs...)
}
