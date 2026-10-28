package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/gateway"
)

// cmdHelp is the help of one command. It is the one source of the help text.
type cmdHelp struct {
	// Name is the command name, for example "chat".
	Name string
	// Group is the heading of the command in the main help.
	Group string
	// Summary is the one line of the command in the main help.
	Summary string
	// Usage is the usage line. It follows "usage: ".
	Usage string
	// Body is the text after the usage line, from the description to the examples.
	Body string
}

// chatCommandsMarker is the line in the chat help that chatCommandsHelp replaces.
const chatCommandsMarker = "{{chat-commands}}\n"

// commandHelps holds the help of each command, in the order of the main help. The hidden
// command gen-tools-doc is not in it.
var commandHelps = []cmdHelp{
	{
		Name:    "setup",
		Group:   "Start here",
		Summary: "Run the setup wizard: provider, Telegram, host programs, systemd.",
		Usage:   "tether setup [flags]",
		Body: `Run the interactive setup in five steps:
  1. The provider.
  2. An optional fallback provider.
  3. An optional Telegram bot, with the owner pairing.
  4. The host programs.
  5. The systemd unit.

A second run shows the current values as the defaults. Before the setup changes
config.yaml or .env, it saves a .bak copy.

Flags:
  --data-dir DIR       The data folder. The default is TETHER_HOME, then
                       ~/.tether.
  --system             Write a system unit to /etc/systemd/system, not a user
                       unit. This needs root.

Examples:
  tether setup
  sudo tether setup --system
`,
	},
	{
		Name:    "doctor",
		Group:   "Start here",
		Summary: "Check the install. Each line shows ✓, ⚠, or ✗ and the next action.",
		Usage:   "tether doctor [flags]",
		Body: `Check the install. The checks are the config, .env, the providers, Telegram,
state.db, the host programs, the MCP servers, the free disk space, and the
newest release. Each line shows ✓, ⚠, or ✗ and the next action. The exit code
is 1 when a check fails. A running gateway does not stop the checks.

Flags:
  --data-dir DIR       The data folder. The default is TETHER_HOME, then
                       ~/.tether.

Examples:
  tether doctor
`,
	},
	{
		Name:    "chat",
		Group:   "Run the agent",
		Summary: "Start the interactive chat in this terminal.",
		Usage:   "tether chat [flags]",
		Body: `Start the interactive chat in this terminal. Type /exit or press Ctrl-D to stop.
Ctrl-C stops the running turn. At the prompt, Ctrl-C ends the chat. A chat can
run next to the gateway.

Flags:
  --data-dir DIR       The data folder. The default is TETHER_HOME, then
                       ~/.tether.
  --log-stderr         Write the logs to stderr instead of logs/tether.log.
  --log-level LEVEL    debug, info, warn, or error. The default is log.level.

Commands in the chat:
{{chat-commands}}

Examples:
  tether chat
  tether chat --log-stderr --log-level debug
`,
	},
	{
		Name:    "gateway",
		Group:   "Run the agent",
		Summary: "Run the Telegram channel and the cron scheduler.",
		Usage:   "tether gateway [flags]",
		Body: `Run the Telegram channel and the cron scheduler until SIGINT or SIGTERM. Only
one gateway can run for a data folder. A chat can run next to it.

Flags:
  --data-dir DIR       The data folder. The default is TETHER_HOME, then
                       ~/.tether.
  --log-stderr         Write the logs to stderr instead of logs/tether.log.
                       Use it under systemd.
  --log-level LEVEL    debug, info, warn, or error. The default is log.level.

Environment:
  TETHER_PPROF         A loopback address, for example 127.0.0.1:6060. The
                       gateway serves net/http/pprof on it.

Run it as a systemd user service (Linux):
  systemctl --user daemon-reload
  systemctl --user enable --now tether
  loginctl enable-linger $USER

Examples:
  tether gateway
  tether gateway --log-stderr --log-level debug
`,
	},
	{
		Name:    "sessions",
		Group:   "Manage data",
		Summary: "List the sessions, or export one as JSONL.",
		Usage:   "tether sessions <command> [flags]",
		Body: `Show the stored sessions, or export one. The command opens only state.db.

Commands:
  list                 Show the 50 newest sessions.
  export <id>          Write the messages of a session as JSONL.

Flags:
  --data-dir DIR       The data folder. The default is TETHER_HOME, then
                       ~/.tether.
  --out FILE           export only: the output file. The default is
                       <data dir>/exports/<id>.jsonl.

Examples:
  tether sessions list
  tether sessions export 0199e2f6a1b2c4f9a1d --out chat.jsonl
`,
	},
	{
		Name:    "skills",
		Group:   "Manage data",
		Summary: "List the skills, or restore a bootstrap skill.",
		Usage:   "tether skills <command> [flags]",
		Body: `Show the skills, or restore the bootstrap skills that come in the binary.

Commands:
  list                 Show each skill with its source: local, external, or
                       builtin.
  reset [name]         Restore a bootstrap skill from the binary. With no name,
                       restore all of them.

Flags:
  --data-dir DIR       The data folder. The default is TETHER_HOME, then
                       ~/.tether.

Examples:
  tether skills list
  tether skills reset tether-memory
`,
	},
	{
		Name:    "pairing",
		Group:   "Manage data",
		Summary: "Approve or revoke the Telegram users.",
		Usage:   "tether pairing <command> [flags]",
		Body: `Control who can talk to the bot. A new user sends a message to the bot and gets
a pairing code. You approve the code here. The default platform is telegram.

Commands:
  list [platform]              Show the pending requests and the approved users.
  approve [platform] <code>    Approve a pairing code.
  revoke [platform] <user id>  Remove the access of a user.

Flags:
  --data-dir DIR       The data folder. The default is TETHER_HOME, then
                       ~/.tether.

Examples:
  tether pairing list
  tether pairing approve K7Q2MXRP
  tether pairing revoke 123456789
`,
	},
	{
		Name:    "usage",
		Group:   "Manage data",
		Summary: "Show the calls, the tokens, and the cost of each model.",
		Usage:   "tether usage [today|7d|30d] [flags]",
		Body: `Show the calls, the tokens, the speed, and the cost of each model. The default
range is today. The cost comes from the stored price of each call.

Flags:
  --data-dir DIR       The data folder. The default is TETHER_HOME, then
                       ~/.tether.

Examples:
  tether usage
  tether usage 7d
`,
	},
	{
		Name:    "update",
		Group:   "Maintain",
		Summary: "Replace this binary with the newest release from GitHub.",
		Usage:   "tether update [flags]",
		Body: `Replace this binary with the newest release from GitHub. The command downloads
the archive for this platform, checks its SHA-256 against checksums.txt, runs
the new binary once, and then replaces the old file in one step. A running
gateway keeps the old version until you restart it.

Flags:
  --check              Report a newer release. Do not download.
  --tag vX.Y.Z         Install this release instead of the newest one.
  --force              Install also when the version is the same or a dev build.
  --data-dir DIR       The data folder. Used only to find a running gateway.

Examples:
  tether update --check
  tether update
  tether update --tag v0.3.0 --force
  sudo tether update            # when the binary is in /usr/local/bin
`,
	},
	{
		Name:    "version",
		Group:   "Maintain",
		Summary: "Print the version.",
		Usage:   "tether version",
		Body: `Print the version of this binary.

Examples:
  tether version
`,
	},
	{
		Name:    "help",
		Group:   "Maintain",
		Summary: "Print this help, or the help of one command.",
		Usage:   "tether help [command]",
		Body: `Print the main help, or the help of one command.

Examples:
  tether help
  tether help update
`,
	},
}

