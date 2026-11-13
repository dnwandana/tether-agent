package goals

import (
	"context"
	"strings"
	"time"
)

// ExecFunc runs one gate command and returns its output and exit code. The app binds it to
// the terminal tool of the goal session.
type ExecFunc func(ctx context.Context, command string, timeout time.Duration) (output string, exitCode int, err error)

// GateOutcome is the result of one gate run before the judge. Index is the 0-based index of
// the failed gate in State.Gates. Refused is true when a gate command failed the check before
// its run. Reason is the plain-text reason of that check.
type GateOutcome struct {
	Failed    bool
	Exhausted bool
	Refused   bool
	Reason    string
	Index     int
	Failure   GateFailure
}

// RunGates runs the gates of st in order, through exec, and records each result in the
// gate. It stops at the first failure and returns it. A pass sets the attempts to 0. An
// error from exec and a timeout count as exit -1. A gate in state.db can change after the
// owner adds it, so each run checks the command again through check. A nil check skips the
// check. A refused command does not run and does not change the gate. A dangerous command
// still runs.
func RunGates(ctx context.Context, st *State, exec ExecFunc, check func(command string) GateCheck) GateOutcome {
	return runGates(ctx, st, exec, check, gateLimits{})
}

// gateLimits holds the limits of a gate run. A field of 0 uses GateTimeout, GateMaxRetries,
// or GateOutputTail.
type gateLimits struct {
	timeout    time.Duration
	maxRetries int
	tail       int
}

// gateRunLimits returns the gate limits from the goals config.
func (m *Manager) gateRunLimits() gateLimits {
	c := m.cfg()
	return gateLimits{
		timeout:    time.Duration(c.GateTimeoutS) * time.Second,
		maxRetries: c.GateMaxRetries,
		tail:       c.GateOutputTailChars,
	}
}

// runGates is RunGates with the limits lim. A gate with a time limit or a retry count of 0
// or less uses the value of lim.
func runGates(ctx context.Context, st *State, exec ExecFunc, check func(command string) GateCheck, lim gateLimits) GateOutcome {
	if lim.timeout <= 0 {
		lim.timeout = GateTimeout
	}
	if lim.maxRetries <= 0 {
		lim.maxRetries = GateMaxRetries
	}
	if lim.tail <= 0 {
		lim.tail = GateOutputTail
	}
	for i := range st.Gates {
		g := &st.Gates[i]
		timeout := time.Duration(g.TimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = lim.timeout
		}
		maxRetries := g.MaxRetries
		if maxRetries <= 0 {
			maxRetries = lim.maxRetries
		}
		if check != nil {
			if c := check(g.Command); c.Refused {
				return GateOutcome{Failed: true, Refused: true, Reason: c.Reason, Index: i, Failure: GateFailure{Command: g.Command}}
			}
		}
		out, code, err := exec(ctx, g.Command, timeout)
		if err != nil {
			code = -1
			out = strings.TrimSuffix(out+"\n"+err.Error(), "\n")
			out = strings.TrimPrefix(out, "\n")
		}
		tail := gateTail(out, lim.tail)
		g.LastExitCode, g.LastOutputTail = &code, tail
		if code == 0 {
			g.Attempts = 0
			continue
		}
		g.Attempts++
		return GateOutcome{Failed: true, Exhausted: g.Attempts > maxRetries, Index: i, Failure: GateFailure{
			Command: g.Command, Attempt: g.Attempts, MaxRetries: maxRetries, ExitCode: code, Output: tail,
		}}
	}
	return GateOutcome{}
}

// gateTail returns the last n runes of s, so that the cut keeps valid UTF-8.
func gateTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}
