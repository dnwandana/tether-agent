package acp

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dnwandana/tether-agent/internal/security"
)

// compoundMarks hide what a command runs or what it writes, so such a command asks each
// time. "&" also covers "&&", "|" covers "||", and "<" and ">" cover redirects and the
// process substitutions "<(" and ">(".
var compoundMarks = []string{"&", "|", ";", "$(", "`", "<", ">", "\n", "\r"}

// wrapperPrograms run another program. Their name does not say what runs.
var wrapperPrograms = map[string]bool{
	"sudo": true, "env": true, "sh": true, "bash": true, "zsh": true,
	"xargs": true, "nohup": true, "timeout": true, "nice": true,
	"exec": true, "eval": true, "command": true, "builtin": true, "time": true,
	"setsid": true, "doas": true, "su": true, "runuser": true, "stdbuf": true,
	"ionice": true, "chroot": true, "unshare": true, "nsenter": true, "flock": true,
	"dash": true, "ksh": true, "fish": true, "csh": true, "tcsh": true, "busybox": true,
	"pkexec": true, "watch": true, "script": true, "strace": true, "taskset": true, "chrt": true,
}

// assignRe matches a leading NAME=value word of a command.
var assignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// plainProgramRe matches a program name with no shell syntax. A quoted, escaped, or
// expanded name, for example "sudo" or $SHELL, does not tell what runs.
var plainProgramRe = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)

// patternID returns the approval pattern id of a permission request. The id has the ACP
// session id, so a new ACP session starts with no "this session" grants. For an execute
// request, the id ends with the base name of the program. noSession is true when the id
// does not tell exactly what runs, so the user must answer each time. This is the case for
// a compound command, a wrapper program, a program word with a path, and a command that
// starts with NAME=value words. A path can point to any file, so "./ls" is not always ls.
// A NAME=value word such as LD_PRELOAD or PATH can change what the program runs.
func patternID(agentID, acpSessionID, kind, command string) (id string, noSession bool) {
	if kind == "" {
		kind = "other"
	}
	id = "acp:" + agentID + ":" + acpSessionID + ":" + kind
	switch kind {
	case "switch_mode":
		return id, true
	case "execute":
	default:
		return id, false
	}
	for _, m := range compoundMarks {
		if strings.Contains(command, m) {
			noSession = true
			break
		}
	}
	program, indirect := commandProgram(command)
	if program == "" {
		return id, true
	}
	if indirect || wrapperPrograms[program] || !plainProgramRe.MatchString(program) {
		noSession = true
	}
	return id + ":" + program, noSession
}

// commandProgram returns the base name of the program of a command. It skips the leading
// NAME=value words. A compound mark ends the program word, so "a; b" gives "a". It
// returns "" when the command has no program word. indirect is true when NAME=value words
// come before the program word, or when the program word has a "/" before the base name
// is taken.
func commandProgram(command string) (program string, indirect bool) {
	for w := range strings.FieldsSeq(command) {
		if assignRe.MatchString(w) {
			indirect = true
			continue
		}
		for _, m := range compoundMarks {
			if i := strings.Index(w, m); i >= 0 {
				w = w[:i]
			}
		}
		if w == "" {
			return "", indirect
		}
		if strings.Contains(w, "/") {
			indirect = true
		}
		return filepath.Base(w), indirect
	}
	return "", indirect
}

// buildApproval maps a permission request of the agent to an approval request. allowID and
// rejectID are the ids of the first allow_once and the first reject_once options. ok is
// false when one of them is missing. An allow_always option is never selected, because
// Tether Agent keeps the grants itself.
func buildApproval(agentID, acpSessionID string, in permissionInput) (req security.ApprovalRequest, allowID, rejectID string, ok bool) {
	for _, o := range in.Options {
		switch o.Kind {
		case "allow_once":
			if allowID == "" {
				allowID = o.ID
			}
		case "reject_once":
			if rejectID == "" {
				rejectID = o.ID
			}
		}
	}
	if allowID == "" || rejectID == "" {
		return security.ApprovalRequest{}, "", "", false
	}
	id, noSession := patternID(agentID, acpSessionID, in.Kind, in.Command)
	req = security.ApprovalRequest{
		Summary:     security.CleanSummary(in.Title),
		Description: agentID + " wants to " + verbFor(in.Kind),
		Command:     bodyFor(in),
		PatternID:   id,
		NoAlways:    true,
		NoSession:   noSession,
	}
	return req, allowID, rejectID, true
}

// verbFor returns the verb of the "Flagged:" line for an ACP tool kind.
func verbFor(kind string) string {
	switch kind {
	case "execute":
		return "run a command"
	case "edit":
		return "edit a file"
	case "delete":
		return "delete a file"
	case "move":
		return "move a file"
	case "fetch":
		return "fetch a URL"
	case "read", "search":
		return "read files"
	case "switch_mode":
		return "leave plan mode and start to code"
	default:
		return "use a tool"
	}
}

// bodyFor returns the text of the code block of the approval message.
func bodyFor(in permissionInput) string {
	switch in.Kind {
	case "execute":
		if in.Command != "" {
			return in.Command
		}
	case "edit":
		if len(in.Diffs) > 0 {
			return editBody(in.Diffs)
		}
	case "delete", "move", "read", "search":
		if len(in.Locations) > 0 {
			return strings.Join(in.Locations, "\n")
		}
	case "switch_mode":
		if in.Plan != "" {
			return in.Plan
		}
	}
	return in.Title
}

// editBody returns the path and the changed lines of each diff item.
func editBody(diffs []diffItem) string {
	var out []string
	for _, d := range diffs {
		oldL, newL := splitLines(d.OldText), splitLines(d.NewText)
		start := 0
		for start < len(oldL) && start < len(newL) && oldL[start] == newL[start] {
			start++
		}
		endO, endN := len(oldL), len(newL)
		for endO > start && endN > start && oldL[endO-1] == newL[endN-1] {
			endO--
			endN--
		}
		out = append(out, d.Path)
		for _, l := range oldL[start:endO] {
			out = append(out, "- "+l)
		}
		for _, l := range newL[start:endN] {
			out = append(out, "+ "+l)
		}
	}
	return strings.Join(out, "\n")
}

// splitLines splits s on "\n". An empty s has no lines.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
