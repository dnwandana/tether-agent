package setup

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
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

// fakeModels is the model list of the fake provider. It holds each model ID that a wizard
// test types, so that the typed ID is an exact match.
var fakeModels = []string{"z-ai/glm-5.3", "glm-5.3", "glm-5.3-flash", "glm-4.7", "deepseek-v4-pro",
	"llama3", "m", "m1", "m2", "deepseek-flash", "qwen3-max", "kimi-k3"}

// fakeProvider is a fake OpenAI endpoint. It answers a model list call with JSON and a chat
// call with the SSE fixture.
type fakeProvider struct {
	*httptest.Server
	calls     atomic.Int32 // the chat calls
	listCalls atomic.Int32 // the GET calls on a path that ends in /models
	auth      atomic.Value
	lastQuery atomic.Value // the raw query of the last model list call
}

// query returns the raw query of the last model list call.
func (p *fakeProvider) query() string {
	v, _ := p.lastQuery.Load().(string)
	return v
}

// lastAuth returns the Authorization header of the last request.
func (p *fakeProvider) lastAuth() string {
	v, _ := p.auth.Load().(string)
	return v
}

func newFakeProvider(t *testing.T) *fakeProvider { return newFakeProviderWith(t, fakeModels) }

// newFakeProviderWith returns a fake provider with the model list models.
func newFakeProviderWith(t *testing.T, models []string) *fakeProvider {
	t.Helper()
	sse, err := os.ReadFile(sseFixture)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.auth.Store(r.Header.Get("Authorization"))
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			p.listCalls.Add(1)
			p.lastQuery.Store(r.URL.RawQuery)
			// A search call gets only the IDs that contain the text, as the OpenRouter search.
			q := strings.ToLower(r.URL.Query().Get("q"))
			items := []map[string]string{}
			for _, id := range models {
				if strings.Contains(strings.ToLower(id), q) {
					items = append(items, map[string]string{"id": id})
				}
			}
			list, err := json.Marshal(map[string]any{"data": items})
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(list)
			return
		}
		p.calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(sse)
	}))
	t.Cleanup(p.Close)
	return p
}

// redirectClient returns a client that sends every request to the server at url, so a
// preset host reaches the fake provider.
func redirectClient(url string) *http.Client {
	return &http.Client{Transport: redirect{to: url}}
}

type redirect struct{ to string }

func (r redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	u, err := neturl.Parse(r.to)
	if err != nil {
		return nil, err
	}
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host, req.Host = u.Scheme, u.Host, u.Host
	return http.DefaultTransport.RoundTrip(req)
}

