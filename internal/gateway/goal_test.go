package gateway

import (
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// doneVerdict is a judge reply that ends a goal.
const doneVerdict = `{"verdict": "done", "reason": "ok"}`

// fixedJudge is a provider that answers each call with reply.
type fixedJudge struct{ reply string }

func (j fixedJudge) Name() string  { return "judge" }
func (j fixedJudge) Model() string { return "judge-model" }

func (j fixedJudge) Stream(context.Context, provider.Request) (<-chan provider.Event, error) {
	ch := make(chan provider.Event, 2)
	ch <- provider.Event{Type: provider.EventText, Delta: j.reply}
	ch <- provider.Event{Type: provider.EventDone, FinishReason: "stop", Usage: &provider.Usage{}}
	close(ch)
	return ch, nil
}

// newGoalManager returns a goal manager over a new goals table. judge answers each judge
// call and each draft call.
func newGoalManager(t *testing.T, st *sessions.Store, judge provider.Provider) *goals.Manager {
	t.Helper()
	return goals.NewManager(goals.Deps{
		Store:          &goals.Store{DB: openTestDB(t)},
		Sessions:       st,
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
}

// goalDeps returns testDeps over fp with a goal manager.
func goalDeps(t *testing.T, fp *fakeProv, judge provider.Provider) *Deps {
	t.Helper()
	d := testDeps(t, fp)
	d.Goals = newGoalManager(t, d.Store, judge)
	return d
}

// setGoal makes text the active goal of the session and returns the state.
func setGoal(t *testing.T, d *Deps, sessionID, text string) goals.State {
	t.Helper()
	st, err := d.Goals.Update(context.Background(), sessionID, func(st *goals.State, _ bool) error {
		*st = goals.NewState(text, goals.Contract{}, 20, time.Now())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// loadGoal returns the goal state of the session, or fails the test when it has none.
func loadGoal(t *testing.T, d *Deps, sessionID string) goals.State {
	t.Helper()
	st, ok, err := d.Goals.Load(context.Background(), sessionID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	return st
}

// goalRec records the replies and the goal hooks of a command context.
type goalRec struct {
	sent   []Reply
	starts []goals.Continuation
	drops  int
}

// goalCtx returns a command context for "telegram:dm:1" with a goal manager and a session.
// With interactive, Send records each Reply. Else Reply records the text only, like the CLI.
func goalCtx(t *testing.T, interactive bool) (*CommandCtx, *goalRec) {
	t.Helper()
	d := goalDeps(t, &fakeProv{}, fixedJudge{reply: doneVerdict})
	rec := &goalRec{}
	c := &CommandCtx{Deps: d, Key: "telegram:dm:1", Hooks: CommandHooks{
		StartGoalTurn: func(gc goals.Continuation) { rec.starts = append(rec.starts, gc) },
		DropGoalTurns: func() { rec.drops++ },
	}}
	if interactive {
		c.Send = func(r Reply) { rec.sent = append(rec.sent, r) }
	} else {
		c.Reply = func(s string) { rec.sent = append(rec.sent, Reply{Text: s}) }
	}
	if _, err := c.current(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c, rec
}

// lastText returns the text of the last recorded reply.
func (r *goalRec) lastText(t *testing.T) string {
	t.Helper()
	if len(r.sent) == 0 {
		t.Fatal("no reply")
	}
	return r.sent[len(r.sent)-1].Text
}

func TestGoalCommandRegistered(t *testing.T) {
	cmd, ok := Commands()["goal"]
	if !ok || !cmd.WhileBusy ||
		cmd.Help != "Work toward a goal over several turns: /goal <text> | status | pause | resume | clear" {
		t.Fatalf("%+v", cmd)
	}
}

func TestGoalTextAndReasonShowAsTyped(t *testing.T) {
	c, rec := goalCtx(t, true)
	goal := "fix *all* the_bug in `main` [now]"
	run(t, c, "/goal "+goal)
	text := rec.lastText(t)
	if !strings.Contains(text, EscapeMarkdown(goal)) ||
		!strings.HasPrefix(StripMarkdown(text), "⊙ Goal set (20-turn budget): "+goal+"\n") {
		t.Fatalf("%q", text)
	}
	if len(rec.starts) != 1 || rec.starts[0].Text != goal || rec.starts[0].SessionID != c.Session.ID {
		t.Fatalf("%+v", rec.starts)
	}
	reason := "the [judge] said *no* to `go test`"
	if _, err := c.Deps.Goals.Update(context.Background(), c.Session.ID, func(st *goals.State, _ bool) error {
		st.SetStatus(goals.StatusPaused, reason)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	run(t, c, "/goal status")
	text = rec.lastText(t)
	want := "⏸ Goal (paused, 0/20 turns — " + reason + "): " + goal
	if !strings.Contains(text, EscapeMarkdown(reason)) || StripMarkdown(text) != want {
		t.Fatalf("got %q, want %q", StripMarkdown(text), want)
	}
}

func TestGoalStatusButtonsAndCLILines(t *testing.T) {
	c, rec := goalCtx(t, true)
	run(t, c, "/goal status")
	if rec.lastText(t) != goals.NoGoalText {
		t.Fatalf("%q", rec.lastText(t))
	}
	setGoal(t, c.Deps, c.Session.ID, "ship it")
	run(t, c, "/goal")
	rep := rec.sent[len(rec.sent)-1]
	pauseClear := [][]Button{{{Text: "Pause", Data: "/goal pause"}, {Text: "Clear", Data: "/goal clear"}}}
	if rep.Text != "⊙ Goal (active, 0/20 turns): ship it" || !reflect.DeepEqual(rep.Buttons, pauseClear) {
		t.Fatalf("%+v", rep)
	}
	run(t, c, "/goal pause")
	run(t, c, "/goal status")
	if rep := rec.sent[len(rec.sent)-1]; !reflect.DeepEqual(rep.Buttons, StopButtons()) {
		t.Fatalf("%+v", rep)
	}

	cli, crec := goalCtx(t, false)
	setGoal(t, cli.Deps, cli.Session.ID, "ship it")
	run(t, cli, "/goal status")
	if got := crec.lastText(t); got != "⊙ Goal (active, 0/20 turns): ship it\nSend /goal pause or /goal clear." {
		t.Fatalf("%q", got)
	}
	run(t, cli, "/goal pause")
	run(t, cli, "/goal status")
	if got := crec.lastText(t); !strings.HasSuffix(got, "\nSend /goal resume or /goal clear.") {
		t.Fatalf("%q", got)
	}
}

func TestGoalPauseResumeClear(t *testing.T) {
	ctx := context.Background()
	c, rec := goalCtx(t, false)
	for text, want := range map[string]string{
		"/goal pause": "No goal set.", "/goal resume": "No goal to resume.", "/goal clear": "No active goal.",
	} {
		run(t, c, text)
		if got := rec.lastText(t); got != want {
			t.Fatalf("%s: got %q, want %q", text, got, want)
		}
	}
	// /goal clear drops the goal turns also with no goal, like Hermes.
	rec.drops = 0
	setGoal(t, c.Deps, c.Session.ID, "ship it")
	if _, err := c.Deps.Goals.Update(ctx, c.Session.ID, func(st *goals.State, _ bool) error {
		st.TurnsUsed = 7
		st.WaitOnPID(4242, "build", time.Now())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	run(t, c, "/goal pause")
	st := loadGoal(t, c.Deps, c.Session.ID)
	if rec.lastText(t) != "⏸ Goal paused: ship it" || st.Status != goals.StatusPaused ||
		st.PausedReason == nil || *st.PausedReason != "paused by the owner" || st.WaitingOnPID != nil || rec.drops != 1 {
		t.Fatalf("%q %+v %d", rec.lastText(t), st, rec.drops)
	}
	run(t, c, "/goal resume")
	st = loadGoal(t, c.Deps, c.Session.ID)
	if rec.lastText(t) != "⊙ Goal (active, 0/20 turns): ship it\nContinuing now — I'll take the next step right away." ||
		st.Status != goals.StatusActive || st.TurnsUsed != 0 {
		t.Fatalf("%q %+v", rec.lastText(t), st)
	}
	if len(rec.starts) != 1 || rec.starts[0].Text != goals.ContinuationPrompt(st, nil) || rec.starts[0].Epoch != st.Epoch {
		t.Fatalf("%+v", rec.starts)
	}
	run(t, c, "/goal clear")
	if st := loadGoal(t, c.Deps, c.Session.ID); rec.lastText(t) != "✓ Goal cleared." || st.Status != goals.StatusCleared || rec.drops != 2 {
		t.Fatalf("%q %+v", rec.lastText(t), st)
	}
}

func TestGoalSetReplacesTheOldGoal(t *testing.T) {
	c, rec := goalCtx(t, false)
	run(t, c, "/goal one")
	if _, err := c.Deps.Goals.Update(context.Background(), c.Session.ID, func(st *goals.State, _ bool) error {
		st.Subgoals = []string{"x"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	run(t, c, "/goal ship it\nverify: go test ./...")
	st := loadGoal(t, c.Deps, c.Session.ID)
	text := rec.lastText(t)
	if !strings.HasPrefix(text, "⊙ Goal set (20-turn budget): ship it\n(replaced the previous goal)\n  was: one\n") ||
		!strings.Contains(text, "\nCompletion contract:\n"+EscapeMarkdown(st.Contract.Render(nil))+"\n") {
		t.Fatalf("%q", text)
	}
	if st.Goal != "ship it" || st.Epoch != 2 || len(st.Subgoals) != 0 || st.Contract.Verification != "go test ./..." {
		t.Fatalf("%+v", st)
	}
	if len(rec.starts) != 2 || rec.starts[1].Epoch != 2 || rec.drops != 2 {
		t.Fatalf("%+v %d", rec.starts, rec.drops)
	}
}

func TestGoalControlVerbIgnoresMoreWords(t *testing.T) {
	c, rec := goalCtx(t, false)
	setGoal(t, c.Deps, c.Session.ID, "ship it")
	run(t, c, "/goal status now please")
	want := "⊙ Goal (active, 0/20 turns): ship it\nSend /goal pause or /goal clear.\n" +
		EscapeMarkdown(goals.IgnoredNote("now please"))
	if got := rec.lastText(t); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestGoalTextRefusedWhileBusy(t *testing.T) {
	ctx := context.Background()
	g := &Gateway{Deps: goalDeps(t, &fakeProv{delay: 2 * time.Second}, fixedJudge{reply: doneVerdict})}
	sess, err := g.Deps.Store.ActiveOrCreate(ctx, "k", sessions.Session{Source: "telegram", ChatID: "1", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	setGoal(t, g.Deps, sess.ID, "ship it")
	done := make(chan struct{})
	go func() { _ = g.Handle(ctx, inb("k", "first"), (&recFactory{}).new); close(done) }()
	waitFor(t, func() bool { _, ok := g.Running("k"); return ok })
	for _, text := range []string{"/goal a new goal", "/goal draft a new goal"} {
		r := &recRenderer{}
		_ = g.Handle(ctx, inb("k", text), func() Renderer { return r })
		want := "Agent is running — use /goal status / pause / clear / wait mid-run, or /stop before setting a new goal."
		if len(r.acks) != 1 || r.acks[0] != want {
			t.Fatalf("%s: %q", text, r.acks)
		}
	}
	r := &recRenderer{}
	_ = g.Handle(ctx, inb("k", "/goal status"), func() Renderer { return r })
	if len(r.acks) != 1 || !strings.HasPrefix(r.acks[0], "⊙ Goal (active") {
		t.Fatalf("%q", r.acks)
	}
	if st := loadGoal(t, g.Deps, sess.ID); st.Goal != "ship it" {
		t.Fatalf("%+v", st)
	}
	_ = g.Handle(ctx, inb("k", "/stop"), func() Renderer { return &recRenderer{} })
	<-done
}

func TestGoalKickTurnRunsWithTheCommandRenderer(t *testing.T) {
	ctx := context.Background()
	fp := &fakeProv{}
	g := &Gateway{Deps: goalDeps(t, fp, fixedJudge{reply: doneVerdict})}
	f := &recFactory{}
	if err := g.Handle(ctx, inb("k", "/goal fix the flaky cron test"), f.new); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(f.finals()) == 1 })
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	if got := fp.lastUserTexts(); len(got) == 0 || got[0] != "fix the flaky cron test" {
		t.Fatalf("%q", got)
	}
	if acks := f.acks(); len(acks) == 0 || !strings.HasPrefix(acks[0], "⊙ Goal set (20-turn budget): fix the flaky cron test") {
		t.Fatalf("%q", acks)
	}
}
