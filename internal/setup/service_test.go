package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixedTZ makes hostTZ return tz for the test.
func fixedTZ(t *testing.T, tz string) {
	t.Helper()
	old := hostTZ
	hostTZ = func() string { return tz }
	t.Cleanup(func() { hostTZ = old })
}

func TestUnitText(t *testing.T) {
	fixedTZ(t, "Asia/Jakarta")
	u := UnitText("/usr/local/bin/tether", "/home/user/.tether", 180)
	for _, want := range []string{
		"ExecStart=/usr/local/bin/tether gateway --data-dir /home/user/.tether --log-stderr",
		"Restart=on-failure", "RestartSec=5", "Environment=TZ=Asia/Jakarta", "[Install]", "WantedBy=default.target",
	} {
		if !strings.Contains(u, want) {
			t.Fatalf("no %q in\n%s", want, u)
		}
	}
}

func TestUnitTextQuotesPaths(t *testing.T) {
	fixedTZ(t, "UTC")
	u := UnitText("/opt/my tools/tether", "/home/user/100%/data", 180)
	if !strings.Contains(u, `ExecStart="/opt/my tools/tether" gateway --data-dir /home/user/100%%/data --log-stderr`) {
		t.Fatal(u)
	}
}

func TestHostTZFromEnv(t *testing.T) {
	t.Setenv("TZ", ":Europe/Berlin")
	if got := detectTZ(); got != "Europe/Berlin" {
		t.Fatal(got)
	}
}

func TestWriteUserUnitWithBak(t *testing.T) {
	fixedTZ(t, "UTC")
	fakeEUID(t, 1000)
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := WriteUnit(false, false, "/usr/local/bin/tether", "/data/one", 180)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".config", "systemd", "user", "tether.service")
	if path != want {
		t.Fatal(path)
	}
	first, _ := os.ReadFile(path)
	if _, err := WriteUnit(false, false, "/usr/local/bin/tether", "/data/two", 180); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil || string(bak) != string(first) {
		t.Fatal(string(bak), err)
	}
	now, _ := os.ReadFile(path)
	if !strings.Contains(string(now), "--data-dir /data/two") {
		t.Fatal(string(now))
	}
}

func TestWriteSystemUnitNeedsRoot(t *testing.T) {
	fakeEUID(t, 1000)
	if _, err := WriteUnit(true, false, "/usr/local/bin/tether", "/data", 180); err == nil || !strings.Contains(err.Error(), "run with sudo") {
		t.Fatal(err)
	}
}

func TestEnableHint(t *testing.T) {
	user := EnableHint(false)
	for _, want := range []string{"systemctl --user enable --now tether", "loginctl enable-linger $USER"} {
		if !strings.Contains(user, want) {
			t.Fatal(user)
		}
	}
	if sys := EnableHint(true); !strings.Contains(sys, "sudo systemctl enable --now tether") || strings.Contains(sys, "--user") {
		t.Fatal(sys)
	}
}

func TestExampleUnitMatchesGenerator(t *testing.T) {
	fixedTZ(t, exampleTZ)
	b, err := os.ReadFile("../../docs/tether.service")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != UnitText("/usr/local/bin/tether", "/home/YOUR_USER/.tether", 180) {
		t.Fatal("docs/tether.service is out of date")
	}
}

func TestWizardStep5(t *testing.T) {
	fixedTZ(t, "UTC")
	dir := t.TempDir()
	prov := newFakeProvider(t)
	w, out := wizard(t, dir, answers("11", "openai", prov.URL, "sk-test-123", "m", "n", "n", "y"))
	w.GOOS = "linux"
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	home, _ := os.UserHomeDir()
	unit, err := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", "tether.service"))
	if err != nil || !strings.Contains(string(unit), "--data-dir "+dir) {
		t.Fatal(string(unit), err)
	}
	if !strings.Contains(out.String(), "systemctl --user enable --now tether") {
		t.Fatal(out.String())
	}

	// "N" writes nothing, and macOS skips the step.
	for _, goos := range []string{"linux", "darwin"} {
		t.Setenv("HOME", t.TempDir())
		w, out := wizard(t, t.TempDir(), answers("11", "openai", prov.URL, "sk-test-123", "m", "n", "n", "y"))
		w.GOOS = goos
		if goos == "linux" {
			w.In = answers("11", "openai", prov.URL, "sk-test-123", "m", "n", "n", "n")
		}
		if err := w.Run(ctx); err != nil {
			t.Fatal(err, out.String())
		}
		home, _ := os.UserHomeDir()
		if _, err := os.Stat(filepath.Join(home, ".config", "systemd")); err == nil {
			t.Fatal(goos, "unit written")
		}
	}
}

func TestUnitTextRestartLines(t *testing.T) {
	fixedTZ(t, "UTC")
	u := UnitText("/usr/local/bin/tether", "/home/user/.tether", 180)
	for _, want := range []string{
		"ExecReload=/bin/kill -USR1 $MAINPID\n", // not escaped to $$MAINPID
		"Restart=on-failure\n", "RestartSec=5\n",
		"RestartForceExitStatus=75\n", "SuccessExitStatus=75\n", "TimeoutStopSec=210\nKillMode=mixed\n",
	} {
		if !strings.Contains(u, want) {
			t.Fatalf("no %q in\n%s", want, u)
		}
	}
}

func TestStopTimeoutS(t *testing.T) {
	for drain, want := range map[int]int{0: 90, 30: 90, 180: 210, 600: 630} {
		if got := StopTimeoutS(drain); got != want {
			t.Fatalf("StopTimeoutS(%d) = %d, want %d", drain, got, want)
		}
	}
}

