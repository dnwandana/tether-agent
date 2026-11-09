package telegram

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/security"
)

// approvalHeader is the first line of an approval message after the answer. Title is
// bold. Detail follows a middle dot, and an empty Detail has no dot.
type approvalHeader struct{ Emoji, Title, Detail string }

// headerFor returns the header for the answer s. A timeout names the wait when timeout
// is above zero.
func headerFor(s security.Scope, timeout time.Duration) approvalHeader {
	const allowed, denied = "Approval allowed", "Approval denied"
	switch s {
	case security.ScopeOnce:
		return approvalHeader{"✅", allowed, "once"}
	case security.ScopeSession:
		return approvalHeader{"✅", allowed, "this session"}
	case security.ScopeAlways:
		return approvalHeader{"✅", allowed, "always"}
	case security.ScopeTimeout:
		if timeout > 0 {
			return approvalHeader{"⌛", denied, "no answer in " + formatWait(timeout)}
		}
		return approvalHeader{"⌛", denied, "no answer"}
	}
	// A stopped turn and an unknown scope deny the command.
	return approvalHeader{"❌", denied, ""}
}

// approvalKeyboard returns the buttons of the approval prompt id. noAlways removes the
// "always" button. noSession keeps only "once" and "deny".
func approvalKeyboard(id string, noAlways, noSession bool) *InlineKeyboardMarkup {
	btn := func(text, arg string) InlineKeyboardButton {
		return InlineKeyboardButton{Text: text, CallbackData: callbackData(approvalPrefix, id, arg)}
	}
	once, session := btn("✅ Allow once", "once"), btn("✅ Allow in this session", "session")
	always, deny := btn("✅ Always allow", "always"), btn("❌ Deny", "deny")
	var rows [][]InlineKeyboardButton
	switch {
	case noSession:
		rows = [][]InlineKeyboardButton{{once, deny}}
	case noAlways:
		rows = [][]InlineKeyboardButton{{once, session}, {deny}}
	default:
		rows = [][]InlineKeyboardButton{{once, session}, {always, deny}}
	}
	return &InlineKeyboardMarkup{InlineKeyboard: rows}
}

// format returns the header with title between the marks open and close. esc
// escapes the title and the detail.
func (h approvalHeader) format(mark string, esc func(string) string) string {
	s := h.Emoji + " " + mark + esc(h.Title) + mark
	if h.Detail != "" {
		s += " · " + esc(h.Detail)
	}
	return s
}

// markdown returns the header in common Markdown, for the /approve and /deny replies.
func (h approvalHeader) markdown() string {
	return h.format("**", func(s string) string { return s })
}

// mdv2 returns the header in MarkdownV2.
func (h approvalHeader) mdv2() string {
	return h.format("*", escapeMDV2)
}

// plain returns the header with no markup, for the button pop-up and the plain fallback.
func (h approvalHeader) plain() string {
	return h.format("", func(s string) string { return s })
}

