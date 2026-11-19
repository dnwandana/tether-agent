package security

import (
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Binding is one bound secret that a command may get. Parts holds the index in Parsed.Cmds of
// the one part that matches a bound prefix of the secret: the command itself, or the program
// that its wrappers run.
type Binding struct {
	Name  string
	Parts []int
}

// Withheld is one bound secret that a command names or matches but may not get.
type Withheld struct {
	Name, Reason string
}

// fixedWrappers are the wrappers that a bound secret can pass through. Each one only runs the
// next words as one program. A shell, eval, or a wrapper that can read or print the environment
// fails, also when it is transparent.
var fixedWrappers = []string{
	"timeout", "nice", "nohup", "stdbuf", "setsid", "ionice", "taskset", "chrt", "flock", "time", "env",
}

// The withheld reasons of BindSecrets.
const (
	reasonUnread   = "the command cannot be read"
	reasonParts    = "the command has more than one part. A bound secret goes only to a command that runs alone"
	reasonLookup   = "the command changes how the shell finds a program"
	reasonNetwork  = "the command can send output to the network through /dev/tcp or /dev/udp"
	reasonRuntime  = "the command builds a redirect target at run time"
	reasonHereText = "the command expands text in a here-document or a here-string"
)

// BindSecrets returns the bound names that the command may get, and one reason for each
// bound name that the command names or matches but may not get. secrets maps each name of
// exec_secrets to its command entries. Both lists are in name order.
//
// A bound secret goes only to a command text that is one simple command. ChildEnvBound gives
// the value to the whole shell, so a second part of the text could read it.
func BindSecrets(p Parsed, secrets map[string][]string) (give []Binding, withheld []Withheld) {
	for _, name := range slices.Sorted(maps.Keys(secrets)) {
		entries := splitEntries(secrets[name])
		re := varRe(name)
		matched := slices.ContainsFunc(p.Cmds, func(c SimpleCmd) bool { return matchesPrefix(c.Argv, entries) })
		candidate := matched || namesVarIn(p, re) ||
			(!p.Read && (viewHasEntry(p.View, entries) || re.MatchString(p.View)))
		if !candidate {
			continue
		}
		part, reason := decide(p, entries)
		if reason != "" {
			withheld = append(withheld, Withheld{Name: name, Reason: reason})
			continue
		}
		give = append(give, Binding{Name: name, Parts: []int{part}})
	}
	return give, withheld
}

// decide applies the rules for one candidate. It returns the index of the bound part, or the
// reason when the secret is withheld.
func decide(p Parsed, entries [][]string) (int, string) {
	if !p.Read {
		return -1, reasonUnread
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(p.text), "")
	if err != nil {
		return -1, reasonUnread
	}
	call, redirs, reason := oneCommand(f)
	if reason != "" {
		return -1, reason
	}
	for _, c := range p.Cmds {
		if w, ok := otherHost(c); ok {
			return -1, `"` + w + `" runs the command as another user or on another host`
		}
	}
	if len(call.Assigns) > 0 || p.LookupChanged() || slices.ContainsFunc(p.Cmds, SimpleCmd.Assigned) {
		return -1, reasonLookup
	}
	part, reason := boundPart(p, entries)
	if reason != "" {
		return -1, reason
	}
	if reason := redirReason(redirs); reason != "" {
		return -1, reason
	}
	return part, ""
}

// oneCommand returns the simple command of f and its redirects. It returns a reason when f
// is not one simple command: a list, a pipe, a background job, a compound command, or a
// command with a command substitution or a process substitution. The time keyword can come
// before the command.
func oneCommand(f *syntax.File) (*syntax.CallExpr, []*syntax.Redirect, string) {
	if len(f.Stmts) != 1 {
		return nil, nil, reasonParts
	}
	if hasSubst(f, false) {
		return nil, nil, reasonParts
	}
	var redirs []*syntax.Redirect
	for s := f.Stmts[0]; s != nil; {
		if s.Negated || s.Background || s.Coprocess || s.Disown {
			return nil, nil, reasonParts
		}
		redirs = append(redirs, s.Redirs...)
		switch c := s.Cmd.(type) {
		case *syntax.TimeClause:
			s = c.Stmt
		case *syntax.CallExpr:
			if len(c.Args) == 0 {
				return nil, nil, reasonParts
			}
			return c, redirs, ""
		case *syntax.DeclClause:
			return nil, nil, `"` + c.Variant.Value + `" is not a bound command`
		default:
			return nil, nil, reasonParts
		}
	}
	return nil, nil, reasonParts
}

// boundPart returns the index of the part that matches a bound prefix. Each part before it
// must be a level of a fixed wrapper. It returns the reason when a part is not bound and is
// not a fixed wrapper.
func boundPart(p Parsed, entries [][]string) (int, string) {
	for i, c := range p.Cmds {
		if matchesPrefix(c.Argv, entries) {
			return i, ""
		}
		if fixedWrapper(p, i) {
			continue
		}
		prog := filepath.Base(c.Argv[0])
		if _, ok := LookupWrapper(prog); ok {
			return -1, `"` + prog + `" is not an allowed wrapper for a bound secret`
		}
		return -1, `"` + prog + `" is not a bound command`
	}
	return -1, reasonUnread
}

// fixedWrapper reports whether part i is a level of a fixed wrapper that runs the next part as
// a program. An inner part from a new parse, such as the string of "flock -c", fails. env
// fails with an option or an assignment.
func fixedWrapper(p Parsed, i int) bool {
	c := p.Cmds[i]
	if c.inner == 0 || i+1 >= len(p.Cmds) || p.Cmds[i+1].Depth != c.Depth {
		return false
	}
	prog := filepath.Base(c.Argv[0])
	if !slices.Contains(fixedWrappers, prog) {
		return false
	}
	if prog == "env" && (len(c.Argv) < 2 || strings.HasPrefix(c.Argv[1], "-") || isAssignWord(c.Argv[1])) {
		return false
	}
	return true
}

// redirReason returns the reason when a redirect of the command can send output past the
// redactor, or when the shell builds a redirect at run time.
func redirReason(redirs []*syntax.Redirect) string {
	for _, rd := range redirs {
		switch rd.Op {
		case syntax.Hdoc, syntax.DashHdoc:
			if rd.Hdoc != nil && slices.ContainsFunc(rd.Hdoc.Parts, func(wp syntax.WordPart) bool {
				_, ok := wp.(*syntax.Lit)
				return !ok
			}) {
				return reasonHereText
			}
			continue
		case syntax.WordHdoc:
			if _, ok := literalWord(rd.Word, false); !ok {
				return reasonHereText
			}
			continue
		}
		target, ok := literalWord(rd.Word, true)
		switch {
		case !ok:
			return reasonRuntime
		case strings.Contains(target, "/dev/tcp/"), strings.Contains(target, "/dev/udp/"):
			return reasonNetwork
		case strings.ContainsAny(target, "$`"):
			return reasonRuntime
		}
	}
	return ""
}

// literalWord returns the value of w after quote removal. A $'...' string is decoded as bash
// does. ok is false when w has an expansion, a substitution, or a $"..." string. When glob is
// true, an unquoted glob character also makes ok false, because bash expands a glob in a
// redirect target.
func literalWord(w *syntax.Word, glob bool) (string, bool) {
	if w == nil {
		return "", true
	}
	var b strings.Builder
	for _, wp := range w.Parts {
		switch v := wp.(type) {
		case *syntax.Lit:
			if glob && hasGlob([]byte(v.Value)) {
				return "", false
			}
			b.WriteString(unquoteBare(v.Value))
		case *syntax.SglQuoted:
			if v.Dollar {
				b.WriteString(decodeANSIC(v.Value))
			} else {
				b.WriteString(v.Value)
			}
		case *syntax.DblQuoted:
			if v.Dollar {
				return "", false
			}
			for _, q := range v.Parts {
				l, ok := q.(*syntax.Lit)
				if !ok {
					return "", false
				}
				b.WriteString(unquoteDouble(l.Value))
			}
		default:
			return "", false
		}
	}
	return b.String(), true
}

// splitEntries splits each entry into words. It skips an entry with no words.
func splitEntries(entries []string) [][]string {
	var out [][]string
	for _, e := range entries {
		if w := strings.Fields(e); len(w) > 0 {
			out = append(out, w)
		}
	}
	return out
}

// matchesPrefix reports whether argv starts with the words of one entry. The program word
// compares by base name, unless the first word of the entry has a slash.
func matchesPrefix(argv []string, entries [][]string) bool {
	for _, e := range entries {
		if len(argv) < len(e) || len(argv) == 0 {
			continue
		}
		ok := slices.Equal(argv[1:len(e)], e[1:])
		if strings.Contains(e[0], "/") {
			ok = ok && argv[0] == e[0]
		} else {
			ok = ok && filepath.Base(argv[0]) == e[0]
		}
		if ok {
			return true
		}
	}
	return false
}

// varRe returns a regexp that finds $NAME or ${NAME in a word.
func varRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`\$\{?` + regexp.QuoteMeta(name) + `(?:[^A-Za-z0-9_]|$)`)
}

// namesVarIn reports whether an Argv word or a Redirs word of a part names the variable.
func namesVarIn(p Parsed, re *regexp.Regexp) bool {
	for _, c := range p.Cmds {
		if slices.ContainsFunc(slices.Concat(c.Argv, c.Redirs), re.MatchString) {
			return true
		}
	}
	return false
}

// viewHasEntry reports whether the view holds the words of an entry joined by one space.
func viewHasEntry(view string, entries [][]string) bool {
	for _, e := range entries {
		if strings.Contains(view, strings.Join(e, " ")) {
			return true
		}
	}
	return false
}

// otherHost reports whether the part runs a wrapper that changes the user or the host. It
// returns the name of the wrapper.
func otherHost(c SimpleCmd) (string, bool) {
	prog := filepath.Base(c.Argv[0])
	if k, ok := LookupWrapper(prog); ok && (k == WrapPrivilege || k == WrapHost) {
		return prog, true
	}
	if len(c.Via) > 0 {
		// A Via entry such as "chroot /srv" holds the folder after the name.
		if w := strings.Fields(c.Via[0]); len(w) > 0 {
			return w[0], true
		}
		return c.Via[0], true
	}
	return "", false
}
