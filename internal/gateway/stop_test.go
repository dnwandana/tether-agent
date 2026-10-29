package gateway

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

func TestStopNoticeTexts(t *testing.T) {
	for _, c := range []struct {
		reason string
		d      time.Duration
		want   string
	}{
		{"signal", 180 * time.Second, "⚠️ The gateway is stopping. I will try to finish this reply first, for up to 3 min. If I cannot, I will continue when the gateway is back."},
		{"restart", 180 * time.Second, "⚠️ The gateway is restarting. I will try to finish this reply first, for up to 3 min. If I cannot, I will continue after the restart."},
		{"reload", 45 * time.Second, "⚠️ The gateway is restarting. I will try to finish this reply first, for up to 45 s. If I cannot, I will continue after the restart."},
		{"signal", 0, "⚠️ The gateway is stopping. I must stop this reply now. I will continue when the gateway is back."},
		{"restart", 0, "⚠️ The gateway is restarting. I must stop this reply now. I will continue after the restart."},
		{"other", 60 * time.Second, "⚠️ The gateway is stopping. I will try to finish this reply first, for up to 1 min. If I cannot, I will continue when the gateway is back."},
	} {
		if got := StopNotice(c.reason, c.d); got != c.want {
			t.Fatalf("%s %v:\n%s", c.reason, c.d, got)
		}
	}
}

func TestFormatDrain(t *testing.T) {
	for d, want := range map[time.Duration]string{
		60 * time.Second: "1 min", 180 * time.Second: "3 min", 90 * time.Second: "90 s", 45 * time.Second: "45 s",
	} {
		if got := FormatDrain(d); got != want {
			t.Fatal(d, got)
		}
	}
}

// failFirstChannel fails its first Send and records the others.
type failFirstChannel struct {
	mu    sync.Mutex
	calls int
	to    []Target
}

func (c *failFirstChannel) Name() string                        { return "telegram" }
func (c *failFirstChannel) Run(context.Context, *Gateway) error { return nil }
func (c *failFirstChannel) Send(_ context.Context, to Target, _ Outbound) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls == 1 {
		return errors.New("boom")
	}
	c.to = append(c.to, to)
	return nil
}

func TestNotifyStoppingOnlyBusyKeys(t *testing.T) {
	ctx := context.Background()
	g, _ := gatewayWithSlowProvider(t, 300*time.Millisecond)
	ch := &recChannel{sent: make(chan Outbound, 4)}
	g.Channels = map[string]Channel{"telegram": ch}
	f := &recFactory{}
	for _, k := range []string{"A", "B"} {
		in := inb(k, "hi")
		in.ChatID = k
		in.Target = Target{Source: "telegram", ChatID: k}
		g.Submit(ctx, in, f.new, nil)
	}
	waitFor(t, func() bool { return len(g.BusyKeys()) == 2 })
	g.mu.Lock()
	g.key("C") // an idle key
	g.mu.Unlock()
	if got := g.BusyKeys(); len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Fatal(got)
	}
	g.NotifyStopping(ctx, "x", "B")
	select {
	case m := <-ch.sent:
		if m.Text != "x" || ch.to.ChatID != "A" {
			t.Fatal(m, ch.to)
		}
	case <-time.After(time.Second):
		t.Fatal("no notice")
	}
	select {
	case m := <-ch.sent:
		t.Fatal("one notice only:", m)
	default:
	}
	waitFor(t, func() bool { return len(g.BusyKeys()) == 0 })
}

