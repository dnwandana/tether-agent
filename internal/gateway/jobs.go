package gateway

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/cron"
)

// The limits of the /jobs views. They keep a message under the Telegram limit of 4096 characters.
const (
	jobsMaxList    = 30
	jobsNameRunes  = 40
	jobsErrorRunes = 300
	jobsLastRuns   = 7
)

// JobsCommand returns the owner-only command /jobs. It lists the cron jobs, shows one job, and
// controls a job, with no model call. runNow starts a run in the gateway. It is nil in the chat
// role. app.New registers the command.
func JobsCommand(store *cron.Store, runNow func(ctx context.Context, id string) error) Command {
	return Command{
		Name:      "jobs",
		Help:      "See and control the cron jobs: /jobs [<id or name>]",
		WhileBusy: true,
		Run: func(ctx context.Context, c *CommandCtx, args string) error {
			arg := strings.TrimSpace(args)
			if verb, rest, _ := strings.Cut(arg, " "); jobsVerbs[verb] {
				return jobsAction(ctx, c, store, runNow, verb, strings.TrimSpace(rest))
			}
			if arg == "" {
				return jobsList(ctx, c, store, "")
			}
			jobs, err := store.List(ctx)
			if err != nil {
				return err
			}
			j, n := findJob(jobs, arg)
			switch n {
			case 1:
				return jobsDetail(ctx, c, store, j, "")
			case 0:
				return jobsList(ctx, c, store, fmt.Sprintf("No cron job has the id or the name \"%s\".", EscapeMarkdown(arg)))
			default:
				return jobsList(ctx, c, store, fmt.Sprintf("%d jobs have the name \"%s\". Send the id.", n, EscapeMarkdown(arg)))
			}
		},
	}
}

// findJob returns the job with the id arg, else the job with the name arg (case is ignored).
// n is 1 for a found job, 0 for no job, and the number of jobs with the name when two or more
// have it.
func findJob(jobs []cron.Job, arg string) (j cron.Job, n int) {
	for _, x := range jobs {
		if x.ID == arg {
			return x, 1
		}
	}
	for _, x := range jobs {
		if strings.EqualFold(x.Name, arg) {
			j = x
			n++
		}
	}
	if n != 1 {
		return cron.Job{}, n
	}
	return j, 1
}

// jobGlyph maps each job state of cron.StateOf to its icon.
var jobGlyph = map[string]string{"done": "✔", "paused": "⏸", "failing": "⚠", "watch": "👁", "ok": "✅", "pending": "⏳"}

// jobIcon returns the state icon of the job j.
func jobIcon(j cron.Job) string {
	return jobGlyph[cron.StateOf(j)]
}

// jobsNext returns the "next <time>" text of the job j.
func jobsNext(j cron.Job) string {
	if !j.Enabled || j.NextRun.IsZero() {
		return "no next run"
	}
	return "next " + j.NextRun.Format("Jan 2 15:04")
}

// jobsList sends the list of the jobs. A note that is not empty is the first line.
func jobsList(ctx context.Context, c *CommandCtx, store *cron.Store, note string) error {
	jobs, err := store.List(ctx)
	if err != nil {
		return err
	}
	var b strings.Builder
	if note != "" {
		b.WriteString(note)
		b.WriteString("\n")
	}
	if len(jobs) == 0 {
		b.WriteString("⏰ No cron jobs.")
		c.send(Reply{Text: b.String(), Edit: true})
		return nil
	}
	b.WriteString("⏰ **Cron jobs**")
	shown := jobs
	if len(shown) > jobsMaxList {
		shown = shown[:jobsMaxList]
	}
	group := groupChat(c)
	var rows [][]Button
	for _, j := range shown {
		name := cron.TruncateRunes(j.Name, jobsNameRunes)
		line := fmt.Sprintf("\n%s %s · %s", jobIcon(j), EscapeMarkdown(name), EscapeMarkdown(j.Schedule))
		// With no run history, a job with no next run shows only "no run history".
		if nx := jobsNext(j); store.RunHistory > 0 || nx != "no next run" {
			line += " · " + nx
		}
		if store.RunHistory > 0 {
			runs, err := store.Runs(ctx, j.ID, jobsLastRuns, 0)
			if err != nil {
				return err
			}
			ok := 0
			for _, r := range runs {
				if r.Status == "ok" {
					ok++
				}
			}
			line += fmt.Sprintf(" · %d/%d ok", ok, jobsLastRuns)
			if len(runs) > 0 && runs[0].CostUSD != nil && !group {
				line += fmt.Sprintf(" · $%.2f", *runs[0].CostUSD)
			}
		} else {
			line += " · no run history"
		}
		b.WriteString(line)
		btn := Button{Text: name, Data: "/jobs " + j.ID}
		if len(btn.Data) > ActionMaxBytes {
			continue
		}
		if n := len(rows); n > 0 && len(rows[n-1]) < 2 {
			rows[n-1] = append(rows[n-1], btn)
		} else {
			rows = append(rows, []Button{btn})
		}
	}
	if len(jobs) > len(shown) {
		fmt.Fprintf(&b, "\nand %d more", len(jobs)-len(shown))
	}
	if !c.interactive() {
		b.WriteString("\nSend /jobs <id> to see a job.")
		rows = nil
	}
	c.send(Reply{Text: b.String(), Buttons: rows, Edit: true})
	return nil
}

