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
	u := UnitText("/usr/local/bin/tether", "/home/user/.tether")
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
	u := UnitText("/opt/my tools/tether", "/home/user/100%/data")
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
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := WriteUnit(false, "/usr/local/bin/tether", "/data/one")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".config", "systemd", "user", "tether.service")
	if path != want {
		t.Fatal(path)
	}
	first, _ := os.ReadFile(path)
	if _, err := WriteUnit(false, "/usr/local/bin/tether", "/data/two"); err != nil {
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
	old := geteuid
	geteuid = func() int { return 1000 }
	t.Cleanup(func() { geteuid = old })
	if _, err := WriteUnit(true, "/usr/local/bin/tether", "/data"); err == nil || !strings.Contains(err.Error(), "run with sudo") {
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
	if string(b) != UnitText("/usr/local/bin/tether", "/home/YOUR_USER/.tether") {
		t.Fatal("docs/tether.service is out of date")
	}
}

func TestWizardStep5(t *testing.T) {
	fixedTZ(t, "UTC")
	dir := t.TempDir()
	prov := newFakeProvider(t)
	w, out := wizard(t, dir, answers("openai", prov.URL, "sk-test-123", "m", "n", "n", "y"))
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
		w, out := wizard(t, t.TempDir(), answers("openai", prov.URL, "sk-test-123", "m", "n", "n", "y"))
		w.GOOS = goos
		if goos == "linux" {
			w.In = answers("openai", prov.URL, "sk-test-123", "m", "n", "n", "n")
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