func TestNotifyStoppingContinuesAfterSendError(t *testing.T) {
	ctx := context.Background()
	g, _ := gatewayWithSlowProvider(t, 300*time.Millisecond)
	ch := &failFirstChannel{}
	g.Channels = map[string]Channel{"telegram": ch}
	f := &recFactory{}
	for _, k := range []string{"A", "B"} {
		in := inb(k, "hi")
		in.ChatID = k
		in.Target = Target{Source: "telegram", ChatID: k}
		g.Submit(ctx, in, f.new, nil)
	}
	waitFor(t, func() bool { return len(g.BusyKeys()) == 2 })
	g.NotifyStopping(ctx, "x", "")
	ch.mu.Lock()
	calls, ok := ch.calls, len(ch.to) == 1 && ch.to[0].ChatID == "B"
	ch.mu.Unlock()
	if calls != 2 || !ok {
		t.Fatal(calls, ch.to)
	}
	waitFor(t, func() bool { return len(g.BusyKeys()) == 0 })
}

// stopCtx returns a context that bounds the Flush of BeginStopping in a test.
func stopCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// photoIn returns a message of key with one image and the caption text.
func photoIn(key, text string) Inbound {
	in := inb(key, text)
	in.IsText = false
	in.Images = []provider.Image{{MIME: "image/png", Data: []byte{1}}}
	return in
}

// requests returns the number of model requests of fp.
func requests(fp *fakeProv) int { return len(fp.lastUserTexts()) }

// wantMarker fails the test when the marker of key does not hold want, or when its
// session is not the active session of key.
func wantMarker(t *testing.T, g *Gateway, key string, want []sessions.QueuedText) {
	t.Helper()
	m := markerOf(t, g.Deps.Store, key)
	if m == nil || !reflect.DeepEqual(m.Queued, want) {
		t.Fatalf("marker %+v, want queued %+v", m, want)
	}
	sess, err := g.Deps.Store.Active(context.Background(), key)
	if err != nil || sess == nil || sess.ID != m.SessionID {
		t.Fatal(sess, err, m.SessionID)
	}
}

// gateFactory makes recRenderers. The call with number block (from 1) waits until release
// is closed, and entered is closed when that call starts.
type gateFactory struct {
	recFactory
	block            int
	entered, release chan struct{}
	calls            int
}

func newGateFactory(block int) *gateFactory {
	return &gateFactory{block: block, entered: make(chan struct{}), release: make(chan struct{})}
}

func (f *gateFactory) new() Renderer {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	if n == f.block {
		close(f.entered)
		<-f.release
	}
	return f.recFactory.new()
}

// doneGate is a renderer whose Done closes entered and then waits until release is closed.
type doneGate struct {
	recRenderer
	entered, release chan struct{}
}

func (r *doneGate) Done(final string, err error) {
	close(r.entered)
	<-r.release
	r.recRenderer.Done(final, err)
}

func TestBeginStoppingKeepsTheCutWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g, fp := gatewayWithSlowProvider(t, 5*time.Second)
	f := &recFactory{}
	g.Submit(ctx, inb("k", "first"), f.new, nil)
	waitFor(t, func() bool { return requests(fp) == 1 })
	rs, _ := g.Running("k")
	rs.Steer.Push("steer me")
	g.Submit(ctx, photoIn("k", "look"), f.new, nil)
	g.BeginStopping(stopCtx(t))
	want := []sessions.QueuedText{{Text: "steer me"}, {Text: "look", Lost: 1}}
	wantMarker(t, g, "k", want)
	// stopWork cancels the turns.
	cancel()
	if !g.Wait(5 * time.Second) {
		t.Fatal("the turns did not end")
	}
	flushMarkers(t, g)
	wantMarker(t, g, "k", want)
	if n := requests(fp); n != 1 {
		t.Fatal("no queued turn may start after the stop:", n)
	}
}

func TestBeginStoppingTakesTheDebouncedText(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g, fp := gatewayWithSlowProvider(t, 5*time.Second)
	g.debounce = NewDebouncer(time.Hour, time.Hour)
	f := &recFactory{}
	g.Submit(ctx, inb("k", "first"), f.new, nil)
	waitFor(t, func() bool { return requests(fp) == 1 })
	g.Submit(ctx, inb("k", "late"), f.new, nil)
	g.Submit(ctx, photoIn("k", "look"), f.new, nil)
	g.BeginStopping(stopCtx(t))
	wantMarker(t, g, "k", []sessions.QueuedText{{Text: "late"}, {Text: "look", Lost: 1}})
	if got := g.debouncer().Take("k"); got != "" {
		t.Fatal("the stop must take the text from the debouncer:", got)
	}
	cancel()
	if !g.Wait(5 * time.Second) {
		t.Fatal("the turns did not end")
	}
}

