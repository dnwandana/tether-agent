package goals

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// stepNow is the clock of the step tests.
var stepNow = time.Unix(1_800_000_000, 0)

// stepProv is a judge provider. Call n gets replies[n], and the last reply repeats. The
// reply "ERR" fails the call before the first byte. onCall runs before the reply.
type stepProv struct {
	mu      sync.Mutex
	replies []string
	reqs    []provider.Request
	onCall  func()
}

func (p *stepProv) Name() string  { return "judge" }
func (p *stepProv) Model() string { return "judge-model" }

func (p *stepProv) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	p.mu.Lock()
	n := len(p.reqs)
	p.reqs = append(p.reqs, req)
	reply := p.replies[min(n, len(p.replies)-1)]
	hook := p.onCall
	p.mu.Unlock()
	if hook != nil {
		hook()
	}
	if reply == "ERR" {
		return nil, fmt.Errorf("HTTP 503: %w", provider.ErrBeforeFirstByte)
	}
	ch := make(chan provider.Event, 2)
	ch <- provider.Event{Type: provider.EventText, Delta: reply}
	ch <- provider.Event{Type: provider.EventDone, FinishReason: "stop"}
	close(ch)
	return ch, nil
}

func (p *stepProv) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reqs)
}

// stepFix is a manager over a new state.db, with a fake judge, fake gates, fake pids, and
// a chat session that has the goal of newStepFix.
type stepFix struct {
	m       *Manager
	prov    *stepProv
	sess    *sessions.Session
	procs   *tools.ProcRegistry
	exits   map[string]int // the exit code of each gate command
	alive   map[int]bool
	batches int
	todo    string
}

// stepState returns an active goal with the budget maxTurns.
func stepState(goal string, maxTurns int) State {
	return State{Goal: goal, Status: StatusActive, MaxTurns: maxTurns, Epoch: 1}
}

func newStepFix(t *testing.T, st State, replies ...string) *stepFix {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	store := sessions.New(d)
	sess, err := store.Create(context.Background(), sessions.Session{Key: "telegram:dm:1", Source: "telegram"})
	if err != nil {
		t.Fatal(err)
	}
	f := &stepFix{prov: &stepProv{replies: replies}, sess: sess, procs: tools.NewProcRegistry(),
		exits: map[string]int{}, alive: map[int]bool{}}
	f.m = NewManager(Deps{
		Store:       &Store{DB: d},
		Sessions:    store,
		Cfg:         func() config.GoalsConfig { return config.GoalsConfig{MaxTurns: 20, JudgeTimeoutS: 30} },
		ProviderFor: func(*sessions.Session) provider.Provider { return f.prov },
		Meta: func(p provider.Provider, _ provider.Result) sessions.MessageMeta {
			return sessions.MessageMeta{Provider: p.Name(), Model: p.Model()}
		},
		RunningBatches: func(string) int { return f.batches },
		Procs:          f.procs,
		Exec: func(_ context.Context, _ *sessions.Session, cmd string, _ time.Duration) (string, int, error) {
			return "output of " + cmd, f.exits[cmd], nil
		},
		TodoOpen: func(context.Context, string) string { return f.todo },
		Alive:    func(pid int) bool { return f.alive[pid] },
		Now:      func() time.Time { return stepNow },
	})
	if _, err := f.m.Update(context.Background(), sess.ID, func(s *State, _ bool) error { *s = st; return nil }); err != nil {
		t.Fatal(err)
	}
	return f
}

// step runs the after-turn step for a turn that answered final.
func (f *stepFix) step(final string) StepResult {
	return f.m.AfterTurn(context.Background(), TurnOutcome{Key: f.sess.Key, Session: f.sess, Final: final})
}