// findHelp returns the help of the command name.
func findHelp(name string) (cmdHelp, bool) {
	for _, h := range commandHelps {
		if h.Name == name {
			return h, true
		}
	}
	return cmdHelp{}, false
}

// mainHelpHead is the start of the main help, before the command groups.
const mainHelpHead = `Tether Agent: a personal AI agent

Usage:
  tether <command> [flags]

`

// mainHelpTail is the end of the main help, after the command groups. The %s is the data
// folder.
const mainHelpTail = `Flags for most commands:
  --data-dir DIR    The data folder. The default is TETHER_HOME, then ~/.tether.

Data folder (%s):
  config.yaml       The settings. See docs/CONFIG.md.
  .env              The API keys and the bot token.
  state.db          The sessions, the messages, and the usage.
  logs/tether.log   The log of chat and gateway.

Environment:
  TETHER_HOME       The data folder, when --data-dir is not set.
  TETHER_PPROF      A loopback address for net/http/pprof in the gateway.

Run "tether help <command>" or "tether <command> --help" for the flags
of a command.
Docs: https://github.com/dnwandana/tether-agent
`

// mainHelp returns the main help. dataDir is the resolved data folder.
func mainHelp(dataDir string) string {
	var b strings.Builder
	b.WriteString(mainHelpHead)
	group := ""
	for _, h := range commandHelps {
		if h.Group != group {
			if group != "" {
				b.WriteString("\n")
			}
			group = h.Group
			b.WriteString(group)
			b.WriteString(":\n")
		}
		fmt.Fprintf(&b, "  %-12s%s\n", h.Name, h.Summary)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, mainHelpTail, dataDir)
	return b.String()
}

// commandHelp returns the help of the command name. The chat help gets the commands of
// gateway.Commands() in place of chatCommandsMarker.
func commandHelp(name string) string {
	h, ok := findHelp(name)
	if !ok {
		return ""
	}
	body := h.Body
	// Only the chat help has the marker. Build the chat lines only for it.
	if before, after, ok := strings.Cut(body, chatCommandsMarker); ok {
		body = before + chatCommandsHelp(gateway.Commands()) + after
	}
	return "usage: " + h.Usage + "\n\n" + body
}

