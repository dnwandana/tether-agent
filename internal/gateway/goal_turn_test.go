package gateway

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// goalGateway returns a gateway with a goal manager and a recording telegram channel.
// The judge parks the goal for an hour after each turn, so the step adds no continuation of
// its own. The park also removes each goal turn that the queue held before it.
func goalGateway(t *testing.T, delay time.Duration) (*Gateway, *fakeProv, *recChannel) {
	t.Helper()
	fp := &fakeProv{delay: delay}
	d := testDeps(t, fp)
	park := `{"verdict": "wait", "wait_seconds": 3600, "reason": "test park"}`
	judge := &judgeProv{replies: slices.Repeat([]string{park}, 64)}
	d.Goals = goals.NewManager(goals.Deps{
		Store: &goals.Store{DB: openTestDB(t)}, Sessions: d.Store,
		Cfg:         func() config.GoalsConfig { return config.Default().Goals },
		ProviderFor: func(*sessions.Session) provider.Provider { return judge },
		Now:         time.Now, Log: slog.New(slog.DiscardHandler),
	})
	ch := &recChannel{sent: make(chan Outbound, 16)}
	return &Gateway{Deps: d, Channels: map[string]Channel{"telegram": ch}}, fp, ch
}

// activeGoal makes the active session of key and gives it an active goal.
func activeGoal(t *testing.T, g *Gateway, key string) (*sessions.Session, goals.State) {
	t.Helper()
	ctx := context.Background()
	sess, err := g.Deps.Store.ActiveOrCreate(ctx, key, sessions.Session{Source: "telegram", ChatID: "1", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := g.Deps.Goals.Update(ctx, sess.ID, func(st *goals.State, _ bool) error {
		*st = goals.NewState("ship the fix", goals.Contract{}, 20, time.Now())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sess, st
}

func goalCont(sess *sessions.Session, st goals.State) goals.Continuation {
	return goals.Continuation{SessionID: sess.ID, Text: "CONTINUE", Epoch: st.Epoch, LineKind: goals.LineContinue, Reason: "not verified", Used: 1, Max: 20}
}

func TestGoalTurnRunsAfterTheQueuedOwnerMessages(t *testing.T) {
	ctx := context.Background()
	j := &judgeProv{hold: make(chan struct{}), replies: []string{
		`{"verdict": "continue", "reason": "the fix is not verified"}`,
		`{"verdict": "continue", "reason": "run the tests 20 times"}`,
		`{"verdict": "done", "reason": "20 runs pass"}`,
	}}
	g, fp, f, _ := stepGateway(t, j)
	go func() { _ = g.Handle(ctx, inb("k", "first"), f.new) }()
	waitFor(t, func() bool { return j.count() == 1 })
	owner := inb("k", "owner file")
	owner.IsText = false // not a steer, so it waits in the queue
	g.Submit(ctx, owner, f.new, nil)
	waitFor(t, func() bool { g.mu.Lock(); defer g.mu.Unlock(); return len(g.key("k").queue) == 1 })
	close(j.hold)
	waitFor(t, func() bool { return len(fp.lastUserTexts()) == 3 })
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	// The step of the owner file replaces the continuation of the first turn, so one
	// continuation runs.
	if got := fp.lastUserTexts(); len(got) != 3 || got[0] != "first" || got[1] != "owner file" || !strings.HasPrefix(got[2], goals.ContinuationPrefix) {
		t.Fatalf("%q", got)
	}
}

// The tests of the stale check call turn directly. The step of a turn that runs before a
// queued goal turn replaces that goal turn, so the queue cannot hold one for the check.
func TestStaleGoalTurnIsDropped(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(st *goals.State)
		runs bool
	}{
		{"pause", func(st *goals.State) { st.SetStatus(goals.StatusPaused, "paused by the owner") }, false},
		{"clear", func(st *goals.State) { st.SetStatus(goals.StatusCleared, "") }, false},
		{"subgoal", func(st *goals.State) { _, _ = st.AddSubgoal("the tests pass") }, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			g, fp, _ := goalGateway(t, 0)
			sess, st := activeGoal(t, g, "k1")
			if _, err := g.Deps.Goals.Update(ctx, sess.ID, func(s *goals.State, _ bool) error { c.edit(s); return nil }); err != nil {
				t.Fatal(err)
			}
			p := &pendingTurn{in: inb("k1", "CONTINUE"), newRenderer: (&recFactory{}).new,
				goalTurn: true, goalSession: sess.ID, goalEpoch: st.Epoch}
			end := g.turn(ctx, p)
			if got := fp.lastUserTexts(); end.ran != c.runs || (len(got) == 1) != c.runs {
				t.Fatal(end.ran, got)
			}
		})
	}
}

func TestGoalTurnOfAnotherSessionIsDropped(t *testing.T) {
	ctx := context.Background()
	g, fp, _ := goalGateway(t, 0)
	sess, st := activeGoal(t, g, "k1")
	other, err := g.Deps.Store.Create(ctx, sessions.Session{Key: "k1", Source: "telegram", ChatID: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Deps.Store.SetActive(ctx, "k1", other.ID); err != nil {
		t.Fatal(err)
	}
	p := &pendingTurn{in: inb("k1", "CONTINUE"), newRenderer: (&recFactory{}).new,
		goalTurn: true, goalSession: sess.ID, goalEpoch: st.Epoch}
	if end := g.turn(ctx, p); end.ran {
		t.Fatal("the continuation of the old session must not run")
	}
	if got := fp.lastUserTexts(); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestGoalTurnOnAFreeKeyIsChecked(t *testing.T) {
	ctx := context.Background()
	g, fp, ch := goalGateway(t, 0)
	sess, st := activeGoal(t, g, "k1")
	if _, err := g.Deps.Goals.Update(ctx, sess.ID, func(s *goals.State, _ bool) error {
		s.SetStatus(goals.StatusPaused, "paused by the owner")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := g.enqueueGoal(ctx, "k1", goalCont(sess, st)); err != nil {
		t.Fatal(err)
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	if got := fp.lastUserTexts(); len(got) != 0 {
		t.Fatal(got)
	}
	// A current continuation on a free key runs. Its goal line goes out before the answer.
	cur, err := g.Deps.Goals.Update(ctx, sess.ID, func(s *goals.State, _ bool) error {
		s.SetStatus(goals.StatusActive, "")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.enqueueGoal(ctx, "k1", goalCont(sess, cur)); err != nil {
		t.Fatal(err)
	}
	if first := <-ch.sent; first.Text != "↻ Goal 1/20: not verified" {
		t.Fatalf("%q", first.Text)
	}
	if answer := <-ch.sent; answer.Text != "ok" {
		t.Fatalf("%q", answer.Text)
	}
}

// goalLineRenderer is a recRenderer that records the goal lines and their place.
type goalLineRenderer struct {
	recRenderer
	lines      []string
	afterDelta bool
}

func (r *goalLineRenderer) GoalLine(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.afterDelta = r.afterDelta || r.text.Len() > 0
	r.lines = append(r.lines, line)
}

func TestGoalLineGoesBeforeTheTurn(t *testing.T) {
	ctx := context.Background()
	g, _, _ := goalGateway(t, 0)
	sess, st := activeGoal(t, g, "k1")
	var mu sync.Mutex
	var list []*goalLineRenderer
	factory := func() Renderer {
		mu.Lock()
		defer mu.Unlock()
		r := &goalLineRenderer{}
		list = append(list, r)
		return r
	}
	if err := g.Handle(ctx, inb("k1", "first"), factory); err != nil {
		t.Fatal(err)
	}
	if err := g.enqueueGoal(ctx, "k1", goalCont(sess, st)); err != nil {
		t.Fatal(err)
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(list) != 2 || len(list[0].lines) != 0 || strings.Join(list[1].lines, "|") != "↻ Goal 1/20: not verified" || list[1].afterDelta {
		t.Fatalf("%d renderers, %+v", len(list), list)
	}
}

func TestQueuedTextsAndDropGoalTurns(t *testing.T) {
	g, _, _ := goalGateway(t, 0)
	g.mu.Lock()
	ks := g.key("k1")
	ks.busy = true
	ks.queue = []*pendingTurn{{in: inb("k1", "a")}, {in: inb("k1", "CONTINUE"), goalTurn: true, system: true}, {in: inb("k1", "b")}, {in: inb("k1", "CONTINUE"), goalTurn: true, system: true}}
	texts := queuedTexts(ks.queue)
	g.mu.Unlock()
	if len(texts) != 2 || texts[0].Text != "a" || texts[1].Text != "b" {
		t.Fatalf("the marker must not save a goal turn: %+v", texts)
	}
	if n := g.dropGoalTurns("k1"); n != 2 {
		t.Fatal(n)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(ks.queue) != 2 || ks.queue[0].in.Text != "a" || ks.queue[1].in.Text != "b" {
		t.Fatal("the owner messages stay")
	}
}

func TestTurnReturnsTheResult(t *testing.T) {
	ctx := context.Background()
	g, _, _ := goalGateway(t, 0)
	f := &recFactory{}
	end := g.turn(ctx, &pendingTurn{in: inb("k1", "hi"), newRenderer: f.new})
	if !end.ran || end.err != nil || end.res.Final != "ok" || end.sess == nil || end.r == nil {
		t.Fatalf("%+v", end)
	}
	sess, st := activeGoal(t, g, "k2")
	stale := &pendingTurn{in: inb("k2", "CONTINUE"), newRenderer: f.new, goalTurn: true, goalSession: sess.ID, goalEpoch: st.Epoch + 1}
	if end := g.turn(ctx, stale); end.ran || end.r != nil {
		t.Fatalf("a stale goal turn must not run: %+v", end)
	}
}

// A goal line holds model text, for example the reason of the judge. A turn with no status
// bubble sends the line as a Markdown message, so the line goes out escaped.
func TestSendRendererEscapesGoalLine(t *testing.T) {
	ch := &recChannel{sent: make(chan Outbound, 1)}
	s := &sendRenderer{ctx: context.Background(), ch: ch, log: slog.New(slog.DiscardHandler)}
	line := "↻ Continuing toward goal (1/20): fix *a* and _b_ in `c` [d]"
	s.GoalLine(line)
	select {
	case m := <-ch.sent:
		if m.Text != EscapeMarkdown(line) {
			t.Fatalf("%q", m.Text)
		}
	default:
		t.Fatal("the goal line must go out")
	}
}

// A turn with no status bubble sends each notice as its own message, for example the
// compaction notice after a restart. An empty notice sends nothing.
func TestSendRendererSendsNotice(t *testing.T) {
	ch := &recChannel{sent: make(chan Outbound, 2)}
	to := Target{Source: "telegram", ChatID: "42"}
	s := &sendRenderer{ctx: context.Background(), ch: ch, to: to, log: slog.New(slog.DiscardHandler)}
	const notice = "🗜 Context compacted. 2954 messages → 1 summary. Context: ~210k → ~38k tokens."
	s.Notice(notice)
	select {
	case m := <-ch.sent:
		if m.Text != notice || ch.to != to {
			t.Fatalf("%+v %+v", m, ch.to)
		}
	default:
		t.Fatal("the notice must go out")
	}
	s.Notice("")
	s.Notice(" \n ")
	select {
	case m := <-ch.sent:
		t.Fatalf("an empty notice must send nothing: %q", m.Text)
	default:
	}
}

// send also sends notices now, so its log text names a message, not an answer.
func TestSendRendererLogsFailedNotice(t *testing.T) {
	var buf bytes.Buffer
	s := &sendRenderer{ctx: context.Background(), ch: &failFirstChannel{}, log: slog.New(slog.NewTextHandler(&buf, nil))}
	s.Notice("⚠️ notice")
	if !strings.Contains(buf.String(), "gateway: cannot send the message") {
		t.Fatal(buf.String())
	}
}
