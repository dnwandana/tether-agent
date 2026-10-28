package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeRelease serves one release with the linux/amd64 archive and checksums.txt under /dl/.
// With tamper, the served archive differs from the one in checksums.txt. The counter
// counts the requests to /dl/.
func fakeRelease(t *testing.T, version string, bin []byte, tamper bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	asset := "tether-agent_" + version + "_linux_amd64.tar.gz"
	archive := tarGz(t, map[string][]byte{"tether": bin, "LICENSE": []byte("mit")})
	sum := sha256.Sum256(archive)
	checksums := hex.EncodeToString(sum[:]) + "  " + asset + "\n"
	if tamper {
		archive = append(bytes.Clone(archive), 0)
	}
	var downloads atomic.Int32
	var srv *httptest.Server
	release := func(w http.ResponseWriter) {
		body := map[string]any{
			"tag_name":     "v" + version,
			"published_at": "2026-10-01T12:00:00Z",
			"assets": []map[string]string{
				{"name": asset, "browser_download_url": srv.URL + "/dl/" + asset},
				{"name": "checksums.txt", "browser_download_url": srv.URL + "/dl/checksums.txt"},
			},
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/releases/latest", r.URL.Path == "/releases/tags/v"+version:
			release(w)
		case strings.HasPrefix(r.URL.Path, "/dl/"):
			downloads.Add(1)
			var body []byte
			switch r.URL.Path {
			case "/dl/checksums.txt":
				body = []byte(checksums)
			case "/dl/" + asset:
				body = archive
			default:
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &downloads
}

// recordPaths wraps the handler of srv and returns the path of each request.
func recordPaths(srv *httptest.Server) func() []string {
	var mu sync.Mutex
	var paths []string
	next := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		next.ServeHTTP(w, r)
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

// writeFakeBinary writes <tmp>/tether with mode 0o755 and returns its path.
func writeFakeBinary(t *testing.T, content string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "tether")
	if err := os.WriteFile(bin, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// newTestUpdater returns an Updater for linux/amd64 whose candidate prints "tether 0.5.0".
func newTestUpdater(srv *httptest.Server, bin, cur string, out *bytes.Buffer) *Updater {
	u := &Updater{
		Current: cur,
		GOOS:    "linux",
		GOARCH:  "amd64",
		BinPath: bin,
		Client:  &Client{HC: srv.Client(), APIBase: srv.URL},
		runVersion: func(context.Context, string) (string, error) {
			return "tether 0.5.0\n", nil
		},
	}
	if out != nil {
		u.Out = out
	}
	return u
}

// binIs fails the test if the file at bin does not hold want.
func binIs(t *testing.T, bin, want string) {
	t.Helper()
	b, err := os.ReadFile(bin)
	if err != nil || string(b) != want {
		t.Fatalf("binary = %q, %v; want %q", b, err, want)
	}
}

func TestRunUpdatesToNewer(t *testing.T) {
	srv, _ := fakeRelease(t, "0.5.0", []byte("newbin"), false)
	bin := writeFakeBinary(t, "old")
	var out bytes.Buffer
	res, err := newTestUpdater(srv, bin, "0.4.0", &out).Run(context.Background(), Options{})
	if err != nil || res.State != StateUpdated || res.Current != "0.4.0" || res.Available != "0.5.0" || res.BinPath != bin {
		t.Fatal(res, err)
	}
	binIs(t, bin, "newbin")
	if !strings.Contains(out.String(), "downloading tether-agent_0.5.0_linux_amd64.tar.gz (1 MB)\nchecksum OK\n") {
		t.Fatal(out.String())
	}
	noCandidates(t, filepath.Dir(bin))
}

func TestRunSameDownloadsNothing(t *testing.T) {
	srv, dl := fakeRelease(t, "0.5.0", []byte("newbin"), false)
	bin := writeFakeBinary(t, "old")
	res, err := newTestUpdater(srv, bin, "0.5.0", nil).Run(context.Background(), Options{})
	if err != nil || res.State != StateSame || res.Available != "0.5.0" {
		t.Fatal(res, err)
	}
	if n := dl.Load(); n != 0 {
		t.Fatalf("%d downloads, want 0", n)
	}
	binIs(t, bin, "old")
}

func TestRunAheadDownloadsNothing(t *testing.T) {
	srv, dl := fakeRelease(t, "0.5.0", []byte("newbin"), false)
	bin := writeFakeBinary(t, "old")
	res, err := newTestUpdater(srv, bin, "0.6.0", nil).Run(context.Background(), Options{})
	if err != nil || res.State != StateAhead || res.Available != "0.5.0" {
		t.Fatal(res, err)
	}
	if n := dl.Load(); n != 0 {
		t.Fatalf("%d downloads, want 0", n)
	}
	binIs(t, bin, "old")
}

func TestRunForceInstallsSame(t *testing.T) {
	srv, _ := fakeRelease(t, "0.5.0", []byte("newbin"), false)
	bin := writeFakeBinary(t, "old")
	res, err := newTestUpdater(srv, bin, "0.5.0", nil).Run(context.Background(), Options{Force: true})
	if err != nil || res.State != StateUpdated {
		t.Fatal(res, err)
	}
	binIs(t, bin, "newbin")
}

func TestRunCheckOnly(t *testing.T) {
	srv, dl := fakeRelease(t, "0.5.0", []byte("newbin"), false)
	bin := writeFakeBinary(t, "old")
	res, err := newTestUpdater(srv, bin, "0.4.0", nil).Run(context.Background(), Options{Check: true})
	if err != nil || res.State != StateNewer || res.Available != "0.5.0" {
		t.Fatal(res, err)
	}
	if n := dl.Load(); n != 0 {
		t.Fatalf("%d downloads, want 0", n)
	}
	binIs(t, bin, "old")
}

func TestRunCheckSameAndAhead(t *testing.T) {
	for cur, want := range map[string]State{"0.5.0": StateSame, "0.6.0": StateAhead} {
		srv, dl := fakeRelease(t, "0.5.0", []byte("newbin"), false)
		bin := writeFakeBinary(t, "old")
		res, err := newTestUpdater(srv, bin, cur, nil).Run(context.Background(), Options{Check: true})
		if err != nil || res.State != want {
			t.Fatal(cur, res, err)
		}
		if n := dl.Load(); n != 0 {
			t.Fatalf("%s: %d downloads, want 0", cur, n)
		}
	}
}

func TestRunCheckWinsOverForce(t *testing.T) {
	srv, dl := fakeRelease(t, "0.5.0", []byte("newbin"), false)
	bin := writeFakeBinary(t, "old")
	res, err := newTestUpdater(srv, bin, "0.4.0", nil).Run(context.Background(), Options{Check: true, Force: true})
	if err != nil || res.State != StateNewer {
		t.Fatal(res, err)
	}
	if n := dl.Load(); n != 0 {
		t.Fatalf("%d downloads, want 0", n)
	}
	binIs(t, bin, "old")
}

func TestRunDevRefusesWithoutForce(t *testing.T) {
	srv, dl := fakeRelease(t, "0.5.0", []byte("newbin"), false)
	bin := writeFakeBinary(t, "old")
	_, err := newTestUpdater(srv, bin, "dev", nil).Run(context.Background(), Options{})
	if err == nil || err.Error() != "this is a dev build; use --force to replace it" {
		t.Fatal(err)
	}
	if n := dl.Load(); n != 0 {
		t.Fatalf("%d downloads, want 0", n)
	}
	binIs(t, bin, "old")

	res, err := newTestUpdater(srv, bin, "dev", nil).Run(context.Background(), Options{Force: true})
	if err != nil || res.State != StateUpdated || res.Current != "dev" || res.Available != "0.5.0" {
		t.Fatal(res, err)
	}
	binIs(t, bin, "newbin")
}

func TestRunDevWithCheck(t *testing.T) {
	srv, dl := fakeRelease(t, "0.5.0", []byte("newbin"), false)
	bin := writeFakeBinary(t, "old")
	res, err := newTestUpdater(srv, bin, "dev", nil).Run(context.Background(), Options{Check: true})
	if err != nil || res.State != StateDev || res.Available != "0.5.0" {
		t.Fatal(res, err)
	}
	if n := dl.Load(); n != 0 {
		t.Fatalf("%d downloads, want 0", n)
	}
}

func TestRunBadCurrentVersion(t *testing.T) {
	srv, _ := fakeRelease(t, "0.5.0", []byte("newbin"), false)
	bin := writeFakeBinary(t, "old")
	_, err := newTestUpdater(srv, bin, "nightly", nil).Run(context.Background(), Options{})
	if err == nil || !strings.HasPrefix(err.Error(), "cannot compare nightly with 0.5.0: ") {
		t.Fatal(err)
	}
	binIs(t, bin, "old")
}

func TestRunTamperedArchiveLeavesBinary(t *testing.T) {
	srv, _ := fakeRelease(t, "0.5.0", []byte("newbin"), true)
	bin := writeFakeBinary(t, "old")
	var out bytes.Buffer
	_, err := newTestUpdater(srv, bin, "0.4.0", &out).Run(context.Background(), Options{})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "checksum OK") {
		t.Fatal(out.String())
	}
	binIs(t, bin, "old")
	noCandidates(t, filepath.Dir(bin))
}

func TestRunVersionMismatchLeavesBinary(t *testing.T) {
	srv, _ := fakeRelease(t, "0.5.0", []byte("newbin"), false)
	bin := writeFakeBinary(t, "old")
	u := newTestUpdater(srv, bin, "0.4.0", nil)
	var ran string
	u.runVersion = func(_ context.Context, candidate string) (string, error) {
		ran = candidate
		return "tether 0.4.9\n", nil
	}
	_, err := u.Run(context.Background(), Options{})
	if err == nil || !strings.Contains(err.Error(), `prints "tether 0.4.9", want "tether 0.5.0"`) {
		t.Fatal(err)
	}
	if filepath.Dir(ran) != filepath.Dir(bin) || ran == bin {
		t.Fatalf("runVersion ran %q, want a candidate next to %q", ran, bin)
	}
	binIs(t, bin, "old")
	noCandidates(t, filepath.Dir(bin))
}

func TestRunVersionNeedsExactOutput(t *testing.T) {
	srv, _ := fakeRelease(t, "0.5.0", []byte("newbin"), false)
	bin := writeFakeBinary(t, "old")
	u := newTestUpdater(srv, bin, "0.4.0", nil)
	u.runVersion = func(context.Context, string) (string, error) {
		return "tether 0.5.0\nextra\n", nil
	}
	if _, err := u.Run(context.Background(), Options{}); err == nil {
		t.Fatal("want an error for extra output")
	}
	binIs(t, bin, "old")
}

func TestRunUnsupportedPlatform(t *testing.T) {
	srv, _ := fakeRelease(t, "0.5.0", []byte("newbin"), false)
	paths := recordPaths(srv)
	bin := writeFakeBinary(t, "old")
	u := newTestUpdater(srv, bin, "0.4.0", nil)
	u.GOOS = "windows"
	_, err := u.Run(context.Background(), Options{})
	if err == nil || !strings.HasPrefix(err.Error(), "no release for windows/amd64") {
		t.Fatal(err)
	}
	if p := paths(); len(p) != 0 {
		t.Fatalf("requests %v, want none", p)
	}
}

func TestRunPermissionBeforeDownload(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	srv, dl := fakeRelease(t, "0.5.0", []byte("newbin"), false)
	bin := writeFakeBinary(t, "old")
	dir := filepath.Dir(bin)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	_, err := newTestUpdater(srv, bin, "0.4.0", nil).Run(context.Background(), Options{})
	if err == nil || !strings.Contains(err.Error(), "sudo tether update") {
		t.Fatal(err)
	}
	if n := dl.Load(); n != 0 {
		t.Fatalf("%d downloads, want 0", n)
	}
	binIs(t, bin, "old")
}

func TestRunMissingAsset(t *testing.T) {
	srv, dl := fakeRelease(t, "0.5.0", []byte("newbin"), false)
	bin := writeFakeBinary(t, "old")
	u := newTestUpdater(srv, bin, "0.4.0", nil)
	u.GOARCH = "arm64"
	_, err := u.Run(context.Background(), Options{})
	if err == nil || err.Error() != "release v0.5.0 has no asset tether-agent_0.5.0_linux_arm64.tar.gz" {
		t.Fatal(err)
	}
	if n := dl.Load(); n != 0 {
		t.Fatalf("%d downloads, want 0", n)
	}
	binIs(t, bin, "old")
}

func TestRunArchiveTooLarge(t *testing.T) {
	srv, _ := fakeRelease(t, "0.5.0", []byte("newbin"), false)
	asset := "/dl/tether-agent_0.5.0_linux_amd64.tar.gz"
	next := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != asset {
			next.ServeHTTP(w, r)
			return
		}
		// Stream the body with no Content-Length, so the client must count the bytes.
		chunk := make([]byte, 64<<10)
		for left := MaxArchiveBytes + 1; left > 0; {
			n := min(left, int64(len(chunk)))
			if _, err := w.Write(chunk[:n]); err != nil {
				return
			}
			left -= n
		}
	})
	bin := writeFakeBinary(t, "old")
	_, err := newTestUpdater(srv, bin, "0.4.0", nil).Run(context.Background(), Options{})
	want := "download tether-agent_0.5.0_linux_amd64.tar.gz: file is larger than " +
		strconv.FormatInt(MaxArchiveBytes, 10) + " bytes"
	if err == nil || err.Error() != want {
		t.Fatal(err)
	}
	binIs(t, bin, "old")
	noCandidates(t, filepath.Dir(bin))
}

func TestRunArchiveTooLargeByHeader(t *testing.T) {
	srv, _ := fakeRelease(t, "0.5.0", []byte("newbin"), false)
	asset := "/dl/tether-agent_0.5.0_linux_amd64.tar.gz"
	next := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != asset {
			next.ServeHTTP(w, r)
			return
		}
		// A Content-Length over the limit fails before the body arrives.
		w.Header().Set("Content-Length", strconv.FormatInt(MaxArchiveBytes+1, 10))
		w.WriteHeader(http.StatusOK)
	})
	bin := writeFakeBinary(t, "old")
	var out bytes.Buffer
	_, err := newTestUpdater(srv, bin, "0.4.0", &out).Run(context.Background(), Options{})
	if err == nil || !strings.Contains(err.Error(), "file is larger than") {
		t.Fatal(err)
	}
	binIs(t, bin, "old")
}

