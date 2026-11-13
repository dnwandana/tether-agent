package gateway

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"

	"github.com/dnwandana/tether-agent/internal/security"
)

const (
	approvalsHelp    = "See and remove the Always answers: /approvals [remove <number> <code>]"
	approvalsEmpty   = "No Always answers. The gate asks for each dangerous command."
	approvalsCLILine = "Send /approvals remove <number> <code> to remove an answer."
	approvalsStale   = "The list changed. Send /approvals to see it again."

	// approvalsMaxButtons is the number of Remove buttons that a list reply shows.
	approvalsMaxButtons = 20
	// approvalsButtonsPerRow is the number of Remove buttons in one button row.
	approvalsButtonsPerRow = 4
)

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

// approvalCode returns the first 8 hex characters of the SHA-256 hash of the rule ID. The
// code shows that a remove action belongs to the row that the user saw.
func approvalCode(patternID string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(patternID)))[:8]
}

// approvalDescription returns the escaped description of a rule. An unknown rule shows its
// ID.
func approvalDescription(patternID string) string {
	if p, ok := security.PatternByID(patternID); ok && p.Description != "" {
		return EscapeMarkdown(p.Description)
	}
	return EscapeMarkdown(patternID)
}

// approvalsList sends the list of the "Always" answers.
func approvalsList(ctx context.Context, c *CommandCtx, gate *security.Gate) error {
	rows, err := approvalsRows(ctx, gate)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		c.send(Reply{Text: approvalsEmpty})
		return nil
	}
	now := nowFunc()
	lines := []string{"🛡 **Always answers**"}
	for i, row := range rows {
		lines = append(lines, fmt.Sprintf("%d. %s · %s · `%s`", i+1,
			approvalDescription(row.PatternID), formatDate(row.CreatedAt, now), approvalCode(row.PatternID)))
	}
	text := strings.Join(lines, "\n")
	if !c.interactive() {
		c.send(Reply{Text: text + "\n\n" + approvalsCLILine})
		return nil
	}
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
			Data: fmt.Sprintf("/approvals remove %d %s", i+1, approvalCode(row.PatternID)),
		})
	}
	c.send(Reply{Text: text, Buttons: buttons})
	return nil
}

// approvalsRemove removes the answer that the number and the code name. It removes
// nothing when the list changed.
func approvalsRemove(ctx context.Context, c *CommandCtx, gate *security.Gate, words []string) error {
	if len(words) != 2 {
		c.send(Reply{Text: approvalsStale})
		return nil
	}
	rows, err := approvalsRows(ctx, gate)
	if err != nil {
		return err
	}
	n, convErr := strconv.Atoi(words[0])
	if convErr != nil || n < 1 || n > len(rows) || approvalCode(rows[n-1].PatternID) != words[1] {
		c.send(Reply{Text: approvalsStale})
		return nil
	}
	row := rows[n-1]
	if err := gate.RemoveAlways(ctx, row.PatternID); err != nil {
		return err
	}
	c.send(Reply{Text: fmt.Sprintf("✅ Removed the Always answer for %s. The gate asks again.",
		approvalDescription(row.PatternID))})
	return nil
}
