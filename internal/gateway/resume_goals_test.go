package gateway

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// goalResumeGateway is resumeGateway with a goal manager and a recorder of the notices
// that have no turn.
func goalResumeGateway(t *testing.T) (*Gateway, *fakeProv, *sessions.Session, func() []Reply) {
	t.Helper()
	g, fp, _, _, sess := resumeGateway(t, 0)
	g.Deps.Goals = newGoalManager(t, g.Deps.Store, fixedJudge{reply: doneVerdict})
	var mu sync.Mutex
	var notices []Reply
	g.Deps.SendNotice = func(_ context.Context, _ string, _ *sessions.Session, r Reply) error {
		mu.Lock()
		notices = append(notices, r)
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { g.GoalWaker().Stop() })
	return g, fp, sess, func() []Reply { mu.Lock(); defer mu.Unlock(); return append([]Reply(nil), notices...) }
}

func TestResumeOutcomes(t *testing.T) {
	ctx := context.Background()
	g, fp, _, _, sess := resumeGateway(t, 0)
	now := time.Now()
	putMarker(t, g.Deps.Store, "k", sess.ID, nil, now, 0)
	putMarker(t, g.Deps.Store, "loop", "s-loop", nil, now, maxResumes)
	putMarker(t, g.Deps.Store, "old", "s-old", nil, now.Add(-2*time.Hour), 0)
	got, err := g.ResumeInterrupted(ctx, time.Time{}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]ResumeOutcome{"k": ResumeResumed, "loop": ResumeLoopStopped, "old": ResumeTooOld}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	waitFor(t, func() bool { return len(fp.lastUserTexts()) == 1 })
}

func TestResumeInStepMarkerSkipsTheNote(t *testing.T) {
	ctx := context.Background()
	g, fp, _, _, sess := resumeGateway(t, 0)
	putMarker(t, g.Deps.Store, "k", sess.ID, []sessions.QueuedText{{Text: "later"}}, time.Now(), 0)
	if err := g.Deps.Store.SetMarkerInStep(ctx, "k", true); err != nil {
		t.Fatal(err)
	}
	got, err := g.ResumeInterrupted(ctx, time.Now(), time.Hour)
	if err != nil || got["k"] != ResumeInStep {
		t.Fatal(got, err)
	}
	waitFor(t, func() bool { return len(fp.lastUserTexts()) == 1 })
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	if texts := fp.lastUserTexts(); !reflect.DeepEqual(texts, []string{"later"}) {
		t.Fatalf("a marker in the step gets no resume note: %q", texts)
	}
}

