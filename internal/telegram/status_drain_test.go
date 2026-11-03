package telegram

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
)

const (
	drainStart = "📋 Plan 0/2\n[>] Find the flaky test\n[ ] Run the cron tests 20 times"
	drainDone  = "📋 Plan done 2/2\n[x] Find the flaky test\n[x] Run the cron tests 20 times"
)

// drainQueue adds entries to the send queue of s, as a flood wait leaves them.
func drainQueue(s *Status, entries ...queuedSend) {
	s.mu.Lock()
	s.queue = append(s.queue, entries...)
	s.mu.Unlock()
}

func drainQueueLen(s *Status) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue)
}

func drainFloodUntil(s *Status, t time.Time) {
	s.mu.Lock()
	s.floodUntil = t
	s.mu.Unlock()
}

func TestCloseWithCleanupDropsTheEntriesItDeletes(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.Cleanup = true
	s.Add(ctx, Source{}, "read_file", "a", "{}") // the bubble, message 101
	drainQueue(s, queuedSend{text: drainStart, silent: true, deletable: true},
		queuedSend{text: drainDone, silent: true})
	s.Close(ctx, true)
	if got, want := api.textsOf("sendMessage"), []string{`📖 read_file: "a"`, drainDone}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	// The sends come before the deletes, and the done list stays.
	if got, want := api.methods(), []string{"sendMessage", "sendMessage", "deleteMessage"}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if id := api.lastCall("deleteMessage").Body["message_id"]; id != float64(101) {
		t.Fatal("deleted message:", id)
	}
	if n := drainQueueLen(s); n != 0 {
		t.Fatal("queue entries left:", n)
	}
}

func TestCloseOfAFailedTurnSendsTheQueue(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.Cleanup = true
	s.Add(ctx, Source{}, "read_file", "a", "{}")
	drainQueue(s, queuedSend{text: drainStart, silent: true, deletable: true},
		queuedSend{text: drainDone, silent: true})
	s.Close(ctx, false)
	if got, want := api.textsOf("sendMessage"), []string{`📖 read_file: "a"`, drainStart, drainDone}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if api.count("deleteMessage") != 0 {
		t.Fatal("a failed turn must delete nothing:", api.methods())
	}
}

func TestCloseDropsAnEntryThatFailsAgain(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	api.setReply(func(method string, _ map[string]any) *APIError {
		if method == "sendMessage" {
			return &APIError{Code: 502, Description: "Bad Gateway"}
		}
		return nil
	})
	drainQueue(s, queuedSend{text: drainDone, silent: true})
	s.Close(ctx, false)
	if api.count("sendMessage") != 1 || drainQueueLen(s) != 0 {
		t.Fatal(api.methods(), drainQueueLen(s))
	}
}

func TestCloseKeepsAnEntryThatWaitsForAFloodWait(t *testing.T) {
	s, api, clock := statusWithFakeAPI(t, "all")
	s.mu.Lock()
	s.flushCtx = ctx
	s.floodUntil = clock.Now().Add(3 * time.Second)
	s.mu.Unlock()
	drainQueue(s, queuedSend{text: drainDone, silent: true})
	s.Close(ctx, true)
	if api.count("sendMessage") != 0 || drainQueueLen(s) != 1 {
		t.Fatal("Close must keep the entry while the flood wait runs", api.methods(), drainQueueLen(s))
	}
	clock.Advance(3 * time.Second)
	fireTimer(s)
	if got := api.textsOf("sendMessage"); len(got) != 1 || got[0] != drainDone {
		t.Fatalf("the timer must send the done list after Close, got %q", got)
	}
}

func TestDropCleanupEntries(t *testing.T) {
	for _, tc := range []struct {
		cleanup, success bool
		want             []string
	}{
		{true, true, []string{drainDone}},
		{true, false, []string{drainStart, drainDone}},
		{false, true, []string{drainStart, drainDone}},
	} {
		s, _, _ := statusWithFakeAPI(t, "all")
		s.Cleanup = tc.cleanup
		drainQueue(s, queuedSend{text: drainStart, silent: true, deletable: true},
			queuedSend{text: drainDone, silent: true})
		s.DropCleanupEntries(tc.success)
		s.mu.Lock()
		var got []string
		for _, e := range s.queue {
			got = append(got, e.text)
		}
		s.mu.Unlock()
		if !slices.Equal(got, tc.want) {
			t.Fatalf("cleanup %v, success %v: got %q, want %q", tc.cleanup, tc.success, got, tc.want)
		}
	}
}

