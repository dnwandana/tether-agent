package gateway

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/sessions"
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
	// SteerReadAck, SteerLaterAck, and NotSentAck replace a steer ack when the steer is over.
	SteerReadAck  = "✅ The agent read your note."
	SteerLaterAck = "↪️ The task ended first. Your note runs as a new message."
	NotSentAck    = "⏹ Not sent. The task stopped."
	// QueueStartedAck replaces a queue ack when the last message that it covers starts.
	QueueStartedAck = "▶️ The agent works on your message now."
)

// GoalPausedLine goes after the reply to /stop and /stop keep when they pause a goal.
const GoalPausedLine = "⏸ Goal paused. Use /goal resume to continue."

// GoalBusyLine goes after BusyCommandReply when the key has an active goal. The key stays
// busy from one goal turn to the next.
const GoalBusyLine = "A goal runs. Send /goal pause first."

// The paused reasons of the goal code.
const (
	ReasonStopped        = "stopped by /stop"
	ReasonSessionChanged = "the session changed"
	ReasonChatEnded      = "the chat ended"
	ReasonLoop           = "the turn crashed the gateway 3 times"
)

// stopButton is on the reply to a command that cannot run during a task. It runs /stop.
var stopButton = []Button{{Text: "⏹ Stop the task", Data: "/stop"}}

// nowButton returns the button of the busy replies. It runs /now for the turn of the reply,
// so a tap on the reply of an old turn does nothing.
func nowButton(turn int) []Button {
	return []Button{{Text: "⚡ Do this now", Data: fmt.Sprintf("/now %d", turn)}}
}

const (
	// busyAckEvery is the minimum time between two busy acks of one kind in one session
	// (Hermes).
	busyAckEvery = 30 * time.Second
	// debounceWindow and debounceHardCap merge the busy texts of a key (Hermes).
	debounceWindow  = 350 * time.Millisecond
	debounceHardCap = time.Second
)

// ackKind tells a steer ack from a queue ack. Each kind has its own busy limit.
type ackKind int

const (
	ackSteer ackKind = iota
	ackQueue
)

// ackSlot is one busy ack, from the reservation until its outcome. Gateway.mu guards it.
type ackSlot struct {
	kind ackKind
	// seq is the highest steer sequence number that the ack covers. A queue ack has 0.
	seq uint64
	// pending counts the queued messages that a queue ack covers.
	pending int
	// outcome is the text of the close. It stays when the slot closes before its send
	// returns, so sendAck can edit the ack then.
	outcome string
	// handle is nil until the send returns, and stays nil for a renderer with no handle.
	handle AckHandle
	closed bool
	// markKey is the key of the busy limit mark of the slot. The /steer ack has no mark and
	// a zero markKey.
	markKey ackKey
}

// ackKey is the key of a busy limit mark: a session (or a key with no session yet) and a kind.
type ackKey struct {
	id   string
	kind ackKind
}

// ackClose is an edit of an ack. Collect it under Gateway.mu and run it after the unlock.
type ackClose struct {
	h    AckHandle
	text string
}

// run starts the edit. The channel closes when the edit ends. With no handle, it is closed.
func (c ackClose) run() <-chan struct{} {
	if c.h == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	return c.h.Close(c.text)
}

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

// errGoalNotActive tells pauseGoal that the session has no active goal, so the update
// writes nothing.
var errGoalNotActive = errors.New("gateway: the goal is not active")

