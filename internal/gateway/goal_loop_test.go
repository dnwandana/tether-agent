package gateway

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// judgeProv answers the judge calls in order from replies, and then answers done. The
// first call waits for hold when hold is not nil. A cancel of that wait sets canceled.
type judgeProv struct {
	mu       sync.Mutex
	replies  []string
	hold     chan struct{}
	calls    int
	canceled bool
}

func (j *judgeProv) Name() string  { return "judge" }
func (j *judgeProv) Model() string { return "judge-model" }

func (j *judgeProv) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Event, error) {
	j.mu.Lock()
	j.calls++
	first := j.calls == 1
	reply := `{"verdict": "done", "reason": "ok"}`
	if len(j.replies) > 0 {
		reply, j.replies = j.replies[0], j.replies[1:]
	}
	j.mu.Unlock()
	ch := make(chan provider.Event, 2)
	go func() {
		defer close(ch)
		if first && j.hold != nil {
			select {
			case <-j.hold:
			case <-ctx.Done():
				j.mu.Lock()
				j.canceled = true
				j.mu.Unlock()
				ch <- provider.Event{Type: provider.EventError, Err: ctx.Err()}
				return
			}
		}
		ch <- provider.Event{Type: provider.EventText, Delta: reply}
		ch <- provider.Event{Type: provider.EventDone, FinishReason: "stop", Usage: &provider.Usage{}}
	}()
	return ch, nil
}

func (j *judgeProv) count() int        { j.mu.Lock(); defer j.mu.Unlock(); return j.calls }
func (j *judgeProv) wasCanceled() bool { j.mu.Lock(); defer j.mu.Unlock(); return j.canceled }

// stepGateway returns a gateway with an active goal on key "k". judge answers the judge
// calls. The debounce window is short.
func stepGateway(t *testing.T, judge *judgeProv) (*Gateway, *fakeProv, *recFactory, *sessions.Session) {
	t.Helper()
	ctx := context.Background()
	g, fp := gatewayWithSlowProvider(t, 0)
	g.debounce = NewDebouncer(time.Millisecond, 10*time.Millisecond)
	g.Deps.Goals = goals.NewManager(goals.Deps{
		Store:          &goals.Store{DB: openTestDB(t)},
		Sessions:       g.Deps.Store,
		Cfg:            func() config.GoalsConfig { return config.Default().Goals },
		ProviderFor:    func(*sessions.Session) provider.Provider { return judge },
		ChainFor:       func(string) provider.Provider { return judge },
		Meta:           func(provider.Provider, provider.Result) sessions.MessageMeta { return sessions.MessageMeta{} },
		RunningBatches: func(string) int { return 0 },
		Procs:          tools.NewProcRegistry(),
		Exec: func(context.Context, *sessions.Session, string, time.Duration) (string, int, error) {
			return "", 0, nil
		},
		TodoOpen: func(context.Context, string) string { return "" },
		Alive:    func(int) bool { return true },
		Now:      time.Now,
		Log:      slog.New(slog.DiscardHandler),
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

func TestGoalStepOwnerTextBeforeContinuation(t *testing.T) {
	ctx := context.Background()
	j := &judgeProv{hold: make(chan struct{}), replies: []string{
		`{"verdict": "continue", "reason": "the fix is not verified"}`,
		`{"verdict": "continue", "reason": "run the tests 20 times"}`,
		`{"verdict": "done", "reason": "20 runs pass"}`,
	}}
	g, fp, f, _ := stepGateway(t, j)
	go func() { _ = g.Handle(ctx, inb("k", "first"), f.new) }()
	waitFor(t, func() bool { return j.count() == 1 })
	// The judge of the first turn runs now. The owner text goes into the steer queue of the run.
	_ = g.Handle(ctx, inb("k", "owner note"), f.new)
	waitFor(t, func() bool { rs, ok := g.Running("k"); return ok && rs.Steer.Pending() == "owner note" })
	close(j.hold)
	waitFor(t, func() bool { return len(fp.lastUserTexts()) == 3 })
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	got := fp.lastUserTexts()
	if len(got) != 3 || got[0] != "first" || got[1] != "owner note" || !strings.HasPrefix(got[2], goals.ContinuationPrefix) {
		t.Fatalf("the owner text must run before the continuation: %q", got)
	}
}

func TestGoalStepMarksTheMarkerInStep(t *testing.T) {
	ctx := context.Background()
	j := &judgeProv{hold: make(chan struct{})}
	g, _, f, sess := stepGateway(t, j)
	go func() { _ = g.Handle(ctx, inb("k", "first"), f.new) }()
	waitFor(t, func() bool { return j.count() == 1 })
	if m := markerOf(t, g.Deps.Store, "k"); m == nil || !m.InStep || m.SessionID != sess.ID {
		t.Fatalf("the marker must be in the step while the judge runs: %+v", m)
	}
	close(j.hold)
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	if j.wasCanceled() {
		t.Fatal("a step with no stop must not cancel the judge")
	}
	flushMarkers(t, g)
	noMarkers(t, g.Deps.Store)
}

func TestGoalStepNoticeGoesThroughTheTurnRenderer(t *testing.T) {
	j := &judgeProv{replies: []string{`{"verdict": "done", "reason": "20 runs pass"}`}}
	g, _, _, _ := stepGateway(t, j)
	r := &recRenderer{}
	if err := g.Handle(context.Background(), inb("k", "first"), func() Renderer { return r }); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.done || r.final != "ok" || len(r.acks) != 1 || r.acks[0] != "✓ Goal achieved: 20 runs pass" {
		t.Fatalf("done=%v final=%q acks=%q", r.done, r.final, r.acks)
	}
}

func TestGoalStepSkipsAKeyWithNoGoal(t *testing.T) {
	j := &judgeProv{}
	g, fp, f, sess := stepGateway(t, j)
	if _, err := g.Deps.Goals.Update(context.Background(), sess.ID, func(st *goals.State, _ bool) error {
		st.SetStatus(goals.StatusPaused, "paused by the owner")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = g.Handle(context.Background(), inb("k", "first"), f.new)
	if j.count() != 0 || len(fp.lastUserTexts()) != 1 {
		t.Fatalf("a paused goal runs no step: judge calls %d, turns %q", j.count(), fp.lastUserTexts())
	}
	flushMarkers(t, g)
	noMarkers(t, g.Deps.Store)
}
