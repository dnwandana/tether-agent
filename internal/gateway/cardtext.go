package gateway

import (
	"strconv"
	"time"
)

// CardResult describes the answer of an approval card, a pin card, or a send card.
type CardResult struct {
	// Kind is "approval", "pin", or "send".
	Kind string
	// Scope is "deny", "once", "session", "always", "redacted", or "timeout".
	Scope string
	// Wait is the time that the card waited. It names the wait of a timeout.
	Wait time.Duration
	// Stopped is true when the turn stopped before an answer came.
	Stopped bool
}

// CardResultHeader returns the result header of r. Telegram, the CLI, and the dashboard
// show this one text. A stopped turn has its own text and never shows the deny text.
func CardResultHeader(r CardResult) string {
	if r.Stopped {
		return "⏹ No answer · the turn stopped"
	}
	if r.Kind == "pin" {
		switch r.Scope {
		case "once", "session", "always", "redacted":
			return "✅ Program allowed"
		case "timeout":
			return "⌛ Program denied · no answer" + cardWait(r.Wait)
		}
		return "❌ Program denied"
	}
	switch r.Scope {
	case "once":
		return "✅ Approval allowed · once"
	case "session":
		return "✅ Approval allowed · this session"
	case "always":
		return "✅ Approval allowed · always"
	case "redacted":
		return "✅ Sent redacted"
	case "timeout":
		return "⌛ Approval denied · no answer" + cardWait(r.Wait)
	}
	return "❌ Approval denied"
}

// cardWait returns " in N min", or " in N s" under one minute. A wait of 0 or less gives "".
func cardWait(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d < time.Minute:
		return " in " + strconv.Itoa(int(d/time.Second)) + " s"
	}
	return " in " + strconv.Itoa(int(d/time.Minute)) + " min"
}
