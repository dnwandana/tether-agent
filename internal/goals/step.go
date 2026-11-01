package goals

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/sessions"
)

// The kinds of the goal line that a continuation turn shows.
const (
	// LineContinue is a continuation after a judge verdict or a failed gate.
	LineContinue = "continue"
	// LineLifted is a continuation after a barrier lifted.
	LineLifted = "lifted"
	// LineRetry is a continuation after a failed turn.
	LineRetry = "retry"
)

// RetryDelay is the wait before the one retry after a failed or empty turn. It is the
// skip time of a failed provider in provider.Health.
const RetryDelay = 60 * time.Second

// Continuation is the next turn of a goal. The caller queues Text as a system turn and drops
// it when the goal of SessionID is not active with Epoch.
type Continuation struct {
	SessionID string
	Text      string
	Epoch     int64
	LineKind  string
	Reason    string
	Used      int
	Max       int
}

// StatusLine returns the plain-text goal line of the continuation turn.
func (c Continuation) StatusLine() string {
	switch c.LineKind {
	case LineLifted:
		return "▶ Goal barrier lifted — resuming."
	case LineRetry:
		return fmt.Sprintf("↻ Goal %d/%d: retry after a failed turn.", c.Used, c.Max)
	}
	return fmt.Sprintf("↻ Goal %d/%d: %s", c.Used, c.Max, c.Reason)
}

// The notice kinds of the goal step. NoticeParseFail and NoticeTransportFail are in judge.go.
const (
	// NoticeDone tells that the judge found the goal done.
	NoticeDone = "done"
	// NoticeBlocked tells that the judge found the goal not achievable.
	NoticeBlocked = "blocked"
	// NoticeBudget tells that the goal used all its turns.
	NoticeBudget = "budget"
	// NoticeBudgetGate tells that a gate failed on the last turn of the budget.
	NoticeBudgetGate = "budget_gate"
	// NoticeParkedJudge tells that the judge parked the goal on a barrier.
	NoticeParkedJudge = "parked_judge"
	// NoticeStillParked tells that a barrier still holds the goal after a turn.
	NoticeStillParked = "still_parked"
	// NoticeGateFailed tells that a gate failed and the goal continues.
	NoticeGateFailed = "gate_failed"
	// NoticeGateExhausted tells that a gate failed more times than its retries.
	NoticeGateExhausted = "gate_exhausted"
	// NoticeTurnFailed tells that a goal turn failed.
	NoticeTurnFailed = "turn_failed"
	// NoticeRestartPause tells that a restart paused the goal.
	NoticeRestartPause = "restart_pause"
	// NoticeLoopPause tells that the turn loop guard paused the goal.
	NoticeLoopPause = "loop_pause"
)

// Notice is one goal event for the owner. The gateway formats it. Only the fields of its
// Kind are set.
type Notice struct {
	Kind        string
	Reason      string
	Used        int
	Max         int
	Count       int
	Target      string
	Command     string
	Attempt     int
	MaxRetries  int
	ExitCode    int
	Err         string
	Down        time.Duration
	StopButtons bool
}

// TurnOutcome is the end of one turn, the input of the after-turn step.
type TurnOutcome struct {
	Key     string
	Session *sessions.Session
	Final   string
	Err     error
}

// StepResult is the output of the after-turn step. Ran is false when the session has no
// active goal.
type StepResult struct {
	Ran          bool
	Continuation *Continuation
	Notices      []Notice
	Parked       bool
	RetryAfter   time.Duration
}

// stepRun is the slow part of one step: the gate runs and the judge call. The step computes
// it once, so a write conflict never runs a gate or the judge a second time.
type stepRun struct {
	gates        GateOutcome
	gateRuns     []Gate
	verdict      Verdict
	pidAlive     bool
	batches      int
	judgeSession string
}

