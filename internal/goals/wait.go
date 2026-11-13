package goals

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dnwandana/tether-agent/internal/tools"
)

// MaxBarrierWait is the default for WaitEnv.MaxBarrier. It is the longest time that a pid or a
// process barrier holds a goal. A process that never exits must not park the goal for ever.
const MaxBarrierWait = 30 * time.Minute

// WaitEnv gives IsWaiting the live facts that a barrier depends on.
type WaitEnv struct {
	// Alive reports if a pid runs. Nil uses PIDAlive.
	Alive func(pid int) bool
	// Proc finds a process in the registry of the session. Nil finds none.
	Proc func(pid int) (tools.ProcInfo, bool)
	// Batches counts the delegate batches that run. Nil counts 0.
	Batches func() int
	// Now is the clock. The zero value uses time.Now.
	Now time.Time
	// MaxBarrier is the longest time that a pid or a process barrier holds a goal. 0 means
	// MaxBarrierWait.
	MaxBarrier time.Duration
}

// BarrierLimit returns the longest time that a pid or a process barrier holds a goal.
func (env WaitEnv) BarrierLimit() time.Duration {
	if env.MaxBarrier <= 0 {
		return MaxBarrierWait
	}
	return env.MaxBarrier
}

func (env WaitEnv) alive(pid int) bool {
	if env.Alive == nil {
		return PIDAlive(pid)
	}
	return env.Alive(pid)
}

func (env WaitEnv) batches() int {
	if env.Batches == nil {
		return 0
	}
	return env.Batches()
}

// procWaiting reports if the process of a session barrier still runs. A pid
// outside the registry, for example after a restart, uses the alive check.
func (env WaitEnv) procWaiting(id string) bool {
	pid, err := strconv.Atoi(strings.TrimSpace(id))
	if err != nil || pid <= 0 {
		return false
	}
	if env.Proc != nil {
		if p, ok := env.Proc(pid); ok {
			return !p.Exited
		}
	}
	return env.alive(pid)
}

// secsOf returns t as Unix seconds. The state keeps its times in this form,
// like Hermes.
func secsOf(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

// park replaces the barrier with a new one. It sets the reason and the start
// time, and the caller sets the barrier field.
func (st *State) park(reason string, now time.Time) {
	st.ClearWait()
	if r := strings.TrimSpace(reason); r != "" {
		st.WaitingReason = &r
	}
	st.WaitingSince = secsOf(now)
}

// WaitOnPID parks the goal until pid exits. The caller checks the pid.
func (st *State) WaitOnPID(pid int, reason string, now time.Time) {
	st.park(reason, now)
	st.WaitingOnPID = &pid
}

// WaitOnSession parks the goal until the background process id exits. The
// caller checks the id.
func (st *State) WaitOnSession(id, reason string, now time.Time) {
	st.park(reason, now)
	st.WaitingOnSession = &id
}

// WaitFor parks the goal for seconds. A delegations count above 0 also lifts
// the barrier when fewer batches than that count run.
func (st *State) WaitFor(seconds float64, delegations int, reason string, now time.Time) {
	st.park(reason, now)
	st.WaitingUntil = secsOf(now) + seconds
	st.WaitingOnDelegations = max(0, delegations)
}

// IsWaiting reports if a barrier still holds the goal. It clears a barrier
// that lifted. A pid or a process barrier ends after env.BarrierLimit().
func (st *State) IsWaiting(env WaitEnv) bool {
	now := env.Now
	if now.IsZero() {
		now = time.Now()
	}
	nowS := secsOf(now)
	var still bool
	switch {
	case st.WaitingOnSession != nil:
		still = env.procWaiting(*st.WaitingOnSession)
	case st.WaitingOnPID != nil:
		still = env.alive(*st.WaitingOnPID)
	case st.WaitingUntil != 0:
		still = nowS < st.WaitingUntil
		// A wait for running batches lifts when one batch returns.
		if still && st.WaitingOnDelegations > 0 && env.batches() < st.WaitingOnDelegations {
			still = false
		}
	default:
		return false
	}
	// A pid or a process that never exits must not park the goal for ever.
	if still && st.WaitingSince != 0 && st.WaitingUntil == 0 && nowS-st.WaitingSince > env.BarrierLimit().Seconds() {
		still = false
	}
	if !still {
		st.ClearWait()
	}
	return still
}

// WaitTarget returns the barrier in a short form: "session X", "pid N", or
// "Ns remaining". It returns "" when no barrier is set.
func (st *State) WaitTarget(now time.Time) string {
	switch {
	case st.WaitingOnSession != nil:
		return "session " + *st.WaitingOnSession
	case st.WaitingOnPID != nil:
		return fmt.Sprintf("pid %d", *st.WaitingOnPID)
	case st.WaitingUntil != 0:
		return fmt.Sprintf("%ds remaining", max(0, int(st.WaitingUntil-secsOf(now))))
	}
	return ""
}

// PIDAlive reports if pid runs. It refuses a pid of 0 or less, because
// signal 0 to such a pid goes to a process group. EPERM means that the
// process exists, so it counts as alive.
func PIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
