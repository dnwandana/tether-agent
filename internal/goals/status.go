package goals

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// NoGoalText is the reply when the session has no goal, the Hermes text.
const NoGoalText = "No active goal. Set one with /goal <text>."

// noContractText is the /goal show line for a goal with an empty contract, the Hermes text.
const noContractText = "(no completion contract — set one with /goal draft <objective> or inline field: value lines)"

// NewState returns a new active goal, like Hermes set. A maxTurns of 0 or less gives the
// Hermes default of 20. The epoch is 1, so a caller that replaces a row must set it to the
// old epoch plus 1.
func NewState(goal string, c Contract, maxTurns int, now time.Time) State {
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}
	return State{
		Goal:      goal,
		Status:    StatusActive,
		MaxTurns:  maxTurns,
		CreatedAt: float64(now.UnixMilli()) / 1000,
		Contract:  c,
		Epoch:     1,
	}
}

// plural returns ", N word" with an "s" when n is not 1, or "" when n is 0.
func plural(n int, word string) string {
	if n == 0 {
		return ""
	}
	if n != 1 {
		word += "s"
	}
	return fmt.Sprintf(", %d %s", n, word)
}

// orText returns *p when p is not nil and not "", else def.
func orText(p *string, def string) string {
	if p != nil && *p != "" {
		return *p
	}
	return def
}

// StatusLine returns the Hermes status line of the goal as plain text. Hermes also checks
// that a pid or a process is alive. This function shows the stored barrier, because the
// after-turn step clears a lifted barrier.
func StatusLine(st State, now time.Time) string {
	if st.Status == "" || st.Status == StatusCleared {
		return NoGoalText
	}
	meta := fmt.Sprintf("%d/%d turns", st.TurnsUsed, st.MaxTurns) + plural(len(st.Subgoals), "subgoal")
	if !st.Contract.Empty() {
		meta += ", contract"
	}
	meta += plural(len(st.Gates), "gate")
	switch st.Status {
	case StatusActive:
		nowS := float64(now.UnixMilli()) / 1000
		switch {
		case st.WaitingOnSession != nil && *st.WaitingOnSession != "":
			reason := orText(st.WaitingReason, "session "+*st.WaitingOnSession)
			return fmt.Sprintf("⏳ Goal (parked on %s, %s): %s", reason, meta, st.Goal)
		case st.WaitingOnPID != nil:
			reason := orText(st.WaitingReason, fmt.Sprintf("pid %d", *st.WaitingOnPID))
			return fmt.Sprintf("⏳ Goal (parked on %s, %s): %s", reason, meta, st.Goal)
		case st.WaitingUntil > nowS:
			remaining := int(st.WaitingUntil - nowS)
			reason := orText(st.WaitingReason, fmt.Sprintf("%ds", remaining))
			return fmt.Sprintf("⏳ Goal (parked %ds — %s, %s): %s", remaining, reason, meta, st.Goal)
		}
		return fmt.Sprintf("⊙ Goal (active, %s): %s", meta, st.Goal)
	case StatusPaused:
		extra := ""
		if st.PausedReason != nil && *st.PausedReason != "" {
			extra = " — " + *st.PausedReason
		}
		return fmt.Sprintf("⏸ Goal (paused, %s%s): %s", meta, extra, st.Goal)
	case StatusDone:
		return fmt.Sprintf("✓ Goal done (%s): %s", meta, st.Goal)
	}
	return fmt.Sprintf("Goal (%s, %s): %s", st.Status, meta, st.Goal)
}

// ShowText returns the /goal show text: the status line and the contract block. The block
// has no extra criteria, like Hermes render_contract.
func ShowText(st State, now time.Time) string {
	line := StatusLine(st, now)
	if line == NoGoalText {
		return line
	}
	if st.Contract.Empty() {
		return line + "\n" + noContractText
	}
	return line + "\n" + st.Contract.Render(nil)
}

// errNoGoal is the Hermes error of a subgoal edit when the session has no goal.
var errNoGoal = errors.New("no active goal")

// hasGoal reports whether the goal is active or paused, like Hermes has_goal.
func (st *State) hasGoal() bool {
	return st.Status == StatusActive || st.Status == StatusPaused
}

// AddSubgoal adds the trimmed text as an extra criterion and returns its 1-based index.
// It does not change Epoch, so a queued continuation stays valid.
func (st *State) AddSubgoal(text string) (int, error) {
	if !st.hasGoal() {
		return 0, errNoGoal
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, errors.New("subgoal text is empty")
	}
	st.Subgoals = append(st.Subgoals, text)
	return len(st.Subgoals), nil
}

// RemoveSubgoal removes the subgoal with the 1-based index n and returns its text.
func (st *State) RemoveSubgoal(n int) (string, error) {
	if !st.hasGoal() {
		return "", errNoGoal
	}
	if n < 1 || n > len(st.Subgoals) {
		return "", fmt.Errorf("index out of range (1..%d)", len(st.Subgoals))
	}
	removed := st.Subgoals[n-1]
	st.Subgoals = append(st.Subgoals[:n-1], st.Subgoals[n:]...)
	return removed, nil
}

// ClearSubgoals removes all subgoals. The caller reads the count before the call for its
// reply.
func (st *State) ClearSubgoals() {
	st.Subgoals = nil
}
