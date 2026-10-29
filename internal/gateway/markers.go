package gateway

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/sessions"
)

// markerQueueSize is the capacity of the op channel of a markerWriter.
const markerQueueSize = 256

// markerKind is the kind of one markerOp.
type markerKind int

const (
	markerUpsert markerKind = iota + 1
	markerTouch
	markerRemove
	markerFlush
)

// markerOp is one write of a turn marker, or a flush.
type markerOp struct {
	kind           markerKind
	key, sessionID string
	queued         []sessions.QueuedText
	// at is the time of the op. send sets it, so a slow write keeps the time of the event.
	at time.Time
	// seq orders the ops of one key. The gateway takes it under Gateway.mu but sends the op
	// after the unlock, so a newer op can arrive first. The writer skips an op whose seq is
	// lower than the last seq that it ran for the key. Zero skips no op.
	//
	// The direct methods (upsert, touch, remove) send seq 0. Such an op has no order against
	// the hook ops of the same key from other goroutines. A caller that needs this order
	// must take a seq under Gateway.mu, like the hooks.
	seq uint64
	// done is closed when the writer reaches a flush op.
	done chan struct{}
}

// markerWriter writes the turn markers of a Gateway in one goroutine, in the order of the
// ops. The goroutine starts with the first op and stops when no op is pending, so an idle
// writer holds no goroutine. A nil writer drops every op.
type markerWriter struct {
	store *sessions.Store
	log   *slog.Logger
	ch    chan markerOp

	// mu guards running, frozen, bounds, and pending. Nobody holds mu during a channel
	// operation, so a full channel cannot block the goroutine.
	mu      sync.Mutex
	running bool
	frozen  bool
	// bounds holds the last seq per key that a frozen writer still accepts.
	bounds map[string]uint64
	// pending counts the ops that a send accepted and the goroutine did not finish yet. The
	// goroutine stops only when pending is zero, so an accepted op always has a goroutine.
	pending int
	// last holds the seq of the last op that ran for each key. Only the goroutine uses it.
	last map[string]uint64
}

func newMarkerWriter(store *sessions.Store, log *slog.Logger) *markerWriter {
	return newMarkerWriterSize(store, log, markerQueueSize)
}

// newMarkerWriterSize returns a writer whose channel holds size ops. Tests use a small size.
func newMarkerWriterSize(store *sessions.Store, log *slog.Logger, size int) *markerWriter {
	return &markerWriter{store: store, log: log, ch: make(chan markerOp, size), last: map[string]uint64{}}
}

// markerW returns the marker writer of g. It returns nil when g has no store, and then
// every marker op is dropped.
func (g *Gateway) markerW() *markerWriter {
	if g.Deps == nil || g.Deps.Store == nil {
		return nil
	}
	g.markerOnce.Do(func() { g.markers = newMarkerWriter(g.Deps.Store, g.logger()) })
	return g.markers
}

// upsert writes the marker of key with its session and its queued texts. The direct methods
// send seq 0, so they have no order against the hook ops of key from other goroutines.
func (w *markerWriter) upsert(key, sessionID string, queued []sessions.QueuedText) {
	w.send(markerOp{kind: markerUpsert, key: key, sessionID: sessionID, queued: queued})
}

// touch sets the time of the marker of key.
func (w *markerWriter) touch(key string) { w.send(markerOp{kind: markerTouch, key: key}) }

// remove deletes the marker of key.
func (w *markerWriter) remove(key string) { w.send(markerOp{kind: markerRemove, key: key}) }

// send accepts op, starts the goroutine when it does not run, and puts op into the
// channel. After a freeze, it drops each write that the freeze does not accept. It blocks
// while the channel is full, so the caller must not hold Gateway.mu.
func (w *markerWriter) send(op markerOp) {
	if w == nil {
		return
	}
	if op.at.IsZero() {
		op.at = time.Now()
	}
	w.mu.Lock()
	if w.frozen && op.kind != markerFlush && (op.seq == 0 || op.seq > w.bounds[op.key]) {
		w.mu.Unlock()
		return
	}
	w.pending++
	if !w.running {
		w.running = true
		go w.loop()
	}
	w.mu.Unlock()
	// The goroutine does not stop while pending counts this op, so the send cannot hang.
	w.ch <- op
}

