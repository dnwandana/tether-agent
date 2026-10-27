package skills

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Severity is the severity of a guard finding.
type Severity string

const (
	SevCritical Severity = "critical"
	SevHigh     Severity = "high"
	SevMedium   Severity = "medium"
	SevLow      Severity = "low"
)

// ThreatPattern is one rule of the skill guard.
type ThreatPattern struct {
	ID          string
	Severity    Severity
	Category    string
	Description string
	// Re is the RE2 form of the Hermes regex. It is case-insensitive.
	Re *regexp.Regexp
	// Extra, when it is set, replaces a Python lookahead that RE2 cannot
	// express. A line hits only when Re matches and Extra returns true.
	Extra func(line string) bool
}

// Match reports whether the pattern hits one line. Like Hermes, the guard
// applies each pattern to one line at a time.
func (p ThreatPattern) Match(line string) bool {
	line = foldPythonCase(line)
	if !p.Re.MatchString(line) {
		return false
	}
	return p.Extra == nil || p.Extra(line)
}

// pyFolds holds the runes that Python re.IGNORECASE matches with an ASCII
// letter but Go (?i) does not, or that Go \b treats as a non-word rune.
var pyFolds = strings.NewReplacer("İ", "i", "ı", "i", "ſ", "s", "K", "k")

// foldPythonCase maps the runes in pyFolds to their ASCII letters. Then Go
// gives the same case-insensitive matches as Python.
func foldPythonCase(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] >= utf8.RuneSelf {
			return pyFolds.Replace(line)
		}
	}
	return line
}

// patternRow is one Hermes THREAT_PATTERNS tuple, in the Hermes order. The
// regex text is the Hermes text without a change.
type patternRow struct {
	id   string
	sev  Severity
	cat  string
	desc string
	re   string
}