// AfterTurn runs the goal step after one turn of out.Key, in the Hermes order: the parked
// check, the count, the gates, the judge, and the write. It returns the continuation and
// the notices. The caller queues the continuation and sends the notices.
func (m *Manager) AfterTurn(ctx context.Context, out TurnOutcome) StepResult {
	if out.Session == nil || (out.Session.Kind != "" && out.Session.Kind != "chat") {
		return StepResult{} // a cron session and a delegate_task child have no goal
	}
	sid := out.Session.ID
	st, rev, err := m.d.Store.Get(ctx, sid)
	if err != nil {
		m.logger().Warn("goals: cannot read the goal for the after-turn step", "session", sid, "err", err.Error())
		return StepResult{}
	}
	m.remember(sid, st, rev)
	if rev == 0 || st.Status != StatusActive {
		return StepResult{}
	}
	now := m.now()
	env := m.WaitEnv(out.Key)
	env.Now = now
	// A barrier that holds uses no turn and no judge call. IsWaiting clears a lifted
	// barrier, and the write below saves the clear.
	if st.IsWaiting(env) {
		target := st.WaitTarget(now)
		reason := target
		if st.WaitingReason != nil {
			reason = *st.WaitingReason
		}
		return StepResult{Ran: true, Parked: true, Notices: []Notice{{Kind: NoticeStillParked, Target: target, Reason: reason}}}
	}
	// A failed or empty turn skips the judge and does not count. The first one asks for
	// one retry. The second one in a row pauses the goal.
	if out.Err != nil || strings.TrimSpace(out.Final) == "" {
		errText := "the turn gave no answer"
		if out.Err != nil {
			errText = out.Err.Error()
		}
		return m.writeStep(ctx, sid, st, rev, func(s *State, _ bool) StepResult {
			return applyFailedTurn(s, errText)
		})
	}
	run := m.runChecks(ctx, out, cloneState(st), env, now)
	return m.writeStep(ctx, sid, st, rev, func(s *State, again bool) StepResult {
		return m.applyRun(s, sid, run, env, now, again)
	})
}

// applyFailedTurn counts one failed or empty turn in s.
func applyFailedTurn(s *State, errText string) StepResult {
	s.ConsecutiveTurnFailures++
	if s.ConsecutiveTurnFailures >= 2 {
		s.SetStatus(StatusPaused, "the last turn failed: "+errText)
		return StepResult{Ran: true, Notices: []Notice{{Kind: NoticeTurnFailed, Err: errText, StopButtons: true}}}
	}
	return StepResult{Ran: true, RetryAfter: RetryDelay}
}

// cloneState copies the lists of st, so that a change to the copy does not change st.
func cloneState(st State) State {
	st.Subgoals = slices.Clone(st.Subgoals)
	st.Gates = slices.Clone(st.Gates)
	return st
}

// execFor binds the gate runner of the deps to sess.
func (m *Manager) execFor(sess *sessions.Session) ExecFunc {
	return func(ctx context.Context, command string, timeout time.Duration) (string, int, error) {
		if m.d.Exec == nil {
			return "", -1, errors.New("goals: no command runner is set for the gates")
		}
		return m.d.Exec(ctx, sess, command, timeout)
	}
}