func TestRunDownloadStatusError(t *testing.T) {
	srv, _ := fakeRelease(t, "0.5.0", []byte("newbin"), false)
	next := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dl/checksums.txt" {
			http.Error(w, "gone", http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(w, r)
	})
	bin := writeFakeBinary(t, "old")
	_, err := newTestUpdater(srv, bin, "0.4.0", nil).Run(context.Background(), Options{})
	if err == nil || err.Error() != "download checksums.txt: server returned 500 Internal Server Error" {
		t.Fatal(err)
	}
	binIs(t, bin, "old")
}

func TestRunUsesTag(t *testing.T) {
	srv, _ := fakeRelease(t, "0.5.0", []byte("newbin"), false)
	paths := recordPaths(srv)
	bin := writeFakeBinary(t, "old")
	res, err := newTestUpdater(srv, bin, "0.4.0", nil).Run(context.Background(), Options{Tag: "v0.5.0"})
	if err != nil || res.State != StateUpdated {
		t.Fatal(res, err)
	}
	p := paths()
	if len(p) == 0 || p[0] != "/releases/tags/v0.5.0" {
		t.Fatalf("requests %v, want /releases/tags/v0.5.0 first", p)
	}
	for _, path := range p {
		if path == "/releases/latest" {
			t.Fatalf("requests %v, want no /releases/latest", p)
		}
	}
	binIs(t, bin, "newbin")
}

