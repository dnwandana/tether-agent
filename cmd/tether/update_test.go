package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/gatewaylock"
	"github.com/dnwandana/tether-agent/internal/update"
)

// fakeReleaseServer serves one linux/amd64 release of version. It records the path of each
// request. The archive holds a script that prints "tether <version>", so the version check
// of the new binary passes.
func fakeReleaseServer(t *testing.T, version string) (*httptest.Server, *[]string) {
	t.Helper()
	asset := "tether-agent_" + version + "_linux_amd64.tar.gz"
	archive := tarGzEntry(t, "tether", []byte("#!/bin/sh\necho \"tether "+version+"\"\n"))
	sum := sha256.Sum256(archive)
	sums := hex.EncodeToString(sum[:]) + "  " + asset + "\n"

	var mu sync.Mutex
	var paths []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/releases/latest", "/releases/tags/v" + version:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name":     "v" + version,
				"published_at": "2026-10-01T00:00:00Z",
				"assets": []map[string]string{
					{"name": asset, "browser_download_url": srv.URL + "/dl/" + asset},
					{"name": "checksums.txt", "browser_download_url": srv.URL + "/dl/checksums.txt"},
				},
			})
		case "/dl/checksums.txt":
			_, _ = io.WriteString(w, sums)
		case "/dl/" + asset:
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &paths
}

