package cron

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
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
	// Sweep runs at the start of each tick, before Due. The app sets it to the sweep of the
	// held sends. Nil does nothing.
	Sweep func(ctx context.Context)
	// Quiet reports whether t is in the no-sound window. A send in the window has no sound.
	// Nil means never.
	Quiet func(t time.Time) bool
	// Brief builds the daily status: the text of /brief, and empty true when nothing needs the
	// owner. Nil turns the daily send off.
	Brief func(ctx context.Context) (text string, empty bool, err error)
	// BriefAt is cron.brief_at in minutes after midnight, in the zone of the clock.
	BriefAt int
	// RunEnd runs after the record of each run, on each exit path, with the recorded status.
	// The app sets it to send the dashboard event. Nil does nothing.
	RunEnd func(jobID, status string)

	initOnce sync.Once
	sem      chan struct{}
	mu       sync.Mutex
	running  map[string]bool
	// retry holds the jobs that wait after a failed run record.
	retry map[string]markRetry
	// manual holds the jobs that RunNow started. The run of the job reads and clears the flag.
	manual map[string]bool
	// workdirBusy is true while a job with a workdir runs. Such jobs run one at a time.
	workdirBusy bool
	// base is the context of Start. RunNow uses it, so a run does not end with the turn
	// that started it.
	base context.Context
	wg   sync.WaitGroup
	// afterTick is a test hook. Start calls it after each tick.
	afterTick func()
	// briefMu guards briefBusy. briefBusy is true while a check of the daily status runs, so
	// two ticks that overlap start one check.
	briefMu   sync.Mutex
	briefBusy bool
}

// BriefKVKey is the kv key with the date of the window start of the last daily status.
const BriefKVKey = "cron.brief_last"

// briefWindow is the time after cron.brief_at in which the daily status can go out.
const briefWindow = 2 * time.Hour

// briefStart returns the start of the window of the daily status that contains now, in the
// zone of now. ok is false when now is in no window.
func briefStart(now time.Time, at int) (start time.Time, ok bool) {
	y, m, d := now.Date()
	start = time.Date(y, m, d, at/60, at%60, 0, 0, now.Location())
	if now.Before(start) {
		// A window that started yesterday can cross midnight.
		start = time.Date(y, m, d-1, at/60, at%60, 0, 0, now.Location())
	}
	return start, now.Before(start.Add(briefWindow))
}

// briefReady reports whether the daily status has all the parts it needs to send.
func (s *Scheduler) briefReady() bool {
	return s.Brief != nil && s.Deliver != nil && s.Deliver.Send != nil && s.Deliver.OwnerHome != nil
}

// sendBrief sends the daily status to the owner chat when now is in the window and the
// status of this window did not go out yet.
func (s *Scheduler) sendBrief(ctx context.Context) {
	if !s.briefReady() {
		return
	}
	now := s.now()
	start, ok := briefStart(now, s.BriefAt)
	if !ok {
		return
	}
	day := start.Format("2006-01-02")
	last, _, err := s.Store.DB.KVGet(ctx, BriefKVKey)
	if err != nil {
		s.log().Warn("cron: cannot read the date of the last daily status", "err", err)
		return
	}
	if last == day {
		return
	}
	to, ok := s.Deliver.OwnerHome(ctx)
	if !ok || to.ChatID == "" {
		return
	}
	if to.Source == "" {
		to.Source = "telegram"
	}
	text, empty, err := s.Brief(ctx)
	if err != nil {
		s.log().Warn("cron: cannot build the daily status", "err", err)
		return
	}
	if empty {
		if err := s.Store.DB.KVSet(ctx, BriefKVKey, day); err != nil {
			s.log().Warn("cron: cannot save the date of the daily status", "err", err)
		}
		return
	}
	noSound := s.Quiet != nil && s.Quiet(now)
	if err := s.Deliver.Send(ctx, to, s.Deliver.redact(text), nil, noSound); err != nil {
		s.log().Warn("cron: the daily status did not go out. The next tick in the window tries again", "err", err)
		return
	}
	if err := s.Store.DB.KVSet(ctx, BriefKVKey, day); err != nil {
		s.log().Warn("cron: cannot save the date of the daily status", "err", err)
	}
}

// warnBrief logs one warning when Brief is set but the status has no send function or no
// owner chat. Start calls it once.
func (s *Scheduler) warnBrief(ctx context.Context) {
	if s.Brief == nil {
		return
	}
	off := !s.briefReady()
	if !off {
		to, ok := s.Deliver.OwnerHome(ctx)
		off = !ok || to.ChatID == ""
	}
	if off {
		s.log().Warn("cron: the daily status is off: no private chat of the owner or no send function")
	}
}

