package cron

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// The scheduler defaults, and the rules for a missed run.
const (
	DefaultMaxParallel = 2
	DefaultTick        = 30 * time.Second
	// MissedSkip moves a missed run to the next run after now.
	MissedSkip = "skip"
	// MissedCatchup runs a missed job once.
	MissedCatchup = "catchup"
)

// The grace window of a late recurring run is half its period, between these limits
// (Hermes _compute_grace_seconds). A run that is late by less than the grace still runs,
// so a short restart does not lose a run.
const (
	minGrace = 2 * time.Minute
	maxGrace = 2 * time.Hour
)

// After a run record fails to write, the next run of the job stays in the past. The job then
// waits before it runs again. The wait starts at markRetryMin and doubles up to markRetryMax.
const (
	markRetryMin = time.Minute
	markRetryMax = time.Hour
)

// markRetry is the wait of one job after a failed run record.
type markRetry struct {
	// at is the time when the job can run again.
	at   time.Time
	wait time.Duration
}

// Scheduler runs the due jobs of Store in the gateway process. It ticks every Tick.
// The zero values use the defaults: MaxParallel 2, Missed "skip", Tick 30 s, GraceMin 2 min,
// GraceMax 2 h.
type Scheduler struct {
	Store   *Store
	Runner  *Runner
	Deliver *Deliverer
	// MaxParallel is the maximum number of jobs that run at the same time.
	MaxParallel int
	// Missed is the rule for a run that was missed while the process was down: "skip" or
	// "catchup".
	Missed string
	Tick   time.Duration
	// GraceMin and GraceMax bound the grace window of a late recurring run. 0 uses 2 min
	// and 2 h.
	GraceMin time.Duration
	GraceMax time.Duration
	// Now returns the current time. Nil uses Store.Now, then time.Now.
	Now func() time.Time
	Log *slog.Logger

	initOnce sync.Once
	sem      chan struct{}
	mu       sync.Mutex
	running  map[string]bool
	// retry holds the jobs that wait after a failed run record.
	retry map[string]markRetry
	// workdirBusy is true while a job with a workdir runs. Such jobs run one at a time.
	workdirBusy bool
	// base is the context of Start. RunNow uses it, so a run does not end with the turn
	// that started it.
	base context.Context
	wg   sync.WaitGroup
	// afterTick is a test hook. Start calls it after each tick.
	afterTick func()
}

func (s *Scheduler) init() {
	s.initOnce.Do(func() {
		n := s.MaxParallel
		if n <= 0 {
			n = DefaultMaxParallel
		}
		s.sem = make(chan struct{}, n)
		s.running = map[string]bool{}
		s.retry = map[string]markRetry{}
	})
}

// Start applies the missed-run rule, then runs the due jobs at once and after each Tick.
// It returns when ctx ends and the started runs have stopped. Call it in a goroutine.
func (s *Scheduler) Start(ctx context.Context) {
	s.init()
	s.mu.Lock()
	s.base = ctx
	s.mu.Unlock()
	s.applyMissed(ctx)
	tick := s.Tick
	if tick <= 0 {
		tick = DefaultTick
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	defer s.wg.Wait()
	for {
		// Each tick runs in its own goroutine, so a slow job does not stop the next tick.
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.TickOnce(ctx)
			if s.afterTick != nil {
				s.afterTick()
			}
		}()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// TickOnce starts the due jobs that fit in the limits, and waits for them to end. A job
// that still runs is not started again. A due job over MaxParallel waits for the next tick.
// Only one job with a workdir runs at a time (Hermes).
func (s *Scheduler) TickOnce(ctx context.Context) {
	s.init()
	due, err := s.Store.Due(ctx, s.now())
	if err != nil {
		if ctx.Err() == nil {
			s.log().Error("cron: cannot read the due jobs", "err", err)
		}
		return
	}
	var wg sync.WaitGroup
	for _, j := range due {
		if !s.claim(j) {
			continue
		}
		// Another tick can end this job between Due and claim. Read the job again, so
		// that a finished run does not start a second time.
		cur, err := s.Store.Get(ctx, j.ID)
		if err != nil || !cur.Enabled || cur.NextRun.IsZero() || cur.NextRun.After(s.now()) {
			s.release(j)
			continue
		}
		wg.Add(1)
		go func(j Job) {
			defer wg.Done()
			defer s.release(j)
			s.runJob(ctx, j)
		}(cur)
	}
	wg.Wait()
}

// RunNow makes the job id due now and starts a tick, so the job runs at once when a slot
// is free. It returns before the run ends. The cronjob tool "run" action uses it.
func (s *Scheduler) RunNow(ctx context.Context, id string) error {
	s.init()
	j, err := s.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	if !j.Enabled {
		return fmt.Errorf("cron: job %s is paused. Resume it first", id)
	}
	now := s.now()
	s.mu.Lock()
	busy := s.running[id]
	base := s.base
	var wait time.Duration
	if r, ok := s.retry[id]; ok && now.Before(r.at) {
		wait = r.at.Sub(now)
	}
	s.mu.Unlock()
	if busy {
		return fmt.Errorf("cron: job %s is already running", id)
	}
	// claim does not start a job that waits after a failed run record, so tell the caller.
	if wait > 0 {
		return fmt.Errorf("cron: job %s waits %s after a failed run record. It runs again after the wait",
			id, wait.Round(time.Second))
	}
	if base == nil {
		base = context.WithoutCancel(ctx)
	}
	if base.Err() != nil {
		return errors.New("cron: the scheduler is stopped")
	}
	if _, err := s.Store.Update(ctx, id, JobPatch{NextRun: &now}); err != nil {
		return err
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.TickOnce(base)
	}()
	return nil
}

// claim reserves a slot for the job j. It returns false when the job runs already, when
// the job waits after a failed run record, when another workdir job runs, or when all
// MaxParallel slots are in use.
func (s *Scheduler) claim(j Job) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[j.ID] || (j.Workdir != "" && s.workdirBusy) {
		return false
	}
	if r, ok := s.retry[j.ID]; ok && now.Before(r.at) {
		return false
	}
	select {
	case s.sem <- struct{}{}:
	default:
		return false
	}
	s.running[j.ID] = true
	if j.Workdir != "" {
		s.workdirBusy = true
	}
	return true
}

func (s *Scheduler) release(j Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, j.ID)
	if j.Workdir != "" {
		s.workdirBusy = false
	}
	<-s.sem
}

