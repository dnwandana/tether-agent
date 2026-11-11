package gateway

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// goalRunningReply refuses a new goal on a busy key, the Hermes text.
const goalRunningReply = "Agent is running — use /goal status / pause / clear / wait mid-run, or /stop before setting a new goal."

// reasonUserPaused is the pause reason of /goal pause.
const reasonUserPaused = "paused by the user"

// errNoGoalRow stops an Update when the verb finds no goal to change, so nothing is
// written. The command does not return it.
var errNoGoalRow = errors.New("gateway: no goal to change")

func init() {
	Register(Command{Name: "goal", Help: "Work toward a goal over several turns: /goal <text> | status | pause | resume | clear",
		Run: cmdGoal, WhileBusy: true})
	registerGoalVerb(goals.VerbStatus, goalStatus)
	registerGoalVerb(goals.VerbShow, goalShow)
	registerGoalVerb(goals.VerbSet, goalSet)
	registerGoalVerb(goals.VerbPause, goalPause)
	registerGoalVerb(goals.VerbResume, goalResume)
	registerGoalVerb(goals.VerbClear, goalClear)
}

// goalVerbFunc runs one /goal verb for sess and returns the reply. A reply with no text
// sends nothing.
type goalVerbFunc func(ctx context.Context, c *CommandCtx, sess *sessions.Session, cmd goals.Command) (Reply, error)

// goalVerbs maps a verb const of goals to its function. init functions fill it.
var goalVerbs = map[string]goalVerbFunc{}

// registerGoalVerb adds the function of verb to goalVerbs.
func registerGoalVerb(verb string, f goalVerbFunc) {
	goalVerbs[verb] = f
}

func cmdGoal(ctx context.Context, c *CommandCtx, args string) error {
	if c.Deps.Goals == nil {
		c.send(Reply{Text: "Goals unavailable on this session."})
		return nil
	}
	cmd := goals.ParseCommand(args)
	if cmd.Usage != "" {
		c.send(Reply{Text: EscapeMarkdown(cmd.Usage)})
		return nil
	}
	f, ok := goalVerbs[cmd.Verb]
	if !ok {
		c.send(Reply{Text: "/goal " + cmd.Verb + " is not available."})
		return nil
	}
	sess, err := c.current(ctx)
	if err != nil {
		return err
	}
	// The goal turn that the verb starts waits until the reply is sent, so the reply comes
	// before the answer of the turn.
	held := &heldStarts{start: c.Hooks.StartGoalTurn}
	vc := *c
	vc.Hooks.StartGoalTurn = held.add
	defer held.release()
	rep, err := f(ctx, &vc, sess, cmd)
	if err != nil {
		return err
	}
	if rep.Text != "" && cmd.Ignored != "" {
		rep.Text += "\n" + EscapeMarkdown(goals.IgnoredNote(cmd.Ignored))
	}
	if rep.Text != "" {
		c.send(rep)
	}
	return nil
}

// heldStarts holds the goal turns that a verb starts until release. After release, a start
// goes through at once.
type heldStarts struct {
	mu       sync.Mutex
	start    func(goals.Continuation)
	held     []goals.Continuation
	released bool
}