func TestResumeGoalsFreshGoalContinues(t *testing.T) {
	ctx := context.Background()
	g, fp, sess, _ := goalResumeGateway(t)
	st := setGoal(t, g.Deps, sess.ID, "make the cron tests stable")
	if err := g.ResumeGoals(ctx, time.Now().Add(-time.Minute), time.Hour, map[string]ResumeOutcome{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(fp.lastUserTexts()) >= 1 })
	if got := fp.lastUserTexts()[0]; got != goals.ContinuationPrompt(st, nil) {
		t.Fatalf("%q", got)
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
}

func TestResumeGoalsInStepOutcomeContinues(t *testing.T) {
	ctx := context.Background()
	g, fp, sess, _ := goalResumeGateway(t)
	setGoal(t, g.Deps, sess.ID, "ship it")
	if err := g.ResumeGoals(ctx, time.Now(), time.Hour, map[string]ResumeOutcome{"k": ResumeInStep}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(fp.lastUserTexts()) >= 1 })
	if !strings.HasPrefix(fp.lastUserTexts()[0], goals.ContinuationPrefix) {
		t.Fatalf("%q", fp.lastUserTexts())
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
}

func TestResumeGoalsPausesAnOldGoal(t *testing.T) {
	ctx := context.Background()
	for _, freshness := range []time.Duration{time.Hour, 0} {
		g, fp, sess, notices := goalResumeGateway(t)
		setGoal(t, g.Deps, sess.ID, "ship it")
		if err := g.ResumeGoals(ctx, time.Now().Add(-2*time.Hour), freshness, map[string]ResumeOutcome{}); err != nil {
			t.Fatal(err)
		}
		noTurn(t, g, fp)
		if st := loadGoal(t, g.Deps, sess.ID); st.Status != goals.StatusPaused {
			t.Fatalf("freshness %s: %+v", freshness, st)
		}
		got := notices()
		if len(got) != 1 || !strings.HasPrefix(got[0].Text, "⏸ Goal paused — the gateway was down for ") ||
			!reflect.DeepEqual(got[0].Buttons, StopButtons()) {
			t.Fatalf("freshness %s: %+v", freshness, got)
		}
	}
}

func TestResumeGoalsPausesALoopAndASessionChange(t *testing.T) {
	ctx := context.Background()
	g, fp, sess, _ := goalResumeGateway(t)
	setGoal(t, g.Deps, sess.ID, "ship it")
	if err := g.ResumeGoals(ctx, time.Now(), time.Hour, map[string]ResumeOutcome{"k": ResumeLoopStopped}); err != nil {
		t.Fatal(err)
	}
	noTurn(t, g, fp)
	if st := loadGoal(t, g.Deps, sess.ID); st.Status != goals.StatusPaused || *st.PausedReason != ReasonLoop {
		t.Fatalf("%+v", st)
	}

	g2, fp2, old, _ := goalResumeGateway(t)
	setGoal(t, g2.Deps, old.ID, "ship it")
	next, err := g2.Deps.Store.Create(ctx, sessions.Session{Key: "k", Source: "telegram", ChatID: "1", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if err := g2.Deps.Store.SetActive(ctx, "k", next.ID); err != nil {
		t.Fatal(err)
	}
	if err := g2.ResumeGoals(ctx, time.Now(), time.Hour, map[string]ResumeOutcome{}); err != nil {
		t.Fatal(err)
	}
	noTurn(t, g2, fp2)
	if st := loadGoal(t, g2.Deps, old.ID); st.Status != goals.StatusPaused || *st.PausedReason != ReasonSessionChanged {
		t.Fatalf("%+v", st)
	}
}

func TestResumeGoalsLeavesAResumedTurnAndACLIKey(t *testing.T) {
	ctx := context.Background()
	g, fp, sess, _ := goalResumeGateway(t)
	setGoal(t, g.Deps, sess.ID, "ship it")
	cli, err := g.Deps.Store.ActiveOrCreate(ctx, "cli:test", sessions.Session{Source: "cli", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	setGoal(t, g.Deps, cli.ID, "a cli goal")
	if err := g.ResumeGoals(ctx, time.Now().Add(-2*time.Hour), time.Hour, map[string]ResumeOutcome{"k": ResumeResumed}); err != nil {
		t.Fatal(err)
	}
	noTurn(t, g, fp)
	for _, id := range []string{sess.ID, cli.ID} {
		if st := loadGoal(t, g.Deps, id); st.Status != goals.StatusActive {
			t.Fatalf("%s: %+v", id, st)
		}
	}
}

func TestResumeGoalsRearmsAParkedGoal(t *testing.T) {
	ctx := context.Background()
	g, fp, sess, notices := goalResumeGateway(t)
	setGoal(t, g.Deps, sess.ID, "ship it")
	if _, err := g.Deps.Goals.Update(ctx, sess.ID, func(st *goals.State, _ bool) error {
		st.WaitOnSession("4242", "build", time.Now())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A parked goal does not pause for its age.
	if err := g.ResumeGoals(ctx, time.Now().Add(-2*time.Hour), time.Hour, map[string]ResumeOutcome{}); err != nil {
		t.Fatal(err)
	}
	noTurn(t, g, fp)
	st := loadGoal(t, g.Deps, sess.ID)
	if st.Status != goals.StatusActive || st.WaitingOnSession != nil || st.WaitingOnPID == nil || *st.WaitingOnPID != 4242 {
		t.Fatalf("a process barrier must become a pid barrier: %+v", st)
	}
	if len(notices()) != 0 {
		t.Fatalf("%+v", notices())
	}
}

func TestResumeGoalsLoopPauseSendsTheNotice(t *testing.T) {
	ctx := context.Background()
	g, fp, sess, notices := goalResumeGateway(t)
	setGoal(t, g.Deps, sess.ID, "ship it")
	if err := g.ResumeGoals(ctx, time.Now(), time.Hour, map[string]ResumeOutcome{"k": ResumeLoopStopped}); err != nil {
		t.Fatal(err)
	}
	noTurn(t, g, fp)
	got := notices()
	want := "⏸ Goal paused — " + ReasonLoop + ". Use /goal resume to keep going, or /goal clear to stop."
	if len(got) != 1 || StripMarkdown(got[0].Text) != want || !reflect.DeepEqual(got[0].Buttons, StopButtons()) {
		t.Fatalf("%+v", got)
	}
}
