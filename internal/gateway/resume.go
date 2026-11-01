package gateway

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// ResumeLoopText goes to the chat of a key whose turn a restart cut too many times in a row.
const ResumeLoopText = "⚠️ I stopped the automatic resume after 3 restarts in a row. Send a message to continue."

// LostMediaLine replaces the images and the files of a queued message, because a turn
// marker keeps the text only.
const LostMediaLine = "[A photo or a file of this message was lost in a restart.]"

// maxResumes is the number of resumes in a row after which a key does not resume again. A
// turn that crashes the gateway each time must not run forever. Hermes uses a window of 1 h
// and a limit of 3.
const maxResumes = 3

// ResumeOutcome is the result of the resume of one turn marker.
type ResumeOutcome int

const (
	// ResumeNone means that the key did not resume: its session changed, or the
	// enqueue failed.
	ResumeNone ResumeOutcome = iota
	// ResumeResumed means that the cut turn runs again after agent.ResumeNote.
	ResumeResumed
	// ResumeTooOld means that the marker was older than the freshness, or the resume is
	// off.
	ResumeTooOld
	// ResumeLoopStopped means that the key reached maxResumes.
	ResumeLoopStopped
	// ResumeInStep means that the restart came during the goal step, after the reply.
	ResumeInStep
)

// reasonGatewayDown is the pause reason of a goal whose gateway was down longer than the
// resume freshness.
const reasonGatewayDown = "the gateway was down too long"

// errBarrierChanged makes Update write nothing when the barrier of the goal changed after
// the goal read.
var errBarrierChanged = errors.New("gateway: the barrier of the goal changed")

// ResumeInterrupted continues the turns that a restart cut. For each fresh marker, it
// enqueues agent.ResumeNote and then each queued text of the marker. It never runs a tool
// call again: the only new turns are the enqueued texts.
//
// cut is the time when the last gateway stopped. A zero cut uses the UpdatedAt of each
// marker. A marker older than freshness is deleted. A zero freshness deletes every marker
// and runs no turn. It returns the outcome of each marker key, for ResumeGoals. Only an
// error of the marker read is returned. The other errors are logged, and the next marker
// continues.
func (g *Gateway) ResumeInterrupted(ctx context.Context, cut time.Time, freshness time.Duration) (map[string]ResumeOutcome, error) {
	outcomes := map[string]ResumeOutcome{}
	if g.Deps == nil || g.Deps.Store == nil {
		return outcomes, nil
	}
	// Markers closes its rows before it returns, so the loop below can query the database.
	markers, err := g.Deps.Store.Markers(ctx)
	if err != nil {
		return outcomes, fmt.Errorf("gateway: resume the cut turns: %w", err)
	}
	log := g.logger()
	now := time.Now()
	for _, m := range markers {
		at := cut
		if at.IsZero() {
			at = m.UpdatedAt
		}
		switch {
		case freshness <= 0:
			log.Info("gateway: the resume is off, so the turn does not resume", "key", m.SessionKey)
			g.deleteMarker(ctx, m.SessionKey)
			outcomes[m.SessionKey] = ResumeTooOld
		case now.Sub(at) > freshness:
			log.Info("gateway: a turn marker is too old, so the turn does not resume",
				"key", m.SessionKey, "age", now.Sub(at).Round(time.Second).String())
			g.deleteMarker(ctx, m.SessionKey)
			outcomes[m.SessionKey] = ResumeTooOld
		case m.Resumes >= maxResumes:
			log.Warn("gateway: the turn resumed too many times in a row, so it does not resume",
				"key", m.SessionKey, "resumes", m.Resumes)
			g.deleteMarker(ctx, m.SessionKey)
			g.sendResumeLoop(ctx, m.SessionKey)
			outcomes[m.SessionKey] = ResumeLoopStopped
		default:
			outcomes[m.SessionKey] = g.resumeMarker(ctx, m)
		}
	}
	return outcomes, nil
}

