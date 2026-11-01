package gateway

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// queuedGoalTurns returns the queued goal turns of key.
func queuedGoalTurns(g *Gateway, key string) []*pendingTurn {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []*pendingTurn
	for _, p := range g.key(key).queue {
		if p.goalTurn {
			out = append(out, p)
		}
	}
	return out
}

// A continuation that waits in the queue while the owner text runs must not run after the
// judge of the owner text parks the goal.
func TestGoalParkDropsTheQueuedContinuation(t *testing.T) {
	ctx := context.Background()
	j := &judgeProv{hold: make(chan struct{}), replies: []string{
		`{"verdict": "continue", "reason": "the fix is not verified"}`,
		`{"verdict": "wait", "wait_seconds": 3600, "reason": "the build runs"}`,
	}}
	g, fp, f, sess := stepGateway(t, j)
	go func() { _ = g.Handle(ctx, inb("k", "first"), f.new) }()
	waitFor(t, func() bool { return j.count() == 1 })
	_ = g.Handle(ctx, inb("k", "owner note"), f.new)
	waitFor(t, func() bool { rs, ok := g.Running("k"); return ok && rs.Steer.Pending() == "owner note" })
	close(j.hold)
	waitFor(t, func() bool { return j.count() == 2 })
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	if got := fp.lastUserTexts(); len(got) != 2 || got[0] != "first" || got[1] != "owner note" {
		t.Fatalf("no goal turn may run while the goal is parked: %q", got)
	}
	if n := j.count(); n != 2 {
		t.Fatalf("judge calls %d", n)
	}
	if st := wakeState(t, g.Deps.Goals, sess.ID); st.Status != goals.StatusActive || st.WaitingUntil == 0 {
		t.Fatalf("the goal must stay parked: %+v", st)
	}
}

func TestEnqueueGoalKeepsOneQueuedGoalTurn(t *testing.T) {
	ctx := context.Background()
	g, _, _ := goalGateway(t, 0)
	sess, st := activeGoal(t, g, "k1")
	g.mu.Lock()
	g.key("k1").busy = true
	g.mu.Unlock()
	first, second := goalCont(sess, st), goalCont(sess, st)
	second.Text = "CONTINUE AGAIN"
	for _, c := range []goals.Continuation{first, second} {
		if err := g.enqueueGoal(ctx, "k1", c); err != nil {
			t.Fatal(err)
		}
	}
	if q := queuedGoalTurns(g, "k1"); len(q) != 1 || q[0].in.Text != "CONTINUE AGAIN" {
		t.Fatalf("one goal turn, the newest: %d", len(q))
	}
	// A continuation of an older epoch does not replace a newer one.
	old := goalCont(sess, st)
	old.Epoch, old.Text = st.Epoch-1, "OLD"
	if err := g.enqueueGoal(ctx, "k1", old); err != nil {
		t.Fatal(err)
	}
	if q := queuedGoalTurns(g, "k1"); len(q) != 1 || q[0].in.Text != "CONTINUE AGAIN" {
		t.Fatal("an older continuation must not win")
	}
}

