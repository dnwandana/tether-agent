package goals

import (
	"fmt"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
)

// The limits of a quality gate, copied from Hermes.
const (
	// GateTimeout is the run time limit of one gate run.
	GateTimeout = 300 * time.Second
	// GateMaxRetries is the number of failed runs before the goal pauses.
	GateMaxRetries = 3
	// GateOutputTail is the number of output chars that a gate run keeps.
	GateOutputTail = 3000
)

// GateCheck is the result of the check of a gate command when the owner adds it.
// Reason is plain text. The caller escapes it for its channel.
type GateCheck struct {
	Refused     bool
	Reason      string
	Dangerous   bool
	Description string
}

// CheckGateCommand checks cmd once, when the owner adds it as a gate. A gate
// runs before each judge call with no approval prompt, so a hardline or a
// self-kill command is refused, and a dangerous command needs a confirm. The
// order of the checks is the order of the approval gate.
func CheckGateCommand(cmd string, inGateway, sudoPasswordSet bool) GateCheck {
	if p, ok := security.MatchHardline(cmd); ok {
		return GateCheck{Refused: true, Description: p.Description,
			Reason: "BLOCKED (hardline): " + p.Description + ". This command cannot be a gate."}
	}
	if inGateway {
		if p, ok := security.MatchGatewaySelf(cmd); ok {
			return GateCheck{Refused: true, Description: p.Description,
				Reason: "BLOCKED: " + p.Description + ". This command cannot be a gate."}
		}
	}
	if security.MatchSudoStdin(cmd, sudoPasswordSet) {
		return GateCheck{Refused: true, Reason: "sudo with a password on stdin cannot be a gate."}
	}
	if p, ok := security.MatchDangerous(cmd); ok {
		return GateCheck{Dangerous: true, Description: p.Description,
			Reason: "⚠️ This gate matches a dangerous pattern: " + p.Description +
				". The gate runs before each judge call, with no approval prompt. Add it?"}
	}
	return GateCheck{}
}

// AddGate appends a gate for cmd and returns its 1-based number. An empty
// command adds nothing and returns 0. The epoch stays, so a queued
// continuation stays valid.
func (st *State) AddGate(cmd string) int {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return 0
	}
	st.Gates = append(st.Gates, Gate{
		Command:        cmd,
		TimeoutSeconds: int(GateTimeout / time.Second),
		MaxRetries:     GateMaxRetries,
	})
	return len(st.Gates)
}

// RemoveGate removes the gate with the 1-based number n and returns it.
func (st *State) RemoveGate(n int) (Gate, error) {
	if n < 1 || n > len(st.Gates) {
		return Gate{}, fmt.Errorf("index out of range (1..%d)", len(st.Gates))
	}
	g := st.Gates[n-1]
	st.Gates = append(st.Gates[:n-1], st.Gates[n:]...)
	return g, nil
}

// ClearGates removes all gates.
func (st *State) ClearGates() {
	st.Gates = nil
}

// RenderGates returns the gate list as plain text, one line for each gate,
// like Hermes render_gates. A gate that has not run yet shows no result.
func RenderGates(gates []Gate) string {
	if len(gates) == 0 {
		return "(no quality gates — use /goal gate add <command> to require one)"
	}
	lines := make([]string, 0, len(gates))
	for i, g := range gates {
		status := ""
		if g.LastExitCode != nil {
			if code := *g.LastExitCode; code == 0 {
				status = " ✓ passing"
			} else {
				status = fmt.Sprintf(" ✗ failing (exit %d, attempt %d/%d)", code, g.Attempts, g.MaxRetries)
			}
		}
		lines = append(lines, fmt.Sprintf("- %d. $ %s%s", i+1, g.Command, status))
	}
	return strings.Join(lines, "\n")
}
