package gateway

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/sessions"
)

// sessionsPageSize is the number of sessions on one page of a session list.
const sessionsPageSize = 8

// newSessionTitle is the name of a session that has no title.
const newSessionTitle = "New session"

// buttonTitleMax is the maximum number of runes of a title in a button label.
const buttonTitleMax = 32

// noAction is the action of a button that does nothing.
const noAction = "·"

// nowFunc returns the time for the session lists and replies. Tests replace it.
var nowFunc = time.Now

// listMode selects the session list that listReply makes.
type listMode int

const (
	// listSessions is the /sessions list. The current session is on a separate top line.
	listSessions listMode = iota
	// listResume is the /resume list. It is the same as listSessions, with another header.
	listResume
	// listDelete is the /delete list. The current session is in the numbered list.
	listDelete
	// listArchived is the /sessions archived list.
	listArchived
)

// listArgs holds the arguments of /sessions, /resume, and /delete.
type listArgs struct {
	// Page is the page number. It is 0 when the arguments have no page number.
	Page int
	// Search holds the search words, or "" for all sessions.
	Search string
	// ID is a session ID. Only /resume and /delete set it.
	ID string
	// Archived selects the archived list. Only /sessions sets it.
	Archived bool
}

// parseListArgs parses the arguments of the command cmd ("sessions", "resume", or
// "delete"). A first word of 1 to 4 digits is a page number, and the rest is the search
// words. For /sessions, the search words "archived" select the archived list. Else, for
// /resume and /delete, one word of 19 hex characters is a session ID. Other text is a
// search.
func parseListArgs(cmd, args string) listArgs {
	var a listArgs
	fields := strings.Fields(args)
	if len(fields) > 0 && len(fields[0]) <= 4 && isDigits(fields[0]) {
		a.Page, _ = strconv.Atoi(fields[0])
		a.Page = max(a.Page, 1)
		fields = fields[1:]
	}
	rest := strings.Join(fields, " ")
	switch {
	case cmd == "sessions" && strings.EqualFold(rest, "archived"):
		a.Archived = true
	case cmd != "sessions" && a.Page == 0 && isSessionID(rest):
		a.ID = rest
	default:
		a.Search = rest
	}
	return a
}

// isDigits reports whether s is not empty and has only ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isSessionID reports whether s has the form of a session ID: 19 hex characters.
func isSessionID(s string) bool {
	if len(s) != 19 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// formatAge returns the time t relative to now, in local time: "now", "5 min ago",
// "today", "yesterday", "3 days ago", "Oct 14", or "Oct 14, 2025".
func formatAge(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	}
	switch days := daysBetween(t, now); {
	case days == 0:
		return "today"
	case days == 1:
		return "yesterday"
	case days >= 2 && days <= 6:
		return fmt.Sprintf("%d days ago", days)
	}
	return formatDate(t, now)
}

// formatDay returns the day of t relative to now, in local time: "today", "yesterday",
// "Oct 14", or "Oct 14, 2025".
func formatDay(t, now time.Time) string {
	switch daysBetween(t, now) {
	case 0:
		return "today"
	case 1:
		return "yesterday"
	}
	return formatDate(t, now)
}

// formatDate returns the local date of t. The year shows only when it is not the year of
// now.
func formatDate(t, now time.Time) string {
	t = t.In(time.Local)
	if t.Year() == now.In(time.Local).Year() {
		return t.Format("Jan 2")
	}
	return t.Format("Jan 2, 2006")
}

// daysBetween returns the number of local calendar days from the day of t to the day of
// now.
func daysBetween(t, now time.Time) int {
	day := func(x time.Time) time.Time {
		y, m, d := x.In(time.Local).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	}
	// The rounding removes the hour that a change to or from summer time adds or removes.
	return int(math.Round(day(now).Sub(day(t)).Hours() / 24))
}

// sessionTitle returns the title for a reply text. The Markdown characters of the title
// show literally. A session with no title is "New session".
func sessionTitle(title string) string {
	if title == "" {
		return newSessionTitle
	}
	return EscapeMarkdown(title)
}

// plainTitle returns the title for a plain text, or "New session" for no title.
func plainTitle(title string) string {
	if title == "" {
		return newSessionTitle
	}
	return title
}

// buttonTitle returns the title for a button label. A label has no Markdown, so the title
// is not escaped. A long title is cut to buttonTitleMax runes, with "…" at the end.
func buttonTitle(title string) string {
	title = plainTitle(title)
	if utf8.RuneCountInString(title) <= buttonTitleMax {
		return title
	}
	return string([]rune(title)[:buttonTitleMax-1]) + "…"
}

// msgCount returns "1 msg" or "{n} msgs".
func msgCount(n int) string {
	if n == 1 {
		return "1 msg"
	}
	return fmt.Sprintf("%d msgs", n)
}

