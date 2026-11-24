package goals

import (
	"errors"
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
	GateHash  string // the <hash8> of a gate remove, or ""
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
	usageGate         = "Usage: /goal gate [list | add <command> | remove <N> [<hash8>] | clear]"
	usageGateIndex    = "/goal gate remove: <N> must be an integer (1-based index)."
	usageGateHash     = "/goal gate remove: <hash8> must be 8 lowercase hex characters."
	usageSubgoalRm    = "Usage: /subgoal remove <n> [<hash8>]"
	usageSubgoalIndex = "/subgoal remove: <n> must be an integer (1-based index)."
	usageSubgoalHash  = "/subgoal remove: <hash8> must be 8 lowercase hex characters."
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

// ErrSessionFlag is the usage error of a --session flag with no valid session id.
//
//lint:ignore ST1005 The reply shows this text to the user as it is.
var ErrSessionFlag = errors.New("Usage: --session <id>. The id is a session id with 19 hex characters.")

// sessionFlagForm reports whether words (the words before a --session flag) are a verb
// that takes the flag.
func sessionFlagForm(words []string) bool {
	lower := make([]string, len(words))
	for i, w := range words {
		lower[i] = strings.ToLower(w)
	}
	isNum := func(w string) bool {
		if w == "" {
			return false
		}
		for _, r := range w {
			if r < '0' || r > '9' {
				return false
			}
		}
		return true
	}
	removeTail := func(w []string) bool {
		return (len(w) == 2 || len(w) == 3) && isNum(w[1]) && (w[0] == "remove" || w[0] == "rm")
	}
	switch {
	case len(lower) == 1 && (lower[0] == "pause" || lower[0] == "clear"):
		return true
	case len(lower) == 2 && lower[0] == "gate" && lower[1] == "clear":
		return true
	case len(lower) >= 3 && lower[0] == "gate":
		return removeTail(lower[1:])
	case len(lower) >= 2:
		return removeTail(lower)
	}
	return false
}

func isSessionID(s string) bool {
	if len(s) != 19 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

// StripSessionFlag takes the --session flag out of the args of /goal or /subgoal. It reads
// the flag only at the end of a pause, a clear, or a remove, never in a goal text and
// never after --.
func StripSessionFlag(args string) (rest string, sessionID string, err error) {
	words := strings.Fields(args)
	if len(words) == 0 || words[0] == "--" {
		return args, "", nil
	}
	at := -1
	for i, w := range words {
		if w == "--session" {
			at = i
			break
		}
	}
	if at < 0 || !sessionFlagForm(words[:at]) {
		return args, "", nil
	}
	if len(words) != at+2 || !isSessionID(words[at+1]) {
		return args, "", ErrSessionFlag
	}
	return strings.Join(words[:at], " "), words[at+1], nil
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
	hash := ""
	if op == "remove" {
		var extra string
		arg, extra = splitFirst(arg)
		if extra != "" {
			if !isHash8(extra) {
				return Command{Verb: VerbGate, Usage: usageGateHash}
			}
			hash = extra
		}
	}
	n, err := strconv.Atoi(arg)
	if err != nil {
		if op == "remove" {
			return Command{Verb: VerbGate, Usage: usageGateIndex}
		}
		return Command{Verb: VerbGate, Usage: usageGate}
	}
	return Command{Verb: VerbGate, GateOp: op, GateIndex: n, GateHash: hash}
}

// isHash8 reports whether s is exactly 8 lowercase hex characters.
func isHash8(s string) bool {
	if len(s) != 8 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
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
	Hash  string // the <hash8> of a remove, or ""
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
		word, more := splitFirst(rest)
		n, err := strconv.Atoi(word)
		if err != nil {
			return SubgoalCommand{Op: "remove", Usage: usageSubgoalIndex}
		}
		// A word after <n> must be the hash of the item. A bad hash never removes by index
		// alone, because the hash guards the remove against a changed list.
		hash, _ := splitFirst(more)
		if hash != "" && !isHash8(hash) {
			return SubgoalCommand{Op: "remove", Usage: usageSubgoalHash}
		}
		return SubgoalCommand{Op: "remove", Index: n, Hash: hash}
	case "clear":
		return SubgoalCommand{Op: "clear"}
	}
	return SubgoalCommand{Op: "add", Text: args}
}
