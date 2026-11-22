package cron

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
)

// ErrJobPaused is the error of RunSoon for a disabled job.
var ErrJobPaused = errors.New("cron: the job is paused. Resume it first")

// FlaggedRun is a run that cost more than 3 times the median of its job.
type FlaggedRun struct {
	JobID, JobName      string
	At                  time.Time
	CostUSD, CostMedian float64
}

// RunsOK counts the runs that started at or after since. The ok value counts only the runs
// with status ok. An empty jobID counts the runs of all jobs.
func (s *Store) RunsOK(ctx context.Context, jobID string, since time.Time) (ok, total int, err error) {
	q := `SELECT COUNT(*), COALESCE(SUM(status='ok'),0) FROM cron_runs WHERE started_at>=?`
	args := []any{since.UnixMilli()}
	if jobID != "" {
		q += ` AND job_id=?`
		args = append(args, jobID)
	}
	if err := s.DB.QueryRowContext(ctx, q, args...).Scan(&total, &ok); err != nil {
		return 0, 0, fmt.Errorf("cron: count runs of job %q: %w", jobID, err)
	}
	return ok, total, nil
}

// LastDelivered returns the newest run that is ok and was sent or kept local.
func (s *Store) LastDelivered(ctx context.Context, jobID string) (Run, bool, error) {
	r, err := s.scanRun(s.DB.QueryRowContext(ctx, `SELECT `+runColumns+` FROM cron_runs
		WHERE job_id=? AND status='ok' AND delivery IN ('sent','local') ORDER BY id DESC LIMIT 1`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, fmt.Errorf("cron: read last delivered run of job %s: %w", jobID, err)
	}
	return r, true, nil
}

// PricedCosts returns the costs of the n newest runs that have a price, newest first.
func (s *Store) PricedCosts(ctx context.Context, jobID string, n int) ([]float64, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT cost_usd FROM cron_runs
		WHERE job_id=? AND cost_usd IS NOT NULL ORDER BY id DESC LIMIT ?`, jobID, n)
	if err != nil {
		return nil, fmt.Errorf("cron: read priced costs of job %s: %w", jobID, err)
	}
	defer rows.Close()
	var out []float64
	for rows.Next() {
		var v float64
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("cron: read priced costs of job %s: %w", jobID, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cron: read priced costs of job %s: %w", jobID, err)
	}
	return out, nil
}

// ModelRunsSince counts the cron sessions of the job that started at or after since.
func (s *Store) ModelRunsSince(ctx context.Context, jobID string, since time.Time) (int, error) {
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE session_key=? AND started_at>=?`,
		"cron:"+jobID, since.UnixMilli()).Scan(&n); err != nil {
		return 0, fmt.Errorf("cron: count model runs of job %s: %w", jobID, err)
	}
	return n, nil
}

// FlaggedRuns returns the costly runs that started at or after since, newest first.
func (s *Store) FlaggedRuns(ctx context.Context, since time.Time) ([]FlaggedRun, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT r.job_id, j.name, r.started_at,
		COALESCE(r.cost_usd,0), COALESCE(r.cost_median,0)
		FROM cron_runs r JOIN cron_jobs j ON j.id=r.job_id
		WHERE r.cost_flag=1 AND r.started_at>=? ORDER BY r.started_at DESC, r.id DESC`, since.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("cron: read flagged runs: %w", err)
	}
	defer rows.Close()
	var out []FlaggedRun
	for rows.Next() {
		var f FlaggedRun
		var ms int64
		if err := rows.Scan(&f.JobID, &f.JobName, &ms, &f.CostUSD, &f.CostMedian); err != nil {
			return nil, fmt.Errorf("cron: read flagged runs: %w", err)
		}
		f.At = time.UnixMilli(ms).In(s.loc())
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cron: read flagged runs: %w", err)
	}
	return out, nil
}

// RunSoon sets the next run of the job to now. It returns ErrJobPaused for a disabled job.
func (s *Store) RunSoon(ctx context.Context, id string) (Job, error) {
	j, err := s.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if !j.Enabled {
		return Job{}, ErrJobPaused
	}
	now := s.now()
	return s.Update(ctx, id, JobPatch{NextRun: &now})
}

// ErrorSummary returns the first line of the alert text of the job name with no
// "⚠ Cron job <name>" prefix, so a status line shows only the cause.
func ErrorSummary(name, text string) string {
	line := strings.TrimSpace(strings.SplitN(strings.TrimLeft(text, "\n"), "\n", 2)[0])
	rest, ok := strings.CutPrefix(line, "⚠ Cron job "+name+" ")
	if !ok {
		return line
	}
	if after, ok := strings.CutPrefix(rest, "failed"); ok {
		after = strings.TrimPrefix(after, ": ")
		if strings.HasPrefix(after, " (") && strings.HasSuffix(after, ")") {
			after = after[2 : len(after)-1]
		}
		if after != "" {
			return after
		}
	}
	return rest
}

// RunsTotal counts the kept cron_runs rows of the job jobID. The Jobs page pages Runs with it.
func (s *Store) RunsTotal(ctx context.Context, jobID string) (int, error) {
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM cron_runs WHERE job_id=?`, jobID).Scan(&n); err != nil {
		return 0, fmt.Errorf("cron: count runs of job %s: %w", jobID, err)
	}
	return n, nil
}

// CostByJob returns the cost of the cron sessions of each job since since, by job id. It sums
// messages.cost_usd of the assistant rows of the "cron:<id>" sessions, because cron_runs keeps
// only the last runs of a job. A job whose calls have no price gets 0.
func CostByJob(ctx context.Context, d *db.DB, since time.Time) (map[string]float64, error) {
	rows, err := d.QueryContext(ctx, `SELECT substr(s.session_key, 6), COALESCE(SUM(m.cost_usd), 0)
		FROM messages m JOIN sessions s ON s.id = m.session_id
		WHERE s.session_key LIKE 'cron:%' AND m.role = 'assistant' AND m.timestamp >= ?
		GROUP BY s.session_key`, since.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("cron: read the cost by job: %w", err)
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var id string
		var cost float64
		if err := rows.Scan(&id, &cost); err != nil {
			return nil, fmt.Errorf("cron: read the cost by job: %w", err)
		}
		out[id] += cost
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cron: read the cost by job: %w", err)
	}
	return out, nil
}
