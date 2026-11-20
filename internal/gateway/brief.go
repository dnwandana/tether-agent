package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/brief"
)

// briefMaxLines is the number of status lines that the text shows.
const briefMaxLines = 6

// briefGroupText is the reply of /brief in a Telegram group. The status holds the text of
// private sessions, so a group does not get it.
const briefGroupText = "Send /brief in a private chat."

// briefHelp is the help text of /brief.
const briefHelp = "Show what needs you: failing jobs, held sends, paused goals, and costly runs"

// FormatBrief returns the status text of st as common Markdown, and empty true when no line
// needs the owner. Each name and each detail goes through EscapeMarkdown. The caller redacts
// the text before a send.
func FormatBrief(st brief.Status) (text string, empty bool) {
	var parts []string
	switch n := len(st.Lines); n {
	case 0:
		parts = append(parts, "Nothing needs you.")
	case 1:
		parts = append(parts, "1 thing needs you")
	default:
		parts = append(parts, fmt.Sprintf("%d things need you", n))
	}
	for i, l := range st.Lines {
		if i == briefMaxLines {
			break
		}
		parts = append(parts, briefLine(l))
	}
	if more := len(st.Lines) - briefMaxLines; more > 0 {
		parts = append(parts, fmt.Sprintf("and %d more", more))
	}
	if st.HistoryOff {
		parts = append(parts, "Jobs: no run history (cron.run_history is 0)")
	} else {
		parts = append(parts, fmt.Sprintf("Jobs: %d of %d runs OK in 24 h", st.RunsOK, st.RunsTotal))
	}
	plus := ""
	if st.SpendUnpriced {
		plus = "+"
	}
	parts = append(parts, fmt.Sprintf("Spend in 24 h: $%.2f%s (all chats)", st.Spend, plus))
	return strings.Join(parts, "\n"), len(st.Lines) == 0
}

// briefLine returns the text of one status line.
func briefLine(l brief.Line) string {
	name := EscapeMarkdown(l.Name)
	detail := ""
	if l.Detail != "" {
		detail = " (" + EscapeMarkdown(oneLine(l.Detail)) + ")"
	}
	switch l.Kind {
	case brief.KindFailing:
		return fmt.Sprintf("⚠ %s failed %s%s", name, briefCount(l.Count, "run", "runs"), detail)
	case brief.KindPausedJob:
		return fmt.Sprintf("⏸ %s paused after %s", name, briefCount(l.Count, "failure", "failures"))
	case brief.KindBadSchedule:
		return fmt.Sprintf("⏸ %s has a bad schedule", name)
	case brief.KindHeld:
		return fmt.Sprintf("⏸ held send from %s (%s)", name, briefAge(l.Age))
	case brief.KindGoal:
		return fmt.Sprintf("⏸ goal \"%s\" paused %s%s", name, briefAge(l.Age), detail)
	case brief.KindCost:
		return fmt.Sprintf("💸 %s cost $%.2f, %.1f× the usual", name, l.CostUSD, l.Ratio)
	}
	return name
}

// briefCount returns n with the singular or the plural noun.
func briefCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// briefAge returns d as "<n>m" under one hour, "<n>h" under one day, else "<n>d".
func briefAge(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}

// BriefCommand returns the owner-only command /brief. It replies with the status text that fn
// builds, also when nothing needs the owner. fn makes no model call. app.New registers it.
func BriefCommand(fn func(ctx context.Context) (text string, empty bool, err error)) Command {
	return Command{
		Name:      "brief",
		Help:      briefHelp,
		WhileBusy: true,
		Run: func(ctx context.Context, c *CommandCtx, args string) error {
			if groupChat(c) {
				c.send(Reply{Text: briefGroupText})
				return nil
			}
			text, _, err := fn(ctx)
			if err != nil {
				c.send(Reply{Text: "⚠️ " + EscapeMarkdown(err.Error())})
				return nil
			}
			c.send(Reply{Text: text})
			return nil
		},
	}
}
