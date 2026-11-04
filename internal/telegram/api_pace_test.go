package telegram

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// pacedClient returns a client for the fake with a pace on the fake clock.
// A wait moves the clock by the wait and takes no real time.
func (b *fakeBot) pacedClient(clk *fakeClock) *Client {
	c := b.client()
	c.Pace = NewChatPace()
	c.Pace.Now = clk.Now
	c.sleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		clk.Advance(d)
		return nil
	}
	return c
}

// timer adds a timer like AfterFunc and returns a stop function. stop
// removes the timer and reports whether it was still pending.
func (ft *fakeTimers) timer(d time.Duration, f func()) (stop func() bool) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.seq++
	id := ft.seq
	ft.pending = append(ft.pending, fakeTimer{d: d, f: f, id: id})
	ft.waits = append(ft.waits, d)
	return func() bool {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		for i, p := range ft.pending {
			if p.id == id {
				ft.pending = slices.Delete(ft.pending, i, i+1)
				return true
			}
		}
		return false
	}
}

// flood429Once makes the first call of method fail with a 429.
func flood429Once(api *fakeBot, method string, retryAfter int) {
	done := false
	api.setReply(func(m string, _ map[string]any) *APIError {
		if m != method || done {
			return nil
		}
		done = true
		return &APIError{Code: 429, Description: "Too Many Requests: retry after", RetryAfter: retryAfter}
	})
}

