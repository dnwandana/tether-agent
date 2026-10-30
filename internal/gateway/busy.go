package gateway

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The busy texts of Tether Agent, in Markdown. A busy ack is one plain line.
const (
	SteerAck           = "↪️ Got it. I'll do this after the current step."
	QueueAck           = "⏳ Queued. I'll answer after the current task."
	StopReply          = "⏹ **Stopped**\nThe session is safe. Send a message to continue."
	NoTaskReply        = "💤 **No task to stop**"
	SteerStartingReply = "⏩ **Note saved**\nThe agent did not start yet. It reads the note at the start of the task."
	steerUsage         = "⏩ **Send a note with the command**\nExample: `/steer also check the error log`"
	// stopKeepOneReply and stopKeepManyReply are the replies to /stop keep while messages wait.
	stopKeepOneReply  = "⏹ **Stopped**\nThe waiting message runs now."
	stopKeepManyReply = "⏹ **Stopped**\nThe waiting messages run now, one at a time."
	// nowOneReply, nowManyReply, and nowNothingReply are the replies to /now.
	nowOneReply     = "⚡ Taking over. The current answer stopped."
	nowManyReply    = "⚡ Taking over. The current answer stopped. The waiting messages run now, one at a time."
	nowNothingReply = "✅ Nothing waits. The agent already has your message."
)

// stopButton is on the reply to a command that cannot run during a task. It runs /stop.
var stopButton = []Button{{Text: "⏹ Stop the task", Data: "/stop"}}

// nowButton returns the button of the busy replies. It runs /now for the turn of the reply,
// so a tap on the reply of an old turn does nothing.
func nowButton(turn int) []Button {
	return []Button{{Text: "⚡ Do this now", Data: fmt.Sprintf("/now %d", turn)}}
}

const (
	// busyAckEvery is the minimum time between two busy acks of one session (Hermes).
	busyAckEvery = 30 * time.Second
	// debounceWindow and debounceHardCap merge the busy texts of a key (Hermes).
	debounceWindow  = 350 * time.Millisecond
	debounceHardCap = time.Second
)

func init() {
	Register(Command{Name: "stop", Help: "Stop the running task", Run: cmdStop, WhileBusy: true})
	Register(Command{Name: "steer", Help: "Guide the running task: /steer <text>", Run: cmdSteer, WhileBusy: true})
	Register(Command{Name: "now", Help: "Stop the task and run your waiting message now", Run: cmdNow, WhileBusy: true})
}

// cmdStop stops the run of the key. The Stop hook of the channel sends the reply. With no
// hook, no task runs. "/stop keep" stops only the running task and keeps the queue. Other
// arguments do not change /stop.
func cmdStop(_ context.Context, c *CommandCtx, args string) error {
	if strings.TrimSpace(args) == "keep" {
		stopKeep(c)
		return nil
	}
	if c.Hooks.Stop == nil {
		c.reply("%s", NoTaskReply)
		return nil
	}
	c.Hooks.Stop()
	return nil
}

// stopKeep runs the StopKeep hook and replies with the number of the waiting messages.
func stopKeep(c *CommandCtx) {
	if c.Hooks.StopKeep == nil {
		c.reply("%s", NoTaskReply)
		return
	}
	stopped, waiting := c.Hooks.StopKeep()
	switch {
	case !stopped:
		c.reply("%s", NoTaskReply)
	case waiting == 1:
		c.reply("%s", stopKeepOneReply)
	case waiting > 1:
		c.reply("%s", stopKeepManyReply)
	default:
		c.reply("%s", StopReply)
	}
}

// cmdSteer gives the text to the Steer hook of the channel, which sends the reply.
func cmdSteer(_ context.Context, c *CommandCtx, args string) error {
	text := strings.TrimSpace(args)
	if text == "" {
		c.reply("%s", steerUsage)
		return nil
	}
	if c.Hooks.Steer == nil {
		c.reply("%s", NoTaskReply)
		return nil
	}
	c.Hooks.Steer(text)
	return nil
}

