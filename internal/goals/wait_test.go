package goals

import (
	"os"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/tools"
)

// waitNow is the clock of the wait tests.
var waitNow = time.Unix(1_800_000_000, 0)

func waitGoal() State {
	return State{Goal: "ship it", Status: StatusActive, MaxTurns: 5, Epoch: 1}
}

func TestWaitOnPIDParksAndLifts(t *testing.T) {
	alive := true
	env := WaitEnv{Alive: func(pid int) bool { return pid == 4242 && alive }, Now: waitNow}
	st := waitGoal()
	st.WaitOnPID(4242, "  CI green ", waitNow)
	if st.WaitingOnPID == nil || *st.WaitingOnPID != 4242 || st.WaitingReason == nil || *st.WaitingReason != "CI green" ||
		st.WaitingSince != 1_800_000_000 || st.WaitingUntil != 0 {
		t.Fatalf("%+v", st)
	}
	if !st.IsWaiting(env) || st.WaitTarget(waitNow) != "pid 4242" {
		t.Fatal("a live pid holds the barrier")
	}
	alive = false
	if st.IsWaiting(env) || st.WaitingOnPID != nil || st.WaitingReason != nil || st.WaitingSince != 0 {
		t.Fatalf("a dead pid lifts and clears the barrier: %+v", st)
	}
	if st.Epoch != 1 || st.Status != StatusActive {
		t.Fatal("a barrier does not change the epoch or the status")
	}
	st.WaitOnPID(4242, "", waitNow)
	if st.WaitingReason != nil {
		t.Fatal("an empty reason is nil")
	}
	if st.WaitTarget(waitNow) != "pid 4242" {
		t.Fatal(st.WaitTarget(waitNow))
	}
	st.ClearWait()
	if st.WaitTarget(waitNow) != "" || st.IsWaiting(env) {
		t.Fatal("no barrier gives no target and no wait")
	}
}

func TestBarrierOnAProcessThatNeverExitsEnds(t *testing.T) {
	env := WaitEnv{Alive: func(int) bool { return true }, Now: waitNow.Add(MaxBarrierWait)}
	st := waitGoal()
	st.WaitOnPID(4242, "poller", waitNow)
	if !st.IsWaiting(env) {
		t.Fatal("the barrier holds at exactly 30 min")
	}
	env.Now = waitNow.Add(MaxBarrierWait + time.Second)
	if st.IsWaiting(env) || st.WaitingOnPID != nil {
		t.Fatal("the barrier ends after 30 min")
	}
	st.WaitOnSession("555", "watcher", waitNow)
	if st.IsWaiting(env) || st.WaitingOnSession != nil {
		t.Fatal("a process barrier also ends after 30 min")
	}
}

func TestWaitOnSessionUsesTheRegistry(t *testing.T) {
	procs := tools.NewProcRegistry()
	procs.Add("s1", 555, "watch.sh", "", waitNow)
	alive := map[int]bool{777: true}
	env := WaitEnv{Alive: func(pid int) bool { return alive[pid] }, Proc: procs.Get, Now: waitNow}
	st := waitGoal()
	st.WaitOnSession("555", "watcher", waitNow)
	if st.WaitingOnSession == nil || *st.WaitingOnSession != "555" || !st.IsWaiting(env) || st.WaitTarget(waitNow) != "session 555" {
		t.Fatalf("%+v", st)
	}
	procs.Exit(555, 0, waitNow)
	if st.IsWaiting(env) || st.WaitingOnSession != nil {
		t.Fatal("the exit of the process lifts the barrier")
	}
	st.WaitOnSession("777", "", waitNow)
	if !st.IsWaiting(env) {
		t.Fatal("a pid outside the registry uses the alive check")
	}
	st.WaitOnSession("proc_abc", "", waitNow)
	if st.IsWaiting(env) {
		t.Fatal("a session ID that is not a pid lifts at once")
	}
}

func TestTimeBarrierClearsAfterTheDeadline(t *testing.T) {
	st := waitGoal()
	st.WaitFor(120, 0, "backoff", waitNow)
	if st.WaitingUntil != 1_800_000_120 || st.WaitingOnDelegations != 0 || st.WaitingSince != 1_800_000_000 {
		t.Fatalf("%+v", st)
	}
	if !st.IsWaiting(WaitEnv{Now: waitNow.Add(60 * time.Second)}) || st.WaitTarget(waitNow.Add(60*time.Second)) != "60s remaining" {
		t.Fatal("the deadline holds the barrier")
	}
	if st.IsWaiting(WaitEnv{Now: waitNow.Add(121 * time.Second)}) || st.WaitingUntil != 0 {
		t.Fatal("the barrier clears after the deadline")
	}
	st.WaitFor(60, -3, "", waitNow)
	if st.WaitingOnDelegations != 0 {
		t.Fatal("a negative batch count is 0")
	}
	if st.WaitTarget(waitNow.Add(90*time.Second)) != "0s remaining" {
		t.Fatal(st.WaitTarget(waitNow.Add(90 * time.Second)))
	}
}

func TestDelegationWaitLiftsWhenABatchReturns(t *testing.T) {
	live := 4
	env := WaitEnv{Batches: func() int { return live }, Now: waitNow.Add(time.Second)}
	st := waitGoal()
	st.WaitFor(1200, 4, "4 batches running", waitNow)
	if !st.IsWaiting(env) {
		t.Fatal("4 live batches hold the barrier")
	}
	live = 3
	if st.IsWaiting(env) || st.WaitingUntil != 0 || st.WaitingOnDelegations != 0 {
		t.Fatalf("a returned batch lifts the barrier: %+v", st)
	}
	st.WaitFor(1200, 0, "cooldown", waitNow)
	live = 0
	if !st.IsWaiting(env) {
		t.Fatal("a plain timed wait ignores the batch count")
	}
}

func TestPIDAlive(t *testing.T) {
	if !PIDAlive(os.Getpid()) {
		t.Fatal("this process is alive")
	}
	if !PIDAlive(1) {
		t.Fatal("pid 1 is alive; EPERM counts as alive")
	}
	for _, pid := range []int{0, -1, 2_000_000_000} {
		if PIDAlive(pid) {
			t.Fatalf("pid %d is not alive", pid)
		}
	}
	st := waitGoal()
	st.WaitOnPID(os.Getpid(), "", waitNow)
	if !st.IsWaiting(WaitEnv{Now: waitNow}) {
		t.Fatal("a nil Alive uses PIDAlive")
	}
}

func TestBarrierLimitFromField(t *testing.T) {
	if got := (WaitEnv{}).BarrierLimit(); got != MaxBarrierWait {
		t.Fatal("0 keeps the const:", got)
	}
	env := WaitEnv{Alive: func(int) bool { return true }, Now: waitNow.Add(10 * time.Minute), MaxBarrier: 10 * time.Minute}
	if got := env.BarrierLimit(); got != 10*time.Minute {
		t.Fatal(got)
	}
	st := waitGoal()
	st.WaitOnPID(4242, "poller", waitNow)
	if !st.IsWaiting(env) {
		t.Fatal("the barrier holds at exactly 10 min")
	}
	env.Now = waitNow.Add(10*time.Minute + time.Second)
	if st.IsWaiting(env) || st.WaitingOnPID != nil {
		t.Fatal("the barrier ends after 10 min")
	}
}
