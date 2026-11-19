package cron

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

	"github.com/dnwandana/tether-agent/internal/sessions"
)

// fakeTurn is a Runner.Turn that counts the calls by session key. With gate set, each
// call waits until gate closes.
type fakeTurn struct {
	mu        sync.Mutex
	calls     map[string]int
	active    int
	maxActive int
	gate      chan struct{}
	started   chan string
}

func (f *fakeTurn) turn(ctx context.Context, t CronTurn) (TurnOutput, error) {
	f.mu.Lock()
	f.calls[t.Session.Key]++
	f.active++
	f.maxActive = max(f.maxActive, f.active)
	gate, started := f.gate, f.started
	f.mu.Unlock()
	if started != nil {
		started <- t.Session.Key
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
		}
	}
	f.mu.Lock()
	f.active--
	f.mu.Unlock()
	return TurnOutput{Final: "report"}, nil
}

func (f *fakeTurn) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		n += c
	}
	return n
}

func (f *fakeTurn) max() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxActive
}

// schedFixture holds a Scheduler with a fake agent turn and a recording Deliverer.
type schedFixture struct {
	*Scheduler
	turn *fakeTurn
	mu   sync.Mutex
	sent []string
	// sendErr is the error of each send.
	sendErr error
}

func (f *schedFixture) sends() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

func scheduler(t *testing.T) (*schedFixture, *clock) {
	t.Helper()
	c := fixedNow(2026, 10, 27, 10, 0)
	st := store(t, c)
	f := &schedFixture{turn: &fakeTurn{calls: map[string]int{}}}
	f.Scheduler = &Scheduler{
		Store: st,
		Runner: &Runner{
			Store:      sessions.New(st.DB),
			ScriptsDir: t.TempDir(),
			SpillDir:   t.TempDir(),
			Turn:       f.turn.turn,
		},
		Deliver: &Deliverer{
			Send: func(_ context.Context, _ Target, text string, _ []string) error {
				f.mu.Lock()
				defer f.mu.Unlock()
				if f.sendErr != nil {
					return f.sendErr
				}
				f.sent = append(f.sent, text)
				return nil
			},
			Home: homeAt("100", ""),
		},
		Now: c.Now,
	}
	return f, c
}

func mustCreate(t *testing.T, s *Store, j Job) string {
	t.Helper()
	got, err := s.Create(ctx, j)
	if err != nil {
		t.Fatal(err)
	}
	return got.ID
}

func mustGet(t *testing.T, s *Store, id string) Job {
	t.Helper()
	j, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// runs returns the number of agent turns of the job id.
func runs(t *testing.T, f *schedFixture, id string) int {
	t.Helper()
	f.turn.mu.Lock()
	defer f.turn.mu.Unlock()
	return f.turn.calls["cron:"+id]
}

// startOnce runs Start until its first tick ends.
func startOnce(t *testing.T, s *Scheduler) {
	t.Helper()
	c, cancel := context.WithCancel(ctx)
	defer cancel()
	s.afterTick = cancel
	done := make(chan struct{})
	go func() { s.Start(c); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return after the first tick")
	}
}

func TestRunsDueJobsAndMarks(t *testing.T) {
	s, clk := scheduler(t)
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x", Enabled: true})
	clk.t = clk.t.Add(31 * time.Minute)
	s.TickOnce(ctx)
	j := mustGet(t, s.Store, id)
	if j.LastRun.IsZero() || !j.NextRun.After(clk.Now()) || j.LastStatus != "ok" || j.LastOutput != "report" {
		t.Fatal(j)
	}
	if runs(t, s, id) != 1 || len(s.sends()) != 1 || s.sends()[0] != "report" {
		t.Fatal(runs(t, s, id), s.sends())
	}
	s.TickOnce(ctx)
	if runs(t, s, id) != 1 {
		t.Fatal("a job that is not due must not run")
	}
}

func TestNotDueJobDoesNotRun(t *testing.T) {
	s, _ := scheduler(t)
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x", Enabled: true})
	s.TickOnce(ctx)
	if runs(t, s, id) != 0 {
		t.Fatal("ran too early")
	}
}

func TestMaxParallel2(t *testing.T) {
	s, clk := scheduler(t)
	s.turn.gate = make(chan struct{})
	s.turn.started = make(chan string, 8)
	var ids []string
	for _, n := range []string{"a", "b", "c", "d"} {
		ids = append(ids, mustCreate(t, s.Store, Job{Name: n, Schedule: "30m", Prompt: "x", Enabled: true}))
	}
	clk.t = clk.t.Add(31 * time.Minute)
	done := make(chan struct{})
	go func() { s.TickOnce(ctx); close(done) }()
	<-s.turn.started
	<-s.turn.started
	// A second tick while two jobs run starts nothing: the slots are full.
	s.TickOnce(ctx)
	if got := s.turn.total(); got != 2 {
		t.Fatalf("%d jobs started, want 2", got)
	}
	close(s.turn.gate)
	<-done
	s.TickOnce(ctx)
	if s.turn.total() != 4 || s.turn.max() != 2 {
		t.Fatal(s.turn.total(), s.turn.max())
	}
	for _, id := range ids {
		if runs(t, s, id) != 1 {
			t.Fatal(id, runs(t, s, id))
		}
	}
}

func TestRunningJobNotStartedAgain(t *testing.T) {
	s, clk := scheduler(t)
	s.turn.gate = make(chan struct{})
	s.turn.started = make(chan string, 4)
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x", Enabled: true})
	clk.t = clk.t.Add(31 * time.Minute)
	done := make(chan struct{})
	go func() { s.TickOnce(ctx); close(done) }()
	<-s.turn.started
	s.TickOnce(ctx)
	if err := s.RunNow(ctx, id); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatal(err)
	}
	close(s.turn.gate)
	<-done
	if runs(t, s, id) != 1 {
		t.Fatal(runs(t, s, id))
	}
}

