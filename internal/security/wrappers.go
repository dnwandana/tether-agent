package security

import (
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// WrapperKind is the kind of a program that runs another command.
type WrapperKind int

const (
	WrapTransparent WrapperKind = iota + 1 // runs the command as the same user on the same host
	WrapPrivilege                          // runs the command as another user
	WrapHost                               // runs the command on another host or in a container
)

// wrapForm tells how the reader finds the inner command of a wrapper.
type wrapForm int

const (
	formArgv   wrapForm = iota // the words after the options run as a program
	formEnv                    // NAME=value words, then a program
	formShell                  // a shell: the -c string is a new parse
	formEval                   // the joined words are a new parse
	formSu                     // su and runuser: the -c string, or a program
	formSSH                    // a host, then the joined words are a new parse
	formExec                   // docker, podman, kubectl: exec, a target, then a program
	formChroot                 // a folder, then a program
	formFlock                  // a lock file, then a program or -c and a string
	formScript                 // a log file; the -c string is a new parse
	formWatch                  // the joined words are a new parse; with -x, a program
)

// wrapSpec is one row of the wrapper table.
//
// For a shell, val holds the short options that take the next word, also inside a cluster.
type wrapSpec struct {
	kind  WrapperKind
	form  wrapForm
	val   string // short options that take a value
	opt   string // short options that take a value only when it is attached, such as -i{}
	flag  string // short options that take no value
	lval  string // long options that take a value, without the leading dashes
	lopt  string // long options that take a value only after "=", such as --replace=R
	lflag string // long options that take no value; "*" accepts each long option
	skip  int    // the words between the options and the program
	num   bool   // a word such as -5 is an option (the old form of nice)
	cstr  bool   // a shell whose -c option takes a value (fish, csh)
	env   bool   // NAME=value words can come before the program (sudo)
	// report lists options that make the wrapper only report, so no command runs.
	report []string
	// setEnv lists options that change the environment of the program.
	setEnv []string
}

var wrapperTable = buildWrapperTable()

func buildWrapperTable() map[string]*wrapSpec {
	t := map[string]*wrapSpec{}
	put := func(names string, s wrapSpec) {
		for n := range strings.FieldsSeq(names) {
			c := s
			t[n] = &c
		}
	}
	// The help and version options of the GNU and util-linux tools only report.
	hv := []string{"-h", "-V", "--help", "--version"}
	tr := WrapTransparent
	put("nice", wrapSpec{kind: tr, val: "n", lval: "adjustment", lflag: "help version", num: true, report: hv})
	put("nohup", wrapSpec{kind: tr, lflag: "help version", report: hv})
	put("builtin", wrapSpec{kind: tr})
	put("busybox", wrapSpec{kind: tr, lflag: "list list-full install help",
		report: []string{"--list", "--list-full", "--install", "--help"}})
	put("timeout", wrapSpec{kind: tr, val: "ks", flag: "fpv", lval: "signal kill-after",
		lflag: "foreground preserve-status verbose help version", skip: 1, report: hv})
	put("stdbuf", wrapSpec{kind: tr, val: "ioe", lval: "input output error", lflag: "help version", report: hv})
	put("time", wrapSpec{kind: tr, val: "fo", flag: "pvaqV", lval: "format output",
		lflag: "portability verbose append quiet help version", report: []string{"-V", "--help", "--version"}})
	put("exec", wrapSpec{kind: tr, val: "a", flag: "cl"})
	put("command", wrapSpec{kind: tr, flag: "pvV", report: []string{"-v", "-V"}})
	put("setsid", wrapSpec{kind: tr, flag: "cfwhV", lflag: "ctty fork wait help version", report: hv})
	put("ionice", wrapSpec{kind: tr, val: "cnpPu", flag: "thV", lval: "class classdata pid pgid uid",
		lflag: "ignore help version", report: append([]string{"-p", "-P", "-u", "--pid", "--pgid", "--uid"}, hv...)})
	// The -c option of taskset only changes how it reads the mask, so the mask word stays.
	put("taskset", wrapSpec{kind: tr, flag: "acphV", lflag: "all-tasks cpu-list pid help version",
		skip: 1, report: append([]string{"-p", "--pid"}, hv...)})
	put("chrt", wrapSpec{kind: tr, val: "TPD", flag: "abdefimoprRvhV", lval: "sched-runtime sched-period sched-deadline",
		lflag: "all-tasks batch deadline ext fifo idle max other pid reset-on-fork rr verbose help version",
		skip:  1, report: append([]string{"-p", "-m", "--pid", "--max"}, hv...)})
	put("flock", wrapSpec{kind: tr, form: formFlock, val: "wE", flag: "sxenuoFhV", lval: "timeout wait conflict-exit-code",
		lflag: "shared exclusive unlock nonblock nb close no-fork verbose help version", report: hv})
	put("xargs", wrapSpec{kind: tr, val: "adEILnPs", opt: "eil", flag: "0rtpxo",
		lval:   "arg-file delimiter max-args max-procs max-chars process-slot-var",
		lopt:   "eof replace max-lines",
		lflag:  "null no-run-if-empty verbose interactive exit open-tty show-limits help version",
		report: []string{"--help", "--version"}})
	put("watch", wrapSpec{kind: tr, form: formWatch, val: "nq", opt: "d", flag: "bcCegprtwxhv",
		lval: "interval equexit", lopt: "differences",
		lflag:  "beep color no-color errexit chgexit precise no-rerun no-title no-wrap exec help version",
		report: []string{"-h", "-v", "--help", "--version"}})
	put("strace", wrapSpec{kind: tr, val: "abeEIoOpPsSuUX", flag: "AcCdDfFhikqnrtTvVwxyYzZ",
		lval: "env attach user argv0 detach-on interruptible trace signal status trace-fds trace-path columns " +
			"abbrev verbose raw read write kvm decode-pids output string-limit const-print-style summary-sort-by " +
			"summary-columns summary-syscall-overhead syscall-limit inject fault stack-trace-frame-limit",
		lopt: "daemonize stack-trace relative-timestamps absolute-timestamps syscall-times strings-in-hex " +
			"decode-fds quiet tips",
		lflag: "follow-forks successful-only failed-only kill-on-exit instruction-pointer syscall-number " +
			"output-append-mode output-separately no-abbrev summary-only summary summary-wall-clock debug " +
			"seccomp-bpf help version",
		report: hv, setEnv: []string{"-E", "--env"}})
	put("unshare", wrapSpec{kind: tr, val: "RwSG", flag: "fmuinpUCTrchV",
		lval: "map-user map-group map-users map-groups propagation setgroups root wd setuid setgid " +
			"monotonic boottime",
		lopt:  "mount uts ipc net pid user cgroup time kill-child mount-proc",
		lflag: "fork map-root-user map-current-user map-auto keep-caps help version", report: hv})
	// The table holds the options of util-linux script and of BSD script.
	put("script", wrapSpec{kind: tr, form: formScript, val: "cEIOBTmot", flag: "adefFkpqrhV",
		lval:  "command echo log-in log-out log-io log-timing logging-format output-limit",
		lopt:  "timing",
		lflag: "append return flush force quiet help version", report: append([]string{"-p"}, hv...)})
	put("env", wrapSpec{kind: tr, form: formEnv, val: "uCSa", flag: "i0v-", lval: "unset chdir split-string argv0",
		lopt:   "block-signal default-signal ignore-signal",
		lflag:  "ignore-environment null debug list-signal-handling help version",
		report: []string{"--help", "--version"}})
	put("eval", wrapSpec{kind: tr, form: formEval})
	// The shells. A posix shell reads -c as a flag and runs the first word that is not an
	// option.
	posixLong := "debug debugger dump-po-strings dump-strings help login noediting noprofile norc posix " +
		"pretty-print protected restricted verbose version wordexp"
	put("sh bash dash ash", wrapSpec{kind: tr, form: formShell, val: "oO", flag: "abcefhiklmnprstuvxBCDEHIPTVq",
		lval: "rcfile init-file", lflag: posixLong})
	put("ksh", wrapSpec{kind: tr, form: formShell, val: "oOR", flag: "abcefhiklmnprstuvxBCDEHIPTV",
		lval: "rcfile init-file", lflag: posixLong})
	put("zsh", wrapSpec{kind: tr, form: formShell, val: "o",
		flag: "0123456789abcdefghijklmnpqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", lval: "emulate", lflag: "*"})
	put("fish", wrapSpec{kind: tr, form: formShell, cstr: true, val: "cCpdofD", flag: "hilNnPv",
		lval: "command init-command profile profile-startup debug debug-output features debug-stack-frames",
		lflag: "help interactive login no-config no-execute private version print-rusage-self " +
			"print-debug-categories"})
	put("csh tcsh", wrapSpec{kind: tr, form: formShell, cstr: true, val: "c", flag: "bdefFilmnqstvVxX"})

	pr := WrapPrivilege
	put("sudo", wrapSpec{kind: pr, val: "ughpCDRrtTUa", opt: "h", flag: "AbBeEHiklnNPSsVvK",
		lval: "user group host prompt chdir chroot role type command-timeout other-user close-from " +
			"login-class auth-type",
		lopt: "preserve-env",
		lflag: "login shell non-interactive no-update stdin askpass bell set-home list edit preserve-groups " +
			"reset-timestamp remove-timestamp validate version help background",
		env: true,
		report: []string{"-l", "--list", "-e", "--edit", "-v", "--validate", "-V", "--version",
			"-K", "--remove-timestamp", "--help"}})
	put("doas", wrapSpec{kind: pr, val: "uC", flag: "nsL", report: []string{"-C", "-L"}})
	put("pkexec", wrapSpec{kind: pr, lval: "user", lflag: "disable-internal-agent keep-cwd version help",
		report: []string{"--version", "--help"}})
	put("su", wrapSpec{kind: pr, form: formSu, val: "csgGw", flag: "lmpfP-hV",
		lval:  "command session-command shell group supp-group whitelist-environment",
		lflag: "login preserve-environment fast pty help version", report: hv})
	put("runuser", wrapSpec{kind: pr, form: formSu, val: "ugGcsw", flag: "lmpfP-hV",
		lval:  "user group supp-group command session-command shell whitelist-environment",
		lflag: "login preserve-environment fast pty help version", report: hv})

	ho := WrapHost
	put("ssh", wrapSpec{kind: ho, form: formSSH, val: "bcDEeFIiJLlmOopPQRSWwB",
		flag: "46AaCfGgKkMNnqsTtVvXxYy", report: []string{"-G", "-V", "-Q"}})
	put("docker", wrapSpec{kind: ho, form: formExec, val: "cHleuw", flag: "Ddit",
		lval:   "config context host log-level tlscacert tlscert tlskey env env-file user workdir detach-keys",
		lflag:  "debug tls tlsverify help version detach interactive privileged tty",
		report: []string{"-v", "--version", "--help"}})
	put("podman", wrapSpec{kind: ho, form: formExec, val: "ceuw", flag: "rdit",
		lval: "cgroup-manager conmon connection events-backend hooks-dir identity imagestore log-level module " +
			"network-cmd-path network-config-dir out root runroot runtime runtime-flag ssh storage-driver " +
			"storage-opt tmpdir url volumepath env env-file user workdir detach-keys preserve-fd preserve-fds",
		lflag:  "remote syslog transient-store help version detach interactive privileged tty",
		report: []string{"-v", "--version", "--help"}})
	put("kubectl", wrapSpec{kind: ho, form: formExec, val: "nscfv", flag: "itq",
		lval: "namespace context kubeconfig cluster user token server as as-group as-uid certificate-authority " +
			"client-certificate client-key request-timeout tls-server-name cache-dir container filename " +
			"pod-running-timeout v vmodule log-flush-frequency profile profile-output username password",
		lflag:  "stdin tty quiet insecure-skip-tls-verify match-server-version warnings-as-errors disable-compression help",
		report: []string{"--help"}})
	put("chroot", wrapSpec{kind: ho, form: formChroot, lval: "userspec groups", lflag: "skip-chdir help version",
		report: []string{"--help", "--version"}})
	put("nsenter", wrapSpec{kind: ho, val: "tW", opt: "mnuipCUTSGrw", flag: "aceFZhV",
		lval:  "target wdns",
		lopt:  "mount uts ipc net pid cgroup user time setuid setgid root wd",
		lflag: "all env no-fork follow-context preserve-credentials join-cgroup help version", report: hv})
	return t
}

// LookupWrapper returns the kind of the wrapper with this program name.
func LookupWrapper(name string) (WrapperKind, bool) {
	s, ok := wrapperTable[name]
	if !ok {
		return 0, false
	}
	return s.kind, true
}

// ViaKey returns the sorted, unique Via entries of all commands, joined with a comma.
func (p Parsed) ViaKey() string {
	var all []string
	for _, c := range p.Cmds {
		all = append(all, c.Via...)
	}
	sort.Strings(all)
	return strings.Join(slices.Compact(all), ",")
}

// optVal is one option that scanOpts found.
type optVal struct {
	name string // "-c" or "--command"; "--" for the end of the options
	val  string
	lit  bool
}

func hasWord(list, w string) bool { return slices.Contains(strings.Fields(list), w) }

// unknownOpt records that a wrapper has an option that the table does not know. The reader
// cannot tell which words the option takes.
func (r *reader) unknownOpt(name, opt string) {
	r.fail("the command has an option that the gate does not know: " + name + " " + opt)
}

// isOpt reports whether the word w is an option of the wrapper.
func isOpt(s *wrapSpec, w string) bool {
	if w == "-" {
		return strings.Contains(s.flag, "-")
	}
	return len(w) > 1 && w[0] == '-'
}

// isNumOpt reports whether w is an option such as -5, --5, or -+5.
func isNumOpt(w string) bool {
	d := strings.TrimLeft(strings.TrimPrefix(w, "-"), "-+")
	if len(w) < 2 || w[0] != '-' || d == "" || len(w)-len(d) > 2 {
		return false
	}
	return strings.Trim(d, "0123456789") == ""
}

// option reads the option at ws[n] and adds it to got. It returns the index of the next word.
// ok is false when the wrapper has an option that the table does not know.
func (r *reader) option(s *wrapSpec, name string, ws []string, ls []bool, n int, got []optVal) ([]optVal, int, bool) {
	tok := ws[n]
	switch {
	case s.num && isNumOpt(tok):
		return append(got, optVal{name: "-N", val: tok, lit: ls[n]}), n + 1, true
	case tok == "-":
		return append(got, optVal{name: "-"}), n + 1, true
	case strings.HasPrefix(tok, "--"):
		long, val, hasVal := strings.Cut(tok[2:], "=")
		lit := ls[n]
		switch {
		case hasWord(s.lval, long):
			if !hasVal && n+1 < len(ws) {
				val, lit = ws[n+1], ls[n+1]
				n++
			}
		case hasWord(s.lopt, long), hasWord(s.lflag, long), s.lflag == "*":
		default:
			r.unknownOpt(name, "--"+long)
			return got, n, false
		}
		return append(got, optVal{name: "--" + long, val: val, lit: lit}), n + 1, true
	}
	for j := 1; j < len(tok); j++ {
		c := tok[j]
		switch {
		case strings.IndexByte(s.val, c) >= 0:
			v, lit := tok[j+1:], ls[n]
			if v == "" && n+1 < len(ws) {
				v, lit = ws[n+1], ls[n+1]
				n++
			}
			got = append(got, optVal{name: "-" + string(c), val: v, lit: lit})
			j = len(tok)
		case strings.IndexByte(s.opt, c) >= 0:
			got = append(got, optVal{name: "-" + string(c), val: tok[j+1:], lit: ls[n]})
			j = len(tok)
		case strings.IndexByte(s.flag, c) >= 0:
			got = append(got, optVal{name: "-" + string(c)})
		default:
			r.unknownOpt(name, "-"+string(c))
			return got, n, false
		}
	}
	return got, n + 1, true
}

// scan reads the options at the start of ws. It returns the options it found and the index of
// the first word that is not an option. An end mark "--" adds the option "--". ok is false
// when the wrapper has an option that the table does not know.
func (r *reader) scan(s *wrapSpec, name string, ws []string, ls []bool) (got []optVal, n int, ok bool) {
	for n < len(ws) {
		switch {
		case ws[n] == "--":
			return append(got, optVal{name: "--"}), n + 1, true
		case !isOpt(s, ws[n]):
			return got, n, true
		}
		if got, n, ok = r.option(s, name, ws, ls, n, got); !ok {
			return got, n, false
		}
	}
	return got, n, true
}

// scanAll reads the options in all of ws, as GNU getopt does when options can come after the
// other words. It returns the options and the other words, in order.
func (r *reader) scanAll(s *wrapSpec, name string, ws []string, ls []bool) (got []optVal, pos []string, pl []bool, ok bool) {
	n := 0
	for n < len(ws) {
		switch {
		case ws[n] == "--":
			got = append(got, optVal{name: "--"})
			return got, append(pos, ws[n+1:]...), append(pl, ls[n+1:]...), true
		case !isOpt(s, ws[n]):
			pos, pl = append(pos, ws[n]), append(pl, ls[n])
			n++
			continue
		}
		if got, n, ok = r.option(s, name, ws, ls, n, got); !ok {
			return got, pos, pl, false
		}
	}
	return got, pos, pl, true
}

func findOpt(got []optVal, names ...string) (optVal, bool) {
	for _, o := range got {
		if slices.Contains(names, o.name) {
			return o, true
		}
	}
	return optVal{}, false
}

// reports reports whether an option of got makes the wrapper only report.
func (s *wrapSpec) reports(got []optVal) bool {
	return slices.ContainsFunc(got, func(o optVal) bool { return slices.Contains(s.report, o.name) })
}

// unwrap adds the command that a wrapper runs, when c is a wrapper.
func (r *reader) unwrap(c SimpleCmd, lit []bool, wc walkCtx) {
	name := filepath.Base(c.Argv[0])
	s := wrapperTable[name]
	if s == nil {
		return
	}
	if wc.chain+1 > MaxWrapperChain {
		r.fail("the command has more than 16 wrappers")
		return
	}
	wc.chain++
	wc.redirs = nil
	ws, ls := c.Argv[1:], lit[1:]
	if s.kind == WrapPrivilege || name == "nsenter" {
		wc.via = append(slices.Clone(wc.via), name)
	}
	switch s.form {
	case formArgv:
		got, n, ok := r.scan(s, name, ws, ls)
		if !ok || s.reports(got) {
			return
		}
		n = min(n+s.skip, len(ws))
		assigned := len(s.setEnv) > 0 && slices.ContainsFunc(got, func(o optVal) bool {
			return slices.Contains(s.setEnv, o.name)
		})
		ws, ls = ws[n:], ls[n:]
		for s.env && len(ws) > 0 && isAssignWord(ws[0]) {
			assigned = true
			ws, ls = ws[1:], ls[1:]
		}
		r.runArgv(ws, ls, assigned, wc)
	case formEnv:
		r.unwrapEnv(s, name, ws, ls, wc)
	case formShell:
		r.unwrapShell(s, name, ws, ls, wc)
	case formEval:
		if len(ws) > 0 {
			r.runString(strings.Join(ws, " "), wc)
		}
	case formSu:
		r.unwrapSu(s, name, ws, ls, wc)
	case formSSH:
		r.unwrapSSH(s, name, ws, ls, wc)
	case formExec:
		r.unwrapExec(s, name, ws, ls, wc)
	case formChroot:
		got, n, ok := r.scan(s, name, ws, ls)
		if !ok || s.reports(got) || n >= len(ws) {
			return
		}
		wc.via = append(slices.Clone(wc.via), "chroot "+ws[n])
		r.runArgv(ws[n+1:], ls[n+1:], false, wc)
	case formFlock:
		// util-linux flock reads -c only after the lock file.
		got, n, ok := r.scan(s, name, ws, ls)
		if !ok || s.reports(got) || n >= len(ws) {
			return
		}
		ws, ls = ws[n+1:], ls[n+1:]
		if len(ws) > 0 && (ws[0] == "-c" || ws[0] == "--command") {
			if len(ws) > 1 {
				r.runString(ws[1], wc)
			}
			return
		}
		r.runArgv(ws, ls, false, wc)
	case formScript:
		// util-linux script reads options after the log file. BSD script runs the words after
		// the log file as a program.
		got, pos, pl, ok := r.scanAll(s, name, ws, ls)
		if !ok || s.reports(got) {
			return
		}
		if o, found := findOpt(got, "-c", "--command"); found {
			r.runString(o.val, wc)
			return
		}
		if len(pos) > 1 {
			r.runArgv(pos[1:], pl[1:], false, wc)
		}
	case formWatch:
		// watch gives the joined words to "sh -c". With -x, it runs them as a program.
		got, n, ok := r.scan(s, name, ws, ls)
		if !ok || s.reports(got) || n >= len(ws) {
			return
		}
		if _, found := findOpt(got, "-x", "--exec"); found {
			r.runArgv(ws[n:], ls[n:], false, wc)
			return
		}
		r.runString(strings.Join(ws[n:], " "), wc)
	}
}

// runArgv adds the command that a wrapper runs as a program.
func (r *reader) runArgv(ws []string, ls []bool, assigned bool, wc walkCtx) {
	if len(ws) == 0 {
		return
	}
	r.add(SimpleCmd{Argv: slices.Clone(ws), assigned: assigned}, ls, wc)
}

// runString reads text as a new shell command, one level deeper.
func (r *reader) runString(text string, wc walkCtx) {
	if wc.depth+1 > MaxParseDepth {
		r.fail("the command has more than 4 levels of nested shells")
		return
	}
	// The joined words of a wrapper can be longer than the command after brace expansion.
	if len(text) > MaxCommandBytes {
		r.fail(whyTooLong)
		return
	}
	wc.depth++
	r.parse(text, wc)
}

func isAssignWord(w string) bool {
	name, _, ok := strings.Cut(w, "=")
	if !ok || name == "" || name[0] >= '0' && name[0] <= '9' {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c != '_' && (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

func (r *reader) unwrapEnv(s *wrapSpec, name string, ws []string, ls []bool, wc walkCtx) {
	got, n, ok := r.scan(s, name, ws, ls)
	if !ok || s.reports(got) {
		return
	}
	var words []string
	var lits []bool
	assigned := false
	for _, o := range got {
		switch o.name {
		case "-i", "-u", "-C", "-", "--ignore-environment", "--unset", "--chdir":
			assigned = true
		case "-S", "--split-string":
			if !o.lit {
				r.fail(whyRuntime)
				return
			}
			if strings.ContainsAny(o.val, `$\#`) {
				r.fail(`the env -S value has $, \, or #`)
				return
			}
			for f := range strings.FieldsSeq(o.val) {
				words = append(words, f)
				lits = append(lits, true)
			}
		}
	}
	words = append(words, ws[n:]...)
	lits = append(lits, ls[n:]...)
	for len(words) > 0 && isAssignWord(words[0]) {
		assigned = true
		words, lits = words[1:], lits[1:]
	}
	r.runArgv(words, lits, assigned, wc)
}

// unwrapShell reads the options of a shell and parses its -c string. It returns false when
// the words have no -c option.
func (r *reader) unwrapShell(s *wrapSpec, name string, ws []string, ls []bool, wc walkCtx) bool {
	if s.cstr {
		// fish and csh take the string as the value of -c. fish can take more than one.
		got, _, ok := r.scan(s, name, ws, ls)
		if !ok {
			return true
		}
		found := false
		for _, o := range got {
			switch o.name {
			case "-c", "--command", "-C", "--init-command":
				found = true
				r.runString(o.val, wc)
			}
		}
		return found
	}
	cflag := false
	i := 0
loop:
	for i < len(ws) {
		tok := ws[i]
		switch {
		case tok == "--" || tok == "-":
			i++
			break loop
		case strings.HasPrefix(tok, "--"):
			long, _, hasVal := strings.Cut(tok[2:], "=")
			switch {
			case hasWord(s.lval, long):
				if !hasVal {
					i++
				}
			case hasWord(s.lflag, long), s.lflag == "*":
			default:
				r.unknownOpt(name, "--"+long)
				return true
			}
			i++
		case len(tok) > 1 && (tok[0] == '-' || tok[0] == '+'):
			// An option such as -o takes the next word, also inside a cluster such as -oc.
			next := i + 1
			for j := 1; j < len(tok); j++ {
				switch c := tok[j]; {
				case strings.IndexByte(s.val, c) >= 0:
					next++
				case strings.IndexByte(s.flag, c) >= 0:
					if c == 'c' && tok[0] == '-' {
						cflag = true
					}
				default:
					r.unknownOpt(name, tok[:1]+string(c))
					return true
				}
			}
			i = next
		default:
			break loop
		}
	}
	if cflag && i < len(ws) {
		r.runString(ws[i], wc)
	}
	return cflag
}

// unwrapSu reads su and runuser. Both read options after the user name. The words after the
// user name go to the shell of the user.
func (r *reader) unwrapSu(s *wrapSpec, name string, ws []string, ls []bool, wc walkCtx) {
	got, pos, pl, ok := r.scanAll(s, name, ws, ls)
	if !ok || s.reports(got) {
		return
	}
	if o, found := findOpt(got, "-c", "--command", "--session-command"); found {
		r.runString(o.val, wc)
		return
	}
	if _, found := findOpt(got, "-u", "--user"); found && name == "runuser" {
		r.runArgv(pos, pl, false, wc)
		return
	}
	if len(pos) < 2 {
		return
	}
	if !r.unwrapShell(wrapperTable["sh"], name, pos[1:], pl[1:], wc) {
		// The shell runs a script file, which the gate cannot read.
		r.fail("the command gives arguments to the shell of " + name)
	}
}

// unwrapSSH reads ssh. OpenSSH reads options again after the host, unless "--" came before
// the host. The -o options ProxyCommand, LocalCommand, and KnownHostsCommand run a local
// command, and RemoteCommand runs a command on the host.
func (r *reader) unwrapSSH(s *wrapSpec, name string, ws []string, ls []bool, wc walkCtx) {
	got, n, ok := r.scan(s, name, ws, ls)
	if !ok {
		return
	}
	var host string
	var rest []string
	if n < len(ws) {
		host, rest = ws[n], ws[n+1:]
		if _, dashed := findOpt(got, "--"); !dashed && len(rest) > 0 {
			more, m, ok := r.scan(s, name, rest, ls[n+1:])
			if !ok {
				return
			}
			got, rest = append(got, more...), rest[m:]
		}
	}
	remote := wc
	remote.via = append(slices.Clone(wc.via), "ssh "+host)
	for _, o := range got {
		if o.name != "-o" {
			continue
		}
		// ssh reads "Key=Value" and "Key Value".
		kv := strings.TrimSpace(o.val)
		i := strings.IndexAny(kv, "= \t")
		if i < 0 {
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(strings.TrimLeft(kv[i:], " \t"), "="))
		switch strings.ToLower(kv[:i]) {
		case "proxycommand", "localcommand", "knownhostscommand":
			r.runString(val, wc)
		case "remotecommand":
			if host != "" {
				r.runString(val, remote)
			}
		}
	}
	if host == "" || s.reports(got) || len(rest) == 0 {
		return
	}
	r.runString(strings.Join(rest, " "), remote)
}

// unwrapExec reads "docker exec", "podman exec", and "kubectl exec". docker and podman also
// take "container exec". kubectl reads options after the target, up to "--".
func (r *reader) unwrapExec(s *wrapSpec, name string, ws []string, ls []bool, wc walkCtx) {
	got, n, ok := r.scan(s, name, ws, ls)
	if !ok || s.reports(got) {
		return
	}
	if name != "kubectl" && n < len(ws) && ws[n] == "container" {
		n++
	}
	if n >= len(ws) || ws[n] != "exec" {
		return
	}
	ws, ls = ws[n+1:], ls[n+1:]
	var target string
	if name == "kubectl" {
		got, pos, pl, ok := r.scanAll(s, name, ws, ls)
		if !ok || s.reports(got) {
			return
		}
		if o, found := findOpt(got, "-f", "--filename"); found {
			target = o.val
		} else if len(pos) > 0 {
			target, pos, pl = pos[0], pos[1:], pl[1:]
		}
		if target == "" {
			return
		}
		wc.via = append(slices.Clone(wc.via), name+" exec "+target)
		r.runArgv(pos, pl, false, wc)
		return
	}
	got, n, ok = r.scan(s, name, ws, ls)
	if !ok || s.reports(got) || n >= len(ws) {
		return
	}
	wc.via = append(slices.Clone(wc.via), name+" exec "+ws[n])
	ws, ls = ws[n+1:], ls[n+1:]
	if len(ws) > 0 && ws[0] == "--" {
		ws, ls = ws[1:], ls[1:]
	}
	r.runArgv(ws, ls, false, wc)
}
