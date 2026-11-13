package gateway

import (
	"context"
	"fmt"
	"runtime/debug"
	"slices"

	"github.com/dnwandana/tether-agent/internal/goals"
)

// goalStep runs the goal step after the turn of p. It returns the continuation to queue,
// or nil. It does not hold g.mu, so the key stays busy and an owner text steers the run.
// A panic in the step gives nil, so the queue of the key goes on, as after a panic in the
// turn.
func (g *Gateway) goalStep(ctx context.Context, p *pendingTurn, end turnEnd) (cont *goals.Continuation) {
	if g.Deps.Goals == nil || !end.ran || end.sess == nil || end.sess.Kind != "chat" || g.stopping.Load() {
		return nil
	}
	key := p.in.Key
	defer func() {
		if v := recover(); v != nil {
			g.logger().Error("gateway: the goal step panicked", "key", key, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			cont = nil
		}
	}()
	g.mu.Lock()
	skip := p.discard || p.takeOver
	rs := g.key(key).running
	if rs == nil || rs.stopped {
		skip = true
	}
	g.mu.Unlock()
	if skip {
		return nil
	}
	// This read keeps a turn with no active goal free of marker writes.
	before, ok, err := g.Deps.Goals.Load(ctx, end.sess.ID)
	if err != nil || !ok || before.Status != goals.StatusActive {
		return nil
	}
	stepCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	g.mu.Lock()
	rs.StepCancel = cancel
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		rs.StepCancel = nil
		g.mu.Unlock()
	}()
	log := g.logger()
	// The marker row of the turn must exist before the flag goes on it.
	if err := g.markerW().Flush(stepCtx); err != nil {
		log.Warn("gateway: cannot flush the turn marker before the goal step", "key", key, "err", err.Error())
	}
	if err := g.Deps.Store.SetMarkerInStep(stepCtx, key, true); err != nil {
		log.Warn("gateway: cannot mark the goal step in the turn marker", "key", key, "err", err.Error())
	}
	defer func() {
		// During a stop, the flag stays, so the next gateway sends no resume note for this
		// turn.
		if g.stopping.Load() {
			return
		}
		if err := g.Deps.Store.SetMarkerInStep(context.WithoutCancel(ctx), key, false); err != nil {
			log.Warn("gateway: cannot clear the goal step in the turn marker", "key", key, "err", err.Error())
		}
	}()
	res := g.Deps.Goals.AfterTurn(stepCtx, goals.TurnOutcome{Key: key, Session: end.sess, Final: end.res.Final, Err: end.err})
	g.applyGoalDecision(key, end.sess.ID, before.Epoch, p, rs, res)
	// The waker must know the barrier before the notices go out. A process can exit while a
	// notice is sent, and Wake ignores a session that Arm did not see.
	if res.Parked || res.RetryAfter > 0 {
		st, ok, err := g.Deps.Goals.Load(ctx, end.sess.ID)
		if err != nil {
			log.Warn("gateway: cannot read the goal to arm the waker", "key", key, "session", end.sess.ID, "err", err.Error())
		}
		if ok && res.Parked {
			g.GoalWaker().Arm(key, end.sess.ID, st)
		}
		if ok && res.RetryAfter > 0 {
			g.GoalWaker().ArmRetry(key, end.sess.ID, st.Epoch, res.RetryAfter)
		}
	}
	// The reply of the turn is already out, so each notice comes after it.
	for _, n := range res.Notices {
		g.sendGoalNotice(ctx, key, end.sess, end.r, n)
	}
	return res.Continuation
}

// applyGoalDecision makes the step result res the only open decision of the goal of
// sessionID. The step started at epoch. A goal turn that waits in the queue, or a retry
// timer, comes from an older decision. It must not run after a park, an end, or a new
// continuation of the step. The removal runs before Arm, so a wake that Arm causes keeps
// its turn. A result with no decision, for example after a failed write, keeps the queue.
// A still-parked result also keeps the queue. A wake can queue its turn after the step
// saw the barrier hold, and the barrier is clear after that, so no later Arm wakes the goal.
func (g *Gateway) applyGoalDecision(key, sessionID string, epoch int64, p *pendingTurn, rs *RunState, res goals.StepResult) {
	decided := res.Ran && (res.Continuation != nil || newPark(res) || res.RetryAfter > 0 ||
		!g.Deps.Goals.IsCurrent(sessionID, epoch))
	if !decided {
		return
	}
	g.mu.Lock()
	// /stop or the stop of the gateway owns the queue after it came.
	if p.discard || rs.stopped || g.stopping.Load() {
		g.mu.Unlock()
		return
	}
	removeGoalTurnsLocked(g.key(key), sessionID, epoch)
	g.mu.Unlock()
	if res.RetryAfter <= 0 {
		g.GoalWaker().StopRetry(sessionID)
	}
}

// newPark reports if the step itself parked the goal. Only a judge park writes a new
// barrier. A barrier that still holds, or a park of the owner during the step, has no
// NoticeParkedJudge, and the owner command handles its own queue.
func newPark(res goals.StepResult) bool {
	return res.Parked && slices.ContainsFunc(res.Notices, func(n goals.Notice) bool {
		return n.Kind == goals.NoticeParkedJudge
	})
}

// goalTurnLocked returns the queued goal turn of c for the key of p. It uses the newest
// message of the key, or else the message of p. The caller holds g.mu.
func (g *Gateway) goalTurnLocked(ks *keyState, p *pendingTurn, c goals.Continuation) *pendingTurn {
	in, newRenderer := ks.last, ks.lastRenderer
	if newRenderer == nil {
		in, newRenderer = p.in, p.newRenderer
	}
	in.Key, in.Text, in.Images, in.Files, in.IsText, in.Slash = p.in.Key, c.Text, nil, nil, true, false
	// Only an owner starts a goal, so a goal turn is an owner turn.
	in.Owner = true
	return &pendingTurn{
		in: in, newRenderer: newRenderer,
		system: true, goalTurn: true, goalSession: c.SessionID, goalEpoch: c.Epoch, goalLine: c.StatusLine(),
	}
}
