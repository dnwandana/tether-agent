package telegram

import (
	"bytes"
	"log/slog"
	"slices"
	"testing"
	"time"
)

// pacedStatus returns a status with a paced client, fake timers, and a log at Info
// level. The pace, the status, and the timers use one fake clock.
func pacedStatus(t *testing.T, chatID int64) (*Status, *fakeBot, *fakeClock, *fakeTimers, *bytes.Buffer) {
	t.Helper()
	api := newFakeBot(t)
	clk := newFakeClock()
	ft := &fakeTimers{clk: clk}
	logs := &bytes.Buffer{}
	s := &Status{C: api.pacedClient(clk), ChatID: chatID, Mode: "all", Interval: 1500 * time.Millisecond,
		Now: clk.Now, Log: slog.New(slog.NewTextHandler(logs, nil)), FlushTimer: ft.timer}
	t.Cleanup(func() { s.Close(ctx, false) })
	return s, api, clk, ft, logs
}

// takeSlot lets another sender of the chat take the slot now.
func takeSlot(t *testing.T, s *Status) {
	t.Helper()
	if w := s.C.Pace.TryTake(s.ChatID, paceWait); w != 0 {
		t.Fatalf("the slot is not free: wait %s", w)
	}
}

func TestStatusEditHoldWaitsTheExactWait(t *testing.T) {
	s, api, clk, ft, logs := pacedStatus(t, 42)
	start := clk.Now()
	s.Add(ctx, Source{}, "read_file", "a", "{}") // the first send goes out at once
	clk.Advance(2 * time.Second)
	takeSlot(t, s)
	s.Add(ctx, Source{}, "read_file", "b", "{}") // the edit holds for 1 s
	if api.count("editMessageText") != 0 {
		t.Fatal("a held edit must send nothing:", api.methods())
	}
	if got := ft.waitList(); !slices.Equal(got, []time.Duration{time.Second}) {
		t.Fatalf("timer waits %v, want the exact hold wait of 1 s", got)
	}
	s.mu.Lock()
	lastEdit, floodUntil := s.lastEdit, s.floodUntil
	s.mu.Unlock()
	if !lastEdit.Equal(start) || !floodUntil.IsZero() {
		t.Fatalf("lastEdit %v, floodUntil %v: a hold must set neither", lastEdit, floodUntil)
	}
	if logs.Len() != 0 {
		t.Fatal("a hold must not log at Info:", logs.String())
	}
	ft.run()
	if got := lastEditOf(api, firstBubble); got != `📖 read_file: "b" (×2)` {
		t.Fatal("the timer must send the held edit:", got)
	}
}

func TestClosedBubbleKeepsItsLastEditOverHolds(t *testing.T) {
	s, api, clk, ft, _ := pacedStatus(t, 42)
	const text = `📖 read_file: "z" (×9)`
	s.mu.Lock()
	s.waiting = []closedEdit{{msgID: firstBubble, text: text, tries: 1}}
	s.mu.Unlock()
	for i := range 2 * maxClosedEditTries {
		clk.Advance(2 * time.Second)
		takeSlot(t, s)
		s.mu.Lock()
		done := s.editClosed(ctx)
		tries := s.waiting[0].tries
		s.mu.Unlock()
		if done || tries != 1 {
			t.Fatalf("hold %d: done %v, tries %d: a hold is not a try", i+1, done, tries)
		}
	}
	if api.count("editMessageText") != 0 {
		t.Fatal("a held edit must send nothing:", api.methods())
	}
	if w := ft.waitList(); len(w) == 0 || w[0] != time.Second {
		t.Fatalf("timer waits %v, want the exact hold wait of 1 s", w)
	}
	clk.Advance(2 * time.Second)
	s.mu.Lock()
	done := s.editClosed(ctx)
	left := len(s.waiting)
	s.mu.Unlock()
	if !done || left != 0 || lastEditOf(api, firstBubble) != text {
		t.Fatalf("done %v, left %d: the closed bubble must keep its last edit", done, left)
	}
}

func TestQueuedSendHoldsForTheExactWait(t *testing.T) {
	s, api, _, ft, logs := pacedStatus(t, 42)
	takeSlot(t, s)
	queueSend(s, queuedPlanText)
	if api.count("sendMessage") != 0 || len(queueOf(s)) != 1 {
		t.Fatal("a held send must stay at the head of the queue:", api.methods())
	}
	if got := ft.waitList(); !slices.Equal(got, []time.Duration{time.Second}) {
		t.Fatalf("timer waits %v, want the exact hold wait of 1 s", got)
	}
	s.mu.Lock()
	floodUntil := s.floodUntil
	s.mu.Unlock()
	if !floodUntil.IsZero() || logs.Len() != 0 {
		t.Fatalf("floodUntil %v, log %q: a hold is not a flood", floodUntil, logs.String())
	}
	ft.run()
	if got := api.textsOf("sendMessage"); !slices.Equal(got, []string{queuedPlanText}) || len(queueOf(s)) != 0 {
		t.Fatalf("the timer must send the held entry: %q", got)
	}
}

