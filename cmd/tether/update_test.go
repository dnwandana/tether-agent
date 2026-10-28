package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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

	"github.com/dnwandana/tether-agent/internal/gateway"
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
	rel, err := gateway.AcquireLock(filepath.Join(dir, "gateway.lock"))
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
