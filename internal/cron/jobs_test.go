package cron

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
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
	if err := s.MarkRun(ctx, j.ID, "ok", "done", c.t); err != nil {
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
		if err := s.MarkRun(ctx, j.ID, "ok", "", c.t); err != nil {
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
	if err := s.MarkRun(ctx, j.ID, "ok", "", ranAt); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, j.ID)
	if !got.NextRun.After(c.t) || got.RepeatLeft != -1 || !got.Enabled {
		t.Fatalf("%+v", got)
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
	if err := s.MarkRun(ctx, j.ID, "ok", "", c.t); !errors.Is(err, ErrNotFound) {
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
