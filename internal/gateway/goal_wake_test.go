package gateway

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// wakeManager returns a goal manager over st with an empty process registry and a fake alive
// check.
func wakeManager(t *testing.T, st *sessions.Store, alive *atomic.Bool) *goals.Manager {
	t.Helper()
	return goals.NewManager(goals.Deps{
		Store: &goals.Store{DB: openTestDB(t)}, Sessions: st,
		Cfg:   func() config.GoalsConfig { return config.Default().Goals },
		Procs: tools.NewProcRegistry(), Alive: func(int) bool { return alive.Load() },
		Now: time.Now, Log: slog.New(slog.DiscardHandler),
	})
}

// parkGoal makes the active session of k1, gives it an active goal, and applies park to it.
func parkGoal(t *testing.T, m *goals.Manager, st *sessions.Store, park func(s *goals.State)) (string, goals.State) {
	t.Helper()
	ctx := context.Background()
	sess, err := st.ActiveOrCreate(ctx, "k1", sessions.Session{Source: "telegram", ChatID: "1", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.Update(ctx, sess.ID, func(s *goals.State, _ bool) error {
		*s = goals.NewState("ship the fix", goals.Contract{}, 20, time.Now())
		park(s)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sess.ID, s
}

// fireRec records the continuations that a GoalWaker fires.
type fireRec struct {
	mu  sync.Mutex
	got []goals.Continuation
	err error
}

func (f *fireRec) fire(_ context.Context, key, _ string, c goals.Continuation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if key != "k1" {
		return errors.New("wrong key " + key)
	}
	f.got = append(f.got, c)
	return f.err
}

func (f *fireRec) list() []goals.Continuation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]goals.Continuation(nil), f.got...)
}

// wakeState returns the stored goal of sid, or fails the test when it has none.
func wakeState(t *testing.T, m *goals.Manager, sid string) goals.State {
	t.Helper()
	st, ok, err := m.Load(context.Background(), sid)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	return st
}

func newWakeRig(t *testing.T) (*goals.Manager, *sessions.Store, *atomic.Bool) {
	t.Helper()
	st := sessions.New(openTestDB(t))
	alive := &atomic.Bool{}
	alive.Store(true)
	return wakeManager(t, st, alive), st, alive
}

func TestWakeAtTheDeadlineLiftsTheBarrier(t *testing.T) {
	m, st, _ := newWakeRig(t)
	sid, s := parkGoal(t, m, st, func(s *goals.State) { s.WaitFor(0.05, 0, "the build", time.Now()) })
	rec := &fireRec{}
	w := &GoalWaker{Goals: m, Fire: rec.fire}
	w.Arm("k1", sid, s)
	waitFor(t, func() bool { return len(rec.list()) == 1 })
	c := rec.list()[0]
	if c.LineKind != goals.LineLifted || c.SessionID != sid || c.Epoch != s.Epoch || c.Text == "" || c.Max != 20 {
		t.Fatalf("%+v", c)
	}
	waitFor(t, func() bool { return wakeState(t, m, sid).WaitingUntil == 0 })
	waitFor(t, func() bool { return w.armed() == 0 })
}

func TestWakeKeepsTheBarrierWhenFireFails(t *testing.T) {
	m, st, _ := newWakeRig(t)
	sid, s := parkGoal(t, m, st, func(s *goals.State) { s.WaitFor(0.05, 0, "the build", time.Now()) })
	rec := &fireRec{err: ErrStopping}
	w := &GoalWaker{Goals: m, Fire: rec.fire}
	w.Arm("k1", sid, s)
	waitFor(t, func() bool { return len(rec.list()) == 1 })
	time.Sleep(50 * time.Millisecond)
	if wakeState(t, m, sid).WaitingUntil == 0 {
		t.Fatal("the barrier must stay when the enqueue fails")
	}
}

func TestWakeChecksAPIDOutsideTheRegistry(t *testing.T) {
	m, st, alive := newWakeRig(t)
	sid, s := parkGoal(t, m, st, func(s *goals.State) { s.WaitOnPID(4242, "the build", time.Now()) })
	rec := &fireRec{}
	w := &GoalWaker{Goals: m, Fire: rec.fire, checkEvery: 20 * time.Millisecond}
	w.Arm("k1", sid, s)
	time.Sleep(80 * time.Millisecond)
	if len(rec.list()) != 0 {
		t.Fatal("a live pid must keep the goal parked")
	}
	alive.Store(false)
	waitFor(t, func() bool { return len(rec.list()) == 1 })
	waitFor(t, func() bool { return wakeState(t, m, sid).WaitingOnPID == nil })
	time.Sleep(60 * time.Millisecond)
	if len(rec.list()) != 1 {
		t.Fatal("the check must stop after the wake")
	}
}

func TestWakeEndsAPIDBarrierAfterTheLimit(t *testing.T) {
	m, st, _ := newWakeRig(t)
	since := time.Now().Add(-goals.MaxBarrierWait + 50*time.Millisecond)
	sid, s := parkGoal(t, m, st, func(s *goals.State) { s.WaitOnPID(4242, "the build", since) })
	rec := &fireRec{}
	w := &GoalWaker{Goals: m, Fire: rec.fire, checkEvery: time.Hour}
	w.Arm("k1", sid, s)
	waitFor(t, func() bool { return len(rec.list()) == 1 })
}

func TestWakeOnAnEvent(t *testing.T) {
	ctx := context.Background()
	m, st, alive := newWakeRig(t)
	sid, s := parkGoal(t, m, st, func(s *goals.State) { s.WaitOnPID(4242, "the build", time.Now()) })
	rec := &fireRec{}
	w := &GoalWaker{Goals: m, Fire: rec.fire, checkEvery: time.Hour}
	w.Wake(ctx, sid)
	if len(rec.list()) != 0 {
		t.Fatal("a session that Arm never saw does nothing")
	}
	w.Arm("k1", sid, s)
	w.Wake(ctx, sid)
	if len(rec.list()) != 0 {
		t.Fatal("a held barrier does nothing")
	}
	alive.Store(false) // the process exited
	w.Wake(ctx, sid)
	if len(rec.list()) != 1 || wakeState(t, m, sid).WaitingOnPID != nil {
		t.Fatal(rec.list())
	}
}

func TestArmWithNoBarrierStartsNoTimer(t *testing.T) {
	m, st, _ := newWakeRig(t)
	sid, s := parkGoal(t, m, st, func(*goals.State) {})
	w := &GoalWaker{Goals: m, Fire: (&fireRec{}).fire}
	w.Arm("k1", sid, s)
	if w.armed() != 0 {
		t.Fatal("no goal is parked, so no timer runs")
	}
}

func TestArmRetryFiresOnlyForTheCurrentEpoch(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newWakeRig(t)
	sid, s := parkGoal(t, m, st, func(*goals.State) {})
	rec := &fireRec{}
	w := &GoalWaker{Goals: m, Fire: rec.fire}
	w.ArmRetry("k1", sid, s.Epoch, 20*time.Millisecond)
	waitFor(t, func() bool { return len(rec.list()) == 1 })
	if c := rec.list()[0]; c.LineKind != goals.LineRetry || c.Epoch != s.Epoch {
		t.Fatalf("%+v", c)
	}
	w.ArmRetry("k1", sid, s.Epoch, 50*time.Millisecond)
	if _, err := m.Update(ctx, sid, func(s *goals.State, _ bool) error { s.SetStatus(goals.StatusPaused, "paused by the owner"); return nil }); err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
	if len(rec.list()) != 1 {
		t.Fatal("a retry of an old epoch must do nothing")
	}
}

func TestStopStopsEachTimer(t *testing.T) {
	m, st, _ := newWakeRig(t)
	sid, s := parkGoal(t, m, st, func(s *goals.State) { s.WaitFor(0.05, 0, "the build", time.Now()) })
	rec := &fireRec{}
	w := &GoalWaker{Goals: m, Fire: rec.fire}
	w.Arm("k1", sid, s)
	w.ArmRetry("k1", sid, s.Epoch, 50*time.Millisecond)
	w.Stop()
	time.Sleep(120 * time.Millisecond)
	w.Arm("k1", sid, s)
	if len(rec.list()) != 0 || w.armed() != 0 {
		t.Fatal(rec.list(), w.armed())
	}
}

// A wake timer that fires after BeginStopping must enqueue nothing and keep the barrier.
func TestWakeAfterBeginStoppingEnqueuesNothing(t *testing.T) {
	ctx := context.Background()
	fp := &fakeProv{}
	d := testDeps(t, fp)
	alive := &atomic.Bool{}
	alive.Store(true)
	d.Goals = wakeManager(t, d.Store, alive)
	g := &Gateway{Deps: d, Channels: map[string]Channel{"telegram": &recChannel{sent: make(chan Outbound, 8)}}}
	// The deadline is 200 ms away, so both timers fire after BeginStopping.
	sid, s := parkGoal(t, d.Goals, d.Store, func(s *goals.State) { s.WaitFor(0.2, 0, "the build", time.Now()) })
	errs := make(chan error, 1)
	// late is a timer that BeginStopping does not stop: its callback runs after the stop.
	late := &GoalWaker{Goals: d.Goals, Fire: func(ctx context.Context, key, _ string, c goals.Continuation) error {
		err := g.enqueueGoal(ctx, key, c)
		errs <- err
		return err
	}}
	late.Arm("k1", sid, s)
	g.GoalWaker().Arm("k1", sid, s)
	g.BeginStopping(ctx)
	select {
	case err := <-errs:
		if !errors.Is(err, ErrStopping) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the late timer did not fire")
	}
	time.Sleep(50 * time.Millisecond)
	if got := fp.lastUserTexts(); len(got) != 0 {
		t.Fatal("no turn may start after the stop:", got)
	}
	if wakeState(t, d.Goals, sid).WaitingUntil == 0 {
		t.Fatal("the barrier must stay for the restart")
	}
	g.GoalWaker().Arm("k1", sid, s)
	if g.GoalWaker().armed() != 0 {
		t.Fatal("BeginStopping must stop the waker of the gateway")
	}
}

// /goal wait keeps the epoch, so the retry must check the barrier, not only the epoch.
func TestRetrySkipsAParkedGoal(t *testing.T) {
	m, st, _ := newWakeRig(t)
	sid, s := parkGoal(t, m, st, func(s *goals.State) { s.WaitFor(3600, 0, "the build", time.Now()) })
	rec := &fireRec{}
	w := &GoalWaker{Goals: m, Fire: rec.fire}
	t.Cleanup(w.Stop)
	w.ArmRetry("k1", sid, s.Epoch, 10*time.Millisecond)
	time.Sleep(80 * time.Millisecond)
	if len(rec.list()) != 0 {
		t.Fatal("a retry must not run through a barrier:", rec.list())
	}
}

// A park supersedes a retry, so Arm for a parked goal stops the retry timer of the session.
func TestArmOfAParkStopsTheRetry(t *testing.T) {
	ctx := context.Background()
	m, st, _ := newWakeRig(t)
	sid, s := parkGoal(t, m, st, func(*goals.State) {})
	w := &GoalWaker{Goals: m, Fire: (&fireRec{}).fire, checkEvery: time.Hour}
	t.Cleanup(w.Stop)
	w.ArmRetry("k1", sid, s.Epoch, time.Hour)
	parked, err := m.Update(ctx, sid, func(s *goals.State, _ bool) error {
		s.WaitOnPID(4242, "the build", time.Now())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	w.Arm("k1", sid, parked)
	w.mu.Lock()
	_, ok := w.retries[sid]
	w.mu.Unlock()
	if ok {
		t.Fatal("Arm of a park must stop the retry timer")
	}
}

// The process registry wakes only the session that started a process. A goal that waits on
// the process of another session needs the alive check.
func TestArmChecksAPIDOfAnotherSession(t *testing.T) {
	st := sessions.New(openTestDB(t))
	alive := &atomic.Bool{}
	alive.Store(true)
	procs := tools.NewProcRegistry()
	m := goals.NewManager(goals.Deps{
		Store: &goals.Store{DB: openTestDB(t)}, Sessions: st,
		Cfg:   func() config.GoalsConfig { return config.Default().Goals },
		Procs: procs, Alive: func(int) bool { return alive.Load() },
		Now: time.Now, Log: slog.New(slog.DiscardHandler),
	})
	procs.Add("other-session", 4242, "make build", "", time.Now())
	sid, s := parkGoal(t, m, st, func(s *goals.State) { s.WaitOnPID(4242, "the build", time.Now()) })
	rec := &fireRec{}
	w := &GoalWaker{Goals: m, Fire: rec.fire, checkEvery: 20 * time.Millisecond}
	t.Cleanup(w.Stop)
	w.Arm("k1", sid, s)
	alive.Store(false)
	waitFor(t, func() bool { return len(rec.list()) == 1 })
}