func TestBeginStoppingPutsTheSteerBeforeTheDebouncedText(t *testing.T) {
	// The debouncer holds the newest texts. A steer text went through it before.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g, fp := gatewayWithSlowProvider(t, 5*time.Second)
	g.debounce = NewDebouncer(time.Hour, time.Hour)
	f := &recFactory{}
	g.Submit(ctx, inb("k", "first"), f.new, nil)
	waitFor(t, func() bool { return requests(fp) == 1 })
	rs, _ := g.Running("k")
	rs.Steer.Push("early")
	g.Submit(ctx, inb("k", "late"), f.new, nil)
	g.BeginStopping(stopCtx(t))
	wantMarker(t, g, "k", []sessions.QueuedText{{Text: "early"}, {Text: "late"}})
	cancel()
	if !g.Wait(5 * time.Second) {
		t.Fatal("the turns did not end")
	}
}

func TestStoppingRefusesNewWork(t *testing.T) {
	ctx := context.Background()
	g, fp := gatewayWithSlowProvider(t, 0)
	g.BeginStopping(stopCtx(t))
	if err := g.Enqueue(ctx, "k", "follow-up"); !errors.Is(err, ErrStopping) {
		t.Fatal(err)
	}
	f := &recFactory{}
	got := make(chan error, 1)
	g.Submit(ctx, inb("k", "hi"), f.new, func(err error) { got <- err })
	select {
	case err := <-got:
		if !errors.Is(err, ErrStopping) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("done was not called")
	}
	// A nil done must not panic.
	g.Submit(ctx, inb("k", "hi"), f.new, nil)
	if err := g.Handle(ctx, inb("k", "hi"), f.new); !errors.Is(err, ErrStopping) {
		t.Fatal(err)
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	if n := requests(fp); n != 0 || len(f.finals()) != 0 || len(g.BusyKeys()) != 0 {
		t.Fatal(n, f.finals(), g.BusyKeys())
	}
	if ErrStopping.Error() != "gateway: the gateway is stopping, so it takes no new work" {
		t.Fatal(ErrStopping)
	}
}

func TestQueuedWaiterLeavingDoesNotEraseTheMarker(t *testing.T) {
	// The turn ends by itself after the stop. next does not give the key to the waiter,
	// and the waiter leaves at the end of its context.
	g, fp := gatewayWithSlowProvider(t, 300*time.Millisecond)
	f := &recFactory{}
	g.Submit(context.Background(), inb("k", "first"), f.new, nil)
	waitFor(t, func() bool { return requests(fp) == 1 })
	wctx, wcancel := context.WithCancel(context.Background())
	defer wcancel()
	left := make(chan error, 1)
	g.Submit(wctx, photoIn("k", "look"), f.new, func(err error) { left <- err })
	g.BeginStopping(stopCtx(t))
	want := []sessions.QueuedText{{Text: "look", Lost: 1}}
	wantMarker(t, g, "k", want)
	waitFor(t, func() bool { return len(f.finals()) == 1 })
	wcancel()
	select {
	case err := <-left:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter did not leave")
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("the turns did not end")
	}
	flushMarkers(t, g)
	wantMarker(t, g, "k", want)
	if n := requests(fp); n != 1 || len(g.BusyKeys()) != 1 {
		t.Fatal(n, g.BusyKeys())
	}
}

func TestBeginStoppingKeepsAHandedMessage(t *testing.T) {
	// The first turn ends and gives the key to the queued photo. The stop comes before
	// the turn of the photo starts its agent, so the photo is in the marker and runs no
	// agent.
	g, fp := gatewayWithSlowProvider(t, 300*time.Millisecond)
	f := &recFactory{}
	g.Submit(context.Background(), inb("k", "first"), f.new, nil)
	waitFor(t, func() bool { return requests(fp) == 1 })
	// The first call is the queue ack, and the second call starts the turn of the photo.
	pf := newGateFactory(2)
	g.Submit(context.Background(), photoIn("k", "look"), pf.new, nil)
	<-pf.entered
	g.BeginStopping(stopCtx(t))
	want := []sessions.QueuedText{{Text: "look", Lost: 1}}
	wantMarker(t, g, "k", want)
	close(pf.release)
	if !g.Wait(5 * time.Second) {
		t.Fatal("the turns did not end")
	}
	flushMarkers(t, g)
	wantMarker(t, g, "k", want)
	if n := requests(fp); n != 1 {
		t.Fatal("the handed message must run no agent:", n)
	}
}

func TestBeginStoppingKeepsAFirstMessageWithNoSession(t *testing.T) {
	// The first message of a key has no session and no marker yet. The stop gets the
	// session, so the message is in the marker.
	g, fp := gatewayWithSlowProvider(t, 0)
	pf := newGateFactory(1)
	g.Submit(context.Background(), inb("k", "hello"), pf.new, nil)
	<-pf.entered
	g.BeginStopping(stopCtx(t))
	want := []sessions.QueuedText{{Text: "hello"}}
	wantMarker(t, g, "k", want)
	close(pf.release)
	if !g.Wait(5 * time.Second) {
		t.Fatal("the turn did not end")
	}
	if n := requests(fp); n != 0 {
		t.Fatal("the message must run no agent:", n)
	}
	flushMarkers(t, g)
	wantMarker(t, g, "k", want)
}

func TestBeginStoppingKeepsTheUndeliveredSteer(t *testing.T) {
	// The turn ended with a steer that it did not deliver, and its answer is still on the
	// way. The stop saves the steer.
	g, fp := gatewayWithSlowProvider(t, 300*time.Millisecond)
	r := &doneGate{entered: make(chan struct{}), release: make(chan struct{})}
	g.Submit(context.Background(), inb("k", "first"), func() Renderer { return r }, nil)
	waitFor(t, func() bool { return requests(fp) == 1 })
	rs, _ := g.Running("k")
	rs.Steer.Push("steer me")
	<-r.entered
	g.BeginStopping(stopCtx(t))
	want := []sessions.QueuedText{{Text: "steer me"}}
	wantMarker(t, g, "k", want)
	close(r.release)
	if !g.Wait(5 * time.Second) {
		t.Fatal("the turns did not end")
	}
	flushMarkers(t, g)
	wantMarker(t, g, "k", want)
	if n := requests(fp); n != 1 {
		t.Fatal("the steer must not become a turn after the stop:", n)
	}
}

func TestStopStillDropsTheSteer(t *testing.T) {
	ctx := context.Background()
	g, fp := gatewayWithSlowProvider(t, 5*time.Second)
	f := &recFactory{}
	g.Submit(ctx, inb("k", "first"), f.new, nil)
	waitFor(t, func() bool { return requests(fp) == 1 })
	rs, _ := g.Running("k")
	rs.Steer.Push("steer me")
	if err := g.Handle(ctx, inb("k", "/stop"), f.new); err != nil {
		t.Fatal(err)
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("the turn did not end")
	}
	flushMarkers(t, g)
	if m := markerOf(t, g.Deps.Store, "k"); m != nil {
		t.Fatalf("%+v", m)
	}
	if n := requests(fp); n != 1 {
		t.Fatal("the steer must not become a turn:", n)
	}
}

func TestStopStartedTouchesBusyMarkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	g, fp := gatewayWithSlowProvider(t, 5*time.Second)
	f := &recFactory{}
	g.Submit(ctx, inb("k", "first"), f.new, nil)
	waitFor(t, func() bool { return requests(fp) == 1 })
	flushMarkers(t, g)
	old := time.Unix(1000, 0)
	if err := g.Deps.Store.TouchMarker(context.Background(), "k", old); err != nil {
		t.Fatal(err)
	}
	g.StopStarted()
	flushMarkers(t, g)
	if m := markerOf(t, g.Deps.Store, "k"); m == nil || time.Since(m.UpdatedAt) > time.Minute {
		t.Fatalf("%+v", m)
	}
	cancel()
	if !g.Wait(5 * time.Second) {
		t.Fatal("the turn did not end")
	}
}

