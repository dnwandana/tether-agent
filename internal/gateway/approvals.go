package gateway

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
)

const (
	approvalsHelp    = "See and remove the Always answers, or read the decision log: /approvals [log [all] | remove <number> <code> | remove all <list code>]"
	approvalsEmpty   = "No Always answers. The gate asks for each dangerous command."
	approvalsCLILine = "Send /approvals remove <number> <code> to remove an answer."
	approvalsStale   = "The list changed. Send /approvals to see it again."
	// approvalsCLIAllLine is the CLI line for "Remove all". %s is the list code.
	approvalsCLIAllLine = "Send /approvals remove all %s to remove all answers."
	approvalsChanged    = "⚠️ The list changed. Nothing was removed. This is the list now."
	approvalsGone       = "ℹ️ This answer is already removed. This is the list now."
	// approvalsRemoved is the result line of a remove. %s is the description of the rule.
	approvalsRemoved    = "✅ Removed the Always answer for %s. The gate asks again."
	approvalsAllGone    = "ℹ️ All answers are already removed."
	approvalsNotRemoved = "Not removed."
	approvalsAskAgain   = "The gate asks again for each dangerous command."

	// approvalsMaxButtons is the number of Remove buttons that a list reply shows.
	approvalsMaxButtons = 20
	// approvalsButtonsPerRow is the number of Remove buttons in one button row.
	approvalsButtonsPerRow = 4
	// approvalsMaxRows is the number of rows that the list text shows. The newest rows come first.
	approvalsMaxRows = 20
	// approvalsMore is the last line of a list with more rows. %d is the number of the other rows.
	approvalsMore = "and %d more"
	// approvalsRemovedNotice is the notice of the rows that the update removed. %d is the count.
	approvalsRemovedNotice  = "%d old Always answers were removed by the update. The gate asks again once for each command."
	approvalsRemovedNotice1 = "1 old Always answer was removed by the update. The gate asks again once for each command."
	// approvalsLatchLine is the first line while the saved answers are off. %s is the escaped cause.
	approvalsLatchLine = "Saved answers are off, because the answer store is damaged: %s. Restart the gateway after you repair it."

	// workdirMaxChars is the longest workdir that the list shows without a cut.
	workdirMaxChars = 40
)

// approvalsMu puts the remove actions in order. Then two fast taps edit the list message in
// the order of their removes, and the last edit shows the true list.
var approvalsMu sync.Mutex

// ApprovalsCommand returns the owner-only command /approvals. It lists the "Always"
// answers of gate and removes one answer. app.New registers it with the gate of the
// process. A remove clears the session answers of this process only.
func ApprovalsCommand(gate *security.Gate) Command {
	return Command{
		Name:      "approvals",
		Help:      approvalsHelp,
		WhileBusy: true,
		Run: func(ctx context.Context, c *CommandCtx, args string) error {
			words := strings.Fields(args)
			if len(words) > 0 && words[0] == "log" {
				return approvalsLog(ctx, c, gate, len(words) > 1 && words[1] == "all")
			}
			if len(words) > 0 && words[0] == "remove" {
				return approvalsRemove(ctx, c, gate, words[1:])
			}
			return approvalsList(ctx, c, gate)
		},
	}
}

// approvalsRows returns the "Always" rows of gate. A nil gate has no rows.
func approvalsRows(ctx context.Context, gate *security.Gate) ([]security.AlwaysRow, error) {
	if gate == nil {
		return nil, nil
	}
	return gate.ListAlways(ctx)
}

// approvalCode returns the first 8 hex characters of the SHA-256 hash of the row id. The
// code shows that a remove action belongs to the row that the user saw.
func approvalCode(id string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(id)))[:8]
}