// cmdNow stops the running task and runs the waiting messages of the owner at once. The
// argument is the number of the turn of the button. With no number, /now acts on the
// current turn. Each reply edits the message of the pressed button.
func cmdNow(_ context.Context, c *CommandCtx, args string) error {
	turn, err := strconv.Atoi(strings.TrimSpace(args))
	if err != nil || turn < 0 {
		turn = 0
	}
	if c.Hooks.Now == nil {
		c.send(Reply{Text: nowNothingReply, Edit: true})
		return nil
	}
	took, waiting, stopping := c.Hooks.Now(turn)
	switch {
	case stopping:
		// A command during the stop of the gateway gets no reply.
	case !took:
		c.send(Reply{Text: nowNothingReply, Edit: true})
	case waiting > 1:
		c.send(Reply{Text: nowManyReply, Edit: true})
	default:
		c.send(Reply{Text: nowOneReply, Edit: true})
	}
	return nil
}

// onBusy puts the text of in into the running turn rs as a steer and sends the steer
// ack to r. It returns false when the steer cannot run: in is not plain text, the text is
// empty, the agent did not start yet (no steer queue), the run ended or stopped, or the
// gateway stops. The caller then queues in for the next turn. The caller must not hold g.mu.
func (g *Gateway) onBusy(_ context.Context, rs *RunState, in Inbound, r Renderer) (handled bool) {
	if rs == nil || !steerable(in) {
		return false
	}
	g.mu.Lock()
	// BeginStopping saved the steer queue already, so a later steer would be lost.
	ok := !g.stopping.Load() && rs.Steer != nil && !rs.ended && !rs.stopped && rs.Steer.Push(in.Text)
	g.mu.Unlock()
	if !ok {
		return false
	}
	g.ack(rs, in.Key, SteerAck, r)
	return true
}

// flushBusyText handles the merged busy text of a key. It steers the text into the run,
// or queues it for the next turn. When the key is free again, it starts a turn.
func (g *Gateway) flushBusyText(ctx context.Context, in Inbound, newRenderer func() Renderer) {
	g.mu.Lock()
	if g.stoppingLocked(in.Key) {
		return
	}
	ks := g.key(in.Key)
	if !ks.busy {
		p := &pendingTurn{in: in, newRenderer: newRenderer}
		ks.busy = true
		ks.take(p)
		g.start(ctx, p)
		g.mu.Unlock()
		return
	}
	rs := ks.running
	g.mu.Unlock()
	r := &lazyRenderer{new: newRenderer}
	if g.onBusy(ctx, rs, in, r) {
		return
	}
	g.mu.Lock()
	if g.stoppingLocked(in.Key) {
		return
	}
	if !ks.busy {
		p := &pendingTurn{in: in, newRenderer: newRenderer}
		ks.busy = true
		ks.take(p)
		g.start(ctx, p)
		g.mu.Unlock()
		return
	}
	ks.queue = append(ks.queue, &pendingTurn{in: in, newRenderer: newRenderer})
	rs = ks.running
	op := g.markerUpsertOp(ks, in.Key)
	g.mu.Unlock()
	g.sendMarker(op)
	g.ack(rs, in.Key, QueueAck, r)
}

