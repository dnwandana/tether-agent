package cron

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
)

var ctx = context.Background()

// clock is a test clock that the tests can move.
type clock struct{ t time.Time }

func (c *clock) Now() time.Time { return c.t }

func fixedNow(y, mo, d, h, mi int) *clock {
	return &clock{t: time.Date(y, time.Month(mo), d, h, mi, 0, 0, time.UTC)}
}

func store(t *testing.T, c *clock) *Store {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	valid := map[string]bool{"web": true, "terminal": true, "file": true}
	return &Store{DB: d, Loc: time.UTC, Now: c.Now, ValidToolset: func(s string) bool { return valid[s] }}
}

// histStore returns a store that keeps runHistory runs of each job and pauses at 10 failures.
func histStore(t *testing.T, c *clock, runHistory int) *Store {
	t.Helper()
	s := store(t, c)
	s.RunHistory, s.FailStreakLimit = runHistory, 10
	return s
}

// runRec is the record of a run that ended at once and was sent.
func runRec(status, output string, at time.Time) RunRecord {
	return RunRecord{Status: status, LastStatus: status, Delivery: DeliverySent, Output: output,
		RanAt: at, FinishedAt: at.Add(time.Second)}
}

// stopRec is the record of a run that a shutdown stopped.
func stopRec(output string, at time.Time) RunRecord {
	return RunRecord{Status: StatusStopped, LastStatus: StatusStopped, Delivery: DeliveryNone, Output: output,
		RanAt: at, FinishedAt: at}
}

// storedRun is one cron_runs row.
type storedRun struct {
	id, started, finished                            int64
	status, delivery, output, deliveryErr, sessionID string
}

