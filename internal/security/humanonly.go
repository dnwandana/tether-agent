package security

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// humanOnlyAnchor starts each raw regex at a command position: the start, a separator, or a
// known wrapper. A word inside an argument, such as "/etc/passwd", does not match.
const humanOnlyAnchor = `(?:^|[;&|(\n]|\b(?:sudo|doas|env|exec|command|nice|nohup|time|timeout|setsid|xargs)\s+(?:-\S+\s+)*)\s*(?:\S*/)?`

// humanOnlyScriptStart starts a script regex at the start of a string literal. Only a
// system program directory can come before the program name.
const humanOnlyScriptStart = `["']\s*(?:/(?:usr/)?(?:local/)?s?bin/)?`

// humanOnlyRule is one built-in human-only rule. pat.Re is the raw regex for a command that
// the gate cannot read. cmd matches one parsed simple command. script matches a string
// literal of a Python script.
type humanOnlyRule struct {
	pat    Pattern
	cmd    func(c SimpleCmd) bool
	script *regexp.Regexp
}

var humanOnlyRules = buildHumanOnlyRules()

// HumanOnlyPatterns are the built-in human-only shell rules, in the table order.
var HumanOnlyPatterns = func() []Pattern {
	out := make([]Pattern, len(humanOnlyRules))
	for i, r := range humanOnlyRules {
		out[i] = r.pat
	}
	return out
}()

var (
	humanOnlyMu     sync.RWMutex
	humanOnlyRaised map[string]bool // the entries of approvals.human_only
)

var (
	rawRedirAuthRe = regexp.MustCompile(`(?is)^[&\d]*>{1,2}\|?\s*(\S*authorized_keys)(?:\s|$|[;&|)'"])`)
	redirOpRe      = regexp.MustCompile(`^(?:\d*>{1,2}\|?|&>{1,2})$`)
	rawDevRe       = regexp.MustCompile(`^/dev/(?:sd|nvme|hd|mmcblk|vd|xvd)[a-z0-9]*`)
	privGroups     = []string{"sudo", "wheel", "admin", "docker"}
)

// buildHumanOnlyRules builds the rule table.
func buildHumanOnlyRules() []humanOnlyRule {
	mk := func(id, desc string, progs []string, extra string, cmd func(SimpleCmd) bool, script string) humanOnlyRule {
		alt := strings.Join(progs, "|")
		src := humanOnlyAnchor + `(?:` + alt + `)\b[^\n;&|]*`
		if extra != "" {
			src += `|` + extra
		}
		p := newPattern(id, desc, src)
		p.accept = func(s string, loc []int) bool {
			text := s[loc[0]:loc[1]]
			fields := strings.Fields(text)
			for i, f := range fields {
				f = strings.Trim(f, `"'`)
				if slices.Contains(progs, filepath.Base(f)) {
					argv := make([]string, 0, len(fields)-i)
					for _, a := range fields[i:] {
						argv = append(argv, strings.Trim(a, `"'`))
					}
					if cmd(SimpleCmd{Argv: argv}) {
						return true
					}
					break
				}
			}
			if extra != "" {
				if m := rawRedirAuthRe.FindStringSubmatch(strings.TrimLeft(text, " \t")); m != nil {
					return isAuthorizedKeys(m[1])
				}
			}
			return false
		}
		return humanOnlyRule{pat: p, cmd: cmd, script: regexp.MustCompile(`(?is)` + humanOnlyScriptStart + `(?:` + script + `)`)}
	}
	return []humanOnlyRule{
		mk("human_only_password_change", "password change",
			[]string{"passwd", "chpasswd", "smbpasswd", "usermod", "htpasswd"}, "", passwordChange,
			`passwd\b|chpasswd\b|smbpasswd\b|usermod\b[^"'\n]*\s(?:-\w*p|--password)|htpasswd\b[^"'\n]*`),
		mk("human_only_account_creation", "account creation",
			[]string{"useradd", "adduser", "usermod", "gpasswd"}, "", accountCreation,
			`useradd\b|adduser\b|usermod\b[^"'\n]*|gpasswd\b[^"'\n]*`),
		mk("human_only_credential_access", "new credential access",
			[]string{"ssh-copy-id", "tee", "gh", "gcloud", "aws", "az", "docker", "npm"},
			`&?>>?\|?\s*\S*authorized_keys(?:\s|$|[;&|)'"])`, credentialAccess,
			`ssh-copy-id\b|(?:gh\s+auth\s+(?:login|refresh)|gcloud\s+auth\s+login|aws\s+configure|az\s+login|docker\s+login|npm\s+(?:login|adduser))\b`),
		mk("human_only_permanent_deletion", "permanent deletion",
			[]string{"shred", "wipefs", "srm", "blkdiscard", "dd"}, "", permanentDeletion,
			`shred\b|wipefs\b|srm\b|blkdiscard\b|dd\s[^"'\n]*`),
	}
}

