package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// cronNow is the fixed time of the cron tool tests.
var cronNow = time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)

// cronTool returns a cronjob tool over a new state.db with a fixed clock.
// CurrentProvider reads the provider name that tgEnv stores.
func cronTool(t *testing.T) CronJobTool {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return CronJobTool{
		Store: &cron.Store{DB: d, Loc: time.UTC, Now: func() time.Time { return cronNow }, RunHistory: 50,
			ValidToolset: func(s string) bool { return ValidToolset(s, nil) }},
		CurrentProvider: func(env *Env) string {
			p, _ := env.Values["test_provider"].(string)
			return p
		},
	}
}

// tgEnv returns an Env for the session key, with the current provider prov.
func tgEnv(key, prov string) *Env {
	return &Env{SessionID: "sess1", SessionKey: key, Values: map[string]any{"test_provider": prov}}
}

// onlyJob returns the one job of the tool store.
func onlyJob(t *testing.T, tool CronJobTool) cron.Job {
	t.Helper()
	jobs, err := tool.Store.List(context.Background())
	if err != nil || len(jobs) != 1 {
		t.Fatalf("%d jobs, %v", len(jobs), err)
	}
	return jobs[0]
}

func TestCreateCapturesOriginAndPinsProvider(t *testing.T) {
	tool := cronTool(t)
	env := tgEnv("telegram:group:-100:17585", "primary")
	out := runToolEnv(t, tool, `{"action":"create","name":"digest","schedule":"0 1 * * *","prompt":"GitHub trending digest","model":{"model":"glm-4.7"}}`, env)
	j := onlyJob(t, tool)
	if j.Origin.ChatID != "-100" || j.Origin.ThreadID != "17585" || j.Origin.Source != "telegram" ||
		j.Origin.SessionKey != env.SessionKey || j.Model == nil || j.Model.Provider != "primary" ||
		j.Model.Model != "glm-4.7" || !strings.Contains(out, j.ID) || !j.Enabled || j.CreatedBySession != "sess1" {
		t.Fatal(j, out)
	}
}

func TestCreateOriginFromSessionRow(t *testing.T) {
	tool := cronTool(t)
	// A per-user group key without a topic: the last part is the user id.
	s := sessions.New(tool.Store.DB)
	sess, err := s.Create(context.Background(), sessions.Session{Key: "telegram:group:-100:555", Source: "telegram", ChatID: "-100", UserID: "555"})
	if err != nil {
		t.Fatal(err)
	}
	tool.Sessions = s
	env := tgEnv(sess.Key, "primary")
	env.SessionID = sess.ID
	runToolEnv(t, tool, `{"action":"create","schedule":"30m","prompt":"p"}`, env)
	if j := onlyJob(t, tool); j.Origin.ChatID != "-100" || j.Origin.ThreadID != "" {
		t.Fatalf("%+v", j.Origin)
	}
}

func TestOriginFromKey(t *testing.T) {
	for key, want := range map[string]cron.Origin{
		"telegram:dm:42":               {Source: "telegram", ChatID: "42"},
		"telegram:dm:42:7":             {Source: "telegram", ChatID: "42", ThreadID: "7"},
		"telegram:group:-100":          {Source: "telegram", ChatID: "-100"},
		"telegram:group:-100:17585":    {Source: "telegram", ChatID: "-100", ThreadID: "17585"},
		"telegram:group:-100:17585:55": {Source: "telegram", ChatID: "-100", ThreadID: "17585"},
		"cli:user":                     {Source: "cli"},
		"":                             {},
	} {
		want.SessionKey = key
		if got := originFromKey(key); got != want {
			t.Errorf("%q: got %+v, want %+v", key, got, want)
		}
	}
}

func TestListShowsIDsScheduleNextRun(t *testing.T) {
	tool := cronTool(t)
	env := tgEnv("cli:user", "primary")
	if out := runToolEnv(t, tool, `{"action":"list"}`, env); !strings.Contains(out, "no cron jobs") {
		t.Fatal(out)
	}
	runToolEnv(t, tool, `{"action":"create","name":"digest","schedule":"0 1 * * *","prompt":"p"}`, env)
	j := onlyJob(t, tool)
	out := runToolEnv(t, tool, `{"action":"list"}`, env)
	for _, w := range []string{j.ID, "digest", "0 1 * * *", "2026-10-28 01:00 UTC", "enabled"} {
		if !strings.Contains(out, w) {
			t.Fatalf("list has no %q: %s", w, out)
		}
	}
}

