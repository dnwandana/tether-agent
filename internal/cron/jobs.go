package cron

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
)

// ErrNotFound is the error for a job id that is not in cron_jobs.
var ErrNotFound = errors.New("cron: job not found. Use list to see the job ids")

// The values of Job.PauseReason. They tell why the scheduler disabled a job. "" is a job
// that the scheduler did not pause.
const (
	PauseFailures = "failures" // the fail streak reached cron.fail_streak_limit
	PauseSchedule = "schedule" // the stored schedule is not valid
)

// ErrDraftDeliver is the error for a draft-only job whose delivery target is not private.
//
//lint:ignore ST1005 the text is a fixed user message with a full sentence.
var ErrDraftDeliver = errors.New("A draft-only job delivers only to a private chat or local.")

// errBadSchedule is the error of MarkRun for a stored schedule that cannot be parsed. MarkRun
// then disables the job.
var errBadSchedule = errors.New("cron: the stored schedule is not valid")

// StatusStopped is the LastStatus of a run that a shutdown stopped before the run ended.
const StatusStopped = "stopped"

// stoppedTwiceLine is the output line of a job that a second stop in a row used up. The
// gateway is in its shutdown and sends no alert, so the cronjob list shows the line.
const stoppedTwiceLine = "The job does not run again: two shutdowns in a row stopped it."

// RunRecord is the result of one run that the store writes.
type RunRecord struct {
	Status      string // the run status: ok | error | stopped
	LastStatus  string // the cron_jobs.last_status value: Status, or delivery_failed
	Delivery    string // sent | silent | none | local | failed
	DeliveryErr string
	Output      string // the run text (o.Text), before redaction
	SessionID   string
	RanAt       time.Time
	FinishedAt  time.Time
	Pause       bool // the scheduler decided to pause the job
	Streak      int  // with Pause: the N of the pause line, written as fail_streak
	// Skipped is true for a run that did not wake the model. MarkRun then moves only last_run
	// and next_run, and writes no cron_runs row.
	Skipped bool
	// Manual is true for a run that the owner started with Run now.
	Manual bool
	// ErrKey is the key of the error of a failed run. Two failures with the same key are a repeat.
	ErrKey string
	// AlertSent is true when the failure alert of the run went out (delivery sent or local).
	// Only then does MarkRun write last_error and last_error_key.
	AlertSent bool
	// WatchHash and WatchOutput are the script output of a watch run: the SHA-256 of the raw
	// stdout, and the stdout cut at WatchOutputMaxRunes. MarkRun can save them as the baseline.
	WatchHash, WatchOutput string
	// RunScript and RunWorkdir are the script and the workdir that the run used. MarkRun saves
	// the baseline only when the job row still has the same values.
	RunScript, RunWorkdir string
	// CostUSD is the cost of the run. Nil means no priced call. CostMedian is the median of the
	// job when CostFlag is true.
	CostUSD, CostMedian *float64
	// CostFlag is true when the run cost more than 3 times the median of the job.
	CostFlag bool
}

// RunOutputMaxRunes is the length limit of cron_runs.output, in runes.
const RunOutputMaxRunes = 4000

// TruncateRunes returns the first n runes of s. It adds "…" when the cut removes text.
func TruncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// ModelPin sets the provider and the model for the runs of one job.
type ModelPin struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// Origin is the chat and topic where a job was created. Delivery "origin" sends to it.
type Origin struct {
	Source     string `json:"source,omitempty"`
	ChatID     string `json:"chat_id,omitempty"`
	ThreadID   string `json:"thread_id,omitempty"`
	SessionKey string `json:"session_key,omitempty"`
}

// Job is one row of cron_jobs.
type Job struct {
	ID, Name, Schedule, Prompt, Script string
	NoAgent                            bool
	// Deliver is "", "origin", "local", or "telegram:<chat>[:<thread>]". "" means "origin".
	Deliver  string
	Skills   []string
	Model    *ModelPin
	Toolsets []string
	Workdir  string
	// RepeatLeft is the number of runs that remain. -1 means forever.
	RepeatLeft       int
	Enabled          bool
	Origin           Origin
	CreatedBySession string
	LastRun, NextRun time.Time
	LastStatus       string
	LastOutput       string
	// FailStreak is the number of runs in a row with status error.
	FailStreak int
	// DraftOnly is true for a job that can read and write drafts, but cannot send.
	DraftOnly bool
	// LastError is the redacted text of the last failure alert that went out. LastErrorKey is
	// its key. A failure with the same key sends no new alert.
	LastError, LastErrorKey string
	// PauseReason is "", PauseFailures, or PauseSchedule.
	PauseReason string
	// Watch is true for a job that wakes the model only when the script output changes.
	Watch bool
	// WatchHash and WatchOutput are the baseline of a watch job. "" means no baseline (NULL).
	WatchHash, WatchOutput string
	// Continuity is true for a job whose prompt gets the output of its last delivered run.
	Continuity bool
	// Effort is the reasoning effort of the runs. "" uses the config.
	Effort string
}