// writeConfig writes config.yaml in dir.
func writeConfig(t *testing.T, dir, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeEnv writes .env in dir.
func writeEnv(t *testing.T, dir, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
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
	w, out := wizard(t, dir, answers("11", "openai", prov.URL+"/v1", "sk-test-123", "glm-4.7", "n", "n"))
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
	eq(t, prov.listCalls.Load(), int32(1))
	if strings.Contains(out.String(), "sk-test-123") {
		t.Fatal("the key is in the output")
	}
	// Step 4 shows the host programs.
	if !strings.Contains(out.String(), "✓ python3") {
		t.Fatal(out.String())
	}
}

func TestFallbackStepCustomEntry(t *testing.T) {
	dir := t.TempDir()
	prov := newFakeProvider(t)
	w, out := wizard(t, dir, answers("11", "openai", prov.URL, "sk-one-111", "m1",
		"y", "y", "11", "fallback", "openai", prov.URL, "sk-two-222", "m2", "n", "n"))
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	if !strings.Contains(out.String(), "The fallback list is: none.") || !strings.Contains(out.String(), "Name of the entry [custom]: ") {
		t.Fatal(out.String())
	}
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if !strings.Contains(string(env), "FALLBACK_API_KEY=sk-two-222") {
		t.Fatal(string(env))
	}
	c := loadCfg(t, dir)
	fb := c.Providers["fallback"]
	if fb.Format != "openai" || fb.APIKey != "sk-two-222" || fb.Model != "m2" || strings.Join(c.Agent.Fallback, ",") != "fallback" {
		t.Fatal(fb, c.Agent.Fallback)
	}
}

func TestFallbackStepBuildsList(t *testing.T) {
	dir := t.TempDir()
	prov := newFakeProvider(t)
	// Step 1: OpenRouter primary. Step 2: change? y; add OpenRouter (name default openrouter-2,
	// own key), add DeepSeek (name default deepseek), then stop.
	w, out := wizard(t, dir, answers(
		"2", "or-1", "z-ai/glm-5.3",
		"y",
		"y", "2", "", "or-2", "z-ai/glm-5.3",
		"y", "6", "", "dk-1", "deepseek-v4-pro",
		"n",
		"n", "n"))
	w.HC = redirectClient(prov.URL)
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	if !strings.Contains(out.String(), "Step 2 of 5: the fallback providers (optional)") ||
		!strings.Contains(out.String(), "Name of the entry [openrouter-2]:") ||
		!strings.Contains(out.String(), "The fallback list is: openrouter-2, deepseek.") {
		t.Fatal(out.String())
	}
	if prov.calls.Load() != 3 {
		t.Fatal(prov.calls.Load())
	}
	c := loadCfg(t, dir)
	eq(t, strings.Join(c.Agent.Fallback, ","), "openrouter-2,deepseek")
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if !strings.Contains(string(env), "OPENROUTER_API_KEY=or-1") || !strings.Contains(string(env), "OPENROUTER_API_KEY_2=or-2") {
		t.Fatalf("%s", env)
	}
	if p := c.Providers["openrouter-2"]; p.Preset != "openrouter" || p.APIKey != "or-2" {
		t.Fatalf("%+v", p)
	}
}

func TestFallbackStepNoChangeKeepsList(t *testing.T) {
	dir := t.TempDir()
	prov := newFakeProvider(t)
	writeConfig(t, dir, "providers:\n  primary:\n    format: openai\n    base_url: "+prov.URL+"\n    api_key: ${PRIMARY_API_KEY}\n    model: m\n"+
		"  deepseek:\n    preset: deepseek\n    model: m\nagent:\n  fallback: [deepseek]\n")
	writeEnv(t, dir, "PRIMARY_API_KEY=sk-1\n")
	w, out := wizard(t, dir, answers("", "", "", "", "", "n", "n", "n"))
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	if !strings.Contains(out.String(), "The fallback list is: deepseek.") {
		t.Fatal(out.String())
	}
	eq(t, strings.Join(loadCfg(t, dir).Agent.Fallback, ","), "deepseek")
	cfg, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if !strings.Contains(string(cfg), "fallback: [deepseek]") {
		t.Fatalf("%s", cfg)
	}
}

func TestFallbackStepOldEntry(t *testing.T) {
	dir := t.TempDir()
	prov := newFakeProvider(t)
	writeConfig(t, dir, "providers:\n  primary:\n    format: openai\n    base_url: "+prov.URL+"\n    api_key: ${PRIMARY_API_KEY}\n    model: m\n"+
		"  fallback:\n    format: openai\n    base_url: "+prov.URL+"\n    api_key: ${FALLBACK_API_KEY}\n    model: m2\n")
	writeEnv(t, dir, "PRIMARY_API_KEY=sk-1\nFALLBACK_API_KEY=sk-2\n")
	// No new entry: the old entry stays in providers, but the list is empty.
	w, out := wizard(t, dir, answers("", "", "", "", "", "y", "n", "n", "n"))
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	if !strings.Contains(out.String(), "The fallback list is: fallback.") || !strings.Contains(out.String(), "The fallback list is: none.") {
		t.Fatal(out.String())
	}
	c := loadCfg(t, dir)
	if _, ok := c.Providers["fallback"]; !ok || c.Agent.Fallback == nil || len(c.FallbackList()) != 0 {
		t.Fatal(c.Providers, c.Agent.Fallback)
	}

	// The user adds the old entry again under its name. Its values are the defaults.
	w, out = wizard(t, dir, answers("", "", "", "", "", "y", "y", "11", "fallback", "", "", "", "", "n", "n", "n"))
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	if !strings.Contains(out.String(), "Model [m2]: ") {
		t.Fatal(out.String())
	}
	eq(t, strings.Join(loadCfg(t, dir).Agent.Fallback, ","), "fallback")
}

func TestFallbackStepRefusesNames(t *testing.T) {
	dir := t.TempDir()
	prov := newFakeProvider(t)
	w, out := wizard(t, dir, answers("6", "dk-1", "deepseek-v4-pro",
		"y", "y", "6", "primary", "my key", "deepseek", "", "deepseek-v4-pro", "y", "6", "deepseek", "deepseek-b", "", "deepseek-v4-pro", "n", "n", "n"))
	w.HC = redirectClient(prov.URL)
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	for _, want := range []string{"The name primary is for the primary provider.", "A name can contain only letters, digits",
		"The entry deepseek is in the fallback list already.", "The fallback list is: deepseek, deepseek-b."} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("no %q in\n%s", want, out.String())
		}
	}
}