func TestWorkdirJobsSequential(t *testing.T) {
	s, clk := scheduler(t)
	s.turn.gate = make(chan struct{})
	s.turn.started = make(chan string, 4)
	a := mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x", Workdir: t.TempDir(), Enabled: true})
	b := mustCreate(t, s.Store, Job{Name: "b", Schedule: "30m", Prompt: "x", Workdir: t.TempDir(), Enabled: true})
	clk.t = clk.t.Add(31 * time.Minute)
	done := make(chan struct{})
	go func() { s.TickOnce(ctx); close(done) }()
	<-s.turn.started
	s.TickOnce(ctx)
	if s.turn.total() != 1 {
		t.Fatal("two workdir jobs overlap")
	}
	close(s.turn.gate)
	<-done
	s.TickOnce(ctx)
	if runs(t, s, a) != 1 || runs(t, s, b) != 1 || s.turn.max() != 1 {
		t.Fatal(runs(t, s, a), runs(t, s, b), s.turn.max())
	}
}

func TestWorkdirJobRunsBesideOtherJob(t *testing.T) {
	s, clk := scheduler(t)
	s.turn.gate = make(chan struct{})
	s.turn.started = make(chan string, 4)
	mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x", Workdir: t.TempDir(), Enabled: true})
	mustCreate(t, s.Store, Job{Name: "b", Schedule: "30m", Prompt: "x", Enabled: true})
	clk.t = clk.t.Add(31 * time.Minute)
	done := make(chan struct{})
	go func() { s.TickOnce(ctx); close(done) }()
	<-s.turn.started
	<-s.turn.started
	close(s.turn.gate)
	<-done
	if s.turn.max() != 2 {
		t.Fatal(s.turn.max())
	}
}

func TestMissedRunsSkipped(t *testing.T) {
	s, clk := scheduler(t)
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "0 1 * * *", Prompt: "x", Enabled: true})
	clk.t = clk.t.Add(3 * 24 * time.Hour) // the binary was down
	startOnce(t, s.Scheduler)
	if runs(t, s, id) != 0 {
		t.Fatal("missed runs are skipped")
	}
	j := mustGet(t, s.Store, id)
	if !j.NextRun.After(clk.Now()) || !j.LastRun.IsZero() {
		t.Fatal(j)
	}
}

