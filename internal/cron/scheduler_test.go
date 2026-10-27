package cron

import (
	"context"
	"errors"
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

func (f *fakeTurn) turn(ctx context.Context, t CronTurn) (string, error) {
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
	return "report", nil
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
		if got := grace(sched, now); got != want {
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
	s.Runner.Turn = func(context.Context, CronTurn) (string, error) { return "", errors.New("provider down") }
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
	s.Runner.Turn = func(context.Context, CronTurn) (string, error) { panic("boom") }
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
	if j := mustGet(t, s.Store, id); j.LastStatus != "error" || len(s.sends()) != 0 {
		t.Fatal(j, s.sends())
	}
}
