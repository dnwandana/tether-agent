package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/gatewaylock"
	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/telegram"
	"github.com/dnwandana/tether-agent/internal/tools"
)

var ctx = context.Background()

// fakeProviderURL starts a fake OpenAI provider that answers each request with the text
// fixture of the provider package.
func fakeProviderURL(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../provider/testdata/openai_text_stream.sse")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// writeConfig writes a config.yaml with one provider at url and no price sources.
func writeConfig(t *testing.T, dir, url string, extra ...string) {
	t.Helper()
	cfg := "providers:\n  primary:\n    format: openai\n    base_url: " + url + "\n    api_key: k\n    model: m\n" +
		"pricing:\n  sources: []\n" + strings.Join(extra, "")
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

// newApp returns an App over a new data dir. The test closes it at the end.
func newApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	writeConfig(t, dir, fakeProviderURL(t))
	a, err := New(ctx, Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func TestNewRegistersCoreToolsAndDirs(t *testing.T) {
	dir := t.TempDir()
	// The default config has no image backend, so the test sets one.
	writeConfig(t, dir, fakeProviderURL(t), "media:\n  image:\n    backend: command\n    command: imagegen {prompt} {out}\n")
	a, err := New(ctx, Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, n := range []string{
		"terminal", "read_file", "write_file", "patch", "web_fetch", "web_search",
		"memory", "clarify", "session_search", "skill_view", "skills_list", "skill_manage", "cronjob",
		"browser_navigate", "browser_click", "browser_fill", "browser_read", "browser_take_screenshot",
		"browser_run_js", "browser_cookies", "text_to_speech", "transcribe_audio", "generate_image",
	} {
		if _, ok := a.Registry.Get(n); !ok {
			t.Fatal(n)
		}
	}
	for _, d := range []string{"cache/spill", "cache/media", "cache/screenshots", "logs", "memory", "skills", "scripts", "exports"} {
		fi, err := os.Stat(filepath.Join(a.DataDir, d))
		if err != nil || !fi.IsDir() {
			t.Fatal(d)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: mode %v", d, fi.Mode().Perm())
		}
	}
	// The bootstrap skills are copied.
	if _, err := os.Stat(filepath.Join(a.DataDir, "skills", "tether-self", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	ag := a.Agent
	if ag == nil || ag.Prices == nil || ag.Redactor == nil || ag.DB == nil || ag.Tools != a.Registry ||
		ag.Memory != a.Memory || ag.Skills != a.Skills || ag.Prompts == nil || ag.Compactor == nil || ag.WorkDir == "" {
		t.Fatalf("%+v", ag)
	}
	if a.Browser == nil || a.Media == nil || a.Browser.ChromePath != "" {
		t.Fatal("New must not look for Chrome")
	}
	if a.Prov == nil || a.Providers["primary"] == nil || a.Memory == nil || a.Cron == nil {
		t.Fatal("parts missing")
	}
}

func TestNewMissingProvider(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("log:\n  level: info\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(ctx, Options{DataDir: dir}); err == nil || !strings.Contains(err.Error(), "providers.primary") {
		t.Fatal(err)
	}
}

func TestNewEnvKeepsValuesPerSession(t *testing.T) {
	a := newApp(t)
	s1 := &sessions.Session{ID: "s1", Key: "cli:t", Kind: "chat"}
	s2 := &sessions.Session{ID: "s2", Key: "cli:t", Kind: "chat"}
	e1 := a.NewEnv(s1, "cli")
	e1.Values["x"] = 1
	if e := a.NewEnv(s1, "cli"); e.Values["x"] != 1 {
		t.Fatal("the Values map must stay the same across turns")
	}
	if e := a.NewEnv(s2, "cli"); e.Values["x"] != nil {
		t.Fatal("the Values map is per session")
	}
	if e1.DataDir != a.DataDir || e1.SessionID != "s1" || e1.SessionKey != "cli:t" || e1.Channel != "cli" || e1.WorkDir == "" {
		t.Fatalf("%+v", e1)
	}
}

func TestWriteFileRefusesProtectedPath(t *testing.T) {
	a := newApp(t)
	env := a.NewEnv(&sessions.Session{ID: "s1"}, "cli")
	visible := map[string]bool{"write_file": true}
	res := a.Registry.Dispatch(ctx, "write_file", []byte(`{"path":"`+filepath.Join(a.DataDir, "config.yaml")+`","content":"x"}`), env, visible)
	if !res.IsError {
		t.Fatal(res.Text)
	}
}

func TestSessionPromptHasMemoryToolNotesAndOverride(t *testing.T) {
	a := newApp(t)
	if _, err := a.Memory.Apply(memory.Op{Action: "add", Target: "memory", Content: "VPS = hetzner"}); err != nil {
		t.Fatal(err)
	}
	p, _ := a.Agent.Prompts.Get(ctx, &sessions.Session{ID: "s"}, agent.PromptKey{SessionID: "s"}, "cli")
	first, _, _ := strings.Cut(strings.TrimSpace(agent.BasePrompt()), "\n")
	for _, want := range []string{"## TOOL NOTES", "Channel: cli", "hetzner", "tether-self"} {
		if !strings.Contains(p, want) {
			t.Fatalf("no %q in:\n%s", want, p)
		}
	}
	if !strings.HasPrefix(p, first) {
		t.Fatal(p)
	}
	s := &sessions.Session{ID: "s2", PromptOverride: "Answer in Indonesian."}
	p, _ = a.Agent.Prompts.Get(ctx, s, agent.PromptKey{SessionID: "s2", PromptOverride: s.PromptOverride}, "cli")
	if !strings.HasPrefix(p, "Answer in Indonesian.") {
		t.Fatal(p)
	}
}

func TestCLIToolsetsComeFromConfig(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, fakeProviderURL(t), "toolsets:\n  cli: [file]\n")
	a, err := New(ctx, Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	names, _ := a.Registry.Visible(a.Deps().Toolsets("cli"), nil)
	if !names["read_file"] || names["terminal"] {
		t.Fatal(names)
	}
	if _, ok := a.Registry.Get("terminal"); !ok {
		t.Fatal("the registry keeps all tools")
	}
}

func TestHousekeepingRemovesOldSpill(t *testing.T) {
	a := newApp(t)
	spill := filepath.Join(a.DataDir, "cache", "spill")
	old, fresh := filepath.Join(spill, "old.txt"), filepath.Join(spill, "fresh.txt")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	hctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.Housekeeping(hctx)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("the old spill file must be removed at startup")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("the new spill file must stay")
	}
}

func TestChatDoesNotTakeLock(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, fakeProviderURL(t))
	release, err := gatewaylock.Acquire(filepath.Join(dir, "gateway.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	a, err := New(ctx, Options{DataDir: dir})
	if err != nil {
		t.Fatal("chat must start next to a running gateway:", err)
	}
	defer a.Close()
	if g := a.Gateway(); g == nil || g.Deps == nil || g.Deps.Agent != a.Agent || g != a.Gateway() {
		t.Fatal("Gateway")
	}
}

func TestNewBuildsApprovalGate(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, fakeProviderURL(t), "approvals:\n  mode: smart\n  timeout_s: 42\n  cron_mode: approve\n")
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("SUDO_PASSWORD=pw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := New(ctx, Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	g := a.Gate
	if g == nil || g.Mode != "smart" || g.CronMode != "approve" || g.Timeout != 42*time.Second || g.DB != a.DB ||
		!g.SudoPasswordSet || g.Judge == nil || g.Log == nil {
		t.Fatalf("%+v", g)
	}
	if a.CLIAsk == nil || a.Deps().CLIAsk != a.CLIAsk {
		t.Fatal("the CLI prompt is not wired")
	}
}

func TestNewEnvSetsGateAndAsk(t *testing.T) {
	a := newApp(t)
	cli := a.NewEnv(&sessions.Session{ID: "s1", Key: "cli:t", Kind: "chat"}, "cli")
	ga, ok := cli.Gate.(tools.GateApprover)
	if !ok || ga.Gate != a.Gate || ga.Prompter != security.Prompter(a.CLIAsk) || cli.Ask != tools.Asker(a.CLIAsk) {
		t.Fatalf("%+v", cli)
	}
	// Other channels get the gate with no CLI prompt: a dangerous command is blocked
	// until the channel has its own prompt.
	tg := a.NewEnv(&sessions.Session{ID: "s2", Key: "telegram:dm:1", Kind: "chat"}, "telegram")
	ga, ok = tg.Gate.(tools.GateApprover)
	if !ok || ga.Gate != a.Gate || ga.Prompter != nil || tg.Ask != nil {
		t.Fatalf("%+v", tg)
	}
	if none := a.NewEnv(nil, "cli"); none.Gate == nil {
		t.Fatal("an env with no session must have the gate too")
	}
}

func TestRunCommandNeedsApprovalInChat(t *testing.T) {
	a := newApp(t)
	env := a.NewEnv(&sessions.Session{ID: "s1", Key: "telegram:dm:1", Kind: "chat"}, "telegram")
	visible := map[string]bool{"terminal": true}
	res := a.Registry.Dispatch(ctx, "terminal", []byte(`{"command":"rm -rf /tmp/tether-app-none"}`), env, visible)
	if !res.IsError || !strings.Contains(res.Text, "BLOCKED") {
		t.Fatal(res)
	}
	res = a.Registry.Dispatch(ctx, "terminal", []byte(`{"command":"echo safe"}`), env, visible)
	if res.IsError || !strings.Contains(res.Text, "safe") {
		t.Fatal(res)
	}
}

func TestClarifyTimeoutFromConfig(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, fakeProviderURL(t), "approvals:\n  timeout_s: 33\n")
	a, err := New(ctx, Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	tool, ok := a.Registry.Get("clarify")
	if !ok || tool.(tools.ClarifyTool).Timeout != 33*time.Second {
		t.Fatal(tool)
	}
}

func TestNewWiresMCPAndReviewsWithoutNetwork(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "no", http.StatusTeapot)
	}))
	defer srv.Close()
	dir := t.TempDir()
	writeConfig(t, dir, srv.URL)
	a, err := New(ctx, Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if a.MCP == nil || len(a.MCP.Servers) != 0 || a.MCP.CacheDir != filepath.Join(dir, "cache", "mcp-tools") {
		t.Fatalf("%+v", a.MCP)
	}
	if st := a.MCP.Stats(); st.Servers != 0 || st.Tools != 0 {
		t.Fatalf("%+v", st)
	}
	if a.Agent.MCPGen == nil || a.Agent.MCPGen() != a.MCP.Generation() {
		t.Fatal("Agent.MCPGen is not wired")
	}
	if a.Agent.Compactor.OnCompact == nil || a.Deps().BeforeNew == nil {
		t.Fatal("the review hooks are not wired")
	}
	if _, ok := gateway.Commands()["reload-mcp"]; !ok {
		t.Fatal("/reload-mcp is not registered")
	}
	for _, tool := range a.Registry.All() {
		if strings.HasPrefix(tool.Name(), "mcp_") {
			t.Fatal("no MCP server is configured, but a tool is registered:", tool.Name())
		}
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if a.MCP != nil || a.mcpStop != nil {
		t.Fatal("Close must stop MCP")
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("New made %d network calls", n)
	}
}

// fakeBotAPI starts a fake Bot API. getMe returns the bot "tether_bot", and every other
// method succeeds. calls receives the method names.
func fakeBotAPI(t *testing.T, getMeOK bool) (string, *[]string, *sync.Mutex) {
	t.Helper()
	var (
		mu    sync.Mutex
		calls []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		mu.Lock()
		calls = append(calls, method)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case method == "getMe" && !getMeOK:
			_, _ = w.Write([]byte(`{"ok":false,"error_code":401,"description":"Unauthorized"}`))
		case method == "getMe":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":99,"is_bot":true,"username":"tether_bot"}}`))
		case method == "sendMessage":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":7,"chat":{"id":5}}}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &calls, &mu
}

// keepCommandTable restores the global command table at the end of t. The gateway role
// registers /restart in it.
func keepCommandTable(t *testing.T) {
	t.Helper()
	saved := gateway.Commands()
	t.Cleanup(func() {
		for name := range gateway.Commands() {
			if _, ok := saved[name]; !ok {
				gateway.Unregister(name)
			}
		}
		for _, c := range saved {
			gateway.Register(c)
		}
	})
}

// gatewayApp returns an App of the gateway role with the Telegram config in extra.
func gatewayApp(t *testing.T, botURL string, extra string) *App {
	t.Helper()
	keepCommandTable(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	dir := t.TempDir()
	writeConfig(t, dir, fakeProviderURL(t), extra)
	a, err := New(ctx, Options{DataDir: dir, Gateway: true, TelegramBaseURL: botURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func TestGatewayOptionBuildsScheduler(t *testing.T) {
	if a := newApp(t); a.Sched != nil {
		t.Fatal("the chat role must not build the scheduler")
	} else if ct, _ := a.Registry.Get("cronjob"); ct.(tools.CronJobTool).RunNow != nil {
		t.Fatal("the chat role has no RunNow")
	} else if m := ct.(tools.CronJobTool).MissedRuns; m != "skip" {
		t.Fatalf("the cronjob tool must get cron.missed_runs: %q", m)
	}
	a := gatewayApp(t, "", "")
	if a.Sched == nil || a.Sched.Runner == nil || a.Sched.Runner.Turn == nil || a.Sched.Deliver == nil || a.Sched.Store != a.Cron {
		t.Fatalf("%+v", a.Sched)
	}
	ct, ok := a.Registry.Get("cronjob")
	if !ok || ct.(tools.CronJobTool).RunNow == nil {
		t.Fatal("the gateway role must give RunNow to the cronjob tool")
	}
}

func TestCronTurnRunsWithCronKind(t *testing.T) {
	a := gatewayApp(t, "", "")
	sess, err := a.Store.Create(ctx, sessions.Session{Source: "cron", Kind: "cron", Key: "cron:j1"})
	if err != nil {
		t.Fatal(err)
	}
	env := a.NewEnv(sess, "cron")
	if env.Kind != "cron" || env.Ask != nil || env.Gate.(tools.GateApprover).Prompter != nil {
		t.Fatalf("%+v", env)
	}
	final, err := a.cronTurn(ctx, cron.CronTurn{Session: sess, Text: "report"})
	if err != nil || final != "Hello, world." {
		t.Fatal(final, err)
	}
	a.valuesMu.Lock()
	_, kept := a.values[sess.ID]
	a.valuesMu.Unlock()
	if kept {
		t.Fatal("the per-session values of a cron run must be freed")
	}
	// A "This session" answer of the run must not stay in the gate.
	req := security.ApprovalRequest{SessionID: sess.ID, Kind: "chat", Command: "rm -rf /tmp/tether-app-none"}
	if err := a.Gate.Check(ctx, req, sessionApprover{}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.cronTurn(ctx, cron.CronTurn{Session: sess, Text: "report"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Gate.Check(ctx, req, nil); err == nil {
		t.Fatal("a cron run must reset the session answers of the gate")
	}
}

func TestAddTelegramNeedsToken(t *testing.T) {
	a := gatewayApp(t, "", "telegram:\n  enabled: true\n")
	if _, err := a.AddTelegram(ctx); err == nil || !strings.Contains(err.Error(), "bot token") {
		t.Fatal(err)
	}
	url, _, _ := fakeBotAPI(t, false)
	b := gatewayApp(t, url, "telegram:\n  enabled: true\n  bot_token: T\n")
	if _, err := b.AddTelegram(ctx); err == nil || !strings.Contains(err.Error(), "getMe") {
		t.Fatal(err)
	}
	if len(b.Gateway().Channels) != 0 {
		t.Fatal("a failed check must not add the channel")
	}
}

func TestAddTelegramWiresChannelCronAndPrompts(t *testing.T) {
	url, calls, mu := fakeBotAPI(t, true)
	a := gatewayApp(t, url, "telegram:\n  enabled: true\n  bot_token: T\n  cron_thread: \"9\"\n")
	ch, err := a.AddTelegram(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Bot.Username != "tether_bot" || a.Gateway().Channels["telegram"] != gateway.Channel(ch) || a.Telegram != ch {
		t.Fatalf("%+v", ch)
	}
	if ch.Inbox == nil || ch.Out == nil || ch.UI == nil || ch.Pair == nil {
		t.Fatalf("%+v", ch)
	}
	// The cron deliverer sends through the channel and finds the home channel.
	if err := a.Sched.Deliver.Send(ctx, cron.Target{Source: "telegram", ChatID: "5"}, "done", nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	sent := strings.Join(*calls, " ")
	mu.Unlock()
	if !strings.Contains(sent, "sendMessage") {
		t.Fatal(sent)
	}
	if _, ok := a.Sched.Deliver.Home(ctx, true); ok {
		t.Fatal("no home is set yet")
	}
	if err := telegram.SetHome(ctx, a.DB, "-100", "17585"); err != nil {
		t.Fatal(err)
	}
	if to, ok := a.Sched.Deliver.Home(ctx, true); !ok || to.ChatID != "-100" || to.ThreadID != "9" {
		t.Fatalf("%+v", to)
	}
	// A Telegram session asks in its own chat with buttons.
	env := a.NewEnv(&sessions.Session{ID: "s1", Key: "telegram:dm:5", Source: "telegram", ChatID: "5", Kind: "chat"}, "telegram")
	ui, ok := env.Ask.(*telegram.ChatUI)
	if !ok || ui.ChatID != 5 || env.Gate.(tools.GateApprover).Prompter != security.Prompter(ui) {
		t.Fatalf("%+v", env)
	}
	if info := a.Agent.ChatInfo(&sessions.Session{Source: "telegram", Key: "telegram:group:-1", ChatID: "-1"}); !strings.Contains(info, "@tether_bot") {
		t.Fatal(info)
	}
}

func TestAddTelegramWiresStickerInbox(t *testing.T) {
	url, _, _ := fakeBotAPI(t, true)
	a := gatewayApp(t, url, "telegram:\n  enabled: true\n  bot_token: T\n")
	ch, err := a.AddTelegram(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ch.Inbox.Stickers.(*telegram.KVStickers); !ok || ch.Inbox.Describe != nil {
		t.Fatalf("stickers %T, describe set %v", ch.Inbox.Stickers, ch.Inbox.Describe != nil)
	}
	v := gatewayApp(t, url, "telegram:\n  enabled: true\n  bot_token: T\nagent:\n  vision_provider: primary\n")
	vch, err := v.AddTelegram(ctx)
	if err != nil || vch.Inbox.Describe == nil {
		t.Fatalf("a vision provider must set Describe: %v", err)
	}
}

func TestDelegationAndOptionalTools(t *testing.T) {
	a := newApp(t)
	if _, ok := a.Registry.Get("delegate_task"); !ok || a.Delegator == nil {
		t.Fatal("delegate_task must be registered")
	}
	if a.Batches != nil {
		t.Fatal("the chat role has no background batches")
	}
	if _, ok := a.Registry.Get("view_image"); ok {
		t.Fatal("view_image needs a vision provider")
	}
	_, hasRun := a.Registry.Get("execute_code")
	if _, err := exec.LookPath("python3"); (err == nil) != hasRun {
		t.Fatalf("execute_code must exist exactly when python3 exists: %v", hasRun)
	}
	if a.Deps().CancelKey == nil {
		t.Fatal("Deps.CancelKey must be set")
	}
	a.Deps().CancelKey("cli:t") // no batches: does nothing

	g := gatewayApp(t, "", "")
	if g.Batches == nil || g.Delegator.Batches != g.Batches {
		t.Fatal("the gateway role runs background batches")
	}
	hctx, stop := context.WithCancel(ctx)
	g.Housekeeping(hctx)
	stop()

	dir := t.TempDir()
	writeConfig(t, dir, fakeProviderURL(t))
	b, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	vcfg := strings.Replace(string(b), "    model: m\n", "    model: m\n    vision: true\n", 1)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(vcfg), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := New(ctx, Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if _, ok := v.Registry.Get("view_image"); !ok {
		t.Fatal("view_image must exist with a vision provider")
	}
}

func TestNewEnvProgress(t *testing.T) {
	a := newApp(t)
	// The CLI env has no progress function: gateway.Deps.Turn sends the child lines
	// through the CLI sink, under the lock of the sink.
	if a.NewEnv(&sessions.Session{ID: "s1", Key: "cli:t"}, "cli").Progress != nil {
		t.Fatal("the CLI env must not write the progress lines past the sink")
	}
	url, _, _ := fakeBotAPI(t, true)
	g := gatewayApp(t, url, "telegram:\n  enabled: true\n  bot_token: T\n")
	if _, err := g.AddTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	tg := g.NewEnv(&sessions.Session{ID: "s2", Key: "telegram:dm:5", Source: "telegram", ChatID: "5"}, "telegram")
	if tg.Progress == nil {
		t.Fatal("a Telegram env must route the child lines to the status message")
	}
	tg.Progress("no turn runs, so nothing happens")
}

func TestCloseWaitsForTheMCPReaper(t *testing.T) {
	old := mcpReapEvery
	mcpReapEvery = time.Millisecond
	defer func() { mcpReapEvery = old }()
	dir := t.TempDir()
	writeConfig(t, dir, fakeProviderURL(t))
	a, err := New(ctx, Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	// Let the reaper run some times, so that the race detector sees its reads of the manager.
	time.Sleep(30 * time.Millisecond)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if a.MCP != nil || a.mcpStop != nil {
		t.Fatal("Close must stop MCP")
	}
}

// sessionApprover answers each approval question with "This session".
type sessionApprover struct{}

func (sessionApprover) PromptApproval(context.Context, security.ApprovalRequest) (security.Scope, error) {
	return security.ScopeSession, nil
}

func TestNewAndDeleteFreeSessionState(t *testing.T) {
	a := newApp(t)
	old, err := a.Store.Create(ctx, sessions.Session{Key: "cli:t", Source: "cli", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	const cmd = "rm -rf /tmp/tether-app-none"
	req := security.ApprovalRequest{SessionID: old.ID, SessionKey: old.Key, Kind: "chat", Command: cmd}
	prime := func() {
		a.NewEnv(old, "cli").Values["x"] = 1
		if err := a.Gate.Check(ctx, req, sessionApprover{}); err != nil {
			t.Fatal(err)
		}
		a.Agent.Prompts.Get(ctx, old, agent.PromptKey{SessionID: old.ID}, "cli")
		if a.Agent.Prompts.Len() != 1 {
			t.Fatal("the prompt is not cached")
		}
	}
	freed := func(how string) {
		t.Helper()
		a.valuesMu.Lock()
		_, kept := a.values[old.ID]
		a.valuesMu.Unlock()
		if kept {
			t.Fatal(how, "must free the Values map of the session")
		}
		if err := a.Gate.Check(ctx, req, nil); err == nil {
			t.Fatal(how, "must reset the session answers of the approval gate")
		}
		if a.Agent.Prompts.Len() != 0 {
			t.Fatal(how, "must forget the frozen prompt")
		}
	}

	prime()
	a.Deps().BeforeNew(ctx, old)
	freed("/new")

	prime()
	a.ForgetSession(old.ID)
	freed("/delete")
}

func TestHousekeepingPrunesOldAutomaticSessions(t *testing.T) {
	a := newApp(t)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	a.clock = func() time.Time { return now }
	day := 24 * time.Hour
	mk := func(kind, source string, age time.Duration) *sessions.Session {
		t.Helper()
		s, err := a.Store.Create(ctx, sessions.Session{Source: source, Kind: kind})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.DB.ExecContext(ctx, `UPDATE sessions SET last_active = ? WHERE id = ?`, now.Add(-age).UnixMilli(), s.ID); err != nil {
			t.Fatal(err)
		}
		return s
	}
	oldCron := mk("cron", "cron", 91*day)
	oldSub := mk(agent.KindSubagent, "telegram", 200*day)
	oldReview := mk(agent.KindReview, "cli", 91*day)
	newCron := mk("cron", "cron", 89*day)
	oldChat := mk("chat", "telegram", 500*day)
	a.NewEnv(oldCron, "cron").Values["x"] = 1

	hctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.Housekeeping(hctx)

	for _, s := range []*sessions.Session{oldCron, oldSub, oldReview} {
		if _, err := a.Store.Get(ctx, s.ID); err == nil {
			t.Fatalf("the old %s session must be deleted", s.Kind)
		}
	}
	for _, s := range []*sessions.Session{newCron, oldChat} {
		if _, err := a.Store.Get(ctx, s.ID); err != nil {
			t.Fatalf("the %s session must stay: %v", s.Kind, err)
		}
	}
	a.valuesMu.Lock()
	_, kept := a.values[oldCron.ID]
	a.valuesMu.Unlock()
	if kept {
		t.Fatal("the prune must free the Values map of a deleted session")
	}
}

func TestHousekeepingRetentionZeroKeepsAll(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, fakeProviderURL(t), "sessions:\n  retention_days: 0\n")
	a, err := New(ctx, Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	s, err := a.Store.Create(ctx, sessions.Session{Source: "cron", Kind: "cron"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.ExecContext(ctx, `UPDATE sessions SET last_active = 0 WHERE id = ?`, s.ID); err != nil {
		t.Fatal(err)
	}
	hctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.Housekeeping(hctx)
	if _, err := a.Store.Get(ctx, s.ID); err != nil {
		t.Fatalf("retention_days 0 must keep all sessions: %v", err)
	}
}

func TestCronChatIDs(t *testing.T) {
	jobs := []cron.Job{
		{Deliver: "telegram:-100:9"},
		{Deliver: "telegram:-200"},
		{Deliver: "", Origin: cron.Origin{Source: "telegram", ChatID: "-300"}},
		{Deliver: "origin", Origin: cron.Origin{Source: "telegram", ChatID: "-400"}},
		{Deliver: "origin", Origin: cron.Origin{Source: "cli"}},
		{Deliver: "local", Origin: cron.Origin{Source: "telegram", ChatID: "-500"}},
	}
	if got := cronChatIDs(jobs); !slices.Equal(got, []string{"-100", "-200", "-300", "-400"}) {
		t.Fatal(got)
	}
}

func TestGateInGatewayOnlyForGatewayRole(t *testing.T) {
	keepCommandTable(t)
	for _, role := range []bool{true, false} {
		dir := t.TempDir()
		writeConfig(t, dir, fakeProviderURL(t))
		a, err := New(ctx, Options{DataDir: dir, Gateway: role})
		if err != nil {
			t.Fatal(err)
		}
		if a.Gate.InGateway != role {
			t.Fatalf("gateway role %v: Gate.InGateway %v", role, a.Gate.InGateway)
		}
		a.Close()
	}
}

func TestGatewayToolsGetInGateway(t *testing.T) {
	keepCommandTable(t)
	for _, role := range []bool{true, false} {
		dir := t.TempDir()
		writeConfig(t, dir, fakeProviderURL(t))
		a, err := New(ctx, Options{DataDir: dir, Gateway: role})
		if err != nil {
			t.Fatal(err)
		}
		term, ok := a.Registry.Get("terminal")
		if !ok || term.(*tools.TerminalTool).InGateway != role {
			t.Fatalf("terminal, gateway role %v", role)
		}
		if code, ok := a.Registry.Get("execute_code"); ok && code.(tools.ExecuteCodeTool).InGateway != role {
			t.Fatalf("execute_code, gateway role %v", role)
		}
		a.Close()
	}
}

// fakeChannel is a "telegram" channel that drops each message.
type fakeChannel struct{}

func (fakeChannel) Name() string                                                 { return "telegram" }
func (fakeChannel) Run(context.Context, *gateway.Gateway) error                  { return nil }
func (fakeChannel) Send(context.Context, gateway.Target, gateway.Outbound) error { return nil }

// userTexts returns the user texts of a session in order.
func userTexts(t *testing.T, a *App, sessionID string) []string {
	t.Helper()
	msgs, err := a.Store.Messages(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range msgs {
		if m.Msg.Role == "user" {
			out = append(out, m.Msg.Content)
		}
	}
	return out
}

// resumeApp returns a gateway-role App with a fake channel, an active session on key, and
// a fresh marker of the key with one queued text.
func resumeApp(t *testing.T, key string) (*App, *sessions.Session) {
	t.Helper()
	a := gatewayApp(t, "", "")
	a.Gateway().Channels["telegram"] = fakeChannel{}
	sess, err := a.Store.ActiveOrCreate(ctx, key, sessions.Session{Source: "telegram", ChatID: "5", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Store.UpsertMarker(ctx, key, sess.ID, []sessions.QueuedText{{Text: "queued one"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	return a, sess
}

func TestHousekeepingResumesBeforeLostBatches(t *testing.T) {
	const key = "telegram:dm:5"
	a, sess := resumeApp(t, key)
	if _, err := a.DB.ExecContext(ctx, `INSERT INTO delegation_batches (id, parent_session_key, parent_session_id, background, status, tasks, created_at)
		VALUES ('bg_1', ?, ?, 1, 'running', '[]', 1)`, key, sess.ID); err != nil {
		t.Fatal(err)
	}
	hctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.Housekeeping(hctx)
	want := []string{agent.ResumeNote, "queued one", agent.FormatBatchLost("bg_1")}
	end := time.Now().Add(5 * time.Second)
	for len(userTexts(t, a, sess.ID)) < len(want) && time.Now().Before(end) {
		time.Sleep(10 * time.Millisecond)
	}
	if !a.Gateway().Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	if got := userTexts(t, a, sess.ID); !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestHousekeepingReadsAliveAtBeforeTheHeartbeat(t *testing.T) {
	const key = "telegram:dm:5"
	a, sess := resumeApp(t, key)
	// The last gateway was alive 2 h ago. The marker time is new, but the cut decides.
	if err := a.DB.KVSet(ctx, "gateway.alive_at", strconv.FormatInt(time.Now().Add(-2*time.Hour).UnixMilli(), 10)); err != nil {
		t.Fatal(err)
	}
	hctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.Housekeeping(hctx)
	if !a.Gateway().Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	if got := userTexts(t, a, sess.ID); len(got) != 0 {
		t.Fatalf("an old cut must not resume: %q", got)
	}
	if ms, err := a.Store.Markers(ctx); err != nil || len(ms) != 0 {
		t.Fatal(ms, err)
	}
}

func TestHeartbeatWritesAliveAt(t *testing.T) {
	old := heartbeatEvery
	heartbeatEvery = 20 * time.Millisecond
	t.Cleanup(func() { heartbeatEvery = old })
	a := newApp(t)
	if _, ok := a.AliveAt(ctx); ok {
		t.Fatal("AliveAt must be false before any write")
	}
	if err := a.DB.KVSet(ctx, "gateway.alive_at", "x"); err != nil {
		t.Fatal(err)
	}
	if at, ok := a.AliveAt(ctx); ok || !at.IsZero() {
		t.Fatal("an unparsable value must give false:", at)
	}
	start := time.Now().Add(-time.Millisecond)
	hctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.Housekeeping(hctx)
	first, ok := a.AliveAt(ctx)
	if !ok || first.Before(start) {
		t.Fatal("Housekeeping must write alive_at:", first, ok)
	}
	end := time.Now().Add(5 * time.Second)
	for {
		if at, _ := a.AliveAt(ctx); at.After(first) {
			break
		}
		if time.Now().After(end) {
			t.Fatal("the heartbeat did not write alive_at again")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ackRenderer keeps the command replies of a turn.
type ackRenderer struct {
	mu   sync.Mutex
	acks []string
}

func (*ackRenderer) Delta(string)             {}
func (*ackRenderer) ToolStart(string, string) {}
func (*ackRenderer) ToolEnd(string, bool)     {}
func (*ackRenderer) Boundary()                {}
func (*ackRenderer) Notice(string)            {}
func (*ackRenderer) Done(string, error)       {}
func (r *ackRenderer) Ack(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.acks = append(r.acks, text)
}

// handleCommand runs text on key through the gateway of a and returns the replies.
func handleCommand(t *testing.T, a *App, key, text string) []string {
	t.Helper()
	r := &ackRenderer{}
	in := gateway.Inbound{Key: key, Source: "telegram", ChatID: "42", ThreadID: "7", Text: text, IsText: true}
	if err := a.Gateway().Handle(ctx, in, func() gateway.Renderer { return r }); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.acks)
}

func TestRestartRequestWritesTargetAndSignals(t *testing.T) {
	a := gatewayApp(t, "", "")
	a.Gateway().Channels["telegram"] = fakeChannel{}
	const key = "telegram:group:42:7"
	if _, err := a.Store.ActiveOrCreate(ctx, key, sessions.Session{Source: "telegram", ChatID: "42", ThreadID: "7", Kind: "chat"}); err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Millisecond)
	if got := handleCommand(t, a, key, "/restart"); !slices.Equal(got, []string{"♻ **Restarting**\nThe gateway restarts now."}) {
		t.Fatalf("%q", got)
	}
	v, ok, err := a.DB.KVGet(ctx, RestartNotifyKey)
	if err != nil || !ok {
		t.Fatal("no restart notify:", ok, err)
	}
	var got struct {
		Source   string `json:"source"`
		ChatID   string `json:"chat_id"`
		ThreadID string `json:"thread_id"`
		At       *int64 `json:"at"`
	}
	if err := json.Unmarshal([]byte(v), &got); err != nil {
		t.Fatal(err, v)
	}
	if got.Source != "telegram" || got.ChatID != "42" || got.ThreadID != "7" || got.At == nil {
		t.Fatal(v)
	}
	if at := time.UnixMilli(*got.At); at.Before(start) || at.After(time.Now()) {
		t.Fatal("at is not now:", v)
	}
	select {
	case req := <-a.Restarts():
		if req != (StopRequest{Reason: "restart", Key: key}) {
			t.Fatalf("%+v", req)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no stop request")
	}
	if a.MarkStopping() {
		t.Fatal("the restart must mark the stop")
	}
	if got := handleCommand(t, a, key, "/restart"); !slices.Equal(got, []string{"⏳ **Restart in progress**\nThe gateway already stops. Wait for it."}) {
		t.Fatalf("%q", got)
	}
	select {
	case req := <-a.Restarts():
		t.Fatalf("a second restart must send nothing: %+v", req)
	default:
	}
}

func TestMarkStoppingFirstCallerOnly(t *testing.T) {
	a := newApp(t)
	if !a.MarkStopping() || a.MarkStopping() {
		t.Fatal("only the first call must return true")
	}
}

func TestNoRestartCommandInChatRole(t *testing.T) {
	// The command table is global. A gateway-role test can leave /restart in it, so a
	// marker command must stay the same.
	prev, had := gateway.Commands()["restart"]
	if had {
		gateway.Register(gateway.Command{Name: "restart", Help: "marker", Run: func(context.Context, *gateway.CommandCtx, string) error { return nil }})
		t.Cleanup(func() { gateway.Register(prev) })
	}
	newApp(t)
	cmd, ok := gateway.Commands()["restart"]
	if had && cmd.Help != "marker" || !had && ok {
		t.Fatalf("the chat role must not register /restart: %+v", cmd)
	}
}

// TestGatewayAppRestoresTheCommandTable checks that a gateway-role test leaves no
// /restart in the global command table.
func TestGatewayAppRestoresTheCommandTable(t *testing.T) {
	_, had := gateway.Commands()["restart"]
	t.Run("gateway role", func(t *testing.T) {
		gatewayApp(t, "", "")
		if _, ok := gateway.Commands()["restart"]; !ok {
			t.Fatal("the gateway role must register /restart")
		}
	})
	if _, ok := gateway.Commands()["restart"]; ok != had {
		t.Fatalf("/restart in the table: %v, before the test: %v", ok, had)
	}
}