// JobPatch holds the fields that Update changes. A nil field stays the same.
type JobPatch struct {
	Name, Schedule, Prompt, Script *string
	NoAgent                        *bool
	Deliver                        *string
	// An empty slice clears the field (Hermes).
	Skills   *[]string
	Toolsets *[]string
	// A zero ModelPin clears the field.
	Model      *ModelPin
	Workdir    *string
	RepeatLeft *int
	Enabled    *bool
	// NextRun moves the next run. The cronjob tool uses it to make a job due now.
	NextRun           *time.Time
	DraftOnly         *bool
	Watch, Continuity *bool
	Effort            *string
}

// Store reads and writes cron_jobs. It does no I/O except the database and the workdir
// check, so a job that a chat process creates is visible to the gateway at its next tick.
type Store struct {
	DB *db.DB
	// Loc is the time zone of the schedules. Nil means UTC.
	Loc *time.Location
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time
	// ValidToolset reports whether a toolset name is valid. Nil accepts all names.
	ValidToolset func(string) bool
	// RunHistory is cron.run_history: the runs that cron_runs keeps for each job. 0 keeps none.
	RunHistory int
	// FailStreakLimit is cron.fail_streak_limit: the failures in a row that pause a job. 0 is off.
	FailStreakLimit int
	// Redact removes secrets from the output of a run before cron_runs keeps it. The store
	// masks before it cuts, because a cut can split a secret that the redactor then misses.
	// Nil keeps the text as it is.
	Redact func(string) string
	// IsPrivate reports whether a target stays with the owner. Nil refuses each draft-only
	// target that is not local.
	IsPrivate func(ctx context.Context, t Target) bool
}

const jobColumns = `id, name, schedule, prompt, script, no_agent, deliver, skills, model, toolsets, workdir,
	repeat_left, enabled, origin, created_by_session, last_run, next_run, last_status, last_output, fail_streak, draft_only,
	last_error, last_error_key, pause_reason, watch, watch_hash, watch_output, continuity, effort`

var (
	telegramChatRE   = regexp.MustCompile(`^(-?\d+|@[A-Za-z0-9_]+)$`)
	telegramThreadRE = regexp.MustCompile(`^\d+$`)
)

func (s *Store) redact(text string) string {
	if s.Redact == nil {
		return text
	}
	return s.Redact(text)
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) loc() *time.Location {
	if s.Loc != nil {
		return s.Loc
	}
	return time.UTC
}

// Create validates j and stores it with a new id. It computes NextRun from the schedule.
// A RepeatLeft of 0 or less means the default: 1 for a one-shot job and -1 (forever) for
// other jobs.
func (s *Store) Create(ctx context.Context, j Job) (Job, error) {
	now := s.now()
	j = normalize(j)
	if err := s.validate(ctx, j); err != nil {
		return Job{}, err
	}
	sched, err := Parse(j.Schedule, now, s.loc())
	if err != nil {
		return Job{}, err
	}
	j.Schedule = sched.String()
	if j.RepeatLeft <= 0 {
		j.RepeatLeft = defaultRepeat(sched)
	}
	if j.Name == "" {
		j.Name = defaultName(j)
	}
	j.NextRun = sched.Next(now)
	j.LastRun, j.LastStatus, j.LastOutput = time.Time{}, "", ""
	j.FailStreak = 0
	j.LastError, j.LastErrorKey, j.PauseReason, j.WatchHash, j.WatchOutput = "", "", "", "", ""
	for range 5 {
		j.ID, err = newID()
		if err != nil {
			return Job{}, err
		}
		err = s.insert(ctx, j, now)
		if err == nil {
			return s.Get(ctx, j.ID)
		}
		if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return Job{}, fmt.Errorf("cron: create job: %w", err)
		}
	}
	return Job{}, fmt.Errorf("cron: create job: %w", err)
}

func (s *Store) insert(ctx context.Context, j Job, now time.Time) error {
	args, err := rowArgs(j)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO cron_jobs(`+jobColumns+`, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, append(args, now.UnixMilli())...)
	return err
}

// updateTries is the number of times that Update reads the job again when the draft target
// of the job changes between the check and the transaction.
const updateTries = 3

// errDraftTargetChanged tells Update that another writer changed the draft target of the job.
var errDraftTargetChanged = errors.New("cron: the delivery target of the job changed during the update")

// draftKey holds the fields of a job that checkDraftTarget reads.
type draftKey struct {
	draftOnly, noAgent bool
	deliver            string
	origin             Origin
}

func draftKeyOf(j Job) draftKey {
	return draftKey{draftOnly: j.DraftOnly, noAgent: j.NoAgent, deliver: j.Deliver, origin: j.Origin}
}