// steerCommand runs /steer text for the key of in. With a run, the text goes into the
// run. While the agent starts, the text waits for the next turn. With no run, the text
// runs as a normal message: the returned rest runs its turn. Its reply has no busy limit.
func (g *Gateway) steerCommand(ctx context.Context, in Inbound, newRenderer func() Renderer, r Renderer, text string) (rest func() error) {
	rest = func() error { return nil }
	m := in
	m.Text, m.Images, m.Files, m.IsText = text, nil, nil, true
	g.mu.Lock()
	ks := g.key(in.Key)
	if !ks.busy {
		g.mu.Unlock()
		return g.acceptMessage(ctx, m, newRenderer)
	}
	if rs := ks.running; !g.stopping.Load() && rs != nil && rs.Steer != nil && !rs.ended && !rs.stopped && rs.Steer.Push(text) {
		turn := ks.currentTurn()
		g.mu.Unlock()
		ackReply(r, Reply{Text: SteerAck, Buttons: [][]Button{nowButton(turn)}})
		return rest
	}
	if g.stopping.Load() {
		g.mu.Unlock()
		return func() error { return ErrStopping }
	}
	ks.queue = append(ks.queue, &pendingTurn{in: m, newRenderer: newRenderer})
	op := g.markerUpsertOp(ks, in.Key)
	g.mu.Unlock()
	g.sendMarker(op)
	r.Ack(SteerStartingReply)
	return rest
}

// stopKey stops the run of key, drops its queued messages and its merged busy text, and
// cancels its background work. It returns false when the key had no run.
func (g *Gateway) stopKey(key string) bool {
	g.debouncer().Drop(key)
	g.mu.Lock()
	ks := g.key(key)
	had := ks.current != nil || ks.running != nil
	if ks.current != nil {
		// A stop after /now or /stop keep also drops the steer that the turn holds.
		ks.current.discard, ks.current.takeOver = true, false
	}
	rs := ks.running
	if rs != nil {
		rs.stopped = true
	}
	dropped := ks.queue
	ks.queue = nil
	for _, p := range dropped {
		p.discard = true
	}
	// A stopped turn does not continue after a restart.
	op := g.markerRemoveOp(ks, key)
	g.mu.Unlock()
	g.sendMarker(op)
	for _, p := range dropped {
		if p.ready != nil {
			close(p.ready)
		}
	}
	if rs != nil {
		rs.Cancel()
	}
	if g.Deps.CancelKey != nil {
		g.Deps.CancelKey(key)
	}
	if had {
		g.logger().Info("gateway: the run is stopped", "key", key, "dropped", len(dropped))
	}
	return had
}

// stopKeepQueue stops the run of key and drops its merged busy text, but it keeps the queue.
// The end of the stopped turn gives the key to the next queued message. It does not cancel
// the background work of the key. It returns false when the key had no run, and the number
// of the queued messages that will run.
func (g *Gateway) stopKeepQueue(key string) (stopped bool, waiting int) {
	g.debouncer().Drop(key)
	g.mu.Lock()
	ks := g.key(key)
	stopped = ks.current != nil || ks.running != nil
	if ks.current != nil {
		// A turn that starts now runs no agent. The steer that the turn holds becomes the
		// next turn.
		ks.current.discard = true
		ks.current.takeOver = true
	}
	rs := ks.running
	if rs != nil {
		rs.stopped = true
	}
	for _, p := range ks.queue {
		// next drops a queued slash command, so it does not count.
		if !isCommandText(p.in) {
			waiting++
		}
	}
	// The next queued turn writes the marker again when it starts.
	op := g.markerRemoveOp(ks, key)
	g.mu.Unlock()
	g.sendMarker(op)
	if rs != nil {
		rs.Cancel()
	}
	if stopped {
		g.logger().Info("gateway: the run is stopped, the queue stays", "key", key, "waiting", waiting)
	}
	return stopped, waiting
}

