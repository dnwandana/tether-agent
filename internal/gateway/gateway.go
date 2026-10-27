package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/config"
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
}

// Renderer shows one turn on a channel. The channel builds one for each turn.
type Renderer interface {
	agent.Sink
	// Done receives the final text of the turn and its error.
	Done(final string, err error)
	// Ack sends a short text at once, for example a command reply.
	Ack(text string)
}

// RunState describes the running turn of a key.
type RunState struct {
	// Cancel stops the turn (/stop).
	Cancel context.CancelFunc
	// Steer receives the guidance for the running turn.
	Steer   *agent.SteerQueue
	Started time.Time
	// Round returns the current tool round and the maximum number of rounds.
	Round func() (int, int)
	// Tool returns the name of the running tool, or "".
	Tool func() string
	// SessionID is the session of the turn. The busy acks are limited per session.
	SessionID string

	// ended is true after the turn returned, and stopped is true after /stop. A run in
	// either state takes no steer. Gateway.mu guards both.
	ended, stopped bool
}

// Gateway routes the inbound messages of all channels to the agent. It runs one turn for
// each key at a time, and it queues the other messages of a busy key.
type Gateway struct {
	Deps     *Deps
	Channels map[string]Channel
	Log      *slog.Logger

	mu   sync.Mutex
	keys map[string]*keyState
	// active counts the turns that run or are about to run, for a graceful stop.
	active atomic.Int64
	// now is the clock of the busy acks. Nil uses time.Now.
	now func() time.Time
	// debounce merges the busy texts of a key. Nil uses 350 ms and a hard cap of 1 s.
	debounce *Debouncer
	// lastAck and hinted hold the last busy ack time and the hint flag per session.
	lastAck map[string]time.Time
	hinted  map[string]bool
}