// usageError prints the three bad-usage lines of the command name and returns 2.
func usageError(stderr io.Writer, name, reason string) int {
	usage := ""
	if h, ok := findHelp(name); ok {
		usage = h.Usage
	}
	fmt.Fprintf(stderr, "%s: %s\nusage: %s\nRun \"tether %s help\" for the details.\n", name, reason, usage, name)
	return 2
}

// parseCommand parses the flags and the positional arguments of the command fl.Name(),
// in any order. When ok is false, the caller returns code at once. A request for help
// prints the command help to stdout with code 0. A bad flag prints the bad-usage lines to
// stderr with code 2.
func parseCommand(fl *flag.FlagSet, args []string, stdout, stderr io.Writer) (rest []string, code int, ok bool) {
	// The flag package must not print its own error text or its own usage.
	fl.SetOutput(io.Discard)
	fl.Usage = func() {}
	rest, err := parseInterleaved(fl, args)
	if err == nil {
		return rest, 0, true
	}
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, commandHelp(fl.Name()))
		return nil, 0, false
	}
	return nil, usageError(stderr, fl.Name(), flagReason(err)), false
}

// flagReason turns a parse error of the flag package into a bad-usage reason.
func flagReason(err error) string {
	msg := err.Error()
	if name, ok := strings.CutPrefix(msg, "flag provided but not defined: -"); ok {
		return "unknown flag --" + name
	}
	if name, ok := strings.CutPrefix(msg, "flag needs an argument: -"); ok {
		return "flag --" + name + " needs a value"
	}
	return msg
}

// The layout of the lines in "Commands in the chat".
const (
	chatHelpWidth  = 80 // the maximum line width, in runes
	chatHelpLeft   = 18 // the width of the left column
	chatHelpIndent = 20 // the indent of a wrapped line
)

// chatCommandsHelp returns the lines of the section "Commands in the chat". The lines come
// from the command table, so the help and the chat cannot disagree. /exit and /quit are not
// in the table, because the CLI handles them itself.
func chatCommandsHelp(cmds map[string]gateway.Command) string {
	names := make([]string, 0, len(cmds))
	for n := range cmds {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		help := cmds[name].Help
		if i := strings.Index(help, ". "); i >= 0 {
			help = help[:i]
		}
		left, right := "/"+name, help
		marker := ": /" + name
		if i := strings.Index(help, marker); i >= 0 {
			args := strings.TrimSpace(help[i+len(marker):])
			left, right = "/"+name+" "+args, help[:i]
			if utf8.RuneCountInString(left) > chatHelpLeft-2 {
				left, right = "/"+name+" ...", help[:i]+": "+args
			}
		}
		writeColumns(&b, left, right)
	}
	writeColumns(&b, "/exit, /quit", "End the chat")
	return b.String()
}

// writeColumns writes one line of the chat commands. It wraps the right column at
// chatHelpWidth.
func writeColumns(b *strings.Builder, left, right string) {
	width := max(chatHelpLeft, utf8.RuneCountInString(left)+2)
	line := "  " + left + strings.Repeat(" ", width-utf8.RuneCountInString(left))
	start := true
	for _, word := range strings.Fields(right) {
		switch {
		case start:
			line += word
			start = false
		case utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) > chatHelpWidth:
			b.WriteString(line)
			b.WriteByte('\n')
			line = strings.Repeat(" ", chatHelpIndent) + word
		default:
			line += " " + word
		}
	}
	b.WriteString(strings.TrimRight(line, " "))
	b.WriteByte('\n')
}

// suggestCommand returns the command name that is nearest to input, at a Levenshtein
// distance of 2 or less. On a tie, the first name in alphabetical order wins. It returns ""
// when no name is close.
func suggestCommand(input string) string {
	names := make([]string, 0, len(commandHelps))
	for _, h := range commandHelps {
		names = append(names, h.Name)
	}
	sort.Strings(names)
	best, bestDist := "", 3
	for _, n := range names {
		if d := editDistance(input, n); d < bestDist {
			best, bestDist = n, d
		}
	}
	return best
}

// editDistance returns the Levenshtein distance between a and b, counted in runes.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// helpPointer is the last line of the output for an unknown command.
const helpPointer = `Run "tether help" for the commands.`

// isHelpWord reports whether arg asks for help.
func isHelpWord(arg string) bool {
	switch arg {
	case "help", "-h", "-help", "--help":
		return true
	}
	return false
}

// runHelp prints the main help, or the help of the one command in args, and returns the
// exit code.
func runHelp(args []string, stdout, stderr io.Writer) int {
	switch {
	case len(args) == 0:
		fmt.Fprint(stdout, mainHelp(dataDir("")))
		return 0
	case len(args) > 1:
		return usageError(stderr, "help", "unexpected argument: "+args[1])
	}
	if _, ok := findHelp(args[0]); !ok {
		fmt.Fprintf(stderr, "help: unknown command: %s\n%s\n", args[0], helpPointer)
		return 2
	}
	fmt.Fprint(stdout, commandHelp(args[0]))
	return 0
}
