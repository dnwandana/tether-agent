package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/dnwandana/tether-agent/internal/gatewaylock"
	"github.com/dnwandana/tether-agent/internal/update"
)

// newUpdater builds the Updater of the update command. Tests replace it.
var newUpdater = defaultUpdater

// systemUnitPath is the system-wide unit of the gateway. When it exists, the restart hint
// gives the sudo command. Tests replace it.
var systemUnitPath = "/etc/systemd/system/tether.service"

// geteuid returns the effective user ID. Tests replace it.
var geteuid = os.Geteuid

// lookupHome returns the home folder of the OS user name. Tests replace it.
var lookupHome = func(name string) (string, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return "", err
	}
	return u.HomeDir, nil
}

// userUnitPath returns the user unit of the gateway. Tests replace it.
var userUnitPath = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "systemd", "user", "tether.service")
}

// runSystemctl runs systemctl with args. Tests replace it.
var runSystemctl = func(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// defaultUpdater returns the Updater for this binary and the GitHub releases of the
// repository. Progress lines go to stdout.
func defaultUpdater(stdout io.Writer) *update.Updater {
	return &update.Updater{
		Current: version,
		Client:  &update.Client{UserAgent: "tether-agent/" + version},
		Out:     stdout,
	}
}

// runUpdate replaces this binary with a GitHub release. It never reads or writes the data
// folder. It only probes gateway.lock to find a running gateway.
func runUpdate(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("update", flag.ContinueOnError)
	check := fl.Bool("check", false, "")
	tag := fl.String("tag", "", "")
	force := fl.Bool("force", false, "")
	dir := fl.String("data-dir", "", "")
	restart := fl.Bool("restart", false, "")
	noRestart := fl.Bool("no-restart", false, "")
	rest, code, ok := parseCommand(fl, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return usageError(stderr, "update", "unexpected argument: "+rest[0])
	}
	if *restart && *noRestart {
		return usageError(stderr, "update", "--restart and --no-restart cannot be used together")
	}
	// Updater.Run adds the v prefix to a tag without one.
	opts := update.Options{Tag: *tag, Check: *check, Force: *force}

	// An interrupt cancels the download, so the install does not start with a part file.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := newUpdater(stdout).Run(ctx, opts)
	if err != nil {
		fmt.Fprintf(stderr, "update: %v\n", err)
		return 1
	}

	switch res.State {
	case update.StateNewer:
		fmt.Fprintf(stdout, "tether %s is available; you run %s. Run tether update to install it.\n",
			res.Available, res.Current)
	case update.StateSame:
		fmt.Fprintf(stdout, "tether %s is the newest release\n", res.Current)
	case update.StateAhead:
		fmt.Fprintf(stdout, "tether %s is newer than the newest release, %s\n", res.Current, res.Available)
	case update.StateDev:
		fmt.Fprintf(stdout, "this is a dev build; the newest release is %s\n", res.Available)
	case update.StateUpdated:
		fmt.Fprintf(stdout, "updated tether %s → %s at %s\n", res.Current, res.Available, res.BinPath)
		// gatewaylock.Held opens the file read-only and never creates it. An error gives no hint.
		held, err := gatewaylock.Held(filepath.Join(updateDataDir(*dir), "gateway.lock"))
		if err == nil && held {
			return restartGateway(ctx, stdout, stderr, res.Available, *restart, *noRestart)
		}
	}
	return 0
}

// restartGateway restarts the running gateway after an update, or prints the hint. The order
// of the checks is: the gateway runs this command, --no-restart or a user unit under sudo,
// --restart, a terminal that answers the question, and else the hint. Hermes restarts every gateway after an update with
// no question. Here the owner chooses, because a restart ends the running turns.
func restartGateway(ctx context.Context, stdout, stderr io.Writer, newVersion string, force, skip bool) int {
	if os.Getenv("TETHER_GATEWAY") == "1" {
		fmt.Fprintln(stdout, "the gateway runs this command, so it cannot restart itself. Send /restart in the chat.")
		return 0
	}
	_, statErr := os.Stat(systemUnitPath)
	systemUnit := statErr == nil
	hint := restartHint(runtime.GOOS, systemUnit)
	if skip || !systemUnit && sudoUserUnit() {
		fmt.Fprintln(stdout, hint)
		return 0
	}
	if !force {
		if !stdioIsTerminal() {
			fmt.Fprintln(stdout, hint)
			return 0
		}
		p := &termPrompter{ctx: ctx, r: bufio.NewReader(stdin), out: stdout}
		yes, err := p.ConfirmYes("Restart the gateway now? The running turns get time to end first.")
		if err != nil || !yes {
			fmt.Fprintln(stdout, hint)
			return 0
		}
	}

	var args []string
	switch {
	case runtime.GOOS != "linux":
		fmt.Fprintln(stdout, "the gateway does not run under systemd; stop it and start it again")
		return 0
	case systemUnit && geteuid() == 0:
		args = []string{"restart", "tether"}
	case systemUnit:
		fmt.Fprintln(stdout, hint)
		return 0
	default:
		if _, err := os.Stat(userUnitPath()); err != nil {
			fmt.Fprintln(stdout, "the gateway does not run under systemd; stop it and start it again")
			return 0
		}
		args = []string{"--user", "restart", "tether"}
	}
	command := "systemctl " + strings.Join(args, " ")
	fmt.Fprintf(stdout, "restarting the gateway: %s\n", command)
	if err := runSystemctl(ctx, stdout, stderr, args...); err != nil {
		fmt.Fprintf(stderr, "update: the binary is updated, but the restart failed: %v. Run: %s\n", err, command)
		return 1
	}
	fmt.Fprintf(stdout, "the gateway restarted with tether %s\n", newVersion)
	return 0
}

// sudoUserUnit reports whether the process runs as root under sudo and the user unit of
// SUDO_USER exists. systemctl as root cannot restart the user unit of another user, so the
// command prints the hint for that user and does not ask.
func sudoUserUnit() bool {
	name := os.Getenv("SUDO_USER")
	if name == "" || geteuid() != 0 {
		return false
	}
	home, err := lookupHome(name)
	if err != nil || home == "" {
		return false
	}
	_, err = os.Stat(filepath.Join(home, ".config", "systemd", "user", "tether.service"))
	return err == nil
}

// updateDataDir returns the data folder in which update looks for gateway.lock. The order is
// --data-dir, TETHER_HOME, the home of SUDO_USER when the process runs as root, and then
// ~/.tether. The sudo rule exists because sudo sets the home folder to the home of root.
func updateDataDir(flagValue string) string {
	if flagValue != "" || os.Getenv("TETHER_HOME") != "" {
		return dataDir(flagValue)
	}
	if name := os.Getenv("SUDO_USER"); name != "" && geteuid() == 0 {
		if home, err := lookupHome(name); err == nil && home != "" {
			return filepath.Join(home, ".tether")
		}
	}
	return dataDir("")
}

// restartHint returns the line that tells the user to restart a running gateway. systemUnit
// is true when the system-wide unit of the gateway exists. The command only prints the hint
// and never runs systemctl.
func restartHint(goos string, systemUnit bool) string {
	const head = "the gateway still runs the old version; "
	switch {
	case goos == "linux" && systemUnit:
		return head + "restart it: sudo systemctl restart tether"
	case goos == "linux":
		return head + "restart it: systemctl --user restart tether"
	}
	return head + "stop and start the gateway"
}
