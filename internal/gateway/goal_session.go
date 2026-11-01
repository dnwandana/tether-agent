package gateway

import (
	"context"

	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// pauseOldGoal pauses the active goal of old with ReasonSessionChanged, because the key
// moves to another session, and removes the queued goal turns of the key. A nil old or a
// nil goal manager does nothing. A failure is logged.
func pauseOldGoal(ctx context.Context, c *CommandCtx, old *sessions.Session) {
	if old == nil || c.Deps == nil || c.Deps.Goals == nil {
		return
	}
	paused, err := pauseGoal(ctx, c.Deps, old.ID, ReasonSessionChanged)
	if err != nil {
		c.Deps.logger().Warn("gateway: cannot pause the goal of the old session",
			"key", c.Key, "session", old.ID, "err", err.Error())
		return
	}
	if paused && c.Hooks.DropGoalTurns != nil {
		c.Hooks.DropGoalTurns()
	}
}

// continueGoal starts one continuation for the active goal of sess. A goal that waits on
// a barrier gets no turn: the waker or the next step lifts the barrier.
func continueGoal(ctx context.Context, c *CommandCtx, sess *sessions.Session) {
	if sess == nil || c.Deps == nil || c.Deps.Goals == nil || c.Hooks.StartGoalTurn == nil {
		return
	}
	st, ok, err := c.Deps.Goals.Load(ctx, sess.ID)
	if err != nil {
		c.Deps.logger().Warn("gateway: cannot load the goal of the resumed session",
			"key", c.Key, "session", sess.ID, "err", err.Error())
		return
	}
	if !ok || st.Status != goals.StatusActive ||
		st.WaitingOnPID != nil || st.WaitingOnSession != nil || st.WaitingUntil > 0 {
		return
	}
	c.Hooks.StartGoalTurn(goals.Continuation{
		SessionID: sess.ID,
		Text:      goals.ContinuationPrompt(st, nil),
		Epoch:     st.Epoch,
		LineKind:  goals.LineContinue,
		Reason:    "the session resumes.",
		Used:      st.TurnsUsed,
		Max:       st.MaxTurns,
	})
}