func TestFreezeKeepsAnEarlierRemove(t *testing.T) {
	// The turn of k ends at the stop: next computes the remove before the freeze and sends
	// it after the freeze. The remove still runs.
	ctx := context.Background()
	g, _ := gatewayWithSlowProvider(t, 0)
	g.mu.Lock()
	ks := g.key("k")
	ks.busy, ks.markerSession = true, "s"
	up := g.markerUpsertOp(ks, "k")
	g.mu.Unlock()
	g.sendMarker(up)
	flushMarkers(t, g)
	if markerOf(t, g.Deps.Store, "k") == nil {
		t.Fatal("no marker")
	}
	g.mu.Lock()
	ks.busy = false
	rm := g.markerRemoveOp(ks, "k")
	g.mu.Unlock()
	g.BeginStopping(stopCtx(t))
	g.sendMarker(rm)
	flushMarkers(t, g)
	if m := markerOf(t, g.Deps.Store, "k"); m != nil {
		t.Fatalf("%+v", m)
	}
	// A direct op and a hook op with a newer seq are dropped after the freeze.
	w := g.markerW()
	w.upsert("k", "s", nil)
	w.send(markerOp{kind: markerUpsert, key: "k", sessionID: "s", seq: rm.seq + 1})
	w.send(markerOp{kind: markerUpsert, key: "j", sessionID: "s", seq: 1})
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if ms, _ := g.Deps.Store.Markers(ctx); len(ms) != 0 {
		t.Fatal(ms)
	}
}