func TestMissedRunsCatchupRunsOnce(t *testing.T) {
	s, clk := scheduler(t)
	s.Missed = MissedCatchup
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "0 1 * * *", Prompt: "x", Enabled: true})
	clk.t = clk.t.Add(3 * 24 * time.Hour)
	startOnce(t, s.Scheduler)
	if runs(t, s, id) != 1 {
		t.Fatal("catchup runs a missed job once", runs(t, s, id))
	}
	if j := mustGet(t, s.Store, id); !j.NextRun.After(clk.Now()) {
		t.Fatal(j)
	}
}

func TestLateRunInGraceStillRuns(t *testing.T) {
	s, clk := scheduler(t)
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x", Enabled: true})
	clk.t = clk.t.Add(31 * time.Minute) // 1 min late; the grace of 30m is 15 min
	startOnce(t, s.Scheduler)
	if runs(t, s, id) != 1 {
		t.Fatal("a short restart must not lose a run")
	}
}

func TestMissedOneShotRunsLate(t *testing.T) {
	s, clk := scheduler(t)
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "2026-10-27T12:00", Prompt: "x", Enabled: true})
	clk.t = clk.t.Add(24 * time.Hour)
	startOnce(t, s.Scheduler)
	if runs(t, s, id) != 1 {
		t.Fatal("a missed one-shot job runs once")
	}
	if j := mustGet(t, s.Store, id); j.Enabled {
		t.Fatal(j)
	}
}

func TestGrace(t *testing.T) {
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	for spec, want := range map[string]time.Duration{
		"2m": 2 * time.Minute, "5m": 150 * time.Second, "30m": 15 * time.Minute, "0 1 * * *": 2 * time.Hour,
	} {
		sched, err := parse(spec, time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		if got := (&Scheduler{}).grace(sched, now); got != want {
			t.Fatal(spec, got)
		}
	}
}

func TestScriptReporter100RunsNoModelNoBleed(t *testing.T) {
	s, clk := scheduler(t)
	counter := filepath.Join(t.TempDir(), "count")
	p := filepath.Join(s.Runner.ScriptsDir, "rl.sh")
	if err := os.WriteFile(p, []byte("echo run >> '"+counter+"'\necho 'allsaints: 4120/9000'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	id := mustCreate(t, s.Store, Job{Name: "rl", Schedule: "5m", Script: "rl.sh", NoAgent: true, Enabled: true})
	for range 100 {
		clk.t = clk.t.Add(5 * time.Minute)
		s.TickOnce(ctx)
	}
	b, err := os.ReadFile(counter)
	if err != nil || strings.Count(string(b), "run") != 100 {
		t.Fatal(strings.Count(string(b), "run"), err)
	}
	if s.turn.total() != 0 || len(s.sends()) != 100 {
		t.Fatal(s.turn.total(), len(s.sends()))
	}
	var n int
	if err := s.Store.DB.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&n); err != nil || n != 0 {
		t.Fatal("script jobs create no session rows", n, err)
	}
	if j := mustGet(t, s.Store, id); j.LastStatus != "ok" || j.LastOutput != "allsaints: 4120/9000\n" {
		t.Fatal(j)
	}
}

func TestDeliveryFailureRecorded(t *testing.T) {
	s, clk := scheduler(t)
	s.sendErr = errors.New("telegram down")
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x", Deliver: "telegram:123", Enabled: true})
	clk.t = clk.t.Add(31 * time.Minute)
	s.TickOnce(ctx)
	if j := mustGet(t, s.Store, id); j.LastStatus != "delivery_failed" || !j.NextRun.After(clk.Now()) {
		t.Fatal(j)
	}
}

func TestAgentErrorRecorded(t *testing.T) {
	s, clk := scheduler(t)
	s.Runner.Turn = func(context.Context, CronTurn) (TurnOutput, error) { return TurnOutput{}, errors.New("provider down") }
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x", Enabled: true})
	clk.t = clk.t.Add(31 * time.Minute)
	s.TickOnce(ctx)
	j := mustGet(t, s.Store, id)
	if j.LastStatus != "error" || len(s.sends()) != 1 || !strings.HasPrefix(s.sends()[0], "⚠ Cron job a failed:") {
		t.Fatal(j, s.sends())
	}
}

func TestPanicDoesNotStopScheduler(t *testing.T) {
	s, clk := scheduler(t)
	s.Runner.Turn = func(context.Context, CronTurn) (TurnOutput, error) { panic("boom") }
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x", Enabled: true})
	clk.t = clk.t.Add(31 * time.Minute)
	s.TickOnce(ctx)
	if j := mustGet(t, s.Store, id); j.LastStatus != "error" {
		t.Fatal(j)
	}
}

func TestRunNow(t *testing.T) {
	s, _ := scheduler(t)
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "0 1 * * *", Prompt: "x", Enabled: true})
	before := mustGet(t, s.Store, id).NextRun
	if err := s.RunNow(ctx, id); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for mustGet(t, s.Store, id).LastRun.IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("RunNow did not run the job")
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.wg.Wait()
	if j := mustGet(t, s.Store, id); runs(t, s, id) != 1 || !j.NextRun.Equal(before) {
		t.Fatal(runs(t, s, id), j.NextRun, before)
	}
}

