package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// The busy texts. They are the same as Hermes gateway/run.py.
const (
	SteerAck           = "⏩ Steered into current run (%s). Your message arrives after the next tool call."
	QueueAck           = "⏳ Queued for the next turn (%s). I'll respond once the current task finishes."
	StopReply          = "⚡ Stopped. You can continue this session."
	NoTaskReply        = "No active task to stop."
	SteerStartingReply = "Agent still starting — /steer queued for the next turn."
)

// BusyHint is added to the first busy ack of a session.
const BusyHint = "(Send /stop to cancel, or /steer <text> to guide the run.)"

const (
	// busyAckEvery is the minimum time between two busy acks of one session (Hermes).
	busyAckEvery = 30 * time.Second
	// debounceWindow and debounceHardCap merge the busy texts of a key (Hermes).
	debounceWindow  = 350 * time.Millisecond
	debounceHardCap = time.Second
	// steerPreviewMax is the number of runes of the /steer text in its reply.
	steerPreviewMax = 60
)

func init() {
	Register(Command{Name: "stop", Help: "Stop the running task", Run: cmdStop, WhileBusy: true})
	Register(Command{Name: "steer", Help: "Guide the running task: /steer <text>", Run: cmdSteer, WhileBusy: true})
}

// cmdStop stops the run of the key. The Stop hook of the channel sends the reply. With no
// hook, no task runs.
func cmdStop(_ context.Context, c *CommandCtx, _ string) error {
	if c.Hooks.Stop == nil {
		c.reply("%s", NoTaskReply)
		return nil
	}
	c.Hooks.Stop()
	return nil
}

// cmdSteer gives the text to the Steer hook of the channel, which sends the reply.
func cmdSteer(_ context.Context, c *CommandCtx, args string) error {
	text := strings.TrimSpace(args)
	if text == "" {
		c.reply("Usage: /steer <text>")
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
	g.ack(rs, in.Key, SteerAck, r)
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
	g.ack(rs, in.Key, QueueAck, r)
}

// steerCommand runs /steer text for the key of in. With a run, the text goes into the
// run. While the agent starts, the text waits for the next turn. With no run, the text
// runs as a normal message: the returned rest runs its turn.
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
		r.Ack(fmt.Sprintf("⏩ Steer queued — arrives after the next tool call: '%s'", preview(text, steerPreviewMax)))
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

// ack sends a busy ack with the detail of rs to r. It sends at most one ack per session in
// busyAckEvery. The first ack of a session ends with BusyHint.
func (g *Gateway) ack(rs *RunState, key, format string, r Renderer) {
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
		g.lastAck, g.hinted = map[string]time.Time{}, map[string]bool{}
	}
	g.lastAck[id] = now
	first := !g.hinted[id]
	g.hinted[id] = true
	g.mu.Unlock()
	text := busyAckText(format, BusyAckDetail(rs, now))
	if first {
		text += "\n\n" + BusyHint
	}
	r.Ack(text)
}

// BusyAckDetail returns the state of the run for a busy ack, for example
// "3 min elapsed, iteration 4/90, running: web_fetch". Like Hermes, it leaves out an
// elapsed time under 1 min and an empty tool.
func BusyAckDetail(rs *RunState, now time.Time) string {
	if rs == nil {
		return ""
	}
	var parts []string
	if !rs.Started.IsZero() {
		if m := int(now.Sub(rs.Started) / time.Minute); m > 0 {
			parts = append(parts, fmt.Sprintf("%d min elapsed", m))
		}
	}
	if rs.Round != nil {
		if i, max := rs.Round(); max > 0 {
			parts = append(parts, fmt.Sprintf("iteration %d/%d", i, max))
		}
	}
	if rs.Tool != nil {
		if tool := rs.Tool(); tool != "" {
			parts = append(parts, "running: "+tool)
		}
	}
	return strings.Join(parts, ", ")
}

// busyAckText fills format with detail. An empty detail removes the parentheses.
func busyAckText(format, detail string) string {
	if detail == "" {
		return strings.Replace(fmt.Sprintf(format, ""), " ()", "", 1)
	}
	return fmt.Sprintf(format, detail)
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

// preview returns the first max runes of s, with "..." when s is longer.
func preview(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
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
