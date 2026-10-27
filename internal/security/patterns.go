package security

import (
	"os"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// This file is a port of the command rules in Hermes tools/approval.py:
// HARDLINE_PATTERNS, DANGEROUS_PATTERNS, and the sudo stdin guard. The
// descriptions are the Hermes descriptions. Rules that name Hermes itself
// (~/.hermes, $HERMES_HOME, "hermes gateway", "hermes update") name
// tether here.
//
// Hermes lower-cases the command and matches with IGNORECASE and DOTALL. The
// Match functions do the same: they normalize and lower-case the command, and
// each rule compiles with the (?is) flags.

// Pattern is one command rule.
type Pattern struct {
	// ID is a stable snake-case name. The approval store keeps it.
	ID string
	// Description is the text that the user and the model see.
	Description string
	// Re finds the rule in a normalized, lower-case command.
	Re *regexp.Regexp
	// accept, when set, must accept one match of Re for the rule to match.
	// It replaces a Python lookahead that RE2 cannot express.
	accept func(s string, loc []int) bool
}

// matches reports whether the rule matches the prepared command s.
func (p Pattern) matches(s string) bool {
	if p.accept == nil {
		return p.Re.MatchString(s)
	}
	for _, loc := range p.Re.FindAllStringIndex(s, -1) {
		if p.accept(s, loc) {
			return true
		}
	}
	return false
}

func newPattern(id, desc, src string) Pattern {
	return Pattern{ID: id, Description: desc, Re: regexp.MustCompile(`(?is)` + src)}
}

// HardlinePatterns are always blocked. No mode, cron setting, or "Always"
// answer allows them. This is Hermes HARDLINE_PATTERNS.
var HardlinePatterns = []Pattern{
	newPattern("recursive_delete_root_filesystem", "recursive delete of root filesystem", `\brm\s+(-[^\s]*\s+)*(/|/\*|/ \*)(\s|$)`),
	newPattern("recursive_delete_system_directory", "recursive delete of system directory", `\brm\s+(-[^\s]*\s+)*(/home|/home/\*|/root|/root/\*|/etc|/etc/\*|/usr|/usr/\*|/var|/var/\*|/bin|/bin/\*|/sbin|/sbin/\*|/boot|/boot/\*|/lib|/lib/\*)(\s|$)`),
	newPattern("recursive_delete_home_directory", "recursive delete of home directory", `\brm\s+(-[^\s]*\s+)*(~|\$HOME)(/?|/\*)?(\s|$)`),
	newPattern("format_filesystem_mkfs", "format filesystem (mkfs)", `\bmkfs(\.[a-z0-9]+)?\b`),
	newPattern("dd_to_raw_block_device", "dd to raw block device", `\bdd\b[^\n]*\bof=/dev/(sd|nvme|hd|mmcblk|vd|xvd)[a-z0-9]*`),
	newPattern("redirect_to_raw_block_device", "redirect to raw block device", `>\s*/dev/(sd|nvme|hd|mmcblk|vd|xvd)[a-z0-9]*\b`),
	newPattern("fork_bomb_hardline", "fork bomb", `:\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`),
	newPattern("kill_all_processes_hardline", "kill all processes", `\bkill\s+(-[^\s]+\s+)*-1\b`),
	newPattern("system_shutdown_reboot", "system shutdown/reboot", `(?:^|[;&|\n`+"`"+`]|\$\()\s*(?:sudo\s+(?:-[^\s]+\s+)*)?(?:env\s+(?:\w+=\S*\s+)*)?(?:(?:exec|nohup|setsid|time)\s+)*\s*(shutdown|reboot|halt|poweroff)\b`),
	newPattern("init_0_6_shutdown_reboot", "init 0/6 (shutdown/reboot)", `(?:^|[;&|\n`+"`"+`]|\$\()\s*(?:sudo\s+(?:-[^\s]+\s+)*)?(?:env\s+(?:\w+=\S*\s+)*)?(?:(?:exec|nohup|setsid|time)\s+)*\s*init\s+[06]\b`),
	newPattern("systemctl_poweroff_reboot", "systemctl poweroff/reboot", `(?:^|[;&|\n`+"`"+`]|\$\()\s*(?:sudo\s+(?:-[^\s]+\s+)*)?(?:env\s+(?:\w+=\S*\s+)*)?(?:(?:exec|nohup|setsid|time)\s+)*\s*systemctl\s+(poweroff|reboot|halt|kexec)\b`),
	newPattern("telinit_0_6_shutdown_reboot", "telinit 0/6 (shutdown/reboot)", `(?:^|[;&|\n`+"`"+`]|\$\()\s*(?:sudo\s+(?:-[^\s]+\s+)*)?(?:env\s+(?:\w+=\S*\s+)*)?(?:(?:exec|nohup|setsid|time)\s+)*\s*telinit\s+[06]\b`),
}

// DangerousPatterns need an approval before they run. This is Hermes
// DANGEROUS_PATTERNS.
var DangerousPatterns = []Pattern{
	newPattern("delete_in_root_path", "delete in root path", `\brm\s+(-[^\s]*\s+)*/`),
	newPattern("recursive_delete", "recursive delete", `\brm\s+-[^\s]*r`),
	newPattern("recursive_delete_long_flag", "recursive delete (long flag)", `\brm\s+--recursive\b`),
	newPattern("world_other_writable_permissions", "world/other-writable permissions", `\bchmod\s+(-[^\s]*\s+)*(777|666|o\+[rwx]*w|a\+[rwx]*w)\b`),
	newPattern("recursive_world_other_writable_long_flag", "recursive world/other-writable (long flag)", `\bchmod\s+--recursive\b.*(777|666|o\+[rwx]*w|a\+[rwx]*w)`),
	newPattern("recursive_chown_to_root", "recursive chown to root", `\bchown\s+(-[^\s]*)?R\s+root`),
	newPattern("recursive_chown_to_root_long_flag", "recursive chown to root (long flag)", `\bchown\s+--recursive\b.*root`),
	newPattern("format_filesystem", "format filesystem", `\bmkfs\b`),
	newPattern("disk_copy", "disk copy", `\bdd\s+.*if=`),
	newPattern("write_to_block_device", "write to block device", `>\s*/dev/sd`),
	newPattern("sql_drop", "SQL DROP", `\bDROP\s+(TABLE|DATABASE)\b`),
	// Hermes: \bDELETE\s+FROM\b(?![^\n]*\bWHERE\b). RE2 has no lookahead,
	// so noWhereOnLine does the WHERE test.
	{
		ID:          "sql_delete_without_where",
		Description: "SQL DELETE without WHERE",
		Re:          regexp.MustCompile(`(?is)\bDELETE\s+FROM\b`),
		accept:      noWhereOnLine,
	},
	newPattern("sql_truncate", "SQL TRUNCATE", `\bTRUNCATE\s+(TABLE)?\s*\w`),
	newPattern("overwrite_system_config", "overwrite system config", `>\s*(?:/etc/|/private/(?:etc|var|tmp|home)/)`),
	newPattern("stop_restart_system_service", "stop/restart system service", `\bsystemctl\s+(-[^\s]+\s+)*(stop|restart|disable|mask)\b`),
	newPattern("kill_all_processes", "kill all processes", `\bkill\s+-9\s+-1\b`),
	newPattern("force_kill_processes", "force kill processes", `\bpkill\s+-9\b`),
	newPattern("force_kill_processes_killall_kill", "force kill processes (killall -KILL)", `\bkillall\s+(-[^\s]*\s+)*-(9|KILL|SIGKILL)\b`),
	newPattern("force_kill_processes_killall_s_kill", "force kill processes (killall -s KILL)", `\bkillall\s+(-[^\s]*\s+)*-s\s+(KILL|SIGKILL|9)\b`),
	newPattern("kill_processes_by_regex_killall_r", "kill processes by regex (killall -r)", `\bkillall\s+(-[^\s]*\s+)*-r\b`),
	newPattern("fork_bomb", "fork bomb", `:\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`),
	newPattern("shell_command_via_c_lc_flag", "shell command via -c/-lc flag", `\b(bash|sh|zsh|ksh)\s+-[^\s]*c(\s+|$)`),
	newPattern("script_execution_via_e_c_flag", "script execution via -e/-c flag", `\b(python[23]?|perl|ruby|node)\s+-[ec]\s+`),
	newPattern("pipe_remote_content_to_shell", "pipe remote content to shell", `\b(curl|wget)\b.*\|\s*(?:[/\w]*/)?(?:ba)?sh(?:\s|$|-c)`),
	newPattern("execute_remote_script_via_process_substitution", "execute remote script via process substitution", `\b(bash|sh|zsh|ksh)\s+<\s*<?\s*\(\s*(curl|wget)\b`),
	newPattern("overwrite_system_file_via_tee", "overwrite system file via tee", `\btee\b.*["\']?(?:(?:/etc/|/private/(?:etc|var|tmp|home)/)|/dev/sd|(?:~|\$home|\$\{home\})/\.ssh(?:/|$)|(?:~/\.tether/|(?:\$home|\$\{home\})/\.tether/|(?:\$tether_home|\$\{tether_home\})/)\.env\b|(?:~/\.tether/|(?:\$home|\$\{home\})/\.tether/|(?:\$tether_home|\$\{tether_home\})/)config\.yaml\b|(?:~|\$home|\$\{home\})/\.(?:bashrc|zshrc|profile|bash_profile|zprofile)\b|(?:~|\$home|\$\{home\})/\.(?:netrc|pgpass|npmrc|pypirc)\b)`),
	newPattern("overwrite_system_file_via_redirection", "overwrite system file via redirection", `>>?\s*["\']?(?:(?:/etc/|/private/(?:etc|var|tmp|home)/)|/dev/sd|(?:~|\$home|\$\{home\})/\.ssh(?:/|$)|(?:~/\.tether/|(?:\$home|\$\{home\})/\.tether/|(?:\$tether_home|\$\{tether_home\})/)\.env\b|(?:~/\.tether/|(?:\$home|\$\{home\})/\.tether/|(?:\$tether_home|\$\{tether_home\})/)config\.yaml\b|(?:~|\$home|\$\{home\})/\.(?:bashrc|zshrc|profile|bash_profile|zprofile)\b|(?:~|\$home|\$\{home\})/\.(?:netrc|pgpass|npmrc|pypirc)\b)`),
	newPattern("overwrite_project_env_config_via_tee", "overwrite project env/config via tee", `\btee\b.*["\']?(?:(?:(?:/|\.{1,2}/)?(?:[^\s/"\'`+"`"+`]+/)*\.env(?:\.[^/\s"\'`+"`"+`]+)*)|(?:(?:/|\.{1,2}/)?(?:[^\s/"\'`+"`"+`]+/)*config\.yaml))["\']?(?:\s*(?:&&|\|\||;).*)?$`),
	newPattern("overwrite_project_env_config_via_redirection", "overwrite project env/config via redirection", `>>?\s*["\']?(?:(?:(?:/|\.{1,2}/)?(?:[^\s/"\'`+"`"+`]+/)*\.env(?:\.[^/\s"\'`+"`"+`]+)*)|(?:(?:/|\.{1,2}/)?(?:[^\s/"\'`+"`"+`]+/)*config\.yaml))["\']?(?:\s*(?:&&|\|\||;).*)?$`),
	newPattern("xargs_with_rm", "xargs with rm", `\bxargs\s+.*\brm\b`),
	newPattern("find_exec_execdir_rm", "find -exec/-execdir rm", `\bfind\b.*-exec(?:dir)?\s+(/\S*/)?rm\b`),
	newPattern("find_delete", "find -delete", `\bfind\b.*-delete\b`),
	newPattern("stop_restart_tether_gateway", "stop/restart tether gateway (kills running agents)", `\btether\s+gateway\s+(stop|restart)\b`),
	newPattern("tether_update", "tether update (restarts gateway, kills running agents)", `\btether\s+update\b`),
	newPattern("docker_compose_restart_stop_kill_down", "docker compose restart/stop/kill/down (container lifecycle)", `\bdocker\s+compose\s+(restart|stop|kill|down)\b`),
	newPattern("docker_restart_stop_kill", "docker restart/stop/kill (container lifecycle)", `\bdocker\s+(restart|stop|kill)\b`),
	newPattern("start_gateway_outside_systemd", "start gateway outside systemd (use 'systemctl --user restart tether')", `\btether\s+gateway\b.*(&\s*$|&\s*;|\bdisown\b|\bsetsid\b)`),
	newPattern("start_gateway_outside_systemd_nohup", "start gateway outside systemd (use 'systemctl --user restart tether')", `\bnohup\b.*\btether\s+gateway\b`),
	newPattern("kill_tether_gateway_process", "kill tether/gateway process (self-termination)", `\b(pkill|killall)\b.*\b(tether|gateway)\b`),
	newPattern("kill_process_via_pgrep_expansion", "kill process via pgrep expansion (self-termination)", `\bkill\b.*\$\(\s*pgrep\b`),
	newPattern("kill_process_via_backtick_pgrep_expansion", "kill process via backtick pgrep expansion (self-termination)", `\bkill\b.*`+"`"+`\s*pgrep\b`),
	newPattern("copy_move_file_into_system_config_path", "copy/move file into system config path", `\b(cp|mv|install)\b.*\s(?:/etc/|/private/(?:etc|var|tmp|home)/)`),
	newPattern("overwrite_project_env_config_file", "overwrite project env/config file", `\b(cp|mv|install)\b.*\s["\']?(?:(?:(?:/|\.{1,2}/)?(?:[^\s/"\'`+"`"+`]+/)*\.env(?:\.[^/\s"\'`+"`"+`]+)*)|(?:(?:/|\.{1,2}/)?(?:[^\s/"\'`+"`"+`]+/)*config\.yaml))["\']?(?:\s*(?:&&|\|\||;).*)?$`),
	newPattern("in_place_edit_of_system_config", "in-place edit of system config", `\bsed\s+-[^\s]*i.*\s(?:/etc/|/private/(?:etc|var|tmp|home)/)`),
	newPattern("in_place_edit_of_system_config_long_flag", "in-place edit of system config (long flag)", `\bsed\s+--in-place\b.*\s(?:/etc/|/private/(?:etc|var|tmp|home)/)`),
	newPattern("in_place_edit_of_tether_config_env", "in-place edit of tether config/env", `\bsed\s+-[^\s]*i.*(?:(?:~/\.tether/|(?:\$home|\$\{home\})/\.tether/|(?:\$tether_home|\$\{tether_home\})/)config\.yaml\b|(?:~/\.tether/|(?:\$home|\$\{home\})/\.tether/|(?:\$tether_home|\$\{tether_home\})/)\.env\b)`),
	newPattern("in_place_edit_of_tether_config_env_long_flag", "in-place edit of tether config/env (long flag)", `\bsed\s+--in-place\b.*(?:(?:~/\.tether/|(?:\$home|\$\{home\})/\.tether/|(?:\$tether_home|\$\{tether_home\})/)config\.yaml\b|(?:~/\.tether/|(?:\$home|\$\{home\})/\.tether/|(?:\$tether_home|\$\{tether_home\})/)\.env\b)`),
	newPattern("script_execution_via_heredoc", "script execution via heredoc", `\b(python[23]?|perl|ruby|node)\s+<<`),
	newPattern("git_reset_hard", "git reset --hard (destroys uncommitted changes)", `\bgit\s+reset\s+--hard\b`),
	newPattern("git_force_push", "git force push (rewrites remote history)", `\bgit\s+push\b.*--force\b`),
	newPattern("git_force_push_short_flag", "git force push short flag (rewrites remote history)", `\bgit\s+push\b.*-f\b`),
	newPattern("git_clean_with_force", "git clean with force (deletes untracked files)", `\bgit\s+clean\s+-[^\s]*f`),
	newPattern("git_branch_force_delete", "git branch force delete", `\bgit\s+branch\s+-D\b`),
	newPattern("chmod_x_followed_by_immediate_execution", "chmod +x followed by immediate execution", `\bchmod\s+\+x\b.*[;&|]+\s*\./`),
	newPattern("sudo_with_privilege_flag", "sudo with privilege flag (stdin/askpass/shell/list)", `\bsudo\b[^;|&\n]*?\s+(?:-s\b|--stdin\b|-a\b|--askpass\b)`),
	newPattern("sudo_with_combined_flag_privilege_escalation", "sudo with combined-flag privilege escalation", `\bsudo\b[^;|&\n]*?\s+-[a-z]*[sa][a-z]*\b`),
}

// whereRe is the WHERE test of the Hermes rule "SQL DELETE without WHERE".
var whereRe = regexp.MustCompile(`(?i)\bwhere\b`)

// noWhereOnLine replaces the Hermes lookahead (?![^\n]*\bWHERE\b). It accepts
// a DELETE FROM match when the rest of its line has no WHERE.
func noWhereOnLine(s string, loc []int) bool {
	rest := s[loc[1]:]
	if i := strings.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[:i]
	}
	return !whereRe.MatchString(rest)
}

