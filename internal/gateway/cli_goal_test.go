package gateway

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// cliGoalDeps returns the deps of a CLI with a goal manager over a new state.db. The judge
// answers each judge call.
func cliGoalDeps(t *testing.T, judge provider.Provider) *Deps {
	t.Helper()
	database := openTestDB(t)
	st := sessions.New(database)
	cfg := config.Default()
	log := slog.New(slog.DiscardHandler)
	m := goals.NewManager(goals.Deps{
		Store:       &goals.Store{DB: database},
		Sessions:    st,
		Cfg:         func() config.GoalsConfig { return cfg.Goals },
		ProviderFor: func(*sessions.Session) provider.Provider { return judge },
		ChainFor:    func(string) provider.Provider { return judge },
		Meta:        func(provider.Provider, provider.Result) sessions.MessageMeta { return sessions.MessageMeta{} },
		Procs:       tools.NewProcRegistry(),
		TodoOpen:    func(context.Context, string) string { return "" },
		Alive:       goals.PIDAlive,
		Now:         time.Now,
		Log:         log,
	})
	return &Deps{Cfg: &cfg, Store: st, Goals: m, Log: log}
}

// workTurn is a CLI turn that answers "worked".
func workTurn(got *[]string) func(context.Context, *sessions.Session, string, io.Writer) (agent.TurnResult, error) {
	return func(_ context.Context, _ *sessions.Session, text string, w io.Writer) (agent.TurnResult, error) {
		*got = append(*got, text)
		fmt.Fprintln(w, "worked")
		return agent.TurnResult{Final: "worked"}, nil
	}
}

