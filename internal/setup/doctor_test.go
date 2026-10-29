package setup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/hostos"
)

// ctx is the context of the calls in the tests.
var ctx = context.Background()

// sseFixture is the OpenAI stream that the fake providers send.
const sseFixture = "../provider/testdata/openai_text_stream.sse"

// okProvider answers each call with a short OpenAI stream.
func okProvider(t *testing.T) *httptest.Server {
	t.Helper()
	sse, err := os.ReadFile(sseFixture)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(sse)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// downProvider answers each call with HTTP 503.
func downProvider(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// botServer is a fake Bot API. Each method returns the JSON result of the map. A missing
// method returns an error reply.
func botServer(t *testing.T, results map[string]func() any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
		w.Header().Set("Content-Type", "application/json")
		f, ok := results[method]
		if !ok {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 404, "description": "Not Found"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": f()})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// okTelegram is a fake Bot API whose getMe works.
func okTelegram(t *testing.T) *httptest.Server {
	return botServer(t, map[string]func() any{
		"getMe": func() any {
			return map[string]any{"id": 99, "is_bot": true, "username": "tether_test_bot", "can_read_all_group_messages": true}
		},
	})
}

// allPrograms finds every host program.
func allPrograms(name string) (string, error) { return "/usr/bin/" + name, nil }

// without returns a LookPath that misses the named programs.
func without(names ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		for _, n := range names {
			if n == name {
				return "", exec.ErrNotFound
			}
		}
		return allPrograms(name)
	}
}

// writeDataDir writes config.yaml and a 0600 .env with the key of the primary provider.
func writeDataDir(t *testing.T, provURL, extra string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := "providers:\n  primary:\n    format: openai\n    base_url: " + provURL +
		"\n    api_key: ${PRIMARY_API_KEY}\n    model: m\npricing:\n  sources: []\n" + extra
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	env := "PRIMARY_API_KEY=sk-doctor-secret-123456\nTELEGRAM_BOT_TOKEN=123:doctor-token-abcdef\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// doctorWithFakes returns a doctor over a data dir with the fake provider, the fake Bot
// API, and the LookPath. Telegram is on. Free disk space is 50 GB.
func doctorWithFakes(t *testing.T, prov, tg *httptest.Server, look func(string) (string, error)) *Doctor {
	t.Helper()
	dir := writeDataDir(t, prov.URL, "telegram:\n  enabled: true\n")
	return &Doctor{
		DataDir:      dir,
		ConfigPath:   filepath.Join(dir, "config.yaml"),
		HC:           prov.Client(),
		LookPath:     look,
		TelegramBase: tg.URL,
		Getenv:       func(string) string { return "" },
		UnitPaths:    []string{filepath.Join(dir, "no-unit.service")},
		freeBytes:    func(string) (uint64, error) { return 50 << 30, nil },
	}
}

// find returns the check with the name.
func find(t *testing.T, checks []Check, name string) Check {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check %q in %+v", name, checks)
	return Check{}
}

func TestAllGreen(t *testing.T) {
	d := doctorWithFakes(t, okProvider(t), okTelegram(t), allPrograms)
	checks := d.Run(ctx)
	for _, c := range checks {
		if c.Status != StatusOK {
			t.Errorf("%+v", c)
		}
	}
	for _, name := range []string{"config", ".env", "provider primary", "telegram", "state.db", "chrome",
		"python3", "pdftotext", "ffmpeg", "edge-tts", "whisper-cli", "disk"} {
		find(t, checks, name)
	}
	if c := find(t, checks, "provider primary"); !strings.Contains(c.Detail, "model m answered") {
		t.Fatal(c)
	}
	if c := find(t, checks, "telegram"); !strings.Contains(c.Detail, "@tether_test_bot") {
		t.Fatal(c)
	}
}

func TestLooseDotEnvFails(t *testing.T) {
	d := doctorWithFakes(t, okProvider(t), okTelegram(t), allPrograms)
	env := filepath.Join(d.DataDir, ".env")
	if err := os.Chmod(env, 0o644); err != nil {
		t.Fatal(err)
	}
	c := find(t, d.Run(ctx), ".env")
	if c.Status != StatusFail || !strings.Contains(c.Next, "chmod 600 "+env) {
		t.Fatal(c)
	}
}

func TestStrictDotEnvShowsItsMode(t *testing.T) {
	d := doctorWithFakes(t, okProvider(t), okTelegram(t), allPrograms)
	if err := os.Chmod(filepath.Join(d.DataDir, ".env"), 0o400); err != nil {
		t.Fatal(err)
	}
	c := find(t, d.Run(ctx), ".env")
	if c.Status != StatusOK || !strings.HasPrefix(c.Detail, "mode 0400,") {
		t.Fatal(c)
	}
}

func TestProviderDownFails(t *testing.T) {
	d := doctorWithFakes(t, downProvider(t), okTelegram(t), allPrograms)
	checks := d.Run(ctx)
	c := find(t, checks, "provider primary")
	if c.Status != StatusFail || !strings.Contains(c.Detail, "primary") || c.Next == "" {
		t.Fatal(c)
	}
	if !HasFail(checks) {
		t.Fatal("HasFail is false")
	}
}

func TestMissingPythonIsWarnNamingFeature(t *testing.T) {
	d := doctorWithFakes(t, okProvider(t), okTelegram(t), without("python3"))
	c := find(t, d.Run(ctx), "python3")
	if c.Status != StatusWarn || !strings.Contains(c.Detail, "execute_code is off") || c.Next == "" {
		t.Fatal(c)
	}
}

func TestMissingChromeIsWarn(t *testing.T) {
	t.Cleanup(hostos.Set(hostos.MacOS))
	d := doctorWithFakes(t, okProvider(t), okTelegram(t), without("google-chrome", "chromium", "chromium-browser",
		"/snap/bin/chromium", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium"))
	c := find(t, d.Run(ctx), "chrome")
	if c.Status != StatusWarn || !strings.Contains(c.Detail, "browser_") {
		t.Fatal(c)
	}
	if want := "install Google Chrome (brew install --cask google-chrome), or set browser.chrome_path"; c.Next != want {
		t.Fatalf("Next = %q, want %q", c.Next, want)
	}
}

func TestMissingFFmpegWarnsOnlyWithVoice(t *testing.T) {
	d := doctorWithFakes(t, okProvider(t), okTelegram(t), without("ffmpeg"))
	if c := find(t, d.Run(ctx), "ffmpeg"); c.Status != StatusWarn || c.Next == "" {
		t.Fatal(c)
	}
	// Telegram off: no voice notes, so a missing ffmpeg loses nothing.
	b, err := os.ReadFile(d.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	off := strings.Replace(string(b), "telegram:\n  enabled: true\n", "", 1)
	if err := os.WriteFile(d.ConfigPath, []byte(off), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := find(t, d.Run(ctx), "ffmpeg"); c.Status != StatusOK {
		t.Fatal(c)
	}
}

func TestBadMCPServerFails(t *testing.T) {
	prov := okProvider(t)
	dir := writeDataDir(t, prov.URL, "mcp:\n  servers:\n    broken:\n      command: [\"/nonexistent/tether-mcp-server\"]\n      connect_timeout_s: 5\n")
	d := &Doctor{DataDir: dir, ConfigPath: filepath.Join(dir, "config.yaml"), HC: prov.Client(), LookPath: allPrograms,
		Getenv: func(string) string { return "" }, freeBytes: func(string) (uint64, error) { return 50 << 30, nil }}
	c := find(t, d.Run(ctx), "mcp broken")
	if c.Status != StatusFail || c.Next == "" {
		t.Fatal(c)
	}
}

func TestLowDiskWarns(t *testing.T) {
	d := doctorWithFakes(t, okProvider(t), okTelegram(t), allPrograms)
	d.freeBytes = func(string) (uint64, error) { return 200 << 20, nil }
	if c := find(t, d.Run(ctx), "disk"); c.Status != StatusWarn || c.Next == "" {
		t.Fatal(c)
	}
}

func TestBrokenConfigFails(t *testing.T) {
	d := doctorWithFakes(t, okProvider(t), okTelegram(t), allPrograms)
	if err := os.WriteFile(d.ConfigPath, []byte("providers:\n  primary:\n    format: openai\n    api_key: ${NOT_SET_ANYWHERE_X}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	checks := d.Run(ctx)
	c := find(t, checks, "config")
	if c.Status != StatusFail || !strings.Contains(c.Detail, "NOT_SET_ANYWHERE_X") || c.Next == "" {
		t.Fatal(c)
	}
	// The host programs are still checked.
	find(t, checks, "python3")
}

func TestDoctorHidesSecrets(t *testing.T) {
	// The provider echoes the key in its error body.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key "+r.Header.Get("Authorization"), http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	d := doctorWithFakes(t, srv, okTelegram(t), allPrograms)
	out := Format(d.Run(ctx))
	if strings.Contains(out, "sk-doctor-secret-123456") || strings.Contains(out, "doctor-token-abcdef") {
		t.Fatal(out)
	}
}

func TestEveryWarnAndFailHasNext(t *testing.T) {
	d := doctorWithFakes(t, downProvider(t), botServer(t, nil), func(string) (string, error) { return "", errors.New("no") })
	d.freeBytes = func(string) (uint64, error) { return 1, nil }
	for _, c := range d.Run(ctx) {
		if c.Status != StatusOK && c.Next == "" {
			t.Errorf("no next action: %+v", c)
		}
	}
}

func TestFormatSymbols(t *testing.T) {
	out := Format([]Check{
		{Name: "a", Status: StatusOK, Detail: "fine"},
		{Name: "b", Status: StatusWarn, Detail: "meh", Next: "do b"},
		{Name: "c", Status: StatusFail, Detail: "bad", Next: "do c"},
	})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatal(out)
	}
	if !strings.HasPrefix(lines[0], "✓ a") || strings.Contains(lines[0], "→") {
		t.Fatal(lines[0])
	}
	if !strings.HasPrefix(lines[1], "⚠ b") || !strings.Contains(lines[1], "→ do b") {
		t.Fatal(lines[1])
	}
	if !strings.HasPrefix(lines[2], "✗ c") || !strings.Contains(lines[2], "→ do c") {
		t.Fatal(lines[2])
	}
}

// releaseServer is a fake GitHub API. It answers /releases/latest with the tag and the date,
// and counts the requests.
func releaseServer(t *testing.T, tag, published string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		if r.URL.Path != "/releases/latest" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "published_at": published, "assets": []any{}})
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// releaseDoctor returns a doctor whose release check uses the fake GitHub API.
func releaseDoctor(t *testing.T, version string) (*Doctor, *atomic.Int32) {
	t.Helper()
	srv, n := releaseServer(t, "v0.9.0", "2026-10-20T10:00:00Z")
	return &Doctor{DataDir: t.TempDir(), Version: version, ReleaseAPI: srv.URL, HC: srv.Client()}, n
}

func TestCheckReleaseNewer(t *testing.T) {
	d, _ := releaseDoctor(t, "0.4.0")
	c := d.checkRelease(ctx)
	if c.Name != "release" || c.Status != StatusWarn ||
		c.Detail != "0.9.0 is available; you run 0.4.0 (released 2026-10-20)" || c.Next != "run tether update" {
		t.Fatalf("%+v", c)
	}
}

func TestCheckReleaseSame(t *testing.T) {
	d, _ := releaseDoctor(t, "0.9.0")
	c := d.checkRelease(ctx)
	if c.Name != "release" || c.Status != StatusOK || c.Detail != "0.9.0 is the newest release" || c.Next != "" {
		t.Fatalf("%+v", c)
	}
}

func TestCheckReleaseAhead(t *testing.T) {
	d, _ := releaseDoctor(t, "1.0.0")
	c := d.checkRelease(ctx)
	if c.Status != StatusOK || c.Detail != "1.0.0 is newer than the newest release, 0.9.0" || c.Next != "" {
		t.Fatalf("%+v", c)
	}
}

func TestCheckReleaseDev(t *testing.T) {
	d, n := releaseDoctor(t, "dev")
	c := d.checkRelease(ctx)
	if c.Name != "release" || c.Status != StatusOK || c.Detail != "dev build; the release check is off" || c.Next != "" {
		t.Fatalf("%+v", c)
	}
	if got := n.Load(); got != 0 {
		t.Fatalf("a dev build made %d requests", got)
	}
}

func TestCheckReleaseBadVersion(t *testing.T) {
	d, _ := releaseDoctor(t, "0.4")
	c := d.checkRelease(ctx)
	if c.Status != StatusWarn || !strings.HasPrefix(c.Detail, "cannot compare 0.4 with 0.9.0: ") || c.Next != "" {
		t.Fatalf("%+v", c)
	}
}

func TestCheckReleaseUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	api := srv.URL
	srv.Close()
	d := &Doctor{DataDir: t.TempDir(), Version: "0.4.0", ReleaseAPI: api}
	c := d.checkRelease(ctx)
	if c.Status != StatusWarn || !strings.HasPrefix(c.Detail, "cannot reach GitHub: ") ||
		c.Next != "check the network, or run tether update --check later" {
		t.Fatalf("%+v", c)
	}
}

func TestCheckReleaseSlowServer(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	d := &Doctor{DataDir: t.TempDir(), Version: "0.4.0", ReleaseAPI: srv.URL, HC: srv.Client()}
	cctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	c := d.checkRelease(cctx)
	if el := time.Since(start); el > time.Second {
		t.Fatalf("the check waited %s", el)
	}
	if c.Status != StatusWarn || !strings.HasPrefix(c.Detail, "cannot reach GitHub: ") {
		t.Fatalf("%+v", c)
	}
}

func TestRunEndsWithReleaseCheck(t *testing.T) {
	// The config does not load, but the release check runs.
	d, n := releaseDoctor(t, "dev")
	d.freeBytes = func(string) (uint64, error) { return 50 << 30, nil }
	d.LookPath = allPrograms
	d.Getenv = func(string) string { return "" }
	checks := d.Run(ctx)
	if last := checks[len(checks)-1]; last.Name != "release" || last.Status != StatusOK {
		t.Fatalf("last check %+v", last)
	}
	if got := n.Load(); got != 0 {
		t.Fatalf("a dev build made %d requests", got)
	}
}

func TestTelegramPrivacyMode(t *testing.T) {
	const detail = "getMe works for @tether_test_bot, but privacy mode is on. In a group, the bot gets only commands and mentions, but the config needs every group message."
	const next = "turn off privacy mode with /setprivacy in @BotFather, then remove the bot from each group and add it again"
	cases := []struct {
		name    string
		canRead bool
		mut     func(*config.TelegramConfig)
		warn    bool
	}{
		{"default config, privacy on", false, nil, true},
		{"default config, privacy off", true, nil, false},
		{"mention required, privacy on", false, func(c *config.TelegramConfig) { c.RequireMention = true }, false},
		{"pattern, privacy on", false, func(c *config.TelegramConfig) { c.RequireMention = true; c.MentionPatterns = []string{"^tether"} }, true},
		{"free response, privacy on", false, func(c *config.TelegramConfig) { c.RequireMention = true; c.FreeResponseChats = []string{"-100"} }, true},
		{"observe, privacy on", false, func(c *config.TelegramConfig) { c.RequireMention = true; c.ObserveUnmentionedGroupMessages = true }, true},
	}
	for _, c := range cases {
		srv := botServer(t, map[string]func() any{"getMe": func() any {
			return map[string]any{"id": 99, "is_bot": true, "username": "tether_test_bot", "can_read_all_group_messages": c.canRead}
		}})
		cfg := config.Default()
		cfg.Telegram.Enabled, cfg.Telegram.BotToken = true, "T"
		if c.mut != nil {
			c.mut(&cfg.Telegram)
		}
		got := (&Doctor{TelegramBase: srv.URL, HC: srv.Client()}).checkTelegram(ctx, &cfg)
		switch {
		case c.warn && (got.Status != StatusWarn || got.Detail != detail || got.Next != next):
			t.Errorf("%s: %+v", c.name, got)
		case !c.warn && (got.Status != StatusOK || got.Detail != "getMe works for @tether_test_bot"):
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

func TestHostChecksGit(t *testing.T) {
	t.Cleanup(hostos.Set(hostos.Ubuntu))
	cfg := config.Default()
	d := &Doctor{LookPath: without("git"), Getenv: func(string) string { return "" }}
	got := find(t, d.HostChecks(&cfg), "git")
	want := Check{Name: "git", Status: StatusWarn,
		Detail: "not found, so skills install and update cannot fetch from a git URL",
		Next:   "install git (sudo apt install git)"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	d.LookPath = allPrograms
	got = find(t, d.HostChecks(&cfg), "git")
	if want := (Check{Name: "git", Status: StatusOK, Detail: "/usr/bin/git"}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestHostChecksHintsOnMac(t *testing.T) {
	t.Cleanup(hostos.Set(hostos.MacOS))
	cfg := config.Default()
	cfg.Telegram.Enabled = true // voice on, so a missing ffmpeg is a warning
	d := &Doctor{LookPath: without("python3", "pdftotext", "ffmpeg", "edge-tts", "whisper-cli"),
		Getenv: func(string) string { return "" }}
	checks := d.HostChecks(&cfg)
	for name, want := range map[string]string{
		"python3":     "install python3 (xcode-select --install)",
		"pdftotext":   "install poppler (brew install poppler)",
		"ffmpeg":      "install ffmpeg (brew install ffmpeg)",
		"edge-tts":    "install edge-tts (pipx install edge-tts), or change media.tts",
		"whisper-cli": "install whisper-cpp (brew install whisper-cpp), or change media.stt",
	} {
		if c := find(t, checks, name); c.Status != StatusWarn || c.Next != want {
			t.Errorf("%s: got %+v, want Next %q", name, c, want)
		}
	}
}

// answerRow is one stored assistant row for the "last answer" check.
type answerRow struct {
	session, pin, provider, model string
	age                           time.Duration
}

// seedStateDB makes state.db in the data dir of d with the session and the assistant rows.
func seedStateDB(t *testing.T, d *Doctor, rows ...answerRow) {
	t.Helper()
	sdb, err := db.Open(filepath.Join(d.DataDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	now := time.Now()
	for _, r := range rows {
		if _, err := sdb.Exec(`INSERT OR IGNORE INTO sessions (id, model_override) VALUES (?, ?)`, r.session, r.pin); err != nil {
			t.Fatal(err)
		}
		if _, err := sdb.Exec(`INSERT INTO messages (session_id, role, content, model, provider, timestamp)
			VALUES (?, 'assistant', 'x', ?, ?, ?)`, r.session, r.model, r.provider, now.Add(-r.age).UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLastAnswerPrimaryOK(t *testing.T) {
	d := doctorWithFakes(t, okProvider(t), okTelegram(t), allPrograms)
	seedStateDB(t, d, answerRow{session: "s1", provider: "primary", model: "glm-5.3", age: 5 * time.Minute})
	got := find(t, d.Run(ctx), "last answer")
	want := Check{Name: "last answer", Status: StatusOK, Detail: "primary · glm-5.3, 5 min ago"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestLastAnswerFallbackWarn(t *testing.T) {
	d := doctorWithFakes(t, okProvider(t), okTelegram(t), allPrograms)
	seedStateDB(t, d, answerRow{session: "s1", provider: "fallback",
		model: "deepseek/deepseek-v4.1-flash-1017", age: 2 * time.Minute})
	got := find(t, d.Run(ctx), "last answer")
	want := Check{Name: "last answer", Status: StatusWarn,
		Detail: "fallback · deepseek/deepseek-v4.1-flash-1017, 2 min ago",
		Next:   `the primary failed at that time; see "provider fallback" in the gateway log`}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestLastAnswerPinnedFallbackOK(t *testing.T) {
	d := doctorWithFakes(t, okProvider(t), okTelegram(t), allPrograms)
	seedStateDB(t, d, answerRow{session: "s1", pin: "fallback", provider: "fallback",
		model: "deepseek/deepseek-v4.1-flash-1017", age: 2 * time.Minute})
	got := find(t, d.Run(ctx), "last answer")
	want := Check{Name: "last answer", Status: StatusOK,
		Detail: "fallback · deepseek/deepseek-v4.1-flash-1017, 2 min ago (the session pins the fallback)"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestLastAnswerNoRow(t *testing.T) {
	d := doctorWithFakes(t, okProvider(t), okTelegram(t), allPrograms)
	seedStateDB(t, d)
	checks := d.Run(ctx)
	if c := find(t, checks, "state.db"); c.Status != StatusOK {
		t.Fatal(c)
	}
	for _, c := range checks {
		if c.Name == "last answer" {
			t.Fatalf("no stored answer must give no check: %+v", c)
		}
	}
}

func TestDoctorUnitCheck(t *testing.T) {
	fixedTZ(t, "UTC")
	d := doctorWithFakes(t, okProvider(t), okTelegram(t), allPrograms)
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.service")
	path := filepath.Join(dir, "tether.service")

	d.UnitPaths = []string{missing, path}
	for _, c := range d.Run(ctx) {
		if c.Name == "systemd unit" {
			t.Fatalf("unit check without a unit: %+v", c)
		}
	}

	old := "[Service]\nExecStart=/bin/tether gateway\n"
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	c := find(t, d.Run(ctx), "systemd unit")
	wantDetail := strings.Join(UnitProblems(old, 180), " ")
	wantNext := `run "tether setup" and answer yes at "Write the systemd unit?". Then run "systemctl --user daemon-reload".`
	if c.Status != StatusWarn || c.Detail != wantDetail || c.Next != wantNext {
		t.Fatalf("%+v", c)
	}

	if err := os.WriteFile(path, []byte(UnitText("/bin/tether", "/d", 180)), 0o600); err != nil {
		t.Fatal(err)
	}
	c = find(t, d.Run(ctx), "systemd unit")
	if c.Status != StatusOK || c.Next != "" {
		t.Fatalf("%+v", c)
	}
}
