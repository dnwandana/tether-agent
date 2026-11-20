package gateway

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/cron"
)

// jobsNow is the clock of the /jobs tests.
var jobsNow = time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)

// jobsStore returns a cron store over a new state.db, and fixes the command clock at jobsNow.
// The store clock adds 1 s at each read, so the jobs list in the order of creation.
func jobsStore(t *testing.T) *cron.Store {
	t.Helper()
	setNow(t, jobsNow)
	tick := jobsNow
	return &cron.Store{DB: openTestDB(t), Loc: time.UTC, RunHistory: 50,
		Now: func() time.Time { tick = tick.Add(time.Second); return tick }}
}

// addJobsJob creates the job j and returns it as stored.
func addJobsJob(t *testing.T, s *cron.Store, j cron.Job) cron.Job {
	t.Helper()
	if j.Schedule == "" {
		j.Schedule = "30m"
	}
	if j.Prompt == "" {
		j.Prompt = "x"
	}
	got, err := s.Create(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// jobsSQL runs one statement on the store.
func jobsSQL(t *testing.T, s *cron.Store, q string, args ...any) {
	t.Helper()
	if _, err := s.DB.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// addJobsRun writes one cron_runs row of the job that started age before jobsNow and took 42 s.
// A nil cost stores NULL.
func addJobsRun(t *testing.T, s *cron.Store, jobID, status, delivery string, age time.Duration, cost any) {
	t.Helper()
	at := jobsNow.Add(-age).UnixMilli()
	jobsSQL(t, s, `INSERT INTO cron_runs (job_id, started_at, finished_at, status, delivery, cost_usd)
		VALUES (?, ?, ?, ?, ?, ?)`, jobID, at, at+42000, status, delivery, cost)
}

// next is the "next <time>" text of the job.
func next(j cron.Job) string { return "next " + j.NextRun.Format("Jan 2 15:04") }

func TestJobsCommandFields(t *testing.T) {
	cmd := JobsCommand(jobsStore(t), nil)
	if cmd.Name != "jobs" || cmd.MemberOK || !cmd.WhileBusy || cmd.Help == "" {
		t.Fatalf("%+v", cmd)
	}
}

func TestJobIcon(t *testing.T) {
	for _, c := range []struct {
		j    cron.Job
		want string
	}{
		{cron.Job{Enabled: false, RepeatLeft: 0}, "✔"},
		{cron.Job{Enabled: false, RepeatLeft: -1, FailStreak: 3}, "⏸"},
		{cron.Job{Enabled: true, RepeatLeft: -1, FailStreak: 1, Watch: true, LastStatus: "error"}, "⚠"},
		{cron.Job{Enabled: true, RepeatLeft: -1, Watch: true, LastStatus: "ok"}, "👁"},
		{cron.Job{Enabled: true, RepeatLeft: -1, LastStatus: "ok"}, "✅"},
		{cron.Job{Enabled: true, RepeatLeft: -1}, "⏳"},
	} {
		if got := jobIcon(c.j); got != c.want {
			t.Errorf("%+v: got %s, want %s", c.j, got, c.want)
		}
	}
}

// The list has one line and one button for each job, two buttons in a row.
func TestJobsList(t *testing.T) {
	s := jobsStore(t)
	registerTestCommand(t, JobsCommand(s, nil))
	a := addJobsJob(t, s, cron.Job{Name: "rss_check", Enabled: true})
	jobsSQL(t, s, `UPDATE cron_jobs SET last_status = 'ok' WHERE id = ?`, a.ID)
	addJobsRun(t, s, a.ID, "ok", "sent", 9*time.Hour, 0.5)
	addJobsRun(t, s, a.ID, "error", "sent", 8*time.Hour, nil)
	for i := 6; i >= 2; i-- {
		addJobsRun(t, s, a.ID, "ok", "sent", time.Duration(i)*time.Hour, 0.1)
	}
	addJobsRun(t, s, a.ID, "ok", "sent", time.Hour, 0.12)
	b := addJobsJob(t, s, cron.Job{Name: "backup", Enabled: true})
	if err := s.SetEnabled(context.Background(), b.ID, false); err != nil {
		t.Fatal(err)
	}
	c, sent := interactiveCtx(t)
	run(t, c, "/jobs")
	r := lastSent(t, sent)
	want := "⏰ **Cron jobs**\n" +
		"✅ rss\\_check · " + a.Schedule + " · " + next(a) + " · 6/7 ok · $0.12\n" +
		"⏸ backup · " + b.Schedule + " · no next run · 0/7 ok"
	wantB := [][]Button{{{Text: "rss_check", Data: "/jobs " + a.ID}, {Text: "backup", Data: "/jobs " + b.ID}}}
	if r.Text != want || !reflect.DeepEqual(r.Buttons, wantB) || !r.Edit {
		t.Fatalf("got:\n%s\n%+v edit=%v", r.Text, r.Buttons, r.Edit)
	}

	// A group hides the cost. History off shows "no run history".
	c.Key = "telegram:group:-100"
	s.RunHistory = 0
	run(t, c, "/jobs")
	want = "⏰ **Cron jobs**\n" +
		"✅ rss\\_check · " + a.Schedule + " · " + next(a) + " · no run history\n" +
		"⏸ backup · " + b.Schedule + " · no run history"
	if r := lastSent(t, sent); r.Text != want {
		t.Fatalf("got:\n%s", r.Text)
	}

	// The CLI gets a line with the command to send.
	cli, out := cmdCtx(t, "cli")
	run(t, cli, "/jobs")
	if !strings.HasSuffix(strings.TrimSpace(out.String()), "\nSend /jobs <id> to see a job.") {
		t.Fatal(out.String())
	}
}

// The list shows at most 30 jobs and cuts a name at 40 runes, in the text and on the button.
func TestJobsListCap(t *testing.T) {
	s := jobsStore(t)
	registerTestCommand(t, JobsCommand(s, nil))
	long := strings.Repeat("n", 50)
	addJobsJob(t, s, cron.Job{Name: long, Enabled: true})
	for i := range 31 {
		addJobsJob(t, s, cron.Job{Name: fmt.Sprintf("job%02d", i), Enabled: true})
	}
	c, sent := interactiveCtx(t)
	run(t, c, "/jobs")
	r := lastSent(t, sent)
	lines := strings.Split(r.Text, "\n")
	cut := strings.Repeat("n", 40) + "…"
	if len(lines) != 32 || lines[31] != "and 2 more" || !strings.HasPrefix(lines[1], "⏳ "+cut+" · ") ||
		len(r.Buttons) != 15 || r.Buttons[0][0].Text != cut {
		t.Fatalf("%d lines, last %q, %d rows\n%s", len(lines), lines[len(lines)-1], len(r.Buttons), r.Text)
	}
}

// jobsDetailText is the detail view of the job of TestJobsDetail, in a private chat.
func jobsDetailText(j cron.Job) string {
	return "⏰ **rss\\_check** · `" + j.ID + "`\n" +
		"Schedule: " + j.Schedule + " · " + next(j) + "\n" +
		"Delivery: local · model openrouter:glm-4.7 · effort low\n" +
		"Watch: off · continuity: on\n" +
		"Last check: Nov 19 07:00\n" +
		"Last 7: ✓✓✗✓\n" +
		"2 of 3 runs OK this week\n" +
		"Cost this week: $0.30\n" +
		"Last error: http 502 \\*bad\\*"
}

// An id or a unique name gives the detail view with its button rows.
func TestJobsDetail(t *testing.T) {
	s := jobsStore(t)
	registerTestCommand(t, JobsCommand(s, nil))
	j := addJobsJob(t, s, cron.Job{Name: "rss_check", Enabled: true, Deliver: "local", Effort: "low",
		Continuity: true, Model: &cron.ModelPin{Provider: "openrouter", Model: "glm-4.7"}})
	jobsSQL(t, s, `UPDATE cron_jobs SET last_run = ?, last_error = ?, fail_streak = 1 WHERE id = ?`,
		jobsNow.Add(-time.Hour).UnixMilli(), "http 502 *bad*", j.ID)
	addJobsRun(t, s, j.ID, "ok", "sent", 8*24*time.Hour, 5.0)
	addJobsRun(t, s, j.ID, "ok", "sent", 48*time.Hour, 0.1)
	addJobsRun(t, s, j.ID, "error", "failed", 24*time.Hour, nil)
	addJobsRun(t, s, j.ID, "ok", "local", time.Hour, 0.2)
	wantB := [][]Button{
		{{Text: "⏸ Pause", Data: "/jobs pause " + j.ID}, {Text: "⚡ Run now", Data: "/jobs run " + j.ID}},
		{{Text: "📜 Runs", Data: "/jobs runs " + j.ID}, {Text: "« Back", Data: "/jobs"}},
		{{Text: "🗑 Delete…", Data: "/jobs delete " + j.ID}},
	}
	c, sent := interactiveCtx(t)
	for _, arg := range []string{j.ID, "rss_check", "RSS_CHECK"} {
		run(t, c, "/jobs "+arg)
		r := lastSent(t, sent)
		if r.Text != jobsDetailText(j) || !reflect.DeepEqual(r.Buttons, wantB) || !r.Edit {
			t.Fatalf("%s: got:\n%s\n%+v", arg, r.Text, r.Buttons)
		}
	}

	// A group hides the cost and the last error.
	c.Key = "telegram:group:-100"
	run(t, c, "/jobs "+j.ID)
	if r := lastSent(t, sent); strings.Contains(r.Text, "Cost this week") || strings.Contains(r.Text, "Last error") {
		t.Fatal(r.Text)
	}

	// A paused job gets Resume. The CLI gets the line with the commands.
	if err := s.SetEnabled(context.Background(), j.ID, false); err != nil {
		t.Fatal(err)
	}
	cli, out := cmdCtx(t, "cli")
	run(t, cli, "/jobs "+j.ID)
	want := fmt.Sprintf("Send /jobs resume %s, /jobs run %s, or /jobs delete %s.", j.ID, j.ID, j.ID)
	if !strings.HasSuffix(strings.TrimSpace(out.String()), want) {
		t.Fatal(out.String())
	}
	run(t, c, "/jobs "+j.ID)
	if r := lastSent(t, sent); r.Buttons[0][0] != (Button{Text: "▶ Resume", Data: "/jobs resume " + j.ID}) {
		t.Fatalf("%+v", r.Buttons)
	}

	// With cron.run_history 0, the view shows "no run history" in place of the counts.
	s.RunHistory = 0
	run(t, c, "/jobs "+j.ID)
	if r := lastSent(t, sent); !strings.Contains(r.Text, "\nno run history") || strings.Contains(r.Text, "Last 7") ||
		strings.Contains(r.Text, "this week") {
		t.Fatal(r.Text)
	}
}

// A watch job at 24 model runs in 24 h shows "watch limit reached".
func TestJobsDetailWatchLimit(t *testing.T) {
	s := jobsStore(t)
	registerTestCommand(t, JobsCommand(s, nil))
	j := addJobsJob(t, s, cron.Job{Name: "price", Enabled: true, Watch: true, Script: "price.sh"})
	c, sent := interactiveCtx(t)
	for i := range cron.WatchModelRunsPerDay {
		if i == cron.WatchModelRunsPerDay-1 {
			run(t, c, "/jobs "+j.ID)
			if r := lastSent(t, sent); strings.Contains(r.Text, "watch limit reached") {
				t.Fatal("23 runs must not reach the limit")
			}
		}
		jobsSQL(t, s, `INSERT INTO sessions (id, session_key, started_at) VALUES (?, ?, ?)`,
			fmt.Sprintf("s%02d", i), "cron:"+j.ID, jobsNow.Add(-time.Hour).UnixMilli())
	}
	run(t, c, "/jobs "+j.ID)
	if r := lastSent(t, sent); !strings.HasSuffix(r.Text, "\nwatch limit reached") ||
		!strings.Contains(r.Text, "\nWatch: on · continuity: off\n") {
		t.Fatal(r.Text)
	}
}

// A name of two jobs and an unknown name give the list with a note.
func TestJobsLookupFallsBackToList(t *testing.T) {
	s := jobsStore(t)
	registerTestCommand(t, JobsCommand(s, nil))
	addJobsJob(t, s, cron.Job{Name: "dup", Enabled: true})
	addJobsJob(t, s, cron.Job{Name: "dup", Enabled: true})
	c, sent := interactiveCtx(t)
	run(t, c, "/jobs dup")
	if r := lastSent(t, sent); !strings.HasPrefix(r.Text, "2 jobs have the name \"dup\". Send the id.\n⏰ **Cron jobs**\n") ||
		len(r.Buttons) != 1 {
		t.Fatal(r.Text)
	}
	run(t, c, "/jobs no_such")
	if r := lastSent(t, sent); !strings.HasPrefix(r.Text, "No cron job has the id or the name \"no\\_such\".\n⏰ **Cron jobs**\n") {
		t.Fatal(r.Text)
	}
}

// No job gives a short text.
func TestJobsListEmpty(t *testing.T) {
	s := jobsStore(t)
	registerTestCommand(t, JobsCommand(s, nil))
	c, sent := interactiveCtx(t)
	run(t, c, "/jobs")
	if r := lastSent(t, sent); r.Text != "⏰ No cron jobs." || len(r.Buttons) != 0 {
		t.Fatalf("%+v", r)
	}
}

// Each button action of the list and of the detail view fits the callback data.
func TestJobsViewActionsFit(t *testing.T) {
	s := jobsStore(t)
	registerTestCommand(t, JobsCommand(s, nil))
	j := addJobsJob(t, s, cron.Job{Name: strings.Repeat("x", 60), Enabled: true})
	c, sent := interactiveCtx(t)
	run(t, c, "/jobs")
	run(t, c, "/jobs "+j.ID)
	for _, r := range *sent {
		for _, row := range r.Buttons {
			for _, b := range row {
				if len(b.Data) > ActionMaxBytes {
					t.Fatal(len(b.Data), b.Data)
				}
			}
		}
	}
}

// Pause and resume use the store, and then show the detail view again. A name works too.
func TestJobsPauseResume(t *testing.T) {
	s := jobsStore(t)
	registerTestCommand(t, JobsCommand(s, nil))
	j := addJobsJob(t, s, cron.Job{Name: "rss", Enabled: true})
	c, sent := interactiveCtx(t)
	run(t, c, "/jobs pause rss")
	r := lastSent(t, sent)
	got, err := s.Get(context.Background(), j.ID)
	if err != nil || got.Enabled || !r.Edit || !strings.HasPrefix(r.Text, "⏰ **rss** · ") ||
		r.Buttons[0][0] != (Button{Text: "▶ Resume", Data: "/jobs resume " + j.ID}) {
		t.Fatalf("%+v %v\n%s\n%+v", got, err, r.Text, r.Buttons)
	}
	jobsSQL(t, s, `UPDATE cron_jobs SET fail_streak = 3 WHERE id = ?`, j.ID)
	run(t, c, "/jobs resume "+j.ID)
	r = lastSent(t, sent)
	got, err = s.Get(context.Background(), j.ID)
	if err != nil || !got.Enabled || got.FailStreak != 0 ||
		r.Buttons[0][0] != (Button{Text: "⏸ Pause", Data: "/jobs pause " + j.ID}) {
		t.Fatalf("%+v %v\n%+v", got, err, r.Buttons)
	}
}

// A subcommand word always wins over a job name. A subcommand with no job asks for one.
func TestJobsSubcommandWinsOverName(t *testing.T) {
	s := jobsStore(t)
	registerTestCommand(t, JobsCommand(s, nil))
	p := addJobsJob(t, s, cron.Job{Name: "pause", Enabled: true})
	c, sent := interactiveCtx(t)
	run(t, c, "/jobs pause")
	if r := lastSent(t, sent); r.Text != "Send /jobs pause <id or name>." {
		t.Fatal(r.Text)
	}
	if got, _ := s.Get(context.Background(), p.ID); !got.Enabled {
		t.Fatal("/jobs pause with no job must not pause the job named pause")
	}
	run(t, c, "/jobs pause pause")
	if got, _ := s.Get(context.Background(), p.ID); got.Enabled {
		t.Fatal("/jobs pause pause must pause the job named pause")
	}
	run(t, c, "/jobs run no_such")
	if r := lastSent(t, sent); !strings.HasPrefix(r.Text, "No cron job has the id or the name \"no\\_such\".\n") {
		t.Fatal(r.Text)
	}
}

// With runNow, Run now starts the run and shows "Started." or the escaped error.
func TestJobsRunNow(t *testing.T) {
	s := jobsStore(t)
	var started []string
	var runErr error
	registerTestCommand(t, JobsCommand(s, func(_ context.Context, id string) error {
		started = append(started, id)
		return runErr
	}))
	j := addJobsJob(t, s, cron.Job{Name: "rss", Enabled: true})
	c, sent := interactiveCtx(t)
	run(t, c, "/jobs run "+j.ID)
	if r := lastSent(t, sent); !strings.HasPrefix(r.Text, "Started.\n⏰ **rss** · ") || len(started) != 1 || !r.Edit {
		t.Fatal(r.Text, started)
	}
	runErr = errors.New("cron: job_x is already running")
	run(t, c, "/jobs run "+j.ID)
	if r := lastSent(t, sent); !strings.HasPrefix(r.Text, "⚠️ cron: job\\_x is already running\n⏰ **rss** · ") {
		t.Fatal(r.Text)
	}
}

// With no runNow (the chat role), Run now makes the job due and the gateway runs it.
func TestJobsRunWithoutGateway(t *testing.T) {
	s := jobsStore(t)
	registerTestCommand(t, JobsCommand(s, nil))
	j := addJobsJob(t, s, cron.Job{Name: "rss", Enabled: true})
	c, sent := interactiveCtx(t)
	run(t, c, "/jobs run "+j.ID)
	got, err := s.Get(context.Background(), j.ID)
	if r := lastSent(t, sent); err != nil || !got.NextRun.Before(j.NextRun) ||
		!strings.HasPrefix(r.Text, "The gateway runs it at its next tick.\n⏰ **rss** · ") {
		t.Fatal(r.Text, got.NextRun, err)
	}
	if err := s.SetEnabled(context.Background(), j.ID, false); err != nil {
		t.Fatal(err)
	}
	run(t, c, "/jobs run "+j.ID)
	r := lastSent(t, sent)
	first, _, _ := strings.Cut(r.Text, "\n")
	if !strings.HasPrefix(first, "⚠️ ") || !strings.Contains(first, EscapeMarkdown(cron.ErrJobPaused.Error())) {
		t.Fatal(r.Text)
	}
}

// Runs shows the last 7 runs, newest first, with the time, the status, the delivery, the
// duration, and the cost.
func TestJobsRuns(t *testing.T) {
	s := jobsStore(t)
	registerTestCommand(t, JobsCommand(s, nil))
	j := addJobsJob(t, s, cron.Job{Name: "rss_check", Enabled: true})
	c, sent := interactiveCtx(t)
	run(t, c, "/jobs runs "+j.ID)
	back := [][]Button{{{Text: "« Back", Data: "/jobs " + j.ID}}}
	if r := lastSent(t, sent); r.Text != "📜 **Runs of rss\\_check**\nNo runs yet." || !reflect.DeepEqual(r.Buttons, back) {
		t.Fatalf("%+v", r)
	}
	addJobsRun(t, s, j.ID, "ok", "sent", 3*time.Hour, 0.12)
	addJobsRun(t, s, j.ID, "error", cron.DeliveryRepeat, 2*time.Hour, nil)
	addJobsRun(t, s, j.ID, "ok", "silent", time.Hour, 0.05)
	run(t, c, "/jobs runs "+j.ID)
	want := "📜 **Runs of rss\\_check**\n" +
		"Nov 19 07:00 · ok · silent · 42s · $0.05\n" +
		"Nov 19 06:00 · error · repeat · 42s\n" +
		"Nov 19 05:00 · ok · sent · 42s · $0.12"
	if r := lastSent(t, sent); r.Text != want || !reflect.DeepEqual(r.Buttons, back) || !r.Edit {
		t.Fatalf("got:\n%s", r.Text)
	}
	for i := range 6 {
		addJobsRun(t, s, j.ID, "ok", "sent", time.Duration(30-i)*time.Minute, nil)
	}
	run(t, c, "/jobs runs "+j.ID)
	if r := lastSent(t, sent); strings.Count(r.Text, "\n") != 7 {
		t.Fatalf("want 7 runs:\n%s", r.Text)
	}
	// A group hides the cost. History off shows "no run history". The CLI gets a line.
	c.Key = "telegram:group:-100"
	run(t, c, "/jobs runs "+j.ID)
	if r := lastSent(t, sent); strings.Contains(r.Text, "$") {
		t.Fatal(r.Text)
	}
	s.RunHistory = 0
	run(t, c, "/jobs runs "+j.ID)
	if r := lastSent(t, sent); r.Text != "📜 **Runs of rss\\_check**\nno run history" {
		t.Fatal(r.Text)
	}
	cli, out := cmdCtx(t, "cli")
	run(t, cli, "/jobs runs "+j.ID)
	if !strings.HasSuffix(strings.TrimSpace(out.String()), "\nSend /jobs "+j.ID+" to see the job.") {
		t.Fatal(out.String())
	}
}

// Delete asks first. Only "/jobs delete <id> confirm" with an exact id deletes, then the list
// comes back.
func TestJobsDeleteTelegram(t *testing.T) {
	s := jobsStore(t)
	registerTestCommand(t, JobsCommand(s, nil))
	j := addJobsJob(t, s, cron.Job{Name: "rss_check", Enabled: true})
	c, sent := interactiveCtx(t)
	run(t, c, "/jobs delete "+j.ID)
	r := lastSent(t, sent)
	wantB := [][]Button{{{Text: "🗑 Delete", Data: "/jobs delete " + j.ID + " confirm"}, {Text: "Cancel", Data: "cancel"}}}
	if r.Text != "🗑 Delete rss\\_check? This removes the job and its run history." || !reflect.DeepEqual(r.Buttons, wantB) || !r.Edit {
		t.Fatalf("%+v", r)
	}
	run(t, c, "/jobs delete rss_check confirm")
	if _, err := s.Get(context.Background(), j.ID); err != nil {
		t.Fatal("a name with confirm must not delete:", err)
	}
	run(t, c, "/jobs delete "+j.ID+" confirm")
	if _, err := s.Get(context.Background(), j.ID); !errors.Is(err, cron.ErrNotFound) {
		t.Fatal(err)
	}
	if r := lastSent(t, sent); r.Text != "🗑 Deleted rss\\_check.\n⏰ No cron jobs." || !r.Edit {
		t.Fatal(r.Text)
	}
}

// The CLI asks with c.Confirm. With no Confirm, it gives the command to send.
func TestJobsDeleteCLI(t *testing.T) {
	s := jobsStore(t)
	registerTestCommand(t, JobsCommand(s, nil))
	j := addJobsJob(t, s, cron.Job{Name: "rss_check", Enabled: true})
	cli, out := cmdCtx(t, "cli")
	run(t, cli, "/jobs delete "+j.ID)
	want := "🗑 Delete rss\\_check? This removes the job and its run history.\nTo delete it, send: /jobs delete " + j.ID + " confirm"
	if strings.TrimSpace(out.String()) != want {
		t.Fatal(out.String())
	}
	var asked []string
	answer := false
	cli.Confirm = func(p string) bool { asked = append(asked, p); return answer }
	out.Reset()
	run(t, cli, "/jobs delete "+j.ID)
	if strings.TrimSpace(out.String()) != "Not deleted." ||
		asked[0] != "Delete rss_check? This removes the job and its run history." {
		t.Fatal(out.String(), asked)
	}
	answer = true
	out.Reset()
	run(t, cli, "/jobs delete rss_check")
	if _, err := s.Get(context.Background(), j.ID); !errors.Is(err, cron.ErrNotFound) ||
		!strings.HasPrefix(out.String(), "🗑 Deleted rss\\_check.\n") {
		t.Fatal(out.String(), err)
	}
}

// Each button action of each action reply fits the callback data. The longest is the delete
// confirmation, with 29 bytes.
func TestJobsActionsFit(t *testing.T) {
	s := jobsStore(t)
	registerTestCommand(t, JobsCommand(s, func(context.Context, string) error { return nil }))
	j := addJobsJob(t, s, cron.Job{Name: strings.Repeat("x", 60), Enabled: true})
	addJobsRun(t, s, j.ID, "ok", "sent", time.Hour, 0.1)
	c, sent := interactiveCtx(t)
	for _, verb := range []string{"pause", "resume", "run", "runs", "delete"} {
		run(t, c, "/jobs "+verb+" "+j.ID)
	}
	longest := 0
	for _, r := range *sent {
		for _, row := range r.Buttons {
			for _, b := range row {
				if len(b.Data) > ActionMaxBytes {
					t.Fatal(len(b.Data), b.Data)
				}
				longest = max(longest, len(b.Data))
			}
		}
	}
	if longest != len("/jobs delete 3fa1c2d0 confirm") {
		t.Fatal(longest)
	}
}

// The detail view shows only the cause of the stored alert as the last error.
func TestJobsDetailLastErrorCause(t *testing.T) {
	s := jobsStore(t)
	registerTestCommand(t, JobsCommand(s, nil))
	j := addJobsJob(t, s, cron.Job{Name: "rss", Enabled: true})
	jobsSQL(t, s, `UPDATE cron_jobs SET last_error = ?, fail_streak = 1 WHERE id = ?`,
		"⚠ Cron job rss failed: connection refused\n\nI send no new alert while the job fails with the same error.", j.ID)
	c, sent := interactiveCtx(t)
	run(t, c, "/jobs "+j.ID)
	if r := lastSent(t, sent); !strings.HasSuffix(r.Text, "\nLast error: connection refused") {
		t.Fatal(r.Text)
	}
}