// goalOf returns the goal state of the active session of key.
func goalOf(t *testing.T, d *Deps, key string) goals.State {
	t.Helper()
	ctx := context.Background()
	sess, err := d.Store.Active(ctx, key)
	if err != nil || sess == nil {
		t.Fatal(sess, err)
	}
	st, ok, err := d.Goals.Load(ctx, sess.ID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	return st
}

// setCLIGoal makes an active goal for the session of key.
func setCLIGoal(t *testing.T, d *Deps, key, text string, edit func(st *goals.State)) {
	t.Helper()
	ctx := context.Background()
	sess, err := d.Store.ActiveOrCreate(ctx, key, sessions.Session{Source: "cli", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Goals.Update(ctx, sess.ID, func(st *goals.State, _ bool) error {
		*st = goals.NewState(text, goals.Contract{}, 20, time.Now())
		if edit != nil {
			edit(st)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// blockJudge is a judge that waits for the end of the call context.
type blockJudge struct{ started chan struct{} }

func (b *blockJudge) Name() string  { return "primary" }
func (b *blockJudge) Model() string { return "judge" }
func (b *blockJudge) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Event, error) {
	close(b.started)
	ch := make(chan provider.Event, 1)
	go func() {
		defer close(ch)
		<-ctx.Done()
		ch <- provider.Event{Type: provider.EventError, Err: ctx.Err()}
	}()
	return ch, nil
}

const interruptedLine = "⏸ Goal paused — turn was interrupted. Use /goal resume to continue, or /goal clear to stop."

// /goal <text> runs the kick turn and each continuation with no read of input, until the
// judge says done.
func TestCLIGoalLoopRunsContinuations(t *testing.T) {
	judge := &fakeProv{script: []step{
		{text: `{"verdict": "continue", "reason": "not yet"}`},
		{text: `{"verdict": "done", "reason": "all pass"}`},
	}}
	d := cliGoalDeps(t, judge)
	var got []string
	var out bytes.Buffer
	c := &CLI{In: strings.NewReader("/goal fix the cron tests\n"), Out: &out, Key: "cli:test", Deps: d,
		Interrupts: make(chan os.Signal), Turn: workTurn(&got)}
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "fix the cron tests" || !strings.HasPrefix(got[1], goals.ContinuationPrefix) {
		t.Fatalf("%q", got)
	}
	o := out.String()
	set := strings.Index(o, "⊙ Goal set (20-turn budget): fix the cron tests")
	line := strings.Index(o, ansiDim+"↻ Continuing toward goal (1/20): not yet"+ansiReset+"\n")
	done := strings.Index(o, "✓ Goal achieved: all pass\n")
	if set < 0 || line < set || done < line {
		t.Fatalf("%q", o)
	}
	if st := goalOf(t, d, "cli:test"); st.Status != goals.StatusDone || st.TurnsUsed != 2 {
		t.Fatalf("%+v", st)
	}
	if strings.Contains(o, GoalPausedLine) {
		t.Fatal("a done goal does not pause at the exit")
	}
}

// Ctrl-C during a goal turn stops the turn and pauses the goal. No judge call runs.
func TestCLICtrlCDuringGoalTurnPausesGoal(t *testing.T) {
	judge := &fakeProv{}
	d := cliGoalDeps(t, judge)
	ints := make(chan os.Signal, 1)
	started := make(chan struct{})
	var out bytes.Buffer
	c := &CLI{In: strings.NewReader("/goal fix the cron tests\n"), Out: &out, Key: "cli:test", Deps: d, Interrupts: ints,
		Turn: func(ctx context.Context, _ *sessions.Session, _ string, _ io.Writer) (agent.TurnResult, error) {
			close(started)
			<-ctx.Done()
			return agent.TurnResult{}, ctx.Err()
		}}
	go func() { <-started; ints <- os.Interrupt }()
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	o := out.String()
	if !strings.Contains(o, StoppedReply+"\n"+interruptedLine+"\n") || strings.Contains(o, GoalPausedLine) {
		t.Fatalf("%q", o)
	}
	st := goalOf(t, d, "cli:test")
	if st.Status != goals.StatusPaused || st.PausedReason == nil || *st.PausedReason != "interrupted by Ctrl-C" {
		t.Fatalf("%+v", st)
	}
	if len(judge.reqs) != 0 {
		t.Fatal("a stopped turn gets no judge call")
	}
}

// Ctrl-C during the step cancels the judge call and pauses the goal. No continuation runs.
func TestCLICtrlCDuringStepPausesGoal(t *testing.T) {
	judge := &blockJudge{started: make(chan struct{})}
	d := cliGoalDeps(t, judge)
	ints := make(chan os.Signal, 1)
	var got []string
	var out bytes.Buffer
	c := &CLI{In: strings.NewReader("/goal fix the cron tests\n"), Out: &out, Key: "cli:test", Deps: d,
		Interrupts: ints, Turn: workTurn(&got)}
	go func() { <-judge.started; ints <- os.Interrupt }()
	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Ctrl-C must end the judge call")
	}
	if len(got) != 1 {
		t.Fatalf("no continuation after Ctrl-C: %q", got)
	}
	if o := out.String(); !strings.Contains(o, StoppedReply+"\n"+interruptedLine+"\n") {
		t.Fatalf("%q", o)
	}
	if st := goalOf(t, d, "cli:test"); st.Status != goals.StatusPaused || *st.PausedReason != "interrupted by Ctrl-C" {
		t.Fatalf("%+v", st)
	}
}

// The start pauses an active goal of the CLI session and prints the status line.
func TestCLIStartPausesActiveGoal(t *testing.T) {
	d := cliGoalDeps(t, &fakeProv{})
	setCLIGoal(t, d, "cli:test", "fix the cron tests", nil)
	var got []string
	var out bytes.Buffer
	c := &CLI{In: strings.NewReader(""), Out: &out, Key: "cli:test", Deps: d,
		Interrupts: make(chan os.Signal), Turn: workTurn(&got)}
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if o := out.String(); !strings.Contains(o, "⏸ Goal (paused, 0/20 turns — the chat ended): fix the cron tests\n") {
		t.Fatalf("%q", o)
	}
	if st := goalOf(t, d, "cli:test"); st.Status != goals.StatusPaused || *st.PausedReason != ReasonChatEnded {
		t.Fatalf("%+v", st)
	}
	if len(got) != 0 {
		t.Fatal(got)
	}
}

// /exit pauses an active goal and prints the pause line. A parked goal uses no judge call.
func TestCLIExitPausesActiveGoal(t *testing.T) {
	judge := &fakeProv{}
	d := cliGoalDeps(t, judge)
	var out bytes.Buffer
	c := &CLI{In: strings.NewReader("hello\n/exit\n"), Out: &out, Key: "cli:test", Deps: d, Interrupts: make(chan os.Signal),
		Turn: func(_ context.Context, _ *sessions.Session, _ string, _ io.Writer) (agent.TurnResult, error) {
			// The goal starts after the start check, and it waits 600 s.
			setCLIGoal(t, d, "cli:test", "fix the cron tests", func(st *goals.State) {
				st.WaitFor(600, 0, "build runs", time.Now())
			})
			return agent.TurnResult{Final: "ok"}, nil
		}}
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if o := out.String(); !strings.HasSuffix(o, GoalPausedLine+"\n") {
		t.Fatalf("%q", o)
	}
	if st := goalOf(t, d, "cli:test"); st.Status != goals.StatusPaused || *st.PausedReason != ReasonChatEnded {
		t.Fatalf("%+v", st)
	}
	if len(judge.reqs) != 0 {
		t.Fatal("a parked goal gets no judge call")
	}
}

// /stop at the prompt pauses a parked goal, and the reply is the pause line. The wake of
// the goal then runs no turn.
func TestCLIStopPausesParkedGoal(t *testing.T) {
	d := cliGoalDeps(t, &fakeProv{})
	var got []string
	var out bytes.Buffer
	c := &CLI{In: strings.NewReader("hello\n/stop\n"), Out: &out, Key: "cli:test", Deps: d, Interrupts: make(chan os.Signal),
		Turn: func(_ context.Context, _ *sessions.Session, text string, _ io.Writer) (agent.TurnResult, error) {
			got = append(got, text)
			// The goal starts after the start check, and it waits 600 s.
			setCLIGoal(t, d, "cli:test", "fix the cron tests", func(st *goals.State) {
				st.WaitFor(600, 0, "build runs", time.Now())
			})
			return agent.TurnResult{Final: "ok"}, nil
		}}
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	o := out.String()
	if strings.Count(o, GoalPausedLine) != 1 || strings.Contains(o, "No task to stop") {
		t.Fatalf("%q", o)
	}
	st := goalOf(t, d, "cli:test")
	if st.Status != goals.StatusPaused || *st.PausedReason != ReasonStopped || st.WaitingUntil != 0 {
		t.Fatalf("%+v", st)
	}
	if len(got) != 1 {
		t.Fatalf("%q", got)
	}
}

// /stop keep at the prompt also pauses a parked goal.
func TestCLIStopKeepPausesParkedGoal(t *testing.T) {
	d := cliGoalDeps(t, &fakeProv{})
	var out bytes.Buffer
	c := &CLI{In: strings.NewReader("hello\n/stop keep\n"), Out: &out, Key: "cli:test", Deps: d, Interrupts: make(chan os.Signal),
		Turn: func(_ context.Context, _ *sessions.Session, _ string, _ io.Writer) (agent.TurnResult, error) {
			setCLIGoal(t, d, "cli:test", "fix the cron tests", func(st *goals.State) {
				st.WaitFor(600, 0, "build runs", time.Now())
			})
			return agent.TurnResult{Final: "ok"}, nil
		}}
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if o := out.String(); strings.Count(o, GoalPausedLine) != 1 || strings.Contains(o, "No task to stop") {
		t.Fatalf("%q", o)
	}
	if st := goalOf(t, d, "cli:test"); st.Status != goals.StatusPaused || *st.PausedReason != ReasonStopped {
		t.Fatalf("%+v", st)
	}
}

// /stop with no goal replies that no task runs.
func TestCLIStopWithoutGoal(t *testing.T) {
	d := cliGoalDeps(t, &fakeProv{})
	var got []string
	var out bytes.Buffer
	c := &CLI{In: strings.NewReader("/stop\n/stop keep\n"), Out: &out, Key: "cli:test", Deps: d,
		Interrupts: make(chan os.Signal), Turn: workTurn(&got)}
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := StripMarkdown(NoTaskReply) + "\n" + StripMarkdown(NoTaskReply) + "\n"
	if o := out.String(); o != want {
		t.Fatalf("%q", o)
	}
}