func TestServiceStepUsesConfigDrain(t *testing.T) {
	fixedTZ(t, "UTC")
	for _, tc := range []struct{ name, extra, want string }{
		{"key set", "agent:\n  restart_drain_timeout_s: 45\n", "TimeoutStopSec=90\n"},
		{"key missing", "", "TimeoutStopSec=210\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			prov := newFakeProvider(t)
			w, out := wizard(t, dir, answers("11", "openai", prov.URL, "sk-test-123", "m", "n", "n", "n"))
			w.GOOS = "linux"
			if err := w.Run(ctx); err != nil {
				t.Fatal(err, out.String())
			}
			path := filepath.Join(dir, "config.yaml")
			b, _ := os.ReadFile(path)
			if err := os.WriteFile(path, append(b, tc.extra...), 0o600); err != nil {
				t.Fatal(err)
			}
			w, out = wizard(t, dir, answers("", "", "", "", "", "", "", "y"))
			w.GOOS = "linux"
			if err := w.Run(ctx); err != nil {
				t.Fatal(err, out.String())
			}
			home, _ := os.UserHomeDir()
			unit, err := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", "tether.service"))
			if err != nil || !strings.Contains(string(unit), tc.want) {
				t.Fatal(string(unit), err, out.String())
			}
		})
	}
}

// fakeEUID makes geteuid return uid for the test.
func fakeEUID(t *testing.T, uid int) {
	t.Helper()
	old := geteuid
	geteuid = func() int { return uid }
	t.Cleanup(func() { geteuid = old })
}

// tempSystemDir makes systemUnitDir a temporary folder for the test and returns it.
func tempSystemDir(t *testing.T) string {
	t.Helper()
	old := systemUnitDir
	systemUnitDir = t.TempDir()
	t.Cleanup(func() { systemUnitDir = old })
	return systemUnitDir
}

const rootRefusal = "setup: the unit would run the gateway as root. Run setup as your user " +
	"(with sudo for --system), or add --allow-root to run the gateway as root."

func TestCheckRootUnit(t *testing.T) {
	for _, tc := range []struct {
		name              string
		system, allowRoot bool
		euid              int
		sudoUser          string
		refused           bool
	}{
		{"system as root", true, false, 0, "", true},
		{"system with SUDO_USER=root", true, false, 0, "root", true},
		{"system under sudo", true, false, 0, "alice", false},
		{"system with --allow-root", true, true, 0, "", false},
		{"system not root", true, false, 1000, "", false},
		{"user unit as root", false, false, 0, "", true},
		{"user unit under sudo", false, false, 0, "alice", true},
		{"user unit with --allow-root", false, true, 0, "", false},
		{"user unit as a user", false, false, 1000, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeEUID(t, tc.euid)
			t.Setenv("SUDO_USER", tc.sudoUser)
			err := CheckRootUnit(tc.system, tc.allowRoot)
			if tc.refused != (err != nil) || (err != nil && err.Error() != rootRefusal) {
				t.Fatal(err)
			}
		})
	}
}

func TestWriteUnitRefusesRoot(t *testing.T) {
	fixedTZ(t, "UTC")
	fakeEUID(t, 0)
	unit := filepath.Join(tempSystemDir(t), "tether.service")
	t.Setenv("HOME", t.TempDir())
	for _, sudoUser := range []string{"", "root"} {
		t.Setenv("SUDO_USER", sudoUser)
		if _, err := WriteUnit(true, false, "/usr/local/bin/tether", "/data", 180); err == nil || err.Error() != rootRefusal {
			t.Fatal(sudoUser, err)
		}
		if _, err := os.Stat(unit); err == nil {
			t.Fatal("the refused unit was written")
		}
	}
	// --allow-root writes the unit with no User= line.
	path, err := WriteUnit(true, true, "/usr/local/bin/tether", "/data", 180)
	if b, _ := os.ReadFile(unit); err != nil || path != unit || strings.Contains(string(b), "User=") {
		t.Fatal(path, err, string(b))
	}
	// Under sudo, the unit runs as the user who called sudo.
	t.Setenv("SUDO_USER", "alice")
	if _, err := WriteUnit(true, false, "/usr/local/bin/tether", "/data", 180); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(unit); !strings.Contains(string(b), "User=alice\n") {
		t.Fatal(string(b))
	}
	// A user unit as root fails, and passes with allowRoot.
	t.Setenv("SUDO_USER", "")
	if _, err := WriteUnit(false, false, "/usr/local/bin/tether", "/data", 180); err == nil || err.Error() != rootRefusal {
		t.Fatal(err)
	}
	if _, err := WriteUnit(false, true, "/usr/local/bin/tether", "/data", 180); err != nil {
		t.Fatal(err)
	}
}

func TestServiceStepAllowRoot(t *testing.T) {
	fixedTZ(t, "UTC")
	prov := newFakeProvider(t)
	for _, allow := range []bool{false, true} {
		w, out := wizard(t, t.TempDir(), answers("11", "openai", prov.URL, "sk-test-123", "m", "n", "n", "y"))
		fakeEUID(t, 0) // after wizard(), which sets 1000
		w.GOOS, w.AllowRoot = "linux", allow
		err := w.Run(ctx)
		if allow != (err == nil) || (err != nil && err.Error() != rootRefusal) {
			t.Fatal(allow, err, out.String())
		}
	}
}