// escapePre escapes s for a MarkdownV2 pre block or code span: only "\" and "`".
func escapePre(s string) string {
	var b strings.Builder
	b.Grow(len(s) + len(s)/8)
	for _, r := range s {
		if r == '\\' || r == '`' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// moreLine returns the last line of a cut command.
func moreLine(n int) string { return fmt.Sprintf("… (+%d lines)", n) }

// fitLines splits cmd into lines and returns the lines that fit in budget UTF-16 units.
// A line costs UTF16Len(esc(line)) + perLine. The kept text also stays within
// maxCommandPreview runes. When lines do not fit, the last returned line is moreLine with
// the number of lines that it left out. When the first line does not fit, it cuts that
// line at a rune and adds "…".
func fitLines(cmd string, esc func(string) string, perLine, budget int) []string {
	lines := strings.Split(strings.TrimRight(cmd, "\n"), "\n")
	cost := func(s string) int { return UTF16Len(esc(s)) + perLine }
	used, runes, k := 0, 0, 0
	for k < len(lines) {
		c, r := cost(lines[k]), utf8.RuneCountInString(lines[k])+1
		if used+c > budget || runes+r > maxCommandPreview+1 {
			break
		}
		used, runes, k = used+c, runes+r, k+1
	}
	if k == len(lines) {
		return lines
	}
	// Make room for the marker line.
	for k > 0 && used+cost(moreLine(len(lines)-k)) > budget {
		k--
		used -= cost(lines[k])
	}
	if k > 0 {
		return append(lines[:k:k], moreLine(len(lines)-k))
	}
	room := budget
	var tail []string
	if len(lines) > 1 {
		tail = []string{moreLine(len(lines) - 1)}
		room -= cost(tail[0])
	}
	rs := []rune(lines[0])
	n := min(len(rs), maxCommandPreview-1)
	for n > 0 && cost(string(rs[:n])+"…") > room {
		n--
	}
	return append([]string{string(rs[:n]) + "…"}, tail...)
}

// buildApprovalPrompt returns the approval prompt as MarkdownV2 and as plain text. The
// command is cut first, so the timeout line stays.
func buildApprovalPrompt(req security.ApprovalRequest, timeout time.Duration) (mdv2, plain string) {
	pattern, _, _ := req.FallbackParts()
	child := ""
	if req.ChildName != "" {
		child = "(from subagent " + req.ChildName + ")"
	}
	wait := ""
	if timeout > 0 {
		wait = formatWait(timeout)
	}

	var head strings.Builder
	head.WriteString("🛡 *Approval needed*\n")
	if req.Summary != "" {
		head.WriteString(escapeMDV2(req.Summary))
		head.WriteString("\n")
	}
	head.WriteString("_Flagged: ")
	head.WriteString(escapeMDV2(pattern))
	head.WriteString("_")
	if child != "" {
		head.WriteString(" ")
		head.WriteString(escapeMDV2(child))
	}
	head.WriteString("\n```\n")
	tail := "\n```"
	if wait != "" {
		tail += "\n_No answer in " + escapeMDV2(wait) + " \\= deny\\._"
	}

	lines := fitLines(security.ShowHidden(req.Command), escapePre, 1, maxMessageLen-UTF16Len(head.String())-UTF16Len(tail))
	esc := make([]string, len(lines))
	for i, l := range lines {
		esc[i] = escapePre(l)
	}
	mdv2 = head.String() + strings.Join(esc, "\n") + tail

	parts := []string{"🛡 Approval needed"}
	if req.Summary != "" {
		parts = append(parts, req.Summary)
	}
	flagged := "Flagged: " + pattern
	if child != "" {
		flagged += " " + child
	}
	parts = append(parts, flagged)
	parts = append(parts, lines...)
	if wait != "" {
		parts = append(parts, "No answer in "+wait+" = deny.")
	}
	return mdv2, cutUTF16(strings.Join(parts, "\n"), maxMessageLen)
}

// buildApprovalResult returns the approval message after the answer. full has the header,
// the summary, and the command in a closed expandable quote. short has only the header and
// the summary. plain is short with no markup.
func buildApprovalResult(h approvalHeader, req security.ApprovalRequest) (full, short, plain string) {
	sumMD, sumPlain := escapeMDV2(req.Summary), req.Summary
	if req.Summary == "" {
		pattern, line, more := req.FallbackParts()
		sumMD, sumPlain = escapeMDV2(pattern), pattern
		if line != "" {
			sumMD += ": `" + escapePre(line) + "`"
			sumPlain += ": " + line
		}
		if more > 0 {
			n := fmt.Sprintf("(+%d lines)", more)
			sumMD += " " + escapeMDV2(n)
			sumPlain += " " + n
		}
	}
	short = h.mdv2() + "\n" + sumMD

	// Each line costs ">" and "\n". The budget also keeps room for the "\n" after
	// short, the "**" of the first line, and the final "||".
	lines := fitLines(security.ShowHidden(req.Command), escapeMDV2, 2, maxMessageLen-UTF16Len(short)-1-4)
	var b strings.Builder
	b.WriteString(short)
	for i, l := range lines {
		if i == 0 {
			b.WriteString("\n**>")
		} else {
			b.WriteString("\n>")
		}
		b.WriteString(escapeMDV2(l))
	}
	b.WriteString("||")
	return b.String(), short, cutUTF16(h.plain()+"\n"+sumPlain, maxMessageLen)
}
