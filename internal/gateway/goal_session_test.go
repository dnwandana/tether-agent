package gateway

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// wantPaused fails the test when the goal of the session is not paused with reason.
func wantPaused(t *testing.T, d *Deps, sessionID, reason string) {
	t.Helper()
	st := loadGoal(t, d, sessionID)
	if st.Status != goals.StatusPaused || st.PausedReason == nil || *st.PausedReason != reason {
		t.Fatalf("%+v", st)
	}
}

// noGoal fails the test when the session has a goal row.
func noGoal(t *testing.T, d *Deps, sessionID string) {
	t.Helper()
	if _, ok, err := d.Goals.Load(context.Background(), sessionID); ok || err != nil {
		t.Fatal(ok, err)
	}
}

func TestNewPausesTheGoalOfTheOldSession(t *testing.T) {
	c, _ := goalCtx(t, false)
	old := c.Session
	setGoal(t, c.Deps, old.ID, "ship it")
	run(t, c, "/new")
	wantPaused(t, c.Deps, old.ID, ReasonSessionChanged)
	noGoal(t, c.Deps, c.Session.ID)
}

func TestNewDuringAGoalTurnUsesTheSessionReason(t *testing.T) {
	g, _, sess := goalBusyGateway(t)
	command(g, "/new")
	// /new stops the run with stopKey, not with the Stop hook of /stop.
	wantPaused(t, g.Deps, sess.ID, ReasonSessionChanged)
}

func TestResumePausesTheOldGoalAndContinuesTheNewOne(t *testing.T) {
	ctx := context.Background()
	c, rec := goalCtx(t, false)
	a := c.Session
	setGoal(t, c.Deps, a.ID, "goal of a")
	run(t, c, "/new")
	b := c.Session
	setGoal(t, c.Deps, b.ID, "goal of b")
	// A paused goal stays paused after /resume.
	run(t, c, "/resume "+a.ID)
	wantPaused(t, c.Deps, b.ID, ReasonSessionChanged)
	if len(rec.starts) != 0 {
		t.Fatalf("a paused goal starts no turn: %+v", rec.starts)
	}
	// An active goal gets one continuation.
	stB, err := c.Deps.Goals.Update(ctx, b.ID, func(st *goals.State, _ bool) error { st.Resume(); return nil })
	if err != nil {
		t.Fatal(err)
	}
	run(t, c, "/resume "+b.ID)
	wantPaused(t, c.Deps, a.ID, ReasonSessionChanged)
	if len(rec.starts) != 1 || rec.starts[0].SessionID != b.ID ||
		rec.starts[0].Text != goals.ContinuationPrompt(stB, nil) || rec.starts[0].Epoch != stB.Epoch {
		t.Fatalf("%+v", rec.starts)
	}
	if active(t, c) != b.ID {
		t.Fatal("the key must point at b")
	}
}

func TestResumeOfTheSameSessionKeepsTheGoal(t *testing.T) {
	c, rec := goalCtx(t, false)
	setGoal(t, c.Deps, c.Session.ID, "ship it")
	run(t, c, "/resume "+c.Session.ID)
	if st := loadGoal(t, c.Deps, c.Session.ID); st.Status != goals.StatusActive || len(rec.starts) != 0 {
		t.Fatalf("%+v %+v", st, rec.starts)
	}
}

func TestForkPausesTheGoal(t *testing.T) {
	c, _ := goalCtx(t, false)
	old := c.Session
	setGoal(t, c.Deps, old.ID, "ship it")
	run(t, c, "/fork the copy")
	if c.Session.ID == old.ID {
		t.Fatal("the key must move to the fork")
	}
	wantPaused(t, c.Deps, old.ID, ReasonSessionChanged)
	noGoal(t, c.Deps, c.Session.ID)
}

func TestArchivePausesTheGoal(t *testing.T) {
	c, _ := goalCtx(t, false)
	setGoal(t, c.Deps, c.Session.ID, "ship it")
	run(t, c, "/archive")
	wantPaused(t, c.Deps, c.Session.ID, ReasonSessionChanged)
}

// sharedGoalCtx returns goalCtx with the sessions and the goals in one database, so a
// session delete reaches the goal row.
func sharedGoalCtx(t *testing.T) *CommandCtx {
	t.Helper()
	c, _ := goalCtx(t, false)
	shared := openTestDB(t)
	st := sessions.New(shared)
	c.Deps.Store, c.Deps.Agent.Store = st, st
	c.Deps.Goals = goals.NewManager(goals.Deps{
		Store:    &goals.Store{DB: shared},
		Sessions: st,
		Cfg:      func() config.GoalsConfig { return config.Default().Goals },
		Now:      time.Now,
		Log:      slog.New(slog.DiscardHandler),
	})
	c.Session = nil
	if _, err := c.current(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDeleteRemovesTheGoal(t *testing.T) {
	c := sharedGoalCtx(t)
	old := c.Session
	setGoal(t, c.Deps, old.ID, "ship it")
	run(t, c, "/new")
	run(t, c, "/delete "+old.ID+" confirm")
	noGoal(t, c.Deps, old.ID)
}