func TestMarkerWriterFreezeAt(t *testing.T) {
	ctx := context.Background()
	w, st := newTestMarkerWriter(t)
	w.FreezeAt(map[string]uint64{"k": 3})
	w.send(markerOp{kind: markerUpsert, key: "k", sessionID: "s", seq: 3})
	w.send(markerOp{kind: markerUpsert, key: "a", sessionID: "s", seq: 1})
	w.send(markerOp{kind: markerUpsert, key: "b", sessionID: "s", seq: 4})
	w.upsert("c", "s", nil)
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if ms, _ := st.Markers(ctx); len(ms) != 1 || ms[0].SessionKey != "k" {
		t.Fatal(ms)
	}
	w.send(markerOp{kind: markerUpsert, key: "k", sessionID: "s", seq: 4})
	w.send(markerOp{kind: markerRemove, key: "k", seq: 2})
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	// seq 4 is above the bound, and seq 2 is older than the op that ran.
	if ms, _ := st.Markers(ctx); len(ms) != 1 || ms[0].SessionKey != "k" {
		t.Fatal(ms)
	}
}

func TestDebouncerTake(t *testing.T) {
	d := NewDebouncer(20*time.Millisecond, 50*time.Millisecond)
	ran := make(chan string, 1)
	f := func(merged string) { ran <- merged }
	d.Add("k", "a", f)
	d.Add("k", "b", f)
	if got := d.Take("k"); got != "a\nb" {
		t.Fatal(got)
	}
	if got := d.Take("other"); got != "" {
		t.Fatal(got)
	}
	select {
	case m := <-ran:
		t.Fatal("flush ran:", m)
	case <-time.After(150 * time.Millisecond):
	}
}
