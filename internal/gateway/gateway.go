package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// Target is the place where a channel sends a message.
type Target struct {
	Source, ChatID, ThreadID string
}

// Button is one button of an inline keyboard.
type Button struct {
	Text, Data string
}

// Outbound is one message to a channel.
type Outbound struct {
	Text     string
	Files    []string
	Keyboard [][]Button
	ReplyTo  string
}

// Channel is a source of inbound messages and a place to send answers.
type Channel interface {
	Name() string
	// Run is the receive loop. It calls gw.Handle for each inbound message.
	Run(ctx context.Context, gw *Gateway) error
	// Send sends text, files, or a keyboard.
	Send(ctx context.Context, to Target, msg Outbound) error
}

// Inbound is one message from a channel.
type Inbound struct {
	// Key is the session key of the message.
	Key                                              string
	Source, ChatID, ThreadID, UserID, UserName, Text string
	Images                                           []provider.Image
	Files                                            []string
	IsText                                           bool
	// Target is the place of the answer.
	Target Target
	// ButtonMsgID is the message ID of the pressed button, or 0 for a typed message.
	ButtonMsgID int
	// Slash is true when the owner typed a slash command that is not a built-in and not a
	// reserved name. The gateway treats the text as a message. Deps.Turn runs the skill of
	// the command at the start of the turn.
	Slash bool
}

// Renderer shows one turn on a channel. The channel builds one for each turn.
type Renderer interface {
	agent.Sink
	// Done receives the final text of the turn and its error.
	Done(final string, err error)
	// Ack sends a short text at once, for example a command reply.
	Ack(text string)
}

// ReplyRenderer is a Renderer that can show buttons and rich messages. A Renderer without
// it gets the text of a Reply only.
type ReplyRenderer interface{ AckReply(r Reply) }

// AckHandle edits a busy ack that was sent.
type AckHandle interface {
	// MsgID returns the message ID of the ack.
	MsgID() int
	// Close edits the ack to text and removes its buttons. It runs once and does not
	// block. The channel closes when the edit ends. Close("") closes with no edit.
	Close(text string) <-chan struct{}
}

// HandleRenderer sends a reply with buttons and returns a handle to edit it later.
type HandleRenderer interface{ AckReplyHandle(r Reply) AckHandle }

// GoalLineRenderer shows the goal line of a continuation turn before the agent starts, for
// example as the first line of the status bubble. A Renderer without it shows no goal line.
type GoalLineRenderer interface{ GoalLine(line string) }

// RunState describes the running turn of a key.
type RunState struct {
	// Cancel stops the turn (/stop).
	Cancel context.CancelFunc
	// Steer receives the guidance for the running turn.
	Steer   *agent.SteerQueue
	Started time.Time
	// SessionID is the session of the turn. The busy acks are limited per session.
	SessionID string
	// StepCancel ends the after-turn step of the run: the judge call and the gates. It is
	// nil when no step runs. Gateway.mu guards it.
	StepCancel context.CancelFunc

	// ended is true after the turn returned, and stopped is true after /stop. A run in
	// either state takes no steer. Gateway.mu guards both.
	ended, stopped bool
	// steerAcks holds the steer acks of the run that are not closed. Gateway.mu guards it.
	steerAcks []*ackSlot
}

// Gateway routes the inbound messages of all channels to the agent. It runs one turn for
// each key at a time, and it queues the other messages of a busy key.
type Gateway struct {
	Deps     *Deps
	Channels map[string]Channel
	Log      *slog.Logger

	// mu guards keys. A holder of mu can take Debouncer.mu, but the Debouncer never takes
	// mu, so the two locks cannot deadlock.
	mu   sync.Mutex
	keys map[string]*keyState
	// active counts the turns that run or are about to run, for a graceful stop.
	active atomic.Int64
	// now is the clock of the busy acks. Nil uses time.Now.
	now func() time.Time
	// debounce merges the busy texts of a key. Nil uses 350 ms and a hard cap of 1 s.
	debounce *Debouncer
	// lastAck holds one mark for each session and ack kind: the turn, the time, and the slot
	// of the last busy ack. Each new turn resets the limit.
	lastAck map[ackKey]ackMark
	// markers writes the turn markers of the busy keys. Use markerW.
	markers    *markerWriter
	markerOnce sync.Once
	// stopping is set by BeginStopping, under mu. Then the gateway takes no new work, and
	// no queued turn starts.
	stopping atomic.Bool
	// waker wakes the parked goals. Use GoalWaker.
	waker     *GoalWaker
	wakerOnce sync.Once
}