func TestJobLineShowsFailStreak(t *testing.T) {
	tool := cronTool(t)
	env := tgEnv("cli:user", "primary")
	runToolEnv(t, tool, `{"action":"create","name":"digest","schedule":"every 1h","prompt":"p"}`, env)
	id := onlyJob(t, tool).ID
	for _, c := range []struct {
		enabled bool
		streak  int
		want    string
	}{
		{true, 0, "enabled"},
		{true, 1, "enabled (1 failure in a row)"},
		{true, 10, "enabled (10 failures in a row)"},
		{false, 0, "paused"},
		{false, 1, "paused (1 failure in a row)"},
		{false, 10, "paused (10 failures in a row)"},
	} {
		if _, err := tool.Store.DB.ExecContext(context.Background(),
			`UPDATE cron_jobs SET enabled=?, fail_streak=? WHERE id=?`, c.enabled, c.streak, id); err != nil {
			t.Fatal(err)
		}
		j := onlyJob(t, tool)
		if j.FailStreak != c.streak {
			t.Fatalf("FailStreak %d, want %d", j.FailStreak, c.streak)
		}
		if line := tool.jobLine(j); !strings.Contains(line, " · digest · "+c.want+" · agent · ") {
			t.Errorf("enabled %v, streak %d: %s", c.enabled, c.streak, line)
		}
		if out := runToolEnv(t, tool, `{"action":"list"}`, env); !strings.Contains(out, " · digest · "+c.want+" · agent · ") {
			t.Errorf("list, enabled %v, streak %d: %s", c.enabled, c.streak, out)
		}
	}
}

func TestPauseResumeRemove(t *testing.T) {
	tool := cronTool(t)
	env := tgEnv("cli:user", "primary")
	runToolEnv(t, tool, `{"action":"create","schedule":"every 2h","prompt":"p"}`, env)
	id := onlyJob(t, tool).ID
	runToolEnv(t, tool, fmt.Sprintf(`{"action":"pause","job_id":%q}`, id), env)
	if onlyJob(t, tool).Enabled {
		t.Fatal("pause must disable the job")
	}
	runToolEnv(t, tool, fmt.Sprintf(`{"action":"resume","job_id":%q}`, id), env)
	if !onlyJob(t, tool).Enabled {
		t.Fatal("resume must enable the job")
	}
	runToolEnv(t, tool, fmt.Sprintf(`{"action":"remove","job_id":%q}`, id), env)
	if jobs, _ := tool.Store.List(context.Background()); len(jobs) != 0 {
		t.Fatal("remove must delete the job")
	}
	if out := runToolEnvErr(t, tool, fmt.Sprintf(`{"action":"remove","job_id":%q}`, id), env); !strings.Contains(out, "list") {
		t.Fatal(out)
	}
}

func TestUpdateClearsSkillsAndPins(t *testing.T) {
	tool := cronTool(t)
	env := tgEnv("cli:user", "cheap")
	runToolEnv(t, tool, `{"action":"create","schedule":"30m","prompt":"p","skills":["a","b"],"toolsets":["web"]}`, env)
	id := onlyJob(t, tool).ID
	runToolEnv(t, tool, fmt.Sprintf(`{"action":"update","job_id":%q,"skills":[],"model":{"model":"m2"},"repeat":3,"name":"new"}`, id), env)
	j := onlyJob(t, tool)
	if len(j.Skills) != 0 || len(j.Toolsets) != 1 || j.Model == nil || j.Model.Provider != "cheap" || j.RepeatLeft != 3 || j.Name != "new" {
		t.Fatalf("%+v", j)
	}
	if out := runToolEnvErr(t, tool, fmt.Sprintf(`{"action":"update","job_id":%q}`, id), env); !strings.Contains(out, "no fields") {
		t.Fatal(out)
	}
}

