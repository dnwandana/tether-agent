package cron

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// addRun writes one cron_runs row of the job. A nil cost writes NULL.
func addRun(t *testing.T, s *Store, jobID, status, delivery, output string, at time.Time, cost, median *float64, flag bool) {
	t.Helper()
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO cron_runs(job_id, started_at, finished_at, status, delivery,
		output, cost_usd, cost_median, cost_flag) VALUES(?,?,?,?,?,?,?,?,?)`,
		jobID, at.UnixMilli(), at.Add(time.Second).UnixMilli(), status, delivery, output, cost, median, boolInt(flag)); err != nil {
		t.Fatal(err)
	}
}

func TestRunsOK(t *testing.T) {
	c := fixedNow(2026, 11, 19, 10, 0)
	s := store(t, c)
	since := c.t.Add(-24 * time.Hour)
	addRun(t, s, "a", "ok", DeliverySent, "old", c.t.Add(-30*time.Hour), nil, nil, false)
	addRun(t, s, "a", "ok", DeliverySent, "x", c.t.Add(-2*time.Hour), nil, nil, false)
	addRun(t, s, "a", "error", DeliverySent, "y", c.t.Add(-time.Hour), nil, nil, false)
	addRun(t, s, "b", "ok", DeliverySilent, "z", c.t.Add(-time.Hour), nil, nil, false)
	if ok, total, err := s.RunsOK(ctx, "a", since); err != nil || ok != 1 || total != 2 {
		t.Fatal(ok, total, err)
	}
	if ok, total, err := s.RunsOK(ctx, "", since); err != nil || ok != 2 || total != 3 {
		t.Fatal(ok, total, err)
	}
	if ok, total, err := s.RunsOK(ctx, "none", since); err != nil || ok != 0 || total != 0 {
		t.Fatal(ok, total, err)
	}
}

// LastDelivered skips the silent, failed, and undelivered runs.
func TestLastDelivered(t *testing.T) {
	c := fixedNow(2026, 11, 19, 10, 0)
	s := store(t, c)
	if _, found, err := s.LastDelivered(ctx, "a"); err != nil || found {
		t.Fatal(found, err)
	}
	at := c.t.Add(-5 * time.Hour)
	addRun(t, s, "a", "ok", DeliverySent, "first report", at, nil, nil, false)
	addRun(t, s, "a", "ok", DeliveryLocal, "second report", at.Add(time.Hour), new(0.2), nil, false)
	addRun(t, s, "a", "ok", DeliverySilent, "[SILENT]", at.Add(2*time.Hour), nil, nil, false)
	addRun(t, s, "a", "error", DeliverySent, "⚠ failed", at.Add(3*time.Hour), nil, nil, false)
	addRun(t, s, "a", "ok", DeliveryFailed, "not sent", at.Add(4*time.Hour), nil, nil, false)
	addRun(t, s, "a", "ok", DeliveryHeld, "held", at.Add(4*time.Hour+time.Minute), nil, nil, false)
	r, found, err := s.LastDelivered(ctx, "a")
	if err != nil || !found || r.Output != "second report" || r.Delivery != DeliveryLocal ||
		!r.StartedAt.Equal(at.Add(time.Hour)) || r.CostUSD == nil || *r.CostUSD != 0.2 {
		t.Fatal(r, found, err)
	}
}

func TestPricedCosts(t *testing.T) {
	c := fixedNow(2026, 11, 19, 10, 0)
	s := store(t, c)
	at := c.t.Add(-10 * time.Hour)
	addRun(t, s, "a", "ok", DeliverySent, "", at, new(1.0), nil, false)
	addRun(t, s, "a", "ok", DeliverySent, "", at.Add(time.Hour), new(2.0), nil, false)
	addRun(t, s, "a", "ok", DeliverySent, "", at.Add(2*time.Hour), nil, nil, false)
	addRun(t, s, "a", "ok", DeliverySent, "", at.Add(3*time.Hour), new(3.0), nil, false)
	addRun(t, s, "b", "ok", DeliverySent, "", at.Add(3*time.Hour), new(9.0), nil, false)
	if got, err := s.PricedCosts(ctx, "a", 10); err != nil || !reflect.DeepEqual(got, []float64{3, 2, 1}) {
		t.Fatal(got, err)
	}
	if got, err := s.PricedCosts(ctx, "a", 2); err != nil || !reflect.DeepEqual(got, []float64{3, 2}) {
		t.Fatal(got, err)
	}
	if got, err := s.PricedCosts(ctx, "c", 10); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
}

// The count uses the cron sessions of the job, so the two starts of a retry count as two.
func TestModelRunsSince(t *testing.T) {
	c := fixedNow(2026, 11, 19, 10, 0)
	s := store(t, c)
	for i, row := range []struct {
		key string
		at  time.Time
	}{
		{"cron:a", c.t.Add(-time.Hour)},
		{"cron:a", c.t.Add(-23 * time.Hour)},
		{"cron:a", c.t.Add(-25 * time.Hour)},
		{"cron:b", c.t.Add(-time.Hour)},
		{"telegram:dm:1", c.t.Add(-time.Hour)},
	} {
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO sessions(id, session_key, kind, started_at) VALUES(?,?,?,?)`,
			string(rune('p'+i)), row.key, "cron", row.at.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.ModelRunsSince(ctx, "a", c.t.Add(-24*time.Hour)); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if n, err := s.ModelRunsSince(ctx, "z", c.t.Add(-24*time.Hour)); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}