// ackMark is the turn, the time, and the slot of the last busy ack of one kind in a session.
type ackMark struct {
	turn int
	at   time.Time
	slot *ackSlot
}

// keyState is the state of one session key. Gateway.mu guards it.
type keyState struct {
	// busy is true from the start of a turn until the queue is empty. running is set
	// while the agent runs.
	busy    bool
	running *RunState
	// current is the message that owns the key: the turn that runs or starts.
	current *pendingTurn
	// turns counts the turns that took the key. The number of the current turn is
	// current.turn.
	turns int
	// queue holds the messages that wait for the key, in order.
	queue []*pendingTurn
	// last is the newest message of the key and its renderer factory, for Enqueue.
	last         Inbound
	lastRenderer func() Renderer
	// markerSession is the session id of the marker of the key. It is empty until the turn
	// has its session, and again after the marker is removed. markerSeq counts the marker
	// ops of the key, so the writer can skip an old op.
	markerSession string
	markerSeq     uint64
}

// pendingTurn is one queued message. ready is closed when the message can run. A
// synthetic turn (Enqueue, a pending steer) has no ready channel: the gateway starts it.
type pendingTurn struct {
	in          Inbound
	newRenderer func() Renderer
	ready       chan struct{}
	// discard is true when /stop, /new, or the slash command rule dropped the message.
	// Gateway.mu guards it.
	discard bool
	// takeOver is true when /now or /stop keep stopped the turn. Then the end of the turn
	// keeps its steer. Gateway.mu guards it.
	takeOver bool
	// system is true for a turn from Enqueue, for example a batch result or a resume note.
	// /now does not count it as a waiting message of the owner.
	system bool
	// handed is true when next gave the key to the message. Then the message counts in
	// Gateway.active, and its waiter must run it, so the key goes on. Gateway.mu guards it.
	handed bool
	// turn is the number of the turn of the message in its key. It is 0 until the message
	// takes the key. Gateway.mu guards it.
	turn int
	// ack is the queue ack slot that covers the message, or nil. Gateway.mu guards it.
	ack *ackSlot
	// steerAcks are the open steer acks of the steer text that /now moved into this
	// message. Gateway.mu guards it.
	steerAcks []*ackSlot
	// goalTurn is true when the turn comes from the goal loop.
	goalTurn bool
	// cancel ends the work of a message that holds the key with no agent run, for example
	// the call of /goal draft. /stop calls it, so the key is free at once. It is nil for
	// other messages. Gateway.mu guards it.
	cancel context.CancelFunc
	// goalSession is the session of the goal.
	goalSession string
	// goalEpoch is the goal epoch when the turn was queued.
	goalEpoch int64
	// goalLine is the first line of the status bubble of the turn, or "".
	goalLine string
}

// take gives the key to p and gives p the next turn number. The caller holds Gateway.mu
// and sets busy.
func (ks *keyState) take(p *pendingTurn) {
	ks.turns++
	p.turn = ks.turns
	ks.current = p
}

// currentTurn returns the number of the turn that holds the key, or 0 when no message
// holds it. The caller holds Gateway.mu.
func (ks *keyState) currentTurn() int {
	if ks.current == nil {
		return 0
	}
	return ks.current.turn
}

// currentTurn returns the number of the turn that holds key, or 0 when no message holds it.
func (g *Gateway) currentTurn(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ks := g.keys[key]; ks != nil {
		return ks.currentTurn()
	}
	return 0
}

// Handle routes one inbound message. A slash command from the command table runs at once.
// While the key is busy, only a command with WhileBusy runs; another command gets
// BusyCommandReply (Hermes). Its replies go to Renderer.Ack. Other text runs one agent
// turn. When the key is busy, a text goes into the running turn as a steer,
// and other messages wait for their turn. Handle returns when the turn of this message
// ends, or at once for a steer. Channels call it from their own goroutine.
func (g *Gateway) Handle(ctx context.Context, in Inbound, newRenderer func() Renderer) error {
	return g.accept(ctx, in, newRenderer)()
}

// Submit routes in like Handle, but it does not wait for the turn. It returns when the
// gateway has taken the message: the command ran, the text went to the steer debouncer,
// the message is in the queue of the key, or its turn owns the key. The wait in the queue
// and the turn go on in a new goroutine, and then done (when not nil) gets the result of
// Handle. A channel that calls Submit for the messages of one chat one after the other
// keeps their order, and a long turn does not block the next message. While the gateway
// stops, done gets ErrStopping at once.
func (g *Gateway) Submit(ctx context.Context, in Inbound, newRenderer func() Renderer, done func(error)) {
	if g.stopping.Load() {
		if done != nil {
			done(ErrStopping)
		}
		return
	}
	g.active.Add(1)
	rest := g.accept(ctx, in, newRenderer)
	go func() {
		defer g.active.Add(-1)
		err := rest()
		if done != nil {
			done(err)
		}
	}()
}

