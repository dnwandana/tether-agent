package gateway

import (
	"context"
	"reflect"
	"testing"

	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// targetFix is a dashboard command context with goals, and an active Telegram session
// "Bali trip" with an active goal, two subgoals, and two gates.
type targetFix struct {
	c          *CommandCtx
	sent       *[]Reply
	tg         *sessions.Session
	callerDrop int
	ownerDrops []string
}

func newTargetFix(t *testing.T) *targetFix {
	t.Helper()
	ctx := context.Background()
	c, sent := dashCtx(t)
	c.Deps.Goals = newGoalManager(t, c.Deps.Store, fixedJudge{})
	tg, err := c.Deps.Store.Create(ctx, sessions.Session{Key: "telegram:dm:1", Source: "telegram", ChatID: "1",
		UserID: "1", Kind: "chat", Title: "Bali trip"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Deps.Store.SetActive(ctx, tg.Key, tg.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Deps.Goals.Update(ctx, tg.ID, func(st *goals.State, _ bool) error {
		*st = goals.State{Goal: "ship it", Status: goals.StatusActive, MaxTurns: 20, Epoch: 1,
			Subgoals: []string{"no sleeps", "tests pass"}}
		st.AddGateWith("go test ./...", 0, 0)
		st.AddGateWith("go vet ./...", 0, 0)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f := &targetFix{c: c, sent: sent, tg: tg}
	c.Hooks.DropGoalTurns = func() { f.callerDrop++ }
	c.Hooks.DropGoalTurnsFor = func(key string) { f.ownerDrops = append(f.ownerDrops, key) }
	c.Hooks.StartGoalTurn = func(goals.Continuation) { t.Fatal("a --session command must start no turn") }
	c.Hooks.ArmWake = func(string, goals.State) { t.Fatal("a --session command must arm no waker") }
	return f
}

func (f *targetFix) state(t *testing.T) goals.State {
	t.Helper()
	st, _, err := f.c.Deps.Goals.Load(context.Background(), f.tg.ID)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// Pause and clear of another session drop the goal turns of the owner key only.
func TestGoalPauseClearOtherSession(t *testing.T) {
	f := newTargetFix(t)
	run(t, f.c, "/goal pause --session "+f.tg.ID)
	if r := lastSent(t, f.sent); r.Text != `⏸ Paused the goal of "Bali trip".` {
		t.Fatalf("got:\n%s", r.Text)
	}
	if f.state(t).Status != goals.StatusPaused {
		t.Fatalf("%+v", f.state(t))
	}
	run(t, f.c, "/goal clear --session "+f.tg.ID)
	if r := lastSent(t, f.sent); r.Text != `🗑 Cleared the goal of "Bali trip".` {
		t.Fatalf("got:\n%s", r.Text)
	}
	if f.state(t).Status != goals.StatusCleared {
		t.Fatalf("%+v", f.state(t))
	}
	if f.callerDrop != 0 || !reflect.DeepEqual(f.ownerDrops, []string{"telegram:dm:1", "telegram:dm:1"}) {
		t.Fatal(f.callerDrop, f.ownerDrops)
	}
}

// The removes and the clears of subgoals and gates edit the goal of the target session.
func TestSubgoalGateOpsOtherSession(t *testing.T) {
	f := newTargetFix(t)
	run(t, f.c, "/subgoal remove 1 "+goals.ItemHash("no sleeps")+" --session "+f.tg.ID)
	if got := f.state(t).Subgoals; !reflect.DeepEqual(got, []string{"tests pass"}) {
		t.Fatalf("%q", got)
	}
	run(t, f.c, "/goal gate remove 2 "+goals.ItemHash("go vet ./...")+" --session "+f.tg.ID)
	if got := f.state(t).Gates; len(got) != 1 || got[0].Command != "go test ./..." {
		t.Fatalf("%+v", got)
	}
	run(t, f.c, "/subgoal clear --session "+f.tg.ID)
	run(t, f.c, "/goal gate clear --session "+f.tg.ID)
	if st := f.state(t); len(st.Subgoals) != 0 || len(st.Gates) != 0 || st.Status != goals.StatusActive {
		t.Fatalf("%+v", st)
	}
	if f.callerDrop != 0 {
		t.Fatal("an edit of another session must not drop the turns of the dashboard key")
	}
}

// The flag works only on the dashboard. A CLI session is view only. A missing session gets
// the not-found reply. A goal text keeps the flag as text.
func TestGoalSessionFlagRules(t *testing.T) {
	ctx := context.Background()
	f := newTargetFix(t)
	cli, _ := f.c.Deps.Store.Create(ctx, sessions.Session{Key: "cli:t", Source: "cli", Kind: "chat", Title: "Terminal"})
	run(t, f.c, "/goal pause --session "+cli.ID)
	if r := lastSent(t, f.sent); r.Text != "This goal runs in tether chat. Change it there." {
		t.Fatalf("got:\n%s", r.Text)
	}
	run(t, f.c, "/goal pause --session 0000000000000abcdef")
	if r := lastSent(t, f.sent); r.Text != notFoundText("0000000000000abcdef") {
		t.Fatalf("got:\n%s", r.Text)
	}
	run(t, f.c, "/goal clear --session xyz")
	if r := lastSent(t, f.sent); r.Text != EscapeMarkdown(goals.ErrSessionFlag.Error()) {
		t.Fatalf("got:\n%s", r.Text)
	}
	f.c.Source = "telegram"
	run(t, f.c, "/goal pause --session "+f.tg.ID)
	if r := lastSent(t, f.sent); r.Text != "Usage: --session works only on the dashboard." {
		t.Fatalf("got:\n%s", r.Text)
	}
	if f.state(t).Status != goals.StatusActive {
		t.Fatal("a refused flag must change nothing")
	}
}
