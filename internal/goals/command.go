package goals

import (
	"strconv"
	"strings"
	"unicode"
)

// The verbs of a /goal command.
const (
	VerbStatus = "status"
	VerbShow   = "show"
	VerbSet    = "set"
	VerbDraft  = "draft"
	VerbPause  = "pause"
	VerbResume = "resume"
	VerbClear  = "clear"
	VerbWait   = "wait"
	VerbUnwait = "unwait"
	VerbGate   = "gate"
)

// Command is one parsed /goal command. The parser reads the text only, and
// the gateway applies the command to the goal.
type Command struct {
	Verb      string
	Text      string
	PID       int
	Reason    string
	GateOp    string // list, add, remove, clear, ok, or no
	GateArg   string
	GateIndex int
	Ignored   string // the extra words after a bare control verb
	Usage     string // a usage error text, or ""
}

// The usage texts of /goal and /subgoal. Hermes gives each text except the
// texts for a bad gate index.
const (
	usageEmptyGoal    = "Invalid goal: goal text is empty"
	usageDraft        = "Usage: /goal draft <objective in plain language>"
	usageWait         = "Usage: /goal wait <pid> [reason]"
	usageWaitPID      = "/goal wait: <pid> must be an integer process id."
	usageGate         = "Usage: /goal gate [list | add <command> | remove <N> | clear]"
	usageGateIndex    = "/goal gate remove: <N> must be an integer (1-based index)."
	usageSubgoalRm    = "Usage: /subgoal remove <n>"
	usageSubgoalIndex = "/subgoal remove: <n> must be an integer (1-based index)."
)

// exactVerbs maps a whole command text to its verb, like the Hermes
// _EXACT_HANDLERS table.
var exactVerbs = map[string]string{
	"":         VerbStatus,
	"status":   VerbStatus,
	"show":     VerbShow,
	"pause":    VerbPause,
	"resume":   VerbResume,
	"continue": VerbResume,
	"unpause":  VerbResume,
	"clear":    VerbClear,
	"stop":     VerbClear,
	"done":     VerbClear,
	"unwait":   VerbUnwait,
}

// bareControlVerbs keep their meaning when more words follow, so
// "/goal resume last goal" resumes and does not replace the goal text.
// clear, stop, and done are not in the set, because a wrong guess ends a goal
// that the user wants to keep.
var bareControlVerbs = map[string]bool{
	"resume": true, "continue": true, "unpause": true, "pause": true,
	"status": true, "show": true, "unwait": true,
}

// gateOps maps a gate op word to its op. rm is a short form of remove.
var gateOps = map[string]string{
	"add": "add", "remove": "remove", "rm": "remove", "clear": "clear", "ok": "ok", "no": "no",
}

// splitFirst splits s at the first run of white space, like the Python
// s.split(None, 1). It returns the trimmed rest.
func splitFirst(s string) (first, rest string) {
	s = strings.TrimSpace(s)
	i := strings.IndexFunc(s, unicode.IsSpace)
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i:])
}

// ParseCommand parses the text after /goal in the order of the Hermes
// dispatch_goal_command.
func ParseCommand(args string) Command {
	args = strings.TrimSpace(args)
	first, rest := splitFirst(args)
	verb := strings.ToLower(first)

	if verb == "--" {
		c := Command{Verb: VerbSet, Text: rest}
		if rest == "" {
			c.Usage = usageEmptyGoal
		}
		return c
	}
	if v, ok := exactVerbs[strings.ToLower(args)]; ok {
		return Command{Verb: v}
	}
	if bareControlVerbs[verb] {
		return Command{Verb: exactVerbs[verb], Ignored: rest}
	}
	switch verb {
	case VerbWait:
		return parseWait(rest)
	case VerbGate:
		return parseGate(rest)
	case VerbDraft:
		c := Command{Verb: VerbDraft, Text: rest}
		if rest == "" {
			c.Usage = usageDraft
		}
		return c
	}
	return Command{Verb: VerbSet, Text: args}
}

// parseWait parses the words after /goal wait. It does not check the pid
// value: the gateway refuses a pid that is not alive.
func parseWait(rest string) Command {
	if rest == "" {
		return Command{Verb: VerbWait, Usage: usageWait}
	}
	word, reason := splitFirst(rest)
	pid, err := strconv.Atoi(word)
	if err != nil {
		return Command{Verb: VerbWait, Usage: usageWaitPID}
	}
	return Command{Verb: VerbWait, PID: pid, Reason: reason}
}

// parseGate parses the words after /goal gate, like the Hermes _gate, with
// the ok and no ops of the gate confirm added.
func parseGate(rest string) Command {
	if rest == "" || strings.ToLower(rest) == "list" {
		return Command{Verb: VerbGate, GateOp: "list"}
	}
	word, arg := splitFirst(rest)
	op, ok := gateOps[strings.ToLower(word)]
	if !ok || (op == "clear" && arg != "") || (op != "clear" && arg == "") {
		return Command{Verb: VerbGate, Usage: usageGate}
	}
	switch op {
	case "add":
		return Command{Verb: VerbGate, GateOp: op, GateArg: arg}
	case "clear":
		return Command{Verb: VerbGate, GateOp: op}
	}
	n, err := strconv.Atoi(arg)
	if err != nil {
		if op == "remove" {
			return Command{Verb: VerbGate, Usage: usageGateIndex}
		}
		return Command{Verb: VerbGate, Usage: usageGate}
	}
	return Command{Verb: VerbGate, GateOp: op, GateIndex: n}
}

// IgnoredNote returns the Hermes note for the extra words after a bare
// control verb.
func IgnoredNote(rest string) string {
	return "(ignored " + pyRepr(rest) + ": a control command never sets goal text — " +
		"use /goal -- <text> when the goal really starts with a control word.)"
}

// pyRepr quotes s like the Python repr of a string, because the Hermes note
// uses {rest!r}. It uses double quotes when s has a single quote and no double
// quote, else single quotes.
func pyRepr(s string) string {
	quote := byte('\'')
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == rune(quote):
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// SubgoalCommand is one parsed /subgoal command. Op is add, list, remove, or
// clear.
type SubgoalCommand struct {
	Op    string
	Text  string
	Index int
	Usage string // a usage error text, or ""
}

// ParseSubgoal parses the text after /subgoal, like the Hermes
// _handle_subgoal_command. It does not check for a goal.
func ParseSubgoal(args string) SubgoalCommand {
	args = strings.TrimSpace(args)
	if args == "" {
		return SubgoalCommand{Op: "list"}
	}
	first, rest := splitFirst(args)
	switch strings.ToLower(first) {
	case "remove":
		if rest == "" {
			return SubgoalCommand{Op: "remove", Usage: usageSubgoalRm}
		}
		word, _ := splitFirst(rest)
		n, err := strconv.Atoi(word)
		if err != nil {
			return SubgoalCommand{Op: "remove", Usage: usageSubgoalIndex}
		}
		return SubgoalCommand{Op: "remove", Index: n}
	case "clear":
		return SubgoalCommand{Op: "clear"}
	}
	return SubgoalCommand{Op: "add", Text: args}
}