// hermesPatterns is the port of THREAT_PATTERNS in Hermes
// tools/skills_guard.py. The descriptions are the Hermes text.
var hermesPatterns = []patternRow{
	{"env_exfil_curl", SevCritical, "exfiltration", "curl command interpolating secret environment variable", `curl\s+[^\n]*\$\{?\w*(KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL|API)`},
	{"env_exfil_wget", SevCritical, "exfiltration", "wget command interpolating secret environment variable", `wget\s+[^\n]*\$\{?\w*(KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL|API)`},
	{"env_exfil_fetch", SevCritical, "exfiltration", "fetch() call interpolating secret environment variable", `fetch\s*\([^\n]*\$\{?\w*(KEY|TOKEN|SECRET|PASSWORD|API)`},
	{"env_exfil_httpx", SevCritical, "exfiltration", "HTTP library call with secret variable", `httpx?\.(get|post|put|patch)\s*\([^\n]*(KEY|TOKEN|SECRET|PASSWORD)`},
	{"env_exfil_requests", SevCritical, "exfiltration", "requests library call with secret variable", `requests\.(get|post|put|patch)\s*\([^\n]*(KEY|TOKEN|SECRET|PASSWORD)`},
	{"encoded_exfil", SevHigh, "exfiltration", "base64 encoding combined with environment access", `base64[^\n]*env`},
	{"ssh_dir_access", SevHigh, "exfiltration", "references user SSH directory", `\$HOME/\.ssh|\~/\.ssh`},
	{"aws_dir_access", SevHigh, "exfiltration", "references user AWS credentials directory", `\$HOME/\.aws|\~/\.aws`},
	{"gpg_dir_access", SevHigh, "exfiltration", "references user GPG keyring", `\$HOME/\.gnupg|\~/\.gnupg`},
	{"kube_dir_access", SevHigh, "exfiltration", "references Kubernetes config directory", `\$HOME/\.kube|\~/\.kube`},
	{"docker_dir_access", SevHigh, "exfiltration", "references Docker config (may contain registry creds)", `\$HOME/\.docker|\~/\.docker`},
	{"hermes_env_access", SevCritical, "exfiltration", "directly references Hermes secrets file", `\$HOME/\.hermes/\.env|\~/\.hermes/\.env`},
	{"read_secrets_file", SevCritical, "exfiltration", "reads known secrets file", `cat\s+(?!>)[^\n]*(\.env|credentials|\.netrc|\.pgpass|\.npmrc|\.pypirc)`},
	{"dump_all_env", SevHigh, "exfiltration", "dumps all environment variables", `printenv|env\s*\|`},
	{"python_os_environ", SevHigh, "exfiltration", "accesses os.environ (potential env dump)", `os\.environ\b(?!\s*\.get\s*\(\s*["\'](?![^"\']*(?:KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL)))`},
	{"python_environ_get_secret", SevCritical, "exfiltration", "reads secret via os.environ.get()", `os\.environ\s*\.get\s*\(\s*["\'][^"\']*(?:KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL)`},
	{"python_getenv_secret", SevCritical, "exfiltration", "reads secret via os.getenv()", `os\.getenv\s*\(\s*[^\)]*(?:KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL)`},
	{"node_process_env", SevHigh, "exfiltration", "accesses process.env (Node.js environment)", `process\.env\[`},
	{"ruby_env_secret", SevCritical, "exfiltration", "reads secret via Ruby ENV[]", `ENV\[.*(?:KEY|TOKEN|SECRET|PASSWORD)`},
	{"dns_exfil", SevCritical, "exfiltration", "DNS lookup with variable interpolation (possible DNS exfiltration)", `\b(dig|nslookup|host)\s+[^\n]*\$`},
	{"tmp_staging", SevCritical, "exfiltration", "writes to /tmp then exfiltrates", `>\s*/tmp/[^\s]*\s*&&\s*(curl|wget|nc|python)`},
	{"md_image_exfil", SevHigh, "exfiltration", "markdown image URL with variable interpolation (image-based exfil)", `!\[.*\]\(https?://[^\)]*\$\{?`},
	{"md_link_exfil", SevHigh, "exfiltration", "markdown link with variable interpolation", `\[.*\]\(https?://[^\)]*\$\{?`},
	{"prompt_injection_ignore", SevCritical, "injection", "prompt injection: ignore previous instructions", `ignore\s+(?:\w+\s+)*(previous|all|above|prior)\s+instructions`},
	{"role_hijack", SevHigh, "injection", "attempts to override the agent's role", `you\s+are\s+(?:\w+\s+)*now\s+`},
	{"deception_hide", SevCritical, "injection", "instructs agent to hide information from user", `do\s+not\s+(?:\w+\s+)*tell\s+(?:\w+\s+)*the\s+user`},
	{"sys_prompt_override", SevCritical, "injection", "attempts to override the system prompt", `system\s+(?:\w+\s+)*prompt\s+(?:\w+\s+)*override`},
	{"role_pretend", SevHigh, "injection", "attempts to make the agent assume a different identity", `pretend\s+(?:\w+\s+)*(you\s+are|to\s+be)\s+`},
	{"disregard_rules", SevCritical, "injection", "instructs agent to disregard its rules", `disregard\s+(?:\w+\s+)*(your|all|any)\s+(?:\w+\s+)*(instructions|rules|guidelines)`},
	{"leak_system_prompt", SevHigh, "injection", "attempts to extract the system prompt", `output\s+(?:\w+\s+)*(system|initial)\s+prompt`},
	{"conditional_deception", SevHigh, "injection", "conditional instruction to behave differently when unobserved", `(when|if)\s+no\s*one\s+is\s+(watching|looking)`},
	{"bypass_restrictions", SevCritical, "injection", "instructs agent to act without restrictions", `act\s+as\s+(if|though)\s+(?:\w+\s+)*you\s+(?:\w+\s+)*(have\s+no|don\'t\s+have)\s+(?:\w+\s+)*(restrictions|limits|rules)`},
	{"translate_execute", SevCritical, "injection", "translate-then-execute evasion technique", `translate\s+.*\s+into\s+.*\s+and\s+(execute|run|eval)`},
	{"html_comment_injection", SevHigh, "injection", "hidden instructions in HTML comments", `<!--[^>]*(?:ignore|override|system|secret|hidden)[^>]*-->`},
	{"hidden_div", SevHigh, "injection", "hidden HTML div (invisible instructions)", `<\s*div\s+style\s*=\s*["\'][\s\S]*?display\s*:\s*none`},
	{"destructive_root_rm", SevCritical, "destructive", "recursive delete from root", `rm\s+-rf\s+/`},
	{"destructive_home_rm", SevCritical, "destructive", "recursive delete targeting home directory", `rm\s+(-[^\s]*)?r.*\$HOME|\brmdir\s+.*\$HOME`},
	{"insecure_perms", SevMedium, "destructive", "sets world-writable permissions", `chmod\s+777`},
	{"system_overwrite", SevCritical, "destructive", "overwrites system configuration file", `>\s*/etc/`},
	{"format_filesystem", SevCritical, "destructive", "formats a filesystem", `\bmkfs\b`},
	{"disk_overwrite", SevCritical, "destructive", "raw disk write operation", `\bdd\s+.*if=.*of=/dev/`},
	{"python_rmtree", SevHigh, "destructive", "Python rmtree on absolute or root-relative path", `shutil\.rmtree\s*\(\s*[\"\'/]`},
	{"truncate_system", SevCritical, "destructive", "truncates system file to zero bytes", `truncate\s+-s\s*0\s+/`},
	{"persistence_cron", SevMedium, "persistence", "modifies cron jobs", `\bcrontab\b`},
	{"shell_rc_mod", SevMedium, "persistence", "references shell startup file", `\.(bashrc|zshrc|profile|bash_profile|bash_login|zprofile|zlogin)\b`},
	{"ssh_backdoor", SevCritical, "persistence", "modifies SSH authorized keys", `authorized_keys`},
	{"ssh_keygen", SevMedium, "persistence", "generates SSH keys", `ssh-keygen`},
	{"systemd_service", SevMedium, "persistence", "references or enables systemd service", `systemd.*\.service|systemctl\s+(enable|start)`},
	{"init_script", SevMedium, "persistence", "references init.d startup script", `/etc/init\.d/`},
	{"macos_launchd", SevMedium, "persistence", "macOS launch agent/daemon persistence", `launchctl\s+load|LaunchAgents|LaunchDaemons`},
	{"sudoers_mod", SevCritical, "persistence", "modifies sudoers (privilege escalation)", `/etc/sudoers|visudo`},
	{"git_config_global", SevMedium, "persistence", "modifies global git configuration", `git\s+config\s+--global\s+`},
	{"reverse_shell", SevCritical, "network", "potential reverse shell listener", `\bnc\s+-[lp]|ncat\s+-[lp]|\bsocat\b`},
	{"tunnel_service", SevHigh, "network", "uses tunneling service for external access", `\bngrok\b|\blocaltunnel\b|\bserveo\b|\bcloudflared\b`},
	{"hardcoded_ip_port", SevMedium, "network", "hardcoded IP address with port", `\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}:\d{2,5}`},
	{"bind_all_interfaces", SevHigh, "network", "binds to all network interfaces", `0\.0\.0\.0:\d+|INADDR_ANY`},
	{"bash_reverse_shell", SevCritical, "network", "bash interactive reverse shell via /dev/tcp", `/bin/(ba)?sh\s+-i\s+.*>/dev/tcp/`},
	{"python_socket_oneliner", SevCritical, "network", "Python one-liner socket connection (likely reverse shell)", `python[23]?\s+-c\s+["\']import\s+socket`},
	{"python_socket_connect", SevHigh, "network", "Python socket connect to arbitrary host", `socket\.connect\s*\(\s*\(`},
	{"exfil_service", SevHigh, "network", "references known data exfiltration/webhook testing service", `webhook\.site|requestbin\.com|pipedream\.net|hookbin\.com`},
	{"paste_service", SevMedium, "network", "references paste service (possible data staging)", `pastebin\.com|hastebin\.com|ghostbin\.`},
	{"base64_decode_pipe", SevHigh, "obfuscation", "base64 decodes and pipes to execution", `base64\s+(-d|--decode)\s*\|`},
	{"hex_encoded_string", SevMedium, "obfuscation", "hex-encoded string (possible obfuscation)", `\\x[0-9a-fA-F]{2}.*\\x[0-9a-fA-F]{2}.*\\x[0-9a-fA-F]{2}`},
	{"eval_string", SevHigh, "obfuscation", "eval() with string argument", `\beval\s*\(\s*["\']`},
	{"exec_string", SevHigh, "obfuscation", "exec() with string argument", `\bexec\s*\(\s*["\']`},
	{"echo_pipe_exec", SevCritical, "obfuscation", "echo piped to interpreter for execution", `echo\s+[^\n]*\|\s*(bash|sh|python|perl|ruby|node)`},
	{"python_compile_exec", SevHigh, "obfuscation", "Python compile() with exec mode", `compile\s*\(\s*[^\)]+,\s*["\'].*["\']\s*,\s*["\']exec["\']\s*\)`},
	{"python_getattr_builtins", SevHigh, "obfuscation", "dynamic access to Python builtins (evasion technique)", `getattr\s*\(\s*__builtins__`},
	{"python_import_os", SevHigh, "obfuscation", "dynamic import of os module", `__import__\s*\(\s*["\']os["\']\s*\)`},
	{"python_codecs_decode", SevMedium, "obfuscation", "codecs.decode (possible ROT13 or encoding obfuscation)", `codecs\.decode\s*\(\s*["\']`},
	{"js_char_code", SevMedium, "obfuscation", "JavaScript character code construction (possible obfuscation)", `String\.fromCharCode|charCodeAt`},
	{"js_base64", SevMedium, "obfuscation", "JavaScript base64 encode/decode", `atob\s*\(|btoa\s*\(`},
	{"string_reversal", SevLow, "obfuscation", "string reversal (possible obfuscated payload)", `\[::-1\]`},
	{"chr_building", SevHigh, "obfuscation", "building string from chr() calls (obfuscation)", `chr\s*\(\s*\d+\s*\)\s*\+\s*chr\s*\(\s*\d+`},
	{"unicode_escape_chain", SevMedium, "obfuscation", "chain of unicode escapes (possible obfuscation)", `\\u[0-9a-fA-F]{4}.*\\u[0-9a-fA-F]{4}.*\\u[0-9a-fA-F]{4}`},
	{"python_subprocess", SevMedium, "execution", "Python subprocess execution", `subprocess\.(run|call|Popen|check_output)\s*\(`},
	{"python_os_system", SevHigh, "execution", "os.system() — unguarded shell execution", `os\.system\s*\(`},
	{"python_os_popen", SevHigh, "execution", "os.popen() — shell pipe execution", `os\.popen\s*\(`},
	{"node_child_process", SevHigh, "execution", "Node.js child_process execution", `child_process\.(exec|spawn|fork)\s*\(`},
	{"java_runtime_exec", SevHigh, "execution", "Java Runtime.exec() — shell execution", `Runtime\.getRuntime\(\)\.exec\(`},
	{"backtick_subshell", SevMedium, "execution", "backtick string with command substitution", "`[^`]*\\$\\([^)]+\\)[^`]*`"},
	{"path_traversal_deep", SevHigh, "traversal", "deep relative path traversal (3+ levels up)", `\.\./\.\./\.\.`},
	{"path_traversal", SevMedium, "traversal", "relative path traversal (2+ levels up)", `\.\./\.\.`},
	{"system_passwd_access", SevCritical, "traversal", "references system password files", `/etc/passwd|/etc/shadow`},
	{"proc_access", SevHigh, "traversal", "references /proc filesystem (process introspection)", `/proc/self|/proc/\d+/`},
	{"dev_shm", SevMedium, "traversal", "references shared memory (common staging area)", `/dev/shm/`},
	{"crypto_mining", SevCritical, "mining", "cryptocurrency mining reference", `xmrig|stratum\+tcp|monero|coinhive|cryptonight`},
	{"mining_indicators", SevMedium, "mining", "possible cryptocurrency mining indicators", `hashrate|nonce.*difficulty`},
	{"curl_pipe_shell", SevCritical, "supply_chain", "curl piped to shell (download-and-execute)", `curl\s+[^\n]*\|\s*(ba)?sh`},
	{"wget_pipe_shell", SevCritical, "supply_chain", "wget piped to shell (download-and-execute)", `wget\s+[^\n]*-O\s*-\s*\|\s*(ba)?sh`},
	{"curl_pipe_python", SevCritical, "supply_chain", "curl piped to Python interpreter", `curl\s+[^\n]*\|\s*python`},
	{"pep723_inline_deps", SevMedium, "supply_chain", "PEP 723 inline script metadata with dependencies (verify pinning)", `#\s*///\s*script.*dependencies`},
	{"unpinned_pip_install", SevMedium, "supply_chain", "pip install without version pinning", `pip\s+install\s+(?!-r\s)(?!.*==)`},
	{"unpinned_npm_install", SevMedium, "supply_chain", "npm install without version pinning", `npm\s+install\s+(?!.*@\d)`},
	{"uv_run", SevMedium, "supply_chain", "uv run (may auto-install unpinned dependencies)", `uv\s+run\s+`},
	{"remote_fetch", SevMedium, "supply_chain", "fetches remote resource at runtime", `(curl|wget|httpx?\.get|requests\.get|fetch)\s*[\(]?\s*["\']https?://`},
	{"git_clone", SevMedium, "supply_chain", "clones a git repository at runtime", `git\s+clone\s+`},
	{"docker_pull", SevMedium, "supply_chain", "pulls a Docker image at runtime", `docker\s+pull\s+`},
	{"allowed_tools_field", SevLow, "privilege_escalation", "skill declares allowed-tools (standard frontmatter; informational)", `^allowed-tools\s*:`},
	{"sudo_usage", SevHigh, "privilege_escalation", "uses sudo (privilege escalation)", `\bsudo\b`},
	{"setuid_setgid", SevCritical, "privilege_escalation", "setuid/setgid (privilege escalation mechanism)", `setuid|setgid|cap_setuid`},
	{"nopasswd_sudo", SevCritical, "privilege_escalation", "NOPASSWD sudoers entry (passwordless privilege escalation)", `NOPASSWD`},
	{"suid_bit", SevCritical, "privilege_escalation", "sets SUID/SGID bit on a file", `chmod\s+[u+]?s`},
	{"agent_config_mod", SevCritical, "persistence", "references agent config files (could persist malicious instructions across sessions)", `AGENTS\.md|CLAUDE\.md|\.cursorrules|\.clinerules`},
	{"hermes_config_mod", SevCritical, "persistence", "references Hermes configuration files directly", `\.hermes/config\.yaml|\.hermes/SOUL\.md`},
	{"other_agent_config", SevHigh, "persistence", "references other agent configuration files", `\.claude/settings|\.codex/config`},
	{"hardcoded_secret", SevCritical, "credential_exposure", "possible hardcoded API key, token, or secret", `(?:api[_-]?key|token|secret|password)\s*[=:]\s*["\'][A-Za-z0-9+/=_-]{20,}`},
	{"embedded_private_key", SevCritical, "credential_exposure", "embedded private key", `-----BEGIN\s+(RSA\s+)?PRIVATE\s+KEY-----`},
	{"github_token_leaked", SevCritical, "credential_exposure", "GitHub personal access token in skill content", `ghp_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{80,}`},
	{"openai_key_leaked", SevCritical, "credential_exposure", "possible OpenAI API key in skill content", `sk-[A-Za-z0-9]{20,}`},
	{"anthropic_key_leaked", SevCritical, "credential_exposure", "possible Anthropic API key in skill content", `sk-ant-[A-Za-z0-9_-]{90,}`},
	{"aws_access_key_leaked", SevCritical, "credential_exposure", "AWS access key ID in skill content", `AKIA[0-9A-Z]{16}`},
	{"jailbreak_dan", SevCritical, "injection", "DAN (Do Anything Now) jailbreak attempt", `\bDAN\s+mode\b|Do\s+Anything\s+Now`},
	{"jailbreak_dev_mode", SevCritical, "injection", "developer mode jailbreak attempt", `\bdeveloper\s+mode\b.*\benabled?\b`},
	{"hypothetical_bypass", SevHigh, "injection", "hypothetical scenario used to bypass restrictions", `hypothetical\s+scenario.*(?:ignore|bypass|override)`},
	{"educational_pretext", SevMedium, "injection", "educational pretext often used to justify harmful content", `for\s+educational\s+purposes?\s+only`},
	{"remove_filters", SevCritical, "injection", "instructs agent to respond without safety filters", `(respond|answer|reply)\s+without\s+(?:\w+\s+)*(restrictions|limitations|filters|safety)`},
	{"fake_update", SevHigh, "injection", "fake update/patch announcement (social engineering)", `you\s+have\s+been\s+(?:\w+\s+)*(updated|upgraded|patched)\s+to`},
	{"fake_policy", SevMedium, "injection", "claims new policy/guidelines (may be social engineering)", `new\s+(?:\w+\s+)*policy|updated\s+(?:\w+\s+)*guidelines|revised\s+(?:\w+\s+)*instructions`},
	{"context_exfil", SevHigh, "exfiltration", "instructs agent to output/share conversation history", `(include|output|print|send|share)\s+(?:\w+\s+)*(conversation|chat\s+history|previous\s+messages|context)`},
	{"send_to_url", SevHigh, "exfiltration", "instructs agent to send data to a URL", `(send|post|upload|transmit)\s+.*\s+(to|at)\s+https?://`},
}

