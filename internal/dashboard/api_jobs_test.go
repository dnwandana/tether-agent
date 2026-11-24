package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// jobsFixture is a JobsAPI over a new state.db, with an active dashboard session.
type jobsFixture struct {
	d      *db.DB
	mux    *http.ServeMux
	jobs   *cron.Store
	held   *cron.HeldStore
	active *sessions.Session
	events []Event
	posted []cron.HeldSend
}

func newJobsFixture(t *testing.T, telegramOn bool) *jobsFixture {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ctx := context.Background()
	ss := sessions.New(d)
	active, err := ss.Create(ctx, sessions.Session{Key: SessionKey, Source: ChannelName, ChatID: OwnerChatID, Title: "Owner chat"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ss.SetActive(ctx, SessionKey, active.ID); err != nil {
		t.Fatal(err)
	}
	jobs := &cron.Store{DB: d, Loc: time.UTC, RunHistory: 10}
	f := &jobsFixture{d: d, jobs: jobs, held: &cron.HeldStore{DB: d}, active: active}
	tool := tools.CronJobTool{Store: jobs, Sessions: ss,
		PrivateTarget: func(_ context.Context, _ *tools.Env, deliver string) bool { return deliver == "local" }}
	api := NewJobsAPI(JobsDeps{
		Jobs: jobs, Held: f.held, Goals: &goals.Store{DB: d}, Sessions: ss, DB: d,
		Tool:       tool.Run,
		TelegramOn: telegramOn,
		PostHeld: func(_ context.Context, h cron.HeldSend) error {
			f.posted = append(f.posted, h)
			return nil
		},
		EditCard: func(cron.HeldSend, string) {},
		Publish:  func(e Event) { f.events = append(f.events, e) },
		Redact:   func(s string) string { return strings.ReplaceAll(s, "sk-secret", "[REDACTED]") },
		Now:      time.Now,
		ChatTitle: func(chatID, threadID string) (string, string, bool) {
			if chatID == "-100" && threadID == "7" {
				return "Ops", "Alerts", true
			}
			return "", "", false
		},
	})
	f.mux = http.NewServeMux()
	api.Register(f.mux)
	return f
}

func (f *jobsFixture) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func jobsDecode[T any](t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) T {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status %d, want %d: %s", rec.Code, wantStatus, rec.Body.String())
	}
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	return v
}

type jobsErrBody struct {
	Error string `json:"error"`
}

// The form runs the cronjob tool with the dashboard env, so the origin is the dashboard.
func TestJobsFormCreatesWithDashboardOrigin(t *testing.T) {
	f := newJobsFixture(t, false)
	rec := f.do(t, "POST", "/api/jobs",
		`{"action":"create","name":"nightly-backup","schedule":"0 3 * * *","prompt":"Back up the notes."}`)
	r := jobsDecode[ReplyView](t, rec, 200)
	if !strings.HasPrefix(r.Text, "created job ") {
		t.Fatalf("%q", r.Text)
	}
	l, err := f.jobs.List(context.Background())
	if err != nil || len(l) != 1 {
		t.Fatal(l, err)
	}
	j := l[0]
	if j.Origin.Source != "dashboard" || j.Origin.ChatID != "owner" || j.Origin.SessionKey != "dashboard:owner" ||
		j.CreatedBySession != f.active.ID {
		t.Fatalf("%+v", j)
	}
	rec = f.do(t, "POST", "/api/jobs", fmt.Sprintf(`{"action":"update","job_id":%q,"name":"backup"}`, j.ID))
	jobsDecode[ReplyView](t, rec, 200)
	rows := jobsDecode[[]JobRow](t, f.do(t, "GET", "/api/jobs", ""), 200)
	if len(rows) != 1 || rows[0].Name != "backup" {
		t.Fatalf("%+v", rows)
	}
}

func TestJobsFormRefusesOtherActions(t *testing.T) {
	f := newJobsFixture(t, false)
	for _, body := range []string{`{"action":"remove","job_id":"x"}`, `{"action":"list"}`, `{}`} {
		e := jobsDecode[jobsErrBody](t, f.do(t, "POST", "/api/jobs", body), 400)
		if e.Error != "The job form can only create or update a job." {
			t.Fatalf("%s: %q", body, e.Error)
		}
	}
}

// The draft-only check of the tool applies to the form.
func TestJobsFormDraftOnlyCheck(t *testing.T) {
	f := newJobsFixture(t, false)
	e := jobsDecode[jobsErrBody](t, f.do(t, "POST", "/api/jobs",
		`{"action":"create","schedule":"every 1h","prompt":"p","draft_only":true,"deliver":"telegram:-100123"}`), 400)
	if !strings.Contains(e.Error, cron.ErrDraftDeliver.Error()) {
		t.Fatalf("%q", e.Error)
	}
	if l, _ := f.jobs.List(context.Background()); len(l) != 0 {
		t.Fatalf("no job must exist: %+v", l)
	}
}

func TestJobsListDetailAndRuns(t *testing.T) {
	f := newJobsFixture(t, false)
	ctx := context.Background()
	j, err := f.jobs.Create(ctx, cron.Job{Name: "nightly-backup", Schedule: "0 3 * * *", Prompt: "Back up the notes.", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range []struct {
		status, out string
		cost        any
	}{{"ok", "first", nil}, {"error", "second", nil}, {"ok", "key sk-secret", 0.12}} {
		start := time.Now().Add(time.Duration(i-3) * time.Hour)
		if _, err := f.d.ExecContext(ctx, `INSERT INTO cron_runs (job_id, started_at, finished_at, status, delivery, output, cost_usd)
			VALUES (?, ?, ?, ?, 'sent', ?, ?)`, j.ID, start.UnixMilli(), start.Add(90*time.Second).UnixMilli(), r.status, r.out, r.cost); err != nil {
			t.Fatal(err)
		}
	}
	rows := jobsDecode[[]JobRow](t, f.do(t, "GET", "/api/jobs", ""), 200)
	if len(rows) != 1 || rows[0].ID != j.ID || rows[0].Name != "nightly-backup" || rows[0].Icon != "⏳" || rows[0].State != JobStatePending ||
		rows[0].OK != 2 || rows[0].HistoryOff {
		t.Fatalf("%+v", rows)
	}
	det := jobsDecode[JobDetail](t, f.do(t, "GET", "/api/jobs/"+j.ID, ""), 200)
	if det.Job.ID != j.ID || det.Prompt != "Back up the notes." || det.RunsTotal != 3 || len(det.Last7) != 3 ||
		det.Last7[0].Output != "key [REDACTED]" || det.Last7[0].CostUSD != 0.12 || det.Last7[0].DurationMS != 90000 ||
		det.WeekOK != 2 || det.WeekTotal != 3 {
		t.Fatalf("%+v", det)
	}
	page := jobsDecode[[]RunRow](t, f.do(t, "GET", "/api/jobs/"+j.ID+"/runs?offset=1&limit=1", ""), 200)
	if len(page) != 1 || page[0].Output != "second" || page[0].Status != "error" {
		t.Fatalf("%+v", page)
	}
	for _, p := range []string{"/api/jobs/nope", "/api/jobs/nope/runs"} {
		if e := jobsDecode[jobsErrBody](t, f.do(t, "GET", p, ""), 404); e.Error != "No job with this id." {
			t.Fatalf("%s: %q", p, e.Error)
		}
	}
}

func TestJobsBrief(t *testing.T) {
	f := newJobsFixture(t, false)
	ctx := context.Background()
	j, err := f.jobs.Create(ctx, cron.Job{Name: "watcher", Schedule: "every 1h", Prompt: "p", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.ExecContext(ctx, `UPDATE cron_jobs SET fail_streak=2, last_error='token sk-secret' WHERE id=?`, j.ID); err != nil {
		t.Fatal(err)
	}
	b := jobsDecode[BriefView](t, f.do(t, "GET", "/api/brief", ""), 200)
	if b.DefaultDeliver != "local" || len(b.Lines) != 1 {
		t.Fatalf("%+v", b)
	}
	l := b.Lines[0]
	if l.Kind != "failing" || l.Name != "watcher" || l.Count != 2 || l.JobID != j.ID ||
		strings.Contains(l.Detail, "sk-secret") || !strings.Contains(l.Detail, "[REDACTED]") {
		t.Fatalf("%+v", l)
	}
	on := newJobsFixture(t, true)
	if b := jobsDecode[BriefView](t, on.do(t, "GET", "/api/brief", ""), 200); b.DefaultDeliver != "origin" {
		t.Fatalf("%+v", b)
	}
}

func TestJobsHeldPostAndDrop(t *testing.T) {
	for _, telegramOn := range []bool{false, true} {
		f := newJobsFixture(t, telegramOn)
		ctx := context.Background()
		j, err := f.jobs.Create(ctx, cron.Job{Name: "digest", Schedule: "every 1h", Prompt: "p", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.held.Insert(ctx, cron.HeldSend{ID: "h1", Kind: "hold", JobID: j.ID, Target: "telegram:-1001",
			TargetTitle: "Family", Text: "token sk-secret", Reason: "names a person",
			Sources: []string{"https://example.com"}, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		held := jobsDecode[[]HeldView](t, f.do(t, "GET", "/api/held", ""), 200)
		if len(held) != 1 || held[0].Text != "token [REDACTED]" || held[0].JobName != "digest" || held[0].CanPost != telegramOn ||
			!reflect.DeepEqual(held[0].Actions, []Button{{Text: "❌ Drop", Data: "drop"}, {Text: `✅ Send to "Family"`, Data: "post"}}) {
			t.Fatalf("%+v", held)
		}
		path := "/api/held/" + held[0].ID
		if e := jobsDecode[jobsErrBody](t, f.do(t, "POST", path, `{"action":"keep"}`), 400); e.Error != `Use "post" or "drop".` {
			t.Fatalf("%q", e.Error)
		}
		// An unknown id is gone: ResolveHeld gives ErrHeldGone, which maps to 409.
		jobsDecode[jobsErrBody](t, f.do(t, "POST", "/api/held/abc", `{"action":"drop"}`), 409)
		if !telegramOn {
			e := jobsDecode[jobsErrBody](t, f.do(t, "POST", path, `{"action":"post"}`), 409)
			if e.Error != "Telegram is off, so the text cannot be posted. Drop it, or turn Telegram on." {
				t.Fatalf("%q", e.Error)
			}
			if r := jobsDecode[ReplyView](t, f.do(t, "POST", path, `{"action":"drop"}`), 200); r.Text == "" {
				t.Fatal("drop must give a result text")
			}
		} else {
			jobsDecode[ReplyView](t, f.do(t, "POST", path, `{"action":"post"}`), 200)
			if len(f.posted) != 1 || f.posted[0].Text != "token sk-secret" {
				t.Fatalf("the send gets the real text: %+v", f.posted)
			}
		}
		if left := jobsDecode[[]HeldView](t, f.do(t, "GET", "/api/held", ""), 200); len(left) != 0 {
			t.Fatalf("%+v", left)
		}
		if len(f.events) != 1 || f.events[0].Name != "held" {
			t.Fatalf("%+v", f.events)
		}
	}
}

func TestJobsHeldOriginalButtons(t *testing.T) {
	f := newJobsFixture(t, true)
	ctx := context.Background()
	j, _ := f.jobs.Create(ctx, cron.Job{Name: "digest", Schedule: "every 1h", Prompt: "p", Enabled: true})
	if err := f.held.Insert(ctx, cron.HeldSend{ID: "o1", Kind: "original", JobID: j.ID, Target: "telegram:-1001",
		Text: "full text", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	held := jobsDecode[[]HeldView](t, f.do(t, "GET", "/api/held", ""), 200)
	if len(held) != 1 || !reflect.DeepEqual(held[0].Actions, []Button{{Text: "✅ Post the full text there", Data: "post"}}) {
		t.Fatalf("%+v", held)
	}
}

// A job with no skills or toolsets sends empty lists, and the new fields have their values.
func TestJobsDetailEmptyListsLast7AndFailLimit(t *testing.T) {
	f := newJobsFixture(t, false)
	f.jobs.FailStreakLimit = 4
	ctx := context.Background()
	j, err := f.jobs.Create(ctx, cron.Job{Name: "plain", Schedule: "every 1h", Prompt: "p", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	empty := jobsDecode[[]JobRow](t, f.do(t, "GET", "/api/jobs", ""), 200)
	if len(empty) != 1 || empty[0].Last7 == nil || len(empty[0].Last7) != 0 {
		t.Fatalf("%+v", empty)
	}
	for i := range 9 {
		status := "ok"
		if i == 8 {
			status = "error"
		}
		start := time.Now().Add(time.Duration(i-20) * time.Hour)
		if _, err := f.d.ExecContext(ctx, `INSERT INTO cron_runs (job_id, started_at, finished_at, status, delivery, output)
			VALUES (?, ?, ?, ?, 'sent', '')`, j.ID, start.UnixMilli(), start.Add(time.Second).UnixMilli(), status); err != nil {
			t.Fatal(err)
		}
	}
	raw := f.do(t, "GET", "/api/jobs/"+j.ID, "").Body.String()
	if !strings.Contains(raw, `"skills":[]`) || !strings.Contains(raw, `"toolsets":[]`) {
		t.Fatalf("%s", raw)
	}
	det := jobsDecode[JobDetail](t, f.do(t, "GET", "/api/jobs/"+j.ID, ""), 200)
	want := []string{"ok", "ok", "ok", "ok", "ok", "ok", "error"}
	if det.FailLimit != 4 || !reflect.DeepEqual(det.Job.Last7, want) {
		t.Fatalf("%d %v", det.FailLimit, det.Job.Last7)
	}
}

// A run-mark error after the post or the drop still answers 200 and publishes the event.
// A send failure keeps the 409.
func TestJobsHeldMarkErrorAnswers200(t *testing.T) {
	f := newJobsFixture(t, true)
	ctx := context.Background()
	j, _ := f.jobs.Create(ctx, cron.Job{Name: "digest", Schedule: "every 1h", Prompt: "p", Enabled: true})
	start := time.Now()
	res, err := f.d.ExecContext(ctx, `INSERT INTO cron_runs (job_id, started_at, finished_at, status, delivery, output)
		VALUES (?, ?, ?, 'ok', 'held', '')`, j.ID, start.UnixMilli(), start.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := res.LastInsertId()
	if err := f.held.Insert(ctx, cron.HeldSend{ID: "h1", Kind: "hold", JobID: j.ID, RunID: runID, Target: "telegram:-1001",
		Text: "text", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.ExecContext(ctx, `CREATE TRIGGER no_mark BEFORE UPDATE ON cron_runs
		BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	r := jobsDecode[ReplyView](t, f.do(t, "POST", "/api/held/h1", `{"action":"drop"}`), 200)
	if r.Text != "Dropped." || len(f.events) != 1 || f.events[0].Name != "held" {
		t.Fatalf("%+v %+v", r, f.events)
	}
}

// The detail tells the watch limit, a masked prompt, and the cached title of the delivery chat.
func TestJobsDetailFlagsAndLabel(t *testing.T) {
	f := newJobsFixture(t, true)
	ctx := context.Background()
	j, err := f.jobs.Create(ctx, cron.Job{Name: "w", Schedule: "every 1h", Prompt: "use sk-secret", Script: "check.sh",
		Watch: true, Enabled: true, Deliver: "telegram:-100:7"})
	if err != nil {
		t.Fatal(err)
	}
	ss := sessions.New(f.d)
	for range cron.WatchModelRunsPerDay {
		if _, err := ss.Create(ctx, sessions.Session{Key: "cron:" + j.ID, Source: "cron"}); err != nil {
			t.Fatal(err)
		}
	}
	det := jobsDecode[JobDetail](t, f.do(t, "GET", "/api/jobs/"+j.ID, ""), 200)
	if !det.WatchLimitHit || !det.PromptMasked || det.DeliverLabel != "Ops › Alerts" {
		t.Fatalf("%+v", det)
	}
	plain, err := f.jobs.Create(ctx, cron.Job{Name: "p", Schedule: "every 1h", Prompt: "p", Enabled: true, Deliver: "telegram:55"})
	if err != nil {
		t.Fatal(err)
	}
	det = jobsDecode[JobDetail](t, f.do(t, "GET", "/api/jobs/"+plain.ID, ""), 200)
	if det.WatchLimitHit || det.PromptMasked || det.DeliverLabel != "" {
		t.Fatalf("no watch, no mask, and no cached title: %+v", det)
	}
}

// jobsCost writes one assistant message of the cron session "cron:<jobID>".
func jobsCost(t *testing.T, f *jobsFixture, sessionID, jobID string, cost any) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.d.ExecContext(ctx, `INSERT OR IGNORE INTO sessions (id, session_key, source) VALUES (?, ?, 'cron')`,
		sessionID, "cron:"+jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.ExecContext(ctx, `INSERT INTO messages (session_id, role, cost_usd, model, timestamp)
		VALUES (?, 'assistant', ?, 'm', ?)`, sessionID, cost, time.Now().Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

func TestJobsStateAndWeekCost(t *testing.T) {
	f := newJobsFixture(t, false)
	ctx := context.Background()
	mk := func(name string) cron.Job {
		t.Helper()
		j, err := f.jobs.Create(ctx, cron.Job{Name: name, Schedule: "every 1h", Prompt: "x", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	priced, unpriced, idle := mk("priced"), mk("unpriced"), mk("idle")
	jobsCost(t, f, "s1", priced.ID, 0.25)
	jobsCost(t, f, "s1", priced.ID, 0.5)
	jobsCost(t, f, "s2", unpriced.ID, nil)
	if err := f.jobs.SetEnabled(ctx, idle.ID, false); err != nil {
		t.Fatal(err)
	}
	check := func(rows []JobRow) {
		t.Helper()
		byName := map[string]JobRow{}
		for _, r := range rows {
			byName[r.Name] = r
		}
		if r := byName["priced"]; r.State != JobStatePending || r.Cost7dUSD != 0.75 || !r.Cost7dKnown || r.Cost7dUnpriced {
			t.Errorf("priced: %+v", r)
		}
		if r := byName["unpriced"]; r.Cost7dUSD != 0 || !r.Cost7dKnown || !r.Cost7dUnpriced {
			t.Errorf("unpriced: %+v", r)
		}
		if r := byName["idle"]; r.State != JobStatePaused || r.Cost7dKnown || r.Cost7dUSD != 0 {
			t.Errorf("idle: %+v", r)
		}
	}
	check(jobsDecode[[]JobRow](t, f.do(t, "GET", "/api/jobs", ""), 200))
	det := jobsDecode[JobDetail](t, f.do(t, "GET", "/api/jobs/"+priced.ID, ""), 200)
	if det.Job.Cost7dUSD != 0.75 || !det.Job.Cost7dKnown {
		t.Fatalf("detail: %+v", det.Job)
	}
	// With run history off, the cost still comes from the messages.
	f.jobs.RunHistory = 0
	rows := jobsDecode[[]JobRow](t, f.do(t, "GET", "/api/jobs", ""), 200)
	check(rows)
	for _, r := range rows {
		if !r.HistoryOff {
			t.Fatalf("history off: %+v", r)
		}
	}
	if det := jobsDecode[JobDetail](t, f.do(t, "GET", "/api/jobs/"+priced.ID, ""), 200); det.Job.Cost7dUSD != 0.75 || !det.Job.Cost7dKnown {
		t.Fatalf("detail with history off: %+v", det.Job)
	}
}

func TestJobsNoOldCostFields(t *testing.T) {
	f := newJobsFixture(t, false)
	j, err := f.jobs.Create(context.Background(), cron.Job{Name: "n", Schedule: "every 1h", Prompt: "p", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/api/jobs", "/api/jobs/" + j.ID} {
		rec := f.do(t, "GET", p, "")
		if rec.Code != 200 {
			t.Fatalf("%s: status %d: %s", p, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		for _, k := range []string{`"last_cost_usd"`, `"last_cost_known"`, `"week_cost_usd"`} {
			if strings.Contains(body, k) {
				t.Fatalf("%s: the key %s is still in the answer: %s", p, k, body)
			}
		}
		if !strings.Contains(body, `"cost_7d_usd"`) {
			t.Fatalf("%s: the key \"cost_7d_usd\" is not in the answer: %s", p, body)
		}
	}
}