func TestRunNowPausedOrMissing(t *testing.T) {
	s, _ := scheduler(t)
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x"})
	if err := s.RunNow(ctx, id); err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatal(err)
	}
	if err := s.RunNow(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestShutdownRecordsRunWithoutDelivery(t *testing.T) {
	s, clk := scheduler(t)
	s.turn.gate = make(chan struct{})
	s.turn.started = make(chan string, 2)
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x", Enabled: true})
	clk.t = clk.t.Add(31 * time.Minute)
	c, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { s.TickOnce(c); close(done) }()
	<-s.turn.started
	cancel()
	<-done
	j := mustGet(t, s.Store, id)
	if j.LastStatus != "stopped" || !strings.Contains(j.LastOutput, "shut down") || len(s.sends()) != 0 {
		t.Fatal(j, s.sends())
	}
	if !j.Enabled || j.RepeatLeft != -1 || !j.NextRun.After(clk.Now()) {
		t.Fatalf("a stopped recurring run waits for its next run: %+v", j)
	}
}

// shutdownRun starts the due job id, then cancels the tick while the job runs.
func shutdownRun(t *testing.T, s *schedFixture) {
	t.Helper()
	s.turn.gate = make(chan struct{})
	s.turn.started = make(chan string, 2)
	c, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { s.TickOnce(c); close(done) }()
	<-s.turn.started
	cancel()
	<-done
	s.turn.gate, s.turn.started = nil, nil
}

// A run that a shutdown stops does not use up the job. A one-shot job and a job on its last
// repeat stay enabled and due, so they run again after the restart. A second stop in a row
// counts as the run: the job is used up, and its output tells why.
func TestShutdownDoesNotUseUpLastRun(t *testing.T) {
	for name, job := range map[string]Job{
		"one-shot": {Name: "a", Schedule: "2026-10-27T10:30", Prompt: "x", Enabled: true},
		"repeat 1": {Name: "a", Schedule: "30m", Prompt: "x", RepeatLeft: 1, Enabled: true},
	} {
		t.Run(name, func(t *testing.T) {
			s, clk := scheduler(t)
			id := mustCreate(t, s.Store, job)
			clk.t = clk.t.Add(31 * time.Minute)
			shutdownRun(t, s)
			j := mustGet(t, s.Store, id)
			if j.LastStatus != StatusStopped || !strings.Contains(j.LastOutput, "shut down") || len(s.sends()) != 0 {
				t.Fatal(j, s.sends())
			}
			if !j.Enabled || j.RepeatLeft != 1 || j.NextRun.IsZero() || j.NextRun.After(clk.Now()) {
				t.Fatalf("a stopped last run stays enabled and due: %+v", j)
			}
			// The restart runs the job again. That run uses up the job.
			clk.t = clk.t.Add(time.Minute)
			startOnce(t, s.Scheduler)
			j = mustGet(t, s.Store, id)
			if runs(t, s, id) != 2 || len(s.sends()) != 1 || j.Enabled || j.RepeatLeft != 0 || j.LastStatus != "ok" {
				t.Fatal(runs(t, s, id), s.sends(), j)
			}
		})
		t.Run(name+" stopped again", func(t *testing.T) {
			s, clk := scheduler(t)
			id := mustCreate(t, s.Store, job)
			clk.t = clk.t.Add(31 * time.Minute)
			shutdownRun(t, s)
			clk.t = clk.t.Add(time.Minute)
			shutdownRun(t, s)
			j := mustGet(t, s.Store, id)
			if runs(t, s, id) != 2 || len(s.sends()) != 0 || j.LastStatus != StatusStopped {
				t.Fatal(runs(t, s, id), s.sends(), j)
			}
			if j.Enabled || j.RepeatLeft != 0 || !j.NextRun.IsZero() || !strings.Contains(j.LastOutput, "shut down") ||
				!strings.Contains(j.LastOutput, "The job does not run again: two shutdowns in a row stopped it.") {
				t.Fatalf("a second stop in a row uses up the job: %+v", j)
			}
		})
	}
}

// A one-shot job that stops one time and then completes through RunNow is delivered and used
// up. Its output has no used-up line.
func TestStoppedOneShotCompletesThroughRunNow(t *testing.T) {
	s, clk := scheduler(t)
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "2026-10-27T10:30", Prompt: "x", Enabled: true})
	clk.t = clk.t.Add(31 * time.Minute)
	shutdownRun(t, s)
	clk.t = clk.t.Add(time.Minute)
	if err := s.RunNow(ctx, id); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	j := mustGet(t, s.Store, id)
	if runs(t, s, id) != 2 || len(s.sends()) != 1 || j.Enabled || j.RepeatLeft != 0 || j.LastStatus != "ok" ||
		strings.Contains(j.LastOutput, "two shutdowns") {
		t.Fatal(runs(t, s, id), s.sends(), j)
	}
}

