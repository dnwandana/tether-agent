package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// flushMarkers waits until the marker writer of g ran every op that the test sent.
func flushMarkers(t *testing.T, g *Gateway) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := g.markerW().Flush(ctx); err != nil {
		t.Fatal(err)
	}
}

// markerOf returns the marker of key, or nil when the key has none.
func markerOf(t *testing.T, st *sessions.Store, key string) *sessions.TurnMarker {
	t.Helper()
	ms, err := st.Markers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := range ms {
		if ms[i].SessionKey == key {
			return &ms[i]
		}
	}
	return nil
}

// holdRenderer is a recRenderer whose Done waits for release, so the key stays busy after
// the end of the agent turn until the test releases it.
type holdRenderer struct {
	recRenderer
	release chan struct{}
}

func (r *holdRenderer) Done(final string, err error) {
	<-r.release
	r.recRenderer.Done(final, err)
}

func TestMarkerWhileBusyAndGoneWhenFree(t *testing.T) {
	ctx := context.Background()
	g, _ := gatewayWithSlowProvider(t, 200*time.Millisecond)
	f := &recFactory{}
	g.Submit(ctx, inb("k", "hi"), f.new, nil)
	waitFor(t, func() bool {
		m := markerOf(t, g.Deps.Store, "k")
		if m == nil {
			return false
		}
		sess, err := g.Deps.Store.Active(ctx, "k")
		return err == nil && sess != nil && m.SessionID == sess.ID && m.Resumes == 0
	})
	if !g.Wait(5 * time.Second) {
		t.Fatal("the turn did not end")
	}
	flushMarkers(t, g)
	if ms, err := g.Deps.Store.Markers(ctx); err != nil || len(ms) != 0 {
		t.Fatal(ms, err)
	}
}

func TestMarkerQueuedFollowsTheQueue(t *testing.T) {
	ctx := context.Background()
	g, _ := gatewayWithSlowProvider(t, time.Second)
	g.debounce = NewDebouncer(5*time.Millisecond, 20*time.Millisecond)
	f := &recFactory{}
	g.Submit(ctx, inb("k", "hi"), f.new, nil)
	waitFor(t, func() bool { _, ok := g.Running("k"); return ok })

	photo := inb("k", "look")
	photo.IsText = false
	photo.Images = []provider.Image{{MIME: "image/png", Data: []byte{1}}}
	g.Submit(ctx, photo, f.new, nil)
	// A known command gets the busy reply, and an unknown slash text waits in the queue.
	g.Submit(ctx, inb("k", "/model"), f.new, nil)
	g.Submit(ctx, inb("k", "/nosuch"), f.new, nil)
	// A plain text goes into the run as a steer.
	g.Submit(ctx, inb("k", "more"), f.new, nil)
	rs, _ := g.Running("k")
	waitFor(t, func() bool { return rs.Steer.Pending() != "" })
	// A message whose context ends leaves the queue.
	cctx, cancel := context.WithCancel(ctx)
	gone := inb("k", "gone")
	gone.IsText = false
	g.Submit(cctx, gone, f.new, nil)
	flushMarkers(t, g)
	want := []sessions.QueuedText{{Text: "look", Lost: 1}, {Text: "gone"}}
	if m := markerOf(t, g.Deps.Store, "k"); m == nil || !reflect.DeepEqual(m.Queued, want) {
		t.Fatalf("%+v", m)
	}
	cancel()
	want = want[:1]
	waitFor(t, func() bool {
		flushMarkers(t, g)
		m := markerOf(t, g.Deps.Store, "k")
		return m != nil && reflect.DeepEqual(m.Queued, want)
	})
	if !g.Wait(10 * time.Second) {
		t.Fatal("the turns did not end")
	}
	flushMarkers(t, g)
	if m := markerOf(t, g.Deps.Store, "k"); m != nil {
		t.Fatalf("%+v", m)
	}
}

func TestMarkerEnqueueFollowsTheQueue(t *testing.T) {
	ctx := context.Background()
	g, _ := gatewayWithSlowProvider(t, 500*time.Millisecond)
	f := &recFactory{}
	g.Submit(ctx, inb("k", "hi"), f.new, nil)
	waitFor(t, func() bool { _, ok := g.Running("k"); return ok })
	if err := g.Enqueue(ctx, "k", "follow-up"); err != nil {
		t.Fatal(err)
	}
	flushMarkers(t, g)
	want := []sessions.QueuedText{{Text: "follow-up"}}
	if m := markerOf(t, g.Deps.Store, "k"); m == nil || !reflect.DeepEqual(m.Queued, want) {
		t.Fatalf("%+v", m)
	}
	if !g.Wait(10 * time.Second) {
		t.Fatal("the turns did not end")
	}
	flushMarkers(t, g)
	if m := markerOf(t, g.Deps.Store, "k"); m != nil {
		t.Fatalf("%+v", m)
	}
}

func TestMarkerRemovedByStopAndNew(t *testing.T) {
	for _, cmd := range []string{"/stop", "/stop keep", "/new"} {
		t.Run(cmd, func(t *testing.T) {
			ctx := context.Background()
			g, _ := gatewayWithSlowProvider(t, 5*time.Second)
			g.Deps.BeforeNew = func(context.Context, *sessions.Session) {}
			hold := &holdRenderer{release: make(chan struct{})}
			g.Submit(ctx, inb("k", "hi"), func() Renderer { return hold }, nil)
			waitFor(t, func() bool {
				flushMarkers(t, g)
				return markerOf(t, g.Deps.Store, "k") != nil
			})
			f := &recFactory{}
			if err := g.Handle(ctx, inb("k", cmd), f.new); err != nil {
				t.Fatal(err)
			}
			flushMarkers(t, g)
			// The renderer still holds the turn, so the key is busy, but the marker is gone.
			if len(g.BusyKeys()) != 1 {
				t.Fatal(g.BusyKeys())
			}
			if m := markerOf(t, g.Deps.Store, "k"); m != nil {
				t.Fatalf("%+v", m)
			}
			close(hold.release)
			if !g.Wait(5 * time.Second) {
				t.Fatal("the turn did not end")
			}
			flushMarkers(t, g)
			if m := markerOf(t, g.Deps.Store, "k"); m != nil {
				t.Fatalf("%+v", m)
			}
		})
	}
}

