package gateway

import (
	"context"
	"strings"

	"github.com/dnwandana/tether-agent/internal/security"
)

const (
	modeHelp        = "Set the approval mode of this chat or topic: /mode [read-only | guarded | full]"
	modeUnavailable = "⚠️ /mode is not available here."
	modeConfirmText = "⚠️ **Full mode** runs dangerous commands in this topic with no question. Confirm?"
	modeAskPrompt   = "Full mode runs dangerous commands in this topic with no question. Confirm?"
	modeMenuTail    = "\n\nSend /mode read-only, /mode guarded, or /mode full."
)

// ModeCommand returns the owner-only /mode command. It shows and sets the approval mode of
// the chat or the topic of the session. The mode belongs to the scope, not to the session.
func ModeCommand(gate *security.Gate) Command {
	return Command{
		Name:      "mode",
		Help:      modeHelp,
		WhileBusy: true,
		Run: func(ctx context.Context, c *CommandCtx, args string) error {
			scope := ModeScope(c.Session)
			if gate == nil || c.Session == nil || scope == "" {
				c.send(Reply{Text: modeUnavailable})
				return nil
			}
			f := strings.Fields(args)
			switch {
			case len(f) == 0:
				sendModeMenu(ctx, c, gate, scope)
			case len(f) == 1 && f[0] == security.ModeReadOnly, len(f) == 1 && f[0] == security.ModeGuarded:
				if err := gate.SetMode(ctx, scope, f[0]); err != nil {
					return err
				}
				sendModeMenu(ctx, c, gate, scope)
			case len(f) == 1 && f[0] == security.ModeFull:
				switch {
				case c.interactive():
					c.send(Reply{Text: modeConfirmText, Edit: true, Buttons: [][]Button{
						{{Text: "❌ Cancel", Data: "/mode"}},
						{{Text: "⚡ Confirm full", Data: "/mode full confirm"}},
					}})
				case c.Confirm == nil:
					c.send(Reply{Text: modeConfirmText + "\nSend /mode full confirm to confirm."})
				case !c.Confirm(modeAskPrompt):
					sendModeMenu(ctx, c, gate, scope)
				default:
					if err := gate.SetMode(ctx, scope, security.ModeFull); err != nil {
						return err
					}
					sendModeMenu(ctx, c, gate, scope)
				}
			case len(f) == 2 && f[0] == security.ModeFull && f[1] == "confirm":
				if err := gate.SetMode(ctx, scope, security.ModeFull); err != nil {
					return err
				}
				sendModeMenu(ctx, c, gate, scope)
			default:
				c.send(Reply{Text: modeHelp})
			}
			return nil
		},
	}
}

// sendModeMenu sends the menu with the current mode of scope.
func sendModeMenu(ctx context.Context, c *CommandCtx, gate *security.Gate, scope string) {
	current := gate.ModeOf(ctx, scope)
	if current != security.ModeReadOnly && current != security.ModeFull {
		current = security.ModeGuarded
	}
	approvalsMode := ""
	if c.Deps != nil && c.Deps.Cfg != nil {
		approvalsMode = c.Deps.Cfg.Approvals.Mode
	}
	text := "🧭 **Mode of this topic: " + current + "**\n\n" +
		"**read-only**: the agent reads, searches, and answers. It cannot write files, run commands, or change memory, skills, or cron jobs.\n" +
		"**guarded**: " + guardedLine(approvalsMode) + "\n" +
		"**full**: no approval prompts in this topic. Hardline and human-only rules still apply."
	if !c.interactive() {
		c.send(Reply{Text: text + modeMenuTail})
		return
	}
	row := []Button{
		{Text: "👁 Read-only", Data: "/mode read-only"},
		{Text: "🛡 Guarded", Data: "/mode guarded"},
		{Text: "⚡ Full", Data: "/mode full"},
	}
	for i, m := range []string{security.ModeReadOnly, security.ModeGuarded, security.ModeFull} {
		if m == current {
			row[i].Text += " ✓"
		}
	}
	c.send(Reply{Text: text, Edit: true, Buttons: [][]Button{row}})
}

// guardedLine returns the text of the guarded mode for an approvals.mode value. An unknown
// value gives the manual line.
func guardedLine(approvalsMode string) string {
	switch approvalsMode {
	case "smart":
		return "the smart judge checks dangerous commands first, then asks you (approvals.mode: smart)."
	case "off":
		return "dangerous commands run with no question (approvals.mode: off). Hardline and human-only rules still apply."
	}
	return "the approval gate asks for dangerous commands (approvals.mode: manual)."
}
