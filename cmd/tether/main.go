// Command tether is a personal AI agent for the CLI, Telegram, and cron.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/dnwandana/tether-agent/internal/app"
	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/gatewaylock"
	"github.com/dnwandana/tether-agent/internal/obs"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/setup"
	"github.com/dnwandana/tether-agent/internal/skills"
	"github.com/dnwandana/tether-agent/internal/telegram"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	code := run(os.Args[1:], os.Stdout, os.Stderr)
	if code == exitExecRestart {
		// execSelf returns only when the exec fails.
		err := execSelf()
		fmt.Fprintf(os.Stderr, "gateway: cannot start the gateway again: %v. Start it again by hand.\n", err)
		code = 1
	}
	os.Exit(code)
}

// exitServiceRestart is the exit code of a restart under systemd. The unit lists it in
// RestartForceExitStatus and SuccessExitStatus (EX_TEMPFAIL, as in Hermes).
const exitServiceRestart = 75

// exitExecRestart is the value that run returns when the gateway must start itself again
// without a service manager. Only main acts on it.
const exitExecRestart = -75

// restartExitCode returns the exit code for a stop reason. A plain signal exits with 0.
// A reload or a restart exits with 75 under systemd (INVOCATION_ID is set). Else it asks
// main to exec the binary again.
func restartExitCode(reason string, getenv func(string) string) int {
	if reason != "reload" && reason != "restart" {
		return 0
	}
	if getenv("INVOCATION_ID") != "" {
		return exitServiceRestart
	}
	return exitExecRestart
}

// execSelf replaces the process with a new copy of the same binary. It returns only on
// failure. Hermes starts a detached copy here, and an exec is the simple form in Go. The
// lock file has close-on-exec, and runGateway already released it.
func execSelf() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}

