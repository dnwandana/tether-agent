package gateway

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// draftingReply goes out at once, before the draft call. Hermes sends no such line in the
// gateway, but the call can take 30 s.
const draftingReply = "Drafting completion contract…"

// errDraftDropped stops the Update of a draft that /stop dropped, so nothing is written.
var errDraftDropped = errors.New("gateway: the goal draft was stopped")

func init() {
	registerGoalVerb(goals.VerbDraft, goalDraftVerb)
}

// goalDraftVerb runs /goal draft in the command itself. The CLI uses it, because the CLI
// has no update loop.
func goalDraftVerb(ctx context.Context, c *CommandCtx, sess *sessions.Session, cmd goals.Command) (Reply, error) {
	c.reply(draftingReply)
	return draftGoal(ctx, c, sess, cmd.Text, nil)
}

// goalSetHead returns the first lines of the reply to a new goal: the set line, and the
// replaced lines when the new goal replaces another goal text. previous is the goal text of
// the active or paused goal before the change, or "". The set verb and the draft use it, so
// their replies cannot differ.
func goalSetHead(st goals.State, previous string) string {
	var b strings.Builder
	b.WriteString("⊙ Goal set (")
	b.WriteString(strconv.Itoa(st.MaxTurns))
	b.WriteString("-turn budget): ")
	b.WriteString(EscapeMarkdown(st.Goal))
	if previous != "" && previous != st.Goal {
		b.WriteString("\n(replaced the previous goal)\n  was: ")
		b.WriteString(EscapeMarkdown(previous))
	}
	return b.String()
}

// draftGoal makes the draft call for objective, sets the goal, and starts the kick turn.
// It returns the reply. When keep is not nil and returns false after the call, it sets
// nothing and returns an empty reply.
func draftGoal(ctx context.Context, c *CommandCtx, sess *sessions.Session, objective string, keep func() bool) (Reply, error) {
	m := c.Deps.Goals
	st := goals.NewState(objective, goals.Contract{}, goalMaxTurns(c.Deps), nowFunc())
	// Judge binds the record to st, so the draft call goes into the new judge session of
	// this goal.
	contract, ok := goals.DraftContract(ctx, m.Judge(sess, &st), m.JudgeProvider(sess), objective)
	if keep != nil && !keep() {
		return Reply{}, nil
	}
	if ok {
		st.Contract = contract
	}
	var previous string
	st, err := m.Update(ctx, sess.ID, func(row *goals.State, exists bool) error {
		// A /stop can come during the database work too.
		if keep != nil && !keep() {
			return errDraftDropped
		}
		previous = ""
		if exists && (row.Status == goals.StatusActive || row.Status == goals.StatusPaused) {
			previous = row.Goal
		}
		old := row.Epoch
		*row = st
		if exists {
			// A queued turn of the old goal is stale from now.
			row.Epoch = old + 1
		}
		return nil
	})
	if errors.Is(err, errDraftDropped) {
		return Reply{}, nil
	}
	if err != nil {
		return Reply{}, err
	}
	if c.Hooks.DropGoalTurns != nil {
		c.Hooks.DropGoalTurns()
	}
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
	lines := []string{goalSetHead(st, previous)}
	if !st.Contract.Empty() {
		lines = append(lines, "Drafted completion contract:", EscapeMarkdown(st.Contract.Render(nil)),
			"Tighten any field by re-setting the goal with inline lines (e.g. verify: <command>), then /goal resume. Use /goal show to review.")
	} else {
		lines = append(lines, "Couldn't draft a contract (aux model unavailable) — running as a "+
			"free-form goal. The per-turn judge still applies.")
	}
	return Reply{Text: strings.Join(lines, "\n")}, nil
}

// goalDraftRest takes the key for the draft of objective and returns the rest of the
// command, which makes the draft call. The key stays busy until the call ends, and then
// the queue of the key goes on.
func (g *Gateway) goalDraftRest(ctx context.Context, in Inbound, c *CommandCtx, r Renderer, objective string) (rest func() error) {
	g.mu.Lock()
	if g.stopping.Load() {
		g.mu.Unlock()
		return func() error { return ErrStopping }
	}
	ks := g.key(in.Key)
	if ks.busy {
		g.mu.Unlock()
		ackReply(r, Reply{Text: goalRunningReply})
		return func() error { return nil }
	}
	// /stop cancels dctx, so the key does not wait for the end of the model call.
	dctx, cancel := context.WithCancel(ctx)
	p := &pendingTurn{in: in, newRenderer: func() Renderer { return r }, cancel: cancel}
	ks.busy = true
	ks.take(p)
	// The draft counts as active from now, so Wait does not miss it.
	g.active.Add(1)
	g.mu.Unlock()
	r.Ack(draftingReply)
	return func() (err error) {
		defer g.active.Add(-1)
		defer cancel()
		// next runs the kick turn and the messages that came during the call, in queue
		// order. A panic in the call also frees the key.
		defer func() {
			g.mu.Lock()
			g.next(ctx, in.Key, g.key(in.Key))
		}()
		defer func() {
			if v := recover(); v != nil {
				g.logger().Error("gateway: the goal draft panicked", "key", in.Key, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
				err = errors.New("internal error in the goal draft")
				// The user saw the drafting line, so the user must also see the end.
				ackReply(r, draftErrorReply(err))
			}
		}()
		// A /stop during the call drops the result. A stop of the gateway drops it too,
		// because no turn starts while the gateway stops.
		keep := func() bool {
			g.mu.Lock()
			defer g.mu.Unlock()
			return !p.discard && !g.stopping.Load()
		}
		rep, err := draftGoal(dctx, c, c.Session, objective, keep)
		if err != nil && !keep() {
			// The /stop cancel can fail the database work after the last keep check. The
			// user stopped the draft, so no error goes out.
			return nil
		}
		if err != nil {
			g.logger().Error("gateway: the goal draft failed", "key", in.Key, "err", err.Error())
			ackReply(r, draftErrorReply(err))
			return err
		}
		if rep.Text != "" {
			c.send(rep)
		}
		return nil
	}
}

// draftErrorReply returns the reply of a draft that failed with err.
func draftErrorReply(err error) Reply {
	return Reply{Text: fmt.Sprintf(commandErrorReply, "goal", EscapeMarkdown(strings.TrimSuffix(err.Error(), ".")))}
}
