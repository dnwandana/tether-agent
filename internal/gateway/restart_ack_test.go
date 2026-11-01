package gateway

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestBeginStoppingClosesAcksWithRestartTexts(t *testing.T) {
	if SteerRestartAck != "↪️ Your note runs after the restart." ||
		QueueRestartAck != "⏳ Your message runs after the restart." {
		t.Fatalf("%q %q", SteerRestartAck, QueueRestartAck)
	}
	g, _ := gatewayWithSlowProvider(t, 0)
	first, _ := heldKey(g)
	f := &ackRecFactory{hold: make(chan struct{})}
	g.flushBusyText(context.Background(), inb("k", "note"), f.new)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rest := g.acceptMessage(ctx, media("k"), f.new)
	steer, queue := f.handle(t, 0), f.handle(t, 1)
	if got := f.sent(); !reflect.DeepEqual(got, []string{SteerAck, QueueAck}) {
		t.Fatalf("%q", got)
	}
	// The edits end after 100 ms, and the stop waits for them.
	time.AfterFunc(100*time.Millisecond, func() { close(f.hold) })
	start := time.Now()
	g.BeginStopping(stopCtx(t))
	if d := time.Since(start); d < 100*time.Millisecond {
		t.Fatal("the stop must wait for the edits:", d)
	}
	if got := steer.closes(); !reflect.DeepEqual(got, []string{"↪️ Your note runs after the restart."}) {
		t.Fatalf("%q", got)
	}
	// The queued message is a photo, and the marker keeps only its text.
	if got := queue.closes(); !reflect.DeepEqual(got, []string{QueueRestartMediaAck}) {
		t.Fatalf("%q", got)
	}
	// No later outcome edits the acks: the waiter leaves, and the turn ends.
	cancel()
	if err := rest(); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	endHeld(g, first)
	if len(steer.closes()) != 1 || len(queue.closes()) != 1 {
		t.Fatalf("%q %q", steer.closes(), queue.closes())
	}
}

// A run that /stop keep stopped does not save its steer in the marker, so its ack says
// "Not sent", not "runs after the restart".
func TestBeginStoppingStoppedRunSteerAckNotSent(t *testing.T) {
	g, _ := gatewayWithSlowProvider(t, 0)
	heldKey(g)
	f := &ackRecFactory{}
	g.flushBusyText(context.Background(), inb("k", "note"), f.new)
	h := f.handle(t, 0)
	if stopped, _ := g.stopKeepQueue("k"); !stopped {
		t.Fatal("no run")
	}
	g.BeginStopping(stopCtx(t))
	if got := h.closes(); !reflect.DeepEqual(got, []string{"⏹ Not sent. The task stopped."}) {
		t.Fatalf("%q", got)
	}
}

// /now moves the steer into the queue, and the marker saves the queue. So the ack of that
// steer says that the note runs after the restart.
func TestBeginStoppingAfterNowSteerAckRunsLater(t *testing.T) {
	g, _ := gatewayWithSlowProvider(t, 0)
	heldKey(g)
	f := &ackRecFactory{}
	g.flushBusyText(context.Background(), inb("k", "note"), f.new)
	h := f.handle(t, 0)
	if took, _, _ := g.takeOver("k", 0, 0); !took {
		t.Fatal("/now took nothing")
	}
	g.BeginStopping(stopCtx(t))
	if got := h.closes(); !reflect.DeepEqual(got, []string{SteerRestartAck}) {
		t.Fatalf("%q", got)
	}
}

func TestBeginStoppingWaitsAtMost2sForAckEdits(t *testing.T) {
	if restartAckWait != 2*time.Second {
		t.Fatal(restartAckWait)
	}
	restartAckWait = 300 * time.Millisecond
	t.Cleanup(func() { restartAckWait = 2 * time.Second })
	g, _ := gatewayWithSlowProvider(t, 0)
	heldKey(g)
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	f := &ackRecFactory{hold: hold}
	g.flushBusyText(context.Background(), inb("k", "note"), f.new)
	h := f.handle(t, 0)
	// The edit never ends in the test.
	start := time.Now()
	g.BeginStopping(stopCtx(t))
	if d := time.Since(start); d < 300*time.Millisecond || d > 1500*time.Millisecond {
		t.Fatal("the stop must wait for the edit, but at most restartAckWait:", d)
	}
	if got := h.closes(); !reflect.DeepEqual(got, []string{SteerRestartAck}) {
		t.Fatalf("%q", got)
	}
}
