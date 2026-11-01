package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// draftJudge answers the first call with first after hold is closed. It answers each
// later call with doneVerdict.
type draftJudge struct {
	first string
	hold  chan struct{}
	mu    sync.Mutex
	calls int
}

func (j *draftJudge) Name() string  { return "judge" }
func (j *draftJudge) Model() string { return "judge-model" }

func (j *draftJudge) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Event, error) {
	j.mu.Lock()
	j.calls++
	n := j.calls
	j.mu.Unlock()
	ch := make(chan provider.Event, 2)
	go func() {
		defer close(ch)
		reply := doneVerdict
		if n == 1 {
			reply = j.first
			select {
			case <-j.hold:
			case <-ctx.Done():
				ch <- provider.Event{Type: provider.EventError, Err: ctx.Err()}
				return
			}
		}
		ch <- provider.Event{Type: provider.EventText, Delta: reply}
		ch <- provider.Event{Type: provider.EventDone, FinishReason: "stop", Usage: &provider.Usage{}}
	}()
	return ch, nil
}

func (j *draftJudge) count() int { j.mu.Lock(); defer j.mu.Unlock(); return j.calls }

const draftedJSON = `{"outcome": "20 runs pass", "verification": "go test -count=20 ./internal/cron/", ` +
	`"constraints": "", "boundaries": "", "stop_when": ""}`

