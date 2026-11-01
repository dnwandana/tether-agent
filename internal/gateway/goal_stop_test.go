package gateway

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// goalBusyGateway returns a gateway with an active goal on key "k" and a turn that runs
// for 2 s. The debounce window is short.
func goalBusyGateway(t *testing.T) (*Gateway, *fakeProv, *sessions.Session) {
	t.Helper()
	ctx := context.Background()
	fp := &fakeProv{delay: 2 * time.Second}
	g := &Gateway{Deps: goalDeps(t, fp, fixedJudge{reply: doneVerdict})}
	g.debounce = NewDebouncer(time.Millisecond, 10*time.Millisecond)
	sess, err := g.Deps.Store.ActiveOrCreate(ctx, "k", sessions.Session{Source: "telegram", ChatID: "1", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	setGoal(t, g.Deps, sess.ID, "ship it")
	done := make(chan struct{})
	go func() { _ = g.Handle(ctx, inb("k", "first"), (&recFactory{}).new); close(done) }()
	waitFor(t, func() bool { return len(fp.lastUserTexts()) == 1 })
	t.Cleanup(func() {
		if rs, ok := g.Running("k"); ok {
			rs.Cancel()
		}
		<-done
		g.Wait(5 * time.Second)
	})
	return g, fp, sess
}

// command runs text on key "k" of g and returns the renderer of the reply.
func command(g *Gateway, text string) *replyRenderer {
	r := &replyRenderer{}
	_ = g.Handle(context.Background(), inb("k", text), func() Renderer { return r })
	return r
}

func TestGoalStopTexts(t *testing.T) {
	if GoalPausedLine != "⏸ Goal paused. Use /goal resume to continue." ||
		GoalBusyLine != "A goal runs. Send /goal pause first." {
		t.Fatal(GoalPausedLine, GoalBusyLine)
	}
	if ReasonStopped != "stopped by /stop" || ReasonSessionChanged != "the session changed" ||
		ReasonChatEnded != "the chat ended" || ReasonLoop != "the turn crashed the gateway 3 times" {
		t.Fatal(ReasonStopped, ReasonSessionChanged, ReasonChatEnded, ReasonLoop)
	}
}

func TestPauseGoal(t *testing.T) {
	ctx := context.Background()
	d := goalDeps(t, &fakeProv{}, fixedJudge{reply: doneVerdict})
	if ok, err := pauseGoal(ctx, d, "s1", ReasonStopped); ok || err != nil {
		t.Fatal(ok, err)
	}
	before := setGoal(t, d, "s1", "ship it")
	if _, err := d.Goals.Update(ctx, "s1", func(st *goals.State, _ bool) error {
		st.WaitOnPID(os.Getpid(), "build", time.Now())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if ok, err := pauseGoal(ctx, d, "s1", ReasonStopped); !ok || err != nil {
		t.Fatal(ok, err)
	}
	st := loadGoal(t, d, "s1")
	if st.Status != goals.StatusPaused || st.PausedReason == nil || *st.PausedReason != ReasonStopped ||
		st.WaitingOnPID != nil || st.Epoch != before.Epoch+1 {
		t.Fatalf("%+v", st)
	}
	if ok, err := pauseGoal(ctx, d, "s1", ReasonStopped); ok || err != nil {
		t.Fatal("a paused goal does not pause again:", ok, err)
	}
	if ok, err := pauseGoal(ctx, &Deps{}, "s1", ReasonStopped); ok || err != nil {
		t.Fatal("a nil manager pauses nothing:", ok, err)
	}
}

func TestStopPausesTheGoal(t *testing.T) {
	g, _, sess := goalBusyGateway(t)
	r := command(g, "/stop")
	if want := []string{StopReply + "\n" + GoalPausedLine}; !reflect.DeepEqual(r.acks, want) {
		t.Fatalf("got %q, want %q", r.acks, want)
	}
	st := loadGoal(t, g.Deps, sess.ID)
	if st.Status != goals.StatusPaused || *st.PausedReason != ReasonStopped {
		t.Fatalf("%+v", st)
	}
}

func TestStopKeepPausesTheGoalAndKeepsTheQueue(t *testing.T) {
	g, fp, sess := goalBusyGateway(t)
	fp.mu.Lock()
	fp.delay = 0
	fp.mu.Unlock()
	go func() { _ = g.Handle(context.Background(), media("k"), (&recFactory{}).new) }()
	waitFor(t, func() bool { return g.queueLen("k") == 1 })
	r := command(g, "/stop keep")
	if want := []string{stopKeepOneReply + "\n" + GoalPausedLine}; !reflect.DeepEqual(r.acks, want) {
		t.Fatalf("got %q, want %q", r.acks, want)
	}
	waitFor(t, func() bool { return len(fp.lastUserTexts()) == 2 })
	if st := loadGoal(t, g.Deps, sess.ID); st.Status != goals.StatusPaused || *st.PausedReason != ReasonStopped {
		t.Fatalf("%+v", st)
	}
}

func TestStopOnAnIdleKeyPausesAParkedGoal(t *testing.T) {
	ctx := context.Background()
	g := &Gateway{Deps: goalDeps(t, &fakeProv{}, fixedJudge{reply: doneVerdict})}
	r := command(g, "/stop")
	if want := []string{NoTaskReply}; !reflect.DeepEqual(r.acks, want) {
		t.Fatalf("no goal: got %q", r.acks)
	}
	sess, err := g.Deps.Store.Active(ctx, "k")
	if err != nil || sess == nil {
		t.Fatal(sess, err)
	}
	setGoal(t, g.Deps, sess.ID, "ship it")
	if _, err := g.Deps.Goals.Update(ctx, sess.ID, func(st *goals.State, _ bool) error {
		st.WaitOnPID(os.Getpid(), "build", time.Now())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r = command(g, "/stop")
	if want := []string{GoalPausedLine}; !reflect.DeepEqual(r.acks, want) {
		t.Fatalf("got %q, want %q", r.acks, want)
	}
	if st := loadGoal(t, g.Deps, sess.ID); st.Status != goals.StatusPaused || st.WaitingOnPID != nil {
		t.Fatalf("%+v", st)
	}
	r = command(g, "/stop keep")
	if want := []string{NoTaskReply}; !reflect.DeepEqual(r.acks, want) {
		t.Fatalf("a paused goal does not pause again: %q", r.acks)
	}
}

func TestBusyReplyNamesTheGoal(t *testing.T) {
	g, _, _ := goalBusyGateway(t)
	r := command(g, "/model")
	want := Reply{
		Text:    fmt.Sprintf(BusyCommandReply, "model") + "\n" + GoalBusyLine,
		Buttons: [][]Button{{{Text: "⏹ Stop the task", Data: "/stop"}}},
	}
	if len(r.replies) != 1 || !reflect.DeepEqual(r.replies[0], want) {
		t.Fatalf("%+v", r.replies)
	}
	command(g, "/goal pause")
	// The turn ends, and a paused goal starts no continuation, so the key goes free.
	waitFor(t, func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return !g.key("k").busy
	})
	r = command(g, "/model")
	if len(r.replies) != 1 || r.replies[0].Text == want.Text || r.replies[0].Text == fmt.Sprintf(BusyCommandReply, "model") {
		t.Fatalf("/model must work after the pause: %+v", r.replies)
	}
}

func TestGoalStepStopCancelsTheJudge(t *testing.T) {
	ctx := context.Background()
	j := &judgeProv{hold: make(chan struct{}), replies: []string{`{"verdict": "continue", "reason": "not yet"}`}}
	g, fp, f, _ := stepGateway(t, j)
	go func() { _ = g.Handle(ctx, inb("k", "first"), f.new) }()
	waitFor(t, func() bool { return j.count() == 1 })
	_ = g.Handle(ctx, inb("k", "/stop"), f.new)
	waitFor(t, j.wasCanceled)
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	if got := fp.lastUserTexts(); len(got) != 1 {
		t.Fatalf("no continuation must run after /stop: %q", got)
	}
}