// Update applies patch to the job id and validates the result. A new schedule gives a new
// NextRun. A one-shot time in the past is refused.
//
// IsPrivate can query state.db, and state.db has one connection. Thus Update validates the
// patched job before the transaction starts. In the transaction, it validates again without
// the draft target check, and it starts again when the draft target changed after the check.
func (s *Store) Update(ctx context.Context, id string, patch JobPatch) (Job, error) {
	for range updateTries {
		checked, err := s.get(ctx, s.DB, id)
		if err != nil {
			return Job{}, err
		}
		applyPatch(&checked, patch)
		checked = normalize(checked)
		if err := s.validate(ctx, checked); err != nil {
			return Job{}, err
		}
		err = s.update(ctx, id, patch, draftKeyOf(checked))
		if errors.Is(err, errDraftTargetChanged) {
			continue
		}
		if err != nil {
			return Job{}, err
		}
		return s.Get(ctx, id)
	}
	return Job{}, fmt.Errorf("%w: job %s, try again", errDraftTargetChanged, id)
}

// update writes patch to the job id in one transaction. checked is the draft key of the job
// that Update validated before the transaction.
func (s *Store) update(ctx context.Context, id string, patch JobPatch, checked draftKey) error {
	now := s.now()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		j, err := s.get(ctx, tx, id)
		if err != nil {
			return err
		}
		old := j
		applyPatch(&j, patch)
		j = normalize(j)
		if err := s.validateFields(j); err != nil {
			return err
		}
		if j.DraftOnly && draftKeyOf(j) != checked {
			return errDraftTargetChanged
		}
		if j.Schedule != old.Schedule {
			sched, err := Parse(j.Schedule, now, s.loc())
			if err != nil {
				return err
			}
			j.Schedule = sched.String()
			j.NextRun = sched.Next(now)
			oldSched, oerr := parse(old.Schedule, s.loc())
			if patch.RepeatLeft == nil && (oerr != nil || oldSched.OneShot() != sched.OneShot()) {
				j.RepeatLeft = defaultRepeat(sched)
			}
		}
		if taskChanged(old, j) {
			j.FailStreak = 0
			j.LastError, j.LastErrorKey, j.PauseReason = "", "", ""
		}
		// A new script, workdir, watch flag, or no_agent flag makes the old baseline wrong.
		if j.Script != old.Script || j.Workdir != old.Workdir || j.Watch != old.Watch || j.NoAgent != old.NoAgent {
			j.WatchHash, j.WatchOutput = "", ""
		}
		// An owner pause is not a pause of the scheduler.
		if patch.Enabled != nil && !*patch.Enabled {
			j.PauseReason = ""
		}
		if patch.RepeatLeft != nil && j.RepeatLeft <= 0 {
			j.RepeatLeft = -1
		}
		if j.Enabled && !old.Enabled {
			if err := s.prepareResume(&j, now); err != nil {
				return err
			}
		}
		if patch.NextRun != nil {
			j.NextRun = *patch.NextRun
		}
		// An owner who edits the job gets the full one rerun after a stop again.
		if j.LastStatus == StatusStopped {
			j.LastStatus = ""
		}
		return s.write(ctx, tx, j)
	})
}

// Get returns the job id, or an error that wraps ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (Job, error) {
	return s.get(ctx, s.DB, id)
}

// List returns all jobs in the order of creation.
func (s *Store) List(ctx context.Context) ([]Job, error) {
	return s.query(ctx, `SELECT `+jobColumns+` FROM cron_jobs ORDER BY created_at, id`)
}

// SetEnabled pauses or resumes the job id. A resume moves a next run that is in the past to
// the first run after now. A one-shot job whose time is in the past cannot resume.
func (s *Store) SetEnabled(ctx context.Context, id string, on bool) error {
	now := s.now()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		j, err := s.get(ctx, tx, id)
		if err != nil {
			return err
		}
		if on && !j.Enabled {
			if err := s.prepareResume(&j, now); err != nil {
				return err
			}
		}
		if !on {
			j.PauseReason = ""
		}
		j.Enabled = on
		return s.write(ctx, tx, j)
	})
}

// prepareResume makes a disabled job ready to run again.
func (s *Store) prepareResume(j *Job, now time.Time) error {
	sched, err := parse(j.Schedule, s.loc())
	if err != nil {
		return err
	}
	// A resume gives the full one rerun after a stop again.
	if j.LastStatus == StatusStopped {
		j.LastStatus = ""
	}
	j.FailStreak = 0
	j.LastError, j.LastErrorKey, j.PauseReason = "", "", ""
	if j.RepeatLeft == 0 {
		j.RepeatLeft = defaultRepeat(sched)
	}
	if j.NextRun.IsZero() || j.NextRun.Before(now) {
		j.NextRun = sched.Next(now)
	}
	if j.NextRun.IsZero() {
		return fmt.Errorf("cron: job %s cannot resume: its one-shot time %s is in the past. Update the schedule first",
			j.ID, sched.String())
	}
	return nil
}

// Remove deletes the job id and its runs. When the job exists, it also deletes the runs of
// jobs that do not exist.
func (s *Store) Remove(ctx context.Context, id string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM cron_runs WHERE job_id=?`, id); err != nil {
			return fmt.Errorf("cron: remove job %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM held_sends WHERE job_id=?`, id); err != nil {
			return fmt.Errorf("cron: remove job %s: %w", id, err)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM cron_jobs WHERE id=?`, id)
		if err != nil {
			return fmt.Errorf("cron: remove job %s: %w", id, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM cron_runs WHERE job_id NOT IN (SELECT id FROM cron_jobs)`); err != nil {
			return fmt.Errorf("cron: remove job %s: %w", id, err)
		}
		return nil
	})
}

