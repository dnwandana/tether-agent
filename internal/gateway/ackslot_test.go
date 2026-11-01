package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/provider"
)

// fakeHandle is an AckHandle that records its Close calls.
type fakeHandle struct {
	id     int
	mu     sync.Mutex
	closes []string
}

func (h *fakeHandle) MsgID() int { return h.id }

func (h *fakeHandle) Close(text string) <-chan struct{} {
	h.mu.Lock()
	h.closes = append(h.closes, text)
	h.mu.Unlock()
	done := make(chan struct{})
	close(done)
	return done
}

func (h *fakeHandle) texts() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.closes)
}

// handleFactory hands out renderers whose AckReplyHandle returns a fakeHandle. When hold is
// not nil, AckReplyHandle waits until hold is closed before it returns.
type handleFactory struct {
	mu      sync.Mutex
	hold    chan struct{}
	sent    []Reply
	handles []*fakeHandle
}

func (f *handleFactory) new() Renderer { return &handleRenderer{f: f} }

func (f *handleFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func (f *handleFactory) handle(i int) *fakeHandle {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.handles[i]
}

type handleRenderer struct {
	recRenderer
	f *handleFactory
}

func (r *handleRenderer) AckReplyHandle(rep Reply) AckHandle {
	f := r.f
	f.mu.Lock()
	f.sent = append(f.sent, rep)
	h := &fakeHandle{id: len(f.sent)}
	f.handles = append(f.handles, h)
	hold := f.hold
	f.mu.Unlock()
	if hold != nil {
		<-hold
	}
	return h
}

// wantCloses waits for the Close calls of h and fails the test when they are not want.
func wantCloses(t *testing.T, h *fakeHandle, want ...string) {
	t.Helper()
	waitFor(t, func() bool { return len(h.texts()) >= len(want) })
	time.Sleep(20 * time.Millisecond) // a second Close would come now
	if got := h.texts(); !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// slotGateway returns a gateway with a fake clock and no keys.
func slotGateway() (*Gateway, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	return &Gateway{Log: slog.New(slog.DiscardHandler), now: clk.now}, clk
}

// reserveSteer pushes text into rs and reserves a steer ack for it, like onBusy.
func reserveSteer(t *testing.T, g *Gateway, rs *RunState, text string) (*ackSlot, bool) {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	seq, ok := rs.Steer.PushSeq(text)
	if !ok {
		t.Fatal("push", text)
	}
	return g.reserveAckLocked(rs, "k", ackSteer, seq)
}

func TestSteerAckReadClosesUpToSeq(t *testing.T) {
	g, _ := slotGateway()
	rs := &RunState{Steer: agent.NewSteerQueue(), SessionID: "s1"}
	f := &handleFactory{}
	s1, send := reserveSteer(t, g, rs, "one")
	if !send || s1.seq != 1 {
		t.Fatal(send, s1.seq)
	}
	g.sendAck(s1, SteerAck, 1, f.new())
	// A second note within 30 s is covered by the same ack.
	if s2, send := reserveSteer(t, g, rs, "two"); send || s2 != s1 || s1.seq != 2 {
		t.Fatal(send, s1.seq)
	}
	_, read := rs.Steer.DrainSeq()
	g.steerRead(rs, read)
	wantCloses(t, f.handle(0), SteerReadAck)
	// The close deleted the mark, so a note after the drain gets a new ack at once.
	s3, send := reserveSteer(t, g, rs, "three")
	if !send || s3 == s1 || s3.seq != 3 {
		t.Fatal(send, s3.seq)
	}
	g.sendAck(s3, SteerAck, 1, f.new())
	// A late callback for the old drain does not close the new ack.
	g.steerRead(rs, read)
	wantCloses(t, f.handle(1))
	wantCloses(t, f.handle(0), SteerReadAck)
}

func TestSteerAckReadWhileSendHeld(t *testing.T) {
	g, _ := slotGateway()
	rs := &RunState{Steer: agent.NewSteerQueue(), SessionID: "s1"}
	f := &handleFactory{hold: make(chan struct{})}
	s1, _ := reserveSteer(t, g, rs, "one")
	sent := make(chan struct{})
	go func() { g.sendAck(s1, SteerAck, 1, f.new()); close(sent) }()
	waitFor(t, func() bool { return f.count() == 1 })
	_, read := rs.Steer.DrainSeq()
	g.steerRead(rs, read)
	if got := f.handle(0).texts(); len(got) != 0 {
		t.Fatal("the send did not return, so no edit can go out yet:", got)
	}
	close(f.hold)
	waitClosed(t, sent, "the send must return")
	wantCloses(t, f.handle(0), SteerReadAck)
}

func TestAckMarksPerKind(t *testing.T) {
	g, clk := slotGateway()
	rs := &RunState{Steer: agent.NewSteerQueue(), SessionID: "s1"}
	g.mu.Lock()
	q1, sendQ := g.reserveAckLocked(rs, "k", ackQueue, 0)
	g.mu.Unlock()
	if !sendQ || q1.pending != 1 {
		t.Fatal(sendQ, q1.pending)
	}
	// A queue ack does not hide a steer ack.
	s1, send := reserveSteer(t, g, rs, "one")
	if !send {
		t.Fatal("the steer ack must go out")
	}
	// A second queued message within 30 s is covered by the queue ack.
	g.mu.Lock()
	q2, sendQ := g.reserveAckLocked(rs, "k", ackQueue, 0)
	g.mu.Unlock()
	if sendQ || q2 != q1 || q1.pending != 2 {
		t.Fatal(sendQ, q1.pending)
	}
	// After 30 s, a new steer ack takes the mark.
	clk.add(31 * time.Second)
	s2, send := reserveSteer(t, g, rs, "two")
	if !send || s2 == s1 {
		t.Fatal(send)
	}
	// A late close of the old ack does not delete the newer mark. A slot closes once.
	g.mu.Lock()
	c := g.closeSlotLocked(s1, SteerReadAck)
	again := g.closeSlotLocked(s1, NotSentAck)
	g.mu.Unlock()
	if c.text != SteerReadAck || c.h != nil || again != (ackClose{}) || s1.outcome != SteerReadAck {
		t.Fatalf("%+v %+v %q", c, again, s1.outcome)
	}
	<-c.run() // no handle: the channel is closed
	if s3, send := reserveSteer(t, g, rs, "three"); send || s3 != s2 || s2.seq != 3 {
		t.Fatal(send, s2.seq)
	}
}

func TestSteerEndText(t *testing.T) {
	cases := []struct {
		name                        string
		pending                     string
		discard, takeOver, stopping bool
		want                        string
		ok                          bool
	}{
		{"normal end with a note left", "note", false, false, false, SteerLaterAck, true},
		{"normal end, a steer is lost", "", false, false, false, NotSentAck, true},
		{"/stop keep", "note", true, true, false, SteerLaterAck, true},
		{"/now took the note", "", true, true, false, SteerLaterAck, true},
		{"/stop", "note", true, false, false, NotSentAck, true},
		{"/stop after /now", "", true, false, false, NotSentAck, true},
		{"the gateway stops", "note", false, false, true, "", false},
		{"the gateway stops after /now", "", true, true, true, "", false},
	}
	for _, c := range cases {
		if got, ok := steerEndText(c.pending, c.discard, c.takeOver, c.stopping); got != c.want || ok != c.ok {
			t.Errorf("%s: got %q %v, want %q %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

// readGateway starts a turn on key "k". The first answer of the model calls read_file, and
// each answer takes 300 ms. A note that comes during the first answer reaches the agent with
// the tool result. done is closed when the turn ends.
func readGateway(t *testing.T) (g *Gateway, done chan struct{}) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(p, []byte("data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"path": p})
	fp := &fakeProv{delay: 300 * time.Millisecond, script: []step{
		{calls: []provider.ToolCall{{ID: "c1", Name: "read_file", Args: string(args)}}},
		{text: "done"},
	}}
	g = &Gateway{Deps: testDeps(t, fp)}
	g.debounce = NewDebouncer(time.Millisecond, 10*time.Millisecond)
	done = make(chan struct{})
	go func() { _ = g.Handle(context.Background(), inb("k", "read it"), (&recFactory{}).new); close(done) }()
	waitFor(t, func() bool { return len(fp.lastUserTexts()) == 1 })
	t.Cleanup(func() { <-done; g.Wait(5 * time.Second) })
	return g, done
}

func TestSteerAckReadByAgent(t *testing.T) {
	g, done := readGateway(t)
	f := &handleFactory{}
	_ = g.Handle(context.Background(), inb("k", "also count lines"), f.new)
	waitFor(t, func() bool { return f.count() == 1 })
	waitClosed(t, done, "the turn must end")
	wantCloses(t, f.handle(0), SteerReadAck)
}

func TestSteerAckRunEndsFirst(t *testing.T) {
	g, fp := gatewayWithSlowProvider(t, 300*time.Millisecond)
	g.debounce = NewDebouncer(time.Millisecond, 10*time.Millisecond)
	done := make(chan struct{})
	go func() { _ = g.Handle(context.Background(), inb("k", "first"), (&recFactory{}).new); close(done) }()
	waitFor(t, func() bool { return len(fp.lastUserTexts()) == 1 })
	f := &handleFactory{}
	_ = g.Handle(context.Background(), inb("k", "note"), f.new)
	waitFor(t, func() bool { return f.count() == 1 })
	waitClosed(t, done, "the turn must end")
	wantCloses(t, f.handle(0), SteerLaterAck)
	// The note runs as the next turn.
	waitFor(t, func() bool { got := fp.lastUserTexts(); return len(got) == 2 && got[1] == "note" })
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
}

func TestSteerAckAfterNowAndStopKeep(t *testing.T) {
	for _, cmd := range []string{"/now", "/stop keep"} {
		t.Run(cmd, func(t *testing.T) {
			g, _, _, done, fp := busyGatewayProv(t)
			f := &handleFactory{}
			_ = g.Handle(context.Background(), inb("k", "note"), f.new)
			waitFor(t, func() bool { return f.count() == 1 })
			_ = g.Handle(context.Background(), inb("k", cmd), func() Renderer { return &replyRenderer{} })
			waitClosed(t, done, cmd+" must end the run")
			wantCloses(t, f.handle(0), SteerLaterAck)
			waitFor(t, func() bool { got := fp.lastUserTexts(); return len(got) == 2 && got[1] == "note" })
		})
	}
}

func TestSteerAckNotSentAfterStop(t *testing.T) {
	for _, cmd := range []string{"/stop", "/new", "/reset"} {
		t.Run(cmd, func(t *testing.T) {
			g, _, _, done := busyGateway(t)
			f := &handleFactory{}
			_ = g.Handle(context.Background(), inb("k", "note"), f.new)
			waitFor(t, func() bool { return f.count() == 1 })
			// A /steer note gets its own ack, with no 30 s limit.
			_ = g.Handle(context.Background(), inb("k", "/steer use the cache"), f.new)
			waitFor(t, func() bool { return f.count() == 2 })
			_ = g.Handle(context.Background(), inb("k", cmd), func() Renderer { return &recRenderer{} })
			waitClosed(t, done, cmd+" must end the run")
			wantCloses(t, f.handle(0), NotSentAck)
			wantCloses(t, f.handle(1), NotSentAck)
		})
	}
}

func TestSteerAckWithNoHandle(t *testing.T) {
	g, f, _, done := busyGateway(t)
	_ = g.Handle(context.Background(), inb("k", "note"), f.new)
	waitFor(t, func() bool { return len(f.acks()) == 1 })
	_ = g.Handle(context.Background(), inb("k", "/stop"), func() Renderer { return &recRenderer{} })
	waitClosed(t, done, "/stop must end the run")
	if got := f.acks(); !slices.Equal(got, []string{SteerAck}) {
		t.Fatal(got)
	}
}
