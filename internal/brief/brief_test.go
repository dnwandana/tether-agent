package brief

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

var ctx = context.Background()

// now is the clock of the tests.
var now = time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)

// testDeps returns the deps over a new state.db.
func testDeps(t *testing.T) Deps {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return Deps{
		Jobs:     &cron.Store{DB: d, Loc: time.UTC, Now: func() time.Time { return now }, RunHistory: 50},
		Held:     &cron.HeldStore{DB: d},
		Goals:    &goals.Store{DB: d},
		Sessions: sessions.New(d),
		DB:       d,
	}
}

// jobState is the state of one job row. lastRun 0 stores NULL. ended stores repeat_left 0,
// else repeat_left is -1.
type jobState struct {
	enabled, ended  bool
	streak          int
	lastErr, reason string
	lastRun         time.Duration // the age of the last run
}

// addJob creates a job and sets its state with SQL.
func addJob(t *testing.T, d Deps, name string, s jobState) string {
	t.Helper()
	j, err := d.Jobs.Create(ctx, cron.Job{Name: name, Schedule: "30m", Prompt: "x", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var last any
	if s.lastRun > 0 {
		last = now.Add(-s.lastRun).UnixMilli()
	}
	enabled := 0
	if s.enabled {
		enabled = 1
	}
	repeat := -1
	if s.ended {
		repeat = 0
	}
	if _, err := d.DB.Exec(`UPDATE cron_jobs SET enabled=?, fail_streak=?, last_error=?, pause_reason=?,
		repeat_left=?, last_run=? WHERE id=?`, enabled, s.streak, s.lastErr, s.reason, repeat, last, j.ID); err != nil {
		t.Fatal(err)
	}
	return j.ID
}

// addRun writes one cron_runs row that started and ended age ago.
func addRun(t *testing.T, d Deps, jobID, status string, age time.Duration, cost, median any, flag int) {
	t.Helper()
	at := now.Add(-age).UnixMilli()
	if _, err := d.DB.Exec(`INSERT INTO cron_runs (job_id, started_at, finished_at, status, delivery, cost_usd,
		cost_median, cost_flag) VALUES (?, ?, ?, ?, 'sent', ?, ?, ?)`, jobID, at, at, status, cost, median, flag); err != nil {
		t.Fatal(err)
	}
}

// addGoal writes the goal row of the session. pausedAge 0 leaves PausedAt out, and the row
// gets updated_at = now - updatedAge.
func addGoal(t *testing.T, d Deps, sessionID, goal, status, reason string, pausedAge, updatedAge time.Duration) {
	t.Helper()
	st := goals.State{Goal: goal, Status: status, MaxTurns: 20}
	if reason != "" {
		st.PausedReason = &reason
	}
	if pausedAge > 0 {
		st.PausedAt = float64(now.Add(-pausedAge).Unix())
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO goals (session_id, state, revision, updated_at) VALUES (?, ?, 1, ?)`,
		sessionID, string(raw), now.Add(-updatedAge).UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

// Build returns each line kind in the table order of the status, newest first inside a kind.
func TestBuildLines(t *testing.T) {
	d := testDeps(t)
	longErr := strings.Repeat("e", 100)
	rss := addJob(t, d, "rss", jobState{enabled: true, streak: 3, lastErr: "http 502 from the feed", lastRun: time.Hour})
	news := addJob(t, d, "news", jobState{enabled: true, streak: 1, lastErr: longErr, lastRun: 10 * time.Minute})
	backup := addJob(t, d, "backup", jobState{streak: 10, reason: cron.PauseFailures, lastRun: 3 * time.Hour})
	old := addJob(t, d, "old", jobState{reason: cron.PauseSchedule})
	addJob(t, d, "owner-paused", jobState{streak: 2}) // an owner pause: no line
	addJob(t, d, "ended", jobState{ended: true})      // ended: no line
	addJob(t, d, "fine", jobState{enabled: true})     // no line

	if err := d.Held.Insert(ctx, cron.HeldSend{ID: "h1", Kind: "hold", JobID: rss, Target: "telegram:-100",
		CreatedAt: now.Add(-2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}

	s, err := d.Sessions.Create(ctx, sessions.Session{Key: "telegram:dm:1", Title: "Ship the release"})
	if err != nil {
		t.Fatal(err)
	}
	longGoal := strings.Repeat("g", 70)
	addGoal(t, d, s.ID, "Ship v1", goals.StatusPaused, "the judge failed 3 times", 72*time.Hour, time.Minute)
	addGoal(t, d, "no-session", longGoal, goals.StatusPaused, "", 0, time.Hour)
	addGoal(t, d, "changed", "a", goals.StatusPaused, goals.ReasonSessionChanged, time.Hour, time.Hour)
	addGoal(t, d, "ended-chat", "b", goals.StatusPaused, goals.ReasonChatEnded, time.Hour, time.Hour)
	addGoal(t, d, "too-old", "c", goals.StatusPaused, "stopped by /stop", 8*24*time.Hour, time.Hour)
	addGoal(t, d, "active", "d", goals.StatusActive, "", 0, time.Hour)

	addRun(t, d, news, "ok", 30*time.Minute, 1.5, 0.5, 1)
	addRun(t, d, news, "ok", 25*time.Hour, 2.0, 0.5, 1) // older than 24 h
	addRun(t, d, rss, "error", 2*time.Hour, nil, nil, 0)
	addRun(t, d, rss, "ok", 5*time.Hour, nil, nil, 0)
	addRun(t, d, rss, "ok", 30*time.Hour, nil, nil, 0) // older than 24 h

	if _, err := d.DB.Exec(`INSERT INTO messages (session_id, role, content, cost_usd, model, provider, timestamp)
		VALUES ('x', 'assistant', 'a', 0.4, 'm', 'p', ?)`, now.Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}

	got, err := Build(ctx, d, false, now)
	if err != nil {
		t.Fatal(err)
	}
	want := Status{
		Lines: []Line{
			{Kind: KindFailing, Name: "news", Count: 1, Detail: strings.Repeat("e", 80) + "…", JobID: news},
			{Kind: KindFailing, Name: "rss", Count: 3, Detail: "http 502 from the feed", JobID: rss},
			{Kind: KindPausedJob, Name: "backup", Count: 10, JobID: backup},
			{Kind: KindBadSchedule, Name: "old", JobID: old},
			{Kind: KindHeld, Name: "rss", Age: 2 * time.Hour, JobID: rss},
			{Kind: KindGoal, Name: strings.Repeat("g", 60) + "…", Age: time.Hour, SessionID: "no-session"},
			{Kind: KindGoal, Name: "Ship the release", Age: 72 * time.Hour, Detail: "the judge failed 3 times", SessionID: s.ID},
			{Kind: KindCost, Name: "news", CostUSD: 1.5, Ratio: 3, Age: 30 * time.Minute, JobID: news},
		},
		RunsOK: 2, RunsTotal: 3, Spend: 0.4,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

// With cron.run_history 0, Build sets HistoryOff and does not count the runs.
func TestBuildHistoryOff(t *testing.T) {
	d := testDeps(t)
	id := addJob(t, d, "rss", jobState{enabled: true})
	addRun(t, d, id, "ok", time.Hour, nil, nil, 0)
	got, err := Build(ctx, d, true, now)
	if err != nil || !got.HistoryOff || got.RunsOK != 0 || got.RunsTotal != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

// An empty database gives no lines. Nil Held, Goals, and Sessions are skipped.
func TestBuildEmpty(t *testing.T) {
	d := testDeps(t)
	d.Held, d.Goals, d.Sessions = nil, nil, nil
	got, err := Build(ctx, d, false, now)
	if err != nil || !reflect.DeepEqual(got, Status{}) {
		t.Fatalf("%+v %v", got, err)
	}
}

// A failing line shows only the cause of the stored alert, on one line.
func TestBuildFailingDetailOneLine(t *testing.T) {
	d := testDeps(t)
	addJob(t, d, "rss", jobState{enabled: true, streak: 2, lastRun: time.Hour,
		lastErr: "⚠ Cron job rss failed: connection refused\n\nI send no new alert while the job fails with the same error."})
	st, err := Build(ctx, d, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Lines) != 1 || st.Lines[0].Detail != "connection refused" {
		t.Fatalf("%+v", st.Lines)
	}
}