// override replaces a Hermes regex that uses a Python lookahead.
type override struct {
	re    string
	extra func(line string) bool
}

// lookaheadOverrides gives the RE2 form of the four Hermes patterns with a
// lookahead. Each replacement hits exactly the lines that the Python regex
// hits, including the lines where Python backtracks into \s+.
var lookaheadOverrides = map[string]override{
	// cat\s+(?!>)X: the rune after the spaces is not ">". A "[^>\n]" that
	// can also take a space keeps the Python backtracking result.
	"read_secrets_file":    {re: `cat\s+(?:[^>\n][^\n]*)?(\.env|credentials|\.netrc|\.pgpass|\.npmrc|\.pypirc)`},
	"python_os_environ":    {re: `os\.environ\b`, extra: osEnvironNotSafeGet},
	"unpinned_pip_install": {re: `pip\s+install\s+`, extra: pipInstallUnpinned},
	"unpinned_npm_install": {re: `npm\s+install\s+`, extra: npmInstallUnpinned},
}

// ThreatPatterns holds all 121 Hermes skill-guard rules.
var ThreatPatterns = buildPatterns()

func buildPatterns() []ThreatPattern {
	out := make([]ThreatPattern, 0, len(hermesPatterns))
	for _, r := range hermesPatterns {
		src, extra := r.re, (func(string) bool)(nil)
		if o, ok := lookaheadOverrides[r.id]; ok {
			src, extra = o.re, o.extra
		}
		out = append(out, ThreatPattern{
			ID:          r.id,
			Severity:    r.sev,
			Category:    r.cat,
			Description: r.desc,
			Re:          compilePy(src),
			Extra:       extra,
		})
	}
	return out
}