// listReply returns one page of the session list of mode for the arguments a. On a
// text-only channel, each row shows the session ID, and the last lines tell the commands
// to send instead of the buttons.
func (c *CommandCtx) listReply(ctx context.Context, mode listMode, a listArgs, now time.Time) (Reply, error) {
	cur := c.Session
	if cur != nil && cur.Archived != (mode == listArchived) {
		cur = nil
	}
	// /sessions and /resume show the current session on a separate top line. A search
	// shows it as a row when its title matches.
	topLine := cur != nil && (mode == listSessions || mode == listResume) && a.Search == ""
	q := sessions.ListQuery{Limit: sessionsPageSize, Search: a.Search, Archived: mode == listArchived}
	if topLine {
		q.Exclude = cur.ID
	}
	page := max(a.Page, 1)
	q.Offset = (page - 1) * sessionsPageSize
	rows, total, err := c.store().ListPage(ctx, c.Key, q)
	if err != nil {
		return Reply{}, err
	}
	pages := (total + sessionsPageSize - 1) / sessionsPageSize
	if total > 0 && page > pages {
		// A page number after the last page shows the last page.
		page = pages
		q.Offset = (page - 1) * sessionsPageSize
		if rows, total, err = c.store().ListPage(ctx, c.Key, q); err != nil {
			return Reply{}, err
		}
	}

	head := listEmoji(mode)
	words := EscapeMarkdown(a.Search)
	if total == 0 {
		switch {
		case a.Search != "":
			return Reply{Text: fmt.Sprintf("%s **No sessions with \"%s\"**\nSend /%s to see all sessions.",
				head, words, listCommand(mode))}, nil
		case mode == listArchived:
			return Reply{Text: "🗄 **No archived sessions**"}, nil
		case !topLine:
			return Reply{Text: "📭 **No sessions yet**\nSend a message to start one."}, nil
		}
	}

	var b strings.Builder
	switch {
	case a.Search != "":
		fmt.Fprintf(&b, "%s **Sessions with \"%s\"** · %d found", head, words, total)
	case total == 0:
		fmt.Fprintf(&b, "%s **%s**", head, listHeader(mode))
	default:
		first := q.Offset + 1
		fmt.Fprintf(&b, "%s **%s** · %d–%d of %d", head, listHeader(mode), first, q.Offset+len(rows), total)
	}
	if topLine {
		fmt.Fprintf(&b, "\n**▸ %s** · %s _(current)_", sessionTitle(cur.Title), formatAge(cur.LastActive, now))
	}

	interactive := c.interactive()
	action := "/resume "
	if mode == listDelete {
		action = "/delete "
	}
	var numbers []Button
	if len(rows) > 0 {
		b.WriteString("\n")
	}
	for i, s := range rows {
		pos := q.Offset + i + 1
		fmt.Fprintf(&b, "\n%d. **%s** · %s · %s", pos, sessionTitle(s.Title), formatAge(s.LastActive, now), msgCount(s.MessageCount))
		if c.Session != nil && s.ID == c.Session.ID {
			b.WriteString(" _(current)_")
		}
		if !interactive {
			b.WriteString(" · ")
			b.WriteString(s.ID)
		}
		numbers = append(numbers, Button{Text: strconv.Itoa(pos), Data: action + s.ID})
	}

	if len(rows) > 0 {
		b.WriteString("\n\n")
		switch {
		case interactive && mode == listDelete:
			b.WriteString("Tap a number. I ask again before I delete.")
		case interactive && mode == listArchived:
			b.WriteString("Tap a number to resume. This also takes it out of the archive.")
		case interactive:
			b.WriteString("Tap a number to resume.")
		case mode == listDelete:
			b.WriteString("Send /delete <id> to delete.")
		case mode == listArchived:
			b.WriteString("Send /resume <id> to resume. This also takes it out of the archive.")
		default:
			b.WriteString("Send /resume <id> to resume.")
		}
		if !interactive && page < pages {
			fmt.Fprintf(&b, "\nSend %s for the next page.", pageCommand(mode, page+1, a.Search))
		}
	}
	if mode == listSessions && a.Search == "" {
		n, err := c.store().CountArchived(ctx, c.Key)
		if err != nil {
			return Reply{}, err
		}
		if n > 0 {
			fmt.Fprintf(&b, "\n\n_%d archived. Send /sessions archived to see them._", n)
		}
	}

	r := Reply{Text: b.String(), Edit: a.Page != 0}
	if !interactive {
		return r, nil
	}
	for len(numbers) > 0 {
		n := min(len(numbers), 4)
		r.Buttons = append(r.Buttons, numbers[:n])
		numbers = numbers[n:]
	}
	if pages > 1 {
		prev, next := Button{Text: noAction, Data: noAction}, Button{Text: noAction, Data: noAction}
		if page > 1 {
			prev = Button{Text: "◀ Prev", Data: pageCommand(mode, page-1, a.Search)}
		}
		if page < pages {
			next = Button{Text: "Next ▶", Data: pageCommand(mode, page+1, a.Search)}
		}
		r.Buttons = append(r.Buttons, []Button{prev, {Text: fmt.Sprintf("%d / %d", page, pages), Data: noAction}, next})
	}
	return r, nil
}

// listEmoji returns the emoji of the header of mode.
func listEmoji(mode listMode) string {
	switch mode {
	case listResume:
		return "↩️"
	case listDelete:
		return "🗑"
	case listArchived:
		return "🗄"
	}
	return "📋"
}

// listHeader returns the bold header text of mode.
func listHeader(mode listMode) string {
	switch mode {
	case listResume:
		return "Resume a session"
	case listDelete:
		return "Delete a session"
	case listArchived:
		return "Archived sessions"
	}
	return "Your sessions"
}

// listCommand returns the name of the command that shows the list of mode.
func listCommand(mode listMode) string {
	switch mode {
	case listResume:
		return "resume"
	case listDelete:
		return "delete"
	}
	return "sessions"
}

// pageCommand returns the command line that shows page of the list of mode.
func pageCommand(mode listMode, page int, search string) string {
	line := fmt.Sprintf("/%s %d", listCommand(mode), page)
	switch {
	case mode == listArchived:
		line += " archived"
	case search != "":
		line += " " + search
	}
	return line
}