func TestMarkerWriteErrorKeepsTheTurn(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	fp := &fakeProv{delay: 20 * time.Millisecond}
	deps := providerDeps(t, sessions.New(d), fp)
	var logs syncBuffer
	deps.Log = slog.New(slog.NewTextHandler(&logs, nil))
	g := &Gateway{Deps: deps}
	if _, err := deps.Store.ActiveOrCreate(ctx, "k", sessions.Session{Source: "telegram", ChatID: "1", Kind: "chat"}); err != nil {
		t.Fatal(err)
	}
	// Each marker write fails from now on. The other tables still work.
	if _, err := d.ExecContext(ctx, `DROP TABLE turn_markers`); err != nil {
		t.Fatal(err)
	}
	f := &recFactory{}
	if err := g.Handle(ctx, inb("k", "hi"), f.new); err != nil {
		t.Fatal(err)
	}
	if got := f.finals(); len(got) != 1 || got[0] != "ok" {
		t.Fatal(got)
	}
	flushMarkers(t, g)
	out := logs.String()
	if !strings.Contains(out, "gateway: cannot save the turn marker") || !strings.Contains(out, "key=k") {
		t.Fatal(out)
	}
}

func newTestMarkerWriter(t *testing.T) (*markerWriter, *sessions.Store) {
	t.Helper()
	st := sessions.New(openTestDB(t))
	return newMarkerWriter(st, slog.New(slog.DiscardHandler)), st
}

func TestMarkerWriterOrderFlushFreeze(t *testing.T) {
	ctx := context.Background()
	w, st := newTestMarkerWriter(t)
	flush := func() {
		t.Helper()
		if err := w.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}
	w.upsert("k", "s", nil)
	w.remove("k")
	w.upsert("k", "s", nil)
	flush()
	if ms, _ := st.Markers(ctx); len(ms) != 1 || ms[0].SessionKey != "k" || ms[0].SessionID != "s" {
		t.Fatal(ms)
	}
	w.touch("k")
	w.remove("k")
	flush()
	if ms, _ := st.Markers(ctx); len(ms) != 0 {
		t.Fatal(ms)
	}
	w.Freeze()
	w.upsert("k", "s", nil)
	flush()
	if ms, _ := st.Markers(ctx); len(ms) != 0 {
		t.Fatal(ms)
	}
}

func TestMarkerWriterSkipsAnOldOpOfAKey(t *testing.T) {
	ctx := context.Background()
	w, st := newTestMarkerWriter(t)
	// The gateway sends its ops after the unlock, so a newer op can arrive first.
	w.send(markerOp{kind: markerRemove, key: "k", seq: 2})
	w.send(markerOp{kind: markerUpsert, key: "k", sessionID: "s", seq: 1})
	w.send(markerOp{kind: markerUpsert, key: "j", sessionID: "s", seq: 1})
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if ms, _ := st.Markers(ctx); len(ms) != 1 || ms[0].SessionKey != "j" {
		t.Fatal(ms)
	}
}

func TestMarkerWriterStopsWhenIdle(t *testing.T) {
	w, _ := newTestMarkerWriter(t)
	w.upsert("k", "s", nil)
	if err := w.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return !w.running
	})
}

func TestMarkerWriterFullChannelWhileIdle(t *testing.T) {
	// Many senders fill a small channel again and again while the goroutine stops and
	// starts. No sender and no Flush may hang.
	st := sessions.New(openTestDB(t))
	w := newMarkerWriterSize(st, slog.New(slog.DiscardHandler), 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for round := 0; round < 20; round++ {
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 4; j++ {
					w.touch(fmt.Sprintf("k%d", i))
				}
			}()
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("a send hangs in round", round)
		}
		if err := w.Flush(ctx); err != nil {
			t.Fatal("Flush hangs in round", round, err)
		}
	}
	w.upsert("k", "s", nil)
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if ms, _ := st.Markers(context.Background()); len(ms) != 1 {
		t.Fatal(ms)
	}
}

func TestMarkerFlushStopsAtTheContextEnd(t *testing.T) {
	// No goroutine reads the unbuffered channel, so the flush op cannot go in.
	w := newMarkerWriterSize(nil, slog.New(slog.DiscardHandler), 0)
	w.running = true
	t.Cleanup(func() { <-w.ch })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Flush(ctx); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestMarkerHooksWithNoStore(t *testing.T) {
	g := &Gateway{Deps: &Deps{}}
	if g.markerW() != nil {
		t.Fatal("no store must give no writer")
	}
	var w *markerWriter
	w.upsert("k", "s", nil)
	w.touch("k")
	w.remove("k")
	w.Freeze()
	if err := w.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestQueuedTexts(t *testing.T) {
	doc := inb("k", "read this")
	doc.Files = []string{"/a.pdf", "/b.pdf"}
	photo := inb("k", "")
	photo.Images = []provider.Image{{}}
	q := []*pendingTurn{{in: inb("k", "/model x")}, {in: doc}, {in: photo}, {in: inb("k", "plain")}}
	want := []sessions.QueuedText{{Text: "read this", Lost: 2}, {Text: "", Lost: 1}, {Text: "plain"}}
	if got := queuedTexts(q); !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}
