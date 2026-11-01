package gateway

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/goals"
)

// pidCheckEvery is the interval of the alive check of a pid that the process registry does
// not keep for the session. No exit event of that session comes for such a pid.
const pidCheckEvery = 30 * time.Second

// wakeSlack goes on each barrier deadline. The state keeps the time as float seconds, and a
// timer that fires before the deadline finds the barrier still held and never fires again.
const wakeSlack = 10 * time.Millisecond

// errWakeStale makes Update write nothing when the goal changed after the wake loaded it.
var errWakeStale = errors.New("gateway: the goal changed before the barrier was cleared")

// GoalWaker wakes a parked goal with no owner message. It sets a timer for the deadline of a
// timed wait or for the limit of a pid or a process barrier, and an alive check for a pid
// outside the process registry. Wake also runs on an event, for example a process exit or a
// batch end. It also fires the one retry after a failed turn.
type GoalWaker struct {
	Goals *goals.Manager
	// Fire enqueues the continuation c for key. An error keeps the barrier.
	Fire func(ctx context.Context, key, sessionID string, c goals.Continuation) error
	Log  *slog.Logger

	mu      sync.Mutex
	stopped bool
	// keys maps a session ID to its key.
	keys map[string]string
	// timers holds the barrier timers of each session.
	timers  map[string][]*time.Timer
	retries map[string]*time.Timer
	// waking marks a session whose Wake runs now. again marks a session that got a Wake
	// during that run, so the run checks the barrier one more time.
	waking map[string]bool
	again  map[string]bool
	// gen counts the Arm calls of each session. A Wake stops only the timers of the Arm that
	// it saw, not the timers of a later Arm.
	gen map[string]uint64
	// checkEvery is the interval of the pid check. 0 means pidCheckEvery.
	checkEvery time.Duration
}

// Arm sets the barrier timers of the goal st of sessionID, and replaces the old timers of the
// session. A goal that is not active or has no barrier gets no timer. A barrier that lifted
// before Arm gets a wake at once, because its event can come before Arm and Wake ignores a
// session that Arm did not see.
func (w *GoalWaker) Arm(key, sessionID string, st goals.State) {
	if w.Goals == nil {
		return
	}
	env := w.Goals.WaitEnv(key)
	// IsWaiting clears the barrier of the copy that it gets, so st stays as it is.
	check := st
	lifted := st.Status == goals.StatusActive && hasBarrier(st) && !check.IsWaiting(env)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	w.initLocked()
	w.keys[sessionID] = key
	w.gen[sessionID]++
	w.stopTimersLocked(sessionID)
	if st.Status != goals.StatusActive || !hasBarrier(st) {
		return
	}
	// A park supersedes the retry of a failed turn, and the retry must not run through the
	// barrier.
	if t := w.retries[sessionID]; t != nil {
		t.Stop()
		delete(w.retries, sessionID)
	}
	wake := func() { w.Wake(context.Background(), sessionID) }
	var deadline time.Time
	switch {
	case st.WaitingOnPID != nil || st.WaitingOnSession != nil:
		if st.WaitingSince > 0 {
			deadline = timeOfSecs(st.WaitingSince).Add(goals.MaxBarrierWait)
		}
	default:
		deadline = timeOfSecs(st.WaitingUntil)
	}
	var timers []*time.Timer
	if lifted {
		// The wake runs in a timer, so a caller that holds a lock does not run Fire.
		timers = append(timers, time.AfterFunc(0, wake))
	}
	if !deadline.IsZero() {
		timers = append(timers, time.AfterFunc(time.Until(deadline)+wakeSlack, wake))
	}
	if pid, ok := barrierPID(st); ok {
		// The exit event of the registry wakes only the session that started the process.
		known := false
		if env.Proc != nil {
			p, ok := env.Proc(pid)
			known = ok && p.SessionID == sessionID
		}
		if !known {
			timers = append(timers, w.checkLocked(sessionID))
		}
	}
	if len(timers) > 0 {
		w.timers[sessionID] = timers
	}
}