// state returns the stored goal.
func (f *stepFix) state(t *testing.T) State {
	t.Helper()
	st, ok, err := f.m.Load(context.Background(), f.sess.ID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	return st
}

func stepStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func TestStepNoActiveGoalDoesNotRun(t *testing.T) {
	f := newStepFix(t, stepState("ship it", 20), `{"verdict":"done","reason":"x"}`)
	cron := *f.sess
	cron.Kind = "cron"
	if res := f.m.AfterTurn(context.Background(), TurnOutcome{Key: f.sess.Key, Session: &cron, Final: "x"}); res.Ran {
		t.Fatal("only a chat session runs the step")
	}
	none := &sessions.Session{ID: "no-such-session", Key: "telegram:dm:9", Kind: "chat"}
	if res := f.m.AfterTurn(context.Background(), TurnOutcome{Key: none.Key, Session: none, Final: "x"}); res.Ran {
		t.Fatal("a session with no goal does not run the step")
	}
	paused := stepState("ship it", 20)
	paused.Status = StatusPaused
	f = newStepFix(t, paused, `{"verdict":"done","reason":"x"}`)
	if res := f.step("done"); res.Ran || f.prov.calls() != 0 {
		t.Fatalf("a paused goal does not run the step: %+v", res)
	}
}

func TestStepParkedUsesNoTurnAndNoJudge(t *testing.T) {
	st := stepState("ship it", 5)
	st.WaitOnPID(4242, "CI green", stepNow)
	f := newStepFix(t, st, `{"verdict":"continue","reason":"x"}`)
	f.alive[4242] = true
	res := f.step("still waiting on CI")
	want := Notice{Kind: NoticeStillParked, Target: "pid 4242", Reason: "CI green"}
	if !res.Ran || !res.Parked || res.Continuation != nil || len(res.Notices) != 1 || res.Notices[0] != want {
		t.Fatalf("%+v", res)
	}
	if f.prov.calls() != 0 {
		t.Fatal("the judge must not run on a parked goal")
	}
	if got := f.state(t); got.TurnsUsed != 0 || got.Status != StatusActive || got.WaitingOnPID == nil {
		t.Fatalf("a parked goal uses no turn: %+v", got)
	}
}

func TestStepLiftedBarrierGoesOnToTheJudge(t *testing.T) {
	st := stepState("ship it", 5)
	st.WaitOnPID(4242, "CI green", stepNow)
	f := newStepFix(t, st, `{"verdict":"continue","reason":"CI passed, now merge"}`)
	res := f.step("CI is green") // pid 4242 is not alive
	if res.Parked || res.Continuation == nil || f.prov.calls() != 1 {
		t.Fatalf("%+v", res)
	}
	if got := f.state(t); got.WaitingOnPID != nil || got.TurnsUsed != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestStepFailingGateSkipsTheJudge(t *testing.T) {
	st := stepState("ship it", 20)
	st.AddGate("exit 5")
	f := newStepFix(t, st, `{"verdict":"done","reason":"x"}`)
	f.exits["exit 5"] = 5
	res := f.step("I think it is done!")
	if f.prov.calls() != 0 {
		t.Fatal("a failing gate skips the judge")
	}
	c := res.Continuation
	if c == nil || c.LineKind != LineContinue || c.Used != 1 || c.Max != 20 || c.Reason != "gate failed (exit 5): $ exit 5" ||
		!strings.Contains(c.Text, "$ exit 5") || !strings.Contains(c.Text, "Exit code: 5") {
		t.Fatalf("%+v", c)
	}
	want := Notice{Kind: NoticeGateFailed, Used: 1, Max: 20, Command: "exit 5", Attempt: 1, MaxRetries: 3, ExitCode: 5}
	if len(res.Notices) != 1 || res.Notices[0] != want {
		t.Fatalf("%+v", res.Notices)
	}
	got := f.state(t)
	if got.TurnsUsed != 1 || got.Status != StatusActive || got.Gates[0].Attempts != 1 || got.Gates[0].LastExitCode == nil || *got.Gates[0].LastExitCode != 5 {
		t.Fatalf("%+v", got)
	}
}

func TestStepPassingGatesGoOnToTheJudge(t *testing.T) {
	st := stepState("ship it", 20)
	st.AddGate("true")
	f := newStepFix(t, st, `{"verdict":"done","reason":"all good"}`)
	res := f.step("finished")
	if f.prov.calls() != 1 || len(res.Notices) != 1 || res.Notices[0].Kind != NoticeDone {
		t.Fatalf("%+v", res)
	}
	if got := f.state(t); got.Gates[0].Attempts != 0 || got.Gates[0].LastExitCode == nil || *got.Gates[0].LastExitCode != 0 {
		t.Fatalf("%+v", got.Gates[0])
	}
}

func TestStepGateFailureOnTheLastTurnPauses(t *testing.T) {
	st := stepState("budget goal", 1)
	st.AddGate("exit 1")
	f := newStepFix(t, st, `{"verdict":"done","reason":"x"}`)
	f.exits["exit 1"] = 1
	res := f.step("only turn")
	want := Notice{Kind: NoticeBudgetGate, Used: 1, Max: 1, StopButtons: true}
	if res.Continuation != nil || len(res.Notices) != 1 || res.Notices[0] != want {
		t.Fatalf("%+v", res)
	}
	if got := f.state(t); got.Status != StatusPaused || stepStr(got.PausedReason) != "turn budget exhausted (1/1)" {
		t.Fatalf("%+v", got)
	}
}

func TestStepFourthGateFailurePauses(t *testing.T) {
	st := stepState("ship it", 20)
	st.AddGate("exit 1")
	f := newStepFix(t, st, `{"verdict":"done","reason":"x"}`)
	f.exits["exit 1"] = 1
	for i := 1; i <= 3; i++ {
		res := f.step(fmt.Sprintf("try %d", i))
		if res.Continuation == nil || len(res.Notices) != 1 || res.Notices[0].Attempt != i {
			t.Fatalf("failure %d: %+v", i, res)
		}
	}
	res := f.step("try 4")
	want := Notice{Kind: NoticeGateExhausted, Command: "exit 1", MaxRetries: 3, ExitCode: 1, StopButtons: true}
	if res.Continuation != nil || len(res.Notices) != 1 || res.Notices[0] != want {
		t.Fatalf("%+v", res)
	}
	got := f.state(t)
	if got.Status != StatusPaused || stepStr(got.PausedReason) != "quality gate exhausted 3 retries: $ exit 1" || got.TurnsUsed != 4 {
		t.Fatalf("%+v", got)
	}
	if f.prov.calls() != 0 {
		t.Fatal("the judge never ran")
	}
}

func TestStepAppliesEachVerdict(t *testing.T) {
	cases := []struct {
		name, reply string
		alive       bool
		status      string
		paused      string
		notice      *Notice
		cont        string // the reason of the continuation, or "" for none
		parked      bool
	}{
		{name: "continue", reply: `{"verdict":"continue","reason":"the fix is not verified"}`,
			status: StatusActive, paused: "<nil>", cont: "the fix is not verified"},
		{name: "done", reply: `{"verdict":"done","reason":"20 test runs pass"}`,
			status: StatusDone, paused: "<nil>", notice: &Notice{Kind: NoticeDone, Reason: "20 test runs pass"}},
		{name: "blocked", reply: `{"verdict":"blocked","reason":"needs the owner's key."}`,
			status: StatusPaused, paused: "judged unachievable: needs the owner's key.",
			notice: &Notice{Kind: NoticeBlocked, Reason: "needs the owner's key.", StopButtons: true}},
		{name: "wait on a live pid", reply: `{"verdict":"wait","reason":"CI runs","wait_on_pid":77}`, alive: true,
			status: StatusActive, paused: "<nil>", notice: &Notice{Kind: NoticeParkedJudge, Target: "pid 77", Reason: "CI runs"}, parked: true},
		{name: "wait on a dead pid", reply: `{"verdict":"wait","reason":"CI runs","wait_on_pid":77}`,
			status: StatusActive, paused: "<nil>", cont: "CI runs"},
		{name: "wait for seconds", reply: `{"verdict":"wait","reason":"cooldown","wait_for_seconds":120}`,
			status: StatusActive, paused: "<nil>", notice: &Notice{Kind: NoticeParkedJudge, Target: "120s", Reason: "cooldown"}, parked: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newStepFix(t, stepState("ship it", 20), c.reply)
			f.alive[77] = c.alive
			res := f.step("an answer")
			got := f.state(t)
			if got.Status != c.status || stepStr(got.PausedReason) != c.paused || got.TurnsUsed != 1 || res.Parked != c.parked {
				t.Fatalf("%+v %+v", got, res)
			}
			if c.notice == nil && len(res.Notices) != 0 || c.notice != nil && (len(res.Notices) != 1 || res.Notices[0] != *c.notice) {
				t.Fatalf("%+v", res.Notices)
			}
			if c.cont == "" {
				if res.Continuation != nil {
					t.Fatalf("%+v", res.Continuation)
				}
				return
			}
			cont := res.Continuation
			if cont == nil || cont.Reason != c.cont || cont.LineKind != LineContinue || cont.Used != 1 || cont.Max != 20 ||
				cont.Epoch != got.Epoch || cont.SessionID != f.sess.ID || !strings.HasPrefix(cont.Text, ContinuationPrefix) {
				t.Fatalf("%+v", cont)
			}
			if !f.m.IsCurrent(f.sess.ID, cont.Epoch) {
				t.Fatal("the continuation is current")
			}
		})
	}
	f := newStepFix(t, stepState("ship it", 20), `{"verdict":"wait","reason":"cooldown","wait_for_seconds":120}`)
	f.step("an answer")
	if got := f.state(t); got.WaitingUntil != 1_800_000_120 {
		t.Fatal(got.WaitingUntil)
	}
}

func TestStepDoneOnTheLastTurnIsDone(t *testing.T) {
	f := newStepFix(t, stepState("ship it", 1), `{"verdict":"done","reason":"all pass"}`)
	res := f.step("all pass")
	if len(res.Notices) != 1 || res.Notices[0] != (Notice{Kind: NoticeDone, Reason: "all pass"}) || f.state(t).Status != StatusDone {
		t.Fatalf("%+v", res)
	}
}

func TestStepBudgetPause(t *testing.T) {
	f := newStepFix(t, stepState("ship it", 1), `{"verdict":"continue","reason":"more to do"}`)
	res := f.step("one step")
	want := Notice{Kind: NoticeBudget, Used: 1, Max: 1, StopButtons: true}
	if res.Continuation != nil || len(res.Notices) != 1 || res.Notices[0] != want {
		t.Fatalf("%+v", res)
	}
	if got := f.state(t); got.Status != StatusPaused || stepStr(got.PausedReason) != "turn budget exhausted (1/1)" {
		t.Fatalf("%+v", got)
	}
}

func TestStepJudgeFailuresPause(t *testing.T) {
	f := newStepFix(t, stepState("ship it", 20), "not json at all")
	for i := 1; i <= 2; i++ {
		if res := f.step("x"); res.Continuation == nil {
			t.Fatalf("parse failure %d continues: %+v", i, res)
		}
	}
	res := f.step("x")
	if len(res.Notices) != 1 || res.Notices[0] != (Notice{Kind: NoticeParseFail, Count: 3, StopButtons: true}) {
		t.Fatalf("%+v", res)
	}
	if got := f.state(t); got.Status != StatusPaused || stepStr(got.PausedReason) != "the judge reply could not be parsed 3 times" {
		t.Fatalf("%+v", got)
	}

	f = newStepFix(t, stepState("ship it", 20), "ERR")
	for i := 1; i <= 4; i++ {
		if res := f.step("x"); res.Continuation == nil {
			t.Fatalf("API error %d continues: %+v", i, res)
		}
	}
	res = f.step("x")
	if len(res.Notices) != 1 || res.Notices[0] != (Notice{Kind: NoticeTransportFail, Count: 5, StopButtons: true}) {
		t.Fatalf("%+v", res)
	}
	if got := f.state(t); got.Status != StatusPaused || stepStr(got.PausedReason) != "the judge call failed 5 times" {
		t.Fatalf("%+v", got)
	}
}

func TestStepJudgeInput(t *testing.T) {
	f := newStepFix(t, stepState("ship it", 20), `{"verdict":"continue","reason":"x"}`)
	log := filepath.Join(t.TempDir(), "bg-4242.log")
	if err := os.WriteFile(log, []byte("ok  internal/cron 0.4s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.procs.Add(f.sess.ID, 4242, "go test ./internal/cron/ -count=20", log, stepNow.Add(-12*time.Second))
	f.procs.Add("other-session", 5151, "sleep 600", "", stepNow)
	f.batches = 2
	f.todo = "Open items of the agent's task list:\n- [ ] 3. Run the cron tests 20 times (pending)\n\n"
	f.step("I started the cron tests.")
	if f.prov.calls() != 1 {
		t.Fatal(f.prov.calls())
	}
	user := f.prov.reqs[0].Messages[0].Content
	for _, want := range []string{"I started the cron tests.", "pid 4242", "go test ./internal/cron/ -count=20", "running 12s",
		"ok  internal/cron 0.4s", "2 delegated subagent batch(es)", "- [ ] 3. Run the cron tests 20 times (pending)"} {
		if !strings.Contains(user, want) {
			t.Errorf("the judge prompt has no %q:\n%s", want, user)
		}
	}
	if strings.Contains(user, "5151") {
		t.Error("the judge sees only the processes of the goal session")
	}
}

func TestStepConflictPauseWins(t *testing.T) {
	f := newStepFix(t, stepState("ship it", 20), `{"verdict":"continue","reason":"more to do"}`)
	f.prov.onCall = func() {
		if _, err := f.m.Update(context.Background(), f.sess.ID, func(s *State, _ bool) error {
			s.SetStatus(StatusPaused, "paused by the owner")
			return nil
		}); err != nil {
			t.Error(err)
		}
	}
	res := f.step("an answer")
	if !res.Ran || res.Continuation != nil || len(res.Notices) != 0 || res.Parked {
		t.Fatalf("a /goal pause during the judge call wins: %+v", res)
	}
	got := f.state(t)
	if got.Status != StatusPaused || stepStr(got.PausedReason) != "paused by the owner" || got.TurnsUsed != 0 || got.LastVerdict != nil {
		t.Fatalf("the step drops its verdict: %+v", got)
	}
	if f.m.IsCurrent(f.sess.ID, got.Epoch) {
		t.Fatal("a paused goal is not current")
	}
}

func TestStepConflictSubgoalKeepsTheVerdict(t *testing.T) {
	f := newStepFix(t, stepState("ship it", 20), `{"verdict":"continue","reason":"more to do"}`)
	f.prov.onCall = func() {
		if _, err := f.m.Update(context.Background(), f.sess.ID, func(s *State, _ bool) error {
			_, err := s.AddSubgoal("add a regression test")
			return err
		}); err != nil {
			t.Error(err)
		}
	}
	res := f.step("an answer")
	got := f.state(t)
	c := res.Continuation
	if c == nil || !strings.Contains(c.Text, "add a regression test") || c.Epoch != got.Epoch || c.Reason != "more to do" {
		t.Fatalf("a /subgoal edit keeps the verdict: %+v", c)
	}
	if got.TurnsUsed != 1 || stepStr(got.LastVerdict) != VerdictContinue || len(got.Subgoals) != 1 {
		t.Fatalf("%+v", got)
	}
	if !f.m.IsCurrent(f.sess.ID, c.Epoch) {
		t.Fatal("the continuation is current")
	}
}

func TestContinuationStatusLine(t *testing.T) {
	cases := []struct {
		c    Continuation
		want string
	}{
		{Continuation{LineKind: LineContinue, Reason: "the fix is not verified", Used: 2, Max: 20}, "↻ Goal 2/20: the fix is not verified"},
		{Continuation{LineKind: LineLifted, Used: 3, Max: 20}, "▶ Goal barrier lifted — resuming."},
		{Continuation{LineKind: LineRetry, Used: 3, Max: 20}, "↻ Goal 3/20: retry after a failed turn."},
	}
	for _, c := range cases {
		if got := c.c.StatusLine(); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}

// failedTurn returns the outcome of a turn of f that returned err.
func (f *stepFix) failedTurn(err error) StepResult {
	return f.m.AfterTurn(context.Background(), TurnOutcome{Key: f.sess.Key, Session: f.sess, Err: err})
}

func TestStepFailedTurnRetriesOnceThenPauses(t *testing.T) {
	f := newStepFix(t, stepState("ship it", 20), `{"verdict":"continue","reason":"x"}`)
	boom := errors.New("HTTP 503: the provider is down")
	res := f.failedTurn(boom)
	if !res.Ran || res.RetryAfter != 60*time.Second || RetryDelay != 60*time.Second || res.Continuation != nil || len(res.Notices) != 0 {
		t.Fatalf("the first failure asks for one retry after 60 s: %+v", res)
	}
	got := f.state(t)
	if got.ConsecutiveTurnFailures != 1 || got.TurnsUsed != 0 || got.Status != StatusActive || got.Epoch != 1 {
		t.Fatalf("a failed turn does not count: %+v", got)
	}
	if f.prov.calls() != 0 {
		t.Fatal("a failed turn skips the judge")
	}
	res = f.failedTurn(boom)
	want := Notice{Kind: NoticeTurnFailed, Err: "HTTP 503: the provider is down", StopButtons: true}
	if res.RetryAfter != 0 || res.Continuation != nil || len(res.Notices) != 1 || res.Notices[0] != want {
		t.Fatalf("the second failure in a row pauses: %+v", res)
	}
	got = f.state(t)
	if got.Status != StatusPaused || stepStr(got.PausedReason) != "the last turn failed: HTTP 503: the provider is down" || got.ConsecutiveTurnFailures != 2 {
		t.Fatalf("%+v", got)
	}
}

func TestStepEmptyAnswerCountsAsFailed(t *testing.T) {
	f := newStepFix(t, stepState("ship it", 20), `{"verdict":"continue","reason":"x"}`)
	if res := f.step("  \n"); res.RetryAfter != RetryDelay || f.prov.calls() != 0 {
		t.Fatalf("a blank answer is an empty turn: %+v", res)
	}
	res := f.step("")
	want := Notice{Kind: NoticeTurnFailed, Err: "the turn gave no answer", StopButtons: true}
	if len(res.Notices) != 1 || res.Notices[0] != want {
		t.Fatalf("%+v", res)
	}
	if got := f.state(t); stepStr(got.PausedReason) != "the last turn failed: the turn gave no answer" {
		t.Fatalf("%+v", got)
	}
}

func TestStepGoodTurnResetsTheFailures(t *testing.T) {
	f := newStepFix(t, stepState("ship it", 20), `{"verdict":"continue","reason":"more to do"}`)
	f.failedTurn(errors.New("HTTP 503"))
	res := f.step("I fixed the clock.")
	if res.Continuation == nil || f.prov.calls() != 1 {
		t.Fatalf("a good turn runs the judge: %+v", res)
	}
	if got := f.state(t); got.ConsecutiveTurnFailures != 0 || got.TurnsUsed != 1 {
		t.Fatalf("%+v", got)
	}
	if res := f.failedTurn(errors.New("HTTP 503")); res.RetryAfter != RetryDelay || len(res.Notices) != 0 {
		t.Fatalf("the count starts again after a good turn: %+v", res)
	}
}

func TestStepToolRoundLimitIsNotAFailure(t *testing.T) {
	f := newStepFix(t, stepState("ship it", 20), `{"verdict":"continue","reason":"more to do"}`)
	res := f.step("I stopped at the tool-round limit. Summary: the cron test still fails.")
	if res.RetryAfter != 0 || res.Continuation == nil || f.prov.calls() != 1 {
		t.Fatalf("a turn with a summary is not a failed turn: %+v", res)
	}
}

func TestStepParkedGoalIgnoresAFailedTurn(t *testing.T) {
	st := stepState("ship it", 20)
	st.WaitOnPID(4242, "CI green", stepNow)
	f := newStepFix(t, st, `{"verdict":"continue","reason":"x"}`)
	f.alive[4242] = true
	res := f.failedTurn(errors.New("HTTP 503"))
	if !res.Parked || res.RetryAfter != 0 || len(res.Notices) != 1 || res.Notices[0].Kind != NoticeStillParked {
		t.Fatalf("the parked check comes first: %+v", res)
	}
	if got := f.state(t); got.ConsecutiveTurnFailures != 0 {
		t.Fatalf("%+v", got)
	}
}