// jobsDetail sends the detail view of the job j. A note that is not empty is the first line.
func jobsDetail(ctx context.Context, c *CommandCtx, store *cron.Store, j cron.Job, note string) error {
	now := nowFunc()
	group := groupChat(c)
	onOff := func(v bool) string {
		if v {
			return "on"
		}
		return "off"
	}
	var lines []string
	if note != "" {
		lines = append(lines, note)
	}
	deliver := j.Deliver
	if deliver == "" {
		deliver = "origin"
	}
	model := "default"
	if j.Model != nil && (j.Model.Provider != "" || j.Model.Model != "") {
		model = strings.Trim(j.Model.Provider+":"+j.Model.Model, ":")
	}
	effort := j.Effort
	if effort == "" {
		effort = "default"
	}
	lastCheck := "never"
	if !j.LastRun.IsZero() {
		lastCheck = j.LastRun.Format("Jan 2 15:04")
	}
	lines = append(lines,
		fmt.Sprintf("⏰ **%s** · `%s`", EscapeMarkdown(j.Name), j.ID),
		fmt.Sprintf("Schedule: %s · %s", EscapeMarkdown(j.Schedule), jobsNext(j)),
		fmt.Sprintf("Delivery: %s · model %s · effort %s", EscapeMarkdown(deliver), EscapeMarkdown(model), EscapeMarkdown(effort)),
		fmt.Sprintf("Watch: %s · continuity: %s", onOff(j.Watch), onOff(j.Continuity)),
		"Last check: "+lastCheck,
	)
	if store.RunHistory > 0 {
		runs, err := store.Runs(ctx, j.ID, store.RunHistory, 0)
		if err != nil {
			return err
		}
		week := now.Add(-7 * 24 * time.Hour)
		ok, total, err := store.RunsOK(ctx, j.ID, week)
		if err != nil {
			return err
		}
		marks := "none"
		if len(runs) > 0 {
			last := runs
			if len(last) > jobsLastRuns {
				last = last[:jobsLastRuns]
			}
			var m strings.Builder
			for _, r := range slices.Backward(last) {
				if r.Status == "ok" {
					m.WriteString("✓")
				} else {
					m.WriteString("✗")
				}
			}
			marks = m.String()
		}
		lines = append(lines, "Last 7: "+marks, fmt.Sprintf("%d of %d runs OK this week", ok, total))
		var sum float64
		priced := false
		for _, r := range runs {
			if r.CostUSD != nil && !r.StartedAt.Before(week) {
				sum += *r.CostUSD
				priced = true
			}
		}
		if priced && !group {
			lines = append(lines, fmt.Sprintf("Cost this week: $%.2f", sum))
		}
	} else {
		lines = append(lines, "no run history")
	}
	if j.LastError != "" && !group {
		lines = append(lines, "Last error: "+EscapeMarkdown(cron.TruncateRunes(oneLine(cron.ErrorSummary(j.Name, j.LastError)), jobsErrorRunes)))
	}
	if j.Watch {
		n, err := store.ModelRunsSince(ctx, j.ID, now.Add(-24*time.Hour))
		if err != nil {
			return err
		}
		if n >= cron.WatchModelRunsPerDay {
			lines = append(lines, "watch limit reached")
		}
	}

	toggle := Button{Text: "⏸ Pause", Data: "/jobs pause " + j.ID}
	verb := "pause"
	if !j.Enabled {
		toggle = Button{Text: "▶ Resume", Data: "/jobs resume " + j.ID}
		verb = "resume"
	}
	rows := [][]Button{
		{toggle, {Text: "⚡ Run now", Data: "/jobs run " + j.ID}},
		{{Text: "📜 Runs", Data: "/jobs runs " + j.ID}, {Text: "« Back", Data: "/jobs"}},
		{{Text: "🗑 Delete…", Data: "/jobs delete " + j.ID}},
	}
	if !c.interactive() {
		lines = append(lines, fmt.Sprintf("Send /jobs %s %s, /jobs run %s, or /jobs delete %s.", verb, j.ID, j.ID, j.ID))
		rows = nil
	}
	c.send(Reply{Text: strings.Join(lines, "\n"), Buttons: rows, Edit: true})
	return nil
}

// jobsVerbs are the subcommand words of /jobs. A word in this set is always the subcommand,
// also when a job has the same name.
var jobsVerbs = map[string]bool{"pause": true, "resume": true, "run": true, "runs": true, "delete": true}