func TestRunWithoutGatewayMarksDue(t *testing.T) {
	tool := cronTool(t)
	env := tgEnv("cli:user", "primary")
	runToolEnv(t, tool, `{"action":"create","schedule":"0 1 * * *","prompt":"p"}`, env)
	id := onlyJob(t, tool).ID
	out := runToolEnv(t, tool, fmt.Sprintf(`{"action":"run","job_id":%q}`, id), env)
	if !onlyJob(t, tool).NextRun.Equal(cronNow) || !strings.Contains(out, "the gateway runs it on the next tick") {
		t.Fatal(onlyJob(t, tool).NextRun, out)
	}
}

func TestRunWithoutGatewayTellsMissedRule(t *testing.T) {
	env := tgEnv("cli:user", "primary")
	runText := func(tool CronJobTool, schedule string) string {
		t.Helper()
		runToolEnv(t, tool, fmt.Sprintf(`{"action":"create","schedule":%q,"prompt":"p"}`, schedule), env)
		return runToolEnv(t, tool, fmt.Sprintf(`{"action":"run","job_id":%q}`, onlyJob(t, tool).ID), env)
	}
	// With "skip", a recurring run that starts late by more than the grace window is skipped.
	for _, missed := range []string{"", "skip"} {
		tool := cronTool(t)
		tool.MissedRuns = missed
		out := runText(tool, "0 1 * * *")
		if !strings.Contains(out, "grace window") || !strings.Contains(out, "next scheduled time") ||
			strings.Contains(out, "the job runs when it starts") {
			t.Fatalf("%q: %s", missed, out)
		}
	}
	tool := cronTool(t)
	tool.MissedRuns = "catchup"
	if out := runText(tool, "0 1 * * *"); !strings.Contains(out, "the job runs when it starts") || strings.Contains(out, "grace") {
		t.Fatal(out)
	}
	// A one-shot job always runs late, because it has no next run.
	tool = cronTool(t)
	at := cronNow.Add(time.Hour).Format("2006-01-02T15:04")
	if out := runText(tool, at); !strings.Contains(out, "the job runs when it starts") || strings.Contains(out, "grace") {
		t.Fatal(out)
	}
}

func TestRunWithGatewayCallsRunNow(t *testing.T) {
	tool := cronTool(t)
	var got string
	tool.RunNow = func(_ context.Context, id string) error { got = id; return nil }
	env := tgEnv("cli:user", "primary")
	runToolEnv(t, tool, `{"action":"create","schedule":"0 1 * * *","prompt":"p"}`, env)
	id := onlyJob(t, tool).ID
	out := runToolEnv(t, tool, fmt.Sprintf(`{"action":"run","job_id":%q}`, id), env)
	want := fmt.Sprintf("started job %s (%s); the result goes to its delivery target, unless the run answers [SILENT]",
		id, onlyJob(t, tool).Name)
	if got != id || out != want {
		t.Fatalf("RunNow got %q; reply %q, want %q", got, out, want)
	}
	tool.RunNow = func(context.Context, string) error { return errors.New("busy") }
	if out := runToolEnvErr(t, tool, fmt.Sprintf(`{"action":"run","job_id":%q}`, id), env); !strings.Contains(out, "busy") {
		t.Fatal(out)
	}
}

func TestValidationErrorsReachModel(t *testing.T) {
	tool := cronTool(t)
	env := tgEnv("cli:user", "primary")
	for args, want := range map[string]string{
		`{"action":"create","prompt":"p"}`:                                   "schedule is required",
		`{"action":"create","schedule":"30m"}`:                               "prompt",
		`{"action":"create","schedule":"30m","prompt":"p","workdir":"rel"}`:  "absolute",
		`{"action":"create","schedule":"30m","prompt":"p","toolsets":["x"]}`: "unknown toolset",
		`{"action":"pause"}`:                     "job_id is required",
		`{"action":"pause","job_id":"deadbeef"}`: "list",
		`{"action":"nope"}`:                      "unknown action",
	} {
		if out := runToolEnvErr(t, tool, args, env); !strings.Contains(out, want) {
			t.Errorf("%s: %s", args, out)
		}
	}
}

