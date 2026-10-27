package setup

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/telegram"
)

// fakeProvider is a fake OpenAI endpoint that counts its calls.
type fakeProvider struct {
	*httptest.Server
	calls atomic.Int32
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	sse, err := os.ReadFile(sseFixture)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		p.calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(sse)
	}))
	t.Cleanup(p.Close)
	return p
}

// answers returns the input of the wizard: one line per answer, then the end of the input.
func answers(lines ...string) *bufio.Reader {
	s := ""
	for _, l := range lines {
		s += l + "\n"
	}
	return bufio.NewReader(strings.NewReader(s))
}

// wizard returns a wizard over dir that finds every host program and writes its output
// to a buffer.
func wizard(t *testing.T, dir string, in *bufio.Reader) (*Wizard, *bytes.Buffer) {
	t.Helper()
	// The systemd unit goes below HOME.
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	return &Wizard{
		In:       in,
		Out:      &out,
		DataDir:  dir,
		HC:       &http.Client{},
		Now:      time.Now,
		LookPath: allPrograms,
		BinPath:  "/usr/local/bin/tether",
	}, &out
}

// loadCfg loads the config of dir with its .env.
func loadCfg(t *testing.T, dir string) *config.Config {
	t.Helper()
	dotenv, err := security.LoadDotEnv(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(dir, "config.yaml"), func(name string) (string, bool) {
		v, ok := dotenv[name]
		return v, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestProviderStepWritesEnvAndConfig(t *testing.T) {
	dir := t.TempDir()
	prov := newFakeProvider(t)
	w, out := wizard(t, dir, answers("openai", prov.URL+"/v1", "sk-test-123", "glm-4.7", "n", "n"))
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	cfg, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if !strings.Contains(string(env), "PRIMARY_API_KEY=sk-test-123") || !strings.Contains(string(cfg), "${PRIMARY_API_KEY}") ||
		strings.Contains(string(cfg), "sk-test-123") {
		t.Fatalf("env %q\nconfig %q", env, cfg)
	}
	for _, name := range []string{".env", "config.yaml"} {
		if fi, err := os.Stat(filepath.Join(dir, name)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatal(name, fi, err)
		}
	}
	c := loadCfg(t, dir)
	p := c.Providers["primary"]
	if p.Format != "openai" || p.BaseURL != prov.URL+"/v1" || p.APIKey != "sk-test-123" || p.Model != "glm-4.7" {
		t.Fatal(p)
	}
	if prov.calls.Load() != 1 || !strings.Contains(out.String(), "glm-4.7") || !strings.Contains(out.String(), " ms") {
		t.Fatal(prov.calls.Load(), out.String())
	}
	if strings.Contains(out.String(), "sk-test-123") {
		t.Fatal("the key is in the output")
	}
	// Step 4 shows the host programs.
	if !strings.Contains(out.String(), "✓ python3") {
		t.Fatal(out.String())
	}
}

func TestFallbackStep(t *testing.T) {
	dir := t.TempDir()
	prov := newFakeProvider(t)
	w, out := wizard(t, dir, answers("openai", prov.URL, "sk-one-111", "m1",
		"y", "openai", prov.URL, "sk-two-222", "m2", "n"))
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if !strings.Contains(string(env), "FALLBACK_API_KEY=sk-two-222") {
		t.Fatal(string(env))
	}
	fb := loadCfg(t, dir).Providers["fallback"]
	if fb.Format != "openai" || fb.APIKey != "sk-two-222" || fb.Model != "m2" {
		t.Fatal(fb)
	}
}

// startBot is a fake Bot API. getUpdates returns the result of updates for the offset of
// the call.
func startBot(t *testing.T, updates func(offset int64) []telegram.Update) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
		w.Header().Set("Content-Type", "application/json")
		var result any
		switch method {
		case "getMe":
			result = telegram.User{ID: 99, IsBot: true, Username: "tether_test_bot"}
		case "getUpdates":
			var p struct {
				Offset int64 `json:"offset"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			result = updates(p.Offset)
		default:
			result = true
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// startFromFive gives a /start from user 5 in a private chat to a call with an offset of
// at most 7.
func startFromFive(offset int64) []telegram.Update {
	if offset > 7 {
		return []telegram.Update{}
	}
	return []telegram.Update{
		// A message in a group is not the owner.
		{UpdateID: 6, Message: &telegram.Message{MessageID: 1, From: &telegram.User{ID: 8, FirstName: "Eve"},
			Chat: telegram.Chat{ID: -100, Type: "group"}, Text: "/start"}},
		{UpdateID: 7, Message: &telegram.Message{MessageID: 2, From: &telegram.User{ID: 5, FirstName: "Alice", Username: "alice"},
			Chat: telegram.Chat{ID: 5, Type: "private"}, Text: "/start"}},
	}
}

func TestTelegramOwnerPairing(t *testing.T) {
	dir := t.TempDir()
	prov := newFakeProvider(t)
	bot := startBot(t, startFromFive)
	w, out := wizard(t, dir, answers("openai", prov.URL, "sk-test-123", "m", "n", "y", "123:bot-token-abc", "y"))
	w.TelegramBase = bot.URL
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	for _, want := range []string{"Send /start to @tether_test_bot now", "Pairing code: ", "Approve Alice (5) as the owner? [y/N]"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("no %q in\n%s", want, out.String())
		}
	}
	c := loadCfg(t, dir)
	if !c.Telegram.Enabled || !slices.Equal(c.Telegram.AllowedUsers, []string{"5"}) || c.Telegram.BotToken != "123:bot-token-abc" {
		t.Fatal(c.Telegram)
	}
	cfgText, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if strings.Contains(string(cfgText), "bot-token-abc") {
		t.Fatal("the bot token is in config.yaml")
	}
	d, err := db.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if !(&telegram.Pairing{DB: d}).IsApproved(ctx, "telegram", "5") {
		t.Fatal("user 5 is not approved")
	}
}

func TestTelegramOwnerDeclined(t *testing.T) {
	dir := t.TempDir()
	prov := newFakeProvider(t)
	bot := startBot(t, startFromFive)
	w, out := wizard(t, dir, answers("openai", prov.URL, "sk-test-123", "m", "n", "y", "123:bot-token-abc", "n"))
	w.TelegramBase = bot.URL
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	if c := loadCfg(t, dir); len(c.Telegram.AllowedUsers) != 0 {
		t.Fatal(c.Telegram.AllowedUsers)
	}
	if !strings.Contains(out.String(), "tether pairing approve telegram") {
		t.Fatal(out.String())
	}
}

func TestTelegramNoStartTimesOut(t *testing.T) {
	dir := t.TempDir()
	prov := newFakeProvider(t)
	bot := startBot(t, func(int64) []telegram.Update { return []telegram.Update{} })
	w, out := wizard(t, dir, answers("openai", prov.URL, "sk-test-123", "m", "n", "y", "123:bot-token-abc"))
	w.TelegramBase = bot.URL
	w.TelegramWait = 300 * time.Millisecond
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	if !strings.Contains(out.String(), "No /start received; run tether setup again later.") {
		t.Fatal(out.String())
	}
	// The token is kept for the next run.
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if !strings.Contains(string(env), "TELEGRAM_BOT_TOKEN=123:bot-token-abc") {
		t.Fatal(string(env))
	}
}

func TestRerunKeepsValuesAndWritesBak(t *testing.T) {
	dir := t.TempDir()
	prov := newFakeProvider(t)
	w, out := wizard(t, dir, answers("openai", prov.URL, "sk-test-123", "glm-4.7", "n", "n"))
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	// The user adds a comment and a key that the wizard does not know.
	path := filepath.Join(dir, "config.yaml")
	b, _ := os.ReadFile(path)
	edited := "# my notes\n" + string(b) + "agent:\n  max_turns: 7 # fewer turns\n"
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}

	w, out = wizard(t, dir, answers())
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	c := loadCfg(t, dir)
	if p := c.Providers["primary"]; p.BaseURL != prov.URL || p.APIKey != "sk-test-123" || p.Model != "glm-4.7" {
		t.Fatal(p)
	}
	if c.Agent.MaxTurns != 7 {
		t.Fatal(c.Agent.MaxTurns)
	}
	now, _ := os.ReadFile(path)
	if !strings.Contains(string(now), "# my notes") || !strings.Contains(string(now), "# fewer turns") {
		t.Fatal(string(now))
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil || string(bak) != edited {
		t.Fatal(string(bak), err)
	}
	if fi, err := os.Stat(path + ".bak"); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatal(fi, err)
	}
	// The current values are the defaults of the questions.
	if !strings.Contains(out.String(), "["+prov.URL+"]") || !strings.Contains(out.String(), "[glm-4.7]") {
		t.Fatal(out.String())
	}
}

func TestProviderTestFailureReasksOnce(t *testing.T) {
	dir := t.TempDir()
	down := downProvider(t)
	prov := newFakeProvider(t)
	w, out := wizard(t, dir, answers("openai", down.URL, "sk-test-123", "m", "", prov.URL, "", "", "n", "n"))
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	if !strings.Contains(out.String(), "test call failed") || prov.calls.Load() != 1 {
		t.Fatal(out.String())
	}
	if p := loadCfg(t, dir).Providers["primary"]; p.BaseURL != prov.URL || p.APIKey != "sk-test-123" {
		t.Fatal(p)
	}

	// Two failures stop the wizard, and it writes nothing.
	dir2 := t.TempDir()
	w, out = wizard(t, dir2, answers("openai", down.URL, "sk-test-123", "m", "", "", "", ""))
	if err := w.Run(ctx); err == nil {
		t.Fatal("no error", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir2, "config.yaml")); err == nil {
		t.Fatal("config.yaml was written")
	}
}

func TestKeyReferenceKeepsEnv(t *testing.T) {
	dir := t.TempDir()
	prov := newFakeProvider(t)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("# keys\nMY_KEY=sk-mine-999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, out := wizard(t, dir, answers("openai", prov.URL, "${MY_KEY}", "m", "n", "n"))
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if !strings.Contains(string(cfg), "${MY_KEY}") {
		t.Fatal(string(cfg))
	}
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if string(env) != "# keys\nMY_KEY=sk-mine-999\n" {
		t.Fatal(string(env))
	}
}

func TestEnvValueQuoting(t *testing.T) {
	dir := t.TempDir()
	prov := newFakeProvider(t)
	w, out := wizard(t, dir, answers("openai", prov.URL, `a b#"c`, "m", "n", "n"))
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	dotenv, err := security.LoadDotEnv(filepath.Join(dir, ".env"))
	if err != nil || dotenv["PRIMARY_API_KEY"] != `a b#"c` {
		t.Fatal(dotenv, err)
	}
}