// add holds gc, or starts it at once after release. A nil start drops gc.
func (h *heldStarts) add(gc goals.Continuation) {
	h.mu.Lock()
	if !h.released {
		h.held = append(h.held, gc)
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()
	if h.start != nil {
		h.start(gc)
	}
}

// release starts the held goal turns in order.
func (h *heldStarts) release() {
	h.mu.Lock()
	held := h.held
	h.held, h.released = nil, true
	h.mu.Unlock()
	if h.start == nil {
		return
	}
	for _, gc := range held {
		h.start(gc)
	}
}

// startGoalTurn queues c as a goal turn of the key of in. The turn answers through
// newRenderer, the renderer of the command message.
func (g *Gateway) startGoalTurn(ctx context.Context, in Inbound, newRenderer func() Renderer, c goals.Continuation) {
	g.mu.Lock()
	ks := g.key(in.Key)
	ks.last, ks.lastRenderer = routeOf(in), newRenderer
	g.mu.Unlock()
	if err := g.enqueueGoal(ctx, in.Key, c); err != nil {
		g.logger().Warn("gateway: cannot start the goal turn", "key", in.Key, "session", c.SessionID, "err", err.Error())
	}
}

// lastUserText returns the text of the newest user message of the session, or "".
func lastUserText(ctx context.Context, st *sessions.Store, sessionID string) string {
	msgs, err := st.Messages(ctx, sessionID)
	if err != nil {
		return ""
	}
	for _, m := range slices.Backward(msgs) {
		if m.Msg.Role == provider.RoleUser {
			return m.Msg.Content
		}
	}
	return ""
}

// goalMaxTurns returns the turn budget of a new goal.
func goalMaxTurns(d *Deps) int {
	if d.Cfg == nil {
		return config.Default().Goals.MaxTurns
	}
	return d.Cfg.Goals.MaxTurns
}

// goalStatus replies the status line. The buttons or the command line give the next step.
func goalStatus(ctx context.Context, c *CommandCtx, sess *sessions.Session, _ goals.Command) (Reply, error) {
	st, ok, err := c.Deps.Goals.Load(ctx, sess.ID)
	if err != nil {
		return Reply{}, err
	}
	if !ok || st.Status == goals.StatusCleared {
		return Reply{Text: goals.NoGoalText}, nil
	}
	rep := Reply{Text: EscapeMarkdown(goals.StatusLine(st, nowFunc()))}
	switch st.Status {
	case goals.StatusActive:
		if c.interactive() {
			rep.Buttons = [][]Button{{{Text: "Pause", Data: "/goal pause"}, {Text: "Clear", Data: "/goal clear"}}}
		} else {
			rep.Text += "\nSend /goal pause or /goal clear."
		}
	case goals.StatusPaused:
		if c.interactive() {
			rep.Buttons = StopButtons()
		} else {
			rep.Text += "\nSend /goal resume or /goal clear."
		}
	}
	return rep, nil
}

// goalShow replies the status line and the contract block.
func goalShow(ctx context.Context, c *CommandCtx, sess *sessions.Session, _ goals.Command) (Reply, error) {
	st, ok, err := c.Deps.Goals.Load(ctx, sess.ID)
	if err != nil {
		return Reply{}, err
	}
	if !ok || st.Status == goals.StatusCleared {
		return Reply{Text: goals.NoGoalText}, nil
	}
	return Reply{Text: EscapeMarkdown(goals.ShowText(st, nowFunc()))}, nil
}

// goalSet replaces the goal of the session and starts the kick turn, like Hermes _set.
func goalSet(ctx context.Context, c *CommandCtx, sess *sessions.Session, cmd goals.Command) (Reply, error) {
	headline, contract := goals.ParseGoalText(cmd.Text)
	maxTurns := goalMaxTurns(c.Deps)
	var previous string
	st, err := c.Deps.Goals.Update(ctx, sess.ID, func(st *goals.State, exists bool) error {
		previous = ""
		if exists && (st.Status == goals.StatusActive || st.Status == goals.StatusPaused) {
			previous = st.Goal
		}
		old := st.Epoch
		*st = goals.NewState(headline, contract, maxTurns, nowFunc())
		if exists {
			// A queued turn of the old goal is stale from now.
			st.Epoch = old + 1
		}
		return nil
	})
	if err != nil {
		return Reply{}, err
	}
	if c.Hooks.DropGoalTurns != nil {
		c.Hooks.DropGoalTurns()
	}
	var b strings.Builder
	b.WriteString(goalSetHead(st, previous))
	against := ""
	if !st.Contract.Empty() {
		b.WriteString("\nCompletion contract:\n")
		b.WriteString(EscapeMarkdown(st.Contract.Render(nil)))
		against = " against the contract above"
	}
	b.WriteString("\nAfter each turn, a judge model checks if the goal is done")
	b.WriteString(against)
	b.WriteString(". Tether Agent keeps working until it is, you pause/clear it, or the budget is exhausted. " +
		"Use /goal status, /goal show, /goal pause, /goal resume, /goal clear.")
	if c.Hooks.StartGoalTurn != nil {
		c.Hooks.StartGoalTurn(goals.Continuation{
			SessionID: sess.ID,
			Text:      goals.KickPrompt(st.Goal, lastUserText(ctx, c.Deps.Store, sess.ID)),
			Epoch:     st.Epoch,
			LineKind:  goals.LineContinue,
			Reason:    "the goal starts.",
			Used:      0,
			Max:       st.MaxTurns,
		})
	}
	return Reply{Text: b.String()}, nil
}

// goalPause pauses the goal and removes its barrier and its queued turns.
func goalPause(ctx context.Context, c *CommandCtx, sess *sessions.Session, _ goals.Command) (Reply, error) {
	st, err := c.Deps.Goals.Update(ctx, sess.ID, func(st *goals.State, exists bool) error {
		if !exists || st.Status == goals.StatusCleared {
			return errNoGoalRow
		}
		st.SetStatus(goals.StatusPaused, reasonUserPaused)
		st.ClearWait()
		return nil
	})
	if errors.Is(err, errNoGoalRow) {
		return Reply{Text: "No goal set."}, nil
	}
	if err != nil {
		return Reply{}, err
	}
	if c.Hooks.DropGoalTurns != nil {
		c.Hooks.DropGoalTurns()
	}
	return Reply{Text: "⏸ Goal paused: " + EscapeMarkdown(st.Goal)}, nil
}

// goalResume makes the goal active with a new budget and starts the next turn.
func goalResume(ctx context.Context, c *CommandCtx, sess *sessions.Session, _ goals.Command) (Reply, error) {
	st, err := c.Deps.Goals.Update(ctx, sess.ID, func(st *goals.State, exists bool) error {
		if !exists || st.Status == goals.StatusCleared {
			return errNoGoalRow
		}
		st.Resume()
		return nil
	})
	if errors.Is(err, errNoGoalRow) {
		return Reply{Text: "No goal to resume."}, nil
	}
	if err != nil {
		return Reply{}, err
	}
	if c.Hooks.StartGoalTurn != nil {
		c.Hooks.StartGoalTurn(goals.Continuation{
			SessionID: sess.ID,
			Text:      goals.ContinuationPrompt(st, nil),
			Epoch:     st.Epoch,
			LineKind:  goals.LineContinue,
			Reason:    "the goal resumes.",
			Used:      st.TurnsUsed,
			Max:       st.MaxTurns,
		})
	}
	return Reply{Text: EscapeMarkdown(goals.StatusLine(st, nowFunc())) +
		"\nContinuing now — I'll take the next step right away."}, nil
}

// goalClear clears an active or paused goal. It removes the queued goal turns also when
// the session has no goal, like Hermes.
func goalClear(ctx context.Context, c *CommandCtx, sess *sessions.Session, _ goals.Command) (Reply, error) {
	_, err := c.Deps.Goals.Update(ctx, sess.ID, func(st *goals.State, exists bool) error {
		if !exists || (st.Status != goals.StatusActive && st.Status != goals.StatusPaused) {
			return errNoGoalRow
		}
		st.SetStatus(goals.StatusCleared, "")
		return nil
	})
	if c.Hooks.DropGoalTurns != nil {
		c.Hooks.DropGoalTurns()
	}
	if errors.Is(err, errNoGoalRow) {
		return Reply{Text: "No active goal."}, nil
	}
	if err != nil {
		return Reply{}, err
	}
	return Reply{Text: "✓ Goal cleared."}, nil
}