// accept does the part of Handle that must keep the order of the messages of a key. It
// returns the rest: the wait in the queue and the turn.
func (g *Gateway) accept(ctx context.Context, in Inbound, newRenderer func() Renderer) (rest func() error) {
	if name, args, ok := IsCommand(in.Text); ok {
		if cmd, ok := lookupCommand(name); ok {
			return g.runCommand(ctx, in, newRenderer, cmd, args)
		}
	}
	// The check reads no skill folder. Deps.Turn resolves the skill when the turn starts.
	in.Slash = SlashCandidate(in.Text)
	return g.acceptMessage(ctx, in, newRenderer)
}

// acceptMessage takes in: it gives the free key to in, steers in into the busy key, or
// queues in. It returns the rest, which waits in the queue and runs the turn. While the
// gateway stops, the rest returns ErrStopping.
func (g *Gateway) acceptMessage(ctx context.Context, in Inbound, newRenderer func() Renderer) (rest func() error) {
	g.mu.Lock()
	if g.stopping.Load() {
		g.mu.Unlock()
		return func() error { return ErrStopping }
	}
	ks := g.key(in.Key)
	ks.last, ks.lastRenderer = in, newRenderer
	if !ks.busy {
		p := &pendingTurn{in: in, newRenderer: newRenderer}
		ks.busy = true
		ks.take(p)
		// The turn counts as active from now, so Wait does not miss it.
		g.active.Add(1)
		g.mu.Unlock()
		return func() error {
			defer g.active.Add(-1)
			g.run(ctx, p)
			return nil
		}
	}
	if steerable(in) {
		// Several texts in a short time become one steer (Hermes debounce). The text goes
		// in under g.mu, so BeginStopping either takes it or refuses it.
		g.debouncerLocked().Add(in.Key, in.Text, func(merged string) {
			m := in
			m.Text = merged
			g.flushBusyText(ctx, m, newRenderer)
		})
		g.mu.Unlock()
		return func() error { return nil }
	}
	p := &pendingTurn{in: in, newRenderer: newRenderer, ready: make(chan struct{})}
	ks.queue = append(ks.queue, p)
	// A built-in or reserved slash text is dropped when it leaves the queue, so it gets no ack.
	var slot *ackSlot
	send, turn := false, 0
	if !isCommandText(in) {
		slot, send = g.reserveAckLocked(ks.running, in.Key, ackQueue, 0)
		p.ack = slot
		turn = ks.currentTurn()
	}
	op := g.markerUpsertOp(ks, in.Key)
	g.mu.Unlock()
	g.sendMarker(op)
	if send {
		g.sendAck(slot, QueueAck, turn, newRenderer())
	}
	return func() error {
		select {
		case <-p.ready:
		case <-ctx.Done():
			g.mu.Lock()
			for i, q := range ks.queue {
				if q == p {
					ks.queue = append(ks.queue[:i], ks.queue[i+1:]...)
					// While the gateway stops, BeginStopping owns the ack.
					var left ackClose
					if !g.stopping.Load() {
						left = g.releaseAckLocked(p, NotSentAck)
					}
					op := g.markerUpsertOp(ks, in.Key)
					g.mu.Unlock()
					g.sendMarker(op)
					left.run()
					return ctx.Err()
				}
			}
			g.mu.Unlock()
			// The key was given to this message at the same time. Run it, so that the
			// queue of the key goes on.
		}
		g.mu.Lock()
		handed := p.handed
		g.mu.Unlock()
		if !handed {
			// /stop, /new, or the slash command rule dropped the message from the queue.
			return nil
		}
		// next counted the message in active. A message that /stop dropped after the
		// handoff runs no turn, but run gives the key to the next message.
		defer g.active.Add(-1)
		g.run(ctx, p)
		return nil
	}
}

// Enqueue adds a turn with text for key, for example a synthetic turn or a queued
// follow-up. It runs after the current turn and the turns that wait before it. Enqueue
// does not wait for the turn. The answer goes to the channel of the newest message of the
// key, or else to the channel and the chat of the active session of the key. While the
// gateway stops, it returns ErrStopping. The turn is not a message of the owner, so /now
// does not count it.
func (g *Gateway) Enqueue(ctx context.Context, key, text string) error {
	return g.enqueue(ctx, key, text, true)
}