// sudoStdinRe is Hermes _SUDO_STDIN_RE.
var sudoStdinRe = regexp.MustCompile("(?i)(?:^|[;&|`\\n]|&&|\\|\\||\\$\\()\\s*sudo\\s+-S\\b")

// MatchHardline returns the first hardline rule that matches cmd.
func MatchHardline(cmd string) (Pattern, bool) {
	return matchFirst(HardlinePatterns, prepare(cmd))
}

// MatchSudoStdin reports whether cmd pipes a password to "sudo -S" while no
// sudo password is configured. The model then guesses the password, so the
// gate blocks the command. When sudoPasswordSet is true, the guard is off.
func MatchSudoStdin(cmd string, sudoPasswordSet bool) bool {
	if sudoPasswordSet {
		return false
	}
	return sudoStdinRe.MatchString(prepare(cmd))
}

// MatchDangerous returns the first dangerous rule that matches cmd. It checks
// DangerousPatterns first, then the protected path rules.
func MatchDangerous(cmd string) (Pattern, bool) {
	s := prepare(cmd)
	if p, ok := matchFirst(DangerousPatterns, s); ok {
		return p, true
	}
	protectedMu.RLock()
	extra := protectedPatterns
	protectedMu.RUnlock()
	return matchFirst(extra, s)
}

