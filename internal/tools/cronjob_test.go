package tools

import (
	"context"
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
		Store: &cron.Store{DB: d, Loc: time.UTC, Now: func() time.Time { return cronNow },
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
	runToolEnv(t, tool, fmt.Sprintf(`{"action":"run","job_id":%q}`, id), env)
	if got != id {
		t.Fatal(got)
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

func TestDescriptionHasNoSilent(t *testing.T) {
	d := CronJobTool{}.Schema().Description
	if strings.Contains(d, "SILENT") || strings.Contains(d, "context_from") {
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
