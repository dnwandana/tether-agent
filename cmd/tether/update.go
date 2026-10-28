package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/dnwandana/tether-agent/internal/gateway"
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
	rest, code, ok := parseCommand(fl, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return usageError(stderr, "update", "unexpected argument: "+rest[0])
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
		// LockHeld opens the file read-only and never creates it. An error gives no hint.
		held, err := gateway.LockHeld(filepath.Join(updateDataDir(*dir), "gateway.lock"))
		if err == nil && held {
			_, statErr := os.Stat(systemUnitPath)
			fmt.Fprintln(stdout, restartHint(runtime.GOOS, statErr == nil))
		}
	}
	return 0
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
