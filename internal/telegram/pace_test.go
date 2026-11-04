package telegram

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
)

// testPace returns a pace on the fake clock.
func testPace(clk *fakeClock) *ChatPace {
	p := NewChatPace()
	p.Now = clk.Now
	return p
}

func TestPaceDMSlot(t *testing.T) {
	clk := newFakeClock()
	p := testPace(clk)
	if d := p.TryTake(42, paceWait); d != 0 {
		t.Fatal("first call held for", d)
	}
	if d := p.Next(42, paceWait); d != time.Second {
		t.Fatal("next", d)
	}
	clk.Advance(400 * time.Millisecond)
	if d := p.TryTake(42, paceWait); d != 600*time.Millisecond {
		t.Fatal("second call in the same second", d)
	}
	clk.Advance(600 * time.Millisecond)
	if d := p.TryTake(42, paceWait); d != 0 {
		t.Fatal("after the slot", d)
	}
	// A private chat has no window: 30 calls 1 s apart all go out.
	for i := range 30 {
		clk.Advance(time.Second)
		if d := p.TryTake(42, paceInterim); d != 0 {
			t.Fatalf("DM call %d held for %s", i, d)
		}
	}
}

func TestPaceGroupWindow(t *testing.T) {
	clk := newFakeClock()
	p := testPace(clk)
	for i := range 18 {
		if d := p.TryTake(-100, paceWait); d != 0 {
			t.Fatalf("call %d held for %s", i+1, d)
		}
		clk.Advance(time.Second)
	}
	// The calls are at 0..17 s and now is 18 s. The 19th call waits until
	// the oldest call is 60 s old.
	if d := p.Next(-100, paceWait); d != 42*time.Second {
		t.Fatal("19th call", d)
	}
	clk.Advance(42 * time.Second)
	if d := p.TryTake(-100, paceWait); d != 0 {
		t.Fatal("at 60 s", d)
	}
	// The window holds 1..17 s and 60 s. The next call waits for the call at 1 s.
	if d := p.Next(-100, paceWait); d != time.Second {
		t.Fatal("after the window moved", d)
	}
}

func TestPaceInterimLimit(t *testing.T) {
	clk := newFakeClock()
	p := testPace(clk)
	for i := range 15 {
		if d := p.TryTake(-100, paceQueue); d != 0 {
			t.Fatalf("call %d held for %s", i+1, d)
		}
		clk.Advance(time.Second)
	}
	// 15 calls at 0..14 s, now 15 s. An interim call holds until 60 s.
	if d := p.TryTake(-100, paceInterim); d != 45*time.Second {
		t.Fatal("interim at 15 calls", d)
	}
	// A queue call and a wait call still go out.
	if d := p.TryTake(-100, paceQueue); d != 0 {
		t.Fatal("queue call", d)
	}
	clk.Advance(time.Second)
	if d, _ := p.Reserve(-100, paceWait); d != 0 {
		t.Fatal("wait call", d)
	}
	clk.Advance(time.Second)
	if d := p.TryTake(-100, paceQueue); d != 0 {
		t.Fatal("18th call", d)
	}
	clk.Advance(time.Second)
	// 18 calls at 0..17 s, now 18 s.
	if d := p.Next(-100, paceWait); d != 42*time.Second {
		t.Fatal("wait at 18 calls", d)
	}
	if d := p.Next(-100, paceQueue); d != 42*time.Second {
		t.Fatal("queue at 18 calls", d)
	}
	if d := p.Next(-100, paceInterim); d != 45*time.Second {
		t.Fatal("interim at 18 calls", d)
	}
	// A reply call ignores the window.
	if d := p.Next(-100, paceReply); d != 0 {
		t.Fatal("reply at 18 calls", d)
	}
}

