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
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/dnwandana/tether-agent/internal/app"
	"github.com/dnwandana/tether-agent/internal/config"
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
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
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

// shutdownWait is the maximum time that the gateway waits for the running turns at a stop.
// The turns that still run after it are cancelled.
const shutdownWait = 10 * time.Second

// shutdownGrace is the maximum time that the gateway waits for the cancelled work at a
// stop. Hermes also waits 5 s after it interrupts the running agents.
const shutdownGrace = 5 * time.Second

// notifyContext is signal.NotifyContext. Tests replace it.
var notifyContext = signal.NotifyContext

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

	<-ctx.Done()
	// Restore the default signal action now, so that a second Ctrl-C ends the process.
	stop()
	log.Info("gateway stopping")
	waits := []stopWait{
		{name: "channels", wait: waitChan(channelsDone)},
		{name: "cron", wait: waitChan(schedDone)},
		{name: "background reviews", wait: a.Agent.WaitReviews},
	}
	if a.Batches != nil {
		waits = append(waits, stopWait{name: "background delegations", wait: a.Batches.Wait})
	}
	gatewayStop{
		drain:       shutdownWait,
		grace:       shutdownGrace,
		stopReceive: stopRecv,
		waitTurns:   gw.Wait,
		stopWork:    stopWork,
		waits:       waits,
		log:         log,
	}.run()
	// The deferred a.Close closes MCP, Chrome, and the database last.
	log.Info("gateway stopped")
	return 0
}

// gatewayStop holds the steps of a gateway stop.
type gatewayStop struct {
	// drain is the maximum time for the running turns. grace is the maximum time for the
	// cancelled work.
	drain, grace time.Duration
	// stopReceive stops the receive loops of the channels.
	stopReceive func()
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
	s.stopReceive()
	if !s.waitTurns(s.drain) {
		s.log.Warn("gateway: some turns did not end in time, so the gateway cancels them", "wait", s.drain.String())
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

// runSkills runs the skills subcommand and returns the exit code.
func runSkills(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("skills", flag.ContinueOnError)
	dataFlag := fl.String("data-dir", "", "data folder (default: TETHER_HOME, then ~/.tether)")
	rest, code, ok := parseCommand(fl, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(rest) == 0 {
		return usageError(stderr, "skills", "a command is needed: list or reset")
	}
	dir := dataDir(*dataFlag)
	skillsDir := filepath.Join(dir, "skills")
	switch rest[0] {
	case "list":
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
	default:
		return usageError(stderr, "skills", "unknown command: "+rest[0])
	}
}

// skillsList prints the skills that a session loads. A config error is a
// warning, because the local skills do not need the config.
func skillsList(dir, skillsDir string, stdout, stderr io.Writer) int {
	cfg := config.Default()
	dotenv, err := security.LoadDotEnv(filepath.Join(dir, ".env"))
	if err == nil {
		var loaded *config.Config
		loaded, err = config.Load(filepath.Join(dir, "config.yaml"), security.EnvLookup(dotenv))
		if err == nil {
			cfg = *loaded
		}
	}
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
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSOURCE\tDESCRIPTION")
	for _, s := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Name, skillSource(s), shortText(s.Description, 100))
	}
	if err := tw.Flush(); err != nil {
		return 1
	}
	return 0
}

// skillSource names where a skill comes from.
func skillSource(s skills.Skill) string {
	switch {
	case s.Trust == skills.TrustBuiltin:
		return "builtin"
	case s.External:
		return "external"
	default:
		return "local"
	}
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