// tarGzEntry returns a .tar.gz with one regular entry of mode 0o755.
func tarGzEntry(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// writeFakeBinary writes <tmp>/tether with the content "old" and returns its path.
func writeFakeBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "tether")
	if err := os.WriteFile(bin, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// useUpdater makes runUpdate use the server srv, the binary bin, and the version cur. It
// also points systemUnitPath to a file that does not exist.
func useUpdater(t *testing.T, srv *httptest.Server, bin, cur string) {
	t.Helper()
	newUpdater = func(stdout io.Writer) *update.Updater {
		return &update.Updater{
			Current: cur,
			GOOS:    "linux",
			GOARCH:  "amd64",
			BinPath: bin,
			Client:  &update.Client{APIBase: srv.URL},
			Out:     stdout,
		}
	}
	oldUnit := systemUnitPath
	systemUnitPath = filepath.Join(t.TempDir(), "tether.service")
	t.Cleanup(func() {
		newUpdater = defaultUpdater
		systemUnitPath = oldUnit
	})
}

func TestUpdateCheckNewer(t *testing.T) {
	srv, paths := fakeReleaseServer(t, "0.9.0")
	bin := writeFakeBinary(t)
	useUpdater(t, srv, bin, "0.4.0")
	var out, errb bytes.Buffer
	if code := run([]string{"update", "--check"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if out.String() != "tether 0.9.0 is available; you run 0.4.0. Run tether update to install it.\n" {
		t.Fatal(out.String())
	}
	for _, p := range *paths {
		if strings.HasPrefix(p, "/dl/") {
			t.Fatalf("--check downloaded %s", p)
		}
	}
	if b, _ := os.ReadFile(bin); string(b) != "old" {
		t.Fatalf("binary changed: %q", b)
	}
}

func TestUpdateInstallsAndHintsRestart(t *testing.T) {
	srv, _ := fakeReleaseServer(t, "0.9.0")
	bin := writeFakeBinary(t)
	useUpdater(t, srv, bin, "0.4.0")
	dir := t.TempDir()
	rel, err := gatewaylock.Acquire(filepath.Join(dir, "gateway.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	var out, errb bytes.Buffer
	if code := run([]string{"update", "--data-dir", dir}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "checksum OK\nupdated tether 0.4.0 → 0.9.0 at "+bin+"\n") {
		t.Fatal(out.String())
	}
	if !strings.HasSuffix(out.String(), restartHint(runtime.GOOS, false)+"\n") {
		t.Fatal(out.String())
	}
}

func TestUpdateNoLockFileNoHint(t *testing.T) {
	srv, _ := fakeReleaseServer(t, "0.9.0")
	useUpdater(t, srv, writeFakeBinary(t), "0.4.0")
	dir := t.TempDir()
	// A data folder that does not exist must stay absent: update creates nothing in it.
	missing := filepath.Join(t.TempDir(), "absent")
	for _, d := range []string{dir, missing} {
		var out, errb bytes.Buffer
		if code := run([]string{"update", "--data-dir", d}, &out, &errb); code != 0 {
			t.Fatalf("%s: exit %d: %s", d, code, errb.String())
		}
		if strings.Contains(out.String(), "gateway still runs") {
			t.Fatalf("%s: unexpected hint: %s", d, out.String())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "gateway.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("gateway.lock exists after the run: %v", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("update created the data folder: %v", err)
	}
}

func TestUpdateSame(t *testing.T) {
	srv, _ := fakeReleaseServer(t, "0.9.0")
	useUpdater(t, srv, writeFakeBinary(t), "0.9.0")
	var out, errb bytes.Buffer
	if code := run([]string{"update"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if out.String() != "tether 0.9.0 is the newest release\n" {
		t.Fatal(out.String())
	}
}

func TestUpdateAhead(t *testing.T) {
	srv, _ := fakeReleaseServer(t, "0.9.0")
	useUpdater(t, srv, writeFakeBinary(t), "1.0.0")
	var out, errb bytes.Buffer
	if code := run([]string{"update"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if out.String() != "tether 1.0.0 is newer than the newest release, 0.9.0\n" {
		t.Fatal(out.String())
	}
}

func TestUpdateDevCheck(t *testing.T) {
	srv, _ := fakeReleaseServer(t, "0.9.0")
	useUpdater(t, srv, writeFakeBinary(t), "dev")
	var out, errb bytes.Buffer
	if code := run([]string{"update", "--check"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if out.String() != "this is a dev build; the newest release is 0.9.0\n" {
		t.Fatal(out.String())
	}
}

func TestUpdateTagWithoutV(t *testing.T) {
	srv, paths := fakeReleaseServer(t, "0.3.0")
	useUpdater(t, srv, writeFakeBinary(t), "0.4.0")
	var out, errb bytes.Buffer
	if code := run([]string{"update", "--tag", "0.3.0", "--force"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !slices.Contains(*paths, "/releases/tags/v0.3.0") {
		t.Fatalf("paths %v", *paths)
	}
}

func TestUpdateBadUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"update", "extra"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
	want := "update: unexpected argument: extra\nusage: tether update [flags]\nRun \"tether update help\" for the details.\n"
	if errb.String() != want {
		t.Fatalf("stderr %q", errb.String())
	}
	if out.Len() != 0 {
		t.Fatalf("stdout %q", out.String())
	}
}

func TestUpdateHelpFlag(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"update", "--check", "--help"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if out.String() != commandHelp("update") {
		t.Fatalf("stdout %q", out.String())
	}
}

func TestUpdateServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	useUpdater(t, srv, writeFakeBinary(t), "0.4.0")
	var out, errb bytes.Buffer
	if code := run([]string{"update"}, &out, &errb); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.HasPrefix(errb.String(), "update: GitHub returned 500") {
		t.Fatalf("stderr %q", errb.String())
	}
}

func TestRestartHint(t *testing.T) {
	for _, c := range []struct {
		goos string
		sys  bool
		want string
	}{
		{"linux", true, "the gateway still runs the old version; restart it: sudo systemctl restart tether"},
		{"linux", false, "the gateway still runs the old version; restart it: systemctl --user restart tether"},
		{"darwin", false, "the gateway still runs the old version; stop and start the gateway"},
	} {
		if got := restartHint(c.goos, c.sys); got != c.want {
			t.Errorf("%+v: %q", c, got)
		}
	}
}

func TestUpdateDataDirUnderSudo(t *testing.T) {
	oldEUID, oldLookup := geteuid, lookupHome
	t.Cleanup(func() { geteuid, lookupHome = oldEUID, oldLookup })
	t.Setenv("TETHER_HOME", "")
	t.Setenv("SUDO_USER", "alice")
	lookupHome = func(name string) (string, error) {
		if name != "alice" {
			t.Fatalf("lookup %q", name)
		}
		return "/home/alice", nil
	}

	geteuid = func() int { return 0 }
	if got := updateDataDir(""); got != "/home/alice/.tether" {
		t.Fatalf("root with SUDO_USER: %q", got)
	}
	if got := updateDataDir("/x"); got != "/x" {
		t.Fatalf("flag: %q", got)
	}
	t.Setenv("TETHER_HOME", "/th")
	if got := updateDataDir(""); got != "/th" {
		t.Fatalf("TETHER_HOME: %q", got)
	}
	t.Setenv("TETHER_HOME", "")

	geteuid = func() int { return 1000 }
	if got := updateDataDir(""); got != dataDir("") {
		t.Fatalf("not root: %q, want %q", got, dataDir(""))
	}
}

func TestDefaultUpdater(t *testing.T) {
	var out bytes.Buffer
	u := defaultUpdater(&out)
	if u.Current != version || u.Out != &out {
		t.Fatalf("%+v", u)
	}
	if u.Client == nil || u.Client.UserAgent != "tether-agent/"+version || u.Client.APIBase != "" {
		t.Fatalf("client %+v", u.Client)
	}
}

const restartQuestion = "Restart the gateway now? The running turns get time to end first. [Y/n] "

// restartEnv is the setup of a restart test: a held gateway.lock, unit files that the test
// controls, and a recorder in place of systemctl.
type restartEnv struct {
	dir   string
	calls [][]string
	err   error
}

// setupRestart prepares runUpdate for a restart test. sysUnit and userUnit say which unit
// files exist. uid is the effective user ID.
func setupRestart(t *testing.T, sysUnit, userUnit bool, uid int) *restartEnv {
	t.Helper()
	srv, _ := fakeReleaseServer(t, "0.9.0")
	useUpdater(t, srv, writeFakeBinary(t), "0.4.0")
	t.Setenv("TETHER_GATEWAY", "")
	env := &restartEnv{dir: t.TempDir()}
	rel, err := gatewaylock.Acquire(filepath.Join(env.dir, "gateway.lock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rel() })
	unitDir := t.TempDir()
	sysPath := filepath.Join(unitDir, "system.service")
	userPath := filepath.Join(unitDir, "user.service")
	if sysUnit {
		if err := os.WriteFile(sysPath, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if userUnit {
		if err := os.WriteFile(userPath, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	oldSys, oldUser, oldRun, oldUID := systemUnitPath, userUnitPath, runSystemctl, geteuid
	systemUnitPath = sysPath
	userUnitPath = func() string { return userPath }
	runSystemctl = func(_ context.Context, _, _ io.Writer, args ...string) error {
		env.calls = append(env.calls, args)
		return env.err
	}
	geteuid = func() int { return uid }
	t.Cleanup(func() {
		systemUnitPath, userUnitPath, runSystemctl, geteuid = oldSys, oldUser, oldRun, oldUID
	})
	return env
}

func (e *restartEnv) update(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(append([]string{"update", "--data-dir", e.dir}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func TestUpdateAsksAndRestartsUserUnit(t *testing.T) {
	env := setupRestart(t, false, true, 1000)
	withTerminal(t, true, "\n")
	code, out, errs := env.update()
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	want := restartQuestion + "restarting the gateway: systemctl --user restart tether\n" +
		"the gateway restarted with tether 0.9.0\n"
	if !strings.HasSuffix(out, want) {
		t.Fatal(out)
	}
	if !slices.Equal(env.calls[0], []string{"--user", "restart", "tether"}) || len(env.calls) != 1 {
		t.Fatalf("calls %v", env.calls)
	}
}

func TestUpdateAnswers(t *testing.T) {
	tests := []struct {
		input     string
		restart   bool
		questions int
	}{
		{"y\n", true, 1},
		{"yes\n", true, 1},
		{"n\n", false, 1},
		{"no\n", false, 1},
		{"maybe\ny\n", true, 2},
		{"", false, 1},
	}
	for _, tt := range tests {
		t.Run(strings.TrimSpace(tt.input), func(t *testing.T) {
			env := setupRestart(t, false, true, 1000)
			withTerminal(t, true, tt.input)
			code, out, errs := env.update()
			if code != 0 {
				t.Fatalf("exit %d: %s", code, errs)
			}
			if n := strings.Count(out, restartQuestion); n != tt.questions {
				t.Fatalf("question asked %d times: %s", n, out)
			}
			if tt.restart != (len(env.calls) == 1) {
				t.Fatalf("calls %v", env.calls)
			}
			if hint := restartHint(runtime.GOOS, false); tt.restart == strings.Contains(out, hint) {
				t.Fatalf("hint mismatch: %s", out)
			}
		})
	}
}

// TestUpdateAnswerInterrupt checks that an interrupt at the question is a no. The Enter
// after the interrupt must not count as a yes.
func TestUpdateAnswerInterrupt(t *testing.T) {
	env := setupRestart(t, false, true, 1000)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	withTerminal(t, true, "")
	stdin = pr
	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = pw.Write([]byte("\n"))
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errb bytes.Buffer
	if code := restartGateway(ctx, &out, &errb, "0.9.0", false, false); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if len(env.calls) != 0 || !strings.HasSuffix(out.String(), restartHint(runtime.GOOS, false)+"\n") {
		t.Fatalf("out %q calls %v", out.String(), env.calls)
	}
}

func TestUpdateRestartFlags(t *testing.T) {
	t.Run("restart without terminal", func(t *testing.T) {
		env := setupRestart(t, false, true, 1000)
		withTerminal(t, false, "")
		code, out, errs := env.update("--restart")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errs)
		}
		if strings.Contains(out, "[Y/n]") || len(env.calls) != 1 {
			t.Fatalf("out %q calls %v", out, env.calls)
		}
	})
	t.Run("no-restart on terminal", func(t *testing.T) {
		env := setupRestart(t, false, true, 1000)
		withTerminal(t, true, "y\n")
		code, out, errs := env.update("--no-restart")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errs)
		}
		if strings.Contains(out, "[Y/n]") || len(env.calls) != 0 ||
			!strings.HasSuffix(out, restartHint(runtime.GOOS, false)+"\n") {
			t.Fatalf("out %q calls %v", out, env.calls)
		}
	})
	t.Run("both flags", func(t *testing.T) {
		env := setupRestart(t, false, true, 1000)
		code, _, errs := env.update("--restart", "--no-restart")
		if code != 2 {
			t.Fatalf("exit %d", code)
		}
		if !strings.HasPrefix(errs, "update: --restart and --no-restart cannot be used together\n") {
			t.Fatal(errs)
		}
		if len(env.calls) != 0 {
			t.Fatalf("calls %v", env.calls)
		}
	})
}

func TestUpdateNoTerminalKeepsTheHint(t *testing.T) {
	env := setupRestart(t, false, true, 1000)
	withTerminal(t, false, "")
	code, out, errs := env.update()
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if strings.Contains(out, "[Y/n]") || len(env.calls) != 0 ||
		!strings.HasSuffix(out, restartHint(runtime.GOOS, false)+"\n") {
		t.Fatalf("out %q calls %v", out, env.calls)
	}
}

func TestUpdateUnitCases(t *testing.T) {
	t.Run("system unit as root", func(t *testing.T) {
		env := setupRestart(t, true, false, 0)
		code, _, errs := env.update("--restart")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errs)
		}
		if len(env.calls) != 1 || !slices.Equal(env.calls[0], []string{"restart", "tether"}) {
			t.Fatalf("calls %v", env.calls)
		}
	})
	t.Run("system unit as user", func(t *testing.T) {
		env := setupRestart(t, true, false, 1000)
		code, out, errs := env.update("--restart")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errs)
		}
		if len(env.calls) != 0 || !strings.HasSuffix(out, restartHint("linux", true)+"\n") {
			t.Fatalf("out %q calls %v", out, env.calls)
		}
	})
	t.Run("no unit", func(t *testing.T) {
		env := setupRestart(t, false, false, 1000)
		code, out, errs := env.update("--restart")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errs)
		}
		want := "the gateway does not run under systemd; stop it and start it again\n"
		if len(env.calls) != 0 || !strings.HasSuffix(out, want) {
			t.Fatalf("out %q calls %v", out, env.calls)
		}
	})
}

func TestUpdateInsideTheGateway(t *testing.T) {
	env := setupRestart(t, false, true, 1000)
	withTerminal(t, true, "y\n")
	t.Setenv("TETHER_GATEWAY", "1")
	code, out, errs := env.update("--restart")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	want := "the gateway runs this command, so it cannot restart itself. Send /restart in the chat.\n"
	if !strings.Contains(out, "updated tether 0.4.0 → 0.9.0") || !strings.HasSuffix(out, want) ||
		strings.Contains(out, "[Y/n]") || len(env.calls) != 0 {
		t.Fatalf("out %q calls %v", out, env.calls)
	}
}

func TestUpdateRestartFails(t *testing.T) {
	env := setupRestart(t, false, true, 1000)
	env.err = errors.New("unit not found")
	code, out, errs := env.update("--restart")
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	want := "update: the binary is updated, but the restart failed: unit not found. Run: systemctl --user restart tether\n"
	if errs != want {
		t.Fatalf("stderr %q", errs)
	}
	if !strings.Contains(out, "updated tether 0.4.0 → 0.9.0") {
		t.Fatal(out)
	}
}

func TestUpdateSudoWithUserUnit(t *testing.T) {
	for _, tt := range []struct {
		name  string
		args  []string
		input string
	}{
		{"terminal", nil, "y\n"},
		{"restart flag", []string{"--restart"}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := setupRestart(t, false, false, 0)
			withTerminal(t, true, tt.input)
			home := t.TempDir()
			unitDir := filepath.Join(home, ".config", "systemd", "user")
			if err := os.MkdirAll(unitDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(unitDir, "tether.service"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			oldLookup := lookupHome
			lookupHome = func(name string) (string, error) {
				if name != "alice" {
					return "", errors.New("no user")
				}
				return home, nil
			}
			t.Cleanup(func() { lookupHome = oldLookup })
			t.Setenv("SUDO_USER", "alice")
			code, out, errs := env.update(tt.args...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, errs)
			}
			if strings.Contains(out, "[Y/n]") || len(env.calls) != 0 ||
				!strings.HasSuffix(out, restartHint(runtime.GOOS, false)+"\n") {
				t.Fatalf("out %q calls %v", out, env.calls)
			}
		})
	}
}