func matchFirst(list []Pattern, s string) (Pattern, bool) {
	for _, p := range list {
		if p.matches(s) {
			return p, true
		}
	}
	return Pattern{}, false
}

// prepare normalizes and lower-cases cmd for the rules.
func prepare(cmd string) string {
	return strings.ToLower(NormalizeCommand(cmd))
}

var (
	protectedMu       sync.RWMutex
	protectedPatterns []Pattern
)

// protectedOps finds a shell write to the path that follows it: a ">" or
// ">>" redirection, or tee, cp, mv, install, ln, rm, truncate, chmod, chown,
// dd, rsync, or "sed -i" with the path as an argument. The argument must be in
// the same simple command.
const protectedOps = `(?:>>?\s*["']?|\b(?:tee|cp|mv|install|ln|rm|truncate|chmod|chown|dd|rsync)\b[^\n;&|]*?[\s="']|\bsed\s+(?:-[^\s]*i|--in-place)[^\n;&|]*?[\s"'])`

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// AddProtectedPathPatterns sets the dangerous rules for shell writes to the
// protected paths. A path that ends in "/" is a folder and covers all files
// in it. A file path also covers the names that start with it, for example
// state.db-wal. Each call replaces the rules of the call before it. A path in
// the home folder also matches the "~", "$HOME", and "${HOME}" forms.
func AddProtectedPathPatterns(paths []string) {
	home, _ := os.UserHomeDir()
	out := make([]Pattern, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		dir := strings.HasSuffix(p, "/")
		base := strings.TrimSuffix(p, "/")
		forms := []string{base}
		if home != "" && home != "/" && strings.HasPrefix(base, home+"/") {
			rest := strings.TrimPrefix(base, home)
			forms = append(forms, "~"+rest, "$home"+rest, "${home}"+rest)
		}
		alts := make([]string, len(forms))
		for i, f := range forms {
			alts[i] = regexp.QuoteMeta(strings.ToLower(f))
		}
		src := protectedOps + `(?:` + strings.Join(alts, "|") + `)`
		if dir {
			src += "(?:/|[\\s\"'`;&|)]|$)"
		}
		slug := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(base), "_"), "_")
		out = append(out, newPattern("protected_path_write_"+slug, "write to protected path "+p, src))
	}
	protectedMu.Lock()
	protectedPatterns = out
	protectedMu.Unlock()
}