// ErrRunNotFound is the error for a run id that is not in cron_runs for the job.
var ErrRunNotFound = errors.New("cron: run not found. Use action='runs' with the job_id to see the run ids")

// Run is one row of cron_runs.
type Run struct {
	ID                             int64
	JobID                          string
	StartedAt, FinishedAt          time.Time
	Status, Delivery               string
	Output, DeliveryErr, SessionID string
	// CostUSD and CostMedian are nil when the column is NULL.
	CostUSD, CostMedian *float64
	CostFlag            bool
}

const runColumns = "id, job_id, started_at, finished_at, status, delivery, output, delivery_error, session_id, cost_usd, cost_median, cost_flag"

func (s *Store) scanRun(r scanner) (Run, error) {
	var run Run
	var started, finished int64
	var cost, median sql.NullFloat64
	var flag int
	if err := r.Scan(&run.ID, &run.JobID, &started, &finished, &run.Status, &run.Delivery,
		&run.Output, &run.DeliveryErr, &run.SessionID, &cost, &median, &flag); err != nil {
		return Run{}, err
	}
	if cost.Valid {
		run.CostUSD = &cost.Float64
	}
	if median.Valid {
		run.CostMedian = &median.Float64
	}
	run.CostFlag = flag != 0
	run.StartedAt = time.UnixMilli(started).In(s.loc())
	run.FinishedAt = time.UnixMilli(finished).In(s.loc())
	return run, nil
}

// Runs returns limit runs of the job id after the newest offset runs, newest first.
func (s *Store) Runs(ctx context.Context, jobID string, limit, offset int) ([]Run, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+runColumns+` FROM cron_runs WHERE job_id=?
		ORDER BY id DESC LIMIT ? OFFSET ?`, jobID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("cron: read runs of job %s: %w", jobID, err)
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		run, err := s.scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("cron: read runs of job %s: %w", jobID, err)
		}
		out = append(out, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cron: read runs of job %s: %w", jobID, err)
	}
	return out, nil
}