func TestScriptPathChecked(t *testing.T) {
	tool := cronTool(t)
	tool.ScriptsDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(tool.ScriptsDir, "check.sh"), []byte("echo hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := tgEnv("cli:user", "primary")
	runToolEnvErr(t, tool, `{"action":"create","schedule":"30m","no_agent":true,"script":"missing.sh"}`, env)
	runToolEnv(t, tool, `{"action":"create","schedule":"30m","no_agent":true,"script":"check.sh"}`, env)
	if j := onlyJob(t, tool); !j.NoAgent || j.Script != "check.sh" {
		t.Fatalf("%+v", j)
	}
}

func TestDescriptionHasSilentNote(t *testing.T) {
	d := CronJobTool{}.Schema().Description
	const note = "NOTE: The agent's final response is auto-delivered to the target. When a run finds nothing new, " +
		"the agent answers [SILENT], and nothing is sent. A failed run always sends an alert, except with " +
		"deliver='local'. Put the primary user-facing content in the final response. Cron jobs run autonomously " +
		"with no user present — they cannot ask questions or request clarification."
	if !strings.Contains(d, note) || !strings.Contains(d, "[SILENT]") {
		t.Fatal("the description has no [SILENT] NOTE:", d)
	}
	if strings.Contains(d, "context_from") || strings.Contains(d, "on every run") {
		t.Fatal(d)
	}
	for _, w := range []string{"Never guess job IDs", "self-contained", "empty stdout sends nothing", "always alerts"} {
		if !strings.Contains(CronJobTool{}.Schema().Description+string(CronJobTool{}.Schema().Parameters), w) {
			t.Errorf("the description has no %q", w)
		}
	}
	if (CronJobTool{}).Toolset() != SetCron || (CronJobTool{}).Name() != "cronjob" {
		t.Fatal("bad tool metadata")
	}
}

func TestCronjobWrappedCreateGetsNote(t *testing.T) {
	reg := NewRegistry()
	reg.Register(cronTool(t))
	args := json.RawMessage(`{"action":"create","args":{"schedule":"0 1 * * *","prompt":"p"}}`)
	res := reg.Dispatch(ctx, "cronjob", args, newEnv(t), map[string]bool{"cronjob": true})
	if !res.IsError || !strings.HasPrefix(res.Text, "cron: schedule is required") {
		t.Fatalf("result = %+v", res)
	}
	for _, s := range []string{"Tether ignored these unknown arguments: args.", "The known arguments are: action, job_id, prompt, schedule,"} {
		if !strings.Contains(res.Text, s) {
			t.Fatalf("result %q does not contain %q", res.Text, s)
		}
	}
}

func TestRunTextShowsConfiguredTimes(t *testing.T) {
	env := tgEnv("cli:user", "primary")
	runText := func(tool CronJobTool) string {
		t.Helper()
		runToolEnv(t, tool, `{"action":"create","schedule":"0 1 * * *","prompt":"p"}`, env)
		return runToolEnv(t, tool, fmt.Sprintf(`{"action":"run","job_id":%q}`, onlyJob(t, tool).ID), env)
	}
	out := runText(cronTool(t))
	if !strings.Contains(out, "on the next tick (within 30 s). ") ||
		!strings.Contains(out, "(half the period of the schedule, from 2 min to 2 h)") {
		t.Fatal("zero fields keep the text of today:", out)
	}
	tool := cronTool(t)
	tool.Tick, tool.GraceMin, tool.GraceMax = 45*time.Second, 5*time.Minute, 3*time.Hour
	out = runText(tool)
	if !strings.Contains(out, "(within 45 s)") || !strings.Contains(out, "from 5 min to 3 h)") {
		t.Fatal(out)
	}
	tool = cronTool(t)
	tool.Tick, tool.GraceMin, tool.GraceMax = 90*time.Second, 150*time.Second, 90*time.Minute
	out = runText(tool)
	if !strings.Contains(out, "(within 90 s)") || !strings.Contains(out, "from 150 s to 90 min)") {
		t.Fatal(out)
	}
}

// newRunsJob creates an agent job with the name as the local owner and returns its id.
func newRunsJob(t *testing.T, tool CronJobTool, name string) string {
	t.Helper()
	out := runToolEnv(t, tool, fmt.Sprintf(`{"action":"create","name":%q,"schedule":"every 1h","prompt":"p"}`, name),
		tgEnv("cli:user", "primary"))
	return strings.Fields(strings.SplitN(out, "\n", 2)[0])[2]
}

// seedRun records one run of the job through Store.MarkRun. An empty LastStatus copies Status.
func seedRun(t *testing.T, tool CronJobTool, id string, rec cron.RunRecord) {
	t.Helper()
	if rec.LastStatus == "" {
		rec.LastStatus = rec.Status
	}
	if err := tool.Store.MarkRun(context.Background(), id, rec); err != nil {
		t.Fatal(err)
	}
}

// seedRuns records n good runs with the outputs "out 1" to "out n". The run ids are 1 to n
// when the job is the first job with runs.
func seedRuns(t *testing.T, tool CronJobTool, id string, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		at := cronNow.Add(time.Duration(i-n) * time.Minute)
		seedRun(t, tool, id, cron.RunRecord{Status: "ok", Delivery: cron.DeliverySent,
			Output: fmt.Sprintf("out %d", i), RanAt: at, FinishedAt: at.Add(time.Second)})
	}
}