// keyState is the state of one session key. Gateway.mu guards it.
type keyState struct {
	// busy is true from the start of a turn until the queue is empty. running is set
	// while the agent runs.
	busy    bool
	running *RunState
	// current is the message that owns the key: the turn that runs or starts.
	current *pendingTurn
	// queue holds the messages that wait for the key, in order.
	queue []*pendingTurn
	// last is the newest message of the key and its renderer factory, for Enqueue.
	last         Inbound
	lastRenderer func() Renderer
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
	// handed is true when next gave the key to the message. Then the message counts in
	// Gateway.active, and its waiter must run it, so the key goes on. Gateway.mu guards it.
	handed bool
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
// keeps their order, and a long turn does not block the next message.
func (g *Gateway) Submit(ctx context.Context, in Inbound, newRenderer func() Renderer, done func(error)) {
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
	return g.acceptMessage(ctx, in, newRenderer)
}

// acceptMessage takes in: it gives the free key to in, steers in into the busy key, or
// queues in. It returns the rest, which waits in the queue and runs the turn.
func (g *Gateway) acceptMessage(ctx context.Context, in Inbound, newRenderer func() Renderer) (rest func() error) {
	g.mu.Lock()
	ks := g.key(in.Key)
	ks.last, ks.lastRenderer = in, newRenderer
	if !ks.busy {
		p := &pendingTurn{in: in, newRenderer: newRenderer}
		ks.busy, ks.current = true, p
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
		g.mu.Unlock()
		// Several texts in a short time become one steer (Hermes debounce).
		g.debouncer().Add(in.Key, in.Text, func(merged string) {
			m := in
			m.Text = merged
			g.flushBusyText(ctx, m, newRenderer)
		})
		return func() error { return nil }
	}
	p := &pendingTurn{in: in, newRenderer: newRenderer, ready: make(chan struct{})}
	ks.queue = append(ks.queue, p)
	rs := ks.running
	g.mu.Unlock()
	// A slash text is dropped when it leaves the queue, so it gets no ack.
	if !isCommandText(in) {
		g.ack(rs, in.Key, QueueAck, newRenderer())
	}
	return func() error {
		select {
		case <-p.ready:
		case <-ctx.Done():
			g.mu.Lock()
			for i, q := range ks.queue {
				if q == p {
					ks.queue = append(ks.queue[:i], ks.queue[i+1:]...)
					g.mu.Unlock()
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
// key, or else to the channel and the chat of the active session of the key.
func (g *Gateway) Enqueue(ctx context.Context, key, text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("gateway: enqueue: the text is empty")
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
	p := &pendingTurn{in: in, newRenderer: newRenderer}
	g.mu.Lock()
	defer g.mu.Unlock()
	if ks.busy {
		ks.queue = append(ks.queue, p)
		return nil
	}
	ks.busy, ks.current = true, p
	g.start(ctx, p)
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

// Wait waits until no turn runs and no submitted message waits for its turn, or until
// timeout. It returns false at the timeout. A busy text in the steer debouncer does not
// count.
func (g *Gateway) Wait(timeout time.Duration) bool {
	end := time.Now().Add(timeout)
	for g.active.Load() > 0 {
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
	pending := g.turn(ctx, p)
	g.mu.Lock()
	ks := g.key(p.in.Key)
	if rs := ks.running; rs != nil {
		rs.ended = true
		// A steer that came after the last check of the agent is not lost.
		if late := rs.Steer.Drain(); late != "" {
			pending = strings.TrimSpace(pending + "\n" + late)
		}
	}
	ks.running = nil
	if pending != "" && !p.discard {
		// The steer that the turn did not deliver becomes the next turn, before the queue.
		in := p.in
		in.Text, in.Images, in.Files, in.IsText = pending, nil, nil, true
		ks.queue = append([]*pendingTurn{{in: in, newRenderer: p.newRenderer}}, ks.queue...)
	}
	g.next(ctx, ks)
}

// next gives the key to the next message in the queue, or frees the key. A queued slash
// command is dropped. The caller holds g.mu, and next unlocks it.
func (g *Gateway) next(ctx context.Context, ks *keyState) {
	defer g.mu.Unlock()
	for len(ks.queue) > 0 {
		nx := ks.queue[0]
		ks.queue = ks.queue[1:]
		if isCommandText(nx.in) {
			nx.discard = true
			g.logger().Info("gateway: a queued slash command is dropped", "key", nx.in.Key)
			if nx.ready != nil {
				close(nx.ready)
			}
			continue
		}
		ks.current = nx
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
}

// turn runs one agent turn and returns the steer text that the turn did not deliver. A
// panic in the turn becomes an error of the turn, so the queue of the key goes on. A
// message that /stop dropped before its start runs no turn.
func (g *Gateway) turn(ctx context.Context, p *pendingTurn) (pendingSteer string) {
	g.mu.Lock()
	discarded := p.discard
	g.mu.Unlock()
	if discarded {
		return ""
	}
	r := p.newRenderer()
	tctx, cancel := context.WithCancel(ctx)
	defer cancel()
	tr := &trackSink{Sink: r}
	rs := &RunState{
		Cancel:  cancel,
		Steer:   agent.NewSteerQueue(),
		Started: time.Now(),
		Round:   func() (int, int) { return tr.round(), g.maxTurns() },
		Tool:    tr.tool,
	}
	defer func() {
		if v := recover(); v != nil {
			g.logger().Error("gateway: the turn panicked", "key", p.in.Key, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			r.Done("", errors.New("internal error in the turn"))
			pendingSteer = rs.Steer.Drain()
		}
	}()
	sess, err := g.Deps.Store.ActiveOrCreate(tctx, p.in.Key, sessions.Session{
		Source: p.in.Source, ChatID: p.in.ChatID, ThreadID: p.in.ThreadID, UserID: p.in.UserID, Kind: "chat",
	})
	if err != nil {
		g.logger().Error("gateway: cannot get the session", "key", p.in.Key, "err", err.Error())
		r.Done("", err)
		return ""
	}
	rs.SessionID = sess.ID
	g.mu.Lock()
	if p.discard {
		// /stop came while the turn started.
		g.mu.Unlock()
		r.Done("", context.Canceled)
		return ""
	}
	g.key(p.in.Key).running = rs
	g.mu.Unlock()
	// A review notice goes out as its own message after the answer.
	notify, release := agent.HoldNotices(r.Ack)
	res, err := g.Deps.Turn(tctx, sess, TurnInput{
		Text: p.in.Text, Images: p.in.Images, Channel: p.in.Source, Sink: tr, Steer: rs.Steer, Notify: notify,
	})
	// The renderer sends the files of the MEDIA tags. A TTS file goes out also when the
	// model left out its tag (Hermes _collect_auto_append_media_tags).
	r.Done(agent.AppendMediaTags(res.Final, res.Media), err)
	release()
	return res.PendingSteer
}

// BusyCommandReply is the reply to a command that cannot run while the key is busy. It is
// the text of Hermes gateway/run.py; %s is the command name.
const BusyCommandReply = "⏳ Agent is running — `/%s` can't run mid-turn. Wait for the current response or `/stop` first."

// runCommand runs one slash command for the key of in. While the key is busy, a command
// without WhileBusy gets BusyCommandReply, because it can change the session of the turn
// (Hermes). /new and /reset first stop the run of the key and drop its queue.
// It returns the rest of the command: the turn of /steer on a free key.
func (g *Gateway) runCommand(ctx context.Context, in Inbound, newRenderer func() Renderer, cmd Command, args string) (rest func() error) {
	rest = func() error { return nil }
	r := newRenderer()
	if !cmd.WhileBusy {
		g.mu.Lock()
		busy := g.key(in.Key).busy
		g.mu.Unlock()
		if busy {
			r.Ack(fmt.Sprintf(BusyCommandReply, cmd.Name))
			return rest
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
	c := &CommandCtx{
		Deps:    g.Deps,
		Key:     in.Key,
		Session: sess,
		Reply:   r.Ack,
		Hooks: CommandHooks{
			BeforeNew: g.Deps.BeforeNew,
			Stop: func() {
				if g.stopKey(in.Key) {
					r.Ack(StopReply)
				} else {
					r.Ack(NoTaskReply)
				}
			},
			Steer: func(text string) { rest = g.steerCommand(ctx, in, newRenderer, r, text) },
		},
	}
	if err := cmd.Run(ctx, c, args); err != nil {
		g.logger().Error("gateway: the command failed", "command", cmd.Name, "key", in.Key, "err", err.Error())
		r.Ack("error: " + err.Error())
		return func() error { return err }
	}
	return rest
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

func (g *Gateway) maxTurns() int {
	if g.Deps.Cfg != nil && g.Deps.Cfg.Agent.MaxTurns > 0 {
		return g.Deps.Cfg.Agent.MaxTurns
	}
	return config.Default().Agent.MaxTurns
}

func (g *Gateway) logger() *slog.Logger {
	if g.Log != nil {
		return g.Log
	}
	return g.Deps.logger()
}

// trackSink passes the events to a Sink and records the tool round and the running tool.
type trackSink struct {
	agent.Sink

	mu         sync.Mutex
	boundaries int
	current    string
}

func (t *trackSink) Boundary() {
	t.mu.Lock()
	t.boundaries++
	t.mu.Unlock()
	t.Sink.Boundary()
}

func (t *trackSink) ToolStart(name, preview string) {
	t.mu.Lock()
	t.current = name
	t.mu.Unlock()
	t.Sink.ToolStart(name, preview)
}

func (t *trackSink) ToolEnd(name string, isError bool) {
	t.mu.Lock()
	if t.current == name {
		t.current = ""
	}
	t.mu.Unlock()
	t.Sink.ToolEnd(name, isError)
}

// round returns the number of the current model call: 1 before the first tool round.
func (t *trackSink) round() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.boundaries + 1
}

func (t *trackSink) tool() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.current
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
