package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// RestartState is the answer of a RestartRequest.
type RestartState struct {
	// Busy is the number of turns that still run.
	Busy int
	// CodingTasks holds the agent ids of the running acp_delegate calls, one id for each call.
	CodingTasks []string
	// Already is true when a stop is in progress. The request then starts no new one.
	Already bool
}

// RestartRequest asks the gateway to restart. It returns the state at once. When Already
// is false, start begins the stop. The command calls start after it sends the reply. start
// is nil when Already is true.
type RestartRequest func(ctx context.Context, key string, to Target) (st RestartState, start func())

// RestartCommand returns the /restart command. The app registers it in the gateway role
// only. drain is the time that the gateway waits for the running turns.
//
// Hermes keeps a dedup file here so that a redelivered /restart does not restart again.
// Tether Agent does not need it, because the poller saves the update offset first.
func RestartCommand(request RestartRequest, drain time.Duration) Command {
	return Command{
		Name:      "restart",
		Help:      "Restart the gateway after the running turns end",
		WhileBusy: true,
		Run: func(ctx context.Context, c *CommandCtx, _ string) error {
			var to Target
			if s := c.Session; s != nil {
				to = Target{Source: s.Source, ChatID: s.ChatID, ThreadID: s.ThreadID}
			}
			st, start := request(ctx, c.Key, to)
			c.send(Reply{Text: restartReply(st, drain)})
			if start != nil && !st.Already {
				start()
			}
			return nil
		},
	}
}

// restartReply returns the reply to /restart for the state st.
func restartReply(st RestartState, drain time.Duration) string {
	switch {
	case st.Already:
		return "⏳ **Restart in progress**\nThe gateway already stops. Wait for it."
	case st.Busy > 0 && drain <= 0:
		return fmt.Sprintf("⏳ **Restarting**\n%d turn(s) still run. The gateway stops them now and restarts.", st.Busy)
	case st.Busy > 0:
		var b strings.Builder
		fmt.Fprintf(&b, "⏳ **Restarting**\n%d turn(s) still run. The gateway waits for them, for up to %s, and then restarts.",
			st.Busy, FormatDrain(drain))
		// A coding task often runs longer than the drain, so tell the user that it can stop.
		for _, id := range st.CodingTasks {
			fmt.Fprintf(&b, "\nA coding task of %s runs. It stops if it does not end in %s.",
				EscapeMarkdown(id), FormatDrain(drain))
		}
		return b.String()
	}
	return "♻ **Restarting**\nThe gateway restarts now."
}
