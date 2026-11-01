package gateway

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// A tap on the button of an old turn takes nothing. The /now reply edits the message, and
// the ack stays open, so the start of the last covered message still edits it.
func TestTapOnOldTurnKeepsQueueAckOpen(t *testing.T) {
	ctx := context.Background()
	g, _ := gatewayWithSlowProvider(t, 0)
	first, _ := heldKey(g)
	f := &ackRecFactory{}
	rest1 := g.acceptMessage(ctx, media("k"), f.new)
	rest2 := g.acceptMessage(ctx, media("k"), f.new)
	h := f.handle(t, 0)
	// The first photo takes the key, so the button of the first turn is old now.
	endHeld(g, first)
	if took, _, _ := g.takeOver("k", first.turn, h.MsgID()); took {
		t.Fatal("a tap on an old turn must take nothing")
	}
	if got := h.closes(); len(got) != 0 {
		t.Fatalf("the second photo still waits: %q", got)
	}
	if err := rest1(); err != nil {
		t.Fatal(err)
	}
	if got := h.closes(); !reflect.DeepEqual(got, []string{QueueStartedAck}) {
		t.Fatalf("%q", got)
	}
	if err := rest2(); err != nil {
		t.Fatal(err)
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
}

// /now moves the steer into the queue. A /stop before that turn starts drops it, so the
// ack says "Not sent".
func TestNowThenStopSteerAckNotSent(t *testing.T) {
	g, _ := gatewayWithSlowProvider(t, 0)
	heldKey(g)
	f := &ackRecFactory{}
	g.flushBusyText(context.Background(), inb("k", "note"), f.new)
	h := f.handle(t, 0)
	if took, _, _ := g.takeOver("k", 0, 0); !took {
		t.Fatal("/now took nothing")
	}
	if got := h.closes(); len(got) != 0 {
		t.Fatalf("the note did not run yet: %q", got)
	}
	if !g.stopKey("k") {
		t.Fatal("no run")
	}
	if got := h.closes(); !reflect.DeepEqual(got, []string{NotSentAck}) {
		t.Fatalf("%q", got)
	}
}

// /now moves the steer into the queue. The ack closes when that turn starts.
func TestNowSteerAckClosesWhenTheNoteStarts(t *testing.T) {
	ctx := context.Background()
	g, _ := gatewayWithSlowProvider(t, 0)
	first, _ := heldKey(g)
	f := &ackRecFactory{}
	g.flushBusyText(ctx, inb("k", "note"), f.new)
	h := f.handle(t, 0)
	if took, _, _ := g.takeOver("k", 0, 0); !took {
		t.Fatal("/now took nothing")
	}
	g.run(ctx, first)
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	if got := h.closes(); !reflect.DeepEqual(got, []string{SteerLaterAck}) {
		t.Fatalf("%q", got)
	}
}

// The marker keeps only the text of a queued message. So the restart text of a photo says
// that the photo is lost.
func TestBeginStoppingQueuedMediaAckSaysMediaIsLost(t *testing.T) {
	if QueueRestartMediaAck != "⏳ After the restart, the agent gets your message without its photo or file." {
		t.Fatal(QueueRestartMediaAck)
	}
	g, _ := gatewayWithSlowProvider(t, 0)
	heldKey(g)
	f := &ackRecFactory{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rest := g.acceptMessage(ctx, media("k"), f.new)
	h := f.handle(t, 0)
	g.BeginStopping(stopCtx(t))
	if got := h.closes(); !reflect.DeepEqual(got, []string{QueueRestartMediaAck}) {
		t.Fatalf("%q", got)
	}
	cancel()
	_ = rest()
}