// checkLocked starts the alive check of sessionID. The check calls Wake, and it sets itself
// again while the session still has it among its timers. The caller holds w.mu.
func (w *GoalWaker) checkLocked(sessionID string) *time.Timer {
	every := w.checkEvery
	if every <= 0 {
		every = pidCheckEvery
	}
	var t *time.Timer
	t = time.AfterFunc(every, func() {
		w.Wake(context.Background(), sessionID)
		w.mu.Lock()
		defer w.mu.Unlock()
		if !w.stopped && slices.Contains(w.timers[sessionID], t) {
			t.Reset(every)
		}
	})
	return t
}

// ArmRetry fires a retry continuation of the goal of sessionID after d, when the goal is still
// active with epoch. It replaces an old retry timer of the session. A pause does not stop the
// timer, because the epoch check makes it do nothing.
func (w *GoalWaker) ArmRetry(key, sessionID string, epoch int64, d time.Duration) {
	if w.Goals == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	w.initLocked()
	if old := w.retries[sessionID]; old != nil {
		old.Stop()
	}
	var t *time.Timer
	t = time.AfterFunc(d, func() { w.retry(key, sessionID, epoch, t) })
	w.retries[sessionID] = t
}

// StopRetry stops the retry timer of sessionID. A later decision of the goal step makes the
// retry stale, and the epoch check cannot see that, because the decision keeps the epoch.
func (w *GoalWaker) StopRetry(sessionID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t := w.retries[sessionID]; t != nil {
		t.Stop()
		delete(w.retries, sessionID)
	}
}

// retry is the callback of the retry timer t.
func (w *GoalWaker) retry(key, sessionID string, epoch int64, t *time.Timer) {
	w.mu.Lock()
	if w.stopped || w.retries[sessionID] != t {
		w.mu.Unlock()
		return
	}
	delete(w.retries, sessionID)
	w.mu.Unlock()
	if !w.Goals.IsCurrent(sessionID, epoch) {
		return
	}
	ctx := context.Background()
	st, ok, err := w.Goals.Load(ctx, sessionID)
	if err != nil {
		w.logger().Warn("gateway: cannot load the goal for the retry", "session", sessionID, "err", err.Error())
		return
	}
	// /goal wait parks the goal and keeps the epoch. A barrier wake owns the next turn.
	if !ok || st.Status != goals.StatusActive || st.Epoch != epoch || hasBarrier(st) {
		return
	}
	c := goals.Continuation{SessionID: sessionID, Text: goals.ContinuationPrompt(st, nil), Epoch: st.Epoch,
		LineKind: goals.LineRetry, Used: st.TurnsUsed, Max: st.MaxTurns}
	if err := w.Fire(ctx, key, sessionID, c); err != nil {
		w.logger().Warn("gateway: cannot enqueue the goal retry", "key", key, "session", sessionID, "err", err.Error())
	}
}

// Wake lifts the barrier of the goal of sessionID when the barrier no longer holds. It
// enqueues the continuation first, so a failed enqueue keeps the barrier. It does nothing for a
// session that Arm never saw, and a Wake during a Wake of the same session gives no second
// continuation.
func (w *GoalWaker) Wake(ctx context.Context, sessionID string) {
	if w.Goals == nil {
		return
	}
	w.mu.Lock()
	key, seen := w.keys[sessionID]
	if w.stopped || !seen {
		w.mu.Unlock()
		return
	}
	if w.waking[sessionID] {
		w.again[sessionID] = true
		w.mu.Unlock()
		return
	}
	w.waking[sessionID] = true
	w.mu.Unlock()
	for {
		fired := w.wakeOnce(ctx, key, sessionID)
		w.mu.Lock()
		// A Wake that came during a fire finds nothing to do: the barrier is clear, or the
		// enqueue failed and fails again.
		if fired || !w.again[sessionID] || w.stopped {
			delete(w.again, sessionID)
			delete(w.waking, sessionID)
			w.mu.Unlock()
			return
		}
		delete(w.again, sessionID)
		w.mu.Unlock()
	}
}