func TestDrainSendsTheQueueAfterTheFloodWait(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.Now = time.Now
	start := time.Now()
	drainQueue(s, queuedSend{text: drainDone, silent: true})
	drainFloodUntil(s, start.Add(100*time.Millisecond))
	s.Drain(ctx)
	if el := time.Since(start); el < 100*time.Millisecond || el > 5*time.Second {
		t.Fatal("Drain returned after", el)
	}
	if got := api.textsOf("sendMessage"); !slices.Equal(got, []string{drainDone}) {
		t.Fatalf("%q", got)
	}
	if silent, _ := api.lastCall("sendMessage").Body["disable_notification"].(bool); !silent {
		t.Fatal("the plan message must be silent")
	}
	if n := drainQueueLen(s); n != 0 {
		t.Fatal("queue entries left:", n)
	}
}

func TestDrainStopsWhenTheContextEnds(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	drainQueue(s, queuedSend{text: drainDone, silent: true})
	drainFloodUntil(s, clk.Now().Add(time.Hour))
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	time.AfterFunc(50*time.Millisecond, cancel) // a /stop while Drain waits
	start := time.Now()
	s.Drain(cctx)
	if el := time.Since(start); el < 50*time.Millisecond || el > 5*time.Second {
		t.Fatal("Drain returned after", el)
	}
	if api.count("sendMessage") != 0 || drainQueueLen(s) != 1 {
		t.Fatal("the entry must stay in the queue:", api.methods(), drainQueueLen(s))
	}
}

func TestDrainStopsAtTheLimit(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	drainQueue(s, queuedSend{text: drainDone, silent: true})
	drainFloodUntil(s, clk.Now().Add(time.Hour))
	// Each read moves the clock 31 s, so the second read is after the 30 s limit.
	s.Now = func() time.Time {
		now := clk.Now()
		clk.Advance(31 * time.Second)
		return now
	}
	start := time.Now()
	s.Drain(ctx)
	if el := time.Since(start); el > 5*time.Second {
		t.Fatal("Drain did not stop at the limit:", el)
	}
	if api.count("sendMessage") != 0 || drainQueueLen(s) != 1 {
		t.Fatal("the entry must stay in the queue:", api.methods(), drainQueueLen(s))
	}
}

// drainRenderer returns a group renderer with a queued start list and done
// list. The status has a real clock and a flood wait of 100 ms.
func drainRenderer(t *testing.T) (*tgRenderer, *fakeBot) {
	t.Helper()
	api := newFakeBot(t)
	ch := &Channel{C: api.client(), Cfg: config.Default().Telegram, typingEvery: 20 * time.Millisecond}
	r := ch.newRenderer(ctx, 42, "", false)
	r.status.Cleanup = true
	drainQueue(r.status, queuedSend{text: drainStart, silent: true, deletable: true},
		queuedSend{text: drainDone, silent: true})
	drainFloodUntil(r.status, time.Now().Add(100*time.Millisecond))
	return r, api
}

func TestRendererDoneDrainsTheQueueBeforeTheAnswer(t *testing.T) {
	r, api := drainRenderer(t)
	// Only the Drain call in Done can send the done list first.
	r.stream.BeforeNewMessage = nil
	r.Done("All tests pass", nil)
	// The cleanup deletes the start list, so it never goes out.
	if got, want := api.textsOf("sendMessage"), []string{drainDone, "All tests pass"}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if api.count("deleteMessage") != 0 {
		t.Fatal(api.methods())
	}
}

func TestRendererStoppedAnswerDoesNotWaitForTheQueue(t *testing.T) {
	r, api := drainRenderer(t)
	drainFloodUntil(r.status, time.Now().Add(time.Hour))
	start := time.Now()
	r.Done("Stopped", fmt.Errorf("turn: %w", context.Canceled))
	if el := time.Since(start); el > 5*time.Second {
		t.Fatal("the answer after a /stop waited for the queue:", el)
	}
	if got := api.textsOf("sendMessage"); !slices.Equal(got, []string{"Stopped"}) {
		t.Fatalf("%q", got)
	}
}

