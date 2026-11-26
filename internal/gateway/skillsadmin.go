package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/skills"
)

// The texts of /skills.
const (
	skillsOff     = "Skill commands are not available here."
	skillsUsage   = "Use /skills stale, /skills archived, /skills log, /skills keep <name>, /skills unkeep <name>, /skills restore <name>, or /skills undo <id>.<stamp>."
	skillsNoCopy  = "The skill history is not available."
	skillsNoLog   = "No skill change is recorded yet."
	skillsNoStale = "No skill is a curator candidate."
	skillsNoArch  = "No skill is archived."
	skillsLogMax  = 10
)

func init() {
	Register(Command{Name: "skills", Help: "See the skill curator and the skill copies: /skills [stale|archived|log|keep <name>|unkeep <name>|restore <name>|undo <id>.<stamp>]", Run: cmdSkills, WhileBusy: true})
}

// CuratorCardText returns the text of the curator card. It has no line for an empty list of
// archived or stale skills.
func CuratorCardText(r skills.CuratorReport) string {
	lines := []string{"🗂 Skill curator"}
	if len(r.Archived) > 0 {
		lines = append(lines, fmt.Sprintf("Archived (no read in %d days):", r.ArchiveAfterDays))
		for _, f := range r.Archived {
			lines = append(lines, "• "+EscapeMarkdown(f.Name))
		}
	}
	if n := len(r.Stale); n > 0 {
		noun := "skills"
		if n == 1 {
			noun = "skill"
		}
		lines = append(lines, fmt.Sprintf("Stale (no read in %d days): %d %s. /skills stale", r.StaleAfterDays, n, noun))
	}
	lines = append(lines, fmt.Sprintf("Kept: %d · Skipped: %d bundled, %d from the hub", r.Kept, r.SkippedBundled, r.SkippedHub))
	return strings.Join(lines, "\n")
}

func cmdSkills(ctx context.Context, c *CommandCtx, args string) error {
	l := c.Deps.Learn
	if l == nil || l.Curator == nil {
		c.send(Reply{Text: skillsOff})
		return nil
	}
	words := strings.Fields(args)
	verb := "stale"
	if len(words) > 0 {
		verb = words[0]
	}
	switch verb {
	case "stale", "archived", "log":
		if len(words) > 1 {
			c.send(Reply{Text: skillsUsage})
			return nil
		}
	case "keep", "unkeep", "restore", "undo":
		if len(words) != 2 {
			c.send(Reply{Text: skillsUsage})
			return nil
		}
	default:
		c.send(Reply{Text: skillsUsage})
		return nil
	}
	switch verb {
	case "stale":
		return c.skillsStale(ctx, l)
	case "archived":
		return c.skillsArchived(ctx, l)
	case "log":
		return c.skillsLog(l)
	case "undo":
		return c.skillsUndo(ctx, l, words[1])
	}
	name := words[1]
	var err error
	switch verb {
	case "keep":
		if err = l.Curator.SetKept(ctx, name, true); err == nil {
			c.send(Reply{Text: "✓ Kept " + EscapeMarkdown(name) + ". The curator never archives it."})
		}
	case "unkeep":
		if err = l.Curator.SetKept(ctx, name, false); err == nil {
			c.send(Reply{Text: EscapeMarkdown(name) + " is not kept now. The curator can archive it."})
		}
	case "restore":
		if err = l.Curator.Restore(ctx, name); err == nil {
			c.send(Reply{Text: "↩ Restored " + EscapeMarkdown(name) + ". It loads in new sessions."})
			if l.RefreshMenu != nil {
				l.RefreshMenu(ctx)
			}
		}
	}
	if err != nil {
		c.send(Reply{Text: EscapeMarkdown(err.Error())})
	}
	return nil
}

// skillsDay formats a time for a line of /skills.
func skillsDay(t time.Time) string { return t.Format("Jan 2") }