// ansiRe is Hermes tools/ansi_strip.py _ANSI_ESCAPE_RE: the ECMA-48 escape
// sequences and the 8-bit C1 control characters.
var ansiRe = regexp.MustCompile("\x1b(?:\\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]" +
	"|\\][\\s\\S]*?(?:\x07|\x1b\\\\)" +
	"|[PX^_][\\s\\S]*?(?:\x1b\\\\)" +
	"|[\x20-\x2f]+[\x30-\x7e]" +
	"|[\x30-\x7e])" +
	"|\u009b[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]" +
	"|\u009d[\\s\\S]*?(?:\x07|\u009c)" +
	"|[\u0080-\u009f]")

// invisible lists the zero-width and other invisible characters that a
// command can hide between two letters.
var invisible = map[rune]bool{
	'\u00ad': true, // soft hyphen
	'\u180e': true, // Mongolian vowel separator
	'\u200b': true, // zero-width space
	'\u200c': true, // zero-width non-joiner
	'\u200d': true, // zero-width joiner
	'\u2060': true, // word joiner
	'\u2061': true, '\u2062': true, '\u2063': true, '\u2064': true,
	'\ufeff': true, // zero-width no-break space
}

// lookalike folds Cyrillic and Greek letters that look like Latin letters,
// dash variants, and some slash and bar variants. NFKC already folds the
// fullwidth ASCII forms.
var lookalike = map[rune]rune{
	// Cyrillic, lower case
	'\u0430': 'a', '\u0432': 'b', '\u0433': 'r', '\u0435': 'e', '\u0451': 'e', '\u043a': 'k', '\u043c': 'm',
	'\u043d': 'h', '\u043e': 'o', '\u043f': 'n', '\u0440': 'p', '\u0441': 'c', '\u0442': 't', '\u0443': 'y',
	'\u0445': 'x', '\u044c': 'b', '\u0455': 's', '\u0456': 'i', '\u0457': 'i', '\u0458': 'j', '\u0501': 'd',
	'\u04bb': 'h', '\u04cf': 'l', '\u051b': 'q', '\u051d': 'w',
	// Cyrillic, upper case
	'\u0410': 'A', '\u0412': 'B', '\u0415': 'E', '\u0401': 'E', '\u041a': 'K', '\u041c': 'M', '\u041d': 'H',
	'\u041e': 'O', '\u0420': 'P', '\u0421': 'C', '\u0422': 'T', '\u0423': 'Y', '\u0425': 'X', '\u0405': 'S',
	'\u0406': 'I', '\u0408': 'J', '\u051a': 'Q', '\u051c': 'W', '\u04c0': 'I',
	// Greek, lower case
	'\u03b1': 'a', '\u03b3': 'y', '\u03b5': 'e', '\u03b9': 'i', '\u03ba': 'k', '\u03bd': 'v', '\u03bf': 'o',
	'\u03c1': 'p', '\u03c4': 't', '\u03c5': 'u', '\u03c7': 'x',
	// Greek, upper case
	'\u0391': 'A', '\u0392': 'B', '\u0395': 'E', '\u0396': 'Z', '\u0397': 'H', '\u0399': 'I', '\u039a': 'K',
	'\u039c': 'M', '\u039d': 'N', '\u039f': 'O', '\u03a1': 'P', '\u03a4': 'T', '\u03a5': 'Y', '\u03a7': 'X',
	// Latin look-alikes
	'\u0261': 'g', '\u0131': 'i',
	// Dashes and minus signs
	'\u02d7': '-', '\u2010': '-', '\u2011': '-', '\u2012': '-', '\u2013': '-',
	'\u2014': '-', '\u2015': '-', '\u2043': '-', '\u2212': '-', '\ufe58': '-',
	'\ufe63': '-', '\uff0d': '-',
	// Slashes and bars
	'\u2044': '/', '\u2215': '/', '\u29f8': '/', '\u01c0': '|', '\u2223': '|',
}