// storedRuns reads the cron_runs rows of the job, oldest first. It closes the rows before it
// returns, because the database has one connection.
func storedRuns(t *testing.T, s *Store, jobID string) []storedRun {
	t.Helper()
	rows, err := s.DB.QueryContext(ctx, `SELECT id, started_at, finished_at, status, delivery, output,
		delivery_error, session_id FROM cron_runs WHERE job_id=? ORDER BY id`, jobID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []storedRun
	for rows.Next() {
		var r storedRun
		if err := rows.Scan(&r.id, &r.started, &r.finished, &r.status, &r.delivery, &r.output,
			&r.deliveryErr, &r.sessionID); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCreateComputesNextRun(t *testing.T) {
	s := store(t, fixedNow(2026, 10, 27, 10, 0))
	j, err := s.Create(ctx, Job{Name: "digest", Schedule: "0 1 * * *", Prompt: "GitHub trending digest", Enabled: true})
	if err != nil || j.NextRun.Day() != 28 || j.RepeatLeft != -1 {
		t.Fatal(j, err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(j.ID) {
		t.Fatal("id", j.ID)
	}
	got, err := s.Get(ctx, j.ID)
	if err != nil || got.Name != "digest" || !got.NextRun.Equal(j.NextRun) || !got.Enabled || !got.LastRun.IsZero() {
		t.Fatal(got, err)
	}
}

func TestCreateRoundTripsAllFields(t *testing.T) {
	s := store(t, fixedNow(2026, 10, 27, 10, 0))
	dir := t.TempDir()
	in := Job{Name: "n", Schedule: "every 2h", Prompt: "p", Script: "check.sh", Deliver: "telegram:-100:17585",
		Skills: []string{"a", "b"}, Model: &ModelPin{Provider: "cheap", Model: "m1"}, Toolsets: []string{"web"},
		Workdir: dir, RepeatLeft: 3, Enabled: true,
		Origin:           Origin{Source: "telegram", ChatID: "-100", ThreadID: "17585", SessionKey: "telegram:group:-100:17585"},
		CreatedBySession: "sess1"}
	j, err := s.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Script != "check.sh" || got.Deliver != in.Deliver || len(got.Skills) != 2 || got.Skills[1] != "b" ||
		got.Model == nil || *got.Model != *in.Model || len(got.Toolsets) != 1 || got.Workdir != dir ||
		got.RepeatLeft != 3 || got.Origin != in.Origin || got.CreatedBySession != "sess1" || got.Schedule != "every 2h" {
		t.Fatalf("%+v", got)
	}
}

func TestCreateDefaultName(t *testing.T) {
	s := store(t, fixedNow(2026, 10, 27, 10, 0))
	j, err := s.Create(ctx, Job{Schedule: "30m", Prompt: "Check the disk space on the server and report it", Enabled: true})
	if err != nil || j.Name == "" {
		t.Fatal(j, err)
	}
}

func TestValidation(t *testing.T) {
	s := store(t, fixedNow(2026, 10, 27, 10, 0))
	for _, bad := range []Job{
		{Schedule: "30m"},
		{Prompt: "x"},
		{Schedule: "30m", NoAgent: true},
		{Schedule: "30m", Prompt: "x", Workdir: "rel/path"},
		{Schedule: "30m", Prompt: "x", Workdir: "/no/such/dir/at/all"},
		{Schedule: "30m", Prompt: "x", Toolsets: []string{"nope"}},
		{Schedule: "30m", Prompt: "x", Deliver: "slack:#x"},
		{Schedule: "30m", Prompt: "x", Deliver: "telegram:"},
		{Schedule: "30m", Prompt: "x", Deliver: "telegram:-100:abc"},
		{Schedule: "sometimes", Prompt: "x"},
		{Schedule: "2026-10-01T09:00", Prompt: "x"},
		{Schedule: "30m", Prompt: "x", Skills: []string{""}},
	} {
		if _, err := s.Create(ctx, bad); err == nil {
			t.Fatalf("%+v", bad)
		}
	}
	for _, good := range []Job{
		{Schedule: "30m", NoAgent: true, Script: "a.sh"},
		{Schedule: "30m", Prompt: "x", Deliver: "origin"},
		{Schedule: "30m", Prompt: "x", Deliver: "local"},
		{Schedule: "30m", Prompt: "x", Deliver: "telegram:-100"},
		{Schedule: "30m", Prompt: "x", Deliver: "telegram:-100:17585"},
		{Schedule: "30m", Prompt: "x", Workdir: t.TempDir(), Toolsets: []string{"web", "file"}},
	} {
		if _, err := s.Create(ctx, good); err != nil {
			t.Fatalf("%+v: %v", good, err)
		}
	}
}

func TestOneShotDisablesAfterRun(t *testing.T) {
	c := fixedNow(2026, 10, 27, 10, 0)
	s := store(t, c)
	j, err := s.Create(ctx, Job{Schedule: "2026-10-28T09:00", Prompt: "x", Enabled: true})
	if err != nil || j.RepeatLeft != 1 || !j.NextRun.Equal(time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)) {
		t.Fatal(j, err)
	}
	c.t = j.NextRun
	if err := s.MarkRun(ctx, j.ID, runRec("ok", "done", c.t)); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, j.ID)
	if got.Enabled || !got.NextRun.IsZero() || got.LastStatus != "ok" || got.LastOutput != "done" || !got.LastRun.Equal(c.t) {
		t.Fatalf("%+v", got)
	}
	// The stored one-shot time is now in the past. A resume must refuse it.
	if err := s.SetEnabled(ctx, j.ID, true); err == nil {
		t.Fatal("resume of a past one-shot job")
	}
}

func TestRepeatCount(t *testing.T) {
	c := fixedNow(2026, 10, 27, 10, 0)
	s := store(t, c)
	j, _ := s.Create(ctx, Job{Schedule: "every 1h", Prompt: "x", RepeatLeft: 2, Enabled: true})
	for i := 0; i < 2; i++ {
		c.t = c.t.Add(time.Hour)
		if err := s.MarkRun(ctx, j.ID, runRec("ok", "", c.t)); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.Get(ctx, j.ID)
	if got.Enabled || got.RepeatLeft != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestMarkRunNextRunNeverInPast(t *testing.T) {
	c := fixedNow(2026, 10, 27, 10, 0)
	s := store(t, c)
	j, _ := s.Create(ctx, Job{Schedule: "every 30m", Prompt: "x", Enabled: true})
	ranAt := c.t.Add(30 * time.Minute)
	c.t = ranAt.Add(2 * time.Hour) // the run took two hours
	if err := s.MarkRun(ctx, j.ID, runRec("ok", "", ranAt)); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, j.ID)
	if !got.NextRun.After(c.t) || got.RepeatLeft != -1 || !got.Enabled {
		t.Fatalf("%+v", got)
	}
}

func TestMarkStoppedKeepsRepeat(t *testing.T) {
	c := fixedNow(2026, 10, 27, 10, 0)
	s := store(t, c)
	j, _ := s.Create(ctx, Job{Schedule: "every 1h", Prompt: "x", RepeatLeft: 3, Enabled: true})
	c.t = j.NextRun
	if err := s.MarkStopped(ctx, j.ID, stopRec("stopped", c.t)); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, j.ID)
	if !got.Enabled || got.RepeatLeft != 3 || !got.NextRun.After(c.t) || got.LastStatus != StatusStopped ||
		got.LastOutput != "stopped" {
		t.Fatalf("%+v", got)
	}
}

// A one-shot job that two shutdowns in a row stop is used up, and its output tells why.
func TestMarkStoppedTwiceUsesUpOneShot(t *testing.T) {
	c := fixedNow(2026, 10, 27, 10, 0)
	s := store(t, c)
	j, _ := s.Create(ctx, Job{Schedule: "2026-10-28T09:00", Prompt: "x", Enabled: true})
	c.t = j.NextRun
	if err := s.MarkStopped(ctx, j.ID, stopRec("stopped 1", c.t)); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, j.ID)
	if !got.Enabled || got.RepeatLeft != 1 || !got.NextRun.Equal(c.t) || got.LastStatus != StatusStopped {
		t.Fatalf("the first stop keeps the job due: %+v", got)
	}
	c.t = c.t.Add(time.Minute)
	if err := s.MarkStopped(ctx, j.ID, stopRec("stopped 2", c.t)); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, j.ID)
	if got.Enabled || got.RepeatLeft != 0 || !got.NextRun.IsZero() || got.LastStatus != StatusStopped ||
		got.LastOutput != "stopped 2\nThe job does not run again: two shutdowns in a row stopped it." {
		t.Fatalf("the second stop uses up the job: %+v", got)
	}
}

// A recurring job with repeats left moves to its next run at each stop. The stops do not
// count, also two in a row.
func TestMarkStoppedTwiceKeepsRecurringRepeat(t *testing.T) {
	c := fixedNow(2026, 10, 27, 10, 0)
	s := store(t, c)
	j, _ := s.Create(ctx, Job{Schedule: "every 1h", Prompt: "x", RepeatLeft: 3, Enabled: true})
	for range 2 {
		cur, _ := s.Get(ctx, j.ID)
		c.t = cur.NextRun
		if err := s.MarkStopped(ctx, j.ID, stopRec("stopped", c.t)); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.Get(ctx, j.ID)
	if !got.Enabled || got.RepeatLeft != 3 || !got.NextRun.After(c.t) || got.LastStatus != StatusStopped ||
		got.LastOutput != "stopped" {
		t.Fatalf("%+v", got)
	}
}

// An edit or a resume clears the stopped status, so the next stop keeps the job due again.
func TestEditClearsStoppedStatus(t *testing.T) {
	for name, edit := range map[string]func(s *Store, id string) error{
		"update": func(s *Store, id string) error {
			p := "y"
			_, err := s.Update(ctx, id, JobPatch{Prompt: &p})
			return err
		},
		"resume": func(s *Store, id string) error {
			if err := s.SetEnabled(ctx, id, false); err != nil {
				return err
			}
			return s.SetEnabled(ctx, id, true)
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := fixedNow(2026, 10, 27, 10, 0)
			s := store(t, c)
			j, _ := s.Create(ctx, Job{Schedule: "2026-10-28T09:00", Prompt: "x", Enabled: true})
			c.t = j.NextRun
			if err := s.MarkStopped(ctx, j.ID, stopRec("stopped", c.t)); err != nil {
				t.Fatal(err)
			}
			if err := edit(s, j.ID); err != nil {
				t.Fatal(err)
			}
			if got, _ := s.Get(ctx, j.ID); got.LastStatus != "" {
				t.Fatalf("the edit clears the stopped status: %+v", got)
			}
			c.t = c.t.Add(time.Minute)
			if err := s.MarkStopped(ctx, j.ID, stopRec("stopped", c.t)); err != nil {
				t.Fatal(err)
			}
			got, _ := s.Get(ctx, j.ID)
			if !got.Enabled || got.RepeatLeft != 1 || !got.NextRun.Equal(c.t) || got.LastStatus != StatusStopped {
				t.Fatalf("a stop after an edit keeps the job due: %+v", got)
			}
		})
	}
}

func TestUpdateEmptySliceClears(t *testing.T) {
	s := store(t, fixedNow(2026, 10, 27, 10, 0))
	j, _ := s.Create(ctx, Job{Schedule: "30m", Prompt: "x", Skills: []string{"a"}, Toolsets: []string{"web"}, Enabled: true})
	got, err := s.Update(ctx, j.ID, JobPatch{Skills: &[]string{}})
	if err != nil || len(got.Skills) != 0 || len(got.Toolsets) != 1 {
		t.Fatal(got, err)
	}
	got, err = s.Update(ctx, j.ID, JobPatch{Toolsets: &[]string{}})
	if err != nil || len(got.Toolsets) != 0 {
		t.Fatal(got, err)
	}
	got, _ = s.Get(ctx, j.ID)
	if len(got.Skills) != 0 || len(got.Toolsets) != 0 || got.Prompt != "x" {
		t.Fatalf("%+v", got)
	}
}

func TestUpdateScheduleRecomputesAndValidates(t *testing.T) {
	c := fixedNow(2026, 10, 27, 10, 0)
	s := store(t, c)
	j, _ := s.Create(ctx, Job{Schedule: "30m", Prompt: "x", Enabled: true})
	sched := "0 1 * * *"
	got, err := s.Update(ctx, j.ID, JobPatch{Schedule: &sched})
	if err != nil || !got.NextRun.Equal(time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)) {
		t.Fatal(got, err)
	}
	bad := "rel/path"
	if _, err := s.Update(ctx, j.ID, JobPatch{Workdir: &bad}); err == nil {
		t.Fatal("update must validate")
	}
	past := "2026-10-01T09:00"
	if _, err := s.Update(ctx, j.ID, JobPatch{Schedule: &past}); err == nil {
		t.Fatal("update must refuse a past one-shot time")
	}
	model := ModelPin{Provider: "cheap"}
	if got, _ := s.Update(ctx, j.ID, JobPatch{Model: &model}); got.Model == nil || got.Model.Provider != "cheap" {
		t.Fatal(got.Model)
	}
	if got, _ := s.Update(ctx, j.ID, JobPatch{Model: &ModelPin{}}); got.Model != nil {
		t.Fatal("an empty model pin clears the field", got.Model)
	}
}