// jobsAction runs the subcommand verb on the job that rest names.
func jobsAction(ctx context.Context, c *CommandCtx, store *cron.Store,
	runNow func(ctx context.Context, id string) error, verb, rest string) error {
	if rest == "" {
		c.send(Reply{Text: "Send /jobs " + verb + " <id or name>."})
		return nil
	}
	if fields := strings.Fields(rest); verb == "delete" && len(fields) == 2 && fields[1] == confirmWord {
		j, err := store.Get(ctx, fields[0])
		switch {
		case err == nil:
			return jobsDelete(ctx, c, store, j, true)
		case !errors.Is(err, cron.ErrNotFound):
			return err
		}
	}
	jobs, err := store.List(ctx)
	if err != nil {
		return err
	}
	j, n := findJob(jobs, rest)
	switch {
	case n == 0:
		return jobsList(ctx, c, store, fmt.Sprintf("No cron job has the id or the name \"%s\".", EscapeMarkdown(rest)))
	case n > 1:
		return jobsList(ctx, c, store, fmt.Sprintf("%d jobs have the name \"%s\". Send the id.", n, EscapeMarkdown(rest)))
	}
	// failNote shows the detail view of the old job with the error as the note.
	failNote := func(err error) error {
		return jobsDetail(ctx, c, store, j, "⚠️ "+EscapeMarkdown(err.Error()))
	}
	switch verb {
	case "pause", "resume":
		if err := store.SetEnabled(ctx, j.ID, verb == "resume"); err != nil {
			return failNote(err)
		}
		got, err := store.Get(ctx, j.ID)
		if err != nil {
			return err
		}
		return jobsDetail(ctx, c, store, got, "")
	case "run":
		note := "Started."
		var err error
		if runNow != nil {
			err = runNow(ctx, j.ID)
		} else {
			_, err = store.RunSoon(ctx, j.ID)
			note = "The gateway runs it at its next tick."
		}
		if err != nil {
			note = "⚠️ " + EscapeMarkdown(err.Error())
		}
		got, gerr := store.Get(ctx, j.ID)
		if gerr != nil {
			return gerr
		}
		return jobsDetail(ctx, c, store, got, note)
	case "runs":
		return jobsRuns(ctx, c, store, j)
	}
	return jobsDelete(ctx, c, store, j, false)
}

// jobsRuns sends the last 7 runs of the job j, newest first.
func jobsRuns(ctx context.Context, c *CommandCtx, store *cron.Store, j cron.Job) error {
	lines := []string{"📜 **Runs of " + EscapeMarkdown(cron.TruncateRunes(j.Name, jobsNameRunes)) + "**"}
	if store.RunHistory == 0 {
		lines = append(lines, "no run history")
	} else {
		runs, err := store.Runs(ctx, j.ID, jobsLastRuns, 0)
		if err != nil {
			return err
		}
		if len(runs) == 0 {
			lines = append(lines, "No runs yet.")
		}
		group := groupChat(c)
		for _, r := range runs {
			line := fmt.Sprintf("%s · %s · %s · %s", r.StartedAt.Format("Jan 2 15:04"), EscapeMarkdown(r.Status),
				EscapeMarkdown(r.Delivery), runDuration(r.FinishedAt.Sub(r.StartedAt)))
			if r.CostUSD != nil && !group {
				line += fmt.Sprintf(" · $%.2f", *r.CostUSD)
			}
			lines = append(lines, line)
		}
	}
	rows := [][]Button{{{Text: "« Back", Data: "/jobs " + j.ID}}}
	if !c.interactive() {
		lines = append(lines, "Send /jobs "+j.ID+" to see the job.")
		rows = nil
	}
	c.send(Reply{Text: strings.Join(lines, "\n"), Buttons: rows, Edit: true})
	return nil
}

// jobsDelete asks before it deletes the job j. confirmed is true for the confirm form.
func jobsDelete(ctx context.Context, c *CommandCtx, store *cron.Store, j cron.Job, confirmed bool) error {
	if !confirmed {
		const tail = "? This removes the job and its run history."
		question := "🗑 Delete " + EscapeMarkdown(j.Name) + tail
		switch {
		case c.interactive():
			c.send(Reply{Text: question, Edit: true, Buttons: [][]Button{{
				{Text: "🗑 Delete", Data: "/jobs delete " + j.ID + " " + confirmWord},
				{Text: "Cancel", Data: "cancel"},
			}}})
			return nil
		case c.Confirm == nil:
			c.send(Reply{Text: question + "\nTo delete it, send: /jobs delete " + j.ID + " " + confirmWord})
			return nil
		case !c.Confirm("Delete " + j.Name + tail):
			c.send(Reply{Text: "Not deleted."})
			return nil
		}
	}
	if err := store.Remove(ctx, j.ID); err != nil {
		return err
	}
	return jobsList(ctx, c, store, "🗑 Deleted "+EscapeMarkdown(j.Name)+".")
}

// runDuration returns d as "<n>s" under one minute, "<n>m" under one hour, else "<n>h".
func runDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}