// wakeOnce checks the barrier of sessionID one time. It returns true when it called Fire.
func (w *GoalWaker) wakeOnce(ctx context.Context, key, sessionID string) bool {
	w.mu.Lock()
	gen := w.gen[sessionID]
	w.mu.Unlock()
	st, ok, err := w.Goals.Load(ctx, sessionID)
	if err != nil {
		w.logger().Warn("gateway: cannot load the goal for the wake", "session", sessionID, "err", err.Error())
		return false
	}
	if !ok || st.Status != goals.StatusActive || !hasBarrier(st) {
		w.stopTimers(sessionID, gen)
		return false
	}
	since := st.WaitingSince
	if st.IsWaiting(w.Goals.WaitEnv(key)) {
		return false
	}
	c := goals.Continuation{SessionID: sessionID, Text: goals.ContinuationPrompt(st, nil), Epoch: st.Epoch,
		LineKind: goals.LineLifted, Used: st.TurnsUsed, Max: st.MaxTurns}
	if err := w.Fire(ctx, key, sessionID, c); err != nil {
		w.logger().Warn("gateway: cannot enqueue the goal after the barrier lifted; the barrier stays",
			"key", key, "session", sessionID, "err", err.Error())
		return true
	}
	// The continuation can set a new barrier with the same epoch, so the clear also compares
	// the start time of the barrier.
	_, err = w.Goals.Update(ctx, sessionID, func(s *goals.State, exists bool) error {
		if !exists || s.Status != goals.StatusActive || s.Epoch != c.Epoch || s.WaitingSince != since {
			return errWakeStale
		}
		s.ClearWait()
		return nil
	})
	if err != nil && !errors.Is(err, errWakeStale) {
		w.logger().Warn("gateway: cannot clear the goal barrier", "session", sessionID, "err", err.Error())
	}
	w.stopTimers(sessionID, gen)
	return true
}

// Stop stops each timer. Each later Arm, ArmRetry, and Wake does nothing.
func (w *GoalWaker) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
	for sid := range w.timers {
		w.stopTimersLocked(sid)
	}
	for _, t := range w.retries {
		t.Stop()
	}
	w.keys, w.timers, w.retries, w.waking, w.again, w.gen = nil, nil, nil, nil, nil, nil
}

// armed returns the number of sessions with barrier timers.
func (w *GoalWaker) armed() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, ts := range w.timers {
		if len(ts) > 0 {
			n++
		}
	}
	return n
}

// stopTimers stops the barrier timers of sessionID when no Arm came after the generation gen.
func (w *GoalWaker) stopTimers(sessionID string, gen uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.stopped && w.gen[sessionID] == gen {
		w.stopTimersLocked(sessionID)
	}
}

func (w *GoalWaker) stopTimersLocked(sessionID string) {
	for _, t := range w.timers[sessionID] {
		t.Stop()
	}
	delete(w.timers, sessionID)
}

func (w *GoalWaker) initLocked() {
	if w.keys == nil {
		w.keys = make(map[string]string)
		w.timers = make(map[string][]*time.Timer)
		w.retries = make(map[string]*time.Timer)
		w.waking = make(map[string]bool)
		w.again = make(map[string]bool)
		w.gen = make(map[string]uint64)
	}
}

func (w *GoalWaker) logger() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}

// hasBarrier reports if st holds a pid, a process, or a timed barrier.
func hasBarrier(st goals.State) bool {
	return st.WaitingOnPID != nil || st.WaitingOnSession != nil || st.WaitingUntil > 0
}

// barrierPID returns the pid of a pid barrier, or of a process barrier whose ID is a pid.
func barrierPID(st goals.State) (int, bool) {
	switch {
	case st.WaitingOnSession != nil:
		pid, err := strconv.Atoi(strings.TrimSpace(*st.WaitingOnSession))
		return pid, err == nil && pid > 0
	case st.WaitingOnPID != nil:
		return *st.WaitingOnPID, true
	}
	return 0, false
}

// timeOfSecs converts the Unix seconds of the goal state to a time.
func timeOfSecs(sec float64) time.Time { return time.UnixMilli(int64(sec * 1000)) }