// NormalizeCommand prepares a command for the rules, so that hidden
// characters cannot get past them. It removes ANSI escape sequences, NUL
// bytes, and zero-width characters. It applies Unicode NFKC and folds the
// look-alike characters. It removes each backslash-newline line continuation,
// as the shell does. It collapses each run of whitespace to one space, or to
// one newline if the run has a newline, and trims the ends. NormalizeCommand
// keeps the case.
func NormalizeCommand(cmd string) string {
	s := strings.ToValidUTF8(cmd, "")
	s = ansiRe.ReplaceAllLiteralString(s, "")
	s = strings.Map(func(r rune) rune {
		if r == 0 || invisible[r] {
			return -1
		}
		return r
	}, s)
	s = norm.NFKC.String(s)
	s = strings.Map(func(r rune) rune {
		if f, ok := lookalike[r]; ok {
			return f
		}
		return r
	}, s)
	s = strings.ReplaceAll(s, "\\\r\n", "")
	s = strings.ReplaceAll(s, "\\\n", "")

	var b strings.Builder
	b.Grow(len(s))
	inSpace, newline := false, false
	for _, r := range s {
		if unicode.IsSpace(r) {
			inSpace = true
			newline = newline || r == '\n' || r == '\r'
			continue
		}
		if inSpace && b.Len() > 0 {
			if newline {
				b.WriteByte('\n')
			} else {
				b.WriteByte(' ')
			}
		}
		inSpace, newline = false, false
		b.WriteRune(r)
	}
	return b.String()
}