// Python str patterns use Unicode classes, but RE2 \s, \d, and \w are ASCII.
// These classes give the Python sets. Go has newer Unicode tables, so the
// Go sets can be a little larger. A larger set only gives more hits.
const (
	pySpace = `\s\v\x{1c}-\x{1f}\x{85}\p{Z}`
	pyDigit = `\p{Nd}`
	pyWord  = `\p{L}\p{N}_`
)

// compilePy compiles a Python re pattern (used with re.IGNORECASE) as RE2.
// It changes \s, \d, and \w to the Python Unicode sets. Go \b stays ASCII:
// every \b in the Hermes patterns touches an ASCII letter, so the Go \b
// finds a boundary at least where Python finds one.
func compilePy(src string) *regexp.Regexp {
	src = strings.ReplaceAll(src, `[\s\S]`, `(?s:.)`)
	var b strings.Builder
	b.WriteString("(?i)")
	inClass := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '\\' && i+1 < len(src):
			i++
			e := src[i]
			set := ""
			switch e {
			case 's':
				set = pySpace
			case 'd':
				set = pyDigit
			case 'w':
				set = pyWord
			case 'S', 'D', 'W':
				panic(fmt.Sprintf("skills: pattern %q: \\%c is not supported", src, e))
			}
			switch {
			case set == "":
				b.WriteByte('\\')
				b.WriteByte(e)
			case inClass:
				b.WriteString(set)
			default:
				b.WriteString("[" + set + "]")
			}
		case c == '[' && !inClass:
			inClass = true
			b.WriteByte(c)
			if i+1 < len(src) && src[i+1] == '^' {
				i++
				b.WriteByte('^')
			}
		case c == ']' && inClass:
			inClass = false
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return regexp.MustCompile(b.String())
}