// takeOver stops the turn of key and runs the waiting messages of the owner at once. A
// turn above 0 must still hold the key, else nothing changes. The waiting steer text and
// the merged busy text become the next turn, before the queue. Queued slash commands and
// Enqueue turns do not count, and they stay in their place. took is false when the turn
// was already stopped, or when no message of the owner waits, and then nothing changes. waiting is the number of the turns that
// wait now. stopping is true when the gateway stops, and then nothing changes.
func (g *Gateway) takeOver(key string, turn int) (took bool, waiting int, stopping bool) {
	g.mu.Lock()
	if g.stopping.Load() {
		// BeginStopping saved the steer queue and the queue in the marker.
		g.mu.Unlock()
		return false, 0, true
	}
	ks := g.key(key)
	cur := ks.current
	// A turn that /stop, /stop keep, or an earlier /now stopped does not go on, and its
	// end already applies the rule for its steer.
	if !ks.busy || cur == nil || cur.discard || turn > 0 && turn != cur.turn {
		g.mu.Unlock()
		return false, 0, false
	}
	// The agent drains the steer queue without g.mu, so the decision uses the drained text.
	var parts []string
	rs := ks.running
	if rs != nil {
		if s := rs.Steer.Drain(); s != "" {
			parts = append(parts, s)
		}
	}
	if s := g.debouncerLocked().Take(key); s != "" {
		parts = append(parts, s)
	}
	text := strings.Join(parts, "\n")
	for _, p := range ks.queue {
		// next drops a queued slash command, so it does not count.
		if !p.system && !isCommandText(p.in) {
			waiting++
		}
	}
	if text == "" && waiting == 0 {
		g.mu.Unlock()
		return false, 0, false
	}
	if text != "" {
		in := cur.in
		in.Text, in.Images, in.Files, in.IsText = text, nil, nil, true
		ks.queue = append([]*pendingTurn{{in: in, newRenderer: cur.newRenderer}}, ks.queue...)
		waiting++
	}
	// A turn that starts now runs no agent. The steer that the stopped turn holds becomes
	// the next turn.
	cur.discard, cur.takeOver = true, true
	if rs != nil {
		rs.stopped = true
	}
	// The next queued turn writes the marker again when it starts.
	op := g.markerRemoveOp(ks, key)
	g.mu.Unlock()
	g.sendMarker(op)
	if rs != nil {
		rs.Cancel()
	}
	g.logger().Info("gateway: the run is stopped, and the waiting messages run now", "key", key, "waiting", waiting)
	return true, waiting, false
}

// ack sends text with the button of the turn that holds the key to r. In one turn of the
// key, it sends at most one ack per session in busyAckEvery (Hermes). A new turn can get an
// ack at once.
func (g *Gateway) ack(rs *RunState, key, text string, r Renderer) {
	now := g.clock()
	id := key
	if rs != nil && rs.SessionID != "" {
		id = rs.SessionID
	}
	g.mu.Lock()
	turn := 0
	if ks := g.keys[key]; ks != nil {
		turn = ks.currentTurn()
	}
	if last, ok := g.lastAck[id]; ok && last.turn == turn && now.Sub(last.at) < busyAckEvery {
		g.mu.Unlock()
		return
	}
	if g.lastAck == nil {
		g.lastAck = map[string]ackMark{}
	}
	g.lastAck[id] = ackMark{turn: turn, at: now}
	g.mu.Unlock()
	ackReply(r, Reply{Text: text, Buttons: [][]Button{nowButton(turn)}})
}

// ackReply sends rep with AckReply when r can show buttons, else it sends rep.Text with Ack.
func ackReply(r Renderer, rep Reply) {
	if rr, ok := r.(ReplyRenderer); ok {
		rr.AckReply(rep)
		return
	}
	r.Ack(rep.Text)
}

// steerable reports whether in is a plain text that can go into a run as a steer.
func steerable(in Inbound) bool {
	return in.IsText && len(in.Images) == 0 && len(in.Files) == 0 &&
		strings.TrimSpace(in.Text) != "" && !isCommandText(in)
}

// isCommandText reports whether the text of in is a slash command.
func isCommandText(in Inbound) bool {
	_, _, ok := IsCommand(in.Text)
	return ok
}

func (g *Gateway) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

func (g *Gateway) debouncer() *Debouncer {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.debouncerLocked()
}

// debouncerLocked is debouncer for a caller that holds g.mu.
func (g *Gateway) debouncerLocked() *Debouncer {
	if g.debounce == nil {
		g.debounce = NewDebouncer(debounceWindow, debounceHardCap)
	}
	return g.debounce
}

