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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/db"
)

// ErrNotFound is the error for a job id that is not in cron_jobs.
var ErrNotFound = errors.New("cron: job not found. Use list to see the job ids")

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
	NextRun *time.Time
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
}

const jobColumns = `id, name, schedule, prompt, script, no_agent, deliver, skills, model, toolsets, workdir,
	repeat_left, enabled, origin, created_by_session, last_run, next_run, last_status, last_output`

var (
	telegramChatRE   = regexp.MustCompile(`^(-?\d+|@[A-Za-z0-9_]+)$`)
	telegramThreadRE = regexp.MustCompile(`^\d+$`)
)

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
	if err := s.validate(j); err != nil {
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
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, append(args, now.UnixMilli())...)
	return err
}

// Update applies patch to the job id and validates the result. A new schedule gives a new
// NextRun. A one-shot time in the past is refused.
func (s *Store) Update(ctx context.Context, id string, patch JobPatch) (Job, error) {
	now := s.now()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		j, err := s.get(ctx, tx, id)
		if err != nil {
			return err
		}
		old := j
		applyPatch(&j, patch)
		j = normalize(j)
		if err := s.validate(j); err != nil {
			return err
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
		return s.write(ctx, tx, j)
	})
	if err != nil {
		return Job{}, err
	}
	return s.Get(ctx, id)
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

// Remove deletes the job id.
func (s *Store) Remove(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM cron_jobs WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("cron: remove job %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return nil
}

// Due returns the enabled jobs with NextRun at or before now, in NextRun order.
func (s *Store) Due(ctx context.Context, now time.Time) ([]Job, error) {
	return s.query(ctx, `SELECT `+jobColumns+` FROM cron_jobs
		WHERE enabled=1 AND next_run IS NOT NULL AND next_run<=? ORDER BY next_run, id`, now.UnixMilli())
}

// MarkRun records a run that started at ranAt. It computes NextRun from the schedule, but
// never earlier than now. It decrements RepeatLeft, and it disables the job when RepeatLeft
// reaches 0 or when the schedule has no further run.
func (s *Store) MarkRun(ctx context.Context, id string, status, output string, ranAt time.Time) error {
	now := s.now()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		j, err := s.get(ctx, tx, id)
		if err != nil {
			return err
		}
		j.LastRun, j.LastStatus, j.LastOutput = ranAt, status, output
		sched, err := parse(j.Schedule, s.loc())
		if err != nil {
			return fmt.Errorf("cron: job %s has a bad stored schedule: %w", id, err)
		}
		next := sched.Next(ranAt)
		if !next.IsZero() && !next.After(now) {
			next = sched.Next(now)
		}
		j.NextRun = next
		if j.RepeatLeft > 0 {
			j.RepeatLeft--
		}
		if j.RepeatLeft == 0 || next.IsZero() {
			j.Enabled = false
			j.NextRun = time.Time{}
		}
		return s.write(ctx, tx, j)
	})
}

// validate checks a job before the store saves it. It checks the schedule, the prompt or
// the script, the workdir, the toolsets, the skills, and the deliver value.
func (s *Store) validate(j Job) error {
	if j.Schedule == "" {
		return fmt.Errorf("cron: schedule is required: %s", formatHelp)
	}
	if j.NoAgent && j.Script == "" {
		return errors.New("cron: a no_agent job needs a script")
	}
	if !j.NoAgent && j.Prompt == "" {
		return errors.New("cron: a job needs a prompt, or no_agent with a script")
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
		last_run=?, next_run=?, last_status=?, last_output=? WHERE id=?`, append(args[1:], j.ID)...)
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
		msOrNil(j.LastRun), msOrNil(j.NextRun), j.LastStatus, j.LastOutput}, nil
}

type scanner interface{ Scan(dest ...any) error }

func (s *Store) scan(r scanner) (Job, error) {
	var (
		j                               Job
		noAgent, enabled                int
		skills, model, toolsets, origin sql.NullString
		lastRun, nextRun                sql.NullInt64
	)
	if err := r.Scan(&j.ID, &j.Name, &j.Schedule, &j.Prompt, &j.Script, &noAgent, &j.Deliver, &skills, &model,
		&toolsets, &j.Workdir, &j.RepeatLeft, &enabled, &origin, &j.CreatedBySession, &lastRun, &nextRun,
		&j.LastStatus, &j.LastOutput); err != nil {
		return Job{}, err
	}
	j.NoAgent, j.Enabled = noAgent != 0, enabled != 0
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
