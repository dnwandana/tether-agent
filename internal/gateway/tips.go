package gateway

import (
	"fmt"
	"math/rand/v2"
)

// Tip is one tip of the /new reply. Channel is "telegram" or "cli" for a tip that names a
// command of one channel only, or "" for a tip of all channels.
type Tip struct {
	Text    string
	Channel string
}

// Tips holds the tips of the /new reply, like Hermes hermes_cli/tips.py. On Telegram, a
// command name in a tip becomes a link, so a tip writes a slash command in plain text. A
// command for the terminal keeps the code style, because Telegram cannot run it. Telegram
// does not allow "-" in a command link, so a command with "-" has a tip for each channel.
var Tips = []Tip{
	{"/fork copies this session, so you can try a different idea and keep the original.", ""},
	{"/steer sends a note to the agent while it works. You do not have to wait for the answer.", ""},
	{"/sessions bali finds the sessions with \"bali\" in the title.", ""},
	{"/model changes the model of this session only. Other sessions keep the default.", ""},
	{"/prompt set replaces the base prompt in this session only.", ""},
	{"/compact makes a long session shorter. The agent keeps a summary of the old messages.", ""},
	{"/usage 7d shows the tokens and the cost of the last 7 days.", ""},
	{"/rename gives this session a title, so you can find it later in /sessions.", ""},
	{"/archive hides a session from the list. /sessions archived shows the hidden sessions again.", ""},
	{"/stop stops the current task. The session stays, and you can continue it.", ""},
	{"Ask \"remind me every morning at 8 to check my email\" to make a cron job.", ""},
	{"/sethome sends the cron results to this chat.", "telegram"},
	{"Send a voice note. The agent transcribes it and answers.", "telegram"},
	{"Send a photo. The agent can look at it and answer questions about it.", "telegram"},
	{"Ask \"what do you remember about me?\" to see what the agent keeps in its memory.", ""},
	{"The agent can search your old sessions. Ask \"what did we decide about the trip?\".", ""},
	{"Ask the agent to \"save this as a skill\". It can use the skill again later.", ""},
	{"Send /usage all to see the tokens, the cache use, and the cost of all chats since the start.", ""},
	{"Run `tether doctor` in a terminal to check your setup.", ""},
	{"Run `tether update` in a terminal to install the newest release.", ""},
	{"/goal gives the agent a task that can need many turns. The agent works on it until the goal is done.", ""},
	{"/goal draft asks the agent to write the goal and its checks. You give only the idea.", ""},
	{"Every skill is a slash command. Send /help skills to see them all.", ""},
	{"/effort sets the reasoning effort of this session. Lower levels think less and cost less.", ""},
	{"/now stops the current task and runs your waiting message at once.", ""},
	{"/approvals shows the saved \"Always\" answers of the approval gate. You can remove them there.", ""},
	{"Ask \"run disk.sh every 30 minutes, with no agent\" to make a script job. It costs no tokens and sends a message only when the script prints output or fails.", ""},
	{"The agent can make an image or a voice clip. Ask \"make an image of a cat\".", ""},
	{"/delete removes a session with all its messages. This cannot be undone.", ""},
	{"/todo shows the task list of this session.", ""},
	{"/restart restarts the gateway. It waits a short time for the running tasks to end.", "telegram"},
	{"/reload_mcp loads the MCP servers and their tools again.", "telegram"},
	{"/reload-mcp loads the MCP servers and their tools again.", "cli"},
	{"/usage session shows the tokens and the cost of this session only.", ""},
	{"The agent can drive a real browser. Ask \"open the page and click the button\".", ""},
	{"The agent can hand a coding task to Claude Code or Codex. It asks before each dangerous step.", ""},
	{"Give the agent a big job. It can start helper agents that work in parallel.", ""},
	{"Ask a cron job to \"stay silent when nothing changed\". A silent run sends no message.", ""},
	{"/goal gate add \"go test ./...\" makes a check that must pass before the goal can complete.", ""},
	{"Send a sticker. The agent tells you what it shows.", "telegram"},
	{"Run `tether backup` in a terminal to make a copy of the data dir while the gateway runs.", ""},
}

// pickTip returns one random tip for channel, "telegram" or "cli". Tests replace it to get
// a fixed reply.
var pickTip = func(channel string) string {
	tips := tipsFor(channel)
	return tips[rand.IntN(len(tips))]
}

// tipsFor returns the texts of the tips that channel can show.
func tipsFor(channel string) []string {
	var out []string
	for _, t := range Tips {
		if t.Channel == "" || t.Channel == channel {
			out = append(out, t.Text)
		}
	}
	return out
}

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