// procStepGateway is stepGateway with the process registry procs and the alive check alive.
func procStepGateway(t *testing.T, judge *judgeProv, procs *tools.ProcRegistry, alive *atomic.Bool) (*Gateway, *fakeProv, *recFactory, *sessions.Session) {
	t.Helper()
	ctx := context.Background()
	g, fp := gatewayWithSlowProvider(t, 0)
	g.Deps.Goals = goals.NewManager(goals.Deps{
		Store:          &goals.Store{DB: openTestDB(t)},
		Sessions:       g.Deps.Store,
		Cfg:            func() config.GoalsConfig { return config.Default().Goals },
		ProviderFor:    func(*sessions.Session) provider.Provider { return judge },
		Meta:           func(provider.Provider, provider.Result) sessions.MessageMeta { return sessions.MessageMeta{} },
		RunningBatches: func(string) int { return 0 },
		Procs:          procs,
		TodoOpen:       func(context.Context, string) string { return "" },
		Alive:          func(int) bool { return alive.Load() },
		Now:            time.Now,
		Log:            slog.New(slog.DiscardHandler),
	})
	sess, err := g.Deps.Store.ActiveOrCreate(ctx, "k", sessions.Session{Source: "telegram", ChatID: "1", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Deps.Goals.Update(ctx, sess.ID, func(st *goals.State, _ bool) error {
		*st = goals.NewState("make the cron tests stable", goals.Contract{}, 20, time.Now())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.GoalWaker().Stop() })
	return g, fp, &recFactory{}, sess
}

// The judge parks the goal on a process that exited before the park. The continuation must
// come at once, not at the end of the barrier limit.
func TestGoalParkOnAnExitedProcessContinuesAtOnce(t *testing.T) {
	ctx := context.Background()
	procs := tools.NewProcRegistry()
	alive := &atomic.Bool{}
	alive.Store(true)
	j := &judgeProv{replies: []string{
		`{"verdict": "wait", "wait_on_session": "4242", "reason": "the build runs"}`,
		`{"verdict": "done", "reason": "the build passed"}`,
	}}
	g, fp, f, sess := procStepGateway(t, j, procs, alive)
	procs.Add(sess.ID, 4242, "make build", "", time.Now())
	procs.Exit(4242, 0, time.Now())
	if err := g.Handle(ctx, inb("k", "first"), f.new); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(fp.lastUserTexts()) == 2 })
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	if got := fp.lastUserTexts(); len(got) != 2 || !strings.HasPrefix(got[1], goals.ContinuationPrefix) {
		t.Fatalf("%q", got)
	}
	if st := wakeState(t, g.Deps.Goals, sess.ID); st.Status != goals.StatusDone {
		t.Fatalf("%+v", st)
	}
}

// Arm on a barrier that already lifted fires at once. A pid outside the registry that exited
// must not wait for the next alive check.
func TestArmFiresAtOnceForALiftedBarrier(t *testing.T) {
	for _, c := range []struct {
		name string
		park func(s *goals.State)
	}{
		{"pid", func(s *goals.State) { s.WaitOnPID(4242, "the build", time.Now()) }},
		{"process", func(s *goals.State) { s.WaitOnSession("4242", "the build", time.Now()) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, st, alive := newWakeRig(t)
			sid, s := parkGoal(t, m, st, c.park)
			alive.Store(false) // the process exited before Arm
			rec := &fireRec{}
			w := &GoalWaker{Goals: m, Fire: rec.fire, checkEvery: time.Hour}
			t.Cleanup(w.Stop)
			w.Arm("k1", sid, s)
			waitFor(t, func() bool { return len(rec.list()) == 1 })
			waitFor(t, func() bool { return !hasBarrier(wakeState(t, m, sid)) })
			time.Sleep(30 * time.Millisecond)
			if len(rec.list()) != 1 {
				t.Fatal("one continuation only:", len(rec.list()))
			}
		})
	}
}

// A wake can queue a goal turn after the step saw the barrier hold. The still-parked result
// of the step writes no decision, so it must keep that turn. A new park of the judge is a
// new decision, so it removes the turn.
func TestStillParkedStepKeepsTheWakeTurn(t *testing.T) {
	ctx := context.Background()
	g, _, _ := goalGateway(t, 0)
	sess, st := activeGoal(t, g, "k1")
	g.mu.Lock()
	g.key("k1").busy = true
	g.mu.Unlock()
	if err := g.enqueueGoal(ctx, "k1", goalCont(sess, st)); err != nil {
		t.Fatal(err)
	}
	still := goals.StepResult{Ran: true, Parked: true, Notices: []goals.Notice{{Kind: goals.NoticeStillParked, Target: "pid 4242"}}}
	g.applyGoalDecision("k1", sess.ID, st.Epoch, &pendingTurn{}, &RunState{}, still)
	if q := queuedGoalTurns(g, "k1"); len(q) != 1 {
		t.Fatalf("a still-parked step must keep the queued wake turn: %d", len(q))
	}
	parked := goals.StepResult{Ran: true, Parked: true, Notices: []goals.Notice{{Kind: goals.NoticeParkedJudge, Target: "60s"}}}
	g.applyGoalDecision("k1", sess.ID, st.Epoch, &pendingTurn{}, &RunState{}, parked)
	if q := queuedGoalTurns(g, "k1"); len(q) != 0 {
		t.Fatalf("a new park must remove the queued goal turn: %d", len(q))
	}
}