func TestUpdateNextRunMarksDue(t *testing.T) {
	c := fixedNow(2026, 10, 27, 10, 0)
	s := store(t, c)
	j, _ := s.Create(ctx, Job{Schedule: "0 1 * * *", Prompt: "x", Enabled: true})
	now := c.Now()
	got, err := s.Update(ctx, j.ID, JobPatch{NextRun: &now})
	if err != nil || !got.NextRun.Equal(now) {
		t.Fatal(got, err)
	}
	due, _ := s.Due(ctx, now)
	if len(due) != 1 || due[0].ID != j.ID {
		t.Fatal(due)
	}
}

func TestDueOrder(t *testing.T) {
	c := fixedNow(2026, 10, 27, 10, 0)
	s := store(t, c)
	a, _ := s.Create(ctx, Job{Name: "a", Schedule: "every 2h", Prompt: "x", Enabled: true})
	b, _ := s.Create(ctx, Job{Name: "b", Schedule: "every 1h", Prompt: "x", Enabled: true})
	_, _ = s.Create(ctx, Job{Name: "c", Schedule: "every 3h", Prompt: "x", Enabled: true})
	off, _ := s.Create(ctx, Job{Name: "off", Schedule: "every 1h", Prompt: "x", Enabled: false})
	due, err := s.Due(ctx, c.t.Add(2*time.Hour))
	if err != nil || len(due) != 2 || due[0].ID != b.ID || due[1].ID != a.ID {
		t.Fatal(due, err)
	}
	for _, j := range due {
		if j.ID == off.ID {
			t.Fatal("a disabled job is never due")
		}
	}
}

