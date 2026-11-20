// Package brief builds the status text of /brief and of the daily send as data, with no model
// call. Each source reads its rows to the end and closes them before the next query, because
// state.db has one connection.
package brief

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/obs"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

const (
	window       = 24 * time.Hour
	goalAgeLimit = 7 * 24 * time.Hour
	detailMax    = 80
	goalTextMax  = 60
)

// Deps holds the stores that Build reads. A nil Held, Goals, or Sessions is skipped.
type Deps struct {
	Jobs     *cron.Store
	Held     *cron.HeldStore
	Goals    *goals.Store
	Sessions *sessions.Store
	DB       *db.DB
}

// Kind is the type of one status line.
type Kind int

// The kinds follow the order of the lines in a status.
const (
	KindFailing Kind = iota
	KindPausedJob
	KindBadSchedule
	KindHeld
	KindGoal
	KindCost
)

// Line is one thing that needs the owner.
type Line struct {
	Kind Kind
	// Name is the job name, the job of a held send, or the goal text. It is not escaped.
	Name string
	// Count is the fail streak of a failing job or of a job that failures paused.
	Count int
	// Detail is the last error of a failing job, or the reason of a paused goal, cut at 80 runes.
	Detail string
	// Age is the age of a held send, a paused goal, or a costly run. It is 0 for a job line.
	Age time.Duration
	// CostUSD and Ratio are the cost of a flagged run and the cost divided by the stored median.
	CostUSD, Ratio float64
}

// Status is the status as data.
type Status struct {
	// Lines holds all lines. The formatter caps the count.
	Lines []Line
	// RunsOK and RunsTotal count the cron runs in 24 h.
	RunsOK, RunsTotal int
	// HistoryOff is true when cron.run_history is 0.
	HistoryOff bool
	// Spend is the cost in 24 h of all chats. SpendUnpriced is true when a call has no price.
	Spend         float64
	SpendUnpriced bool
}

// Build returns the status as data. It makes no model call, no escape, and no redaction.
func Build(ctx context.Context, d Deps, historyOff bool, now time.Time) (Status, error) {
	var st Status
	since := now.Add(-window)

	jobs, err := d.Jobs.List(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("brief: read the cron jobs: %w", err)
	}
	sorted := append([]cron.Job(nil), jobs...)
	sort.SliceStable(sorted, func(i, k int) bool { return sorted[i].LastRun.After(sorted[k].LastRun) })
	for _, j := range sorted {
		if j.Enabled && j.FailStreak > 0 {
			st.Lines = append(st.Lines, Line{Kind: KindFailing, Name: j.Name, Count: j.FailStreak,
				Detail: cron.TruncateRunes(cron.ErrorSummary(j.Name, j.LastError), detailMax)})
		}
	}
	for _, j := range sorted {
		if j.PauseReason == cron.PauseFailures {
			st.Lines = append(st.Lines, Line{Kind: KindPausedJob, Name: j.Name, Count: j.FailStreak})
		}
	}
	for _, j := range sorted {
		if j.PauseReason == cron.PauseSchedule {
			st.Lines = append(st.Lines, Line{Kind: KindBadSchedule, Name: j.Name})
		}
	}

	if d.Held != nil {
		holds, err := d.Held.Holds(ctx, since)
		if err != nil {
			return Status{}, fmt.Errorf("brief: read the held sends: %w", err)
		}
		for _, h := range holds {
			st.Lines = append(st.Lines, Line{Kind: KindHeld, Name: h.JobName, Age: now.Sub(h.CreatedAt)})
		}
	}

	if d.Goals != nil {
		paused, err := d.Goals.Paused(ctx)
		if err != nil {
			return Status{}, fmt.Errorf("brief: read the paused goals: %w", err)
		}
		var gl []Line
		for _, g := range paused {
			reason := ""
			if g.State.PausedReason != nil {
				reason = *g.State.PausedReason
			}
			if reason == goals.ReasonSessionChanged || reason == goals.ReasonChatEnded {
				continue
			}
			at := g.UpdatedAt
			if g.State.PausedAt > 0 {
				sec := int64(g.State.PausedAt)
				at = time.Unix(sec, int64((g.State.PausedAt-float64(sec))*1e9))
			}
			age := now.Sub(at)
			if age > goalAgeLimit {
				continue
			}
			name := cron.TruncateRunes(g.State.Goal, goalTextMax)
			if d.Sessions != nil {
				s, err := d.Sessions.Get(ctx, g.SessionID)
				switch {
				case err == nil:
					if s.Title != "" {
						name = s.Title
					}
				case errors.Is(err, sessions.ErrNotFound):
				default:
					return Status{}, fmt.Errorf("brief: read the goal session: %w", err)
				}
			}
			gl = append(gl, Line{Kind: KindGoal, Name: name, Age: age, Detail: cron.TruncateRunes(reason, detailMax)})
		}
		sort.SliceStable(gl, func(i, k int) bool { return gl[i].Age < gl[k].Age })
		st.Lines = append(st.Lines, gl...)
	}

	flagged, err := d.Jobs.FlaggedRuns(ctx, since)
	if err != nil {
		return Status{}, fmt.Errorf("brief: read the costly runs: %w", err)
	}
	for _, r := range flagged {
		if r.CostMedian <= 0 {
			continue
		}
		st.Lines = append(st.Lines, Line{Kind: KindCost, Name: r.JobName, CostUSD: r.CostUSD,
			Ratio: r.CostUSD / r.CostMedian, Age: now.Sub(r.At)})
	}

	if historyOff {
		st.HistoryOff = true
	} else {
		st.RunsOK, st.RunsTotal, err = d.Jobs.RunsOK(ctx, "", since)
		if err != nil {
			return Status{}, fmt.Errorf("brief: read the run counts: %w", err)
		}
	}

	st.Spend, st.SpendUnpriced, err = obs.SpendSince(ctx, d.DB, since)
	if err != nil {
		return Status{}, fmt.Errorf("brief: read the spend: %w", err)
	}
	return st, nil
}