func TestHeldFirstSendGivesDrainTheHoldWait(t *testing.T) {
	s, api, _, _, _ := pacedStatus(t, 42)
	takeSlot(t, s)
	s.Add(ctx, Source{}, "read_file", "a", "{}") // the first send holds for 1 s
	s.mu.Lock()
	wait := s.sendHeld(ctx)
	s.mu.Unlock()
	if wait != time.Second || api.count("sendMessage") != 0 {
		t.Fatalf("wait %s, calls %v: Drain must wait for the hold", wait, api.methods())
	}
}

func TestStatusEditsUseTheInterimLimitAndSendsUseTheQueueLimit(t *testing.T) {
	const group int64 = -100
	s, api, clk, ft, _ := pacedStatus(t, group)
	s.Add(ctx, Source{}, "read_file", "a", "{}") // the first send: 1 call in the window
	for range 14 {
		clk.Advance(time.Second)
		takeSlot(t, s)
	}
	// 15 calls are in the window. The last call is 14 s after the first call.
	clk.Advance(2 * time.Second)
	s.Add(ctx, Source{}, "read_file", "b", "{}")
	if api.count("editMessageText") != 0 {
		t.Fatal("an edit must hold at 15 calls in the window:", api.methods())
	}
	if got := ft.waitList(); !slices.Equal(got, []time.Duration{44 * time.Second}) {
		t.Fatalf("timer waits %v, want 44 s until the first call is 60 s old", got)
	}
	queueSend(s, queuedPlanText)
	if got := api.textsOf("sendMessage"); len(got) != 2 || got[1] != queuedPlanText {
		t.Fatalf("a queued send can use all 18 places of the window: %q", got)
	}
}

// TestHeldForcedEditIsNotAClosedTry checks that a bubble that closes while
// its forced edit holds keeps all its tries, because no request went out.
func TestHeldForcedEditIsNotAClosedTry(t *testing.T) {
	s, api, clk, _, _ := pacedStatus(t, 42)
	s.Add(ctx, Source{}, "read_file", "a", "{}") // the first send goes out at once
	clk.Advance(2 * time.Second)
	takeSlot(t, s)
	s.Add(ctx, Source{}, "read_file", "b", "{}") // the edit holds
	s.mu.Lock()
	s.closeBubble(ctx) // the forced edit holds too
	var tries []int
	for _, e := range s.waiting {
		tries = append(tries, e.tries)
	}
	s.mu.Unlock()
	if api.count("editMessageText") != 0 || !slices.Equal(tries, []int{0}) {
		t.Fatalf("edits %d, tries %v: a held forced edit is not a try", api.count("editMessageText"), tries)
	}
}

// TestStaleTimerCallbackKeepsTheNewTimer checks that a timer callback that
// fired before a stopTimer does no work and keeps the handle of the timer
// that came after it.
func TestStaleTimerCallbackKeepsTheNewTimer(t *testing.T) {
	s, api, _, ft, _ := pacedStatus(t, 42)
	takeSlot(t, s)
	queueSend(s, queuedPlanText) // the send holds, and the timer T1 waits
	if n := ft.pendingCount(); n != 1 {
		t.Fatal("pending timers", n)
	}
	// T1 fires. Its callback waits for s.mu, so it does not run yet.
	ft.mu.Lock()
	stale := ft.pending[0].f
	ft.pending = ft.pending[1:]
	ft.mu.Unlock()
	// The lock holder stops T1, which has no effect, and arms T2.
	s.Reset()
	if n := ft.pendingCount(); n != 1 {
		t.Fatal("pending timers after Reset", n)
	}
	stale()
	if n := ft.pendingCount(); n != 1 {
		t.Fatal("the stale callback armed a timer: pending", n)
	}
	if api.count("sendMessage") != 0 {
		t.Fatal("the stale callback sent:", api.methods())
	}
	s.mu.Lock()
	s.stopTimer()
	s.mu.Unlock()
	if n := ft.pendingCount(); n != 0 {
		t.Fatal("stopTimer did not stop T2: pending", n)
	}
}