func TestSetEnabledAndRemove(t *testing.T) {
	c := fixedNow(2026, 10, 27, 10, 0)
	s := store(t, c)
	j, _ := s.Create(ctx, Job{Schedule: "every 1h", Prompt: "x", Enabled: true})
	if err := s.SetEnabled(ctx, j.ID, false); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(5 * time.Hour)
	if due, _ := s.Due(ctx, c.t); len(due) != 0 {
		t.Fatal("paused")
	}
	if err := s.SetEnabled(ctx, j.ID, true); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, j.ID)
	if !got.Enabled || !got.NextRun.After(c.t) {
		t.Fatalf("a resume moves a past next run after now: %+v", got)
	}
	if err := s.Remove(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, j.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.Remove(ctx, j.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.MarkRun(ctx, j.ID, runRec("ok", "", c.t)); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestListOrder(t *testing.T) {
	c := fixedNow(2026, 10, 27, 10, 0)
	s := store(t, c)
	a, _ := s.Create(ctx, Job{Name: "a", Schedule: "30m", Prompt: "x", Enabled: true})
	c.t = c.t.Add(time.Minute)
	b, _ := s.Create(ctx, Job{Name: "b", Schedule: "30m", Prompt: "x", Enabled: true})
	l, err := s.List(ctx)
	if err != nil || len(l) != 2 || l[0].ID != a.ID || l[1].ID != b.ID {
		t.Fatal(l, err)
	}
}

// A job that one Store creates is visible to a second Store on the same file (the chat and
// gateway roles).
func TestJobVisibleAcrossProcesses(t *testing.T) {
	c := fixedNow(2026, 10, 27, 10, 0)
	p := filepath.Join(t.TempDir(), "state.db")
	d1, _ := db.Open(p)
	defer d1.Close()
	d2, _ := db.Open(p)
	defer d2.Close()
	chat := &Store{DB: d1, Loc: time.UTC, Now: c.Now}
	gw := &Store{DB: d2, Loc: time.UTC, Now: c.Now}
	j, err := chat.Create(ctx, Job{Schedule: "30m", Prompt: "x", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	due, _ := gw.Due(ctx, c.t.Add(30*time.Minute))
	if len(due) != 1 || due[0].ID != j.ID {
		t.Fatal(due)
	}
}

// setStreak writes fail_streak directly, so a test can start from any count.
func setStreak(t *testing.T, s *Store, id string, n int) {
	t.Helper()
	if _, err := s.DB.ExecContext(ctx, `UPDATE cron_jobs SET fail_streak=? WHERE id=?`, n, id); err != nil {
		t.Fatal(err)
	}
}

// fail_streak goes through the insert, the scan, and the full-row write.
func TestFailStreakStoredAndRead(t *testing.T) {
	s := store(t, fixedNow(2026, 11, 16, 10, 0))
	j, err := s.Create(ctx, Job{Schedule: "every 1h", Prompt: "x", Enabled: true})
	if err != nil || j.FailStreak != 0 {
		t.Fatal(j, err)
	}
	setStreak(t, s, j.ID, 7)
	got, err := s.Get(ctx, j.ID)
	if err != nil || got.FailStreak != 7 {
		t.Fatal(got.FailStreak, err)
	}
	if l, err := s.List(ctx); err != nil || len(l) != 1 || l[0].FailStreak != 7 {
		t.Fatal(l, err)
	}
	// A pause writes the full row. The count stays.
	if err := s.SetEnabled(ctx, j.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, j.ID); got.FailStreak != 7 || got.Enabled {
		t.Fatalf("%+v", got)
	}
}

// A resume sets the count to 0: SetEnabled(true) and an update with Enabled.
func TestResumeClearsFailStreak(t *testing.T) {
	s := store(t, fixedNow(2026, 11, 16, 10, 0))
	j, _ := s.Create(ctx, Job{Schedule: "every 1h", Prompt: "x", Enabled: true})
	if err := s.SetEnabled(ctx, j.ID, false); err != nil {
		t.Fatal(err)
	}
	setStreak(t, s, j.ID, 10)
	if err := s.SetEnabled(ctx, j.ID, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, j.ID); got.FailStreak != 0 || !got.Enabled {
		t.Fatalf("SetEnabled(true) clears the count: %+v", got)
	}
	if err := s.SetEnabled(ctx, j.ID, false); err != nil {
		t.Fatal(err)
	}
	setStreak(t, s, j.ID, 10)
	on := true
	got, err := s.Update(ctx, j.ID, JobPatch{Enabled: &on})
	if err != nil || got.FailStreak != 0 || !got.Enabled {
		t.Fatalf("an update with Enabled clears the count: %+v %v", got, err)
	}
}

// An edit of the task sets the count to 0. A rename, a new target, a new repeat count, or a
// patch with the same value keeps the count.
func TestUpdateFailStreakReset(t *testing.T) {
	str := func(v string) *string { return &v }
	repeat := 5
	for _, c := range []struct {
		name  string
		patch JobPatch
		want  int
	}{
		{"prompt", JobPatch{Prompt: str("y")}, 0},
		{"schedule", JobPatch{Schedule: str("every 2h")}, 0},
		{"toolsets", JobPatch{Toolsets: &[]string{"web"}}, 0},
		{"model", JobPatch{Model: &ModelPin{Provider: "cheap"}}, 0},
		{"same prompt", JobPatch{Prompt: str("x")}, 4},
		{"same schedule", JobPatch{Schedule: str("every 1h")}, 4},
		{"name", JobPatch{Name: str("renamed")}, 4},
		{"deliver", JobPatch{Deliver: str("local")}, 4},
		{"repeat", JobPatch{RepeatLeft: &repeat}, 4},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := store(t, fixedNow(2026, 11, 16, 10, 0))
			j, err := s.Create(ctx, Job{Schedule: "every 1h", Prompt: "x", Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			setStreak(t, s, j.ID, 4)
			got, err := s.Update(ctx, j.ID, c.patch)
			if err != nil || got.FailStreak != c.want {
				t.Fatalf("streak %d, want %d, err %v", got.FailStreak, c.want, err)
			}
		})
	}
}

func TestMarkRunWritesRunRow(t *testing.T) {
	c := fixedNow(2026, 11, 16, 10, 0)
	s := histStore(t, c, 50)
	j, _ := s.Create(ctx, Job{Schedule: "every 1h", Prompt: "x", Enabled: true})
	c.t = j.NextRun
	r := RunRecord{Status: "error", LastStatus: "delivery_failed", Delivery: DeliveryFailed, DeliveryErr: "flood wait",
		Output: "⚠ Cron job x failed: boom", SessionID: "19a8c4f0e10a2b7c41", RanAt: c.t,
		FinishedAt: c.t.Add(38 * time.Second)}
	if err := s.MarkRun(ctx, j.ID, r); err != nil {
		t.Fatal(err)
	}
	rows := storedRuns(t, s, j.ID)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	got := rows[0]
	if got.started != c.t.UnixMilli() || got.finished != c.t.Add(38*time.Second).UnixMilli() ||
		got.status != "error" || got.delivery != DeliveryFailed || got.output != r.Output ||
		got.deliveryErr != "flood wait" || got.sessionID != "19a8c4f0e10a2b7c41" {
		t.Fatalf("%+v", got)
	}
	job, _ := s.Get(ctx, j.ID)
	if job.LastStatus != "delivery_failed" || job.LastOutput != r.Output || job.FailStreak != 1 {
		t.Fatalf("%+v", job)
	}
}

// The prune keeps the newest RunHistory rows of the job that ran, and only of that job.
func TestMarkRunPrunesToRunHistory(t *testing.T) {
	c := fixedNow(2026, 11, 16, 10, 0)
	s := histStore(t, c, 3)
	a, _ := s.Create(ctx, Job{Name: "a", Schedule: "every 1h", Prompt: "x", Enabled: true})
	b, _ := s.Create(ctx, Job{Name: "b", Schedule: "every 1h", Prompt: "x", Enabled: true})
	if err := s.MarkRun(ctx, b.ID, runRec("ok", "b 1", c.t)); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		c.t = c.t.Add(time.Hour)
		if err := s.MarkRun(ctx, a.ID, runRec("ok", fmt.Sprintf("run %d", i), c.t)); err != nil {
			t.Fatal(err)
		}
	}
	rows := storedRuns(t, s, a.ID)
	if len(rows) != 3 || rows[0].output != "run 3" || rows[2].output != "run 5" {
		t.Fatal(rows)
	}
	if len(storedRuns(t, s, b.ID)) != 1 {
		t.Fatal("the prune deletes only the rows of the job that ran")
	}
}

// cron.run_history lowered to 0: a new run writes no row and deletes the old rows of the job.
func TestMarkRunHistoryZero(t *testing.T) {
	c := fixedNow(2026, 11, 16, 10, 0)
	s := histStore(t, c, 50)
	j, _ := s.Create(ctx, Job{Schedule: "every 1h", Prompt: "x", Enabled: true})
	for i := range 2 {
		c.t = c.t.Add(time.Hour)
		if err := s.MarkRun(ctx, j.ID, runRec("ok", fmt.Sprintf("run %d", i), c.t)); err != nil {
			t.Fatal(err)
		}
	}
	if rows := storedRuns(t, s, j.ID); len(rows) != 2 {
		t.Fatal(rows)
	}
	s.RunHistory = 0
	c.t = c.t.Add(time.Hour)
	if err := s.MarkRun(ctx, j.ID, runRec("ok", "run 2", c.t)); err != nil {
		t.Fatal(err)
	}
	if rows := storedRuns(t, s, j.ID); len(rows) != 0 {
		t.Fatal("cron.run_history 0 writes no row and deletes the old rows", rows)
	}
	if got, _ := s.Get(ctx, j.ID); got.LastOutput != "run 2" || got.LastStatus != "ok" {
		t.Fatalf("%+v", got)
	}
}

func TestTruncateRunes(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"", 5, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc…"},
		{"ééé", 2, "éé…"},
	} {
		if got := TruncateRunes(c.in, c.n); got != c.want {
			t.Errorf("TruncateRunes(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

// The row keeps 4000 runes of the output and "…". last_output keeps the full text.
func TestMarkRunCutsOutput(t *testing.T) {
	c := fixedNow(2026, 11, 16, 10, 0)
	s := histStore(t, c, 50)
	j, _ := s.Create(ctx, Job{Schedule: "every 1h", Prompt: "x", Enabled: true})
	long := strings.Repeat("é", RunOutputMaxRunes+1)
	if err := s.MarkRun(ctx, j.ID, runRec("ok", long, c.t)); err != nil {
		t.Fatal(err)
	}
	rows := storedRuns(t, s, j.ID)
	if want := strings.Repeat("é", 4000) + "…"; len(rows) != 1 || rows[0].output != want {
		t.Fatal(len(rows))
	}
	if got, _ := s.Get(ctx, j.ID); got.LastOutput != long {
		t.Fatal("last_output keeps the full text")
	}
}

// The count: error +1, stopped no change, ok 0 (also a silent run). The delivery has no effect.
func TestMarkRunFailStreak(t *testing.T) {
	c := fixedNow(2026, 11, 16, 10, 0)
	s := histStore(t, c, 50)
	j, _ := s.Create(ctx, Job{Schedule: "every 1h", Prompt: "x", Enabled: true})
	mark := func(r RunRecord, want int) {
		t.Helper()
		c.t = c.t.Add(time.Hour)
		r.RanAt, r.FinishedAt = c.t, c.t.Add(time.Second)
		fn := s.MarkRun
		if r.Status == StatusStopped {
			fn = s.MarkStopped
		}
		if err := fn(ctx, j.ID, r); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Get(ctx, j.ID); got.FailStreak != want || !got.Enabled {
			t.Fatalf("after %s/%s: streak %d, want %d (%+v)", r.Status, r.Delivery, got.FailStreak, want, got)
		}
	}
	failed := runRec("error", "e3", time.Time{})
	failed.LastStatus, failed.Delivery, failed.DeliveryErr = "delivery_failed", DeliveryFailed, "flood wait"
	silent := runRec("ok", "[SILENT]", time.Time{})
	silent.Delivery = DeliverySilent
	mark(runRec("error", "e1", time.Time{}), 1)
	mark(runRec("error", "e2", time.Time{}), 2)
	mark(stopRec("stopped", time.Time{}), 2)
	mark(failed, 3)
	mark(silent, 0)
	mark(runRec("error", "e4", time.Time{}), 1)
	mark(runRec("ok", "fine", time.Time{}), 0)
}

// The owner resumed the job during its 10th failed run, so the stored count is 0. The store
// follows the scheduler: it pauses the job after the repeat step and writes 10, not 1.
func TestMarkRunPauseWritesStreak(t *testing.T) {
	c := fixedNow(2026, 11, 16, 10, 0)
	s := histStore(t, c, 50)
	j, _ := s.Create(ctx, Job{Name: "Backup check", Schedule: "every 1h", Prompt: "x", RepeatLeft: 5, Enabled: true})
	c.t = j.NextRun
	r := runRec("error", "⚠ Cron job Backup check failed: boom\n\n⏸ The job Backup check ("+j.ID+
		") is paused: it failed 10 runs in a row. Fix the cause. Then ask me to resume the job.", c.t)
	r.Pause, r.Streak = true, 10
	if err := s.MarkRun(ctx, j.ID, r); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, j.ID)
	if got.Enabled || !got.NextRun.IsZero() || got.FailStreak != 10 || got.RepeatLeft != 4 ||
		got.LastStatus != "error" || got.LastOutput != r.Output {
		t.Fatalf("%+v", got)
	}
	if rows := storedRuns(t, s, j.ID); len(rows) != 1 || rows[0].status != "error" || rows[0].output != r.Output {
		t.Fatal(rows)
	}
	// A resume clears the count.
	if err := s.SetEnabled(ctx, j.ID, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, j.ID); !got.Enabled || got.FailStreak != 0 {
		t.Fatalf("%+v", got)
	}
}

// The bad-schedule branch writes a row with the run status, the record delivery, and the
// notice. It updates the count like a normal run. Pause has no effect there.
func TestMarkRunBadScheduleWritesRow(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     string
		pause      bool
		wantStreak int
	}{
		{"ok resets", "ok", false, 0},
		{"error counts, pause has no effect", "error", true, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixedNow(2026, 11, 16, 10, 0)
			s := histStore(t, c, 50)
			j, _ := s.Create(ctx, Job{Name: "a", Schedule: "every 1h", Prompt: "x", Enabled: true})
			if _, err := s.DB.ExecContext(ctx, `UPDATE cron_jobs SET schedule='sometimes', fail_streak=2 WHERE id=?`, j.ID); err != nil {
				t.Fatal(err)
			}
			r := runRec(tc.status, "run text", c.t)
			r.Pause, r.Streak = tc.pause, 10
			if err := s.MarkRun(ctx, j.ID, r); !errors.Is(err, errBadSchedule) {
				t.Fatal(err)
			}
			got, _ := s.Get(ctx, j.ID)
			if got.Enabled || got.LastStatus != "error" || got.FailStreak != tc.wantStreak ||
				!strings.Contains(got.LastOutput, `"sometimes"`) {
				t.Fatalf("%+v", got)
			}
			rows := storedRuns(t, s, j.ID)
			if len(rows) != 1 || rows[0].status != tc.status || rows[0].delivery != DeliverySent ||
				rows[0].output != got.LastOutput {
				t.Fatal(rows)
			}
		})
	}
}

// A stopped run writes status stopped and delivery none, keeps the count, and never pauses.
// The row of a second stop in a row has the stoppedTwiceLine.
func TestMarkStoppedWritesRow(t *testing.T) {
	c := fixedNow(2026, 11, 16, 10, 0)
	s := histStore(t, c, 50)
	j, _ := s.Create(ctx, Job{Schedule: "every 1h", Prompt: "x", Enabled: true})
	if _, err := s.DB.ExecContext(ctx, `UPDATE cron_jobs SET fail_streak=4 WHERE id=?`, j.ID); err != nil {
		t.Fatal(err)
	}
	c.t = j.NextRun
	r := RunRecord{Status: "error", LastStatus: "error", Delivery: DeliverySent, Output: "stopped",
		RanAt: c.t, FinishedAt: c.t.Add(time.Second), Pause: true, Streak: 10}
	if err := s.MarkStopped(ctx, j.ID, r); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, j.ID)
	if !got.Enabled || got.FailStreak != 4 || got.LastStatus != StatusStopped {
		t.Fatalf("%+v", got)
	}
	rows := storedRuns(t, s, j.ID)
	if len(rows) != 1 || rows[0].status != StatusStopped || rows[0].delivery != DeliveryNone || rows[0].output != "stopped" {
		t.Fatal(rows)
	}
	one, _ := s.Create(ctx, Job{Schedule: "2026-11-17T09:00", Prompt: "x", Enabled: true})
	c.t = one.NextRun
	if err := s.MarkStopped(ctx, one.ID, stopRec("stopped 1", c.t)); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(time.Minute)
	if err := s.MarkStopped(ctx, one.ID, stopRec("stopped 2", c.t)); err != nil {
		t.Fatal(err)
	}
	if rows := storedRuns(t, s, one.ID); len(rows) != 2 || rows[1].output != "stopped 2\n"+stoppedTwiceLine {
		t.Fatal(rows)
	}
}

// seedHistory records n runs of the job id, one hour apart, with the outputs "run 1" … "run n"
// and the sessions "sess1" … "sessn". Each run takes 30 s.
func seedHistory(t *testing.T, s *Store, c *clock, id string, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		c.t = c.t.Add(time.Hour)
		rec := RunRecord{Status: "ok", LastStatus: "ok", Delivery: DeliverySent, Output: fmt.Sprintf("run %d", i),
			SessionID: fmt.Sprintf("sess%d", i), RanAt: c.t, FinishedAt: c.t.Add(30 * time.Second)}
		if err := s.MarkRun(ctx, id, rec); err != nil {
			t.Fatal(err)
		}
	}
}

// runOutputs joins the outputs of rs with commas.
func runOutputs(rs []Run) string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Output
	}
	return strings.Join(out, ",")
}