// run executes the subcommand in args and returns the process exit code. Bare tether prints
// the main help. It does not call os.Exit, so tests can call it.
func run(args []string, stdout, stderr io.Writer) int {
	// "tether help" goes to runHelp in the switch, because "tether help X" needs it.
	if len(args) == 0 || args[0] == "-h" || args[0] == "-help" || args[0] == "--help" {
		fmt.Fprint(stdout, mainHelp(dataDir("")))
		return 0
	}
	if strings.HasPrefix(args[0], "-") {
		fmt.Fprintf(stderr, "tether: a command is needed\n%s\n", helpPointer)
		return 2
	}
	// The word help counts only directly after the command name. So "tether skills reset
	// help" still resets a skill with the name help.
	if len(args) >= 2 && isHelpWord(args[1]) {
		if _, ok := findHelp(args[0]); ok {
			fmt.Fprint(stdout, commandHelp(args[0]))
			return 0
		}
	}
	switch args[0] {
	case "chat":
		return runChat(args[1:], stdout, stderr)
	case "gateway":
		return runGateway(args[1:], stdout, stderr)
	case "version":
		return runVersion(args[1:], stdout, stderr)
	case "skills":
		return runSkills(args[1:], stdout, stderr)
	case "sessions":
		return runSessions(args[1:], stdout, stderr)
	case "pairing":
		return runPairing(args[1:], stdout, stderr)
	case "usage":
		return runUsage(args[1:], stdout, stderr)
	case "backup":
		return runBackup(args[1:], stdout, stderr)
	case "restore":
		return runRestore(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "setup":
		return runSetup(args[1:], stdout, stderr)
	case "update":
		return runUpdate(args[1:], stdout, stderr)
	case "help":
		return runHelp(args[1:], stdout, stderr)
	case "gen-tools-doc":
		// A hidden command for the developers. It does not show in the help text.
		return runGenToolsDoc(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n", args[0])
		if s := suggestCommand(args[0]); s != "" {
			fmt.Fprintf(stderr, "Did you mean %q? ", s)
		}
		fmt.Fprintln(stderr, helpPointer)
		return 2
	}
}

// runVersion prints the version of this binary and returns the exit code. It takes no flag
// and no argument.
func runVersion(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("version", flag.ContinueOnError)
	rest, code, ok := parseCommand(fl, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return usageError(stderr, "version", "unexpected argument: "+rest[0])
	}
	fmt.Fprintf(stdout, "tether %s\n", version)
	return 0
}

// stdin is the input of the chat. Tests replace it.
var stdin io.Reader = os.Stdin

// runChat starts the interactive CLI chat and returns the exit code. The logs go to
// <data dir>/logs/tether.log, and never to the terminal unless --log-stderr is given.
func runChat(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("chat", flag.ContinueOnError)
	dataFlag := fl.String("data-dir", "", "data folder (default: TETHER_HOME, then ~/.tether)")
	logStderr := fl.Bool("log-stderr", false, "write the logs to stderr instead of the log file")
	logLevel := fl.String("log-level", "", "log level: debug, info, warn, or error (default: log.level)")
	rest, code, ok := parseCommand(fl, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return usageError(stderr, "chat", "unexpected argument: "+rest[0])
	}
	dir := dataDir(*dataFlag)
	// Ctrl-C does not end the chat here: the CLI handles it. During a turn it stops the
	// turn, and at the prompt it ends the chat.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	a, err := app.New(ctx, app.Options{DataDir: dir, LogStderr: *logStderr, LogLevel: *logLevel, LogWriter: stderr})
	if err != nil {
		fmt.Fprintf(stderr, "chat: %v\n", err)
		return 1
	}
	defer a.Close()
	a.Prices.Start(ctx)
	cli := &gateway.CLI{
		In:          stdin,
		Out:         stdout,
		Store:       a.Store,
		Key:         "cli:" + osUserName(),
		Log:         a.Log,
		HistoryFile: filepath.Join(dir, ".history"),
		Deps:        a.Deps(),
	}
	if err := cli.Run(ctx); err != nil {
		a.Log.Error("chat failed", "err", err.Error())
		fmt.Fprintf(stderr, "chat: %v\n", err)
		return 1
	}
	return 0
}

// gatewayContext returns the base context of the gateway. Tests replace it to stop the
// gateway without a signal.
var gatewayContext = context.Background

// drainOf returns the maximum time that the gateway waits for the running turns at a stop.
// The turns that still run after it are cancelled.
func drainOf(cfg *config.Config) time.Duration {
	return time.Duration(cfg.Agent.RestartDrainTimeoutS) * time.Second
}

// gatewayUnitPaths replaces the unit files that the boot check reads. Tests set it. Nil
// uses the default unit paths.
var gatewayUnitPaths []string

// shutdownGrace is the maximum time that the gateway waits for the cancelled work at a
// stop. Hermes also waits 5 s after it interrupts the running agents.
const shutdownGrace = 5 * time.Second

// notifyContext is signal.NotifyContext. Tests replace it.
var notifyContext = signal.NotifyContext

// notifySignal is signal.Notify for SIGUSR1, the reload signal of the unit. Tests replace it.
var notifySignal = signal.Notify

// stopStepLimit is the maximum time of the notify step and of the stopping step. Each step
// has its own limit, so it takes no time from the drain.
const stopStepLimit = 5 * time.Second

// turnContextSetter is a channel that runs its turns under a separate context. The Run
// context of such a channel stops only its receive loop, so the running turns can end at a
// stop. SetTurnContext must run before Run.
type turnContextSetter interface {
	SetTurnContext(ctx context.Context)
}

// telegramBaseURL replaces the Bot API root of the gateway. Tests set it.
var telegramBaseURL = ""

// doctorReleaseAPI replaces the GitHub API base of the doctor release check. Tests set it.
var doctorReleaseAPI = ""

// runGateway runs the gateway role and returns the exit code.
func runGateway(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("gateway", flag.ContinueOnError)
	dataFlag := fl.String("data-dir", "", "data folder (default: TETHER_HOME, then ~/.tether)")
	logStderr := fl.Bool("log-stderr", false, "write the logs to stderr instead of the log file")
	logLevel := fl.String("log-level", "", "log level: debug, info, warn, or error (default: log.level)")
	rest, code, ok := parseCommand(fl, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return usageError(stderr, "gateway", "unexpected argument: "+rest[0])
	}
	dir := dataDir(*dataFlag)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintf(stderr, "gateway: cannot make the data folder: %v\n", err)
		return 1
	}
	release, err := gatewaylock.Acquire(filepath.Join(dir, "gateway.lock"))
	if err != nil {
		fmt.Fprintf(stderr, "gateway: %v\n", err)
		return 1
	}
	defer release()
	ctx, stop := notifyContext(gatewayContext(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// SIGUSR1 restarts the gateway. Keep the handler until the return: the default action
	// of SIGUSR1 ends the process at once.
	reload := make(chan os.Signal, 1)
	notifySignal(reload, syscall.SIGUSR1)
	defer signal.Stop(reload)
	// Only the gateway role builds the cron scheduler and polls Telegram.
	a, err := app.New(ctx, app.Options{DataDir: dir, LogStderr: *logStderr, LogLevel: *logLevel, LogWriter: stderr,
		Gateway: true, TelegramBaseURL: telegramBaseURL})
	if err != nil {
		fmt.Fprintf(stderr, "gateway: %v\n", err)
		return 1
	}
	defer a.Close()
	log := a.Log
	if a.Cfg.Telegram.Enabled {
		// AddTelegram also gives the cron deliverer its send function and the home channel.
		if _, err := a.AddTelegram(ctx); err != nil {
			log.Error("gateway: the Telegram channel cannot start", "err", err.Error())
			fmt.Fprintf(stderr, "gateway: %v\n", err)
			return 1
		}
	} else {
		log.Info("telegram is disabled, so a cron job cannot deliver to a chat", "setting", "telegram.enabled")
	}
	warnOldUnit(log, a.Cfg)
	if addr := os.Getenv("TETHER_PPROF"); addr != "" {
		srv, err := startPprof(addr)
		if err != nil {
			fmt.Fprintf(stderr, "gateway: %v\n", err)
			return 1
		}
		defer srv.Close()
		log.Info("pprof started", "addr", addr)
	}

	// recvCtx stops the receive loops of the channels. workCtx stops the turns, the
	// background delegations, the cron scheduler, and the other background work. The two
	// contexts do not end with ctx: the stop below ends them in order.
	base := context.WithoutCancel(ctx)
	recvCtx, stopRecv := context.WithCancel(base)
	defer stopRecv()
	workCtx, stopWork := context.WithCancel(base)
	defer stopWork()
	if a.Batches != nil {
		a.Batches.Ctx = workCtx
	}
	a.Agent.Background = workCtx
	// The restart line goes out before the replies of the resumed turns.
	a.BootNotice(workCtx, version)
	a.Housekeeping(workCtx)
	a.Prices.Start(workCtx)
	gw := a.Gateway()
	var channels sync.WaitGroup
	for name, ch := range gw.Channels {
		if s, ok := ch.(turnContextSetter); ok {
			s.SetTurnContext(workCtx)
		}
		channels.Add(1)
		go func() {
			defer channels.Done()
			if err := ch.Run(recvCtx, gw); err != nil && recvCtx.Err() == nil {
				log.Error("gateway: the channel stopped", "channel", name, "err", err.Error())
			}
		}()
	}
	channelsDone := make(chan struct{})
	go func() {
		channels.Wait()
		close(channelsDone)
	}()
	schedDone := make(chan struct{})
	go func() {
		defer close(schedDone)
		a.Sched.Start(workCtx)
	}()
	log.Info("gateway started", "pid", os.Getpid(), "channels", len(gw.Channels))

	// reason is the stop reason: "signal", "reload", or "restart". skipKey is the key that
	// sent /restart. Its command reply already tells the chat, so it gets no stop notice.
	var reason, skipKey string
	select {
	case <-ctx.Done():
		reason = "signal"
	case <-reload:
		reason = "reload"
	case req := <-a.Restarts():
		reason, skipKey = req.Reason, req.Key
	}
	// From now on, a /restart starts no new stop, and a SIGUSR1 has no effect.
	a.MarkStopping()
	// Restore the default signal action now, so that a second Ctrl-C ends the process.
	stop()
	drain := drainOf(a.Cfg)
	log.Info("gateway stopping", "reason", reason, "busy_keys", len(gw.BusyKeys()), "drain", drain.String())
	waits := []stopWait{
		{name: "channels", wait: waitChan(channelsDone)},
		{name: "cron", wait: waitChan(schedDone)},
		{name: "background reviews", wait: a.Agent.WaitReviews},
	}
	if a.Batches != nil {
		waits = append(waits, stopWait{name: "background delegations", wait: a.Batches.Wait})
	}
	gatewayStop{
		drain: drain,
		grace: shutdownGrace,
		begin: func() {
			gw.StopStarted()
			a.WriteAliveAt(base)
		},
		stopReceive: stopRecv,
		notify: func() {
			nctx, cancel := context.WithTimeout(base, stopStepLimit)
			defer cancel()
			gw.NotifyStopping(nctx, gateway.StopNotice(reason, drain), skipKey)
		},
		stopping: func() {
			sctx, cancel := context.WithTimeout(base, stopStepLimit)
			defer cancel()
			gw.BeginStopping(sctx)
		},
		waitTurns: gw.Wait,
		stopWork:  stopWork,
		waits:     waits,
		log:       log,
	}.run()
	// The deferred a.Close closes MCP, Chrome, and the database last.
	log.Info("gateway stopped")
	return restartExitCode(reason, os.Getenv)
}

// warnOldUnit logs one warning when the gateway runs under systemd with a unit file that
// is too old for the restart. It does nothing outside systemd or when no unit file exists.
func warnOldUnit(log *slog.Logger, cfg *config.Config) {
	if os.Getenv("INVOCATION_ID") == "" {
		return
	}
	path, text, ok := setup.FindUnit(gatewayUnitPaths)
	if !ok {
		return
	}
	problems := setup.UnitProblems(text, cfg.Agent.RestartDrainTimeoutS)
	if len(problems) == 0 {
		return
	}
	system := strings.HasPrefix(path, "/etc/systemd/system/")
	log.Warn("gateway: the systemd unit is old", "path", path,
		"problems", strings.Join(problems, " "), "next", setup.UnitNext(system))
}

// gatewayStop holds the steps of a gateway stop.
type gatewayStop struct {
	// drain is the maximum time for the running turns. grace is the maximum time for the
	// cancelled work.
	drain, grace time.Duration
	// begin marks the start of the stop, before the receive loops stop. A nil field is skipped.
	begin func()
	// stopReceive stops the receive loops of the channels.
	stopReceive func()
	// notify tells the busy chats that the gateway stops. A nil field is skipped.
	notify func()
	// stopping runs when the drain time ends and some turns still run. A nil field is
	// skipped.
	stopping func()
	// waitTurns waits for the running turns. It returns false at the timeout.
	waitTurns func(timeout time.Duration) bool
	// stopWork cancels the turns, the background work, and the cron scheduler.
	stopWork func()
	// waits are the parts that end after stopWork.
	waits []stopWait
	log   *slog.Logger
}

// stopWait is one part that a gateway stop waits for.
type stopWait struct {
	name string
	// wait returns false at the timeout.
	wait func(timeout time.Duration) bool
}

// run stops the gateway in the order of Hermes. First, the channels stop to receive
// messages, so no new turn starts. Then the running turns get the drain time to end.
// Then the remaining work is cancelled, and each part gets the rest of the grace time.
func (s gatewayStop) run() {
	if s.begin != nil {
		s.begin()
	}
	s.stopReceive()
	if s.notify != nil {
		s.notify()
	}
	start := time.Now()
	if s.waitTurns(s.drain) {
		s.log.Info("gateway: all turns ended", "wait", time.Since(start).Round(time.Millisecond).String())
	} else {
		s.log.Warn("gateway: some turns did not end in time, so the gateway cancels them", "wait", s.drain.String())
		if s.stopping != nil {
			s.stopping()
		}
	}
	s.stopWork()
	end := time.Now().Add(s.grace)
	for _, w := range s.waits {
		if !w.wait(max(time.Until(end), 0)) {
			s.log.Warn("gateway: a part did not stop in time after the cancel", "part", w.name, "wait", s.grace.String())
		}
	}
}

// waitChan returns a wait function that waits until done is closed.
func waitChan(done <-chan struct{}) func(time.Duration) bool {
	return func(timeout time.Duration) bool {
		select {
		case <-done:
			return true
		default:
		}
		t := time.NewTimer(timeout)
		defer t.Stop()
		select {
		case <-done:
			return true
		case <-t.C:
			return false
		}
	}
}

// startPprof serves net/http/pprof on addr. The host of addr must be a loopback address,
// because the profiles show the memory of the process.
func startPprof(addr string) (*http.Server, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("TETHER_PPROF %q is not a host:port address: %w", addr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("TETHER_PPROF %q must be a loopback address, for example 127.0.0.1:6060", addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("TETHER_PPROF: cannot listen on %s: %w", addr, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return srv, nil
}

// osUserName returns the name of the OS user for the CLI session key. It falls back to
// $USER, then to "user".
func osUserName() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if name := os.Getenv("USER"); name != "" {
		return name
	}
	return "user"
}

// dataDir returns the data folder. The --data-dir flag wins, then
// TETHER_HOME, then ~/.tether.
func dataDir(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("TETHER_HOME"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".tether"
	}
	return filepath.Join(home, ".tether")
}

// skillsCommandFlags names the flags that each skills command uses, in
// addition to --data-dir. A command that is not in the map is unknown.
var skillsCommandFlags = map[string][]string{
	"install":   {"skill", "ref", "yes", "force"},
	"list":      {"available"},
	"reset":     {},
	"uninstall": {"yes"},
	"check":     {},
	"update":    {"yes", "force"},
}

// skillsFlagCheck returns false and prints the bad-usage lines when a flag
// is set that the skills command cmd does not use.
func skillsFlagCheck(fl *flag.FlagSet, cmd string, stderr io.Writer) (code int, ok bool) {
	allowed := map[string]bool{"data-dir": true}
	for _, name := range skillsCommandFlags[cmd] {
		allowed[name] = true
	}
	var bad string
	fl.Visit(func(f *flag.Flag) {
		if bad == "" && !allowed[f.Name] {
			bad = f.Name
		}
	})
	if bad != "" {
		return usageError(stderr, "skills", "flag --"+bad+" is not used by skills "+cmd), false
	}
	return 0, true
}

// runSkills runs the skills subcommand and returns the exit code.
func runSkills(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("skills", flag.ContinueOnError)
	dataFlag := fl.String("data-dir", "", "data folder (default: TETHER_HOME, then ~/.tether)")
	skillFlag := fl.String("skill", "", "install: the skill to select by its frontmatter name")
	refFlag := fl.String("ref", "", "install: a branch, a tag, or a full commit SHA")
	yesFlag := fl.Bool("yes", false, "confirm with no prompt")
	forceFlag := fl.Bool("force", false, "accept the risk of a caution verdict or a new install")
	availableFlag := fl.Bool("available", false, "list: show the curated index")
	rest, code, ok := parseCommand(fl, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(rest) == 0 {
		return usageError(stderr, "skills", "a command is needed: list, install, check, update, uninstall, or reset")
	}
	if _, known := skillsCommandFlags[rest[0]]; !known {
		return usageError(stderr, "skills", "unknown command: "+rest[0])
	}
	if code, ok := skillsFlagCheck(fl, rest[0], stderr); !ok {
		return code
	}
	dir := dataDir(*dataFlag)
	skillsDir := filepath.Join(dir, "skills")
	switch rest[0] {
	case "install":
		if len(rest) < 2 {
			return usageError(stderr, "skills", "install needs a source")
		}
		if len(rest) > 2 {
			return usageError(stderr, "skills", "unexpected argument: "+rest[2])
		}
		// An interrupt cancels the fetch, and the installer then removes its staging folder.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		req := skills.InstallRequest{Source: rest[1], Skill: *skillFlag, Ref: *refFlag, Yes: *yesFlag, Force: *forceFlag}
		if _, err := newInstaller(ctx, dir, stdout, stderr).Install(ctx, req); err != nil {
			return skillsError(stderr, "install", err)
		}
		return 0
	case "list":
		if *availableFlag {
			if len(rest) > 2 {
				return usageError(stderr, "skills", "unexpected argument: "+rest[2])
			}
			word := ""
			if len(rest) == 2 {
				word = rest[1]
			}
			return skillsListAvailable(skillsDir, word, stdout, stderr)
		}
		if len(rest) > 1 {
			return usageError(stderr, "skills", "unexpected argument: "+rest[1])
		}
		return skillsList(dir, skillsDir, stdout, stderr)
	case "reset":
		if len(rest) > 2 {
			return usageError(stderr, "skills", "unexpected argument: "+rest[2])
		}
		names := skills.BootstrapNames()
		if len(rest) == 2 {
			names = rest[1:]
		}
		for _, name := range names {
			if err := skills.Reset(skillsDir, name); err != nil {
				fmt.Fprintf(stderr, "skills reset: %v\n", err)
				return 1
			}
			fmt.Fprintf(stdout, "reset %s\n", name)
		}
		return 0
	case "uninstall":
		if len(rest) < 2 {
			return usageError(stderr, "skills", "uninstall needs a skill name")
		}
		if len(rest) > 2 {
			return usageError(stderr, "skills", "unexpected argument: "+rest[2])
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		req := skills.UninstallRequest{Name: rest[1], Yes: *yesFlag, CronUsers: cronUsers(dir)}
		if err := newInstaller(ctx, dir, stdout, stderr).Uninstall(ctx, req); err != nil {
			return skillsError(stderr, "uninstall", err)
		}
		return 0
	case "check":
		if len(rest) > 2 {
			return usageError(stderr, "skills", "unexpected argument: "+rest[2])
		}
		name := ""
		if len(rest) == 2 {
			name = rest[1]
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		rows, err := skills.Check(ctx, skillsDir, skills.FindGit, name)
		if err != nil {
			// A bad lockfile or an unknown name means that no skill was
			// checked. Exit code 1 means "an update is available", so a
			// script must not see it here.
			skillsError(stderr, "check", err)
			return 2
		}
		skills.FormatCheck(stdout, rows)
		return skills.CheckExitCode(rows)
	case "update":
		// A local source needs no git, so a missing git is not an error here.
		// The installer reports it for each skill that needs it.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		req := skills.UpdateRequest{Names: rest[1:], Yes: *yesFlag, Force: *forceFlag}
		if err := newInstaller(ctx, dir, stdout, stderr).Update(ctx, req); err != nil {
			return skillsError(stderr, "update", err)
		}
		return 0
	default:
		return usageError(stderr, "skills", "unknown command: "+rest[0])
	}
}

// skillsConfig loads the config of the data folder dir for the skills
// commands. On an error it returns the default config and the error.
func skillsConfig(dir string) (config.Config, error) {
	dotenv, err := security.LoadDotEnv(filepath.Join(dir, ".env"))
	if err != nil {
		return config.Default(), err
	}
	loaded, err := config.Load(filepath.Join(dir, "config.yaml"), security.EnvLookup(dotenv))
	if err != nil {
		return config.Default(), err
	}
	return *loaded, nil
}

// cronUsers returns a function that names the cron jobs that use a skill, as
// `"<job name>" (<job id>)`. It reads the jobs of <dir>/state.db once. The
// line is only a warning, so a missing state.db or a failed query gives a
// function that returns nil.
func cronUsers(dir string) func(name string) []string {
	none := func(string) []string { return nil }
	// The gateway can have this database open, and the query only reads, so
	// the handle neither creates the file nor applies the migrations.
	d, err := db.OpenReadOnly(filepath.Join(dir, "state.db"))
	if err != nil {
		return none
	}
	defer d.Close()
	jobs, err := (&cron.Store{DB: d}).List(context.Background())
	if err != nil {
		return none
	}
	return func(name string) []string {
		var lines []string
		for _, j := range jobs {
			for _, s := range j.Skills {
				if s == name {
					lines = append(lines, fmt.Sprintf("%q (%s)", j.Name, j.ID))
					break
				}
			}
		}
		return lines
	}
}

// skillsList prints the skills that a session loads. A config error is a
// warning, because the local skills do not need the config.
func skillsList(dir, skillsDir string, stdout, stderr io.Writer) int {
	cfg, err := skillsConfig(dir)
	if err != nil {
		fmt.Fprintf(stderr, "warning: the config did not load, so the list has no external skills: %v\n", err)
	}
	list, warns, err := skills.Load(skillsDir, cfg.Skills.ExternalDirs, cfg.Skills.Guard)
	if err != nil {
		fmt.Fprintf(stderr, "skills list: %v\n", err)
		return 1
	}
	for _, w := range warns {
		fmt.Fprintf(stderr, "warning: %s: %s\n", w.Path, w.Text)
	}
	installed := map[string]skills.LockEntry{}
	if lock, err := skills.ReadLock(skillsDir); err != nil {
		fmt.Fprintf(stderr, "warning: the lockfile did not load, so no skill shows as installed: %v\n", err)
	} else {
		installed = lock.Installed
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSOURCE\tDESCRIPTION")
	for _, s := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", skills.Sanitize(s.Name), skillSource(s, installed),
			skills.Sanitize(shortText(s.Description, 100)))
	}
	if err := tw.Flush(); err != nil {
		return 1
	}
	return 0
}

// skillsListAvailable prints the curated index, filtered by word when word
// is not empty.
func skillsListAvailable(skillsDir, word string, stdout, stderr io.Writer) int {
	entries, err := skills.LoadIndex()
	if err != nil {
		fmt.Fprintf(stderr, "skills list: %v\n", err)
		return 1
	}
	entries = skills.FilterIndex(entries, word)
	if len(entries) == 0 {
		fmt.Fprintf(stdout, "No skill in the index matches %q.\n", skills.Sanitize(word))
		return 0
	}
	installed := map[string]skills.LockEntry{}
	if lock, err := skills.ReadLock(skillsDir); err != nil {
		fmt.Fprintf(stderr, "warning: the lockfile did not load, so no skill shows as installed: %v\n", err)
	} else {
		installed = lock.Installed
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tINSTALLED\tREPO\tDESCRIPTION")
	for _, e := range entries {
		mark := "-"
		if _, ok := installed[e.Name]; ok {
			mark = "yes"
		}
		repo, ok := skills.GitHubRepo(e.URL)
		if !ok {
			repo = e.URL
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", skills.Sanitize(e.Name), mark, skills.Sanitize(repo),
			skills.Sanitize(shortText(e.Description, 100)))
	}
	if err := tw.Flush(); err != nil {
		return 1
	}
	fmt.Fprint(stdout, "\nInstall one with: tether skills install <name>\n")
	return 0
}

// skillSource names where a skill comes from. A skill folder in skills/ that
// the lockfile lists is "installed", else it is "local".
func skillSource(s skills.Skill, installed map[string]skills.LockEntry) string {
	_, inLock := installed[s.Name]
	switch {
	case s.Trust == skills.TrustBuiltin:
		return "builtin"
	case s.External:
		return "external"
	case inLock:
		return "installed"
	default:
		return "local"
	}
}

// stdioIsTerminal is true when stdin and stdout are terminals. Tests replace it.
var stdioIsTerminal = func() bool {
	return setup.IsTerminal(int(os.Stdin.Fd())) && setup.IsTerminal(int(os.Stdout.Fd()))
}

// termPrompter asks the skills questions on the terminal. It reads the
// answers from r and prints the questions to out. When ctx ends, a question
// that waits for an answer returns ctx.Err(), so an interrupt at the prompt
// stops the command.
type termPrompter struct {
	ctx context.Context
	r   *bufio.Reader
	out io.Writer
}

// Terminal returns true when stdin and stdout are terminals.
func (p *termPrompter) Terminal() bool { return stdioIsTerminal() }

// Confirm prints the question with " [y/N] " and returns true only for the
// answer y or yes. An end of input is a no.
func (p *termPrompter) Confirm(question string) (bool, error) {
	fmt.Fprint(p.out, question+" [y/N] ")
	line, err := p.readLine()
	if err != nil {
		return false, err
	}
	answer := strings.ToLower(line)
	return answer == "y" || answer == "yes", nil
}

// ConfirmYes prints the question with " [Y/n] " and returns true for an empty answer, y, or
// yes, and false for n or no. Another answer asks again. An end of input or an interrupt is
// a no.
func (p *termPrompter) ConfirmYes(question string) (bool, error) {
	type result struct {
		line string
		err  error
	}
	ctx := p.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		fmt.Fprint(p.out, question+" [Y/n] ")
		// The read runs apart, like readLine, because a terminal read does not see ctx.
		// readLine is not used here: it gives "" at an end of input, and "" is a yes here.
		done := make(chan result, 1)
		go func() {
			line, err := p.r.ReadString('\n')
			done <- result{line, err}
		}()
		var line string
		var err error
		select {
		case <-ctx.Done():
			fmt.Fprintln(p.out)
			return false, nil
		case r := <-done:
			line, err = r.line, r.err
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		if err != nil && strings.TrimSpace(line) == "" {
			return false, nil
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "", "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		if err != nil {
			return false, nil
		}
	}
}

// Ask prints the question and returns the trimmed answer.
func (p *termPrompter) Ask(question string) (string, error) {
	fmt.Fprint(p.out, question+" ")
	return p.readLine()
}

// readLine returns the next line without the spaces at its ends. An end of
// input gives the text before it and no error.
func (p *termPrompter) readLine() (string, error) {
	type result struct {
		line string
		err  error
	}
	// A read from the terminal does not see the context, so it runs apart.
	// After an interrupt the command returns, so the read that still waits
	// does no harm.
	done := make(chan result, 1)
	go func() {
		line, err := p.r.ReadString('\n')
		done <- result{line, err}
	}()
	ctx := p.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		fmt.Fprintln(p.out)
		return "", ctx.Err()
	case r := <-done:
		if r.err != nil && !errors.Is(r.err, io.EOF) {
			return "", r.err
		}
		return strings.TrimSpace(r.line), nil
	}
}

// newInstaller returns an installer for the data folder dir. It asks its
// questions on stdin and stdout, and a question stops when ctx ends. A config
// error is a warning on stderr, because the install needs the config only for
// the external skill folders.
func newInstaller(ctx context.Context, dir string, stdout, stderr io.Writer) *skills.Installer {
	cfg, err := skillsConfig(dir)
	if err != nil {
		fmt.Fprintf(stderr, "warning: the config did not load, so the install does not check the external skills: %v\n", err)
	}
	return &skills.Installer{
		SkillsDir: filepath.Join(dir, "skills"),
		External:  cfg.Skills.ExternalDirs,
		Prompt:    &termPrompter{ctx: ctx, r: bufio.NewReader(stdin), out: stdout},
		Out:       stdout,
		Err:       stderr,
	}
}

// skillsError prints err of the skills command cmd and returns the exit
// code. A usage error gives 2. Every other error gives 1. The text goes
// through skills.Sanitize, because it can hold text from a skill or a remote.
func skillsError(stderr io.Writer, cmd string, err error) int {
	var usage *skills.UsageError
	var many *skills.ManySkillsError
	switch {
	case errors.As(err, &usage):
		return usageError(stderr, "skills", skills.Sanitize(usage.Msg))
	case errors.Is(err, skills.ErrCancelled):
		// The installer has already printed "Cancelled.".
		return 1
	case errors.Is(err, context.Canceled):
		// An interrupt stopped the command, and the installer removed its
		// staging folder.
		fmt.Fprintln(stderr, "Cancelled.")
		return 1
	case errors.As(err, &many):
		fmt.Fprintf(stderr, "skills %s: %s\n", cmd, skills.Sanitize(many.Error()))
		tw := tabwriter.NewWriter(stderr, 0, 0, 2, ' ', 0)
		for _, f := range many.Found {
			fmt.Fprintf(tw, "  %s\t%s\n", skills.Sanitize(f.Name), skills.Sanitize(f.RelDir))
		}
		_ = tw.Flush()
		if len(many.Found) > 0 {
			src := "<source>"
			if many.Source != "" {
				src = shellQuote(skills.Sanitize(many.Source))
			}
			fmt.Fprintf(stderr, "Example: tether skills install %s --skill %s\n", src, shellQuote(skills.Sanitize(many.Found[0].Name)))
		}
		return 1
	default:
		fmt.Fprintf(stderr, "skills %s: %s\n", cmd, skills.Sanitize(err.Error()))
		return 1
	}
}

// shellSafe matches a word that a POSIX shell reads as it is.
var shellSafe = regexp.MustCompile(`^[A-Za-z0-9@%_+=:,./~-]+$`)

// shellQuote returns s in a form that the owner can paste into a shell.
func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shortText returns the first line of s, cut to max runes.
func shortText(s string, max int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

// parseInterleaved parses the flags of fl in args and returns the other arguments in order.
// Unlike fl.Parse, it also accepts flags after the first positional argument.
func parseInterleaved(fl *flag.FlagSet, args []string) ([]string, error) {
	var rest []string
	for {
		if err := fl.Parse(args); err != nil {
			return nil, err
		}
		args = fl.Args()
		if len(args) == 0 {
			return rest, nil
		}
		rest = append(rest, args[0])
		args = args[1:]
	}
}

// newRedactor returns a redactor that masks the .env values and the values of the secret
// variables in the process environment.
func newRedactor(dotenv map[string]string) *security.Redactor { return app.NewRedactor(dotenv) }

// runSessions runs the sessions subcommand and returns the exit code. It opens state.db
// and nothing else.
func runSessions(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("sessions", flag.ContinueOnError)
	dataFlag := fl.String("data-dir", "", "data folder (default: TETHER_HOME, then ~/.tether)")
	outFlag := fl.String("out", "", "export file (default: <data dir>/exports/<id>.jsonl)")
	rest, code, ok := parseCommand(fl, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(rest) == 0 {
		return usageError(stderr, "sessions", "a command is needed: list or export")
	}
	switch rest[0] {
	case "list":
		if len(rest) > 1 {
			return usageError(stderr, "sessions", "unexpected argument: "+rest[1])
		}
	case "export":
		if len(rest) < 2 {
			return usageError(stderr, "sessions", "export needs a session id")
		}
		if len(rest) > 2 {
			return usageError(stderr, "sessions", "unexpected argument: "+rest[2])
		}
	default:
		return usageError(stderr, "sessions", "unknown command: "+rest[0])
	}
	dir := dataDir(*dataFlag)
	dbPath := filepath.Join(dir, "state.db")
	if _, err := os.Stat(dbPath); err != nil {
		fmt.Fprintf(stderr, "sessions: cannot open %s: %v\n", dbPath, err)
		return 1
	}
	d, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "sessions: %v\n", err)
		return 1
	}
	defer d.Close()
	st := sessions.New(d)
	ctx := context.Background()
	if rest[0] == "list" {
		return sessionsList(ctx, st, stdout, stderr)
	}
	dotenv, err := security.LoadDotEnv(filepath.Join(dir, ".env"))
	if err != nil {
		fmt.Fprintf(stderr, "sessions export: %v\n", err)
		return 1
	}
	st.Redactor = newRedactor(dotenv)
	id := rest[1]
	out := *outFlag
	if out == "" {
		out = filepath.Join(dir, "exports", id+".jsonl")
	}
	n, err := sessionsExport(ctx, st, id, out)
	if err != nil {
		fmt.Fprintf(stderr, "sessions export: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %d messages to %s\n", n, out)
	return 0
}

// sessionsList prints the 50 newest sessions in tab-separated columns.
func sessionsList(ctx context.Context, st *sessions.Store, stdout, stderr io.Writer) int {
	list, err := st.ListAll(ctx, 50)
	if err != nil {
		fmt.Fprintf(stderr, "sessions list: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "id\ttitle\tkind\tlast_active\tmessages")
	for _, s := range list {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%d\n", s.ID, s.Title, s.Kind,
			s.LastActive.Local().Format(time.DateTime), s.MessageCount)
	}
	return 0
}

// sessionsExport writes the JSONL export of the session to path with mode 0600. It
// removes the file again when the export fails.
func sessionsExport(ctx context.Context, st *sessions.Store, id, path string) (int, error) {
	if _, err := st.Get(ctx, id); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	// O_CREATE does not change the mode of a file that exists.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return 0, err
	}
	bw := bufio.NewWriter(f)
	n, err := st.ExportJSONL(ctx, id, bw)
	if err == nil {
		err = bw.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return 0, err
	}
	return n, nil
}

// defaultPairPlatform is the platform of approve and revoke when the command gives none.
const defaultPairPlatform = "telegram"

// runPairing runs the pairing subcommand and returns the exit code. It opens state.db and
// nothing else. The running gateway reads the pairing table on each message, so a change is
// visible at once.
func runPairing(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("pairing", flag.ContinueOnError)
	dataFlag := fl.String("data-dir", "", "data folder (default: TETHER_HOME, then ~/.tether)")
	rest, code, ok := parseCommand(fl, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(rest) == 0 {
		return usageError(stderr, "pairing", "a command is needed: list, approve, or revoke")
	}
	cmd, params := rest[0], rest[1:]
	platform := defaultPairPlatform
	switch cmd {
	case "list":
		if len(params) > 1 {
			return usageError(stderr, "pairing", "unexpected argument: "+params[1])
		}
		platform = ""
		if len(params) == 1 {
			platform = params[0]
		}
	case "approve", "revoke":
		switch len(params) {
		case 0:
			what := "a code"
			if cmd == "revoke" {
				what = "a user id"
			}
			return usageError(stderr, "pairing", cmd+" needs "+what)
		case 1:
		case 2:
			platform, params = params[0], params[1:]
		default:
			return usageError(stderr, "pairing", "unexpected argument: "+params[2])
		}
	default:
		return usageError(stderr, "pairing", "unknown command: "+cmd)
	}
	platform = strings.ToLower(strings.TrimSpace(platform))
	dbPath := filepath.Join(dataDir(*dataFlag), "state.db")
	if _, err := os.Stat(dbPath); err != nil {
		fmt.Fprintf(stderr, "pairing: cannot open %s: %v\n", dbPath, err)
		return 1
	}
	d, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "pairing: %v\n", err)
		return 1
	}
	defer d.Close()
	p := &telegram.Pairing{DB: d}
	ctx := context.Background()
	switch cmd {
	case "list":
		return pairingList(ctx, p, platform, stdout, stderr)
	case "approve":
		return pairingApprove(ctx, p, platform, params[0], stdout, stderr)
	default:
		return pairingRevoke(ctx, p, platform, params[0], stdout, stderr)
	}
}

// pairingList prints the pending requests and the approved users. It never prints a code,
// because the table keeps only a hash of each code.
func pairingList(ctx context.Context, p *telegram.Pairing, platform string, stdout, stderr io.Writer) int {
	rows, err := p.List(ctx, platform)
	if err != nil {
		fmt.Fprintf(stderr, "pairing list: %v\n", err)
		return 1
	}
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "No pairing data found. No one has tried to pair yet.")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PLATFORM\tUSER ID\tNAME\tSTATUS\tSINCE")
	for _, r := range rows {
		since := r.CreatedAt
		if r.Status == telegram.PairApproved {
			since = r.ApprovedAt
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Platform, r.UserID, r.UserName, r.Status,
			since.Local().Format(time.DateTime))
	}
	if err := tw.Flush(); err != nil {
		return 1
	}
	return 0
}

// pairingApprove approves a pairing code and prints the Hermes messages.
func pairingApprove(ctx context.Context, p *telegram.Pairing, platform, code string, stdout, stderr io.Writer) int {
	uid, err := p.Approve(ctx, platform, code)
	switch {
	case err == nil:
		fmt.Fprintf(stdout, "Approved! User %s on %s can now use the bot.\n", uid, platform)
		fmt.Fprintln(stdout, "The bot recognizes the user on the next message.")
		return 0
	case errors.Is(err, telegram.ErrPairLockedOut):
		fmt.Fprintf(stderr, "pairing approve: platform %s is locked after too many failed approvals.\n", platform)
		fmt.Fprintln(stderr, "The lock stays for 1 hour after the last failed approval. Try again after that time.")
		return 1
	case errors.Is(err, telegram.ErrPairCodeNotFound), errors.Is(err, telegram.ErrPairCodeExpired):
		fmt.Fprintf(stderr, "pairing approve: code %s not found or expired for platform %s.\n",
			strings.ToUpper(strings.TrimSpace(code)), platform)
		fmt.Fprintln(stderr, "Run 'tether pairing list' to see the pending requests.")
		return 1
	default:
		fmt.Fprintf(stderr, "pairing approve: %v\n", err)
		return 1
	}
}

// pairingRevoke removes the access of a user.
func pairingRevoke(ctx context.Context, p *telegram.Pairing, platform, userID string, stdout, stderr io.Writer) int {
	err := p.Revoke(ctx, platform, userID)
	switch {
	case err == nil:
		fmt.Fprintf(stdout, "Revoked access for user %s on %s.\n", userID, platform)
		return 0
	case errors.Is(err, telegram.ErrPairUserNotFound):
		fmt.Fprintf(stderr, "pairing revoke: User %s not found for platform %s.\n", userID, platform)
		return 1
	default:
		fmt.Fprintf(stderr, "pairing revoke: %v\n", err)
		return 1
	}
}

// runUsage runs the usage subcommand and returns the exit code. It opens state.db and reads
// the config for the flat-rate providers.
func runUsage(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("usage", flag.ContinueOnError)
	dataFlag := fl.String("data-dir", "", "data folder (default: TETHER_HOME, then ~/.tether)")
	rest, code, ok := parseCommand(fl, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(rest) > 1 {
		return usageError(stderr, "usage", "unexpected argument: "+rest[1])
	}
	scope := "today"
	if len(rest) == 1 {
		scope = rest[0]
	}
	since, ok := obs.UsageSince(scope, time.Now())
	if !ok {
		return usageError(stderr, "usage", "unknown range "+scope+"; use today, 7d, or 30d")
	}
	dir := dataDir(*dataFlag)
	dbPath := filepath.Join(dir, "state.db")
	if _, err := os.Stat(dbPath); err != nil {
		fmt.Fprintf(stderr, "usage: cannot open %s: %v\n", dbPath, err)
		return 1
	}
	cfg := config.Default()
	dotenv, err := security.LoadDotEnv(filepath.Join(dir, ".env"))
	if err == nil {
		var loaded *config.Config
		if loaded, err = config.Load(filepath.Join(dir, "config.yaml"), security.EnvLookup(dotenv)); err == nil {
			cfg = *loaded
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "warning: the config did not load, so no provider shows as a flat plan: %v\n", err)
	}
	d, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "usage: %v\n", err)
		return 1
	}
	defer d.Close()
	rows, compactions, err := obs.UsageReport(context.Background(), d, since, "", gateway.FlatProviders(&cfg))
	if err != nil {
		fmt.Fprintf(stderr, "usage: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Usage %s:\n%s\nCompactions: %d\n", scope, obs.FormatUsage(rows), compactions)
	return 0
}

// runDoctor runs the doctor checks and returns the exit code.
func runDoctor(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("doctor", flag.ContinueOnError)
	dataFlag := fl.String("data-dir", "", "data folder (default: TETHER_HOME, then ~/.tether)")
	rest, code, ok := parseCommand(fl, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return usageError(stderr, "doctor", "unexpected argument: "+rest[0])
	}
	d := &setup.Doctor{DataDir: dataDir(*dataFlag), HC: &http.Client{}, TelegramBase: telegramBaseURL,
		Version: version, ReleaseAPI: doctorReleaseAPI}
	checks := d.Run(context.Background())
	fmt.Fprint(stdout, setup.Format(checks))
	if setup.HasFail(checks) {
		return 1
	}
	return 0
}

// runSetup runs the setup wizard and returns the exit code.
func runSetup(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("setup", flag.ContinueOnError)
	dataFlag := fl.String("data-dir", "", "data folder (default: TETHER_HOME, then ~/.tether)")
	system := fl.Bool("system", false, "write a system unit instead of a user unit (needs root)")
	rest, code, ok := parseCommand(fl, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return usageError(stderr, "setup", "unexpected argument: "+rest[0])
	}
	dir, err := filepath.Abs(dataDir(*dataFlag))
	if err != nil {
		fmt.Fprintf(stderr, "setup: %v\n", err)
		return 1
	}
	bin, err := os.Executable()
	if err != nil {
		bin = "/usr/local/bin/tether"
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	w := &setup.Wizard{
		In:           bufio.NewReader(stdin),
		Out:          stdout,
		DataDir:      dir,
		HC:           &http.Client{},
		TelegramBase: telegramBaseURL,
		BinPath:      bin,
		System:       *system,
	}
	// A real terminal lets the wizard hide the API key and the bot token while the user types.
	if f, ok := stdin.(*os.File); ok {
		w.Term = f
	}
	if err := w.Run(ctx); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	return 0
}

// runGenToolsDoc writes docs/TOOLS.md to stdout. The registry has every built-in tool,
// also when Chrome, python3, or the media programs are not on this host. It has no MCP
// tools. Run: go run ./cmd/tether gen-tools-doc > docs/TOOLS.md
func runGenToolsDoc(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "usage: tether gen-tools-doc > docs/TOOLS.md")
		return 2
	}
	r, done, err := setup.ToolsDocRegistry(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer done()
	if _, err := io.WriteString(stdout, tools.GenerateMarkdown(r)); err != nil {
		fmt.Fprintf(stderr, "gen-tools-doc: %v\n", err)
		return 1
	}
	return 0
}