// runsText calls the runs action as the local owner. extra is more JSON fields, each with a comma first.
func runsText(t *testing.T, tool CronJobTool, id, extra string) string {
	t.Helper()
	out := runToolEnv(t, tool, fmt.Sprintf(`{"action":"runs","job_id":%q%s}`, id, extra), tgEnv("cli:user", "primary"))
	return strings.TrimRight(out, "\n")
}

func TestCronRunsList(t *testing.T) {
	tool := cronTool(t)
	id := newRunsJob(t, tool, "disk")
	at := func(h, m int) time.Time { return time.Date(2026, 10, 27, h, m, 0, 0, time.UTC) }
	seedRun(t, tool, id, cron.RunRecord{Status: "error", LastStatus: "delivery_failed", Delivery: cron.DeliveryFailed,
		DeliveryErr: "chat not found", Output: "⚠ Cron job disk failed", RanAt: at(7, 0), FinishedAt: at(7, 2)})
	seedRun(t, tool, id, cron.RunRecord{Status: "ok", Delivery: cron.DeliverySilent, Output: "[SILENT]",
		SessionID: "19a8bd8a3c09f1e2d80", RanAt: at(8, 0), FinishedAt: at(8, 0).Add(21 * time.Second)})
	seedRun(t, tool, id, cron.RunRecord{Status: "ok", Delivery: cron.DeliverySent, Output: "line one\nline two",
		SessionID: "19a8c4f0e10a2b7c41d", RanAt: at(9, 0), FinishedAt: at(9, 0).Add(38*time.Second + 400*time.Millisecond)})
	// A script job has no session and can have no output. 1.5 s rounds to 2 s.
	seedRun(t, tool, id, cron.RunRecord{Status: "ok", Delivery: cron.DeliveryNone,
		RanAt: at(9, 30), FinishedAt: at(9, 30).Add(1500 * time.Millisecond)})
	want := "4 runs of job " + id + " (disk), newest first:\n" +
		"- run 4 · 2026-10-27 09:30 UTC · ok · none · 2s\n" +
		"  (no output)\n" +
		"- run 3 · 2026-10-27 09:00 UTC · ok · sent · 38s · session 19a8c4f0e10a2b7c41d\n" +
		"  line one\n" +
		"  line two\n" +
		"- run 2 · 2026-10-27 08:00 UTC · ok · silent · 21s · session 19a8bd8a3c09f1e2d80\n" +
		"  [SILENT]\n" +
		"- run 1 · 2026-10-27 07:00 UTC · error · failed · 2m0s · delivery error: chat not found\n" +
		"  ⚠ Cron job disk failed"
	if got := runsText(t, tool, id, ""); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestCronRunsLimitAndOffset(t *testing.T) {
	tool := cronTool(t)
	tool.Store.RunHistory = 60
	id := newRunsJob(t, tool, "disk")
	seedRuns(t, tool, id, 51)
	// The header names the offset, not a run id, so the model does not take it for a run_id.
	head := func(n, from int) string {
		if from == 1 {
			return fmt.Sprintf("%d runs of job %s (disk), newest first:\n", n, id)
		}
		return fmt.Sprintf("%d runs of job %s (disk), newest first, after the newest %d:\n", n, id, from-1)
	}
	for _, c := range []struct {
		extra, head, first, last, more string
	}{
		{"", head(10, 1), "- run 51 · ", "- run 42 · ", "More runs exist: use offset 10."},
		{`,"limit":0`, head(10, 1), "- run 51 · ", "- run 42 · ", "More runs exist: use offset 10."},
		{`,"limit":-3`, head(10, 1), "- run 51 · ", "- run 42 · ", "More runs exist: use offset 10."},
		{`,"limit":100`, head(50, 1), "- run 51 · ", "- run 2 · ", "More runs exist: use offset 50."},
		{`,"limit":2`, head(2, 1), "- run 51 · ", "- run 50 · ", "More runs exist: use offset 2."},
		{`,"offset":-5`, head(10, 1), "- run 51 · ", "- run 42 · ", "More runs exist: use offset 10."},
		{`,"offset":10,"limit":1`, head(1, 11), "- run 41 · ", "- run 41 · ", "More runs exist: use offset 11."},
		{`,"offset":49`, head(2, 50), "- run 2 · ", "- run 1 · ", ""},
	} {
		got := runsText(t, tool, id, c.extra)
		if !strings.HasPrefix(got, c.head+c.first) || !strings.Contains(got, c.last) {
			t.Errorf("%s: %s", c.extra, got)
		}
		if c.more != "" && !strings.HasSuffix(got, "\n"+c.more) {
			t.Errorf("%s: no %q at the end: %s", c.extra, c.more, got)
		}
		if c.more == "" && strings.Contains(got, "More runs exist") {
			t.Errorf("%s: %s", c.extra, got)
		}
	}
	if got := runsText(t, tool, id, `,"offset":51`); got != "no runs recorded for job "+id+" (disk)" {
		t.Fatal(got)
	}
}

func TestCronRunsOutputCut(t *testing.T) {
	tool := cronTool(t)
	id := newRunsJob(t, tool, "disk")
	long, huge := strings.Repeat("a", 400), strings.Repeat("b", 5000)
	seedRun(t, tool, id, cron.RunRecord{Status: "ok", Delivery: cron.DeliverySent, Output: long, RanAt: cronNow, FinishedAt: cronNow})
	seedRun(t, tool, id, cron.RunRecord{Status: "ok", Delivery: cron.DeliverySent, Output: huge, RanAt: cronNow, FinishedAt: cronNow})
	list := runsText(t, tool, id, "")
	if !strings.Contains(list, "\n  "+strings.Repeat("a", 300)+"…") || strings.Contains(list, strings.Repeat("a", 301)) {
		t.Fatal("the list cuts each output to 300 runes:", list)
	}
	// With run_id, the result has the full stored output.
	one := runsText(t, tool, id, `,"run_id":1`)
	if !strings.HasPrefix(one, "- run 1 · ") || !strings.Contains(one, "\n  "+long) || strings.Contains(one, "runs of job") {
		t.Fatal(one)
	}
	if two := runsText(t, tool, id, `,"run_id":2`); !strings.Contains(two, "\n  "+cron.TruncateRunes(huge, cron.RunOutputMaxRunes)) {
		t.Fatal("run_id shows the stored output, cut to 4000 runes:", len(two))
	}
}

func TestCronRunIDErrors(t *testing.T) {
	tool := cronTool(t)
	env := tgEnv("cli:user", "primary")
	id := newRunsJob(t, tool, "disk")
	other := newRunsJob(t, tool, "other")
	seedRuns(t, tool, id, 1)    // run 1
	seedRuns(t, tool, other, 1) // run 2
	for _, runID := range []string{"0", "2", "999"} {
		out := runToolEnvErr(t, tool, fmt.Sprintf(`{"action":"runs","job_id":%q,"run_id":%s}`, id, runID), env)
		if !strings.Contains(out, cron.ErrRunNotFound.Error()) {
			t.Errorf("run_id %s: %s", runID, out)
		}
	}
	if out := runToolEnvErr(t, tool, `{"action":"runs"}`, env); !strings.Contains(out, "job_id is required for runs") {
		t.Fatal(out)
	}
}

func TestCronRunsNone(t *testing.T) {
	tool := cronTool(t)
	id := newRunsJob(t, tool, "disk")
	if got := runsText(t, tool, id, ""); got != "no runs recorded for job "+id+" (disk)" {
		t.Fatal(got)
	}
	tool.Store.RunHistory = 0
	want := "no runs recorded for job " + id + " (disk); cron.run_history is 0, so no runs are kept"
	if got := runsText(t, tool, id, ""); got != want {
		t.Fatal(got)
	}
}

func TestCronRunsOtherChat(t *testing.T) {
	f := newScopeFixture(t)
	f.jobs.RunHistory = 50
	tool := CronJobTool{Store: f.jobs, Sessions: f.store}
	created := runToolEnv(t, tool, `{"action":"create","name":"alice job","prompt":"p","schedule":"every 1h"}`, f.aliceEnv)
	id := strings.Fields(strings.SplitN(created, "\n", 2)[0])[2]
	now := time.Now()
	if err := f.jobs.MarkRun(context.Background(), id, cron.RunRecord{Status: "ok", LastStatus: "ok",
		Delivery: cron.DeliverySent, Output: "alice secret", RanAt: now, FinishedAt: now}); err != nil {
		t.Fatal(err)
	}
	// A job of another chat gives the same error as a job that does not exist.
	for _, args := range []string{`{"action":"runs","job_id":"` + id + `"}`, `{"action":"runs","job_id":"` + id + `","run_id":1}`} {
		out := runToolEnvErr(t, tool, args, f.bobEnv)
		if !strings.Contains(out, "cron: job not found") || strings.Contains(out, "alice secret") {
			t.Fatal(args, out)
		}
	}
	for _, env := range []*Env{f.aliceEnv, f.localEnv} {
		if out := runToolEnv(t, tool, `{"action":"runs","job_id":"`+id+`"}`, env); !strings.Contains(out, "alice secret") {
			t.Fatal(env.SessionKey, out)
		}
	}
}

func TestCronjobSchemaHasRuns(t *testing.T) {
	s := CronJobTool{}.Schema()
	if !json.Valid(s.Parameters) {
		t.Fatal("the parameters are not valid JSON")
	}
	p := string(s.Parameters)
	for _, w := range []string{`"remove", "run", "runs"]`, "One of: create, list, update, pause, resume, remove, run, runs.",
		"Required for update/pause/resume/remove/run/runs.", `"limit": {"type": "integer"`,
		`"offset": {"type": "integer"`, `"run_id": {"type": "integer"`} {
		if !strings.Contains(p, w) {
			t.Errorf("the parameters have no %q", w)
		}
	}
	// The new arguments come after workdir, so the known arguments still start with action, job_id, prompt, schedule.
	if w, l, o, r := strings.Index(p, `"workdir"`), strings.Index(p, `"limit"`), strings.Index(p, `"offset"`), strings.Index(p, `"run_id"`); w >= l || l >= o || o >= r {
		t.Fatal("limit, offset, and run_id must follow workdir:", p)
	}
	const lines = "Use action='update', 'pause', 'resume', 'remove', or 'run' to manage an existing job.\n" +
		"Use action='runs' with job_id to see the last runs of a job: the status, the delivery, the duration, and the output.\n"
	if !strings.Contains(s.Description, lines) {
		t.Fatal(s.Description)
	}
	tool := cronTool(t)
	if out := runToolEnvErr(t, tool, `{"action":"nope"}`, tgEnv("cli:user", "primary")); !strings.Contains(out,
		"use create, list, update, pause, resume, remove, run, or runs") {
		t.Fatal(out)
	}
}

func TestCronDraftOnlyCreateAndList(t *testing.T) {
	tool := cronTool(t)
	tool.Store.IsPrivate = func(context.Context, cron.Target) bool { return true }
	var calls []string
	tool.PrivateTarget = func(_ context.Context, _ *Env, deliver string) bool {
		calls = append(calls, deliver)
		return deliver != "telegram:-100"
	}
	env := tgEnv("telegram:dm:5", "primary")
	out := runToolEnv(t, tool, `{"action":"create","schedule":"every 2h","prompt":"Draft replies to new mail","draft_only":true}`, env)
	j := onlyJob(t, tool)
	if !j.DraftOnly || !strings.Contains(out, "agent, draft-only") || len(calls) != 1 || calls[0] != "" {
		t.Fatal(out, j, calls)
	}
	list := runToolEnv(t, tool, `{"action":"list"}`, env)
	if !strings.Contains(list, "agent, draft-only") {
		t.Fatal(list)
	}
}

func TestCronDraftOnlyRefusesGroup(t *testing.T) {
	tool := cronTool(t)
	tool.Store.IsPrivate = func(context.Context, cron.Target) bool { return true }
	tool.PrivateTarget = func(_ context.Context, _ *Env, deliver string) bool { return deliver != "telegram:-100" }
	env := tgEnv("telegram:group:-100", "primary")
	out := runToolEnvErr(t, tool, `{"action":"create","schedule":"every 2h","prompt":"p","draft_only":true,"deliver":"telegram:-100"}`, env)
	if out != "A draft-only job delivers only to a private chat or local." {
		t.Fatalf("%q", out)
	}
	// An update that turns on draft_only checks the stored target.
	runToolEnv(t, tool, `{"action":"create","schedule":"every 2h","prompt":"p","deliver":"telegram:-100"}`, env)
	j := onlyJob(t, tool)
	out = runToolEnvErr(t, tool, fmt.Sprintf(`{"action":"update","job_id":%q,"draft_only":true}`, j.ID), env)
	if out != "A draft-only job delivers only to a private chat or local." {
		t.Fatalf("%q", out)
	}
	// draft_only alone is a field to change.
	out = runToolEnv(t, tool, fmt.Sprintf(`{"action":"update","job_id":%q,"draft_only":false}`, j.ID), env)
	if !strings.HasPrefix(out, "updated job ") {
		t.Fatal(out)
	}
}

func TestCronDraftOnlyStoreCheckWithoutToolCheck(t *testing.T) {
	tool := cronTool(t)
	// No PrivateTarget and no IsPrivate: only local passes.
	env := tgEnv("telegram:group:-100", "primary")
	out := runToolEnvErr(t, tool, `{"action":"create","schedule":"every 2h","prompt":"p","draft_only":true}`, env)
	if !strings.Contains(out, "A draft-only job delivers only to a private chat or local.") {
		t.Fatalf("%q", out)
	}
	runToolEnv(t, tool, `{"action":"create","schedule":"every 2h","prompt":"p","draft_only":true,"deliver":"local"}`, env)
}

func TestCronUpdateNoFieldsNamesDraftOnly(t *testing.T) {
	tool := cronTool(t)
	env := tgEnv("telegram:dm:5", "primary")
	runToolEnv(t, tool, `{"action":"create","schedule":"every 2h","prompt":"p"}`, env)
	j := onlyJob(t, tool)
	out := runToolEnvErr(t, tool, fmt.Sprintf(`{"action":"update","job_id":%q}`, j.ID), env)
	if !strings.Contains(out, "script, no_agent, draft_only, toolsets, or workdir") {
		t.Fatal(out)
	}
}

func TestCronSchemaHasDraftOnly(t *testing.T) {
	var s struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(CronJobTool{}.Schema().Parameters, &s); err != nil {
		t.Fatal(err)
	}
	p, ok := s.Properties["draft_only"]
	if !ok || p.Type != "boolean" || !strings.Contains(p.Description, "it cannot send") {
		t.Fatalf("%+v", s.Properties["draft_only"])
	}
}

func TestCronRunsHeldTexts(t *testing.T) {
	tool := cronTool(t)
	id := newRunsJob(t, tool, "prices")
	at := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	for i, d := range []string{cron.DeliveryHeld, cron.DeliveryDropped, cron.DeliveryExpired} {
		start := at.Add(time.Duration(i) * time.Hour)
		if d == cron.DeliveryHeld {
			// A held run keeps "held" only while its hold row waits for a tap.
			hs := &cron.HeldStore{DB: tool.Store.DB}
			if err := hs.Insert(context.Background(), cron.HeldSend{ID: "h1", Kind: "hold", JobID: id,
				Target: "telegram:-100", CreatedAt: start}); err != nil {
				t.Fatal(err)
			}
		}
		seedRun(t, tool, id, cron.RunRecord{Status: "ok", Delivery: d, Output: "report", RanAt: start, FinishedAt: start.Add(time.Second)})
	}
	out := runsText(t, tool, id, "")
	for _, want := range []string{
		"- run 3 · 2026-10-27 11:00 UTC · ok · expired · 1s · held, then expired after 24 h with no tap\n",
		"- run 2 · 2026-10-27 10:00 UTC · ok · dropped · 1s · held, then dropped by the owner\n",
		"- run 1 · 2026-10-27 09:00 UTC · ok · held · 1s · held: waits for the owner to tap Send or Drop\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("no %q in:\n%s", want, out)
		}
	}
}