// runJob runs the job j, delivers the outcome, and then records the run.
func (s *Scheduler) runJob(ctx context.Context, j Job) {
	ranAt := s.now()
	o := s.run(ctx, j)
	// The writes use a context without cancel, so a shutdown still records the run.
	wctx := context.WithoutCancel(ctx)
	if ctx.Err() != nil {
		// The process stops before the run ends. The outcome is not complete, so it is not
		// delivered. MarkStopped records the status stopped, and the cronjob list shows it.
		// The first stop does not use up the job, so a one-shot job runs again after the
		// restart. A stopped run that follows a stopped run counts as a run.
		o.Text = fmt.Sprintf("⚠ Cron job %s stopped: the gateway shut down before the run ended.", j.Name)
		s.log().Warn("cron job stopped by shutdown", "job", j.ID, "name", j.Name)
		s.noteMark(j.ID, s.Store.MarkStopped(wctx, j.ID, o.Text, ranAt))
		return
	}
	status := o.Status
	if s.Deliver != nil {
		if err := s.Deliver.Deliver(ctx, j, o); err != nil {
			status = "delivery_failed"
		}
	}
	s.noteMark(j.ID, s.Store.MarkRun(wctx, j.ID, status, o.Text, ranAt))
}

// noteMark keeps the result err of the run record of the job id. A failed write leaves the
// next run in the past, so the job waits before it runs again. Each failure in a row doubles
// the wait. A good write clears the wait. A job with a bad stored schedule needs no wait,
// because the store disabled it.
func (s *Scheduler) noteMark(id string, err error) {
	if errors.Is(err, errBadSchedule) {
		s.log().Error("cron: job disabled: its stored schedule is not valid", "job", id, "err", err)
		err = nil
	}
	// The owner deleted the job during its run. Nothing runs again, so the job needs no wait.
	if errors.Is(err, ErrNotFound) {
		s.log().Info("cron: the job was deleted during its run; the run is not recorded", "job", id)
		err = nil
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		delete(s.retry, id)
		return
	}
	r := s.retry[id]
	r.wait = min(max(2*r.wait, markRetryMin), markRetryMax)
	r.at = now.Add(r.wait)
	s.retry[id] = r
	s.log().Error("cron: cannot record the job run. The job waits before it runs again",
		"job", id, "wait", r.wait, "err", err)
}

// run calls Runner.Run. A panic becomes an error outcome, so one job cannot stop the
// scheduler.
func (s *Scheduler) run(ctx context.Context, j Job) (o RunOutcome) {
	defer func() {
		if r := recover(); r != nil {
			s.log().Error("cron job panic", "job", j.ID, "panic", fmt.Sprint(r))
			o = RunOutcome{Text: fmt.Sprintf("⚠ Cron job %s failed: internal error", j.Name), Send: true, Status: "error"}
		}
	}()
	if s.Runner == nil {
		return RunOutcome{Text: fmt.Sprintf("⚠ Cron job %s failed: no runner is set", j.Name), Send: true, Status: "error"}
	}
	return s.Runner.Run(ctx, j)
}

// applyMissed applies the missed-run rule to the jobs that came due while the process was
// down. With "skip", a recurring job that is later than its grace window moves to its
// next run after now. With "catchup", each due job runs once. A one-shot job always runs
// once, late, because it has no next run (Hermes).
func (s *Scheduler) applyMissed(ctx context.Context) {
	now := s.now()
	due, err := s.Store.Due(ctx, now)
	if err != nil {
		s.log().Error("cron: cannot read the missed jobs", "err", err)
		return
	}
	catchup := s.Missed == MissedCatchup
	for _, j := range due {
		sched, err := parse(j.Schedule, s.Store.loc())
		if err != nil || sched.OneShot() {
			continue
		}
		late := now.Sub(j.NextRun)
		if catchup {
			s.log().Info("cron: missed run catches up", "job", j.ID, "name", j.Name, "late", late.Round(time.Second))
			continue
		}
		if late <= s.grace(sched, now) {
			continue
		}
		next := sched.Next(now)
		if _, err := s.Store.Update(ctx, j.ID, JobPatch{NextRun: &next}); err != nil {
			s.log().Error("cron: cannot skip the missed run", "job", j.ID, "err", err)
			continue
		}
		s.log().Info("cron: missed run skipped", "job", j.ID, "name", j.Name,
			"missed", j.NextRun, "next", next)
	}
}

// grace returns half the period of sched, between the grace minimum and the grace maximum.
func (s *Scheduler) grace(sched Schedule, now time.Time) time.Duration {
	lo, hi := s.GraceMin, s.GraceMax
	if lo <= 0 {
		lo = minGrace
	}
	if hi <= 0 {
		hi = maxGrace
	}
	first := sched.Next(now)
	second := sched.Next(first)
	if first.IsZero() || second.IsZero() {
		return lo
	}
	return min(max(second.Sub(first)/2, lo), hi)
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return s.Store.now()
}

func (s *Scheduler) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}
