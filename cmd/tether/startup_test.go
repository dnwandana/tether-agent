package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startupLimit is the maximum startup time of the binary.
const startupLimit = 100 * time.Millisecond

// TestStartupUnder100ms runs the release binary and checks the startup time
// of "version", and of "chat" up to its prompt with a closed stdin. App.New
// must do no network work, so the chat ends at once. The test takes the best
// of a few runs after one warm-up run, so a busy machine does not make it
// fail by chance.
func TestStartupUnder100ms(t *testing.T) {
	if testing.Short() {
		t.Skip("the test builds the binary")
	}
	bin := buildBinary(t)
	dir := minimalDataDir(t)
	env := startupEnv(t, dir)
	for _, args := range [][]string{
		{"version"},
		{"chat", "--data-dir", dir},
	} {
		best := time.Hour
		for i := 0; i < 6; i++ {
			cmd := exec.Command(bin, args...)
			cmd.Env = env
			cmd.Stdin = nil // /dev/null: the chat reads EOF at its prompt.
			start := time.Now()
			out, err := cmd.CombinedOutput()
			took := time.Since(start)
			if err != nil {
				t.Fatalf("%v: %v\n%s", args, err, out)
			}
			// The first run fills the page cache. Do not count it.
			if i > 0 && took < best {
				best = took
			}
		}
		if args[0] == "chat" {
			// App.New makes state.db, so the chat got past its setup.
			if _, err := os.Stat(filepath.Join(dir, "state.db")); err != nil {
				t.Fatalf("chat did not open state.db: %v", err)
			}
		}
		t.Logf("%s: best startup %v (limit %v)", args[0], best, startupLimit)
		if best > startupLimit {
			t.Errorf("%s: startup took %v, limit %v", args[0], best, startupLimit)
		}
	}
}

// buildBinary builds the binary with the release flags into a temporary
// folder and returns its path.
func buildBinary(t *testing.T) string {
	t.Helper()
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("the go command is not in PATH")
	}
	bin := filepath.Join(t.TempDir(), "tether")
	cmd := exec.Command(gobin, "build", "-trimpath", "-ldflags", "-s -w", "-o", bin, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// minimalDataDir returns a data folder with the smallest config that lets
// the chat start: one provider on a closed local port and no pricing sources,
// so nothing goes to the network.
func minimalDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cfg := strings.Join([]string{
		"providers:",
		"  primary:",
		"    format: openai",
		"    base_url: http://127.0.0.1:1",
		"    api_key: k",
		"    model: m",
		"pricing:",
		"  sources: []",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// startupEnv returns an environment that keeps the binary away from the
// files of the user: HOME and TETHER_HOME point into the test folders.
func startupEnv(t *testing.T, dir string) []string {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "HOME", "TETHER_HOME", "TETHER_PPROF", "TELEGRAM_BOT_TOKEN":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HOME="+t.TempDir(), "TETHER_HOME="+dir)
}
