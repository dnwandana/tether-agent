package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTemp writes body to a config file in a new temp folder and returns its path.
func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// mapEnv returns an env lookup function over m.
func mapEnv(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

const minimal = "providers:\n  primary: {format: openai, base_url: u, api_key: k, model: m}\n"

func TestLoadOverlaysDefaultsAndExpands(t *testing.T) {
	p := writeTemp(t, "providers:\n  primary:\n    format: openai\n    base_url: https://x/v1\n    api_key: ${ZAI_API_KEY}\n    model: glm-4.7\ncontext:\n  protect_last_n: 30\n")
	c, err := Load(p, mapEnv(map[string]string{"ZAI_API_KEY": "k1"}))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, c.Providers["primary"].APIKey, "k1")
	eq(t, c.Context.ProtectLastN, 30)
	eq(t, c.Context.ProtectFirstN, 3) // default kept
}

func TestUnsetVarNamesVarAndKey(t *testing.T) {
	p := writeTemp(t, "providers:\n  primary:\n    format: openai\n    base_url: u\n    api_key: ${MISSING}\n    model: m\n")
	_, err := Load(p, mapEnv(nil))
	if err == nil || !strings.Contains(err.Error(), "MISSING") || !strings.Contains(err.Error(), "providers.primary.api_key") {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), ".env") {
		t.Fatalf("error does not give the next action: %v", err)
	}
}

func TestPrimaryRequired(t *testing.T) {
	_, err := Load(writeTemp(t, "log:\n  level: debug\n"), mapEnv(nil))
	if err == nil || !strings.Contains(err.Error(), "providers.primary") {
		t.Fatal(err)
	}
}

func TestBadFormatRejected(t *testing.T) {
	p := writeTemp(t, "providers:\n  primary: {format: grpc, base_url: u, api_key: k, model: m}\n")
	_, err := Load(p, mapEnv(nil))
	if err == nil || !strings.Contains(err.Error(), "openai | anthropic") {
		t.Fatal(err)
	}
}

func TestMissingFileIsDefaultsButStillNeedsPrimary(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "none.yaml"), mapEnv(nil))
	if err == nil || !strings.Contains(err.Error(), "providers.primary") {
		t.Fatal(err)
	}
}

func TestExpandedValueIsNotExpandedAgain(t *testing.T) {
	p := writeTemp(t, minimal+"telegram:\n  home_chat: ${A}\n  home_thread: pre-${B}-post\n")
	c, err := Load(p, mapEnv(map[string]string{"A": "${B}", "B": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, c.Telegram.HomeChat, "${B}")
	eq(t, c.Telegram.HomeThread, "pre-x-post")
}

func TestUnsetVarInListNamesIndex(t *testing.T) {
	p := writeTemp(t, minimal+"terminal:\n  env_passthrough:\n    - A\n    - ${NOPE}\n")
	_, err := Load(p, mapEnv(nil))
	if err == nil || !strings.Contains(err.Error(), "terminal.env_passthrough[1]") {
		t.Fatal(err)
	}
}

func TestMCPServerKeepsDefaults(t *testing.T) {
	p := writeTemp(t, minimal+"mcp:\n  servers:\n    gh:\n      command: [npx, gh-mcp]\n      timeout_s: 30\n")
	c, err := Load(p, mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	s := c.MCP.Servers["gh"]
	eq(t, s.TimeoutS, 30)
	eq(t, s.ConnectTimeoutS, 60)
	eq(t, s.IdleStopMinutes, 10)
	eq(t, s.Enabled, true)
	eq(t, s.Command[1], "gh-mcp")
}

func TestToolsetsMergeWithDefaults(t *testing.T) {
	p := writeTemp(t, minimal+"toolsets:\n  cron: [file, web]\n")
	c, err := Load(p, mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, len(c.Toolsets["cron"]), 2)
	eq(t, c.Toolsets["cli"][0], "all")
}

func TestNumericIDsDecodeAsStrings(t *testing.T) {
	p := writeTemp(t, minimal+"telegram:\n  allowed_users: [12345, \"67\"]\n  group_allowed_chats: [-1001]\n")
	c, err := Load(p, mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, c.Telegram.AllowedUsers[0], "12345")
	eq(t, c.Telegram.AllowedUsers[1], "67")
	eq(t, c.Telegram.GroupAllowedChats[0], "-1001")
}

func TestBotTokenFromEnvWhenFileDoesNotSetIt(t *testing.T) {
	c, err := Load(writeTemp(t, minimal), mapEnv(map[string]string{"TELEGRAM_BOT_TOKEN": "123:abc"}))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, c.Telegram.BotToken, "123:abc")
	c, err = Load(writeTemp(t, minimal), mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, c.Telegram.BotToken, "")
}

func TestEmptyFileIsDefaults(t *testing.T) {
	_, err := Load(writeTemp(t, ""), mapEnv(nil))
	if err == nil || !strings.Contains(err.Error(), "providers.primary") {
		t.Fatal(err)
	}
}

func TestBadYAMLNamesFile(t *testing.T) {
	p := writeTemp(t, "providers: [\n")
	_, err := Load(p, mapEnv(nil))
	if err == nil || !strings.Contains(err.Error(), p) {
		t.Fatal(err)
	}
}

func TestValidateEnums(t *testing.T) {
	cases := []struct {
		mut  func(*Config)
		want string
	}{
		{func(c *Config) { c.Approvals.Mode = "x" }, "approvals.mode"},
		{func(c *Config) { c.Approvals.CronMode = "x" }, "approvals.cron_mode"},
		{func(c *Config) { c.Cron.MissedRuns = "x" }, "cron.missed_runs"},
		{func(c *Config) { c.Telegram.Streaming.Transport = "x" }, "telegram.streaming.transport"},
		{func(c *Config) { c.Telegram.ToolProgress = "x" }, "telegram.tool_progress"},
		{func(c *Config) { c.Providers["fallback"] = Provider{Format: "x"} }, "providers.fallback.format"},
	}
	for _, tc := range cases {
		c := Default()
		c.Providers["primary"] = Provider{Format: "anthropic"}
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		tc.mut(&c)
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: got %v", tc.want, err)
		}
	}
}

func TestValidateListsAllowedValues(t *testing.T) {
	c := Default()
	c.Providers["primary"] = Provider{Format: "openai"}
	c.Approvals.Mode = "auto"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "manual | smart | off") || !strings.Contains(err.Error(), `"auto"`) {
		t.Fatal(err)
	}
}

