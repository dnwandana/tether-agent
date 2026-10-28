package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// The busy texts of Tether Agent, in Markdown. A busy ack adds the task details line.
const (
	SteerAck           = "⏩ **Note sent to the task**\nThe agent reads it after the current tool."
	QueueAck           = "⏳ **Queued**\nI will answer after the current task."
	StopReply          = "⏹ **Stopped**\nThe session is safe. Send a message to continue."
	NoTaskReply        = "💤 **No task to stop**"
	SteerStartingReply = "⏩ **Note saved**\nThe agent did not start yet. It reads the note at the start of the task."
	steerUsage         = "⏩ **Send a note with the command**\nExample: `/steer also check the error log`"
	// stopKeepOneReply and stopKeepManyReply are the replies to /stop keep while messages wait.
	stopKeepOneReply  = "⏹ **Stopped**\nThe waiting message runs now."
	stopKeepManyReply = "⏹ **Stopped**\nThe waiting messages run now, one at a time."
)

// stopButton is on the busy replies where no message of the owner waits. It runs /stop.
var stopButton = []Button{{Text: "⏹ Stop the task", Data: "/stop"}}

// queuedStopButton is on the reply to a message that waits in the queue. It runs /stop keep,
// so the waiting messages stay and the next one starts.
var queuedStopButton = []Button{{Text: "⏹ Stop the current task", Data: "/stop keep"}}

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

// onBusy puts the text of in into the running turn rs as a steer and sends the steer
// ack to r. It returns false when the steer cannot run: in is not plain text, the text is
// empty, the agent did not start yet (no steer queue), or the run ended or stopped. The
// caller then queues in for the next turn. The caller must not hold g.mu.
func (g *Gateway) onBusy(_ context.Context, rs *RunState, in Inbound, r Renderer) (handled bool) {
	if rs == nil || !steerable(in) {
		return false
	}
	g.mu.Lock()
	ok := rs.Steer != nil && !rs.ended && !rs.stopped && rs.Steer.Push(in.Text)
	g.mu.Unlock()
	if !ok {
		return false
	}
	g.ack(rs, in.Key, SteerAck, stopButton, r)
	return true
}

// flushBusyText handles the merged busy text of a key. It steers the text into the run,
// or queues it for the next turn. When the key is free again, it starts a turn.
func (g *Gateway) flushBusyText(ctx context.Context, in Inbound, newRenderer func() Renderer) {
	g.mu.Lock()
	ks := g.key(in.Key)
	if !ks.busy {
		p := &pendingTurn{in: in, newRenderer: newRenderer}
		ks.busy, ks.current = true, p
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
	if !ks.busy {
		p := &pendingTurn{in: in, newRenderer: newRenderer}
		ks.busy, ks.current = true, p
		g.start(ctx, p)
		g.mu.Unlock()
		return
	}
	ks.queue = append(ks.queue, &pendingTurn{in: in, newRenderer: newRenderer})
	rs = ks.running
	g.mu.Unlock()
	g.ack(rs, in.Key, QueueAck, queuedStopButton, r)
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
	if rs := ks.running; rs != nil && rs.Steer != nil && !rs.ended && !rs.stopped && rs.Steer.Push(text) {
		g.mu.Unlock()
		ackReply(r, Reply{Text: withDetail(SteerAck, BusyAckDetail(rs, g.clock())), Buttons: [][]Button{stopButton}})
		return rest
	}
	ks.queue = append(ks.queue, &pendingTurn{in: m, newRenderer: newRenderer})
	g.mu.Unlock()
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
		ks.current.discard = true
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
	g.mu.Unlock()
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
		// A turn that starts now runs no agent, and a steer for it does not become a turn.
		ks.current.discard = true
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
	g.mu.Unlock()
	if rs != nil {
		rs.Cancel()
	}
	if stopped {
		g.logger().Info("gateway: the run is stopped, the queue stays", "key", key, "waiting", waiting)
	}
	return stopped, waiting
}

// ack sends text with the details line of rs and one row of buttons to r. It sends at most
// one ack per session in busyAckEvery (Hermes).
func (g *Gateway) ack(rs *RunState, key, text string, buttons []Button, r Renderer) {
	now := g.clock()
	id := key
	if rs != nil && rs.SessionID != "" {
		id = rs.SessionID
	}
	g.mu.Lock()
	if last, ok := g.lastAck[id]; ok && now.Sub(last) < busyAckEvery {
		g.mu.Unlock()
		return
	}
	if g.lastAck == nil {
		g.lastAck = map[string]time.Time{}
	}
	g.lastAck[id] = now
	g.mu.Unlock()
	ackReply(r, Reply{Text: withDetail(text, BusyAckDetail(rs, now)), Buttons: [][]Button{buttons}})
}

// ackReply sends rep with AckReply when r can show buttons, else it sends rep.Text with Ack.
func ackReply(r Renderer, rep Reply) {
	if rr, ok := r.(ReplyRenderer); ok {
		rr.AckReply(rep)
		return
	}
	r.Ack(rep.Text)
}

// BusyAckDetail returns the task details of rs, for example "2 min · step 4/90 · web_fetch".
// Like Hermes, it leaves out an elapsed time under 1 min and an empty tool.
func BusyAckDetail(rs *RunState, now time.Time) string {
	if rs == nil {
		return ""
	}
	var parts []string
	if !rs.Started.IsZero() {
		if m := int(now.Sub(rs.Started) / time.Minute); m > 0 {
			parts = append(parts, fmt.Sprintf("%d min", m))
		}
	}
	if rs.Round != nil {
		if i, max := rs.Round(); max > 0 {
			parts = append(parts, fmt.Sprintf("step %d/%d", i, max))
		}
	}
	if rs.Tool != nil {
		if tool := rs.Tool(); tool != "" {
			parts = append(parts, tool)
		}
	}
	return strings.Join(parts, " · ")
}

// withDetail adds detail to text as a last line in italics. An empty detail adds no line.
func withDetail(text, detail string) string {
	if detail == "" {
		return text
	}
	return text + "\n_" + detail + "_"
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

func (d *Debouncer) fire(key string, e *debounceEntry, gen int) {
	d.mu.Lock()
	if d.pending[key] != e || e.gen != gen {
		d.mu.Unlock()
		return
	}
	delete(d.pending, key)
	merged, flush := strings.Join(e.texts, "\n"), e.flush
	d.mu.Unlock()
	flush(merged)
}