// scriptCheck narrows a script regex match: some rules need an argument test on the literal.
func (r humanOnlyRule) scriptMatch(code string) bool {
	for _, loc := range r.script.FindAllStringIndex(code, -1) {
		text := strings.TrimLeft(code[loc[0]:loc[1]], `"' `)
		fields := strings.Fields(text)
		if len(fields) == 0 {
			continue
		}
		if prog := filepath.Base(fields[0]); prog == "usermod" || prog == "gpasswd" || prog == "htpasswd" || prog == "dd" {
			if !r.cmd(SimpleCmd{Argv: fields}) {
				continue
			}
		}
		return true
	}
	return false
}

func shortCluster(w string) bool { return len(w) > 1 && w[0] == '-' && w[1] != '-' }

func nonOptions(args []string) []string {
	var out []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			out = append(out, a)
		}
	}
	return out
}

func passwordChange(c SimpleCmd) bool {
	if len(c.Argv) == 0 {
		return false
	}
	args := c.Argv[1:]
	switch filepath.Base(c.Argv[0]) {
	case "passwd", "chpasswd", "smbpasswd":
		return true
	case "usermod":
		for _, a := range args {
			if a == "--password" || strings.HasPrefix(a, "--password=") || (shortCluster(a) && strings.HasPrefix(a, "-p")) {
				return true
			}
		}
	case "htpasswd":
		for _, a := range args {
			if shortCluster(a) && strings.Contains(a, "c") {
				return false
			}
		}
		return true
	}
	return false
}

// privGroupList reports whether a comma list names a privileged group.
func privGroupList(v string) bool {
	for g := range strings.SplitSeq(v, ",") {
		if slices.Contains(privGroups, strings.TrimSpace(g)) {
			return true
		}
	}
	return false
}

func accountCreation(c SimpleCmd) bool {
	if len(c.Argv) == 0 {
		return false
	}
	args := c.Argv[1:]
	switch filepath.Base(c.Argv[0]) {
	case "useradd", "adduser":
		return true
	case "usermod":
		for i, a := range args {
			switch {
			case strings.HasPrefix(a, "--groups="):
				if privGroupList(strings.TrimPrefix(a, "--groups=")) {
					return true
				}
			case a == "--groups":
				if i+1 < len(args) && privGroupList(args[i+1]) {
					return true
				}
			case shortCluster(a) && strings.Contains(a, "G"):
				rest := a[strings.Index(a, "G")+1:]
				if rest == "" && i+1 < len(args) {
					rest = args[i+1]
				}
				if privGroupList(rest) {
					return true
				}
			}
		}
	case "gpasswd":
		add := false
		for _, a := range args {
			if shortCluster(a) && strings.Contains(a, "a") {
				add = true
			}
		}
		if pos := nonOptions(args); add && len(pos) > 0 && slices.Contains(privGroups, pos[len(pos)-1]) {
			return true
		}
	}
	return false
}

func isAuthorizedKeys(target string) bool {
	return filepath.Base(strings.Trim(target, `"'`)) == "authorized_keys"
}