// resumeMarker enqueues the note turn and the queued texts of m. A key with a different
// active session does not resume, because the user started a new session after the cut.
// A key with no session goes to Enqueue, which fails, so the marker is deleted. A marker
// in the goal step gets no note, because the reply of its turn went out before the cut.
func (g *Gateway) resumeMarker(ctx context.Context, m sessions.TurnMarker) ResumeOutcome {
	log := g.logger()
	sess, err := g.Deps.Store.Active(ctx, m.SessionKey)
	if err != nil {
		log.Warn("gateway: cannot resume the turn", "key", m.SessionKey, "err", err.Error())
		g.deleteMarker(ctx, m.SessionKey)
		return ResumeNone
	}
	if sess != nil && sess.ID != m.SessionID {
		log.Info("gateway: the active session of the key changed, so the turn does not resume",
			"key", m.SessionKey, "session", m.SessionID, "active", sess.ID)
		g.deleteMarker(ctx, m.SessionKey)
		return ResumeNone
	}
	if m.InStep {
		return g.resumeInStep(ctx, m)
	}
	// The count goes up before the turn starts, so a crash in the resumed turn counts too.
	// The marker write of the turn keeps the count.
	if err := g.Deps.Store.SetMarkerResumes(ctx, m.SessionKey, m.Resumes+1); err != nil {
		log.Warn("gateway: cannot resume the turn", "key", m.SessionKey, "err", err.Error())
		g.deleteMarker(ctx, m.SessionKey)
		return ResumeNone
	}
	if err := g.Enqueue(ctx, m.SessionKey, agent.ResumeNote); err != nil {
		log.Warn("gateway: cannot resume the turn", "key", m.SessionKey, "err", err.Error())
		g.deleteMarker(ctx, m.SessionKey)
		return ResumeNone
	}
	log.Info("gateway: resume the cut turn", "key", m.SessionKey, "session", m.SessionID,
		"resumes", m.Resumes+1, "queued", len(m.Queued))
	g.enqueueQueued(ctx, m)
	return ResumeResumed
}

// resumeInStep resumes a marker that a restart cut in the goal step. It clears the in_step
// flag, because the next turn of the key writes the same row and must not keep the flag.
// It enqueues only the queued texts. ResumeGoals gives the goal its continuation.
func (g *Gateway) resumeInStep(ctx context.Context, m sessions.TurnMarker) ResumeOutcome {
	log := g.logger()
	if err := g.Deps.Store.SetMarkerInStep(ctx, m.SessionKey, false); err != nil {
		log.Warn("gateway: cannot clear the goal step flag of the turn marker", "key", m.SessionKey, "err", err.Error())
	}
	if len(m.Queued) == 0 {
		g.deleteMarker(ctx, m.SessionKey)
		return ResumeInStep
	}
	if err := g.Deps.Store.SetMarkerResumes(ctx, m.SessionKey, m.Resumes+1); err != nil {
		log.Warn("gateway: cannot resume the queued messages", "key", m.SessionKey, "err", err.Error())
		g.deleteMarker(ctx, m.SessionKey)
		return ResumeNone
	}
	log.Info("gateway: resume the queued messages after the goal step", "key", m.SessionKey,
		"session", m.SessionID, "resumes", m.Resumes+1, "queued", len(m.Queued))
	g.enqueueQueued(ctx, m)
	return ResumeInStep
}

// enqueueQueued enqueues each queued text of m. The first turn runs now and writes the
// marker of the key, so the marker is not deleted here. The text is a message of the
// owner, so /now counts it.
func (g *Gateway) enqueueQueued(ctx context.Context, m sessions.TurnMarker) {
	for _, q := range m.Queued {
		text := resumeText(q)
		if text == "" {
			continue
		}
		if err := g.enqueue(ctx, m.SessionKey, text, false); err != nil {
			g.logger().Warn("gateway: cannot resume a queued message", "key", m.SessionKey, "err", err.Error())
			return
		}
	}
}

// ResumeGoals continues or pauses the active goals after a restart. It runs after
// ResumeInterrupted, with the same cut and freshness, and the outcomes of that call. A
// zero cut uses the last turn time of each goal. Only an error of the goal read is
// returned. The other errors are logged, and the next goal continues.
func (g *Gateway) ResumeGoals(ctx context.Context, cut time.Time, freshness time.Duration, outcomes map[string]ResumeOutcome) error {
	if g.Deps == nil || g.Deps.Goals == nil || g.Deps.Store == nil {
		return nil
	}
	// Active closes its rows before it returns, so each step below can query the database.
	active, err := g.Deps.Goals.Active(ctx)
	if err != nil {
		return fmt.Errorf("gateway: resume the goals: %w", err)
	}
	for _, a := range active {
		g.resumeGoal(ctx, a, cut, freshness, outcomes)
	}
	return nil
}