// Run returns the run id of the job jobID, or an error that wraps ErrRunNotFound.
func (s *Store) Run(ctx context.Context, jobID string, id int64) (Run, error) {
	run, err := s.scanRun(s.DB.QueryRowContext(ctx, `SELECT `+runColumns+` FROM cron_runs
		WHERE job_id=? AND id=?`, jobID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, fmt.Errorf("%w: job %s, run %d", ErrRunNotFound, jobID, id)
	}
	if err != nil {
		return Run{}, fmt.Errorf("cron: read runs of job %s: %w", jobID, err)
	}
	return run, nil
}

// Due returns the enabled jobs with NextRun at or before now, in NextRun order.
func (s *Store) Due(ctx context.Context, now time.Time) ([]Job, error) {
	return s.query(ctx, `SELECT `+jobColumns+` FROM cron_jobs
		WHERE enabled=1 AND next_run IS NOT NULL AND next_run<=? ORDER BY next_run, id`, now.UnixMilli())
}

// MarkRun records the run rec. It computes NextRun from the schedule, but never earlier
// than now. It decrements RepeatLeft, and it disables the job when RepeatLeft reaches 0 or
// when the schedule has no further run. It updates FailStreak from rec.Status: an error adds
// 1, and ok sets 0. With rec.Pause, it disables the job after the repeat step and writes
// rec.Streak as the count. In the same transaction, it adds a cron_runs row and deletes the
// rows of the job above RunHistory. When the stored schedule cannot be parsed, MarkRun
// disables the job, writes the cause as the last output, and returns an error that wraps
// errBadSchedule and sets PauseReason to PauseSchedule.
//
// A rec with Skipped changes only last_run, next_run, and enabled (when the schedule ends). It
// writes no cron_runs row. An error with AlertSent writes last_error and last_error_key. An error
// with no alert keeps them, and an ok run that is not skipped clears them. With rec.Pause, MarkRun
// sets PauseReason to PauseFailures.
func (s *Store) MarkRun(ctx context.Context, id string, rec RunRecord) error {
	return s.markRun(ctx, id, rec, false)
}

// MarkStopped records a run that a shutdown stopped before it ended. It always writes
// StatusStopped as the status and DeliveryNone as the delivery, and it never pauses the job.
// A recurring job with more repeats left moves to its next run, and RepeatLeft stays the
// same. A one-shot job, or a job on its last repeat, stays due now, so it runs again after
// the restart. A stopped run that follows a stopped run counts as a run: the job is used up,
// and the output gets a line that tells why. The fail streak does not change.
func (s *Store) MarkStopped(ctx context.Context, id string, rec RunRecord) error {
	rec.Status, rec.LastStatus, rec.Delivery, rec.Pause = StatusStopped, StatusStopped, DeliveryNone, false
	return s.markRun(ctx, id, rec, true)
}

func (s *Store) markRun(ctx context.Context, id string, rec RunRecord, stopped bool) error {
	now := s.now()
	var bad error
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		j, err := s.get(ctx, tx, id)
		if err != nil {
			return err
		}
		prev := j.LastStatus
		if newBaseline(j, rec) {
			j.WatchHash, j.WatchOutput = rec.WatchHash, rec.WatchOutput
		}
		j.LastRun = rec.RanAt
		if !rec.Skipped { // a skipped run keeps the output, the failure state, and (after a run) the status
			j.LastStatus, j.LastOutput = rec.LastStatus, rec.Output
			switch rec.Status { // the count uses only the run status
			case "error":
				j.FailStreak++
				if rec.AlertSent {
					j.LastError = TruncateRunes(s.redact(rec.Output), RunOutputMaxRunes)
					j.LastErrorKey = rec.ErrKey
				}
			case "ok":
				j.FailStreak = 0
				j.LastError, j.LastErrorKey = "", ""
			}
		}
		sched, err := parse(j.Schedule, s.loc())
		if err != nil {
			// The job has no next run. Disable it, so it does not run on each tick.
			bad = fmt.Errorf("%w: job %s, schedule %q: %v", errBadSchedule, id, j.Schedule, err)
			j.Enabled, j.NextRun, j.LastStatus = false, time.Time{}, "error"
			j.PauseReason = PauseSchedule
			j.LastOutput = fmt.Sprintf("⚠ Cron job %s is paused: its stored schedule %q is not valid. "+
				"Update the schedule, then resume the job.", j.Name, j.Schedule)
			return s.record(ctx, tx, j, rec)
		}
		next := sched.Next(rec.RanAt)
		if !next.IsZero() && !next.After(now) {
			next = sched.Next(now)
		}
		j.NextRun = next
		if rec.Skipped {
			if prev == StatusStopped {
				// The script ran to its end, so the skip ends the chain of stops in a row.
				j.LastStatus = rec.LastStatus
			}
			if next.IsZero() { // the schedule has no further run
				j.Enabled, j.NextRun = false, time.Time{}
			}
			return s.record(ctx, tx, j, rec)
		}
		if stopped {
			if !next.IsZero() && j.RepeatLeft != 1 {
				// A recurring job with repeats left moves to its next run. The stop does not count.
				return s.record(ctx, tx, j, rec)
			}
			if prev != StatusStopped {
				// The first stop keeps the job due now, so it runs again after the restart.
				j.NextRun = now
				return s.record(ctx, tx, j, rec)
			}
			// A second stop in a row counts as the run, so a job that restarts the gateway
			// cannot run with no end.
		}
		// A watch job counts only the delivered model runs (a one-time watch is a wait). A stopped
		// run here is the second stop in a row, which counts for each job.
		counts := !j.Watch || stopped || (deliveredModelRun(rec) && !rec.Manual)
		if counts && j.RepeatLeft > 0 {
			j.RepeatLeft--
		}
		if j.RepeatLeft == 0 || next.IsZero() {
			j.Enabled = false
			j.NextRun = time.Time{}
			if stopped {
				j.LastOutput = rec.Output + "\n" + stoppedTwiceLine
			}
		}
		if rec.Pause {
			j.Enabled, j.NextRun, j.FailStreak = false, time.Time{}, rec.Streak
			j.PauseReason = PauseFailures
		}
		return s.record(ctx, tx, j, rec)
	})
	if err != nil {
		return err
	}
	return bad
}

// deliveredModelRun reports whether rec woke the model, ended ok, and was delivered. Only
// such a run moves the baseline of a watch job and counts toward its repeat.
func deliveredModelRun(rec RunRecord) bool {
	if rec.Skipped || rec.Status != "ok" {
		return false
	}
	switch rec.Delivery {
	case DeliverySent, DeliveryLocal, DeliverySilent:
		return true
	}
	return false
}

// newBaseline reports whether rec gives the watch job j a new baseline. A failed, held,
// or manual run keeps the old baseline, so the next run sees the change again. An edit of
// the script or the workdir during the run also keeps it.
func newBaseline(j Job, rec RunRecord) bool {
	if !j.Watch || rec.Manual || rec.WatchHash == "" || j.Script != rec.RunScript || j.Workdir != rec.RunWorkdir {
		return false
	}
	if rec.Skipped {
		return j.WatchHash == "" // the baseline run
	}
	return deliveredModelRun(rec)
}

