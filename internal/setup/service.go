package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// unitName is the file name of the systemd unit.
const unitName = "tether.service"

// systemUnitDir is the folder of the system units.
const systemUnitDir = "/etc/systemd/system"

// exampleTZ is the time zone in the example unit docs/tether.service.
const exampleTZ = "UTC"

// hostTZ returns the time zone of the host for the unit. Tests replace it.
var hostTZ = detectTZ

// geteuid returns the effective user id. Tests replace it.
var geteuid = os.Geteuid

// detectTZ returns the IANA time zone of the host: TZ, then /etc/timezone, then the
// target of the /etc/localtime link. The fallback is UTC.
func detectTZ() string {
	if tz := strings.TrimPrefix(os.Getenv("TZ"), ":"); tz != "" {
		return tz
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		if tz := strings.TrimSpace(string(b)); tz != "" {
			return tz
		}
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if _, tz, ok := strings.Cut(target, "zoneinfo/"); ok && tz != "" {
			return tz
		}
	}
	return "UTC"
}

// UnitText returns the systemd unit that runs the gateway. The logs go to stderr, so
// journald keeps them. TZ comes from the host, because the cron schedules use
// the local time.
func UnitText(binPath, dataDir string) string {
	return unitText(binPath, dataDir, hostTZ(), "")
}

// unitText returns the unit. A user that is not empty adds a User= line.
func unitText(binPath, dataDir, tz, user string) string {
	var b strings.Builder
	b.WriteString("# tether gateway: the Telegram channel and the cron scheduler.\n")
	b.WriteString("# tether setup writes this file. TZ sets the time zone of the cron schedules.\n")
	b.WriteString("[Unit]\n")
	b.WriteString("Description=tether gateway\n")
	b.WriteString("After=network-online.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	if user != "" {
		fmt.Fprintf(&b, "User=%s\n", user)
	}
	fmt.Fprintf(&b, "ExecStart=%s gateway --data-dir %s --log-stderr\n", systemdArg(binPath), systemdArg(dataDir))
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=5\n")
	fmt.Fprintf(&b, "Environment=TZ=%s\n\n", systemdArg(tz))
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.String()
}

// systemdArg escapes one argument of a unit line. "%" and "$" are special in systemd. An
// argument with spaces, quotes, or a backslash goes in double quotes.
func systemdArg(s string) string {
	s = strings.NewReplacer("%", "%%", "$", "$$").Replace(s)
	if !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// unitPath returns the path of the user unit or the system unit.
func unitPath(system bool) (string, error) {
	if system {
		return filepath.Join(systemUnitDir, unitName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("setup: cannot find the home folder for the user unit: %w", err)
	}
	return filepath.Join(home, ".config", "systemd", "user", unitName), nil
}

// WriteUnit writes the systemd unit and returns its path. The default is a user unit in
// ~/.config/systemd/user. With system, it writes a system unit, which needs root. Before
// it replaces a unit, it copies the old unit to a .bak file.
func WriteUnit(system bool, binPath, dataDir string) (path string, err error) {
	user := ""
	if system {
		if geteuid() != 0 {
			return "", errors.New("setup: a system unit needs root; run with sudo, or write a user unit without --system")
		}
		// Under sudo, the service runs as the user who called sudo, not as root.
		if u := os.Getenv("SUDO_USER"); u != "" && u != "root" {
			user = u
		}
	}
	if path, err = unitPath(system); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("setup: cannot make the folder %s: %w", filepath.Dir(path), err)
	}
	old, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := atomicWrite(path+".bak", old, 0o644); err != nil {
			return "", err
		}
	case !errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("setup: cannot read %s for the backup: %w", path, err)
	}
	if err := atomicWrite(path, []byte(unitText(binPath, dataDir, hostTZ(), user)), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// EnableHint returns the commands that start the service now and at each boot. The
// wizard only prints them. It never runs systemctl.
func EnableHint(system bool) string {
	if system {
		return "sudo systemctl daemon-reload\n" +
			"sudo systemctl enable --now tether\n" +
			"journalctl -u tether -f    # shows the logs\n"
	}
	return "systemctl --user daemon-reload\n" +
		"systemctl --user enable --now tether\n" +
		"loginctl enable-linger $USER     # keeps the service on after you log out\n" +
		"journalctl --user -u tether -f    # shows the logs\n"
}

// serviceStep is step 5: it writes the systemd unit when the user agrees. It runs only on
// Linux.
func (w *Wizard) serviceStep() error {
	goos := w.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos != "linux" {
		w.say("systemd is only on Linux, so this step does nothing on %s.", goos)
		return nil
	}
	yes, err := w.confirm("Write the systemd unit?")
	if err != nil || !yes {
		return err
	}
	bin := w.BinPath
	if bin == "" {
		bin = "/usr/local/bin/tether"
	}
	path, err := WriteUnit(w.System, bin, w.DataDir)
	if err != nil {
		return err
	}
	w.say("Wrote %s. To start the service now and at each boot, run:\n%s", path, EnableHint(w.System))
	return nil
}