// runChecks runs the gates and, when they pass, the judge on work, a copy of the goal row.
func (m *Manager) runChecks(ctx context.Context, out TurnOutcome, work State, env WaitEnv, now time.Time) stepRun {
	sid := out.Session.ID
	work.TurnsUsed++
	var run stepRun
	if len(work.Gates) > 0 {
		run.gates = RunGates(ctx, &work, m.execFor(out.Session))
		run.gateRuns = work.Gates
		if run.gates.Failed {
			return run // a failed gate skips the judge
		}
	}
	if m.d.RunningBatches != nil {
		run.batches = m.d.RunningBatches(out.Key)
	}
	in := JudgeInput{
		Goal:        work.Goal,
		Contract:    work.Contract,
		Subgoals:    work.Subgoals,
		Response:    out.Final,
		Delegations: run.batches,
		Now:         now,
	}
	if m.d.Procs != nil {
		for _, p := range m.d.Procs.Running(sid) {
			in.Processes = append(in.Processes, ProcLine{
				PID:     p.PID,
				Command: p.Command,
				Running: now.Sub(p.StartedAt),
				Tail:    m.d.Procs.Tail(p.PID, 480),
			})
		}
	}
	if m.d.TodoOpen != nil {
		in.TodoBlock = m.d.TodoOpen(ctx, sid)
	}
	run.verdict = m.Judge(out.Session, &work).Evaluate(ctx, m.JudgeProvider(out.Session), in)
	run.judgeSession = work.JudgeSessionID
	if v := run.verdict; v.Kind == VerdictWait && v.WaitPID > 0 && v.WaitSession == "" {
		alive := env.Alive
		if alive == nil {
			alive = PIDAlive
		}
		run.pidAlive = alive(v.WaitPID)
	}
	return run
}

// writeStep writes the result of apply with a compare-and-set on the revision rev of the
// first read. After a conflict, a row that is still active with the same epoch gets apply
// once more. A row with a new status or a new epoch wins, and the step drops its result.
func (m *Manager) writeStep(ctx context.Context, sid string, st State, rev int64, apply func(st *State, again bool) StepResult) StepResult {
	epoch := st.Epoch
	for attempt := range 2 {
		res := apply(&st, attempt > 0)
		newRev, err := m.d.Store.Put(ctx, sid, st, rev)
		if err == nil {
			m.remember(sid, st, newRev)
			return res
		}
		if !errors.Is(err, ErrConflict) {
			m.logger().Warn("goals: cannot save the after-turn step", "session", sid, "err", err.Error())
			return StepResult{Ran: true}
		}
		// A command changed the goal during the step. A pause, a clear, a resume, and a new
		// goal change the status or the epoch, and they win.
		cur, curRev, err := m.d.Store.Get(ctx, sid)
		if err != nil {
			m.logger().Warn("goals: cannot read the goal after a conflict", "session", sid, "err", err.Error())
			return StepResult{Ran: true}
		}
		m.remember(sid, cur, curRev)
		if curRev == 0 || cur.Status != StatusActive || cur.Epoch != epoch {
			return StepResult{Ran: true}
		}
		st, rev = cur, curRev
	}
	return StepResult{Ran: true}
}