func TestGoalDraftRunsInRest(t *testing.T) {
	ctx := context.Background()
	j := &draftJudge{first: draftedJSON, hold: make(chan struct{})}
	fp := &fakeProv{}
	g := &Gateway{Deps: goalDeps(t, fp, j)}
	f := &recFactory{}
	done := make(chan error, 1)
	g.Submit(ctx, inb("k", "/goal draft make the cron tests stable"), f.new, func(err error) { done <- err })
	// Submit returned. The model call runs in the rest of the command.
	if got := f.acks(); len(got) != 1 || got[0] != "Drafting completion contract…" {
		t.Fatalf("%q", got)
	}
	waitFor(t, func() bool { return j.count() == 1 })
	select {
	case err := <-done:
		t.Fatal("the draft must not end before the model answers:", err)
	default:
	}
	// The key is busy during the call.
	busy := &replyRenderer{}
	_ = g.Handle(ctx, inb("k", "/model"), func() Renderer { return busy })
	if len(busy.replies) != 1 || !strings.HasPrefix(busy.replies[0].Text, "⏳ **Busy**") {
		t.Fatalf("%+v", busy.replies)
	}
	close(j.hold)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	sess, err := g.Deps.Store.Active(ctx, "k")
	if err != nil || sess == nil {
		t.Fatal(sess, err)
	}
	st := loadGoal(t, g.Deps, sess.ID)
	if st.Goal != "make the cron tests stable" || st.Status != goals.StatusActive ||
		st.Contract.Outcome != "20 runs pass" || st.Contract.Verification != "go test -count=20 ./internal/cron/" {
		t.Fatalf("%+v", st)
	}
	acks := f.acks()
	want := "⊙ Goal set (20-turn budget): make the cron tests stable\nDrafted completion contract:\n" +
		EscapeMarkdown(st.Contract.Render(nil)) +
		"\nTighten any field by re-setting the goal with inline lines (e.g. verify: <command>), then /goal resume. Use /goal show to review."
	if len(acks) < 2 || acks[1] != want {
		t.Fatalf("got %q, want %q", acks, want)
	}
	waitFor(t, func() bool { return len(fp.lastUserTexts()) >= 1 })
	if got := fp.lastUserTexts(); got[0] != "make the cron tests stable" {
		t.Fatalf("the kick turn must run after the draft: %q", got)
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
}

func TestGoalDraftWithNoContract(t *testing.T) {
	ctx := context.Background()
	j := &draftJudge{first: "not json", hold: make(chan struct{})}
	close(j.hold)
	g := &Gateway{Deps: goalDeps(t, &fakeProv{}, j)}
	f := &recFactory{}
	if err := g.Handle(ctx, inb("k", "/goal draft ship it"), f.new); err != nil {
		t.Fatal(err)
	}
	acks := f.acks()
	want := "⊙ Goal set (20-turn budget): ship it\nCouldn't draft a contract (aux model unavailable) — running as a " +
		"free-form goal. The per-turn judge still applies."
	if len(acks) < 2 || acks[0] != "Drafting completion contract…" || acks[1] != want {
		t.Fatalf("%q", acks)
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
}

func TestGoalDraftEmptyAndCLI(t *testing.T) {
	ctx := context.Background()
	c, rec := goalCtx(t, false)
	run(t, c, "/goal draft")
	if got := rec.lastText(t); got != EscapeMarkdown(goals.ParseCommand("draft").Usage) {
		t.Fatalf("%q", got)
	}
	// The CLI has no rest, so the command runs the draft call itself.
	c.Deps.Goals = newGoalManager(t, c.Deps.Store, fixedJudge{reply: draftedJSON})
	run(t, c, "/goal draft ship the fix")
	if len(rec.sent) < 3 || rec.sent[1].Text != "Drafting completion contract…" ||
		!strings.HasPrefix(rec.sent[2].Text, "⊙ Goal set (20-turn budget): ship the fix\nDrafted completion contract:\n") {
		t.Fatalf("%+v", rec.sent)
	}
	if len(rec.starts) != 1 || rec.starts[0].Text != "ship the fix" {
		t.Fatalf("%+v", rec.starts)
	}
	if st := loadGoal(t, c.Deps, c.Session.ID); st.Contract.Outcome != "20 runs pass" {
		t.Fatalf("%+v", st)
	}
	_ = ctx
}

func TestGoalDraftStoppedDropsTheResult(t *testing.T) {
	ctx := context.Background()
	j := &draftJudge{first: draftedJSON, hold: make(chan struct{})}
	fp := &fakeProv{}
	g := &Gateway{Deps: goalDeps(t, fp, j)}
	sess, err := g.Deps.Store.ActiveOrCreate(ctx, "k", sessions.Session{Source: "telegram", ChatID: "1", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	setGoal(t, g.Deps, sess.ID, "the old goal")
	done := make(chan error, 1)
	g.Submit(ctx, inb("k", "/goal draft a new goal"), (&recFactory{}).new, func(err error) { done <- err })
	waitFor(t, func() bool { return j.count() == 1 })
	_ = g.Handle(ctx, inb("k", "/stop"), func() Renderer { return &recRenderer{} })
	close(j.hold)
	<-done
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	if st := loadGoal(t, g.Deps, sess.ID); st.Goal != "the old goal" || len(fp.lastUserTexts()) != 0 {
		t.Fatalf("a stopped draft must set nothing: %+v %q", st, fp.lastUserTexts())
	}
}

// /stop during the draft call ends the call. So the key is free at once, and the next text
// runs before the model answers.
func TestGoalDraftStopFreesTheKey(t *testing.T) {
	ctx := context.Background()
	j := &draftJudge{first: draftedJSON, hold: make(chan struct{})}
	defer close(j.hold)
	fp := &fakeProv{}
	g := &Gateway{Deps: goalDeps(t, fp, j)}
	done := make(chan error, 1)
	g.Submit(ctx, inb("k", "/goal draft a new goal"), (&recFactory{}).new, func(err error) { done <- err })
	waitFor(t, func() bool { return j.count() == 1 })
	_ = g.Handle(ctx, inb("k", "/stop"), func() Renderer { return &recRenderer{} })
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("/stop must end the draft call")
	}
	if err := g.Handle(ctx, inb("k", "hello"), (&recFactory{}).new); err != nil {
		t.Fatal(err)
	}
	if got := fp.lastUserTexts(); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("only the next text must run: %q", got)
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	sess, err := g.Deps.Store.Active(ctx, "k")
	if err != nil || sess == nil {
		t.Fatal(sess, err)
	}
	if _, ok, err := g.Deps.Goals.Load(ctx, sess.ID); err != nil || ok {
		t.Fatalf("a stopped draft must set no goal: %v %v", ok, err)
	}
}

// panicJudge panics in each call.
type panicJudge struct{}

func (panicJudge) Name() string  { return "judge" }
func (panicJudge) Model() string { return "judge-model" }
func (panicJudge) Stream(context.Context, provider.Request) (<-chan provider.Event, error) {
	panic("judge broke")
}

// A panic in the draft call sends the error reply of the command, so the user sees an end.
func TestGoalDraftPanicSendsErrorReply(t *testing.T) {
	ctx := context.Background()
	g := &Gateway{Deps: goalDeps(t, &fakeProv{}, panicJudge{})}
	f := &recFactory{}
	if err := g.Handle(ctx, inb("k", "/goal draft ship it"), f.new); err == nil {
		t.Fatal("a panic in the draft must give an error")
	}
	want := fmt.Sprintf(commandErrorReply, "goal", EscapeMarkdown("internal error in the goal draft"))
	if acks := f.acks(); len(acks) != 2 || acks[0] != draftingReply || acks[1] != want {
		t.Fatalf("%q", acks)
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
}