func TestRunSendsUserAgent(t *testing.T) {
	srv, _ := fakeRelease(t, "0.5.0", []byte("newbin"), false)
	var mu sync.Mutex
	var agents []string
	next := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		agents = append(agents, r.Header.Get("User-Agent"))
		mu.Unlock()
		next.ServeHTTP(w, r)
	})
	bin := writeFakeBinary(t, "old")
	u := newTestUpdater(srv, bin, "0.4.0", nil)
	u.Client.UserAgent = "tether-agent/0.4.0"
	if _, err := u.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(agents) != 3 {
		t.Fatalf("user agents %v, want 3 requests", agents)
	}
	for _, a := range agents {
		if a != "tether-agent/0.4.0" {
			t.Fatalf("user agents %v", agents)
		}
	}
}

func TestDownloadSizeLine(t *testing.T) {
	for n, want := range map[int64]string{
		-1:              "downloading a.tar.gz\n",
		0:               "downloading a.tar.gz (1 MB)\n",
		512<<10 - 1:     "downloading a.tar.gz (1 MB)\n",
		3<<20 + 512<<10: "downloading a.tar.gz (4 MB)\n",
		11 << 20:        "downloading a.tar.gz (11 MB)\n",
	} {
		if got := downloadLine("a.tar.gz", n); got != want {
			t.Errorf("downloadLine(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestDefaultRunVersion(t *testing.T) {
	t.Setenv("HOME", "/home/someone")
	script := filepath.Join(t.TempDir(), "candidate")
	// The script prints its first argument and HOME, so the test sees the call and the environment.
	body := "#!/bin/sh\nprintf '%s %s\\n' \"$1\" \"${HOME:-none}\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := runVersion(context.Background(), script)
	if err != nil || got != "version none\n" {
		t.Fatalf("runVersion = %q, %v", got, err)
	}
	if _, err := runVersion(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("want an error for a missing candidate")
	}
}

func TestRunTagWithoutV(t *testing.T) {
	srv, _ := fakeRelease(t, "0.5.0", []byte("newbin"), false)
	paths := recordPaths(srv)
	bin := writeFakeBinary(t, "old")
	res, err := newTestUpdater(srv, bin, "0.4.0", nil).Run(context.Background(), Options{Tag: "0.5.0", Check: true})
	if err != nil || res.State != StateNewer {
		t.Fatal(res, err)
	}
	if p := paths(); len(p) != 1 || p[0] != "/releases/tags/v0.5.0" {
		t.Fatalf("requests %v, want [/releases/tags/v0.5.0]", p)
	}
}