func TestPaceFloodEnd(t *testing.T) {
	clk := newFakeClock()
	p := testPace(clk)
	start := clk.Now()
	p.NoteFlood(-100, 30*time.Second)
	if got := p.FloodEnd(-100); !got.Equal(start.Add(31 * time.Second)) {
		t.Fatal("flood end", got.Sub(start))
	}
	for _, class := range []paceClass{paceDefault, paceWait, paceReply, paceQueue, paceInterim} {
		if d := p.Next(-100, class); d != 31*time.Second {
			t.Fatalf("class %d: %s", class, d)
		}
	}
	if d := p.TryTake(-100, paceInterim); d != 31*time.Second {
		t.Fatal("interim in a flood", d)
	}
	// Another chat is not held.
	if d := p.Next(-200, paceWait); d != 0 {
		t.Fatal("other chat", d)
	}
	// An earlier end does not replace the later end.
	p.NoteFlood(-100, 5*time.Second)
	if got := p.FloodEnd(-100); !got.Equal(start.Add(31 * time.Second)) {
		t.Fatal("flood end after a shorter flood", got.Sub(start))
	}
	// A 429 with no retry_after holds for 2 s.
	p.NoteFlood(7, 0)
	if d := p.Next(7, paceReply); d != 2*time.Second {
		t.Fatal("no retry_after", d)
	}
	clk.Advance(31 * time.Second)
	if d := p.TryTake(-100, paceWait); d != 0 {
		t.Fatal("after the flood end", d)
	}
}

func TestPaceParallelReservations(t *testing.T) {
	clk := newFakeClock()
	p := testPace(clk)
	waits := make([]time.Duration, 2)
	var wg sync.WaitGroup
	for i := range waits {
		wg.Go(func() { waits[i], _ = p.Reserve(-100, paceWait) })
	}
	wg.Wait()
	slices.Sort(waits)
	if waits[0] != 0 || waits[1] != time.Second {
		t.Fatal("two callers must get two slots", waits)
	}
}

func TestPaceReleaseGivesSlotBack(t *testing.T) {
	clk := newFakeClock()
	p := testPace(clk)
	if d, _ := p.Reserve(-100, paceWait); d != 0 {
		t.Fatal("first", d)
	}
	d, release := p.Reserve(-100, paceWait)
	if d != time.Second {
		t.Fatal("second", d)
	}
	release()
	release() // A second call does nothing.
	if d := p.Next(-100, paceWait); d != time.Second {
		t.Fatal("after release", d)
	}
	if d, _ := p.Reserve(-100, paceWait); d != time.Second {
		t.Fatal("the released slot", d)
	}
	if d := p.Next(-100, paceWait); d != 2*time.Second {
		t.Fatal("after the new reservation", d)
	}
}

func TestPaceTryReserveRelease(t *testing.T) {
	clk := newFakeClock()
	p := testPace(clk)
	d, release := p.TryReserve(-100, paceQueue)
	if d != 0 {
		t.Fatal("first", d)
	}
	if d, _ := p.TryReserve(-100, paceQueue); d != time.Second {
		t.Fatal("a held call must wait for the slot", d)
	}
	release()
	release() // A second call does nothing.
	if d := p.Next(-100, paceWait); d != 0 {
		t.Fatal("after release", d)
	}
}

func TestPaceNoChat(t *testing.T) {
	clk := newFakeClock()
	p := testPace(clk)
	for range 5 {
		if d := p.TryTake(0, paceWait); d != 0 {
			t.Fatal("chat 0 held", d)
		}
	}
	d, release := p.Reserve(0, paceWait)
	if d != 0 {
		t.Fatal("chat 0 reserve", d)
	}
	release()
	p.NoteFlood(0, 30*time.Second)
	if !p.FloodEnd(0).IsZero() {
		t.Fatal("chat 0 has a flood end")
	}
	var nilPace *ChatPace
	if !nilPace.FloodEnd(-100).IsZero() {
		t.Fatal("nil pace has a flood end")
	}
}

func TestPaceIdleCleanup(t *testing.T) {
	clk := newFakeClock()
	p := testPace(clk)
	p.TryTake(-100, paceWait)
	p.NoteFlood(-200, 10*time.Minute)
	clk.Advance(2*time.Minute + time.Second)
	p.TryTake(42, paceWait)
	p.mu.Lock()
	_, old := p.chats[-100]
	_, flood := p.chats[-200]
	n := len(p.chats)
	p.mu.Unlock()
	if old || !flood || n != 2 {
		t.Fatalf("old %v, flood %v, chats %d", old, flood, n)
	}
}

func TestPaceHoldError(t *testing.T) {
	err := fmt.Errorf("telegram sendMessage: %w", &paceHold{Wait: 3 * time.Second})
	if d, ok := isPaceHold(err); !ok || d != 3*time.Second {
		t.Fatal(d, ok)
	}
	if _, ok := floodWait(err); ok {
		t.Fatal("a hold is not a flood")
	}
	if _, ok := isPaceHold(errors.New("other")); ok {
		t.Fatal("other error is a hold")
	}
}