// isRowCode returns true when code has the form of a row code: 8 lowercase hex characters.
func isRowCode(code string) bool {
	if len(code) != 8 {
		return false
	}
	for _, r := range code {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// approvalsListCode returns the first 8 hex characters of the SHA-256 hash of the row ids of
// all rows, in list order, joined with "\n". The code shows that a "Remove all" action
// belongs to the list that the user saw. It covers also the rows that have no button.
func approvalsListCode(rows []security.AlwaysRow) string {
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	return approvalCode(strings.Join(ids, "\n"))
}

// approvalDescription returns the escaped description of a rule. An unknown rule shows its
// ID.
func approvalDescription(patternID string) string {
	if p, ok := security.PatternByID(patternID); ok && p.Description != "" {
		return EscapeMarkdown(p.Description)
	}
	return EscapeMarkdown(patternID)
}

// groupChat reports whether the caller writes in a Telegram group. The members of a group
// can read the reply, so the list shows no command text there.
func groupChat(c *CommandCtx) bool {
	return strings.HasPrefix(c.Key, "telegram:") && !strings.HasPrefix(c.Key, "telegram:dm:")
}

// oneLine returns s as one visible line: hidden runes are shown and each newline is a space.
func oneLine(s string) string {
	s = security.ShowHidden(s)
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
}

// codeText returns s as one line in a code span. A text with a backtick cannot go in a code
// span, so it goes out as escaped plain text.
func codeText(s string) string {
	s = oneLine(s)
	if strings.Contains(s, "`") {
		return EscapeMarkdown(s)
	}
	return "`" + s + "`"
}

// shortWorkdir returns dir with the home folder as "~". A dir of more than 40 characters
// keeps its last 39 characters after "…".
func shortWorkdir(dir string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && home != "/" {
		if dir == home {
			dir = "~"
		} else if strings.HasPrefix(dir, home+"/") {
			dir = "~" + dir[len(home):]
		}
	}
	if r := []rune(dir); len(r) > workdirMaxChars {
		dir = "…" + string(r[len(r)-(workdirMaxChars-1):])
	}
	return dir
}

// alwaysPreviewLine returns the second line of a row: the preview and the workdir. A row
// with no preview has no second line.
func alwaysPreviewLine(row security.AlwaysRow) string {
	if row.Preview == "" {
		return ""
	}
	line := "  " + codeText(row.Preview)
	if row.Workdir != "" {
		line += " in " + codeText(shortWorkdir(oneLine(row.Workdir)))
	}
	return line
}

// approvalsList sends the list of the "Always" answers.
func approvalsList(ctx context.Context, c *CommandCtx, gate *security.Gate) error {
	rows, err := approvalsRows(ctx, gate)
	var first []string
	notice := false
	if gate != nil {
		if off, cause := gate.GrantsOff(); off {
			// A damaged store gives the latch line and no rows.
			rows, err = nil, nil
			first = append(first, fmt.Sprintf(approvalsLatchLine, EscapeMarkdown(cause)))
		}
		if n, nerr := gate.RemovedAlwaysCount(ctx); nerr == nil && n > 0 {
			notice = true
			if n == 1 {
				first = append(first, approvalsRemovedNotice1)
			} else {
				first = append(first, fmt.Sprintf(approvalsRemovedNotice, n))
			}
		}
	}
	if err != nil {
		return err
	}
	c.send(approvalsReply(c, strings.Join(first, "\n"), rows))
	if notice {
		return gate.ClearRemovedAlways(ctx)
	}
	return nil
}

// approvalsReply returns the list reply of rows. A first line that is not empty comes before
// the list, with a blank line between them. The reply sets Edit, so that a tap edits the list
// message to the true list.
func approvalsReply(c *CommandCtx, first string, rows []security.AlwaysRow) Reply {
	body := approvalsEmpty
	var buttons [][]Button
	if len(rows) > 0 {
		now := nowFunc()
		lines := []string{"🛡 **Always answers**"}
		group := groupChat(c)
		for i, row := range rows {
			if i == approvalsMaxRows {
				lines = append(lines, fmt.Sprintf(approvalsMore, len(rows)-approvalsMaxRows))
				break
			}
			desc := approvalDescription(row.PatternID)
			if strings.HasPrefix(row.PatternID, "mcp:") {
				desc = "MCP write · " + EscapeMarkdown(oneLine(row.Server)) + " · " + EscapeMarkdown(oneLine(row.ToolName))
			}
			lines = append(lines, fmt.Sprintf("%d. %s · %s · `%s`", i+1,
				desc, formatDate(row.CreatedAt, now), approvalCode(row.ID)))
			if !group && !strings.HasPrefix(row.PatternID, "mcp:") {
				if second := alwaysPreviewLine(row); second != "" {
					lines = append(lines, second)
				}
			}
		}
		body = strings.Join(lines, "\n")
		if c.interactive() {
			buttons = approvalsButtons(rows)
		} else {
			body += "\n\n" + approvalsCLILine
			if len(rows) > 1 {
				body += "\n" + fmt.Sprintf(approvalsCLIAllLine, approvalsListCode(rows))
			}
		}
	}
	if first != "" {
		body = first + "\n\n" + body
	}
	return Reply{Text: body, Buttons: buttons, Edit: true}
}

// approvalsButtons returns the Remove buttons of rows, and a "Remove all" row for 2 or more
// rows.
func approvalsButtons(rows []security.AlwaysRow) [][]Button {
	var buttons [][]Button
	for i, row := range rows {
		if i == approvalsMaxButtons {
			break
		}
		if i%approvalsButtonsPerRow == 0 {
			buttons = append(buttons, nil)
		}
		last := len(buttons) - 1
		buttons[last] = append(buttons[last], Button{
			Text: fmt.Sprintf("Remove %d", i+1),
			Data: fmt.Sprintf("/approvals remove %d %s", i+1, approvalCode(row.ID)),
		})
	}
	if len(rows) > 1 {
		buttons = append(buttons, []Button{{
			Text: "🗑 Remove all",
			Data: "/approvals remove all " + approvalsListCode(rows),
		}})
	}
	return buttons
}

// approvalsRemove removes the answer that the number and the code name, and shows the list
// that is left. A stale action removes nothing and shows the current list.
func approvalsRemove(ctx context.Context, c *CommandCtx, gate *security.Gate, words []string) error {
	// This branch comes before the lock, because approvalsRemoveAll takes the lock itself.
	if len(words) > 0 && words[0] == "all" {
		switch {
		case len(words) == 2:
			return approvalsRemoveAll(ctx, c, gate, words[1], false)
		case len(words) == 3 && words[2] == confirmWord:
			return approvalsRemoveAll(ctx, c, gate, words[1], true)
		}
		c.send(Reply{Text: approvalsStale})
		return nil
	}
	if len(words) != 2 {
		c.send(Reply{Text: approvalsStale})
		return nil
	}
	// The lock starts before the first read of the rows and ends after the send.
	approvalsMu.Lock()
	defer approvalsMu.Unlock()
	rows, err := approvalsRows(ctx, gate)
	if err != nil {
		return err
	}
	n, convErr := strconv.Atoi(words[0])
	if convErr != nil || n < 1 || n > len(rows) || approvalCode(rows[n-1].ID) != words[1] {
		// A row code that no row has means that the answer is already removed, for example
		// by the first tap of a double tap. A code that is not a row code is a typo.
		first := approvalsChanged
		if isRowCode(words[1]) {
			first = approvalsGone
			for _, row := range rows {
				if approvalCode(row.ID) == words[1] {
					first = approvalsChanged
					break
				}
			}
		}
		c.send(approvalsReply(c, first, rows))
		return nil
	}
	row := rows[n-1]
	if err := gate.RemoveAlways(ctx, row.ID); err != nil {
		return err
	}
	// Read the rows again, so that the reply shows the true list.
	if rows, err = approvalsRows(ctx, gate); err != nil {
		return err
	}
	c.send(approvalsReply(c, fmt.Sprintf(approvalsRemoved, approvalDescription(row.PatternID)), rows))
	return nil
}

// approvalsRemoveAll removes all rows of the list that listCode names. Without confirmed, it
// asks first. It removes nothing when the list changed.
func approvalsRemoveAll(ctx context.Context, c *CommandCtx, gate *security.Gate, listCode string, confirmed bool) error {
	approvalsMu.Lock()
	defer approvalsMu.Unlock()
	// stale sends the current list and returns true when listCode does not name it.
	stale := func(rows []security.AlwaysRow) bool {
		switch {
		case len(rows) == 0:
			c.send(approvalsReply(c, approvalsAllGone, rows))
		case approvalsListCode(rows) != listCode:
			c.send(approvalsReply(c, approvalsChanged, rows))
		default:
			return false
		}
		return true
	}
	rows, err := approvalsRows(ctx, gate)
	if err != nil {
		return err
	}
	if stale(rows) {
		return nil
	}
	if !confirmed {
		question, prompt := fmt.Sprintf("🗑 **Remove all %d Always answers?**", len(rows)),
			fmt.Sprintf("Remove all %d Always answers?", len(rows))
		if len(rows) == 1 {
			question, prompt = "🗑 **Remove 1 Always answer?**", "Remove 1 Always answer?"
		}
		action := "/approvals remove all " + listCode + " " + confirmWord
		switch {
		case c.interactive():
			// Cancel sends /approvals. The list reply edits this message back to the list.
			c.send(Reply{Text: question + "\n" + approvalsAskAgain, Edit: true, Buttons: [][]Button{
				{{Text: "Cancel", Data: "/approvals"}},
				{{Text: "🗑 Remove all", Data: action}},
			}})
			return nil
		case c.Confirm == nil:
			c.send(Reply{Text: question + "\n" + approvalsAskAgain + "\nTo remove all, send: " + action})
			return nil
		case !c.Confirm(prompt + " " + approvalsAskAgain):
			c.send(approvalsReply(c, approvalsNotRemoved, rows))
			return nil
		}
		// The other process can change the rows while the CLI asks.
		if rows, err = approvalsRows(ctx, gate); err != nil {
			return err
		}
		if stale(rows) {
			return nil
		}
	}
	// On an error, the rows before it stay removed, and runCommand sends the error reply.
	for _, row := range rows {
		if err := gate.RemoveAlways(ctx, row.ID); err != nil {
			return err
		}
	}
	left, err := approvalsRows(ctx, gate)
	if err != nil {
		return err
	}
	result := fmt.Sprintf("✅ Removed %d Always answers. The gate asks again.", len(rows))
	if len(rows) == 1 {
		result = "✅ Removed 1 Always answer. The gate asks again."
	}
	c.send(approvalsReply(c, result, left))
	return nil
}

// approvalsLogRows is the number of rows of the log view.
const approvalsLogRows = 15

// approvalsLogPreviewRunes is the longest preview of a log row, in runes.
const approvalsLogPreviewRunes = 60

// approvalsLog sends the last decisions of the gate for the scope of this chat, or for all
// scopes with all.
func approvalsLog(ctx context.Context, c *CommandCtx, gate *security.Gate, all bool) error {
	if all && !privateChat(c) {
		c.send(Reply{Text: "Send /approvals log all in a private chat. A group can read this reply."})
		return nil
	}
	empty, title := "No approval decisions in this topic yet.", "this topic"
	if all {
		empty, title = "No approval decisions yet.", "all chats"
	}
	scope := ModeScope(c.Session)
	var rows []security.LogRow
	if gate != nil && (all || scope != "") {
		var err error
		if rows, err = gate.ListLog(ctx, scope, all, approvalsLogRows); err != nil {
			return err
		}
	}
	if len(rows) == 0 {
		c.send(Reply{Text: empty})
		return nil
	}
	now := nowFunc()
	lines := []string{fmt.Sprintf("📜 **Approval log** (%s, last %d)", title, len(rows))}
	for _, r := range rows {
		lines = append(lines, logRowText(r, now, all))
	}
	c.send(Reply{Text: strings.Join(lines, "\n")})
	return nil
}

// privateChat reports whether only the owner can read a reply in the chat of c: the CLI or a
// Telegram private chat.
func privateChat(c *CommandCtx) bool {
	return !groupChat(c)
}

// logRowText returns the lines of one log row. all adds the scope.
func logRowText(r security.LogRow, now time.Time, all bool) string {
	icon := "❓"
	switch r.Decision {
	case security.DecisionAllowed:
		icon = "✅"
	case security.DecisionDenied:
		icon = "❌"
	case security.DecisionBlocked:
		icon = "⛔"
	}
	parts := []string{EscapeMarkdown(oneLine(r.Source))}
	if r.Kind == "cron" {
		parts = append(parts, "cron")
	}
	switch r.Answer {
	case "once":
		parts = append(parts, "Once")
	case "redacted":
		parts = append(parts, "Send redacted")
	case "session":
		parts = append(parts, "Session")
	case "always":
		parts = append(parts, "Always")
	}
	switch {
	case r.Class != "":
		parts = append(parts, fmt.Sprintf("MCP %s (%s · %s)", EscapeMarkdown(oneLine(r.Class)),
			EscapeMarkdown(oneLine(r.Server)), EscapeMarkdown(oneLine(r.ToolName))))
	case r.Tool == "goal_gate" && r.Rule == "":
		parts = append(parts, "goal gate")
	default:
		parts = append(parts, logRuleText(r.Rule))
	}
	if all {
		if r.Scope == "" {
			parts = append(parts, "no chat")
		} else {
			parts = append(parts, EscapeMarkdown(oneLine(r.Scope)))
		}
	}
	text := logTime(r.At, now) + " " + icon + " " + strings.Join(parts, " · ")
	if r.Preview != "" && r.Class == "" {
		p := oneLine(r.Preview)
		if rs := []rune(p); len(rs) > approvalsLogPreviewRunes {
			p = string(rs[:approvalsLogPreviewRunes-1]) + "…"
		}
		text += "\n  " + codeText(p)
	}
	return text
}

// logRuleText returns the escaped text of the rule of a log row.
// security.RuleText holds the texts.
func logRuleText(rule string) string {
	if text, ok := security.RuleText(rule); ok {
		return EscapeMarkdown(text)
	}
	return EscapeMarkdown(oneLine(rule))
}

// logTime returns "15:04" for a time of today and "Jan 2 15:04" for another day.
func logTime(t, now time.Time) string {
	t, now = t.In(time.Local), now.In(time.Local)
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.Date()
	if y1 == y2 && m1 == m2 && d1 == d2 {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}