func TestPresetResolves(t *testing.T) {
	path := writeTemp(t, "providers:\n  primary:\n    preset: zai-coding\n    model: glm-5.3\n")
	c, err := Load(path, mapEnv(map[string]string{"ZAI_API_KEY": "k1"}))
	if err != nil {
		t.Fatal(err)
	}
	p := c.Providers["primary"]
	eq(t, p.Format, "openai")
	eq(t, p.BaseURL, "https://api.z.ai/api/coding/paas/v4")
	eq(t, p.APIKey, "k1")
	eq(t, strings.Join(QuirksOf(p), ","), "reasoning_content")
	eq(t, EffortStyleOf(p), "glm-think")
}

func TestPresetValueInEntryWins(t *testing.T) {
	path := writeTemp(t, "providers:\n  primary:\n    preset: openai\n    model: m\n    api_key: own\n")
	c, _ := Load(path, mapEnv(map[string]string{"OPENAI_API_KEY": "env"}))
	eq(t, c.Providers["primary"].APIKey, "own")
}

func TestPresetTrailingSlashIsPresetURL(t *testing.T) {
	path := writeTemp(t, "providers:\n  primary:\n    preset: deepseek\n    base_url: https://api.deepseek.com/v1/\n    model: m\n")
	c, err := Load(path, mapEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, c.Providers["primary"].APIKey, "k")
	eq(t, len(QuirksOf(c.Providers["primary"])), 1)
}

func TestPresetOtherHostGetsNoKeyAndNoQuirks(t *testing.T) {
	path := writeTemp(t, "providers:\n  primary:\n    preset: openai\n    model: m\n  proxy:\n    preset: openai\n    base_url: https://my-proxy/v1\n    model: m\n")
	c, err := Load(path, mapEnv(map[string]string{"OPENAI_API_KEY": "secret"}))
	if err != nil {
		t.Fatal(err)
	}
	p := c.Providers["proxy"]
	eq(t, p.APIKey, "")
	eq(t, len(QuirksOf(p)), 0)
	eq(t, EffortStyleOf(p), "responses")
	if !strings.Contains(p.Unusable, "providers.proxy.api_key") {
		t.Fatal(p.Unusable)
	}
}

func TestUnknownPresetListsIDs(t *testing.T) {
	path := writeTemp(t, "providers:\n  primary:\n    preset: nope\n    model: m\n")
	_, err := Load(path, mapEnv(nil))
	want := `config: providers.primary.preset "nope" is not known. Use one of: alibaba, anthropic, deepseek, minimax, moonshot, ollama-cloud, openai, openrouter, xai, zai, zai-coding`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatal(err)
	}
}