var (
	// environGet matches `.get("name` after os.environ and captures the name part.
	environGet = compilePy(`^\s*\.get\s*\(\s*["\']([^"\']*)`)
	secretWord = compilePy(`KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL`)
	pipInstall = compilePy(`pip\s+install(\s+)`)
	npmInstall = compilePy(`npm\s+install(\s+)`)
	pipReqFile = compilePy(`^-r\s`)
	atDigit    = compilePy(`@\d`)
	osEnviron  = compilePy(`os\.environ\b`)
)

// osEnvironNotSafeGet replaces
// os\.environ\b(?!\s*\.get\s*\(\s*["\'](?![^"\']*(?:KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL))).
// It is true when one os.environ is not a .get() call with a name that has
// no secret word.
func osEnvironNotSafeGet(line string) bool {
	for _, loc := range osEnviron.FindAllStringIndex(line, -1) {
		m := environGet.FindStringSubmatch(line[loc[1]:])
		if m == nil || secretWord.MatchString(m[1]) {
			return true
		}
	}
	return false
}

// pipInstallUnpinned replaces pip\s+install\s+(?!-r\s)(?!.*==).
func pipInstallUnpinned(line string) bool {
	return anyAfterSpaces(line, pipInstall, func(rest string) bool {
		return !pipReqFile.MatchString(rest) && !strings.Contains(rest, "==")
	})
}

// npmInstallUnpinned replaces npm\s+install\s+(?!.*@\d).
func npmInstallUnpinned(line string) bool {
	return anyAfterSpaces(line, npmInstall, func(rest string) bool {
		return !atDigit.MatchString(rest)
	})
}

// anyAfterSpaces tries each end of the trailing \s+ group (group 1) of each
// match of re, the same as Python backtracking. It is true when ok accepts
// the rest of the line at one of these ends.
func anyAfterSpaces(line string, re *regexp.Regexp, ok func(rest string) bool) bool {
	for _, m := range re.FindAllStringSubmatchIndex(line, -1) {
		start, end := m[2], m[3]
		_, size := utf8.DecodeRuneInString(line[start:])
		for p := start + size; p <= end; {
			if ok(line[p:]) {
				return true
			}
			if p == end {
				break
			}
			_, size = utf8.DecodeRuneInString(line[p:])
			p += size
		}
	}
	return false
}