// enqueueGoal adds the continuation turn c of a goal at the end of the queue of key. It
// replaces a queued turn of the same goal, so the goal has at most one queued turn. The
// turn is system, so /now does not count it, and it gets no queue ack. The turn checks
// before its start that the goal is still current.
func (g *Gateway) enqueueGoal(ctx context.Context, key string, c goals.Continuation) error {
	p := &pendingTurn{system: true, goalTurn: true, goalSession: c.SessionID, goalEpoch: c.Epoch, goalLine: c.StatusLine()}
	p.in.Text = c.Text
	return g.enqueueTurn(ctx, key, p)
}

// dropGoalTurns removes the queued goal turns of key and returns their count. A goal turn
// is not in the marker, so the marker does not change.
func (g *Gateway) dropGoalTurns(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	ks := g.keys[key]
	if ks == nil {
		return 0
	}
	n := 0
	kept := ks.queue[:0]
	for _, p := range ks.queue {
		if p.goalTurn {
			n++
			continue
		}
		kept = append(kept, p)
	}
	clear(ks.queue[len(kept):])
	ks.queue = kept
	return n
}

// queueGoalTurnLocked puts the goal turn p at the end of the queue of ks. Two queued turns
// of one goal give two model turns for one decision, and the second one can run through a
// barrier. So p replaces the queued turns of its goal. A queued turn with a newer epoch
// stays, and p is dropped, because p is stale. The caller holds g.mu.
func queueGoalTurnLocked(ks *keyState, p *pendingTurn) {
	for _, q := range ks.queue {
		if q.goalTurn && q.goalSession == p.goalSession && q.goalEpoch > p.goalEpoch {
			return
		}
	}
	removeGoalTurnsLocked(ks, p.goalSession, p.goalEpoch)
	ks.queue = append(ks.queue, p)
}

// removeGoalTurnsLocked removes the queued turns of the goal of sessionID whose epoch is not
// newer than epoch. A turn with a newer epoch comes from a later goal command, so it stays.
// The caller holds g.mu.
func removeGoalTurnsLocked(ks *keyState, sessionID string, epoch int64) {
	ks.queue = slices.DeleteFunc(ks.queue, func(q *pendingTurn) bool {
		return q.goalTurn && q.goalSession == sessionID && q.goalEpoch <= epoch
	})
}

// enqueue is Enqueue. system is false for a message of the owner, for example a queued
// text that a restart resumes.
func (g *Gateway) enqueue(ctx context.Context, key, text string, system bool) error {
	p := &pendingTurn{system: system}
	p.in.Text = text
	return g.enqueueTurn(ctx, key, p)
}

// enqueueTurn queues p for key, or starts p when the key is free. The text of p is in
// p.in.Text. enqueueTurn fills the other fields of p.in and p.newRenderer from the newest
// message of the key, or else from the active session of the key.
func (g *Gateway) enqueueTurn(ctx context.Context, key string, p *pendingTurn) error {
	text := p.in.Text
	if strings.TrimSpace(text) == "" {
		return errors.New("gateway: enqueue: the text is empty")
	}
	if g.stopping.Load() {
		return ErrStopping
	}
	g.mu.Lock()
	ks := g.key(key)
	in, newRenderer := ks.last, ks.lastRenderer
	g.mu.Unlock()
	if newRenderer == nil {
		var err error
		if in, newRenderer, err = g.sendRenderer(ctx, key); err != nil {
			return err
		}
	}
	in.Key, in.Text, in.Images, in.Files, in.IsText = key, text, nil, nil, true
	// in can be a copy of a skill command. Only a text of the owner can be a slash command,
	// so a restart keeps a queued skill command, and a system text never is one. The restart
	// marker does not keep the source of a text. So a steer text that the agent did not read
	// before a restart, for example "/arxiv x" from "/steer /arxiv x", runs as a skill
	// command after the restart.
	in.Slash = !p.system && SlashCandidate(text)
	p.in, p.newRenderer = in, newRenderer
	g.mu.Lock()
	if g.stopping.Load() {
		g.mu.Unlock()
		return ErrStopping
	}
	if ks.busy {
		if p.goalTurn {
			queueGoalTurnLocked(ks, p)
		} else {
			ks.queue = append(ks.queue, p)
		}
		op := g.markerUpsertOp(ks, key)
		g.mu.Unlock()
		g.sendMarker(op)
		return nil
	}
	ks.busy = true
	ks.take(p)
	g.start(ctx, p)
	g.mu.Unlock()
	return nil
}

// Running returns the state of the running turn of key.
func (g *Gateway) Running(key string) (*RunState, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ks, ok := g.keys[key]; ok && ks.running != nil {
		return ks.running, true
	}
	return nil, false
}