// applyRun applies the gates and the verdict of run to the row s. The verdict order is the
// Hermes order: wait, blocked, done, the failure counters, the budget, and continue.
func (m *Manager) applyRun(s *State, sid string, run stepRun, env WaitEnv, now time.Time, again bool) StepResult {
	s.TurnsUsed++
	s.LastTurnAt = secsOf(now)
	s.ConsecutiveTurnFailures = 0 // the turn gave an answer
	for i := range min(len(s.Gates), len(run.gateRuns)) {
		if s.Gates[i].Command == run.gateRuns[i].Command {
			s.Gates[i].Attempts = run.gateRuns[i].Attempts
			s.Gates[i].LastExitCode = run.gateRuns[i].LastExitCode
			s.Gates[i].LastOutputTail = run.gateRuns[i].LastOutputTail
		}
	}
	if again && s.IsWaiting(env) {
		return StepResult{Ran: true, Parked: true} // the owner parked the goal during the step
	}
	budgetReason := func() string { return fmt.Sprintf("turn budget exhausted (%d/%d)", s.TurnsUsed, s.MaxTurns) }

	if run.gates.Failed {
		f := run.gates.Failure
		switch {
		case run.gates.Exhausted:
			s.SetStatus(StatusPaused, fmt.Sprintf("quality gate exhausted %d retries: $ %s", f.Attempt-1, f.Command))
			return StepResult{Ran: true, Notices: []Notice{{Kind: NoticeGateExhausted, Command: f.Command,
				MaxRetries: f.MaxRetries, ExitCode: f.ExitCode, StopButtons: true}}}
		case s.TurnsUsed >= s.MaxTurns:
			s.SetStatus(StatusPaused, budgetReason())
			return StepResult{Ran: true, Notices: []Notice{{Kind: NoticeBudgetGate, Used: s.TurnsUsed, Max: s.MaxTurns, StopButtons: true}}}
		}
		return StepResult{
			Ran: true,
			Continuation: &Continuation{SessionID: sid, Text: ContinuationPrompt(*s, &f), Epoch: s.Epoch, LineKind: LineContinue,
				Reason: fmt.Sprintf("gate failed (exit %d): $ %s", f.ExitCode, f.Command), Used: s.TurnsUsed, Max: s.MaxTurns},
			Notices: []Notice{{Kind: NoticeGateFailed, Used: s.TurnsUsed, Max: s.MaxTurns, Command: f.Command,
				Attempt: f.Attempt, MaxRetries: f.MaxRetries, ExitCode: f.ExitCode}},
		}
	}

	v := run.verdict
	kindCopy, reasonCopy := v.Kind, v.Reason
	s.LastVerdict, s.LastReason = &kindCopy, &reasonCopy
	if s.JudgeSessionID == "" {
		s.JudgeSessionID = run.judgeSession
	}
	kind := ApplyCounters(s, v)

	if v.Kind == VerdictWait {
		target := ""
		switch {
		case v.WaitSession != "":
			s.WaitOnSession(v.WaitSession, v.Reason, now)
			target = "session " + v.WaitSession
		case v.WaitPID > 0 && run.pidAlive:
			s.WaitOnPID(v.WaitPID, v.Reason, now)
			target = fmt.Sprintf("pid %d", v.WaitPID)
		case v.WaitPID <= 0 && v.WaitSeconds > 0:
			s.WaitFor(v.WaitSeconds, run.batches, v.Reason, now)
			target = fmt.Sprintf("%ds", int(v.WaitSeconds))
		}
		if target != "" {
			return StepResult{Ran: true, Parked: true, Notices: []Notice{{Kind: NoticeParkedJudge, Target: target, Reason: v.Reason}}}
		}
		// A wait on a pid that does not run goes on like continue.
	}

	switch {
	case v.Kind == VerdictBlocked:
		s.SetStatus(StatusPaused, "judged unachievable: "+v.Reason)
		return StepResult{Ran: true, Notices: []Notice{{Kind: NoticeBlocked, Reason: v.Reason, StopButtons: true}}}
	case v.Kind == VerdictDone:
		s.SetStatus(StatusDone, "")
		return StepResult{Ran: true, Notices: []Notice{{Kind: NoticeDone, Reason: v.Reason}}}
	case kind == NoticeTransportFail:
		s.SetStatus(StatusPaused, fmt.Sprintf("the judge call failed %d times", s.ConsecutiveTransportFailures))
		return StepResult{Ran: true, Notices: []Notice{{Kind: kind, Count: s.ConsecutiveTransportFailures, StopButtons: true}}}
	case kind == NoticeParseFail:
		s.SetStatus(StatusPaused, fmt.Sprintf("the judge reply could not be parsed %d times", s.ConsecutiveParseFailures))
		return StepResult{Ran: true, Notices: []Notice{{Kind: kind, Count: s.ConsecutiveParseFailures, StopButtons: true}}}
	case s.TurnsUsed >= s.MaxTurns:
		s.SetStatus(StatusPaused, budgetReason())
		return StepResult{Ran: true, Notices: []Notice{{Kind: NoticeBudget, Used: s.TurnsUsed, Max: s.MaxTurns, StopButtons: true}}}
	}
	return StepResult{Ran: true, Continuation: &Continuation{SessionID: sid, Text: ContinuationPrompt(*s, nil), Epoch: s.Epoch,
		LineKind: LineContinue, Reason: v.Reason, Used: s.TurnsUsed, Max: s.MaxTurns}}
}
