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
	"Run `tether usage` in a terminal to see the cost of all chats.",
	"Run `tether doctor` in a terminal to check your setup.",
	"Run `tether update` in a terminal to install the newest release.",
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