func (c *CommandCtx) skillsStale(ctx context.Context, l *LearnDeps) error {
	rows, err := l.Curator.Stale(ctx)
	if err != nil {
		c.send(Reply{Text: EscapeMarkdown(err.Error())})
		return nil
	}
	if len(rows) == 0 {
		c.send(Reply{Text: skillsNoStale})
		return nil
	}
	lines := []string{"🗂 **Skills that the curator checks**"}
	for _, r := range rows {
		last := "never read"
		if !r.LastActivity.IsZero() {
			last = "last read " + skillsDay(r.LastActivity)
		}
		lines = append(lines, fmt.Sprintf("%s · %s · %s", EscapeMarkdown(r.Name), r.State, last))
	}
	c.send(Reply{Text: strings.Join(lines, "\n")})
	return nil
}

func (c *CommandCtx) skillsArchived(ctx context.Context, l *LearnDeps) error {
	flags, err := l.Curator.Archived(ctx)
	if err != nil {
		c.send(Reply{Text: EscapeMarkdown(err.Error())})
		return nil
	}
	if len(flags) == 0 {
		c.send(Reply{Text: skillsNoArch})
		return nil
	}
	lines := []string{"🗂 **Archived skills**"}
	var rows [][]Button
	for _, f := range flags {
		lines = append(lines, fmt.Sprintf("%s · archived %s · %s", EscapeMarkdown(f.Name), skillsDay(f.ArchivedAt), EscapeMarkdown(f.RelDir)))
		action := "/skills restore " + f.Name
		if c.interactive() {
			if len(action) <= ActionMaxBytes {
				rows = append(rows, []Button{{Text: "↩ Restore " + f.Name, Data: action}})
			}
		} else {
			lines = append(lines, "Restore: "+action)
		}
	}
	c.send(Reply{Text: strings.Join(lines, "\n"), Buttons: rows})
	return nil
}

func (c *CommandCtx) skillsLog(l *LearnDeps) error {
	if l.Copies == nil {
		c.send(Reply{Text: skillsNoCopy})
		return nil
	}
	copies, err := l.Copies.ListCopies("", skillsLogMax)
	if err != nil {
		c.send(Reply{Text: EscapeMarkdown(err.Error())})
		return nil
	}
	if len(copies) == 0 {
		c.send(Reply{Text: skillsNoLog})
		return nil
	}
	var lines []string
	var rows [][]Button
	for i, cp := range copies {
		lines = append(lines, fmt.Sprintf("%s · %s · %s · %s · copy %d", cp.At.Format("Jan 2 15:04"),
			EscapeMarkdown(cp.Source), EscapeMarkdown(cp.Action), EscapeMarkdown(cp.Name), cp.ID))
		// A copy that an Undo reversed and a Redo did again can be undone again.
		if (&skills.Manager{Copies: l.Copies}).CopyUndone(cp) {
			continue
		}
		action := fmt.Sprintf("/skills undo %d.%s", cp.ID, cp.Stamp)
		if c.interactive() {
			if len(action) <= ActionMaxBytes {
				rows = append(rows, []Button{{Text: fmt.Sprintf("↩ Undo %d", i+1), Data: action}})
			}
		} else {
			lines = append(lines, fmt.Sprintf("Undo %d: %s", i+1, action))
		}
	}
	c.send(Reply{Text: strings.Join(lines, "\n"), Buttons: rows})
	return nil
}

func (c *CommandCtx) skillsUndo(ctx context.Context, l *LearnDeps, arg string) error {
	ref, ok := parseNoticeRef(arg)
	if !ok {
		c.send(Reply{Text: skillsUsage})
		return nil
	}
	if l.Skills == nil {
		c.send(Reply{Text: skillsOff})
		return nil
	}
	o := skills.Origin{Source: "owner"}
	if c.Session != nil {
		o.SessionID = c.Session.ID
	}
	_, err := l.Skills.UndoCopy(o, ref.id, ref.stamp)
	switch {
	case err == nil:
		c.send(Reply{Text: "↩ Undone. The skill before the change is back."})
		if l.RefreshMenu != nil {
			l.RefreshMenu(ctx)
		}
	case errors.Is(err, skills.ErrCopyStamp):
		c.send(Reply{Text: noticeTooOld})
	case errors.Is(err, skills.ErrCopyChanged):
		c.send(Reply{Text: "The skill changed after this copy. Nothing was changed. Use /skills log."})
	case errors.Is(err, skills.ErrCopyUndone):
		c.send(Reply{Text: "This change is undone already."})
	default:
		c.send(Reply{Text: EscapeMarkdown(err.Error())})
	}
	return nil
}