func credentialAccess(c SimpleCmd) bool {
	for i := 0; i+1 < len(c.Redirs); i++ {
		if redirOpRe.MatchString(c.Redirs[i]) && isAuthorizedKeys(c.Redirs[i+1]) {
			return true
		}
	}
	if len(c.Argv) == 0 {
		return false
	}
	pos := nonOptions(c.Argv[1:])
	has := func(words ...string) bool {
		return len(pos) >= len(words) && slices.Equal(pos[:len(words)], words)
	}
	switch filepath.Base(c.Argv[0]) {
	case "ssh-copy-id":
		return true
	case "tee":
		if slices.ContainsFunc(pos, isAuthorizedKeys) {
			return true
		}
	case "gh":
		return has("auth", "login") || has("auth", "refresh")
	case "gcloud":
		return has("auth", "login")
	case "aws":
		return has("configure")
	case "az", "docker":
		return has("login")
	case "npm":
		return has("login") || has("adduser")
	}
	return false
}

func permanentDeletion(c SimpleCmd) bool {
	if len(c.Argv) == 0 {
		return false
	}
	switch filepath.Base(c.Argv[0]) {
	case "shred", "wipefs", "srm", "blkdiscard":
		return true
	case "dd":
		src, of := false, ""
		for _, a := range c.Argv[1:] {
			switch {
			case a == "if=/dev/zero" || a == "if=/dev/urandom":
				src = true
			case strings.HasPrefix(a, "of="):
				of = strings.TrimPrefix(a, "of=")
			}
		}
		return src && of != "" && !rawDevRe.MatchString(of)
	}
	return false
}

// MatchHumanOnlyParsed returns the first human-only rule that the command matches. A readable
// command is checked by Argv and Redirs only, never by the view. An unreadable command is
// checked by the raw regexes.
func MatchHumanOnlyParsed(raw string, p Parsed) (Pattern, bool) {
	if p.Read {
		for _, c := range p.Cmds {
			for _, r := range humanOnlyRules {
				if r.cmd(c) {
					return r.pat, true
				}
			}
		}
		return Pattern{}, false
	}
	s := prepare(raw)
	for _, r := range humanOnlyRules {
		if r.pat.matches(s) {
			return r.pat, true
		}
	}
	return Pattern{}, false
}

// MatchHumanOnlyScript returns the first human-only rule that the script text matches. A rule
// matches a program name at the start of a string literal.
func MatchHumanOnlyScript(code string) (Pattern, bool) {
	for _, r := range humanOnlyRules {
		if r.scriptMatch(code) {
			return r.pat, true
		}
	}
	return Pattern{}, false
}

// SetHumanOnlyRules sets the raise list: the entries of approvals.human_only. An entry is a
// rule ID or "mcp:<server>.<tool>". A nil list clears it.
func SetHumanOnlyRules(entries []string) {
	m := make(map[string]bool, len(entries))
	for _, e := range entries {
		m[e] = true
	}
	humanOnlyMu.Lock()
	humanOnlyRaised = m
	humanOnlyMu.Unlock()
}

// IsHumanOnlyRaised reports whether the raise list holds the rule ID.
func IsHumanOnlyRaised(patternID string) bool {
	if strings.HasPrefix(patternID, "mcp:") {
		return false
	}
	humanOnlyMu.RLock()
	defer humanOnlyMu.RUnlock()
	return humanOnlyRaised[patternID]
}

// IsHumanOnlyMCP reports whether the raise list holds the MCP tool. It compares the whole
// string "mcp:<server>.<tool>", because a name can hold a ".".
func IsHumanOnlyMCP(server, tool string) bool {
	humanOnlyMu.RLock()
	defer humanOnlyMu.RUnlock()
	return humanOnlyRaised["mcp:"+server+"."+tool]
}

// CheckHumanOnly returns an error for the first entry that is not an "mcp:" entry or the ID of
// a dangerous rule.
func CheckHumanOnly(entries []string) error {
	for _, e := range entries {
		if strings.HasPrefix(e, "mcp:") {
			continue
		}
		if _, ok := PatternByID(e); ok {
			continue
		}
		for _, h := range HardlinePatterns {
			if h.ID == e {
				return fmt.Errorf("approvals.human_only: %q: hardline rules are already blocked", e)
			}
		}
		return fmt.Errorf("approvals.human_only: %q is not a rule ID of the dangerous rules", e)
	}
	return nil
}