func TestRemovedOllamaPresetNamesTheNextStep(t *testing.T) {
	path := writeTemp(t, "providers:\n  local:\n    preset: ollama\n    model: m\n")
	_, err := Load(path, mapEnv(nil))
	want := `config: providers.local.preset "ollama" is removed. For a local server, remove the preset line and ` +
		`set format: openai and base_url: http://localhost:11434/v1. For Ollama Cloud, use preset: ollama-cloud`
	if err == nil || err.Error() != want {
		t.Fatal(err)
	}
}

func TestPrimaryNeedsKeyAndModel(t *testing.T) {
	_, err := Load(writeTemp(t, "providers:\n  primary:\n    preset: zai\n    model: m\n"), mapEnv(map[string]string{"ZAI_API_KEY": ""}))
	if err == nil || !strings.Contains(err.Error(), "config: providers.primary needs an API key. Set ZAI_API_KEY in ~/.tether/.env, or set providers.primary.api_key") {
		t.Fatal(err)
	}
	_, err = Load(writeTemp(t, "providers:\n  primary:\n    format: openai\n    base_url: http://x\n"), mapEnv(nil))
	if err == nil || !strings.Contains(err.Error(), "config: providers.primary.model is empty. Set the model name") {
		t.Fatal(err)
	}
}

func TestOtherEntryWithNoKeyIsUnusable(t *testing.T) {
	path := writeTemp(t, "providers:\n  primary:\n    format: openai\n    base_url: http://localhost:11434/v1\n    model: m\n  deepseek:\n    preset: deepseek\n    model: m\n")
	c, err := Load(path, mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, c.Providers["primary"].Unusable, "")
	eq(t, c.Providers["deepseek"].Unusable, "config: providers.deepseek needs an API key. Set DEEPSEEK_API_KEY in ~/.tether/.env, or set providers.deepseek.api_key")
}

func TestOtherEntryWithNoModelIsUnusable(t *testing.T) {
	path := writeTemp(t, "providers:\n  primary:\n    format: openai\n    base_url: http://localhost:11434/v1\n    model: m\n  spare:\n    format: openai\n    base_url: http://x\n")
	c, err := Load(path, mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, c.Providers["spare"].Unusable, "config: providers.spare.model is empty. Set the model name")
}

func TestResolvePresetSetsUnusableCause(t *testing.T) {
	path := writeTemp(t, "providers:\n  primary:\n    format: openai\n    base_url: http://localhost:11434/v1\n    model: m\n"+
		"  deepseek:\n    preset: deepseek\n    model: m\n"+
		"  proxy:\n    preset: openai\n    base_url: https://my-proxy/v1\n    model: m\n"+
		"  spare:\n    format: openai\n    base_url: http://x\n")
	c, err := Load(path, mapEnv(map[string]string{"OPENAI_API_KEY": "secret"}))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, UnusableNoKey, "no API key")
	eq(t, UnusableNoModel, "no model in config.yaml")
	eq(t, c.Providers["primary"].UnusableCause, "")
	eq(t, c.Providers["deepseek"].UnusableCause, UnusableNoKey)
	eq(t, c.Providers["proxy"].UnusableCause, UnusableNoKey)
	eq(t, c.Providers["spare"].UnusableCause, UnusableNoModel)
	// The Unusable text does not change.
	eq(t, c.Providers["deepseek"].Unusable, "config: providers.deepseek needs an API key. Set DEEPSEEK_API_KEY in ~/.tether/.env, or set providers.deepseek.api_key")
	eq(t, c.Providers["spare"].Unusable, "config: providers.spare.model is empty. Set the model name")
	// ResolvePreset clears an old cause of a usable entry.
	p, err := ResolvePreset("box", Provider{Format: "openai", BaseURL: "http://x", Model: "m", Unusable: "old", UnusableCause: UnusableNoModel}, nil)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, p.Unusable, "")
	eq(t, p.UnusableCause, "")
}

func TestEntryWithNoPresetLoadsUnchanged(t *testing.T) {
	path := writeTemp(t, "providers:\n  primary:\n    format: anthropic\n    base_url: https://api.anthropic.com\n    api_key: k\n    model: m\n")
	c, err := Load(path, mapEnv(map[string]string{"ANTHROPIC_API_KEY": "env"}))
	if err != nil {
		t.Fatal(err)
	}
	p := c.Providers["primary"]
	eq(t, p.APIKey, "k")
	eq(t, p.Unusable, "")
	eq(t, len(QuirksOf(p)), 0)
	eq(t, EffortStyleOf(p), "anthropic-std")
}