// startBrief starts sendBrief in its own goroutine under s.wg, unless a check runs already.
// Only the Start loop calls it, so TickOnce and RunNow never send the status.
func (s *Scheduler) startBrief(ctx context.Context) {
	if !s.briefReady() {
		return
	}
	s.briefMu.Lock()
	if s.briefBusy {
		s.briefMu.Unlock()
		return
	}
	s.briefBusy = true
	s.briefMu.Unlock()
	s.wg.Go(func() {
		defer func() {
			s.briefMu.Lock()
			s.briefBusy = false
			s.briefMu.Unlock()
		}()
		defer func() {
			if r := recover(); r != nil {
				s.log().Error("cron: the daily status failed", "panic", r)
			}
		}()
		s.sendBrief(ctx)
	})
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
		s.manual = map[string]bool{}
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
	s.warnBrief(ctx)
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
		s.startBrief(ctx)
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
	s.sweep(ctx)
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

// sweep calls Sweep. A panic in it is logged, so the tick goes on.
func (s *Scheduler) sweep(ctx context.Context) {
	if s.Sweep == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			s.log().Error("cron: the sweep of held sends failed", "panic", r)
		}
	}()
	s.Sweep(ctx)
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
	if !busy && wait <= 0 {
		// Set the flag under the lock of the busy check. A tick that claims the job after this
		// point runs it as the manual run, so the flag cannot stay for a later scheduled run.
		s.manual[id] = true
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
		s.dropManual(id)
		return errors.New("cron: the scheduler is stopped")
	}
	if _, err := s.Store.Update(ctx, id, JobPatch{NextRun: &now}); err != nil {
		s.dropManual(id)
		return err
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.TickOnce(base)
	}()
	return nil
}

// dropManual removes the manual flag of the job id after RunNow fails.
func (s *Scheduler) dropManual(id string) {
	s.mu.Lock()
	delete(s.manual, id)
	s.mu.Unlock()
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
	s.mu.Lock()
	manual := s.manual[j.ID]
	delete(s.manual, j.ID)
	s.mu.Unlock()
	o := s.run(ctx, j, manual)
	status := ""
	defer func() {
		if s.RunEnd != nil {
			s.RunEnd(j.ID, status)
		}
	}()
	// The duration ends before the delivery, so the wait of the Telegram pace budget is not in it.
	finishedAt := s.now()
	// The writes use a context without cancel, so a shutdown still records the run.
	wctx := context.WithoutCancel(ctx)
	if ctx.Err() != nil {
		// The process stops before the run ends. The outcome is not complete, so it is not
		// delivered. MarkStopped records the status stopped, and the cronjob list shows it.
		// The first stop does not use up the job, so a one-shot job runs again after the
		// restart. A stopped run that follows a stopped run counts as a run.
		o.Text = fmt.Sprintf("⚠ Cron job %s stopped: the gateway shut down before the run ended.", j.Name)
		s.log().Warn("cron job stopped by shutdown", "job", j.ID, "name", j.Name)
		status = StatusStopped
		s.noteMark(j.ID, s.Store.MarkStopped(wctx, j.ID, RunRecord{Status: StatusStopped, LastStatus: StatusStopped,
			Delivery: DeliveryNone, Output: o.Text, SessionID: o.SessionID, RanAt: ranAt, FinishedAt: finishedAt}))
		return
	}
	if o.Skipped {
		// A run that skips the model makes no delivery and keeps the fail streak. The store keeps
		// the status of the run before, except after a stop, so the event sends the same status.
		status = j.LastStatus
		if status == StatusStopped {
			status = o.Status
		}
		s.noteMark(j.ID, s.Store.MarkRun(wctx, j.ID, RunRecord{Skipped: true, Status: o.Status,
			LastStatus: o.Status, Delivery: DeliveryNone, Output: o.Text, SessionID: o.SessionID,
			RanAt: ranAt, FinishedAt: finishedAt, Manual: manual, WatchHash: o.WatchHash,
			WatchOutput: o.WatchOutput, RunScript: j.Script, RunWorkdir: j.Workdir}))
		return
	}
	// The pause is decided before the delivery, so the alert can tell the owner.
	n := j.FailStreak + 1
	pausing := s.willPause(j, o)
	if o.Status == "error" && !pausing {
		switch {
		case j.LastErrorKey != "" && o.ErrKey == j.LastErrorKey:
			o.Repeat = true
		case j.LastErrorKey == "":
			o.Text += "\n\n" + firstAlertLine
		}
	}
	runText := o.Text
	if pausing {
		o.Pause = true
		o.Text += "\n\n" + pauseLine(j, n)
	}
	if o.Status == "ok" && j.FailStreak > 0 {
		o.RecoveredLine = fmt.Sprintf("✅ Cron job %s works again after %d failed runs.", j.Name, j.FailStreak)
	}
	o.NoSound = s.Quiet != nil && s.Quiet(finishedAt)
	// The cost read runs before MarkRun, so the median does not include this run.
	var costFlag bool
	var costMedian *float64
	if o.CostUSD != nil {
		if m, ok := s.costMedian(ctx, j.ID); ok && *o.CostUSD > 3*m {
			costFlag, costMedian = true, &m
			o.CostLine = fmt.Sprintf("💸 This run cost $%.2f, %.1f× the usual $%.2f of this job.", *o.CostUSD, *o.CostUSD/m, m)
		}
	}
	delivery, lastStatus, deliveryErr := DeliveryNone, o.Status, ""
	if s.Deliver != nil {
		var err error
		if delivery, err = s.Deliver.Deliver(ctx, j, o); err != nil {
			lastStatus, deliveryErr = "delivery_failed", err.Error()
		}
	}
	status = lastStatus
	// A pause alert that did not go out does not pause the job. The streak goes on, and the
	// next failed run sends the alert and pauses again.
	pause := o.Pause && delivery != DeliveryFailed
	rec := RunRecord{Status: o.Status, LastStatus: lastStatus, Delivery: delivery, DeliveryErr: deliveryErr,
		Output: o.Text, SessionID: o.SessionID, RanAt: ranAt, FinishedAt: finishedAt, Pause: pause,
		ErrKey: o.ErrKey, AlertSent: delivery == DeliverySent || delivery == DeliveryLocal,
		Manual: manual, WatchHash: o.WatchHash, WatchOutput: o.WatchOutput, RunScript: j.Script,
		RunWorkdir: j.Workdir, CostUSD: o.CostUSD, CostMedian: costMedian, CostFlag: costFlag}
	if o.Pause && !pause {
		rec.Output = runText
	}
	if pause {
		rec.Streak = n
		s.log().Warn("cron job paused after failed runs in a row", "job", j.ID, "name", j.Name, "failures", n)
	}
	s.noteMark(j.ID, s.Store.MarkRun(wctx, j.ID, rec))
}

