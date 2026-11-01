package gateway

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
)

const (
	wantStarted = "▶️ The agent works on your message now."
	wantNotSent = "⏹ Not sent. The task stopped."
)

// ackRecHandle is a fake AckHandle. It records the text of each Close call. "" is a close
// with no edit.
type ackRecHandle struct {
	id   int
	hold chan struct{}

	mu    sync.Mutex
	texts []string
}

func (h *ackRecHandle) MsgID() int { return h.id }

// Close records text. When hold is not nil, an edit ends only after hold is closed.
func (h *ackRecHandle) Close(text string) <-chan struct{} {
	h.mu.Lock()
	h.texts = append(h.texts, text)
	h.mu.Unlock()
	done := make(chan struct{})
	go func() {
		if h.hold != nil && text != "" {
			<-h.hold
		}
		close(done)
	}()
	return done
}

func (h *ackRecHandle) closes() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.texts)
}

// ackRecFactory hands out renderers that are HandleRenderers. Each busy ack gets a new
// handle. The message IDs start at 101.
type ackRecFactory struct {
	hold chan struct{}

	mu      sync.Mutex
	texts   []string
	handles []*ackRecHandle
}

func (f *ackRecFactory) new() Renderer { return &ackRecRenderer{f: f} }

// sent returns the text of each busy ack, in order.
func (f *ackRecFactory) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.texts)
}

// handle waits for the busy ack i and returns its handle.
func (f *ackRecFactory) handle(t *testing.T, i int) *ackRecHandle {
	t.Helper()
	waitFor(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.handles) > i })
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.handles[i]
}

type ackRecRenderer struct {
	recRenderer
	f *ackRecFactory
}

func (r *ackRecRenderer) AckReplyHandle(rep Reply) AckHandle {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	h := &ackRecHandle{id: 101 + len(r.f.handles), hold: r.f.hold}
	r.f.handles = append(r.f.handles, h)
	r.f.texts = append(r.f.texts, rep.Text)
	return h
}

// heldKey makes key "k" busy with the turn first and its run rs. The agent did not return
// yet. endHeld ends the turn.
func heldKey(g *Gateway) (first *pendingTurn, rs *RunState) {
	f := &recFactory{}
	first = &pendingTurn{in: inb("k", "first"), newRenderer: f.new}
	rs = &RunState{Cancel: func() {}, Steer: agent.NewSteerQueue(), Started: time.Now(), SessionID: "s1"}
	g.mu.Lock()
	defer g.mu.Unlock()
	ks := g.key("k")
	ks.busy = true
	ks.take(first)
	ks.running = rs
	return first, rs
}

// endHeld ends the turn first like /stop and gives the key to the next queued message.
func endHeld(g *Gateway, first *pendingTurn) {
	g.mu.Lock()
	first.discard = true
	g.mu.Unlock()
	g.run(context.Background(), first)
}