func TestRendererAnswerMessageWaitsForTheQueue(t *testing.T) {
	r, api := drainRenderer(t)
	r.status.Cleanup = false
	r.Delta("All tests pass")
	got := api.textsOf("sendMessage")
	if len(got) != 3 || got[0] != drainStart || got[1] != drainDone || !strings.HasPrefix(got[2], "All tests pass") {
		t.Fatalf("%q", got)
	}
	r.Done("", nil)
}

func TestDrainSendsAHeldBubbleAfterTheQueue(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	s.Now = time.Now
	drainQueue(s, queuedSend{text: drainDone, silent: true})
	drainFloodUntil(s, time.Now().Add(100*time.Millisecond))
	s.Add(ctx, Source{}, "read_file", "a", "{}") // the bubble waits behind the queue
	s.Drain(ctx)
	want := []string{drainDone, `📖 read_file: "a"`}
	if got := api.textsOf("sendMessage"); !slices.Equal(got, want) {
		t.Fatalf("the held bubble must go out before the answer: got %q, want %q", got, want)
	}
	s.Reset() // the answer message
	if got := api.textsOf("sendMessage"); !slices.Equal(got, want) {
		t.Fatalf("the bubble must go out one time: %q", got)
	}
}

func TestDrainReturnsAtOnceOnAnEndedContext(t *testing.T) {
	s, api, _ := statusWithFakeAPI(t, "all")
	drainQueue(s, queuedSend{text: drainDone, silent: true})
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	s.Drain(cctx)
	if api.count("sendMessage") != 0 || drainQueueLen(s) != 1 {
		t.Fatal("Drain must send nothing on an ended context:", api.methods(), drainQueueLen(s))
	}
}

func TestCleanupDropsAHeldBubble(t *testing.T) {
	s, api, clk := statusWithFakeAPI(t, "all")
	s.Cleanup = true
	drainQueue(s, queuedSend{text: drainDone, silent: true})
	drainFloodUntil(s, clk.Now().Add(time.Hour))
	s.Add(ctx, Source{}, "read_file", "a", "{}") // the bubble waits behind the queue
	drainFloodUntil(s, time.Time{})
	// The order of tgRenderer.Done.
	s.DropCleanupEntries(true)
	s.Drain(ctx)
	s.Close(ctx, true)
	if got := api.textsOf("sendMessage"); !slices.Equal(got, []string{drainDone}) {
		t.Fatalf("the cleanup must not send a bubble that it deletes: %q", got)
	}
	if api.count("deleteMessage") != 0 {
		t.Fatal(api.methods())
	}
}

func TestRendererAnswerWaitsForTheQueueOneTime(t *testing.T) {
	r, _ := drainRenderer(t)
	r.Done("All tests pass", nil)
	// After the Drain in Done, the answer of Done does not wait again.
	drainQueue(r.status, queuedSend{text: "📋 Plan 0/1\n[ ] Late", silent: true})
	drainFloodUntil(r.status, time.Now().Add(time.Hour))
	start := time.Now()
	r.stream.BeforeNewMessage(ctx)
	if el := time.Since(start); el > 5*time.Second {
		t.Fatal("the answer waited for the queue two times:", el)
	}
}

func TestRendererStopEndsTheWaitOfTheAnswer(t *testing.T) {
	r, api := drainRenderer(t)
	r.status.Cleanup = false
	drainFloodUntil(r.status, time.Now().Add(time.Hour))
	tctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.TurnContext(tctx)
	time.AfterFunc(50*time.Millisecond, cancel) // a /stop while the answer waits
	start := time.Now()
	r.Delta("All tests pass")
	if el := time.Since(start); el > 5*time.Second {
		t.Fatal("a /stop must end the wait of the answer:", el)
	}
	if got := api.textsOf("sendMessage"); len(got) != 1 || !strings.HasPrefix(got[0], "All tests pass") {
		t.Fatalf("%q", got)
	}
	r.Done("", context.Canceled)
}