// lazyRenderer makes its renderer on the first call, so a steer with no ack makes none.
type lazyRenderer struct {
	new  func() Renderer
	once sync.Once
	r    Renderer
}

func (l *lazyRenderer) get() Renderer {
	l.once.Do(func() { l.r = l.new() })
	return l.r
}

func (l *lazyRenderer) Delta(text string)              { l.get().Delta(text) }
func (l *lazyRenderer) ToolStart(name, preview string) { l.get().ToolStart(name, preview) }
func (l *lazyRenderer) ToolEnd(name string, isErr bool) {
	l.get().ToolEnd(name, isErr)
}
func (l *lazyRenderer) Boundary()                    { l.get().Boundary() }
func (l *lazyRenderer) Notice(text string)           { l.get().Notice(text) }
func (l *lazyRenderer) Done(final string, err error) { l.get().Done(final, err) }
func (l *lazyRenderer) Ack(text string)              { l.get().Ack(text) }

// AckReply sends r with its buttons when the inner renderer can show them, else r.Text.
func (l *lazyRenderer) AckReply(r Reply) {
	if rr, ok := l.get().(ReplyRenderer); ok {
		rr.AckReply(r)
		return
	}
	l.get().Ack(r.Text)
}

// Debouncer merges the texts that arrive for one key within a short time. A text waits
// for window. Each new text restarts the wait, but the merged text flushes at hardCap
// after the first text at the latest. It is safe for concurrent use.
type Debouncer struct {
	window, hardCap time.Duration

	mu      sync.Mutex
	pending map[string]*debounceEntry
	// flushing counts the flush functions that run now.
	flushing int
}

type debounceEntry struct {
	texts []string
	first time.Time
	timer *time.Timer
	flush func(merged string)
	// gen counts the texts, so that an old timer does not flush.
	gen int
}

// NewDebouncer returns a Debouncer. Hermes uses a window of 350 ms and a hard cap of 1 s.
func NewDebouncer(window, hardCap time.Duration) *Debouncer {
	return &Debouncer{window: window, hardCap: hardCap, pending: map[string]*debounceEntry{}}
}

// Add adds text for key. flush receives the texts of the key joined with "\n". The flush
// function of the last Add runs, in a goroutine of its own.
func (d *Debouncer) Add(key, text string, flush func(merged string)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e := d.pending[key]
	if e == nil {
		e = &debounceEntry{first: time.Now()}
		d.pending[key] = e
	}
	e.texts = append(e.texts, text)
	e.flush = flush
	e.gen++
	gen := e.gen
	wait := d.window
	if left := d.hardCap - time.Since(e.first); left < wait {
		wait = max(left, 0)
	}
	if e.timer != nil {
		e.timer.Stop()
	}
	e.timer = time.AfterFunc(wait, func() { d.fire(key, e, gen) })
}

// Drop removes the waiting texts of key with no flush.
func (d *Debouncer) Drop(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if e := d.pending[key]; e != nil {
		e.timer.Stop()
		delete(d.pending, key)
	}
}

// Take returns the merged waiting text of key and forgets it, with no flush. It returns ""
// when no text waits.
func (d *Debouncer) Take(key string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	e := d.pending[key]
	if e == nil {
		return ""
	}
	e.timer.Stop()
	delete(d.pending, key)
	return strings.Join(e.texts, "\n")
}

func (d *Debouncer) fire(key string, e *debounceEntry, gen int) {
	d.mu.Lock()
	if d.pending[key] != e || e.gen != gen {
		d.mu.Unlock()
		return
	}
	delete(d.pending, key)
	d.flushing++
	merged, flush := strings.Join(e.texts, "\n"), e.flush
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.flushing--
		d.mu.Unlock()
	}()
	flush(merged)
}

// Pending reports whether a text waits or a flush function runs.
func (d *Debouncer) Pending() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.pending) > 0 || d.flushing > 0
}