func TestFormatAndEffortValidation(t *testing.T) {
	ok := writeTemp(t, "providers:\n  primary:\n    format: responses\n    base_url: http://x\n    model: m\n    effort: xhigh\n")
	if _, err := Load(ok, mapEnv(nil)); err != nil {
		t.Fatal(err)
	}
	bad := writeTemp(t, "providers:\n  primary:\n    format: openai\n    base_url: http://x\n    model: m\n    effort: turbo\n")
	if _, err := Load(bad, mapEnv(nil)); err == nil || !strings.Contains(err.Error(), "providers.primary.effort") {
		t.Fatal(err)
	}
}

func TestFallbackListShapes(t *testing.T) {
	base := "providers:\n  primary:\n    format: openai\n    base_url: http://x\n    model: m\n" +
		"  fallback:\n    format: openai\n    base_url: http://y\n    model: m2\n" +
		"  deepseek:\n    format: openai\n    base_url: http://z\n    model: m3\n"
	cases := []struct{ extra, want string }{
		{"", "fallback"},
		{"agent:\n  fallback: [deepseek]\n", "deepseek"},
		{"agent:\n  fallback: []\n", ""},
	}
	for _, c := range cases {
		cfg, err := Load(writeTemp(t, base+c.extra), mapEnv(nil))
		if err != nil {
			t.Fatal(err)
		}
		eq(t, strings.Join(cfg.FallbackList(), ","), c.want)
	}
}

func TestFallbackListEmptyWithNoFallbackEntry(t *testing.T) {
	cfg, err := Load(writeTemp(t, minimal), mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FallbackList() != nil {
		t.Fatal(cfg.FallbackList())
	}
}

func TestFallbackListReturnsACopy(t *testing.T) {
	c := Default()
	c.Agent.Fallback = []string{"a"}
	c.FallbackList()[0] = "b"
	eq(t, c.Agent.Fallback[0], "a")
}

func TestOldConfigWithFlatRateLoads(t *testing.T) {
	old := "providers:\n  primary:\n    format: openai\n    base_url: http://x\n    api_key: k\n    model: m\n    flat_rate: true\n" +
		"  fallback:\n    format: anthropic\n    base_url: http://y\n    api_key: k2\n    model: m2\n"
	cfg, err := Load(writeTemp(t, old), mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, strings.Join(cfg.FallbackList(), ","), "fallback")
	eq(t, cfg.Providers["fallback"].Format, "anthropic")
}

func TestFallbackListValidation(t *testing.T) {
	base := "providers:\n  primary:\n    format: openai\n    base_url: http://x\n    model: m\n  a:\n    format: openai\n    base_url: http://y\n    model: m\n"
	cases := map[string]string{
		"agent:\n  fallback: [nope]\n":    `config: agent.fallback names "nope", which is not in providers`,
		"agent:\n  fallback: [primary]\n": "config: agent.fallback must not contain primary",
		"agent:\n  fallback: [a, a]\n":    `config: agent.fallback names "a" two times`,
	}
	for extra, want := range cases {
		_, err := Load(writeTemp(t, base+extra), mapEnv(nil))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", extra, err)
		}
	}
}

func TestMCPServerNameValidation(t *testing.T) {
	c := Default()
	c.Providers["primary"] = Provider{Format: "openai"}
	for _, name := range []string{"a b", "a_b", "GitHub", "github", "search", "a-b"} {
		c.MCP.Servers[name] = DefaultMCPServer()
	}
	err := c.Validate()
	for _, want := range []string{
		`config: the MCP server names "a b" and "a_b" are the same`,
		`config: the MCP server names "GitHub" and "github" are the same`,
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("want %q, got %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "search") || strings.Contains(err.Error(), `"a-b"`) {
		t.Fatal("a name with no clash is in the error:", err)
	}
	delete(c.MCP.Servers, "a b")
	delete(c.MCP.Servers, "GitHub")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMediaCacheRetentionDays(t *testing.T) {
	c, err := Load(writeTemp(t, minimal), mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, c.Media.CacheRetentionDays, 7)
	c, err = Load(writeTemp(t, minimal+"media:\n  cache_retention_days: 0\n"), mapEnv(nil))
	if err != nil {
		t.Fatalf("0 keeps all files and is valid: %v", err)
	}
	eq(t, c.Media.CacheRetentionDays, 0)
	eq(t, c.Media.TTS.Backend, "command") // the other media defaults stay
	_, err = Load(writeTemp(t, minimal+"media:\n  cache_retention_days: -1\n"), mapEnv(nil))
	if err == nil || !strings.Contains(err.Error(), "media.cache_retention_days") {
		t.Fatalf("a negative value must name the key, got %v", err)
	}
}
