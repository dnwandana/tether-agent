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
// the failed gate in State.Gates.
type GateOutcome struct {
	Failed    bool
	Exhausted bool
	Index     int
	Failure   GateFailure
}

// RunGates runs the gates of st in order, through exec, and records each result in the
// gate. It stops at the first failure and returns it. A pass sets the attempts to 0. An
// error from exec and a timeout count as exit -1.
func RunGates(ctx context.Context, st *State, exec ExecFunc) GateOutcome {
	for i := range st.Gates {
		g := &st.Gates[i]
		timeout := time.Duration(g.TimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = GateTimeout
		}
		maxRetries := g.MaxRetries
		if maxRetries <= 0 {
			maxRetries = GateMaxRetries
		}
		out, code, err := exec(ctx, g.Command, timeout)
		if err != nil {
			code = -1
			out = strings.TrimSuffix(out+"\n"+err.Error(), "\n")
			out = strings.TrimPrefix(out, "\n")
		}
		tail := gateTail(out, GateOutputTail)
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