// resumeGoal applies the first restart rule that matches the goal a. The rule order is: a
// CLI key, a session change, a loop stop, a resumed turn, a parked goal, the age, and the
// continuation.
func (g *Gateway) resumeGoal(ctx context.Context, a goals.ActiveGoal, cut time.Time, freshness time.Duration, outcomes map[string]ResumeOutcome) {
	log := g.logger()
	sess, err := g.Deps.Store.Get(ctx, a.SessionID)
	if err != nil {
		if !errors.Is(err, sessions.ErrNotFound) {
			log.Warn("gateway: cannot resume the goal", "session", a.SessionID, "err", err.Error())
		}
		return
	}
	key := sess.Key
	// The CLI process runs its own goal loop.
	if strings.HasPrefix(key, "cli:") {
		return
	}
	pause := func(reason string) bool {
		paused, err := pauseGoal(ctx, g.Deps, a.SessionID, reason)
		if err != nil {
			log.Warn("gateway: cannot pause the goal", "key", key, "session", a.SessionID, "err", err.Error())
		}
		return paused
	}
	cur, err := g.Deps.Store.Active(ctx, key)
	if err != nil {
		log.Warn("gateway: cannot resume the goal", "key", key, "session", a.SessionID, "err", err.Error())
		return
	}
	st := a.State
	switch {
	case cur == nil || cur.ID != a.SessionID:
		log.Info("gateway: the active session of the key changed, so the goal pauses", "key", key, "session", a.SessionID)
		pause(ReasonSessionChanged)
	case outcomes[key] == ResumeLoopStopped:
		log.Info("gateway: the turn resumed too many times in a row, so the goal pauses", "key", key)
		if pause(ReasonLoop) {
			g.sendGoalNotice(ctx, key, sess, nil, goals.Notice{Kind: goals.NoticeLoopPause, Reason: ReasonLoop, StopButtons: true})
		}
	case outcomes[key] == ResumeResumed:
		// The after-turn step of the resumed turn runs as usual.
	case hasBarrier(st):
		g.rearmGoal(ctx, key, a.SessionID, st)
	default:
		now := time.Now()
		at := cut
		if at.IsZero() {
			sec := st.LastTurnAt
			if sec == 0 {
				sec = st.CreatedAt
			}
			at = timeOfSecs(sec)
		}
		age := now.Sub(at)
		if freshness <= 0 || age > freshness {
			log.Info("gateway: the gateway was down too long, so the goal pauses", "key", key,
				"down", age.Round(time.Second).String())
			if pause(reasonGatewayDown) {
				g.sendGoalNotice(ctx, key, sess, nil, goals.Notice{Kind: goals.NoticeRestartPause, Down: age, StopButtons: true})
			}
			return
		}
		c := goals.Continuation{SessionID: a.SessionID, Text: goals.ContinuationPrompt(st, nil), Epoch: st.Epoch,
			LineKind: goals.LineContinue, Reason: "the gateway restarted.", Used: st.TurnsUsed, Max: st.MaxTurns}
		if err := g.enqueueGoal(ctx, key, c); err != nil {
			log.Warn("gateway: cannot continue the goal", "key", key, "session", a.SessionID, "err", err.Error())
		}
	}
}

// rearmGoal sets the barrier timers of the parked goal st again. The process registry is
// empty after a restart, so a process barrier with a number becomes a pid barrier. The
// barrier keeps its start time and its reason.
func (g *Gateway) rearmGoal(ctx context.Context, key, sessionID string, st goals.State) {
	if st.WaitingOnSession != nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(*st.WaitingOnSession)); err == nil && pid > 0 {
			next, err := g.Deps.Goals.Update(ctx, sessionID, func(cur *goals.State, exists bool) error {
				if !exists || cur.Status != goals.StatusActive || cur.WaitingOnSession == nil {
					return errBarrierChanged
				}
				cur.WaitingOnSession = nil
				cur.WaitingOnPID = &pid
				return nil
			})
			if err != nil {
				g.logger().Warn("gateway: cannot change the process barrier of the goal to a pid barrier",
					"key", key, "session", sessionID, "err", err.Error())
				return
			}
			st = next
		}
	}
	g.GoalWaker().Arm(key, sessionID, st)
}

// resumeText returns the turn text of a queued text. A message whose images or files were
// lost gets LostMediaLine. It returns "" when nothing is left to send.
func resumeText(q sessions.QueuedText) string {
	switch {
	case q.Lost > 0 && q.Text != "":
		return q.Text + "\n" + LostMediaLine
	case q.Lost > 0:
		return LostMediaLine
	default:
		return q.Text
	}
}

// sendResumeLoop sends ResumeLoopText to the chat of the active session of key.
func (g *Gateway) sendResumeLoop(ctx context.Context, key string) {
	in, _, err := g.sendRenderer(ctx, key)
	if err == nil {
		err = g.Channels[in.Source].Send(ctx, in.Target, Outbound{Text: ResumeLoopText})
	}
	if err != nil {
		g.logger().Warn("gateway: cannot send the resume loop message", "key", key, "err", err.Error())
	}
}

// deleteMarker deletes the marker of key directly. At boot no turn writes the marker of
// key, so the write does not need the marker writer.
func (g *Gateway) deleteMarker(ctx context.Context, key string) {
	if err := g.Deps.Store.DeleteMarker(ctx, key); err != nil {
		g.logger().Warn("gateway: cannot delete the turn marker", "key", key, "err", err.Error())
	}
}