// record writes the job row, adds the cron_runs row of the run, and deletes the rows of the
// job above RunHistory. With RunHistory 0, it deletes all rows of the job.
func (s *Store) record(ctx context.Context, tx *sql.Tx, j Job, rec RunRecord) error {
	if rec.Skipped { // a skipped run is not part of the run history
		return s.write(ctx, tx, j)
	}
	if err := s.write(ctx, tx, j); err != nil {
		return err
	}
	keep := max(s.RunHistory, 0) // SQLite reads a negative LIMIT as no limit
	if keep > 0 {
		res, err := tx.ExecContext(ctx, `INSERT INTO cron_runs(job_id, started_at, finished_at, status,
			delivery, output, delivery_error, session_id, cost_usd, cost_median, cost_flag)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			j.ID, rec.RanAt.UnixMilli(), rec.FinishedAt.UnixMilli(), rec.Status, rec.Delivery,
			TruncateRunes(s.redact(j.LastOutput), RunOutputMaxRunes), rec.DeliveryErr, rec.SessionID,
			floatOrNil(rec.CostUSD), floatOrNil(rec.CostMedian), boolInt(rec.CostFlag))
		if err != nil {
			return fmt.Errorf("cron: record run of job %s: %w", j.ID, err)
		}
		if rec.Delivery == DeliveryHeld {
			if err := s.linkHeld(ctx, tx, j.ID, res); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM cron_runs WHERE job_id=? AND id NOT IN
		(SELECT id FROM cron_runs WHERE job_id=? ORDER BY id DESC LIMIT ?)`, j.ID, j.ID, keep); err != nil {
		return fmt.Errorf("cron: record run of job %s: %w", j.ID, err)
	}
	return nil
}

// linkHeld writes the id of the new run row res into the hold rows of the job. A job never
// runs two times at once, so each hold row without a run id is of this run.
//
// The hold row and its card exist before the run row. A fast tap can claim the row first, and
// then no row gets the run id, and no tap or sweep can change the run row later. The store
// cannot know if the tap sent or dropped the text, so the run row gets DeliveryNone, not
// DeliveryHeld.
func (s *Store) linkHeld(ctx context.Context, tx *sql.Tx, jobID string, res sql.Result) error {
	runID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("cron: record run of job %s: %w", jobID, err)
	}
	link, err := tx.ExecContext(ctx, `UPDATE held_sends SET run_id = ? WHERE job_id = ? AND kind = 'hold' AND run_id IS NULL`,
		runID, jobID)
	if err != nil {
		return fmt.Errorf("cron: link held send of job %s: %w", jobID, err)
	}
	n, err := link.RowsAffected()
	if err != nil {
		return fmt.Errorf("cron: link held send of job %s: %w", jobID, err)
	}
	if n > 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE cron_runs SET delivery = ? WHERE id = ?`, DeliveryNone, runID); err != nil {
		return fmt.Errorf("cron: record run of job %s: %w", jobID, err)
	}
	return nil
}

// UpdateRunDelivery sets the delivery of the cron_runs row runID. A tap and the sweep of a
// held send call it. A row that the history limit deleted is no error.
func (s *Store) UpdateRunDelivery(ctx context.Context, runID int64, delivery string) error {
	if runID == 0 {
		return nil
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE cron_runs SET delivery=? WHERE id=?`, delivery, runID); err != nil {
		return fmt.Errorf("cron: update delivery of run %d: %w", runID, err)
	}
	return nil
}

// validate checks a job before the store saves it. It checks the fields of validateFields and
// the draft-only rules. Do not call it in a transaction: IsPrivate can query state.db, and
// state.db has one connection.
func (s *Store) validate(ctx context.Context, j Job) error {
	if err := s.validateFields(j); err != nil {
		return err
	}
	if j.DraftOnly {
		return s.checkDraftTarget(ctx, j)
	}
	return nil
}

// validateFields checks the schedule, the prompt or the script, the workdir, the toolsets,
// the skills, and the deliver value. It does no database query.
func (s *Store) validateFields(j Job) error {
	if j.Schedule == "" {
		return fmt.Errorf("cron: schedule is required: %s", formatHelp)
	}
	if j.NoAgent && j.Script == "" {
		return errors.New("cron: a no_agent job needs a script")
	}
	if !j.NoAgent && j.Prompt == "" {
		return errors.New("cron: a job needs a prompt, or no_agent with a script")
	}
	if j.Watch {
		if j.Script == "" {
			return errors.New("cron: watch needs a script")
		}
		if j.NoAgent {
			return errors.New("cron: watch cannot be used with no_agent")
		}
		// A bad schedule fails later in Parse with its own error.
		if sched, err := parse(j.Schedule, s.loc()); err == nil && sched.OneShot() {
			return errors.New("cron: watch needs a recurring schedule")
		}
	}
	if j.Effort != "" && !slices.Contains(config.EffortLevels, j.Effort) {
		return fmt.Errorf("cron: effort %q is not valid. Use one of: %s", j.Effort, strings.Join(config.EffortLevels, ", "))
	}
	if j.Workdir != "" {
		if !filepath.IsAbs(j.Workdir) {
			return fmt.Errorf("cron: workdir %q must be an absolute path", j.Workdir)
		}
		fi, err := os.Stat(j.Workdir)
		if err != nil {
			return fmt.Errorf("cron: workdir %q does not exist", j.Workdir)
		}
		if !fi.IsDir() {
			return fmt.Errorf("cron: workdir %q is not a folder", j.Workdir)
		}
	}
	for _, ts := range j.Toolsets {
		if ts == "" || (s.ValidToolset != nil && !s.ValidToolset(ts)) {
			return fmt.Errorf("cron: unknown toolset %q", ts)
		}
	}
	for _, sk := range j.Skills {
		if sk == "" {
			return errors.New("cron: a skill name must not be empty")
		}
	}
	return validateDeliver(j.Deliver)
}

