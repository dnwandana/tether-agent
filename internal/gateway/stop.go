package gateway

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/dnwandana/tether-agent/internal/sessions"
)

// FormatDrain returns d for a stop notice: "3 min" for a whole number of minutes, else
// a number of seconds, for example "45 s".
func FormatDrain(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%d min", int(d/time.Minute))
	}
	return fmt.Sprintf("%d s", int(d/time.Second))
}

// StopNotice returns the text that the gateway sends to each busy chat when it stops.
// reason is "signal", "reload", or "restart". An unknown reason counts as "signal". A
// drain time of zero or less means that the turn must stop now.
func StopNotice(reason string, drain time.Duration) string {
	head, tail := "The gateway is stopping.", "when the gateway is back."
	if reason == "restart" || reason == "reload" {
		head, tail = "The gateway is restarting.", "after the restart."
	}
	if drain <= 0 {
		return "⚠️ " + head + " I must stop this reply now. I will continue " + tail
	}
	return "⚠️ " + head + " I will try to finish this reply first, for up to " + FormatDrain(drain) +
		". If I cannot, I will continue " + tail
}

// BusyKeys returns the keys that have a turn or a queued message, sorted.
func (g *Gateway) BusyKeys() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var keys []string
	for k, ks := range g.keys {
		if ks.busy {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// NotifyStopping sends text as a new message to the chat of each busy key, except skipKey.
// Hermes sends the same notice before it waits for the running turns. A failed send is
// logged, and the other keys still get the notice.
func (g *Gateway) NotifyStopping(ctx context.Context, text, skipKey string) {
	type dest struct {
		key string
		ch  Channel
		to  Target
	}
	var dests []dest
	var lookup []string
	// Collect the targets under the lock. Do not send and do not read the store here.
	g.mu.Lock()
	for key, ks := range g.keys {
		if !ks.busy || key == skipKey {
			continue
		}
		if ks.current == nil {
			lookup = append(lookup, key)
			continue
		}
		in := ks.current.in
		to := in.Target
		if to == (Target{}) {
			to = Target{Source: in.Source, ChatID: in.ChatID, ThreadID: in.ThreadID}
		}
		if ch, ok := g.Channels[in.Source]; ok {
			dests = append(dests, dest{key, ch, to})
		}
	}
	g.mu.Unlock()
	for _, key := range lookup {
		in, _, err := g.sendRenderer(ctx, key)
		if err != nil {
			g.logger().Warn("gateway: cannot send the stop notice", "key", key, "err", err)
			continue
		}
		dests = append(dests, dest{key, g.Channels[in.Source], in.Target})
	}
	sort.Slice(dests, func(i, j int) bool { return dests[i].key < dests[j].key })
	for _, d := range dests {
		if err := d.ch.Send(ctx, d.to, Outbound{Text: text}); err != nil {
			g.logger().Warn("gateway: cannot send the stop notice", "key", d.key, "err", err)
		}
	}
}

// ErrStopping is the error of Submit, Handle, and Enqueue after BeginStopping.
var ErrStopping = errors.New("gateway: the gateway is stopping, so it takes no new work")

// StopStarted sets the time of the marker of each busy key at the start of the stop, so the
// next gateway finds the markers fresh.
func (g *Gateway) StopStarted() {
	var keys []string
	g.mu.Lock()
	for key, ks := range g.keys {
		if ks.busy && ks.markerSession != "" {
			keys = append(keys, key)
		}
	}
	g.mu.Unlock()
	w := g.markerW()
	for _, key := range keys {
		w.touch(key)
	}
}

// BeginStopping is the stopping step. The stop runs it after the drain time and before it
// cancels the turns. ctx limits the wait for the marker writes.
//
// The cancel makes each queued waiter leave and frees each key, and that would erase the
// markers. So BeginStopping writes the last marker of each key with the texts that wait for
// an answer, and then freezes the markers. From now on, the gateway takes no new work, no
// queued turn starts, and no key becomes free.
func (g *Gateway) BeginStopping(ctx context.Context) {
	type final struct {
		op markerOp
		// in gives the session of a key that has no marker session yet.
		in Inbound
	}
	var finals []final
	g.mu.Lock()
	g.stopping.Store(true)
	d := g.debouncerLocked()
	// bounds holds the last seq of each key. The final op of a key takes its seq here,
	// after each hook op of the key.
	bounds := make(map[string]uint64, len(g.keys))
	for key, ks := range g.keys {
		queued := stopQueued(key, ks, d)
		if ks.markerSession != "" || len(queued) > 0 {
			ks.markerSeq++
			f := final{op: markerOp{kind: markerUpsert, key: key, sessionID: ks.markerSession, queued: queued, seq: ks.markerSeq}, in: ks.last}
			if ks.current != nil {
				f.in = ks.current.in
			}
			finals = append(finals, f)
		}
		bounds[key] = ks.markerSeq
	}
	g.mu.Unlock()
	w := g.markerW()
	if w == nil {
		return
	}
	// A hook op that was computed before the stop still runs, also when it is sent after
	// the freeze. The writer skips it when it is older than the final op of its key.
	w.FreezeAt(bounds)
	for _, f := range finals {
		if f.op.sessionID == "" {
			// The turn of the key did not get its session yet, or /stop keep removed the
			// marker. The texts still wait, so they need a session.
			in := f.in
			sess, err := g.Deps.Store.ActiveOrCreate(ctx, f.op.key, sessions.Session{
				Source: in.Source, ChatID: in.ChatID, ThreadID: in.ThreadID, UserID: in.UserID, Kind: "chat",
			})
			if err != nil {
				g.logger().Warn("gateway: cannot save the turn marker", "key", f.op.key, "err", err)
				continue
			}
			f.op.sessionID = sess.ID
		}
		w.send(f.op)
	}
	if err := w.Flush(ctx); err != nil {
		g.logger().Warn("gateway: the stop did not save all turn markers", "err", err)
	}
}

// stopQueued returns the texts of key that wait for an answer, oldest first:
//  1. The message that owns the key but did not start its agent. It does not start now.
//  2. The steer queue of the run. The queue is empty after the call.
//  3. The merged text in the debouncer. It is the newest text, and it does not flush now.
//  4. The queued messages.
//
// A stopped run (/stop) keeps its steer, because the end of the run drops it. The caller
// holds g.mu.
func stopQueued(key string, ks *keyState, d *Debouncer) []sessions.QueuedText {
	var out []sessions.QueuedText
	if ks.busy {
		if p := ks.current; p != nil && ks.running == nil && !p.discard {
			out = append(out, queuedTexts([]*pendingTurn{p})...)
		}
		if rs := ks.running; rs != nil && !rs.stopped {
			if text := rs.Steer.Drain(); text != "" {
				out = append(out, sessions.QueuedText{Text: text})
			}
		}
	}
	if text := d.Take(key); text != "" {
		out = append(out, sessions.QueuedText{Text: text})
	}
	return append(out, queuedTexts(ks.queue)...)
}

// stoppingLocked reports whether the gateway stops. Then it unlocks g.mu and logs that the
// busy text of key is lost: the text left the debouncer at the same time as the stop. The
// caller holds g.mu.
func (g *Gateway) stoppingLocked(key string) bool {
	if !g.stopping.Load() {
		return false
	}
	g.mu.Unlock()
	g.logger().Warn("gateway: a busy text came during the stop, and it is lost", "key", key)
	return true
}