// A run record that cannot be written leaves the next run in the past. The job then waits
// before it runs again, and the wait doubles. A good write clears the wait.
func TestMarkRunFailureBacksOff(t *testing.T) {
	s, clk := scheduler(t)
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x", Enabled: true})
	if _, err := s.Store.DB.ExecContext(ctx, `CREATE TRIGGER fail_write BEFORE UPDATE ON cron_jobs
		BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(31 * time.Minute)
	for range 3 {
		s.TickOnce(ctx)
	}
	if runs(t, s, id) != 1 || len(s.sends()) != 1 {
		t.Fatal("a failed run record must not start the job on each tick", runs(t, s, id), s.sends())
	}
	clk.t = clk.t.Add(markRetryMin + time.Second)
	s.TickOnce(ctx)
	s.TickOnce(ctx)
	if runs(t, s, id) != 2 {
		t.Fatal("the job runs again after the first wait", runs(t, s, id))
	}
	clk.t = clk.t.Add(markRetryMin + time.Second)
	s.TickOnce(ctx)
	if runs(t, s, id) != 2 {
		t.Fatal("the second wait is longer", runs(t, s, id))
	}
	clk.t = clk.t.Add(markRetryMin)
	s.TickOnce(ctx)
	if runs(t, s, id) != 3 {
		t.Fatal("the job runs again after the second wait", runs(t, s, id))
	}
	if _, err := s.Store.DB.ExecContext(ctx, `DROP TRIGGER fail_write`); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(4*markRetryMin + time.Second)
	s.TickOnce(ctx)
	j := mustGet(t, s.Store, id)
	if runs(t, s, id) != 4 || j.LastStatus != "ok" || !j.NextRun.After(clk.Now()) {
		t.Fatal(runs(t, s, id), j)
	}
	s.mu.Lock()
	_, waits := s.retry[id]
	s.mu.Unlock()
	if waits {
		t.Fatal("a good run record clears the wait")
	}
	for range 20 {
		s.noteMark(id, errors.New("disk I/O error"))
	}
	if w := s.retry[id].wait; w != markRetryMax {
		t.Fatal("the wait has a maximum", w)
	}
}

// A job whose stored schedule cannot be parsed has no next run. The scheduler disables it
// and tells why, so it does not run on each tick.
func TestBadStoredScheduleDisablesJob(t *testing.T) {
	s, clk := scheduler(t)
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x", Enabled: true})
	if _, err := s.Store.DB.ExecContext(ctx, `UPDATE cron_jobs SET schedule='sometimes' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(31 * time.Minute)
	for range 3 {
		s.TickOnce(ctx)
	}
	j := mustGet(t, s.Store, id)
	if runs(t, s, id) != 1 || len(s.sends()) != 1 {
		t.Fatal(runs(t, s, id), s.sends())
	}
	if j.Enabled || !j.NextRun.IsZero() || j.LastStatus != "error" || !strings.Contains(j.LastOutput, `"sometimes"`) {
		t.Fatalf("%+v", j)
	}
	if _, waits := s.retry[id]; waits {
		t.Fatal("a disabled job needs no wait")
	}
}

// A job that the owner deleted during its run gives ErrNotFound at the run record. That is
// not a failed write, so the job gets no wait.
func TestDeletedJobGetsNoWait(t *testing.T) {
	s, _ := scheduler(t)
	s.init()
	s.noteMark("gone", fmt.Errorf("%w: gone", ErrNotFound))
	s.mu.Lock()
	_, waits := s.retry["gone"]
	s.mu.Unlock()
	if waits {
		t.Fatal("a deleted job must not keep a wait")
	}
}

// RunNow tells the caller that a job waits after a failed run record, because the tick
// does not start the job before the wait ends.
func TestRunNowRefusesAJobThatWaits(t *testing.T) {
	s, clk := scheduler(t)
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x", Enabled: true})
	s.init()
	s.mu.Lock()
	s.retry[id] = markRetry{wait: markRetryMin, at: clk.Now().Add(markRetryMin)}
	s.mu.Unlock()
	if err := s.RunNow(ctx, id); err == nil || !strings.Contains(err.Error(), "waits") {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(markRetryMin + time.Second)
	if err := s.RunNow(ctx, id); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
}

func TestGraceFromFields(t *testing.T) {
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	s := &Scheduler{GraceMin: 5 * time.Minute, GraceMax: time.Hour}
	for spec, want := range map[string]time.Duration{
		"2m": 5 * time.Minute, "30m": 15 * time.Minute, "0 1 * * *": time.Hour,
	} {
		sched, err := parse(spec, time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		if got := s.grace(sched, now); got != want {
			t.Fatal(spec, got)
		}
	}
	// A schedule with no second run gets the minimum.
	once, err := parse("2026-10-27T12:00", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.grace(once, now); got != 5*time.Minute {
		t.Fatal(got)
	}
}

// A gateway stop during a cron agent turn with a limit is a stop, not a time-out.
func TestAgentTimeoutStopIsNotTimeout(t *testing.T) {
	s, clk := scheduler(t)
	s.Runner.AgentTimeout = time.Hour
	started := make(chan struct{}, 1)
	s.Runner.Turn = func(ctx context.Context, _ CronTurn) (TurnOutput, error) {
		started <- struct{}{}
		<-ctx.Done()
		return TurnOutput{Final: "partial"}, ctx.Err()
	}
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x", Enabled: true})
	clk.t = clk.t.Add(31 * time.Minute)
	c, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { s.TickOnce(c); close(done) }()
	<-started
	cancel()
	<-done
	j := mustGet(t, s.Store, id)
	if j.LastStatus != "stopped" || strings.Contains(j.LastOutput, "timed out") || len(s.sends()) != 0 {
		t.Fatal(j, s.sends())
	}
}

// failTurn is an agent turn that always fails.
func failTurn(context.Context, CronTurn) (TurnOutput, error) {
	return TurnOutput{}, errors.New("provider down")
}

// failTicks moves the clock past the next run of a "30m" job and runs one tick, n times.
func failTicks(s *schedFixture, clk *clock, n int) {
	for range n {
		clk.t = clk.t.Add(31 * time.Minute)
		s.TickOnce(ctx)
	}
}

// wantPause is the pause paragraph that the scheduler adds to the alert of the job a.
func wantPause(id string, n int) string {
	return fmt.Sprintf("\n\n⏸ The job a (%s) is paused: it failed %d runs in a row. Fix the cause. "+
		"Then ask me to resume the job.", id, n)
}

// schedRunRow is one row of cron_runs.
type schedRunRow struct {
	status, delivery, output, deliveryErr string
	startedAt, finishedAt                 int64
}

// newestRunRow returns the newest cron_runs row of the job id.
func newestRunRow(t *testing.T, s *Store, id string) schedRunRow {
	t.Helper()
	var r schedRunRow
	if err := s.DB.QueryRowContext(ctx, `SELECT status, delivery, output, delivery_error, started_at, finished_at
		FROM cron_runs WHERE job_id=? ORDER BY id DESC LIMIT 1`, id).Scan(
		&r.status, &r.delivery, &r.output, &r.deliveryErr, &r.startedAt, &r.finishedAt); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTenthFailurePausesJob(t *testing.T) {
	s, clk := scheduler(t)
	s.Store.RunHistory, s.Store.FailStreakLimit = 50, 10
	s.Runner.Turn = failTurn
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x", Enabled: true})
	failTicks(s, clk, 9)
	if j := mustGet(t, s.Store, id); !j.Enabled || j.FailStreak != 9 || strings.Contains(j.LastOutput, "⏸") {
		t.Fatalf("%+v", j)
	}
	failTicks(s, clk, 1)
	sends := s.sends()
	if len(sends) != 10 || strings.Contains(sends[8], "⏸") ||
		sends[9] != "⚠ Cron job a failed: provider down"+wantPause(id, 10) {
		t.Fatalf("%q", sends)
	}
	j := mustGet(t, s.Store, id)
	if j.Enabled || !j.NextRun.IsZero() || j.FailStreak != 10 || j.LastStatus != "error" ||
		!strings.HasSuffix(j.LastOutput, wantPause(id, 10)) {
		t.Fatalf("%+v", j)
	}
	if r := newestRunRow(t, s.Store, id); r.status != "error" || r.delivery != DeliverySent ||
		!strings.HasSuffix(r.output, wantPause(id, 10)) {
		t.Fatalf("%+v", r)
	}
}

// A one-shot job and a job on its last repeat stop after the run, so they get no pause line.
func TestLastRunGetsNoPauseLine(t *testing.T) {
	for name, job := range map[string]Job{
		"one-shot":    {Name: "a", Schedule: "2026-10-27T10:30", Prompt: "x", RepeatLeft: 3, Enabled: true},
		"last repeat": {Name: "a", Schedule: "30m", Prompt: "x", RepeatLeft: 1, Enabled: true},
	} {
		s, clk := scheduler(t)
		s.Store.RunHistory, s.Store.FailStreakLimit = 50, 1
		s.Runner.Turn = failTurn
		id := mustCreate(t, s.Store, job)
		failTicks(s, clk, 1)
		sends := s.sends()
		j := mustGet(t, s.Store, id)
		if len(sends) != 1 || strings.Contains(sends[0], "⏸") || strings.Contains(j.LastOutput, "⏸") || j.FailStreak != 1 {
			t.Fatalf("%s: %q %+v", name, sends, j)
		}
	}
}

// A pause alert that cannot be sent does not pause the job. The next failure tries again.
func TestPauseAlertSendFailureDoesNotPause(t *testing.T) {
	s, clk := scheduler(t)
	s.Store.RunHistory, s.Store.FailStreakLimit = 50, 10
	s.Runner.Turn = failTurn
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x", Enabled: true})
	failTicks(s, clk, 9)
	s.mu.Lock()
	s.sendErr = errors.New("telegram down")
	s.mu.Unlock()
	failTicks(s, clk, 1)
	j := mustGet(t, s.Store, id)
	if !j.Enabled || j.FailStreak != 10 || j.LastStatus != "delivery_failed" ||
		j.LastOutput != "⚠ Cron job a failed: provider down" {
		t.Fatalf("%+v", j)
	}
	if r := newestRunRow(t, s.Store, id); r.status != "error" || r.delivery != DeliveryFailed ||
		strings.Contains(r.output, "⏸") || !strings.Contains(r.deliveryErr, "telegram down") {
		t.Fatalf("%+v", r)
	}
	s.mu.Lock()
	s.sendErr = nil
	s.mu.Unlock()
	failTicks(s, clk, 1)
	j = mustGet(t, s.Store, id)
	sends := s.sends()
	if j.Enabled || j.FailStreak != 11 || !strings.HasSuffix(j.LastOutput, wantPause(id, 11)) ||
		len(sends) != 10 || !strings.HasSuffix(sends[9], wantPause(id, 11)) {
		t.Fatalf("%+v %q", j, sends)
	}
}

// A local job with no home channel can never send, so it pauses with the line in its output.
func TestLocalJobWithoutHomePauses(t *testing.T) {
	s, clk := scheduler(t)
	s.Store.RunHistory, s.Store.FailStreakLimit = 50, 1
	s.Runner.Turn = failTurn
	s.Deliver.Home = noHome
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x", Deliver: "local", Enabled: true})
	failTicks(s, clk, 1)
	j := mustGet(t, s.Store, id)
	if j.Enabled || j.FailStreak != 1 || !strings.HasSuffix(j.LastOutput, wantPause(id, 1)) || len(s.sends()) != 0 {
		t.Fatalf("%+v %q", j, s.sends())
	}
	if r := newestRunRow(t, s.Store, id); r.delivery != DeliveryLocal {
		t.Fatalf("%+v", r)
	}
}

func TestSilentRunRecordsDeliverySilent(t *testing.T) {
	s, clk := scheduler(t)
	s.Store.RunHistory, s.Store.FailStreakLimit = 50, 10
	s.Runner.Turn = func(context.Context, CronTurn) (TurnOutput, error) { return TurnOutput{Final: "[SILENT]"}, nil }
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x", Enabled: true})
	failTicks(s, clk, 1)
	j := mustGet(t, s.Store, id)
	if len(s.sends()) != 0 || j.LastStatus != "ok" || j.FailStreak != 0 {
		t.Fatalf("%+v %q", j, s.sends())
	}
	if r := newestRunRow(t, s.Store, id); r.status != "ok" || r.delivery != DeliverySilent || r.output != "[SILENT]" {
		t.Fatalf("%+v", r)
	}
}

// A delivery error does not hide the run status: a failed run still adds to the streak.
func TestDeliveryErrorStillCountsFailure(t *testing.T) {
	s, clk := scheduler(t)
	s.Store.RunHistory, s.Store.FailStreakLimit = 50, 10
	s.Runner.Turn = failTurn
	s.sendErr = errors.New("telegram down")
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x", Deliver: "telegram:123", Enabled: true})
	failTicks(s, clk, 2)
	if j := mustGet(t, s.Store, id); !j.Enabled || j.FailStreak != 2 || j.LastStatus != "delivery_failed" {
		t.Fatalf("%+v", j)
	}
	if r := newestRunRow(t, s.Store, id); r.status != "error" || r.delivery != DeliveryFailed {
		t.Fatalf("%+v", r)
	}
}

// The duration of a run ends before the delivery, so a slow send does not make it longer.
func TestRunDurationEndsBeforeDelivery(t *testing.T) {
	s, clk := scheduler(t)
	s.Store.RunHistory = 50
	s.Deliver.Send = func(context.Context, Target, string, []string) error {
		clk.t = clk.t.Add(time.Minute) // a slow send
		return nil
	}
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x", Enabled: true})
	clk.t = clk.t.Add(31 * time.Minute)
	ranAt := clk.t
	s.TickOnce(ctx)
	r := newestRunRow(t, s.Store, id)
	if r.delivery != DeliverySent || r.startedAt != ranAt.UnixMilli() || r.finishedAt != ranAt.UnixMilli() {
		t.Fatalf("%+v, ran at %d", r, ranAt.UnixMilli())
	}
}

func TestSweepRunsBeforeDue(t *testing.T) {
	s, clk := scheduler(t)
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x", Enabled: true})
	clk.t = clk.t.Add(31 * time.Minute)
	sweeps, before := 0, -1
	s.Sweep = func(context.Context) {
		sweeps++
		before = runs(t, s, id)
	}
	s.TickOnce(ctx)
	if sweeps != 1 || before != 0 || runs(t, s, id) != 1 {
		t.Fatal(sweeps, before, runs(t, s, id))
	}
	s.TickOnce(ctx)
	if sweeps != 2 {
		t.Fatal("the sweep runs on each tick, also with no due job")
	}
	s.Sweep = func(context.Context) { panic("boom") }
	clk.t = clk.t.Add(31 * time.Minute)
	s.TickOnce(ctx)
	if runs(t, s, id) != 2 {
		t.Fatal("a panic in the sweep must not stop the tick")
	}
}