func TestRunsNewestFirst(t *testing.T) {
	c := fixedNow(2026, 10, 27, 10, 0)
	s := store(t, c)
	s.RunHistory = 50
	wib := time.FixedZone("WIB", 7*3600)
	s.Loc = wib
	j, err := s.Create(ctx, Job{Schedule: "every 1h", Prompt: "x", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	seedHistory(t, s, c, j.ID, 3)
	got, err := s.Runs(ctx, j.ID, 10, 0)
	if err != nil || len(got) != 3 {
		t.Fatal(got, err)
	}
	for i, r := range got {
		n := 3 - i
		start := time.Date(2026, 10, 27, 10+n, 0, 0, 0, time.UTC)
		if r.JobID != j.ID || r.Output != fmt.Sprintf("run %d", n) || r.SessionID != fmt.Sprintf("sess%d", n) ||
			r.Status != "ok" || r.Delivery != DeliverySent || r.DeliveryErr != "" ||
			!r.StartedAt.Equal(start) || r.FinishedAt.Sub(r.StartedAt) != 30*time.Second ||
			r.StartedAt.Location() != wib || r.FinishedAt.Location() != wib {
			t.Fatalf("run %d: %+v", i, r)
		}
		if i > 0 && r.ID >= got[i-1].ID {
			t.Fatal("the runs are not newest first", got)
		}
	}
}

func TestRunsLimitAndOffset(t *testing.T) {
	c := fixedNow(2026, 10, 27, 10, 0)
	s := store(t, c)
	s.RunHistory = 50
	j, _ := s.Create(ctx, Job{Schedule: "every 1h", Prompt: "x", Enabled: true})
	seedHistory(t, s, c, j.ID, 5)
	for _, tc := range []struct {
		limit, offset int
		want          string
	}{
		{2, 0, "run 5,run 4"},
		{2, 2, "run 3,run 2"},
		{2, 4, "run 1"},
		{10, 5, ""},
	} {
		got, err := s.Runs(ctx, j.ID, tc.limit, tc.offset)
		if err != nil || runOutputs(got) != tc.want {
			t.Fatalf("limit %d, offset %d: %q, %v", tc.limit, tc.offset, runOutputs(got), err)
		}
	}
}

func TestRunOfAnotherJob(t *testing.T) {
	c := fixedNow(2026, 10, 27, 10, 0)
	s := store(t, c)
	s.RunHistory = 50
	a, _ := s.Create(ctx, Job{Name: "a", Schedule: "every 1h", Prompt: "x", Enabled: true})
	b, _ := s.Create(ctx, Job{Name: "b", Schedule: "every 1h", Prompt: "x", Enabled: true})
	seedHistory(t, s, c, a.ID, 1)
	seedHistory(t, s, c, b.ID, 1)
	ra, err := s.Runs(ctx, a.ID, 1, 0)
	if err != nil || len(ra) != 1 {
		t.Fatal(ra, err)
	}
	got, err := s.Run(ctx, a.ID, ra[0].ID)
	if err != nil || got != ra[0] {
		t.Fatal(got, err)
	}
	for _, tc := range []struct {
		job string
		id  int64
	}{
		{b.ID, ra[0].ID},
		{a.ID, 999999},
		{a.ID, 0},
	} {
		if _, err := s.Run(ctx, tc.job, tc.id); !errors.Is(err, ErrRunNotFound) {
			t.Fatalf("job %s, run %d: %v", tc.job, tc.id, err)
		}
	}
}

func TestRemoveDeletesRuns(t *testing.T) {
	c := fixedNow(2026, 10, 27, 10, 0)
	s := store(t, c)
	s.RunHistory = 50
	a, _ := s.Create(ctx, Job{Name: "a", Schedule: "every 1h", Prompt: "x", Enabled: true})
	b, _ := s.Create(ctx, Job{Name: "b", Schedule: "every 1h", Prompt: "x", Enabled: true})
	seedHistory(t, s, c, a.ID, 2)
	seedHistory(t, s, c, b.ID, 2)
	// An old gateway binary can remove a job and leave its rows.
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO cron_runs (job_id, started_at, finished_at, status, delivery)
		VALUES ('gone', 1, 2, 'ok', 'none')`); err != nil {
		t.Fatal(err)
	}
	count := func(jobID string) int {
		var n int
		if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM cron_runs WHERE job_id=?`, jobID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if err := s.Remove(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if count(a.ID) != 0 || count("gone") != 0 || count(b.ID) != 2 {
		t.Fatal(count(a.ID), count("gone"), count(b.ID))
	}
	if err := s.Remove(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if count(b.ID) != 2 {
		t.Fatal("a failed remove must not delete rows", count(b.ID))
	}
}

// The store masks secrets before it cuts the output. A cut first can split a secret, and then
// the exact-value redactor does not find the part that is left.
func TestMarkRunRedactsBeforeCut(t *testing.T) {
	c := fixedNow(2026, 11, 16, 10, 0)
	s := histStore(t, c, 50)
	s.Redact = func(text string) string { return strings.ReplaceAll(text, "SECRET-TOKEN-VALUE", "***") }
	j, _ := s.Create(ctx, Job{Schedule: "every 1h", Prompt: "x", Enabled: true})
	out := strings.Repeat("a", RunOutputMaxRunes-10) + "SECRET-TOKEN-VALUE"
	if err := s.MarkRun(ctx, j.ID, runRec("ok", out, c.t)); err != nil {
		t.Fatal(err)
	}
	rows := storedRuns(t, s, j.ID)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	if want := strings.Repeat("a", RunOutputMaxRunes-10) + "***"; rows[0].output != want {
		t.Fatalf("stored output ends with %q", rows[0].output[len(rows[0].output)-20:])
	}
}