// pauseGoal pauses the goal of the session with reason when its status is active. A
// parked goal is active, so it pauses too, and its wait barrier is cleared. It returns
// false when the session has no active goal, or when d has no goal manager.
func pauseGoal(ctx context.Context, d *Deps, sessionID, reason string) (bool, error) {
	if d == nil || d.Goals == nil {
		return false, nil
	}
	_, err := d.Goals.Update(ctx, sessionID, func(st *goals.State, exists bool) error {
		if !exists || st.Status != goals.StatusActive {
			return errGoalNotActive
		}
		st.SetStatus(goals.StatusPaused, reason)
		st.ClearWait()
		return nil
	})
	// A wake timer of the goal stays. IsCurrent is false for the new epoch, so the timer
	// does nothing when it fires.
	switch {
	case errors.Is(err, errGoalNotActive):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

// stopGoalText returns the reply to /stop or /stop keep. When a goal paused, a reply with
// no task becomes GoalPausedLine, and another reply gets GoalPausedLine as a new line.
func stopGoalText(reply string, paused bool) string {
	switch {
	case !paused:
		return reply
	case reply == NoTaskReply:
		return GoalPausedLine
	default:
		return reply + "\n" + GoalPausedLine
	}
}

// pauseGoalForStop pauses the active goal of sess for /stop and /stop keep, and removes
// the queued goal turns of key. A failure is logged, and the stop goes on.
func (g *Gateway) pauseGoalForStop(ctx context.Context, key string, sess *sessions.Session) bool {
	if g.Deps == nil || g.Deps.Goals == nil || sess == nil {
		return false
	}
	paused, err := pauseGoal(ctx, g.Deps, sess.ID, ReasonStopped)
	if err != nil {
		g.logger().Warn("gateway: cannot pause the goal for /stop", "key", key, "session", sess.ID, "err", err.Error())
	}
	g.dropGoalTurns(key)
	return paused
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
	if g.stopping.Load() || rs.Steer == nil || rs.ended || rs.stopped {
		g.mu.Unlock()
		return false
	}
	seq, ok := rs.Steer.PushSeq(in.Text)
	if !ok {
		g.mu.Unlock()
		return false
	}
	turn := 0
	if ks := g.keys[in.Key]; ks != nil {
		turn = ks.currentTurn()
	}
	slot, send := g.reserveAckLocked(rs, in.Key, ackSteer, seq)
	g.mu.Unlock()
	if send {
		g.sendAck(slot, SteerAck, turn, r)
	}
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
	p := &pendingTurn{in: in, newRenderer: newRenderer}
	ks.queue = append(ks.queue, p)
	slot, send := g.reserveAckLocked(ks.running, in.Key, ackQueue, 0)
	p.ack = slot
	turn := ks.currentTurn()
	op := g.markerUpsertOp(ks, in.Key)
	g.mu.Unlock()
	g.sendMarker(op)
	if send {
		g.sendAck(slot, QueueAck, turn, r)
	}
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
	if rs := ks.running; !g.stopping.Load() && rs != nil && rs.Steer != nil && !rs.ended && !rs.stopped {
		if seq, ok := rs.Steer.PushSeq(text); ok {
			// The /steer ack has no busy limit, so its slot has no mark.
			slot := &ackSlot{kind: ackSteer, seq: seq}
			rs.steerAcks = append(rs.steerAcks, slot)
			turn := ks.currentTurn()
			g.mu.Unlock()
			g.sendAck(slot, SteerAck, turn, r)
			return rest
		}
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
	var workCancel context.CancelFunc
	if ks.current != nil {
		// A stop after /now or /stop keep also drops the steer that the turn holds.
		ks.current.discard, ks.current.takeOver = true, false
		workCancel = ks.current.cancel
	}
	rs := ks.running
	var stepCancel context.CancelFunc
	if rs != nil {
		rs.stopped = true
		stepCancel = rs.StepCancel
	}
	dropped := ks.queue
	ks.queue = nil
	var closes []ackClose
	for _, p := range dropped {
		p.discard = true
		closes = append(closes, g.releaseAckLocked(p, NotSentAck))
		closes = append(closes, g.releaseSteerAcksLocked(p, NotSentAck)...)
	}
	// A stopped turn does not continue after a restart.
	op := g.markerRemoveOp(ks, key)
	g.mu.Unlock()
	g.sendMarker(op)
	for _, c := range closes {
		c.run()
	}
	for _, p := range dropped {
		if p.ready != nil {
			close(p.ready)
		}
	}
	if rs != nil {
		rs.Cancel()
	}
	// This ends the judge call and the gates of a running goal step.
	if stepCancel != nil {
		stepCancel()
	}
	// This ends the draft call of /goal draft, so the key does not wait for the model.
	if workCancel != nil {
		workCancel()
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
	var workCancel context.CancelFunc
	if ks.current != nil {
		// A turn that starts now runs no agent. The steer that the turn holds becomes the
		// next turn.
		ks.current.discard = true
		ks.current.takeOver = true
		workCancel = ks.current.cancel
	}
	rs := ks.running
	var stepCancel context.CancelFunc
	if rs != nil {
		rs.stopped = true
		stepCancel = rs.StepCancel
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
	// This ends the judge call and the gates of a running goal step.
	if stepCancel != nil {
		stepCancel()
	}
	// This ends the draft call of /goal draft, so the queue goes on at once.
	if workCancel != nil {
		workCancel()
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
// wait now. stopping is true when the gateway stops, and then nothing changes. msgID is the
// message ID of the pressed button, or 0. The open busy ack of that message closes with no
// edit, because the /now reply goes into that message.
func (g *Gateway) takeOver(key string, turn, msgID int) (took bool, waiting int, stopping bool) {
	g.mu.Lock()
	if g.stopping.Load() {
		// BeginStopping saved the steer queue and the queue in the marker.
		g.mu.Unlock()
		return false, 0, true
	}
	ks := g.key(key)
	// The /now reply goes into the tapped message. The tapped ack closes with no edit only
	// when /now takes the turn. When /now takes nothing, the ack stays open, so a later
	// outcome can still edit it.
	tapped := tappedSlotLocked(ks, msgID)
	var tappedClose ackClose
	// Each return unlocks g.mu first, so the deferred run comes after the unlock.
	defer func() { tappedClose.run() }()
	cur := ks.current
	// A turn that /stop, /stop keep, or an earlier /now stopped does not go on, and its
	// end already applies the rule for its steer.
	if !ks.busy || cur == nil || cur.discard || turn > 0 && turn != cur.turn {
		g.mu.Unlock()
		return false, 0, false
	}
	// The agent drains the steer queue without g.mu, so the decision uses the drained text.
	var parts []string
	var drained uint64
	rs := ks.running
	if rs != nil {
		if s, seq := rs.Steer.DrainSeq(); s != "" {
			parts = append(parts, s)
			drained = seq
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
	tappedClose = g.closeSlotLocked(tapped, "")
	if text != "" {
		in := cur.in
		in.Text, in.Images, in.Files, in.IsText, in.Slash = text, nil, nil, true, false
		// The agent cannot read the drained text now. Its steer acks move with the text, so
		// the start, the drop, or a restart of the new message closes them.
		moved := &pendingTurn{in: in, newRenderer: cur.newRenderer, steerAcks: takeSteerAcksLocked(rs, drained)}
		ks.queue = append([]*pendingTurn{moved}, ks.queue...)
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

// reserveAckLocked reserves the busy ack of kind for a message of key. In one turn of the
// key, one ack of each kind per session goes out in busyAckEvery (Hermes). A new turn can
// get an ack at once. When send is false, the open slot of the mark covers the message. The
// caller holds g.mu, and it calls sendAck after the unlock when send is true.
func (g *Gateway) reserveAckLocked(rs *RunState, key string, kind ackKind, seq uint64) (slot *ackSlot, send bool) {
	id := key
	if rs != nil && rs.SessionID != "" {
		id = rs.SessionID
	}
	turn := 0
	if ks := g.keys[key]; ks != nil {
		turn = ks.currentTurn()
	}
	k, now := ackKey{id: id, kind: kind}, g.clock()
	if m, ok := g.lastAck[k]; ok && m.slot != nil && !m.slot.closed && m.turn == turn && now.Sub(m.at) < busyAckEvery {
		// The open ack of this kind covers the new message.
		if kind == ackSteer {
			m.slot.seq = max(m.slot.seq, seq)
		} else {
			m.slot.pending++
		}
		return m.slot, false
	}
	slot = &ackSlot{kind: kind, seq: seq, markKey: k}
	if kind == ackSteer {
		rs.steerAcks = append(rs.steerAcks, slot)
	} else {
		slot.pending = 1
	}
	if g.lastAck == nil {
		g.lastAck = map[ackKey]ackMark{}
	}
	g.lastAck[k] = ackMark{turn: turn, at: now, slot: slot}
	return slot, true
}

// sendAck sends text with the button of turn to r and attaches the handle to slot. When the
// slot closed before the send returned, it edits the ack to the outcome at once. The caller
// must not hold g.mu.
func (g *Gateway) sendAck(slot *ackSlot, text string, turn int, r Renderer) {
	h := ackReply(r, Reply{Text: text, Buttons: [][]Button{nowButton(turn)}})
	if h == nil {
		return
	}
	g.mu.Lock()
	slot.handle = h
	closed, outcome := slot.closed, slot.outcome
	g.mu.Unlock()
	if closed {
		ackClose{h: h, text: outcome}.run()
	}
}

// closeSlotLocked closes s with text, or with no edit when text is "". The first outcome
// wins, so a second close returns a zero ackClose. The mark of s goes away only when it
// still points to s, so a newer ack keeps its limit. The caller holds g.mu, and it runs the
// result after the unlock.
func (g *Gateway) closeSlotLocked(s *ackSlot, text string) ackClose {
	if s == nil || s.closed {
		return ackClose{}
	}
	s.closed, s.outcome = true, text
	if m, ok := g.lastAck[s.markKey]; ok && m.slot == s {
		delete(g.lastAck, s.markKey)
	}
	return ackClose{h: s.handle, text: text}
}

// closeSteerAcksLocked closes the open steer acks of rs with seq <= upTo and removes them
// from rs.steerAcks. The caller holds g.mu, and it runs the result after the unlock.
func (g *Gateway) closeSteerAcksLocked(rs *RunState, upTo uint64, text string) []ackClose {
	var closes []ackClose
	for _, s := range takeSteerAcksLocked(rs, upTo) {
		closes = append(closes, g.closeSlotLocked(s, text))
	}
	return closes
}

// takeSteerAcksLocked removes the open steer acks of rs with seq <= upTo from rs.steerAcks
// and returns them. It also removes the closed acks. The caller holds g.mu.
func takeSteerAcksLocked(rs *RunState, upTo uint64) []*ackSlot {
	if rs == nil {
		return nil
	}
	var taken []*ackSlot
	open := rs.steerAcks[:0]
	for _, s := range rs.steerAcks {
		switch {
		case s.closed:
		case s.seq <= upTo:
			taken = append(taken, s)
		default:
			open = append(open, s)
		}
	}
	clear(rs.steerAcks[len(open):])
	rs.steerAcks = open
	return taken
}

// releaseSteerAcksLocked closes the steer acks that moved with p, with text. The caller holds
// g.mu, and it runs the result after the unlock.
func (g *Gateway) releaseSteerAcksLocked(p *pendingTurn, text string) []ackClose {
	var closes []ackClose
	for _, s := range p.steerAcks {
		closes = append(closes, g.closeSlotLocked(s, text))
	}
	p.steerAcks = nil
	return closes
}

// releaseAckLocked counts the queued message p as over for its queue ack. When p is the
// last message that the ack covers, it closes the slot with text. The caller holds g.mu.
func (g *Gateway) releaseAckLocked(p *pendingTurn, text string) ackClose {
	s := p.ack
	if s == nil {
		return ackClose{}
	}
	p.ack = nil
	s.pending--
	if s.pending > 0 {
		return ackClose{}
	}
	return g.closeSlotLocked(s, text)
}

// tappedSlotLocked returns the open busy ack slot of ks whose message has the ID msgID,
// or nil. The caller holds g.mu.
func tappedSlotLocked(ks *keyState, msgID int) *ackSlot {
	if msgID == 0 {
		return nil
	}
	match := func(s *ackSlot) bool {
		return s != nil && !s.closed && s.handle != nil && s.handle.MsgID() == msgID
	}
	if rs := ks.running; rs != nil {
		for _, s := range rs.steerAcks {
			if match(s) {
				return s
			}
		}
	}
	for _, p := range ks.queue {
		if match(p.ack) {
			return p.ack
		}
	}
	return nil
}

// steerRead closes the steer acks of rs that the agent read: the acks with seq <= seq.
func (g *Gateway) steerRead(rs *RunState, seq uint64) {
	g.mu.Lock()
	closes := g.closeSteerAcksLocked(rs, seq, SteerReadAck)
	g.mu.Unlock()
	for _, c := range closes {
		c.run()
	}
}

// steerEndText returns the text for the open steer acks at the end of a run. pending is the
// steer text that the agent did not read. ok is false while the gateway stops, because
// BeginStopping closes the acks then. A read closes its acks before the run ends, so an open
// ack with no pending text belongs to a lost steer, for example after a panic in the turn.
func steerEndText(pending string, discard, takeOver, stopping bool) (text string, ok bool) {
	switch {
	case stopping:
		return "", false
	case discard && !takeOver:
		return NotSentAck, true
	case pending != "" || takeOver:
		return SteerLaterAck, true
	default:
		return NotSentAck, true
	}
}

// ackReply sends rep and returns a handle to edit it. The handle is nil when r cannot edit a
// sent reply.
func ackReply(r Renderer, rep Reply) AckHandle {
	if hr, ok := r.(HandleRenderer); ok {
		return hr.AckReplyHandle(rep)
	}
	if rr, ok := r.(ReplyRenderer); ok {
		rr.AckReply(rep)
		return nil
	}
	r.Ack(rep.Text)
	return nil
}

// steerable reports whether in is a plain text that can go into a run as a steer. A
// slash text is never a steer.
func steerable(in Inbound) bool {
	return in.IsText && len(in.Images) == 0 && len(in.Files) == 0 &&
		strings.TrimSpace(in.Text) != "" && !in.Slash && !isCommandText(in)
}

// isCommandText reports whether the text of in is a slash command that the queue drops.
// A marked slash text (Inbound.Slash) is a message.
func isCommandText(in Inbound) bool {
	if in.Slash {
		return false
	}
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

// AckReplyHandle sends r with the inner renderer and returns its handle, or nil.
func (l *lazyRenderer) AckReplyHandle(r Reply) AckHandle { return ackReply(l.get(), r) }

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
