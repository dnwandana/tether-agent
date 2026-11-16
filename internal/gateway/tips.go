package gateway

import (
	"fmt"
	"math/rand/v2"
)

// Tips holds the tips of the /new reply, like Hermes hermes_cli/tips.py. On Telegram, a
// command name in a tip becomes a link, so a tip writes a slash command in plain text. A
// command for the terminal keeps the code style, because Telegram cannot run it.
var Tips = []string{
	"/fork copies this session, so you can try a different idea and keep the original.",
	"/steer sends a note to the agent while it works. You do not have to wait for the answer.",
	"/sessions bali finds the sessions with \"bali\" in the title.",
	"/model changes the model of this session only. Other sessions keep the default.",
	"/prompt set replaces the base prompt in this session only.",
	"/compact makes a long session shorter. The agent keeps a summary of the old messages.",
	"/usage 7d shows the tokens and the cost of the last 7 days.",
	"/rename gives this session a title, so you can find it later in /sessions.",
	"/archive hides a session from the list. /sessions archived shows the hidden sessions again.",
	"/stop stops the current task. The session stays, and you can continue it.",
	"Ask \"remind me every morning at 8 to check my email\" to make a cron job.",
	"/sethome sends the cron results to this chat.",
	"Send a voice note. The agent transcribes it and answers.",
	"Send a photo. The agent can look at it and answer questions about it.",
	"Ask \"what do you remember about me?\" to see what the agent keeps in its memory.",
	"The agent can search your old sessions. Ask \"what did we decide about the trip?\".",
	"Ask the agent to \"save this as a skill\". It can use the skill again later.",
	"Send /usage all to see the tokens, the cache use, and the cost of all chats since the start.",
	"Run `tether doctor` in a terminal to check your setup.",
	"Run `tether update` in a terminal to install the newest release.",
	"/goal gives the agent a task that can need many turns. The agent works on it until the goal is done.",
	"/goal draft asks the agent to write the goal and its checks. You give only the idea.",
	"Every skill is a slash command. Send /help skills to see them all.",
	"/effort sets the reasoning effort of this session. Lower levels think less and cost less.",
	"/now stops the current task and runs your waiting message at once.",
	"/approvals shows the saved \"Always\" answers of the approval gate. You can remove them there.",
	"Ask \"run disk.sh every 30 minutes, with no agent\" to make a script job. It costs no tokens and writes only when there is output.",
	"The agent can make an image or a voice clip. Ask \"make an image of a cat\".",
	"/delete removes a session with all its messages. This cannot be undone.",
	"/todo shows the task list of this session.",
	"/restart stops the gateway and starts it again when the running tasks end.",
	"/reload-mcp loads the MCP servers and their tools again.",
	"/usage session shows the tokens and the cost of this session only.",
	"The agent can drive a real browser. Ask \"open the page and click the button\".",
	"The agent can hand a coding task to Claude Code or Codex. It asks before each dangerous step.",
	"Give the agent a big job. It can start helper agents that work in parallel.",
	"Ask a cron job to \"stay silent when nothing changed\". A silent run sends no message.",
	"/goal gate add \"go test ./...\" makes a check that must pass before the goal can complete.",
	"Send a sticker. The agent tells you what it shows.",
	"Run `tether backup` in a terminal to make a copy of the data dir while the gateway runs.",
}

// pickTip returns one random tip. Tests replace it to get a fixed reply.
var pickTip = func() string { return Tips[rand.IntN(len(Tips))] }

// formatContext returns a context window as text, for example "200K tokens" or
// "1.0M tokens", like Hermes _format_session_info.
func formatContext(tokens int) string {
	switch {
	case tokens >= 1_000_000:
		return fmt.Sprintf("%.1fM tokens", float64(tokens)/1_000_000)
	case tokens >= 1000:
		return fmt.Sprintf("%dK tokens", tokens/1000)
	default:
		return fmt.Sprintf("%d tokens", tokens)
	}
}