// GoalWaker returns the goal waker of the gateway. Its Fire enqueues the continuation, and
// BeginStopping stops it.
func (g *Gateway) GoalWaker() *GoalWaker {
	g.wakerOnce.Do(func() {
		w := &GoalWaker{Log: g.Log}
		if g.Deps != nil {
			w.Goals = g.Deps.Goals
			w.Log = g.logger()
		}
		w.Fire = func(ctx context.Context, key, _ string, c goals.Continuation) error {
			return g.enqueueGoal(ctx, key, c)
		}
		g.waker = w
	})
	return g.waker
}

// Wait waits until no turn runs, no submitted message waits for its turn, and no busy text
// waits in the steer debouncer, or until timeout. It returns false at the timeout.
func (g *Gateway) Wait(timeout time.Duration) bool {
	end := time.Now().Add(timeout)
	// The debouncer comes first: a flush starts its turn before it stops to count, so the
	// active count that Wait reads next includes that turn.
	for g.debouncer().Pending() || g.active.Load() > 0 {
		if time.Now().After(end) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

// key returns the state of key and creates it when necessary. The caller holds g.mu.
func (g *Gateway) key(key string) *keyState {
	if g.keys == nil {
		g.keys = map[string]*keyState{}
	}
	ks, ok := g.keys[key]
	if !ok {
		ks = &keyState{}
		g.keys[key] = ks
	}
	return ks
}

// start runs p in a new goroutine. The caller owns the key.
func (g *Gateway) start(ctx context.Context, p *pendingTurn) {
	g.active.Add(1)
	go func() {
		defer g.active.Add(-1)
		g.run(ctx, p)
	}()
}

// run runs the turn of p and then gives the key to the next message in the queue. The
// caller owns the key.
func (g *Gateway) run(ctx context.Context, p *pendingTurn) {
	g.active.Add(1)
	defer g.active.Add(-1)
	end := g.turn(ctx, p)
	// The goal step runs while the key is still busy, so an owner text that comes during
	// the step goes into the steer queue of the run, and the steer turn runs first.
	cont := g.goalStep(ctx, p, end)
	g.mu.Lock()
	ks := g.key(p.in.Key)
	var pending string
	var closes []ackClose
	rs := ks.running
	if rs != nil {
		rs.ended = true
		// The steer that the turn did not deliver is in the queue of the run. A steer that
		// came after the last check of the agent is there too, so it is not lost.
		pending = rs.Steer.Drain()
		// While the gateway stops, BeginStopping closes the acks.
		if text, ok := steerEndText(pending, p.discard, p.takeOver, g.stopping.Load()); ok {
			closes = g.closeSteerAcksLocked(rs, math.MaxUint64, text)
		}
	}
	ks.running = nil
	if pending != "" && !p.discard && g.stopping.Load() {
		// BeginStopping saved the steer that was in the queue of the run. Only a steer that
		// the agent drained at the same time can come here.
		g.logger().Warn("gateway: a steer text is lost in the stop", "key", p.in.Key)
	}
	// A stop drops the steer, because the run that it guides has ended (Hermes). A
	// take-over keeps it, because the owner asked for the waiting text to run now.
	if pending != "" && (!p.discard || p.takeOver && !g.stopping.Load()) {
		// The steer that the turn did not deliver becomes the next turn, before the queue.
		in := p.in
		in.Text, in.Images, in.Files, in.IsText = pending, nil, nil, true
		// p can be a skill command. A steer text is never one.
		in.Slash = false
		ks.queue = append([]*pendingTurn{{in: in, newRenderer: p.newRenderer}}, ks.queue...)
	}
	// The continuation goes to the end of the queue, after the leftover steer and each
	// queued owner message. It replaces a goal turn that a wake queued during the step.
	// /stop or the stop of the gateway can come during the step, so the check runs again
	// under g.mu. A goal turn has no marker text, so it needs no marker op.
	if cont != nil && !p.discard && (rs == nil || !rs.stopped) && !g.stopping.Load() {
		queueGoalTurnLocked(ks, g.goalTurnLocked(ks, p, *cont))
	}
	g.next(ctx, p.in.Key, ks)
	for _, c := range closes {
		c.run()
	}
}

// next gives the key to the next message in the queue, or frees the key. A queued slash
// command is dropped. The caller holds g.mu, and next unlocks it. A free key loses its
// marker. While the gateway stops, next does nothing: BeginStopping saved the queue in the
// marker, and the key stays busy, so the marker stays.
func (g *Gateway) next(ctx context.Context, key string, ks *keyState) {
	var op markerOp
	var started []ackClose
	// Deferred calls run in reverse order: the unlock first, then the ack edits and the
	// marker op.
	defer func() { g.sendMarker(op) }()
	defer func() {
		for _, c := range started {
			c.run()
		}
	}()
	defer g.mu.Unlock()
	if g.stopping.Load() {
		// The waiters stay in the queue and leave at the end of their context.
		return
	}
	for len(ks.queue) > 0 {
		nx := ks.queue[0]
		ks.queue = ks.queue[1:]
		if isCommandText(nx.in) {
			nx.discard = true
			started = append(started, g.releaseAckLocked(nx, NotSentAck))
			started = append(started, g.releaseSteerAcksLocked(nx, NotSentAck)...)
			g.logger().Info("gateway: a queued slash command is dropped", "key", nx.in.Key)
			if nx.ready != nil {
				close(nx.ready)
			}
			continue
		}
		ks.take(nx)
		started = append(started, g.releaseAckLocked(nx, QueueStartedAck))
		// A steer that /now moved here runs as a new message now.
		started = append(started, g.releaseSteerAcksLocked(nx, SteerLaterAck)...)
		if nx.ready != nil {
			// The waiter of nx runs it. Count it now, so Wait does not see zero turns
			// before the waiter wakes up.
			g.active.Add(1)
			nx.handed = true
			close(nx.ready)
		} else {
			g.start(ctx, nx)
		}
		return
	}
	ks.busy, ks.current = false, nil
	op = g.markerRemoveOp(ks, key)
}

// turnEnd is the result of turn for the after-turn step. ran is true when the agent
// started. r and sess are nil when turn did not get them.
type turnEnd struct {
	res  agent.TurnResult
	err  error
	r    Renderer
	sess *sessions.Session
	ran  bool
}

// turn runs one agent turn. The steer text that the turn did not deliver stays in the
// steer queue of the run, and run drains it. A panic in the turn becomes an error of the
// turn, so the queue of the key goes on. A message that /stop dropped before its start
// runs no turn. While the gateway stops, no agent starts: BeginStopping saved the text of
// the message in the marker. A goal turn runs only when its goal is still current and its
// session is still the active session of the key.
func (g *Gateway) turn(ctx context.Context, p *pendingTurn) (end turnEnd) {
	g.mu.Lock()
	skip := p.discard || g.stopping.Load()
	g.mu.Unlock()
	if skip {
		return end
	}
	// IsCurrent reads only the cache of the manager, so this check makes no database call.
	if p.goalTurn && (g.Deps.Goals == nil || !g.Deps.Goals.IsCurrent(p.goalSession, p.goalEpoch)) {
		g.logger().Info("gateway: a stale goal turn is dropped", "key", p.in.Key, "session", p.goalSession, "epoch", p.goalEpoch)
		return end
	}
	r := p.newRenderer()
	end.r = r
	tctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rs := &RunState{
		Cancel:  cancel,
		Steer:   agent.NewSteerQueue(),
		Started: time.Now(),
	}
	defer func() {
		if v := recover(); v != nil {
			g.logger().Error("gateway: the turn panicked", "key", p.in.Key, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			end.err = errors.New("internal error in the turn")
			r.Done("", end.err)
		}
	}()
	sess, err := g.Deps.Store.ActiveOrCreate(tctx, p.in.Key, sessions.Session{
		Source: p.in.Source, ChatID: p.in.ChatID, ThreadID: p.in.ThreadID, UserID: p.in.UserID, Kind: "chat",
	})
	if err != nil {
		g.logger().Error("gateway: cannot get the session", "key", p.in.Key, "err", err.Error())
		end.err = err
		r.Done("", err)
		return end
	}
	end.sess = sess
	if p.goalTurn && sess.ID != p.goalSession {
		// The key has a new session, so the continuation of the old session must not run in
		// it. Done with context.Canceled sends nothing.
		g.logger().Info("gateway: a goal turn of another session is dropped", "key", p.in.Key, "session", p.goalSession, "epoch", p.goalEpoch)
		r.Done("", context.Canceled)
		return end
	}
	rs.SessionID = sess.ID
	g.mu.Lock()
	if p.discard || g.stopping.Load() {
		// /stop or the stop of the gateway came while the turn started.
		g.mu.Unlock()
		r.Done("", context.Canceled)
		return end
	}
	ks := g.key(p.in.Key)
	ks.running = rs
	// From now on the key has a marker, so a restart can continue the turn.
	ks.markerSession = sess.ID
	op := g.markerUpsertOp(ks, p.in.Key)
	g.mu.Unlock()
	g.sendMarker(op)
	// A review notice goes out as its own message after the answer.
	notify, release := agent.HoldNotices(r.Ack)
	if p.goalLine != "" {
		if gl, ok := r.(GoalLineRenderer); ok {
			gl.GoalLine(p.goalLine)
		}
	}
	end.ran = true
	res, err := g.Deps.Turn(tctx, sess, TurnInput{
		Text: p.in.Text, Images: p.in.Images, Channel: p.in.Source, Sink: r, Steer: rs.Steer, Notify: notify,
		Slash:       p.in.Slash,
		OnSteerRead: func(seq uint64) { g.steerRead(rs, seq) },
	})
	end.res, end.err = res, err
	// Put the steer that the turn did not deliver back before the answer goes out. The send
	// can take long, and BeginStopping in that time saves the steer queue in the marker.
	rs.Steer.Push(res.PendingSteer)
	// The renderer sends the files of the MEDIA tags. A TTS file goes out also when the
	// model left out its tag (Hermes _collect_auto_append_media_tags).
	r.Done(agent.AppendMediaTags(res.Final, res.Media), err)
	release()
	return end
}

// BusyCommandReply is the reply to a command that cannot run while the key is busy. %s is
// the command name.
const BusyCommandReply = "⏳ **Busy**\n`/%s` cannot run during a task. Wait for the answer, or stop the task."

// commandErrorReply is the reply to a command that failed. The first %s is the command name,
// and the second %s is the error text with its Markdown characters escaped.
const commandErrorReply = "⚠️ **/%s failed**\n%s. Try again later."

// runCommand runs one slash command for the key of in. While the key is busy, a command
// without WhileBusy gets BusyCommandReply, because it can change the session of the turn
// (Hermes). A new goal text or /goal draft on a busy key gets goalRunningReply. /new and
// /reset first stop the run of the key and drop its queue.
// It returns the rest of the command: the turn of /steer on a free key.
func (g *Gateway) runCommand(ctx context.Context, in Inbound, newRenderer func() Renderer, cmd Command, args string) (rest func() error) {
	rest = func() error { return nil }
	r := newRenderer()
	if !cmd.WhileBusy {
		g.mu.Lock()
		ks := g.key(in.Key)
		busy := ks.busy
		var sessionID string
		if ks.running != nil {
			sessionID = ks.running.SessionID
		}
		g.mu.Unlock()
		if busy {
			text := fmt.Sprintf(BusyCommandReply, cmd.Name)
			if g.busyGoalActive(ctx, in.Key, sessionID) {
				text += "\n" + GoalBusyLine
			}
			ackReply(r, Reply{Text: text, Buttons: [][]Button{stopButton}})
			return rest
		}
	}
	if cmd.Name == "goal" && g.Deps.Goals != nil {
		// The control verbs run while busy, but a new goal waits for the end of the run
		// (Hermes).
		if v := goals.ParseCommand(args).Verb; v == goals.VerbSet || v == goals.VerbDraft {
			g.mu.Lock()
			busy := g.key(in.Key).busy
			g.mu.Unlock()
			if busy {
				ackReply(r, Reply{Text: goalRunningReply})
				return rest
			}
		}
	}
	if cmd.Name == "new" || cmd.Name == "reset" {
		g.stopKey(in.Key)
	}
	sess, err := g.Deps.Store.ActiveOrCreate(ctx, in.Key, sessions.Session{
		Source: in.Source, ChatID: in.ChatID, ThreadID: in.ThreadID, UserID: in.UserID, Kind: "chat",
	})
	if err != nil {
		return func() error { return err }
	}
	// Only the StopKeep hook sets goalPaused, so the replies of the other commands do not
	// change.
	goalPaused := false
	c := &CommandCtx{
		Deps:    g.Deps,
		Key:     in.Key,
		Session: sess,
		Reply:   func(text string) { r.Ack(stopGoalText(text, goalPaused)) },
		Hooks: CommandHooks{
			BeforeNew: g.Deps.BeforeNew,
			// The pause comes before the stop. So the after-turn step finds the new epoch
			// when its write conflicts, and it drops its result.
			Stop: func() {
				paused := g.pauseGoalForStop(ctx, in.Key, sess)
				if g.stopKey(in.Key) {
					r.Ack(stopGoalText(StopReply, paused))
				} else {
					r.Ack(stopGoalText(NoTaskReply, paused))
				}
			},
			StopKeep: func() (bool, int) {
				goalPaused = g.pauseGoalForStop(ctx, in.Key, sess)
				return g.stopKeepQueue(in.Key)
			},
			Now:           func(turn int) (bool, int, bool) { return g.takeOver(in.Key, turn, in.ButtonMsgID) },
			Steer:         func(text string) { rest = g.steerCommand(ctx, in, newRenderer, r, text) },
			StartGoalTurn: func(gc goals.Continuation) { g.startGoalTurn(ctx, in, newRenderer, gc) },
			DropGoalTurns: func() { g.dropGoalTurns(in.Key) },
			ArmWake:       func(id string, st goals.State) { g.GoalWaker().Arm(in.Key, id, st) },
		},
	}
	if rr, ok := r.(ReplyRenderer); ok {
		c.Send = rr.AckReply
	}
	// The draft call can take long, so it runs in the rest. The busy refusal above comes
	// first, so a draft on a busy key does not come here.
	if cmd.Name == "goal" && g.Deps.Goals != nil {
		if gc := goals.ParseCommand(args); gc.Verb == goals.VerbDraft && gc.Usage == "" {
			return g.goalDraftRest(ctx, in, c, r, gc.Text)
		}
	}
	if err := cmd.Run(ctx, c, args); err != nil {
		g.logger().Error("gateway: the command failed", "command", cmd.Name, "key", in.Key, "err", err.Error())
		ackReply(r, Reply{Text: fmt.Sprintf(commandErrorReply, cmd.Name, EscapeMarkdown(strings.TrimSuffix(err.Error(), ".")))})
		return func() error { return err }
	}
	return rest
}

// busyGoalActive reports whether the session of a busy key has an active goal. sessionID
// is the session of the run, or "" when no run exists, and then the active session of key
// is read.
func (g *Gateway) busyGoalActive(ctx context.Context, key, sessionID string) bool {
	if g.Deps.Goals == nil {
		return false
	}
	if sessionID == "" {
		sess, err := g.Deps.Store.Active(ctx, key)
		if err != nil || sess == nil {
			return false
		}
		sessionID = sess.ID
	}
	st, ok, err := g.Deps.Goals.Load(ctx, sessionID)
	return err == nil && ok && st.Status == goals.StatusActive
}

// sendRenderer returns the inbound fields and a renderer for a key with no message in this
// process. The renderer sends the final text with the channel of the active session.
func (g *Gateway) sendRenderer(ctx context.Context, key string) (Inbound, func() Renderer, error) {
	sess, err := g.Deps.Store.Active(ctx, key)
	if err != nil {
		return Inbound{}, nil, err
	}
	if sess == nil {
		return Inbound{}, nil, fmt.Errorf("gateway: key %s has no session", key)
	}
	ch, ok := g.Channels[sess.Source]
	if !ok {
		return Inbound{}, nil, fmt.Errorf("gateway: no channel %q for key %s", sess.Source, key)
	}
	to := Target{Source: sess.Source, ChatID: sess.ChatID, ThreadID: sess.ThreadID}
	in := Inbound{Key: key, Source: sess.Source, ChatID: sess.ChatID, ThreadID: sess.ThreadID, UserID: sess.UserID, Target: to}
	log := g.logger()
	return in, func() Renderer { return &sendRenderer{ctx: ctx, ch: ch, to: to, log: log} }, nil
}

func (g *Gateway) logger() *slog.Logger {
	if g.Log != nil {
		return g.Log
	}
	return g.Deps.logger()
}

// sendRenderer sends the final text of a turn as one message, and each Ack at once.
type sendRenderer struct {
	ctx context.Context
	ch  Channel
	to  Target
	log *slog.Logger
}

func (s *sendRenderer) Delta(string)             {}
func (s *sendRenderer) ToolStart(string, string) {}
func (s *sendRenderer) ToolEnd(string, bool)     {}
func (s *sendRenderer) Boundary()                {}
func (s *sendRenderer) Notice(string)            {}

func (s *sendRenderer) Ack(text string) { s.send(text) }

// GoalLine sends the line as its own message. The turn has no status bubble, so the line
// goes out before the answer. The line can hold model text, and the channel reads Markdown,
// so the line is escaped.
func (s *sendRenderer) GoalLine(line string) { s.Ack(EscapeMarkdown(line)) }

func (s *sendRenderer) Done(final string, err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		final = strings.TrimSpace(final + "\n\nerror: " + err.Error())
	}
	s.send(final)
}

func (s *sendRenderer) send(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if err := s.ch.Send(context.WithoutCancel(s.ctx), s.to, Outbound{Text: text}); err != nil {
		s.log.Error("gateway: cannot send the answer", "source", s.to.Source, "chat", s.to.ChatID, "err", err.Error())
	}
}