func TestFallbackEntryNameMustNotBePrimary(t *testing.T) {
	if err := validEntryName("primary", map[string]bool{}); err == nil {
		t.Fatal("primary must be refused")
	}
	if err := validEntryName("deepseek", map[string]bool{"deepseek": true}); err == nil {
		t.Fatal("a name in the list must be refused")
	}
	if err := validEntryName("deep seek", map[string]bool{}); err == nil {
		t.Fatal("a space must be refused")
	}
	if err := validEntryName("openrouter-2", map[string]bool{}); err != nil {
		t.Fatal(err)
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
	w, out := wizard(t, dir, answers("11", "openai", prov.URL, "sk-test-123", "m", "n", "y", "123:bot-token-abc", "y"))
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
	w, out := wizard(t, dir, answers("11", "openai", prov.URL, "sk-test-123", "m", "n", "y", "123:bot-token-abc", "n"))
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
	w, out := wizard(t, dir, answers("11", "openai", prov.URL, "sk-test-123", "m", "n", "y", "123:bot-token-abc"))
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
	w, out := wizard(t, dir, answers("11", "openai", prov.URL, "sk-test-123", "glm-4.7", "n", "n"))
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
	w, out := wizard(t, dir, answers("11", "openai", down.URL, "sk-test-123", "m", "", "", prov.URL, "", "", "n", "n"))
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
	w, out = wizard(t, dir2, answers("11", "openai", down.URL, "sk-test-123", "m", "", "", "", "", ""))
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
	w, out := wizard(t, dir, answers("11", "openai", prov.URL, "${MY_KEY}", "m", "n", "n"))
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
	w, out := wizard(t, dir, answers("11", "openai", prov.URL, `a b#"c`, "m", "n", "n"))
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	dotenv, err := security.LoadDotEnv(filepath.Join(dir, ".env"))
	if err != nil || dotenv["PRIMARY_API_KEY"] != `a b#"c` {
		t.Fatal(dotenv, err)
	}
}

func TestPresetMenuZaiCodingPlan(t *testing.T) {
	dir := t.TempDir()
	prov := newFakeProvider(t)
	w, out := wizard(t, dir, answers("5", "2", "zk-123", "glm-5.3", "n", "n"))
	w.HC = redirectClient(prov.URL)
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	menu := "   1) xAI\n   2) OpenRouter\n   3) OpenAI\n   4) Anthropic\n   5) Z.AI\n   6) DeepSeek\n" +
		"   7) Moonshot\n   8) Alibaba\n   9) MiniMax\n  10) Ollama\n  11) Custom\n"
	if !strings.Contains(out.String(), menu) || !strings.Contains(out.String(), "   1) Pay as you go\n   2) Coding Plan\n") ||
		!strings.Contains(out.String(), "API key (ZAI_API_KEY)") || !strings.Contains(out.String(), "Provider [1]: ") ||
		!strings.Contains(out.String(), "Plan [1]: ") {
		t.Fatal(out.String())
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if !strings.Contains(string(cfg), "preset: zai-coding") || strings.Contains(string(cfg), "base_url") ||
		strings.Contains(string(cfg), "api_key") || !strings.Contains(string(env), "ZAI_API_KEY=zk-123") {
		t.Fatalf("%s\n%s", cfg, env)
	}
	if strings.Contains(out.String(), "zk-123") {
		t.Fatal("the key is in the output")
	}
	p := loadCfg(t, dir).Providers["primary"]
	if p.Preset != "zai-coding" || p.APIKey != "zk-123" || p.Model != "glm-5.3" {
		t.Fatalf("%+v", p)
	}

	// A second run shows the current preset and plan as the defaults.
	w, out = wizard(t, dir, answers())
	w.HC = redirectClient(prov.URL)
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	if !strings.Contains(out.String(), "Provider [5]: ") || !strings.Contains(out.String(), "Plan [2]: ") ||
		!strings.Contains(out.String(), "Model [glm-5.3]: ") {
		t.Fatal(out.String())
	}
}

func TestPresetMenuOllamaAsksNoSecondQuestion(t *testing.T) {
	dir := t.TempDir()
	prov := newFakeProvider(t)
	w, out := wizard(t, dir, answers("10", "ok-1", "glm-5.3-flash", "n", "n"))
	w.HC = redirectClient(prov.URL)
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	if strings.Contains(out.String(), "Where") || !strings.Contains(out.String(), "API key (OLLAMA_API_KEY)") ||
		!strings.Contains(out.String(), "Model: ") || strings.Contains(out.String(), "Model [") {
		t.Fatal(out.String())
	}
	if p := loadCfg(t, dir).Providers["primary"]; p.Preset != "ollama-cloud" || p.APIKey != "ok-1" || p.Model != "glm-5.3-flash" {
		t.Fatalf("%+v", p)
	}
}

func TestPresetEntryLosesFormatAndBaseURL(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "providers:\n  primary:\n    format: openai\n    base_url: https://old/v1\n    api_key: ${PRIMARY_API_KEY}\n    model: m\n")
	prov := newFakeProvider(t)
	w, _ := wizard(t, dir, answers("6", "dk-1", "deepseek-v4-pro", "n", "n"))
	w.HC = redirectClient(prov.URL)
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if strings.Contains(string(cfg), "format:") || strings.Contains(string(cfg), "base_url:") || !strings.Contains(string(cfg), "preset: deepseek") {
		t.Fatalf("%s", cfg)
	}
}

func TestPresetMenuRefusesBadNumber(t *testing.T) {
	dir := t.TempDir()
	prov := newFakeProvider(t)
	w, out := wizard(t, dir, answers("12", "x", "6", "dk-1", "deepseek-v4-pro", "n", "n"))
	w.HC = redirectClient(prov.URL)
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	if strings.Count(out.String(), "Enter a number from 1 to 11.") != 2 {
		t.Fatal(out.String())
	}
}

func TestCustomFormatAcceptsResponses(t *testing.T) {
	prov := newFakeProvider(t)
	w := &Wizard{In: answers("11", "responses", "https://api.example.com/v1", "sk-1", "m"), Out: &bytes.Buffer{},
		HC: redirectClient(prov.URL), dotenv: map[string]string{}}
	w.cfg = mustConfigDoc(t, "")
	a, err := w.askProvider(ctx, "primary", "PRIMARY_API_KEY", providerAnswers{})
	if err != nil || a.preset != "" || a.format != "responses" || a.baseURL != "https://api.example.com/v1" {
		t.Fatalf("%+v %v", a, err)
	}
	if !strings.Contains(w.Out.(*bytes.Buffer).String(), "Format (openai, anthropic, or responses) [openai]: ") {
		t.Fatal(w.Out.(*bytes.Buffer).String())
	}
}

// mustConfigDoc returns the config document of the YAML text.
func mustConfigDoc(t *testing.T, text string) *configDoc {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := readConfigDoc(path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPresetChangeUsesNewKeyEnv(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "providers:\n  primary:\n    preset: zai\n    api_key: ${PRIMARY_API_KEY}\n    model: glm-5.3\n")
	writeEnv(t, dir, "PRIMARY_API_KEY=zk-old\nDEEPSEEK_API_KEY=dk-env\n")
	prov := newFakeProvider(t)
	// DeepSeek, then Enter on the key: it keeps DEEPSEEK_API_KEY, not the Z.AI key.
	w, out := wizard(t, dir, answers("6", "", "deepseek-v4-pro", "n", "n"))
	w.HC = redirectClient(prov.URL)
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	if got := prov.lastAuth(); got != "Bearer dk-env" {
		t.Fatal(got)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if strings.Contains(string(cfg), "PRIMARY_API_KEY") || strings.Contains(string(cfg), "api_key") {
		t.Fatalf("%s", cfg)
	}
	// The model of the old preset is not the default, and a preset has no default model.
	if !strings.Contains(out.String(), "Model: ") || strings.Contains(out.String(), "Model [") {
		t.Fatal(out.String())
	}
}

func TestPresetToCustomUsesEntryKeyVar(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "providers:\n  primary:\n    preset: deepseek\n    model: deepseek-v4-pro\n")
	writeEnv(t, dir, "DEEPSEEK_API_KEY=dk-env\n")
	prov := newFakeProvider(t)
	// Custom, then a proxy URL and Enter on the key: the preset key does not go to the proxy.
	w, out := wizard(t, dir, answers("11", "", prov.URL, "", "", "n", "n"))
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	if !strings.Contains(out.String(), "Format (openai, anthropic, or responses) [openai]: ") ||
		!strings.Contains(out.String(), "Note: no API key is set.") || strings.Contains(prov.lastAuth(), "dk-env") {
		t.Fatal(prov.lastAuth(), out.String())
	}
}

func TestCustomEntryWithNoKeyGetsNoAPIKey(t *testing.T) {
	dir := t.TempDir()
	prov := newFakeProvider(t)
	// A local model server needs no key: Enter at the key question.
	w, out := wizard(t, dir, answers("11", "", prov.URL, "", "llama3", "n", "n"))
	if err := w.Run(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if strings.Contains(string(cfg), "api_key") || prov.lastAuth() != "" {
		t.Fatalf("%s %q", cfg, prov.lastAuth())
	}
	if p := loadCfg(t, dir).Providers["primary"]; p.Format != "openai" || p.APIKey != "" || p.Model != "llama3" {
		t.Fatalf("%+v", p)
	}
}

func TestSharedKeyEnvGetsSuffix(t *testing.T) {
	w := &Wizard{dotenv: map[string]string{"OPENROUTER_API_KEY": "or-1"}}
	w.cfg = mustConfigDoc(t, "providers:\n  primary:\n    preset: openrouter\n    model: m\n")
	eq(t, w.keyVarFor("openrouter-2", "OPENROUTER_API_KEY", "or-2"), "OPENROUTER_API_KEY_2")
	eq(t, w.keyVarFor("openrouter-2", "OPENROUTER_API_KEY", "or-1"), "OPENROUTER_API_KEY") // the same key: share it
	eq(t, w.keyVarFor("primary", "OPENROUTER_API_KEY", "or-9"), "OPENROUTER_API_KEY")      // the owner of the var
	// A used suffix is skipped, and a suffix with the same key is shared.
	w.dotenv["OPENROUTER_API_KEY_2"] = "or-2"
	eq(t, w.keyVarFor("openrouter-3", "OPENROUTER_API_KEY", "or-3"), "OPENROUTER_API_KEY_3")
	eq(t, w.keyVarFor("openrouter-3", "OPENROUTER_API_KEY", "or-2"), "OPENROUTER_API_KEY_2")
}

func TestSecondPresetEntryKeepsFirstKey(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "providers:\n  primary:\n    preset: openrouter\n    model: z-ai/glm-5.3\n")
	writeEnv(t, dir, "OPENROUTER_API_KEY=or-1\n")
	prov := newFakeProvider(t)
	w, out := wizard(t, dir, nil)
	w.HC = redirectClient(prov.URL)
	w.In = answers("2", "or-2", "m2")
	w.cfg, _ = readConfigDoc(w.configPath())
	w.env, _ = readEnvFile(w.envPath())
	w.dotenv, _ = security.LoadDotEnv(w.envPath())
	w.saved = map[string]bool{}
	if err := w.providerStep(ctx, "openrouter-2", "OPENROUTER_2_API_KEY", nil); err != nil {
		t.Fatal(err, out.String())
	}
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	cfg, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if !strings.Contains(string(env), "OPENROUTER_API_KEY=or-1\n") || !strings.Contains(string(env), "OPENROUTER_API_KEY_2=or-2\n") ||
		!strings.Contains(string(cfg), "api_key: ${OPENROUTER_API_KEY_2}") {
		t.Fatalf("%s\n%s", env, cfg)
	}
	if got := prov.lastAuth(); got != "Bearer or-2" {
		t.Fatal(got)
	}
}