// checkDraftTarget refuses a draft-only job whose target is not private.
func (s *Store) checkDraftTarget(ctx context.Context, j Job) error {
	if j.NoAgent {
		return errors.New("cron: a no_agent job cannot be draft-only, because it has no tools to remove")
	}
	t, local, err := resolveTarget(ctx, j, nil)
	switch {
	case err == nil && local:
		return nil
	case errors.Is(err, ErrNoTarget):
		// The run checks the real home target again.
		t = Target{Source: j.Origin.Source}
	case err != nil:
		return err
	}
	if s.IsPrivate == nil || !s.IsPrivate(ctx, t) {
		return fmt.Errorf("cron: %w", ErrDraftDeliver)
	}
	return nil
}

func validateDeliver(d string) error {
	switch d {
	case "", "origin", "local":
		return nil
	}
	if rest, ok := strings.CutPrefix(d, "telegram:"); ok {
		chat, thread, hasThread := strings.Cut(rest, ":")
		if telegramChatRE.MatchString(chat) && (!hasThread || telegramThreadRE.MatchString(thread)) {
			return nil
		}
	}
	return fmt.Errorf("cron: deliver %q is not valid: use origin, local, or telegram:<chat_id>[:<thread_id>]", d)
}

// normalize trims the text fields and the list entries. validate then refuses an empty
// list entry.
func normalize(j Job) Job {
	j.Name = strings.TrimSpace(j.Name)
	j.Schedule = strings.TrimSpace(j.Schedule)
	j.Script = strings.TrimSpace(j.Script)
	j.Deliver = strings.TrimSpace(j.Deliver)
	j.Workdir = strings.TrimSpace(j.Workdir)
	j.Effort = strings.TrimSpace(j.Effort)
	if strings.TrimSpace(j.Prompt) == "" {
		j.Prompt = ""
	}
	j.Skills = trimList(j.Skills)
	j.Toolsets = trimList(j.Toolsets)
	if j.Model != nil && *j.Model == (ModelPin{}) {
		j.Model = nil
	}
	return j
}

func trimList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = strings.TrimSpace(v)
	}
	return out
}

func applyPatch(j *Job, p JobPatch) {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&j.Name, p.Name)
	set(&j.Schedule, p.Schedule)
	set(&j.Prompt, p.Prompt)
	set(&j.Script, p.Script)
	set(&j.Deliver, p.Deliver)
	set(&j.Workdir, p.Workdir)
	if p.NoAgent != nil {
		j.NoAgent = *p.NoAgent
	}
	if p.Skills != nil {
		j.Skills = append([]string(nil), (*p.Skills)...)
	}
	if p.Toolsets != nil {
		j.Toolsets = append([]string(nil), (*p.Toolsets)...)
	}
	if p.Model != nil {
		m := *p.Model
		j.Model = &m
	}
	if p.RepeatLeft != nil {
		j.RepeatLeft = *p.RepeatLeft
	}
	if p.Enabled != nil {
		j.Enabled = *p.Enabled
	}
	if p.DraftOnly != nil {
		j.DraftOnly = *p.DraftOnly
	}
	if p.Watch != nil {
		j.Watch = *p.Watch
	}
	if p.Continuity != nil {
		j.Continuity = *p.Continuity
	}
	set(&j.Effort, p.Effort)
}

// taskChanged reports whether the edit changes what the job does. Such an edit starts a new
// count of failures in a row. The name, the target, and the repeat count are not part of the task.
func taskChanged(old, j Job) bool {
	samePin := func(a, b *ModelPin) bool {
		if a == nil || b == nil {
			return a == b
		}
		return *a == *b
	}
	return j.Prompt != old.Prompt || j.Script != old.Script || j.NoAgent != old.NoAgent ||
		!slices.Equal(j.Skills, old.Skills) || !samePin(j.Model, old.Model) ||
		!slices.Equal(j.Toolsets, old.Toolsets) || j.Workdir != old.Workdir || j.Schedule != old.Schedule ||
		j.DraftOnly != old.DraftOnly || j.Effort != old.Effort
}

func defaultRepeat(s Schedule) int {
	if s.OneShot() {
		return 1
	}
	return -1
}

// defaultName gives a job without a name the start of its prompt, or its script name (Hermes).
func defaultName(j Job) string {
	src := j.Prompt
	if j.NoAgent || src == "" {
		src = filepath.Base(j.Script)
	}
	src = strings.Join(strings.Fields(src), " ")
	if utf8.RuneCountInString(src) > 50 {
		src = string([]rune(src)[:50])
	}
	return src
}

func newID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("cron: make job id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) get(ctx context.Context, q querier, id string) (Job, error) {
	j, err := s.scan(q.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM cron_jobs WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Job{}, fmt.Errorf("cron: read job %s: %w", id, err)
	}
	return j, nil
}