func TestPaceSendWaitsForSlot(t *testing.T) {
	api := newFakeBot(t)
	clk := newFakeClock()
	c := api.pacedClient(clk)
	start := clk.Now()
	for range 2 {
		if _, err := c.SendMessage(ctx, SendMessageParams{ChatID: -100, Text: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if got := clk.Now().Sub(start); got != time.Second {
		t.Fatal("second send waited", got)
	}
	if n := api.count("sendMessage"); n != 2 {
		t.Fatal("sends", n)
	}
}

func TestPaceDefaultClasses(t *testing.T) {
	api := newFakeBot(t)
	clk := newFakeClock()
	c := api.pacedClient(clk)
	var buf bytes.Buffer
	c.Log = slog.New(slog.NewTextHandler(&buf, nil))
	start := clk.Now()
	for range 18 {
		if _, err := c.SendMessage(ctx, SendMessageParams{ChatID: -100, Text: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if got := clk.Now().Sub(start); got != 17*time.Second {
		t.Fatal("18 sends", got)
	}
	// An edit is the reply class: it waits for the slot, not for the window.
	if _, err := c.EditMessageText(ctx, EditMessageTextParams{ChatID: -100, MessageID: 101, Text: "e"}); err != nil {
		t.Fatal(err)
	}
	if got := clk.Now().Sub(start); got != 18*time.Second {
		t.Fatal("edit", got)
	}
	// A send is the wait class. The 19 calls are at 0..18 s, so the send
	// waits until the call at 1 s leaves the window.
	if _, err := c.SendMessage(ctx, SendMessageParams{ChatID: -100, Text: "y"}); err != nil {
		t.Fatal(err)
	}
	if got := clk.Now().Sub(start); got != 61*time.Second {
		t.Fatal("send after the window", got)
	}
	log := buf.String()
	for _, want := range []string{"telegram pace wait", "method=sendMessage", "chat=-100", "wait_s=43"} {
		if !strings.Contains(log, want) {
			t.Fatalf("log has no %q: %s", want, log)
		}
	}
}

func TestPaceHoldingClasses(t *testing.T) {
	api := newFakeBot(t)
	clk := newFakeClock()
	c := api.pacedClient(clk)
	start := clk.Now()
	ic := interimClient(c)
	if ic.Pace != c.Pace {
		t.Fatal("the copy must share the pace")
	}
	if _, err := ic.EditMessageText(ctx, EditMessageTextParams{ChatID: -100, MessageID: 101, Text: "a"}); err != nil {
		t.Fatal(err)
	}
	_, err := ic.EditMessageText(ctx, EditMessageTextParams{ChatID: -100, MessageID: 101, Text: "b"})
	if d, ok := isPaceHold(err); !ok || d != time.Second {
		t.Fatal("interim edit", err)
	}
	if _, ok := floodWait(err); ok {
		t.Fatal("a hold is not a flood")
	}
	_, err = queueClient(c).SendMessage(ctx, SendMessageParams{ChatID: -100, Text: "q"})
	if d, ok := isPaceHold(err); !ok || d != time.Second {
		t.Fatal("queue send", err)
	}
	if api.count("editMessageText") != 1 || api.count("sendMessage") != 0 {
		t.Fatal(api.methods())
	}
	if !clk.Now().Equal(start) {
		t.Fatal("a holding class must not wait")
	}
}

func TestPaceDeadlineBeforeSlotFailsAtOnce(t *testing.T) {
	api := newFakeBot(t)
	clk := newFakeClock()
	c := api.pacedClient(clk)
	start := clk.Now()
	if _, err := c.SendMessage(ctx, SendMessageParams{ChatID: -100, Text: "a"}); err != nil {
		t.Fatal(err)
	}
	dctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	_, err := c.SendMessage(dctx, SendMessageParams{ChatID: -100, Text: "b"})
	if d, ok := isPaceHold(err); !ok || d != time.Second {
		t.Fatal("deadline before the slot", err)
	}
	if n := api.count("sendMessage"); n != 1 {
		t.Fatal("sends", n)
	}
	if !clk.Now().Equal(start) {
		t.Fatal("the call slept")
	}
	if d := c.Pace.Next(-100, paceWait); d != time.Second {
		t.Fatal("the slot did not go back", d)
	}
}

func TestPaceStopDuringWait(t *testing.T) {
	api := newFakeBot(t)
	clk := newFakeClock()
	c := api.pacedClient(clk)
	if _, err := c.SendMessage(ctx, SendMessageParams{ChatID: -100, Text: "a"}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	c.sleep = func(wctx context.Context, d time.Duration) error {
		close(started)
		<-wctx.Done()
		return wctx.Err()
	}
	sctx, cancel := context.WithCancel(ctx)
	errc := make(chan error, 1)
	go func() {
		_, err := c.SendMessage(sctx, SendMessageParams{ChatID: -100, Text: "b"})
		errc <- err
	}()
	<-started
	cancel() // /stop cancels the turn context.
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if n := api.count("sendMessage"); n != 1 {
		t.Fatal("sends", n)
	}
	if d := c.Pace.Next(-100, paceWait); d != time.Second {
		t.Fatal("the slot did not go back", d)
	}
}

func TestPaceDraftNotCounted(t *testing.T) {
	api := newFakeBot(t)
	clk := newFakeClock()
	c := api.pacedClient(clk)
	start := clk.Now()
	c.Pace.NoteFlood(42, 30*time.Second)
	for range 5 {
		if err := c.SendMessageDraft(ctx, SendMessageDraftParams{ChatID: 42, DraftID: 1, Text: "d"}); err != nil {
			t.Fatal(err)
		}
		if err := c.SendMessageDraft(ctx, SendMessageDraftParams{ChatID: 43, DraftID: 1, Text: "d"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := api.count("sendMessageDraft"); n != 10 {
		t.Fatal("drafts", n)
	}
	if !clk.Now().Equal(start) {
		t.Fatal("a draft waited")
	}
	if d := c.Pace.Next(43, paceWait); d != 0 {
		t.Fatal("a draft was counted", d)
	}
}

func TestPaceNotUsedWithoutPaceOrChat(t *testing.T) {
	api := newFakeBot(t)
	// The clients of tether doctor and the setup wizard have a nil Pace.
	plain := api.client()
	for range 3 {
		if _, err := plain.SendMessage(ctx, SendMessageParams{ChatID: -100, Text: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := api.count("sendMessage"); n != 3 {
		t.Fatal("sends with a nil pace", n)
	}
	clk := newFakeClock()
	c := api.pacedClient(clk)
	start := clk.Now()
	for range 3 {
		if err := c.Call(ctx, "getMe", struct{}{}, nil); err != nil {
			t.Fatal(err)
		}
		if err := c.SetMyCommands(ctx, nil); err != nil {
			t.Fatal(err)
		}
		if err := c.AnswerCallbackQuery(ctx, "cb", "", false); err != nil {
			t.Fatal(err)
		}
		// The fake replies true, so the decode fails. Only the request matters.
		_, _ = c.GetUpdates(ctx, 0, 0)
	}
	if n := api.count("getUpdates"); n != 3 {
		t.Fatal("getUpdates", n)
	}
	if !clk.Now().Equal(start) {
		t.Fatal("a call with no chat waited")
	}
	flood429Once(api, "getMe", 30)
	if err := c.Call(ctx, "getMe", struct{}{}, nil); err != nil {
		t.Fatal(err)
	}
	c.Pace.mu.Lock()
	n := len(c.Pace.chats)
	c.Pace.mu.Unlock()
	if n != 0 {
		t.Fatal("a call with no chat made a pace state", n)
	}
}

func TestPaceFloodRecordedOnSend(t *testing.T) {
	api := newFakeBot(t)
	clk := newFakeClock()
	c := api.pacedClient(clk)
	start := clk.Now()
	flood429Once(api, "sendMessage", 30)
	if _, err := c.SendMessage(ctx, SendMessageParams{ChatID: -100, Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if got := c.Pace.FloodEnd(-100); !got.Equal(start.Add(31 * time.Second)) {
		t.Fatal("flood end", got.Sub(start))
	}
	// The retry waits for the flood end: retry_after plus the pad.
	if got := clk.Now().Sub(start); got != 31*time.Second {
		t.Fatal("retry wait", got)
	}
	if n := api.count("sendMessage"); n != 2 {
		t.Fatal("sends", n)
	}
	// The retry made a new record at the flood end.
	if d := c.Pace.Next(-100, paceWait); d != time.Second {
		t.Fatal("next after the retry", d)
	}
}

func TestPaceFloodRecordedOnUpload(t *testing.T) {
	api := newFakeBot(t)
	clk := newFakeClock()
	c := api.pacedClient(clk)
	start := clk.Now()
	p := filepath.Join(t.TempDir(), "pic.png")
	if err := os.WriteFile(p, []byte("PNGDATA"), 0o600); err != nil {
		t.Fatal(err)
	}
	flood429Once(api, "sendPhoto", 7)
	if err := c.Upload(ctx, "sendPhoto", map[string]string{"chat_id": "-100"}, "photo", p, nil); err != nil {
		t.Fatal(err)
	}
	if got := c.Pace.FloodEnd(-100); !got.Equal(start.Add(8 * time.Second)) {
		t.Fatal("flood end", got.Sub(start))
	}
	if n := api.count("sendPhoto"); n != 2 {
		t.Fatal("uploads", n)
	}
	// An upload is a counted call.
	if d := c.Pace.Next(-100, paceWait); d != time.Second {
		t.Fatal("next after the upload", d)
	}
}

func TestPaceFloodRecordedOnDelete(t *testing.T) {
	api := newFakeBot(t)
	clk := newFakeClock()
	c := api.pacedClient(clk)
	start := clk.Now()
	flood429Once(api, "deleteMessage", 10)
	if err := c.DeleteMessage(ctx, -200, 5); err != nil {
		t.Fatal(err)
	}
	if got := c.Pace.FloodEnd(-200); !got.Equal(start.Add(11 * time.Second)) {
		t.Fatal("flood end", got.Sub(start))
	}
	if n := api.count("deleteMessage"); n != 2 {
		t.Fatal("deletes", n)
	}
	// A delete is not counted, so no slot is taken.
	if d := c.Pace.Next(-200, paceWait); d != 0 {
		t.Fatal("a delete was counted", d)
	}
}

func TestFinalEditClientMakesOneTry(t *testing.T) {
	api := newFakeBot(t)
	clk := newFakeClock()
	c := api.pacedClient(clk)
	start := clk.Now()
	api.setReply(func(m string, _ map[string]any) *APIError {
		if m == "editMessageText" {
			return &APIError{Code: 429, Description: "Too Many Requests: retry after 30", RetryAfter: 30}
		}
		return nil
	})
	fc := finalEditClient(c)
	_, err := fc.EditMessageText(ctx, EditMessageTextParams{ChatID: -100, MessageID: 101, Text: "f"})
	if d, ok := floodWait(err); !ok || d != 30*time.Second {
		t.Fatal(err)
	}
	if n := api.count("editMessageText"); n != 1 {
		t.Fatal("a final edit must not retry", n)
	}
	if got := c.Pace.FloodEnd(-100); !got.Equal(start.Add(31 * time.Second)) {
		t.Fatal("flood end", got.Sub(start))
	}
	if !clk.Now().Equal(start) {
		t.Fatal("the failed try waited")
	}
	// The next final edit waits for the flood end, then makes its try.
	api.setReply(nil)
	if _, err := fc.EditMessageText(ctx, EditMessageTextParams{ChatID: -100, MessageID: 101, Text: "f"}); err != nil {
		t.Fatal(err)
	}
	if got := clk.Now().Sub(start); got != 31*time.Second {
		t.Fatal("wait for the flood end", got)
	}
	if n := api.count("editMessageText"); n != 2 {
		t.Fatal("edits", n)
	}
}

func TestFakeTimersStop(t *testing.T) {
	ft := &fakeTimers{clk: newFakeClock()}
	start := ft.clk.Now()
	ran := false
	stop := ft.timer(5*time.Second, func() { ran = true })
	ft.timer(time.Second, func() {})
	if n := ft.pendingCount(); n != 2 {
		t.Fatal("pending", n)
	}
	if !stop() {
		t.Fatal("stop of a pending timer")
	}
	if stop() {
		t.Fatal("second stop")
	}
	ft.run()
	if ran {
		t.Fatal("a stopped timer ran")
	}
	if got := ft.clk.Now().Sub(start); got != time.Second {
		t.Fatal("clock", got)
	}
	if got := ft.waitList(); !slices.Equal(got, []time.Duration{5 * time.Second, time.Second}) {
		t.Fatal("waits", got)
	}
}

// TestReplyDoesNotWaitBehindWaitReservation checks that a reply in a full
// group window waits only for the slot, also while a wait-class call
// sleeps for its place in the window.
func TestReplyDoesNotWaitBehindWaitReservation(t *testing.T) {
	api := newFakeBot(t)
	clk := newFakeClock()
	c := api.pacedClient(clk)
	for range paceGroupLimit {
		if w := c.Pace.TryTake(-100, paceWait); w != 0 {
			t.Fatal(w)
		}
		clk.Advance(time.Second)
	}
	r := replyClient(c)
	var replyWait time.Duration
	r.sleep = func(_ context.Context, d time.Duration) error {
		replyWait += d
		clk.Advance(d)
		return nil
	}
	inner, asked := c.sleep, false
	c.sleep = func(wctx context.Context, d time.Duration) error {
		if !asked {
			asked = true
			if _, err := r.SendMessage(wctx, SendMessageParams{ChatID: -100, Text: "ack"}); err != nil {
				t.Fatal(err)
			}
		}
		return inner(wctx, d)
	}
	if _, err := c.SendMessage(ctx, SendMessageParams{ChatID: -100, Text: "cron"}); err != nil {
		t.Fatal(err)
	}
	if !asked || replyWait > time.Second {
		t.Fatalf("asked %v, reply wait %s: a reply waits only for the slot", asked, replyWait)
	}
	if api.count("sendMessage") != 2 {
		t.Fatal(api.methods())
	}
}

// TestNoWaitSlotWaitKeepsInlineRetry checks that the slot wait of a
// noWaitClient call does not use up its one inline wait for a 429.
func TestNoWaitSlotWaitKeepsInlineRetry(t *testing.T) {
	api := newFakeBot(t)
	clk := newFakeClock()
	c := api.pacedClient(clk)
	if w := c.Pace.TryTake(42, paceWait); w != 0 {
		t.Fatal(w)
	}
	flood429Once(api, "editMessageText", 2)
	_, err := noWaitClient(c).EditMessageText(ctx, EditMessageTextParams{ChatID: 42, MessageID: 1, Text: "x"})
	if err != nil || api.count("editMessageText") != 2 {
		t.Fatal(err, api.methods())
	}
}

// TestNoWaitInlineGateUsesRetryAfter checks that a paced noWaitClient call
// waits inline for a retry_after of maxInlineFloodWait. The gate does not
// count the pad of the pace, but the wait includes it.
func TestNoWaitInlineGateUsesRetryAfter(t *testing.T) {
	api := newFakeBot(t)
	clk := newFakeClock()
	c := api.pacedClient(clk)
	start := clk.Now()
	flood429Once(api, "editMessageText", int(maxInlineFloodWait/time.Second))
	_, err := noWaitClient(c).EditMessageText(ctx, EditMessageTextParams{ChatID: 42, MessageID: 1, Text: "x"})
	if err != nil || api.count("editMessageText") != 2 {
		t.Fatal(err, api.methods())
	}
	if got := clk.Now().Sub(start); got != maxInlineFloodWait+floodPad {
		t.Fatal("the inline wait must include the pad:", got)
	}
}

// TestNoWaitInlineGateUsesFloodEnd checks that a paced noWaitClient call
// does not wait inline when an earlier 429 moved the flood end of the chat
// far out, also when the retry_after of this reply is short.
func TestNoWaitInlineGateUsesFloodEnd(t *testing.T) {
	api := newFakeBot(t)
	clk := newFakeClock()
	c := api.pacedClient(clk)
	start := clk.Now()
	nc := noWaitClient(c)
	nc.sleep = func(context.Context, time.Duration) error {
		t.Fatal("the call must not wait inline")
		return nil
	}
	api.setReply(func(m string, _ map[string]any) *APIError {
		if m == "editMessageText" {
			// During the request, an other call of the chat gets a 429 with a long wait.
			c.Pace.NoteFlood(42, 30*time.Second)
			return &APIError{Code: 429, Description: "Too Many Requests: retry after 2", RetryAfter: 2}
		}
		return nil
	})
	_, err := nc.EditMessageText(ctx, EditMessageTextParams{ChatID: 42, MessageID: 1, Text: "x"})
	if _, ok := errors.AsType[*floodError](err); !ok {
		t.Fatal("want a *floodError, got", err)
	}
	if api.count("editMessageText") != 1 || !clk.Now().Equal(start) {
		t.Fatal(api.methods(), clk.Now().Sub(start))
	}
}

// TestNoWaitPaceWaitIsNotAFlood checks that a long pace wait of a
// noWaitClient call is a wait and not a *floodError, because no 429 came.
func TestNoWaitPaceWaitIsNotAFlood(t *testing.T) {
	api := newFakeBot(t)
	clk := newFakeClock()
	c := api.pacedClient(clk)
	for range paceGroupLimit {
		if w := c.Pace.TryTake(-100, paceWait); w != 0 {
			t.Fatal(w)
		}
		clk.Advance(time.Second)
	}
	_, err := noWaitClient(c).SendMessage(ctx, SendMessageParams{ChatID: -100, Text: "x"})
	if _, flood := floodWait(err); err != nil || flood || api.count("sendMessage") != 1 {
		t.Fatal(err, flood, api.methods())
	}
}