// Flush returns when the writer ran every op that was sent before the call, or at the end
// of ctx with its error.
func (w *markerWriter) Flush(ctx context.Context) error {
	if w == nil {
		return nil
	}
	done := make(chan struct{})
	// The send can wait for space in the channel, so it does not block the ctx check. After
	// the end of ctx, this goroutine stays until the channel has space.
	go w.send(markerOp{kind: markerFlush, done: done})
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Freeze makes the writer drop every write that is sent after the call. The writes that
// were sent before still run.
func (w *markerWriter) Freeze() { w.FreezeAt(nil) }

// FreezeAt makes the writer drop the writes that are sent after the call, except a hook op
// whose seq is not above bounds[key]. Such an op was computed before the freeze, so it
// still runs, for example the remove of a turn that ended at the stop. A direct op (seq 0)
// is always dropped. The writes that were sent before the call still run.
func (w *markerWriter) FreezeAt(bounds map[string]uint64) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.frozen, w.bounds = true, bounds
	w.mu.Unlock()
}

// loop runs the ops until no op is pending. An op that send accepted is in the channel or
// goes in soon, so the receive does not wait forever.
func (w *markerWriter) loop() {
	for {
		w.mu.Lock()
		if w.pending == 0 {
			w.running = false
			w.mu.Unlock()
			return
		}
		w.mu.Unlock()
		w.run(<-w.ch)
		w.mu.Lock()
		w.pending--
		w.mu.Unlock()
	}
}

// run runs one op. A write does not use the context of a turn, because the end of the turn
// must not stop the write. A store error is logged, and it never changes a turn.
func (w *markerWriter) run(op markerOp) {
	if op.kind == markerFlush {
		close(op.done)
		return
	}
	if op.seq != 0 {
		if op.seq < w.last[op.key] {
			return
		}
		w.last[op.key] = op.seq
	}
	ctx := context.Background()
	var err error
	switch op.kind {
	case markerUpsert:
		err = w.store.UpsertMarker(ctx, op.key, op.sessionID, op.queued, op.at)
	case markerTouch:
		err = w.store.TouchMarker(ctx, op.key, op.at)
	case markerRemove:
		err = w.store.DeleteMarker(ctx, op.key)
	}
	if err != nil {
		w.log.Warn("gateway: cannot save the turn marker", "key", op.key, "err", err)
	}
}

// queuedTexts returns the texts of the queued messages for a marker. A slash command is
// left out, because the queue drops it. Lost counts the images and the files of a message,
// because the marker keeps the text only.
func queuedTexts(q []*pendingTurn) []sessions.QueuedText {
	var out []sessions.QueuedText
	for _, p := range q {
		if isCommandText(p.in) {
			continue
		}
		out = append(out, sessions.QueuedText{Text: p.in.Text, Lost: len(p.in.Images) + len(p.in.Files)})
	}
	return out
}

// markerUpsertOp returns the op that writes the marker of key with the current queue. It
// returns a zero op when the turn of the key has no session yet: that turn writes the
// marker when it starts. It also returns a zero op while the gateway stops, because
// BeginStopping wrote the last marker of each key. The caller holds Gateway.mu and sends
// the op after the unlock.
func (g *Gateway) markerUpsertOp(ks *keyState, key string) markerOp {
	if ks.markerSession == "" || g.stopping.Load() {
		return markerOp{}
	}
	ks.markerSeq++
	return markerOp{kind: markerUpsert, key: key, sessionID: ks.markerSession, queued: queuedTexts(ks.queue), seq: ks.markerSeq}
}

// markerRemoveOp returns the op that deletes the marker of key, and it forgets the marker
// session. While the gateway stops, it returns a zero op, so the marker stays. The caller
// holds Gateway.mu and sends the op after the unlock.
func (g *Gateway) markerRemoveOp(ks *keyState, key string) markerOp {
	ks.markerSession = ""
	if g.stopping.Load() {
		return markerOp{}
	}
	ks.markerSeq++
	return markerOp{kind: markerRemove, key: key, seq: ks.markerSeq}
}

// sendMarker sends op to the marker writer. A zero op does nothing. The caller must not
// hold g.mu.
func (g *Gateway) sendMarker(op markerOp) {
	if op.kind == 0 {
		return
	}
	g.markerW().send(op)
}