func (s *Store) query(ctx context.Context, q string, args ...any) ([]Job, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("cron: read jobs: %w", err)
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := s.scan(rows)
		if err != nil {
			return nil, fmt.Errorf("cron: read jobs: %w", err)
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cron: read jobs: %w", err)
	}
	return out, nil
}

func (s *Store) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cron: begin transaction: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cron: commit: %w", err)
	}
	return nil
}

func (s *Store) write(ctx context.Context, tx *sql.Tx, j Job) error {
	args, err := rowArgs(j)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE cron_jobs SET name=?, schedule=?, prompt=?, script=?, no_agent=?, deliver=?,
		skills=?, model=?, toolsets=?, workdir=?, repeat_left=?, enabled=?, origin=?, created_by_session=?,
		last_run=?, next_run=?, last_status=?, last_output=?, fail_streak=?, draft_only=?,
		last_error=?, last_error_key=?, pause_reason=?, watch=?, watch_hash=?, watch_output=?, continuity=?, effort=? WHERE id=?`, append(args[1:], j.ID)...)
	if err != nil {
		return fmt.Errorf("cron: write job %s: %w", j.ID, err)
	}
	return nil
}

// rowArgs returns the values for jobColumns, in the same order.
func rowArgs(j Job) ([]any, error) {
	skills, err := jsonList(j.Skills)
	if err != nil {
		return nil, err
	}
	toolsets, err := jsonList(j.Toolsets)
	if err != nil {
		return nil, err
	}
	var model any
	if j.Model != nil {
		b, err := json.Marshal(j.Model)
		if err != nil {
			return nil, err
		}
		model = string(b)
	}
	origin, err := json.Marshal(j.Origin)
	if err != nil {
		return nil, err
	}
	return []any{j.ID, j.Name, j.Schedule, j.Prompt, j.Script, boolInt(j.NoAgent), j.Deliver, skills, model, toolsets,
		j.Workdir, j.RepeatLeft, boolInt(j.Enabled), string(origin), j.CreatedBySession,
		msOrNil(j.LastRun), msOrNil(j.NextRun), j.LastStatus, j.LastOutput, j.FailStreak, boolInt(j.DraftOnly),
		j.LastError, j.LastErrorKey, j.PauseReason, boolInt(j.Watch), strOrNil(j.WatchHash), strOrNil(j.WatchOutput),
		boolInt(j.Continuity), j.Effort}, nil
}

type scanner interface{ Scan(dest ...any) error }

func (s *Store) scan(r scanner) (Job, error) {
	var (
		j                               Job
		noAgent, enabled, draftOnly     int
		skills, model, toolsets, origin sql.NullString
		lastRun, nextRun                sql.NullInt64
		watch, continuity               int
		watchHash, watchOutput          sql.NullString
	)
	if err := r.Scan(&j.ID, &j.Name, &j.Schedule, &j.Prompt, &j.Script, &noAgent, &j.Deliver, &skills, &model,
		&toolsets, &j.Workdir, &j.RepeatLeft, &enabled, &origin, &j.CreatedBySession, &lastRun, &nextRun,
		&j.LastStatus, &j.LastOutput, &j.FailStreak, &draftOnly,
		&j.LastError, &j.LastErrorKey, &j.PauseReason, &watch, &watchHash, &watchOutput, &continuity, &j.Effort); err != nil {
		return Job{}, err
	}
	j.NoAgent, j.Enabled, j.DraftOnly = noAgent != 0, enabled != 0, draftOnly != 0
	j.Watch, j.Continuity = watch != 0, continuity != 0
	j.WatchHash, j.WatchOutput = watchHash.String, watchOutput.String
	if err := unmarshalNull(skills, &j.Skills); err != nil {
		return Job{}, fmt.Errorf("skills: %w", err)
	}
	if err := unmarshalNull(toolsets, &j.Toolsets); err != nil {
		return Job{}, fmt.Errorf("toolsets: %w", err)
	}
	if err := unmarshalNull(origin, &j.Origin); err != nil {
		return Job{}, fmt.Errorf("origin: %w", err)
	}
	if model.Valid && model.String != "" {
		var m ModelPin
		if err := json.Unmarshal([]byte(model.String), &m); err != nil {
			return Job{}, fmt.Errorf("model: %w", err)
		}
		if m != (ModelPin{}) {
			j.Model = &m
		}
	}
	if len(j.Skills) == 0 {
		j.Skills = nil
	}
	if len(j.Toolsets) == 0 {
		j.Toolsets = nil
	}
	if lastRun.Valid {
		j.LastRun = time.UnixMilli(lastRun.Int64).In(s.loc())
	}
	if nextRun.Valid {
		j.NextRun = time.UnixMilli(nextRun.Int64).In(s.loc())
	}
	return j, nil
}

func unmarshalNull(v sql.NullString, dst any) error {
	if !v.Valid || v.String == "" {
		return nil
	}
	return json.Unmarshal([]byte(v.String), dst)
}

func jsonList(v []string) (any, error) {
	if len(v) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func msOrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

// floatOrNil returns nil for a nil p, so the column gets NULL.
func floatOrNil(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// strOrNil returns nil for "", so the column gets NULL.
func strOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}
