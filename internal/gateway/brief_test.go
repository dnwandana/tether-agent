package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/brief"
)

// FormatBrief writes one line for each kind, escapes the names and the details, and ends with
// the jobs line and the spend line.
func TestFormatBrief(t *testing.T) {
	st := brief.Status{
		Lines: []brief.Line{
			{Kind: brief.KindFailing, Name: "rss_check", Count: 3, Detail: "http 502"},
			{Kind: brief.KindPausedJob, Name: "backup", Count: 10},
			{Kind: brief.KindBadSchedule, Name: "old"},
			{Kind: brief.KindHeld, Name: "digest", Age: 2 * time.Hour},
			{Kind: brief.KindGoal, Name: "Fix *the* test", Age: 3 * 24 * time.Hour, Detail: "the judge failed 3 times"},
			{Kind: brief.KindCost, Name: "news", CostUSD: 1.5, Ratio: 4.2},
		},
		RunsOK: 40, RunsTotal: 42, Spend: 3.4, SpendUnpriced: true,
	}
	want := "6 things need you\n" +
		"⚠ rss\\_check failed 3 runs (http 502)\n" +
		"⏸ backup paused after 10 failures\n" +
		"⏸ old has a bad schedule\n" +
		"⏸ held send from digest (2h)\n" +
		"⏸ goal \"Fix \\*the\\* test\" paused 3d (the judge failed 3 times)\n" +
		"💸 news cost $1.50, 4.2× the usual\n" +
		"Jobs: 40 of 42 runs OK in 24 h\n" +
		"Spend in 24 h: $3.40+ (all chats)"
	if got, empty := FormatBrief(st); got != want || empty {
		t.Fatalf("empty=%v got:\n%s", empty, got)
	}
}

// One line uses the singular header and the singular nouns. A line with no detail has no
// parentheses. An age under one hour is in minutes.
func TestFormatBriefOneLine(t *testing.T) {
	for _, c := range []struct {
		line brief.Line
		want string
	}{
		{brief.Line{Kind: brief.KindFailing, Name: "rss", Count: 1}, "⚠ rss failed 1 run"},
		{brief.Line{Kind: brief.KindPausedJob, Name: "rss", Count: 1}, "⏸ rss paused after 1 failure"},
		{brief.Line{Kind: brief.KindHeld, Name: "rss", Age: 5 * time.Minute}, "⏸ held send from rss (5m)"},
		{brief.Line{Kind: brief.KindGoal, Name: "g", Age: 0}, "⏸ goal \"g\" paused 0m"},
	} {
		got, empty := FormatBrief(brief.Status{Lines: []brief.Line{c.line}})
		want := "1 thing needs you\n" + c.want + "\nJobs: 0 of 0 runs OK in 24 h\nSpend in 24 h: $0.00 (all chats)"
		if got != want || empty {
			t.Errorf("got:\n%s\nwant:\n%s", got, want)
		}
	}
}

// The text shows at most 6 lines, then "and <M> more".
func TestFormatBriefCap(t *testing.T) {
	var st brief.Status
	for i := range 8 {
		st.Lines = append(st.Lines, brief.Line{Kind: brief.KindBadSchedule, Name: string(rune('a' + i))})
	}
	got, _ := FormatBrief(st)
	lines := strings.Split(got, "\n")
	if lines[0] != "8 things need you" || lines[6] != "⏸ f has a bad schedule" || lines[7] != "and 2 more" ||
		len(lines) != 10 {
		t.Fatalf("got:\n%s", got)
	}
}

// No line gives "Nothing needs you." and empty. History off changes the jobs line.
func TestFormatBriefEmpty(t *testing.T) {
	got, empty := FormatBrief(brief.Status{RunsOK: 5, RunsTotal: 5, Spend: 0.25})
	if want := "Nothing needs you.\nJobs: 5 of 5 runs OK in 24 h\nSpend in 24 h: $0.25 (all chats)"; got != want || !empty {
		t.Fatalf("empty=%v got:\n%s", empty, got)
	}
	got, _ = FormatBrief(brief.Status{HistoryOff: true})
	if want := "Nothing needs you.\nJobs: no run history (cron.run_history is 0)\nSpend in 24 h: $0.00 (all chats)"; got != want {
		t.Fatalf("got:\n%s", got)
	}
}

// /brief replies with the text of fn, also when nothing needs the owner. An error is a reply.
func TestBriefCommand(t *testing.T) {
	text, empty, fnErr := "1 thing needs you\n⏸ old has a bad schedule", false, error(nil)
	calls := 0
	registerTestCommand(t, BriefCommand(func(context.Context) (string, bool, error) {
		calls++
		return text, empty, fnErr
	}))
	c, sent := interactiveCtx(t)
	run(t, c, "/brief")
	if r := lastSent(t, sent); r.Text != text || len(r.Buttons) != 0 {
		t.Fatalf("%+v", r)
	}
	text, empty = "Nothing needs you.\nJobs: 0 of 0 runs OK in 24 h\nSpend in 24 h: $0.00 (all chats)", true
	run(t, c, "/brief")
	if r := lastSent(t, sent); r.Text != text {
		t.Fatal(r.Text)
	}
	text, fnErr = "", errors.New("read *jobs*")
	run(t, c, "/brief")
	if r := lastSent(t, sent); r.Text != "⚠️ read \\*jobs\\*" {
		t.Fatal(r.Text)
	}
	// The CLI gets the same text.
	cli, out := cmdCtx(t, "cli")
	text, fnErr = "Nothing needs you.", nil
	run(t, cli, "/brief")
	if strings.TrimSpace(out.String()) != "Nothing needs you." || calls != 4 {
		t.Fatal(out.String(), calls)
	}
}

// In a Telegram group, /brief asks for a private chat and does not build the status.
func TestBriefInGroup(t *testing.T) {
	calls := 0
	registerTestCommand(t, BriefCommand(func(context.Context) (string, bool, error) {
		calls++
		return "secret line", false, nil
	}))
	c, sent := interactiveCtx(t)
	c.Key = "telegram:group:-100"
	run(t, c, "/brief")
	if r := lastSent(t, sent); r.Text != "Send /brief in a private chat." || calls != 0 {
		t.Fatal(r.Text, calls)
	}
}

// /brief is owner-only and runs during a turn.
func TestBriefOwnerOnly(t *testing.T) {
	cmd := BriefCommand(func(context.Context) (string, bool, error) { return "x", false, nil })
	if cmd.Name != "brief" || cmd.MemberOK || !cmd.WhileBusy || cmd.Help == "" {
		t.Fatalf("%+v", cmd)
	}
	registerTestCommand(t, cmd)
	g := &Gateway{Deps: testDeps(t, &fakeProv{})}
	r := &recRenderer{}
	if err := g.Handle(context.Background(), member("telegram:group:-100:brief", "/brief"), func() Renderer { return r }); err != nil {
		t.Fatal(err)
	}
	if len(r.acks) != 1 || r.acks[0] != "⛔ Only the owner can use /brief." {
		t.Fatalf("%q", r.acks)
	}
}