func TestFlaggedRuns(t *testing.T) {
	c := fixedNow(2026, 11, 19, 10, 0)
	s := store(t, c)
	a := mustCreate(t, s, Job{Name: "digest", Schedule: "30m", Prompt: "x", Enabled: true})
	b := mustCreate(t, s, Job{Name: "prices", Schedule: "30m", Prompt: "x", Enabled: true})
	addRun(t, s, a, "ok", DeliverySent, "", c.t.Add(-30*time.Hour), new(5.0), new(1.0), true) // too old
	addRun(t, s, a, "ok", DeliverySent, "", c.t.Add(-3*time.Hour), new(2.5), new(0.5), true)
	addRun(t, s, a, "ok", DeliverySent, "", c.t.Add(-2*time.Hour), new(0.4), nil, false)
	addRun(t, s, b, "ok", DeliverySilent, "", c.t.Add(-time.Hour), new(0.9), new(0.2), true)
	got, err := s.FlaggedRuns(ctx, c.t.Add(-24*time.Hour))
	if err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
	want := []FlaggedRun{
		{JobID: b, JobName: "prices", At: c.t.Add(-time.Hour), CostUSD: 0.9, CostMedian: 0.2},
		{JobID: a, JobName: "digest", At: c.t.Add(-3 * time.Hour), CostUSD: 2.5, CostMedian: 0.5},
	}
	for i := range want {
		if got[i].JobID != want[i].JobID || got[i].JobName != want[i].JobName || !got[i].At.Equal(want[i].At) ||
			got[i].CostUSD != want[i].CostUSD || got[i].CostMedian != want[i].CostMedian {
			t.Fatalf("%d: %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestRunSoon(t *testing.T) {
	c := fixedNow(2026, 11, 19, 10, 0)
	s := store(t, c)
	id := mustCreate(t, s, Job{Name: "digest", Schedule: "0 1 * * *", Prompt: "x", Enabled: true})
	j, err := s.RunSoon(ctx, id)
	if err != nil || j.ID != id || !j.NextRun.Equal(c.t) || !mustGet(t, s, id).NextRun.Equal(c.t) {
		t.Fatal(j, err)
	}
	if err := s.SetEnabled(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunSoon(ctx, id); !errors.Is(err, ErrJobPaused) {
		t.Fatal(err)
	}
	if _, err := s.RunSoon(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

// ErrorSummary keeps the first line of the alert and drops the "⚠ Cron job <name>" prefix.
func TestErrorSummary(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"⚠ Cron job rss failed: connection refused\n\nI send no new alert while the job fails with the same error.",
			"connection refused"},
		{"⚠ Cron job rss failed (exit 3)\nstderr line", "exit 3"},
		{"⚠ Cron job rss failed (timed out after 30s)", "timed out after 30s"},
		{"⚠ Cron job rss finished, but the model gave no text.", "finished, but the model gave no text."},
		{"\n⚠ Cron job other failed: x", "⚠ Cron job other failed: x"},
		{"http 502 from the feed", "http 502 from the feed"},
		{"", ""},
	} {
		if got := ErrorSummary("rss", c.text); got != c.want {
			t.Errorf("ErrorSummary(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}