// costMedian returns the median cost of the last 10 priced runs of the job. ok is false
// with fewer than 3 priced runs, a median of 0 or less, or a read error.
func (s *Scheduler) costMedian(ctx context.Context, jobID string) (m float64, ok bool) {
	costs, err := s.Store.PricedCosts(ctx, jobID, 10)
	if err != nil {
		s.log().Warn("cron: cannot read the run costs of the job", "job", jobID, "err", err)
		return 0, false
	}
	if len(costs) < 3 {
		return 0, false
	}
	c := append([]float64(nil), costs...)
	sort.Float64s(c)
	if n := len(c); n%2 == 1 {
		m = c[n/2]
	} else {
		m = (c[n/2-1] + c[n/2]) / 2
	}
	return m, m > 0
}

// firstAlertLine ends the first alert of a streak. The next alerts with the same error key are not sent.
const firstAlertLine = "I send no new alert while the job fails with the same error."

// willPause reports whether the outcome o of the job j pauses the job: the run failed, and
// the failures in a row reach Store.FailStreakLimit. A one-shot job stops after this run, so
// it does not pause. A job on its last repeat does not pause, unless it is a watch job,
// because a failed watch run does not use a repeat.
func (s *Scheduler) willPause(j Job, o RunOutcome) bool {
	if o.Status != "error" || s.Store.FailStreakLimit <= 0 || j.FailStreak+1 < s.Store.FailStreakLimit || (j.RepeatLeft == 1 && !j.Watch) {
		return false
	}
	sch, err := parse(j.Schedule, s.Store.loc())
	return err == nil && !sch.OneShot()
}

// pauseLine returns the paragraph that tells the owner that the job j paused after n
// failures in a row.
func pauseLine(j Job, n int) string {
	return fmt.Sprintf("⏸ The job %s (%s) is paused: it failed %d runs in a row. Fix the cause. Then ask me to resume the job.",
		j.Name, j.ID, n)
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
func (s *Scheduler) run(ctx context.Context, j Job, manual bool) (o RunOutcome) {
	defer func() {
		if r := recover(); r != nil {
			s.log().Error("cron job panic", "job", j.ID, "panic", fmt.Sprint(r))
			o = RunOutcome{Text: fmt.Sprintf("⚠ Cron job %s failed: internal error", j.Name), Send: true, Status: "error",
				ErrKey: "internal error", Manual: manual}
		}
	}()
	if s.Runner == nil {
		return RunOutcome{Text: fmt.Sprintf("⚠ Cron job %s failed: no runner is set", j.Name), Send: true, Status: "error",
			ErrKey: "no runner", Manual: manual}
	}
	return s.Runner.Run(ctx, j, manual)
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
