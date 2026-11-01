// Package goals holds the /goal loop of Tether Agent: the goal state, its store, the
// parsers, the prompts, the judge, the wait barriers, the gates, and the after-turn step.
// It is a port of hermes_cli/goals.py and hermes_cli/goal_command.py. It does not import
// gateway, agent, or app. app gives it the provider, cost, batch, exec, and todo functions.
package goals

import "strings"

// The values of State.Status, as in Hermes.
const (
	StatusActive  = "active"
	StatusPaused  = "paused"
	StatusDone    = "done"
	StatusCleared = "cleared"
)

// Contract is the completion contract of a goal, the Hermes GoalContract. Each field is
// free text, and a blank field is not set.
type Contract struct {
	Outcome      string `json:"outcome"`
	Verification string `json:"verification"`
	Constraints  string `json:"constraints"`
	Boundaries   string `json:"boundaries"`
	StopWhen     string `json:"stop_when"`
}

// Empty reports whether each field is blank, like Hermes GoalContract.is_empty.
func (c Contract) Empty() bool {
	for _, f := range []string{c.Outcome, c.Verification, c.Constraints, c.Boundaries, c.StopWhen} {
		if strings.TrimSpace(f) != "" {
			return false
		}
	}
	return true
}

// Gate is a shell command that must exit 0 before the judge runs, the Hermes GoalGate.
type Gate struct {
	// Command is the shell command of the gate.
	Command string `json:"command"`
	// TimeoutSeconds is the time limit of one run.
	TimeoutSeconds int `json:"timeout_seconds"`
	// MaxRetries is the number of failed runs before the goal pauses.
	MaxRetries int `json:"max_retries"`
	// Attempts counts the failed runs.
	Attempts int `json:"attempts"`
	// LastExitCode is the exit code of the last run, or nil before the first run.
	LastExitCode *int `json:"last_exit_code"`
	// LastOutputTail is the end of the output of the last run.
	LastOutputTail string `json:"last_output_tail"`
}

// State is the goal of one session, the Hermes GoalState. Its JSON keeps the Hermes shape,
// so the times are Unix seconds as float64.
type State struct {
	// Goal is the goal text.
	Goal string `json:"goal"`
	// Status is StatusActive, StatusPaused, StatusDone, or StatusCleared.
	Status string `json:"status"`
	// TurnsUsed counts the continuation turns since the start or the last resume.
	TurnsUsed int `json:"turns_used"`
	// MaxTurns is the turn budget.
	MaxTurns int `json:"max_turns"`
	// CreatedAt is the time when the goal was set.
	CreatedAt float64 `json:"created_at"`
	// LastTurnAt is the time of the last judged turn.
	LastTurnAt float64 `json:"last_turn_at"`
	// LastVerdict is the last verdict of the judge, or nil.
	LastVerdict *string `json:"last_verdict"`
	// LastReason is the reason of the last verdict, or nil.
	LastReason *string `json:"last_reason"`
	// PausedReason is the reason of a pause, or nil.
	PausedReason *string `json:"paused_reason"`
	// ConsecutiveParseFailures counts the judge replies in a row that did not parse.
	ConsecutiveParseFailures int `json:"consecutive_parse_failures"`
	// ConsecutiveTransportFailures counts the judge API errors in a row.
	ConsecutiveTransportFailures int `json:"consecutive_transport_failures"`
	// Subgoals are the extra criteria that the user added.
	Subgoals []string `json:"subgoals"`
	// WaitingOnPID is the process of a pid barrier, or nil.
	WaitingOnPID *int `json:"waiting_on_pid"`
	// WaitingOnSession is the process session of a process barrier, or nil.
	WaitingOnSession *string `json:"waiting_on_session"`
	// WaitingUntil is the end time of a time barrier, or 0.
	WaitingUntil float64 `json:"waiting_until"`
	// WaitingOnDelegations is the number of delegation batches to wait for, or 0.
	WaitingOnDelegations int `json:"waiting_on_delegations"`
	// WaitingReason is the reason of the barrier, or nil.
	WaitingReason *string `json:"waiting_reason"`
	// WaitingSince is the start time of the barrier, or 0.
	WaitingSince float64 `json:"waiting_since"`
	// Contract is the completion contract.
	Contract Contract `json:"contract"`
	// Gates are the commands that must pass before the judge runs.
	Gates []Gate `json:"gates"`
	// Epoch counts the status changes, so a stale timer or a stale turn can see that the goal
	// changed. Hermes does not have this field.
	Epoch int64 `json:"epoch"`
	// JudgeSessionID is the hidden goal_judge session that logs the judge calls. Hermes does
	// not have this field.
	JudgeSessionID string `json:"judge_session_id"`
	// ConsecutiveTurnFailures counts the failed or empty turns in a row. Hermes does not have
	// this field.
	ConsecutiveTurnFailures int `json:"consecutive_turn_failures"`
}

// SetStatus sets the status and adds 1 to Epoch. A pause keeps reason as PausedReason, and an
// empty reason gives nil. Each other status clears PausedReason.
func (st *State) SetStatus(status, reason string) {
	st.Status = status
	st.PausedReason = nil
	if status == StatusPaused && reason != "" {
		st.PausedReason = &reason
	}
	st.Epoch++
}

// Resume makes the goal active again with a new turn budget and no barrier. The judge
// counters stay, like Hermes.
func (st *State) Resume() {
	st.SetStatus(StatusActive, "")
	st.TurnsUsed = 0
	st.ClearWait()
	st.ConsecutiveTurnFailures = 0
}

// ClearWait removes the barrier: it sets the six wait fields to their zero values.
func (st *State) ClearWait() {
	st.WaitingOnPID = nil
	st.WaitingOnSession = nil
	st.WaitingUntil = 0
	st.WaitingOnDelegations = 0
	st.WaitingReason = nil
	st.WaitingSince = 0
}