func TestQueueAckClosesWhenTheMediaStarts(t *testing.T) {
	if QueueStartedAck != wantStarted {
		t.Fatalf("%q", QueueStartedAck)
	}
	ctx := context.Background()
	g, _ := gatewayWithSlowProvider(t, 0)
	first, _ := heldKey(g)
	f := &ackRecFactory{}
	rest := g.acceptMessage(ctx, media("k"), f.new)
	h := f.handle(t, 0)
	if got := f.sent(); !reflect.DeepEqual(got, []string{QueueAck}) || len(h.closes()) != 0 {
		t.Fatalf("%q %q", got, h.closes())
	}
	endHeld(g, first)
	if got := h.closes(); !reflect.DeepEqual(got, []string{wantStarted}) {
		t.Fatalf("%q", got)
	}
	if err := rest(); err != nil {
		t.Fatal(err)
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	if got := h.closes(); len(got) != 1 {
		t.Fatalf("the ack must close once: %q", got)
	}
}

func TestQueueAckClosesWhenTheBusyTextStarts(t *testing.T) {
	g, fp := gatewayWithSlowProvider(t, 0)
	first, rs := heldKey(g)
	g.mu.Lock()
	rs.stopped = true // The run takes no steer, so the text waits in the queue.
	g.mu.Unlock()
	f := &ackRecFactory{}
	g.flushBusyText(context.Background(), inb("k", "later"), f.new)
	h := f.handle(t, 0)
	if g.queueLen("k") != 1 || len(h.closes()) != 0 {
		t.Fatal(g.queueLen("k"), h.closes())
	}
	endHeld(g, first)
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	if got := h.closes(); !reflect.DeepEqual(got, []string{wantStarted}) {
		t.Fatalf("%q", got)
	}
	if got := fp.lastUserTexts(); len(got) != 1 || got[0] != "later" {
		t.Fatal(got)
	}
}

func TestQueueAckForTwoMessagesClosesAtTheLastStart(t *testing.T) {
	ctx := context.Background()
	g, _ := gatewayWithSlowProvider(t, 0)
	first, _ := heldKey(g)
	f := &ackRecFactory{}
	rest1 := g.acceptMessage(ctx, media("k"), f.new)
	rest2 := g.acceptMessage(ctx, media("k"), f.new)
	h := f.handle(t, 0)
	if got := f.sent(); len(got) != 1 {
		t.Fatalf("one ack covers both messages: %q", got)
	}
	endHeld(g, first)
	if got := h.closes(); len(got) != 0 {
		t.Fatalf("the second message still waits: %q", got)
	}
	// The turn of the first photo ends, and next gives the key to the second photo.
	if err := rest1(); err != nil {
		t.Fatal(err)
	}
	if got := h.closes(); !reflect.DeepEqual(got, []string{wantStarted}) {
		t.Fatalf("%q", got)
	}
	if err := rest2(); err != nil {
		t.Fatal(err)
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	if got := h.closes(); len(got) != 1 {
		t.Fatalf("the ack must close once: %q", got)
	}
}

func TestQueueAckNotSentWhenTheQueueIsDropped(t *testing.T) {
	for _, cmd := range []string{"/stop", "/new", "/reset"} {
		t.Run(cmd, func(t *testing.T) {
			ctx := context.Background()
			g, _ := gatewayWithSlowProvider(t, 0)
			heldKey(g)
			f := &ackRecFactory{}
			rest1 := g.acceptMessage(ctx, media("k"), f.new)
			rest2 := g.acceptMessage(ctx, media("k"), f.new)
			h := f.handle(t, 0)
			_ = g.Handle(ctx, inb("k", cmd), func() Renderer { return &recRenderer{} })
			if got := h.closes(); !reflect.DeepEqual(got, []string{wantNotSent}) {
				t.Fatalf("%q", got)
			}
			if err := rest1(); err != nil {
				t.Fatal(err)
			}
			if err := rest2(); err != nil {
				t.Fatal(err)
			}
			if got := f.sent(); len(got) != 1 {
				t.Fatalf("%q", got)
			}
		})
	}
}

func TestQueueAckNotSentWhenTheWaiterLeaves(t *testing.T) {
	g, _ := gatewayWithSlowProvider(t, 0)
	heldKey(g)
	f := &ackRecFactory{}
	ctx, cancel := context.WithCancel(context.Background())
	rest := g.acceptMessage(ctx, media("k"), f.new)
	h := f.handle(t, 0)
	cancel()
	if err := rest(); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if got := h.closes(); !reflect.DeepEqual(got, []string{wantNotSent}) {
		t.Fatalf("%q", got)
	}

	// While the gateway stops, the waiter exit does not edit the ack.
	g2, _ := gatewayWithSlowProvider(t, 0)
	heldKey(g2)
	f2 := &ackRecFactory{}
	ctx2, cancel2 := context.WithCancel(context.Background())
	rest2 := g2.acceptMessage(ctx2, media("k"), f2.new)
	h2 := f2.handle(t, 0)
	g2.stopping.Store(true)
	cancel2()
	if err := rest2(); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if got := h2.closes(); len(got) != 0 {
		t.Fatalf("%q", got)
	}
}

func TestTapOnQueueAckClosesWithNoEdit(t *testing.T) {
	ctx := context.Background()
	g, _ := gatewayWithSlowProvider(t, 0)
	first, _ := heldKey(g)
	f := &ackRecFactory{}
	rest := g.acceptMessage(ctx, media("k"), f.new)
	h := f.handle(t, 0)
	tap := inb("k", fmt.Sprintf("/now %d", first.turn))
	tap.ButtonMsgID = h.MsgID()
	rr := &replyRenderer{}
	if err := g.Handle(ctx, tap, func() Renderer { return rr }); err != nil {
		t.Fatal(err)
	}
	if got := h.closes(); !reflect.DeepEqual(got, []string{""}) {
		t.Fatalf("%q", got)
	}
	if len(rr.replies) != 1 || rr.replies[0].Text != nowOneReply || !rr.replies[0].Edit {
		t.Fatalf("%+v", rr.replies)
	}
	// The photo starts now. No later outcome edits the tapped message.
	g.run(ctx, first)
	if err := rest(); err != nil {
		t.Fatal(err)
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	if got := h.closes(); !reflect.DeepEqual(got, []string{""}) {
		t.Fatalf("%q", got)
	}
}

func TestTapOnSteerAckClosesWithNoEdit(t *testing.T) {
	ctx := context.Background()
	g, fp := gatewayWithSlowProvider(t, 0)
	first, rs := heldKey(g)
	f := &ackRecFactory{}
	g.flushBusyText(ctx, inb("k", "note"), f.new)
	h := f.handle(t, 0)
	if rs.Steer.Pending() != "note" || !reflect.DeepEqual(f.sent(), []string{SteerAck}) {
		t.Fatal(rs.Steer.Pending(), f.sent())
	}
	if took, waiting, _ := g.takeOver("k", first.turn, h.MsgID()); !took || waiting != 1 {
		t.Fatal(took, waiting)
	}
	if got := h.closes(); !reflect.DeepEqual(got, []string{""}) {
		t.Fatalf("%q", got)
	}
	// The run end must not edit the tapped ack.
	g.run(ctx, first)
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	if got := h.closes(); !reflect.DeepEqual(got, []string{""}) {
		t.Fatalf("%q", got)
	}
	if got := fp.lastUserTexts(); len(got) != 1 || got[0] != "note" {
		t.Fatal(got)
	}
}
