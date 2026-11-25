package config

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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

func TestApprovalsLogRetentionDays(t *testing.T) {
	c, err := Load(writeTemp(t, minimal), mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, c.Approvals.LogRetentionDays, 180)
	eq(t, Default().Approvals.LogRetentionDays, 180)
	c, err = Load(writeTemp(t, minimal+"approvals:\n  log_retention_days: 0\n"), mapEnv(nil))
	if err != nil {
		t.Fatalf("0 keeps all rows and is valid: %v", err)
	}
	eq(t, c.Approvals.LogRetentionDays, 0)
	eq(t, c.Approvals.Mode, "manual") // the other approvals defaults stay
	_, err = Load(writeTemp(t, minimal+"approvals:\n  log_retention_days: -1\n"), mapEnv(nil))
	if err == nil || !strings.Contains(err.Error(), "config: approvals.log_retention_days is -1, which is negative. "+
		"Use 0 to keep all approval log rows, or a number of days") {
		t.Fatalf("a negative value must name the key, got %v", err)
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

// TestCronHistoryKeysLoad checks that a file without the keys keeps the defaults, and that an
// explicit 0 stays 0.
func TestCronHistoryKeysLoad(t *testing.T) {
	c, err := Load(writeTemp(t, minimal), mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, c.Cron.RunHistory, 50)
	eq(t, c.Cron.FailStreakLimit, 10)
	c, err = Load(writeTemp(t, minimal+"cron:\n  run_history: 0\n  fail_streak_limit: 0\n"), mapEnv(nil))
	if err != nil {
		t.Fatalf("0 turns each feature off and is valid: %v", err)
	}
	eq(t, c.Cron.RunHistory, 0)
	eq(t, c.Cron.FailStreakLimit, 0)
	eq(t, c.Cron.MaxParallelJobs, 2) // the other cron defaults stay
}

// validBase returns the defaults with a primary entry, so that Validate checks only the
// changed key.
func validBase() Config {
	c := Default()
	c.Providers["primary"] = Provider{Format: "openai"}
	return c
}

// intKey is one new int key, its range, and a setter.
type intKey struct {
	key string
	min int
	max int
	set func(c *Config, v int)
}

const maxS = 2592000

func newIntKeys() []intKey {
	i32 := math.MaxInt32
	imax := math.MaxInt
	return []intKey{
		{"cron.script_timeout_s", 1, maxS, func(c *Config, v int) { c.Cron.ScriptTimeoutS = v }},
		{"cron.agent_timeout_s", 0, maxS, func(c *Config, v int) { c.Cron.AgentTimeoutS = v }},
		{"cron.script_output_max_chars", 1000, i32, func(c *Config, v int) { c.Cron.ScriptOutputMaxChars = v }},
		{"cron.script_stderr_tail_lines", 1, i32, func(c *Config, v int) { c.Cron.ScriptStderrTailLines = v }},
		{"cron.tick_s", 1, maxS, func(c *Config, v int) { c.Cron.TickS = v }},
		{"cron.grace_min_s", 1, maxS, func(c *Config, v int) { c.Cron.GraceMinS = v; c.Cron.GraceMaxS = maxS }},
		{"cron.grace_max_s", 1, maxS, func(c *Config, v int) { c.Cron.GraceMaxS = v; c.Cron.GraceMinS = 1 }},
		{"cron.run_history", 0, 1000, func(c *Config, v int) { c.Cron.RunHistory = v }},
		{"cron.fail_streak_limit", 0, 10000, func(c *Config, v int) { c.Cron.FailStreakLimit = v }},
		{"agent.stream_idle_timeout_s", 1, maxS, func(c *Config, v int) { c.Agent.StreamIdleTimeoutS = v }},
		{"agent.max_same_tool_failures", 1, i32, func(c *Config, v int) { c.Agent.MaxSameToolFailures = v }},
		{"agent.tool_preview_chars", 4, i32, func(c *Config, v int) { c.Agent.ToolPreviewChars = v }},
		{"review.max_rounds", 1, i32, func(c *Config, v int) { c.Review.MaxRounds = v }},
		{"review.max_concurrent", 1, i32, func(c *Config, v int) { c.Review.MaxConcurrent = v }},
		{"review.min_user_turns", 1, i32, func(c *Config, v int) { c.Review.MinUserTurns = v }},
		{"provider.failure_skip_s", 1, maxS, func(c *Config, v int) { c.Provider.FailureSkipS = v }},
		{"provider.anthropic_max_tokens", 1, i32, func(c *Config, v int) { c.Provider.AnthropicMaxTokens = v }},
		{"provider.model_cache_ttl_s", 1, maxS, func(c *Config, v int) { c.Provider.ModelCacheTTLS = v }},
		{"provider.model_list_timeout_s", 1, maxS, func(c *Config, v int) { c.Provider.ModelListTimeoutS = v }},
		{"provider.default_context_window", 1000, i32, func(c *Config, v int) { c.Provider.DefaultContextWindow = v }},
		{"context.summary_timeout_s", 1, maxS, func(c *Config, v int) { c.Context.SummaryTimeoutS = v }},
		{"context.summary_min_tokens", 100, i32, func(c *Config, v int) { c.Context.SummaryMinTokens = v; c.Context.SummaryMaxTokens = i32 }},
		{"context.summary_max_tokens", 100, i32, func(c *Config, v int) { c.Context.SummaryMaxTokens = v; c.Context.SummaryMinTokens = 100 }},
		{"context.compact_max_failures", 1, i32, func(c *Config, v int) { c.Context.CompactMaxFailures = v }},
		{"context.compact_max_ineffective", 1, i32, func(c *Config, v int) { c.Context.CompactMaxIneffective = v }},
		{"goals.judge_max_tokens", 256, i32, func(c *Config, v int) { c.Goals.JudgeMaxTokens = v }},
		{"goals.judge_max_parse_failures", 1, i32, func(c *Config, v int) { c.Goals.JudgeMaxParseFailures = v }},
		{"goals.judge_max_transport_failures", 1, i32, func(c *Config, v int) { c.Goals.JudgeMaxTransportFailures = v }},
		{"goals.gate_timeout_s", 1, maxS, func(c *Config, v int) { c.Goals.GateTimeoutS = v }},
		{"goals.gate_max_retries", 1, i32, func(c *Config, v int) { c.Goals.GateMaxRetries = v }},
		{"goals.gate_output_tail_chars", 100, i32, func(c *Config, v int) { c.Goals.GateOutputTailChars = v }},
		{"goals.max_barrier_wait_s", 1, maxS, func(c *Config, v int) { c.Goals.MaxBarrierWaitS = v }},
		{"goals.draft_objective_chars", 100, i32, func(c *Config, v int) { c.Goals.DraftObjectiveChars = v }},
		{"goals.judge_chars.goal", 1, i32, func(c *Config, v int) { c.Goals.JudgeChars.Goal = v }},
		{"goals.judge_chars.response", 1, i32, func(c *Config, v int) { c.Goals.JudgeChars.Response = v }},
		{"goals.judge_chars.contract", 1, i32, func(c *Config, v int) { c.Goals.JudgeChars.Contract = v }},
		{"goals.judge_chars.subgoal", 1, i32, func(c *Config, v int) { c.Goals.JudgeChars.Subgoal = v }},
		{"goals.judge_chars.todo", 1, i32, func(c *Config, v int) { c.Goals.JudgeChars.Todo = v }},
		{"goals.judge_chars.process", 1, i32, func(c *Config, v int) { c.Goals.JudgeChars.Process = v }},
		{"memory.entry_max_chars", 100, i32, func(c *Config, v int) { c.Memory.EntryMaxChars = v }},
		{"memory.lock_wait_s", 1, maxS, func(c *Config, v int) { c.Memory.LockWaitS = v }},
		{"memory.read_max_chars", 1000, i32, func(c *Config, v int) { c.Memory.ReadMaxChars = v }},
		{"memory.search_max_hits", 1, i32, func(c *Config, v int) { c.Memory.SearchMaxHits = v }},
		{"memory.prompt_max_topics", 1, i32, func(c *Config, v int) { c.Memory.PromptMaxTopics = v }},
		{"skills.git_timeout_s", 1, maxS, func(c *Config, v int) { c.Skills.GitTimeoutS = v }},
		{"skills.skill_md_max_chars", 1000, i32, func(c *Config, v int) { c.Skills.SkillMDMaxChars = v }},
		{"skills.support_file_max_bytes", 1024, imax, func(c *Config, v int) { c.Skills.SupportFileMaxBytes = v }},
		{"skills.view_max_linked_files", 1, i32, func(c *Config, v int) { c.Skills.ViewMaxLinkedFiles = v }},
		{"web.http_timeout_s", 1, maxS, func(c *Config, v int) { c.Web.HTTPTimeoutS = v }},
		{"web.search_default_results", 1, i32, func(c *Config, v int) { c.Web.SearchDefaultResults = v; c.Web.SearchMaxResults = i32 }},
		{"web.search_max_results", 1, i32, func(c *Config, v int) { c.Web.SearchMaxResults = v; c.Web.SearchDefaultResults = 1 }},
		{"browser.action_timeout_s", 1, maxS, func(c *Config, v int) { c.Browser.ActionTimeoutS = v; c.Browser.SettleS = 0.1 }},
		{"browser.navigate_excerpt_chars", 100, i32, func(c *Config, v int) { c.Browser.NavigateExcerptChars = v }},
		{"files.read_max_lines", 1, i32, func(c *Config, v int) { c.Files.ReadMaxLines = v }},
		{"files.read_max_line_chars", 100, i32, func(c *Config, v int) { c.Files.ReadMaxLineChars = v }},
		{"files.patch_context_lines", 1, i32, func(c *Config, v int) { c.Files.PatchContextLines = v }},
		{"todo.max_items", 1, i32, func(c *Config, v int) { c.Todo.MaxItems = v }},
		{"todo.max_item_chars", 100, i32, func(c *Config, v int) { c.Todo.MaxItemChars = v }},
		{"terminal.process_keep_s", 1, maxS, func(c *Config, v int) { c.Terminal.ProcessKeepS = v }},
		{"code_execution.max_stderr_chars", 100, i32, func(c *Config, v int) { c.CodeExecution.MaxStderrChars = v }},
		{"acp.result_max_chars", 1000, i32, func(c *Config, v int) { c.ACP.ResultMaxChars = v }},
		{"approvals.smart_timeout_s", 1, maxS, func(c *Config, v int) { c.Approvals.SmartTimeoutS = v }},
		{"approvals.smart_script_max_chars", 100, i32, func(c *Config, v int) { c.Approvals.SmartScriptMaxChars = v }},
		{"mcp.servers.gh.max_dial_tries", 1, 10, func(c *Config, v int) {
			s := DefaultMCPServer()
			s.MaxDialTries = v
			c.MCP.Servers = map[string]MCPServer{"gh": s}
		}},
		{"sessions.search_default_results", 1, i32, func(c *Config, v int) { c.Sessions.SearchDefaultResults = v; c.Sessions.SearchMaxResults = i32 }},
		{"sessions.search_max_results", 1, i32, func(c *Config, v int) { c.Sessions.SearchMaxResults = v; c.Sessions.SearchDefaultResults = 1 }},
		{"sessions.scroll_default_window", 1, i32, func(c *Config, v int) { c.Sessions.ScrollDefaultWindow = v; c.Sessions.ScrollMaxWindow = i32 }},
		{"sessions.scroll_max_window", 1, i32, func(c *Config, v int) { c.Sessions.ScrollMaxWindow = v; c.Sessions.ScrollDefaultWindow = 1 }},
		{"media.vision_max_bytes", 1024, imax, func(c *Config, v int) { c.Media.VisionMaxBytes = v }},
		{"media.image_download_timeout_s", 1, maxS, func(c *Config, v int) { c.Media.ImageDownloadTimeoutS = v }},
		{"media.tts.timeout_s", 0, maxS, func(c *Config, v int) { c.Media.TTS.TimeoutS = v }},
		{"media.stt.timeout_s", 0, maxS, func(c *Config, v int) { c.Media.STT.TimeoutS = v }},
		{"media.image.timeout_s", 0, maxS, func(c *Config, v int) { c.Media.Image.TimeoutS = v }},
		{"telegram.api_timeout_s", 1, maxS, func(c *Config, v int) { c.Telegram.APITimeoutS = v }},
		{"telegram.upload_timeout_s", 1, maxS, func(c *Config, v int) { c.Telegram.UploadTimeoutS = v }},
		{"telegram.rich_media_timeout_s", 1, maxS, func(c *Config, v int) { c.Telegram.RichMediaTimeoutS = v }},
		{"telegram.inline_text_max_bytes", 1024, imax, func(c *Config, v int) { c.Telegram.InlineTextMaxBytes = v }},
		{"telegram.sticker_describe_timeout_s", 1, maxS, func(c *Config, v int) { c.Telegram.StickerDescribeTimeoutS = v }},
		{"telegram.voice_convert_timeout_s", 1, maxS, func(c *Config, v int) { c.Telegram.VoiceConvertTimeoutS = v }},
		{"telegram.menu_max_commands", 22, 100, func(c *Config, v int) { c.Telegram.MenuMaxCommands = v }},
		{"telegram.pairing_code_ttl_s", 1, maxS, func(c *Config, v int) { c.Telegram.PairingCodeTTLS = v }},
		{"gateway.max_queued_messages", 1, i32, func(c *Config, v int) { c.Gateway.MaxQueuedMessages = v }},
		{"gateway.model_page_size", 1, i32, func(c *Config, v int) { c.Gateway.ModelPageSize = v }},
		{"gateway.session_page_size", 1, i32, func(c *Config, v int) { c.Gateway.SessionPageSize = v }},
		{"gateway.help_max_skills", 1, i32, func(c *Config, v int) { c.Gateway.HelpMaxSkills = v }},
		{"gateway.housekeeping_interval_s", 60, maxS, func(c *Config, v int) { c.Gateway.HousekeepingIntervalS = v }},
		{"gateway.media_trust_recent_s", 0, maxS, func(c *Config, v int) { c.Gateway.MediaTrustRecentS = v }},
		{"update.download_timeout_s", 1, maxS, func(c *Config, v int) { c.Update.DownloadTimeoutS = v }},
		{"update.api_timeout_s", 1, maxS, func(c *Config, v int) { c.Update.APITimeoutS = v }},
		{"doctor.check_timeout_s", 1, maxS, func(c *Config, v int) { c.Doctor.CheckTimeoutS = v }},
		{"doctor.acp_check_timeout_s", 1, maxS, func(c *Config, v int) { c.Doctor.ACPCheckTimeoutS = v }},
		{"doctor.min_free_bytes", 1, imax, func(c *Config, v int) { c.Doctor.MinFreeBytes = v }},
	}
}

// TestValidateNewIntKeys checks the lowest and the highest permitted value of each new int
// key, and one value below and one value above them. The error names the key.
func TestValidateNewIntKeys(t *testing.T) {
	for _, k := range newIntKeys() {
		for _, v := range []int{k.min, k.max} {
			c := validBase()
			k.set(&c, v)
			if err := c.Validate(); err != nil {
				t.Errorf("%s = %d must be valid: %v", k.key, v, err)
			}
		}
		for _, v := range []int{k.min - 1, k.max + 1} {
			if v < k.min-1 { // k.max + 1 wraps when k.max is math.MaxInt
				continue
			}
			c := validBase()
			k.set(&c, v)
			want := fmt.Sprintf("config: %s is %d. Use a value from %d to %d", k.key, v, k.min, k.max)
			if err := c.Validate(); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s = %d: got %v, want %q", k.key, v, err, want)
			}
		}
	}
}

func TestValidateNewFloatKeys(t *testing.T) {
	keys := map[string]func(c *Config, v float64){
		"browser.settle_s":           func(c *Config, v float64) { c.Browser.SettleS = v; c.Browser.ActionTimeoutS = maxS },
		"telegram.album_wait_s":      func(c *Config, v float64) { c.Telegram.AlbumWaitS = v },
		"telegram.status_interval_s": func(c *Config, v float64) { c.Telegram.StatusIntervalS = v },
		"gateway.debounce_s":         func(c *Config, v float64) { c.Gateway.DebounceS = v; c.Gateway.DebounceMaxS = maxS },
		"gateway.debounce_max_s":     func(c *Config, v float64) { c.Gateway.DebounceMaxS = v; c.Gateway.DebounceS = 0.001 },
	}
	for key, set := range keys {
		for _, v := range []float64{0.001, 2592000} {
			if key == "browser.settle_s" && v == 2592000 {
				continue // The settle pair needs a larger action timeout, and the max refuses it.
			}
			c := validBase()
			set(&c, v)
			if err := c.Validate(); err != nil {
				t.Errorf("%s = %g must be valid: %v", key, v, err)
			}
		}
		for _, v := range []float64{0, -1, 2592001, math.NaN(), math.Inf(1)} {
			c := validBase()
			set(&c, v)
			want := fmt.Sprintf("config: %s is %g. Use a number above 0 and up to 2592000", key, v)
			if err := c.Validate(); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s = %g: got %v, want %q", key, v, err, want)
			}
		}
	}
}

func TestValidateOrderedPairs(t *testing.T) {
	cases := []struct {
		mut  func(*Config)
		want string
	}{
		{func(c *Config) { c.Cron.GraceMinS, c.Cron.GraceMaxS = 300, 200 },
			"config: cron.grace_min_s is 300, which is more than cron.grace_max_s (200). Make cron.grace_min_s smaller or cron.grace_max_s larger"},
		{func(c *Config) { c.Context.SummaryMinTokens, c.Context.SummaryMaxTokens = 5000, 4000 },
			"config: context.summary_min_tokens is 5000, which is more than context.summary_max_tokens (4000). Make context.summary_min_tokens smaller or context.summary_max_tokens larger"},
		{func(c *Config) { c.Gateway.DebounceS, c.Gateway.DebounceMaxS = 2, 1.5 },
			"config: gateway.debounce_s is 2, which is more than gateway.debounce_max_s (1.5). Make gateway.debounce_s smaller or gateway.debounce_max_s larger"},
		{func(c *Config) { c.Web.SearchDefaultResults, c.Web.SearchMaxResults = 12, 10 },
			"config: web.search_default_results is 12, which is more than web.search_max_results (10). Make web.search_default_results smaller or web.search_max_results larger"},
		{func(c *Config) { c.Sessions.SearchDefaultResults, c.Sessions.SearchMaxResults = 30, 20 },
			"config: sessions.search_default_results is 30, which is more than sessions.search_max_results (20). Make sessions.search_default_results smaller or sessions.search_max_results larger"},
		{func(c *Config) { c.Sessions.ScrollDefaultWindow, c.Sessions.ScrollMaxWindow = 60, 50 },
			"config: sessions.scroll_default_window is 60, which is more than sessions.scroll_max_window (50). Make sessions.scroll_default_window smaller or sessions.scroll_max_window larger"},
		{func(c *Config) { c.Browser.SettleS, c.Browser.ActionTimeoutS = 30, 30 },
			"config: browser.settle_s is 30, which is not less than browser.action_timeout_s (30). Make browser.settle_s smaller"},
	}
	for _, tc := range cases {
		c := validBase()
		tc.mut(&c)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("got %v, want %q", err, tc.want)
		}
	}
	// Equal values are in order, except for the settle pair.
	c := validBase()
	c.Cron.GraceMinS, c.Cron.GraceMaxS = 600, 600
	c.Gateway.DebounceS, c.Gateway.DebounceMaxS = 1, 1
	c.Browser.SettleS, c.Browser.ActionTimeoutS = 29.9, 30
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateImageSizeAndAllowDirs(t *testing.T) {
	for _, ok := range []string{"1024x1024", "1x1", "1792x1024"} {
		c := validBase()
		c.Media.ImageSize = ok
		if err := c.Validate(); err != nil {
			t.Errorf("%q must be valid: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "1024", "0x10", "10x0", "-1x5", "axb", "1024x1024x1", "1024 x 1024"} {
		c := validBase()
		c.Media.ImageSize = bad
		want := fmt.Sprintf("config: media.image_size is %q. Use the form <W>x<H>, for example 1024x1024", bad)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v", bad, err)
		}
	}
	c := validBase()
	c.Gateway.MediaAllowDirs = []string{"/srv/out", "~/exports"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Gateway.MediaAllowDirs = []string{"/srv/out", "exports"}
	want := `config: gateway.media_allow_dirs has "exports", which is not an absolute path. Use a path that starts with / or ~/`
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatal(err)
	}
}

func TestLoadRefusesNewKeyOutOfRange(t *testing.T) {
	_, err := Load(writeTemp(t, minimal+"cron:\n  script_timeout_s: 0\n  agent_timeout_s: -1\n"), mapEnv(nil))
	if err == nil || !strings.Contains(err.Error(), "config: cron.script_timeout_s is 0. Use a value from 1 to 2592000") ||
		!strings.Contains(err.Error(), "config: cron.agent_timeout_s is -1. Use a value from 0 to 2592000") {
		t.Fatal(err)
	}
	c, err := Load(writeTemp(t, minimal+"cron:\n  script_timeout_s: 900\n  agent_timeout_s: 0\n"), mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, c.Cron.ScriptTimeoutS, 900)
}

// A config that sets only one channel keeps the default member list.
func TestToolsetsKeepDefaultMember(t *testing.T) {
	c, err := Load(writeTemp(t, minimal+"toolsets:\n  telegram: [file]\n"), mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, strings.Join(c.Toolsets["telegram"], ","), "file")
	eq(t, strings.Join(c.Toolsets["member"], ","), "web,ask,todo")
}

// An empty member list stays empty. It gives a member no tools.
func TestToolsetsEmptyMember(t *testing.T) {
	c, err := Load(writeTemp(t, minimal+"toolsets:\n  member: []\n"), mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, len(c.Toolsets["member"]), 0)
}

func TestApprovalsHumanOnlyForm(t *testing.T) {
	c, err := Load(writeTemp(t, minimal), mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Approvals.HumanOnly == nil || len(c.Approvals.HumanOnly) != 0 {
		t.Fatalf("the default must be an empty list: %#v", c.Approvals.HumanOnly)
	}
	c, err = Load(writeTemp(t, minimal+
		"approvals:\n  human_only: [git_force_push, \"mcp:stripe.create_refund\", \"mcp:my.server.tool-x\"]\n"), mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, strings.Join(c.Approvals.HumanOnly, ","), "git_force_push,mcp:stripe.create_refund,mcp:my.server.tool-x")
	for _, bad := range []string{`"Git Force"`, `""`, `"mcp:"`, `git-force-push`, `"rm -rf"`} {
		_, err := Load(writeTemp(t, minimal+"approvals:\n  human_only: ["+bad+"]\n"), mapEnv(nil))
		if err == nil || !strings.Contains(err.Error(), "approvals.human_only") {
			t.Fatalf("%s: got %v", bad, err)
		}
	}
}

// mcpTools is the start of a config file with one MCP server gh and a tools block. Line 7 is
// "      tools:".
const mcpTools = minimal + "mcp:\n  servers:\n    gh:\n      command: [gh-mcp]\n      tools:\n"

func TestLoadMCPTools(t *testing.T) {
	path := writeTemp(t, mcpTools+
		"        include: [\"search_*\", get_issue]\n"+
		"        exclude: [\"*_delete\"]\n"+
		"        class: {create_issue: send, list_pulls: read}\n")
	c, err := Load(path, mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	gh := c.MCP.Servers["gh"]
	want := MCPTools{
		Include: []string{"search_*", "get_issue"},
		Exclude: []string{"*_delete"},
		Class:   map[string]string{"create_issue": "send", "list_pulls": "read"},
	}
	if !reflect.DeepEqual(gh.Tools, want) {
		t.Fatalf("%+v", gh.Tools)
	}
	if gh.TimeoutS != 120 || !gh.Enabled {
		t.Fatalf("the other keys keep their defaults: %+v", gh)
	}

	// A server with no tools block keeps the empty defaults.
	c, err = Load(writeTemp(t, minimal+"mcp:\n  servers:\n    gh:\n      command: [gh-mcp]\n"), mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if tl := c.MCP.Servers["gh"].Tools; len(tl.Include) != 0 || len(tl.Exclude) != 0 || tl.Class == nil || len(tl.Class) != 0 {
		t.Fatalf("%+v", tl)
	}
}

func TestLoadMCPToolsUnknownKeyFails(t *testing.T) {
	path := writeTemp(t, mcpTools+"        exlude: [x]\n")
	_, err := Load(path, mapEnv(nil))
	want := "config: " + path + `: mcp.servers: the key "exlude" under tools at line 8 is not known. Use one of: class, exclude, include. Correct the file and try again`
	if err == nil || err.Error() != want {
		t.Fatalf("got  %v\nwant %q", err, want)
	}
}

func TestValidateMCPTools(t *testing.T) {
	cases := []struct{ block, want string }{
		{"        include: [\"a/b\"]\n",
			`config: mcp.servers.gh.tools.include has the pattern "a/b", which has a "/". Use a raw MCP tool name or a pattern with "*"`},
		{"        exclude: [\"[x\"]\n",
			`config: mcp.servers.gh.tools.exclude has the pattern "[x", which is not a valid pattern: syntax error in pattern`},
		{"        class: {create_issue: admin}\n",
			`config: mcp.servers.gh.tools.class.create_issue is "admin", which is not a known value. Use read | write | send`},
		{"        class: {create_refund: human-only}\n",
			`config: mcp.servers.gh.tools.class.create_refund is "human-only", which is not a known value. Use read | write | send`},
	}
	for _, c := range cases {
		_, err := Load(writeTemp(t, mcpTools+c.block), mapEnv(nil))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("block %q:\ngot  %v\nwant %q", c.block, err, c.want)
		}
	}
	// A plain name and a pattern with "*" are valid.
	if _, err := Load(writeTemp(t, mcpTools+"        include: [get_issue, \"list_*\"]\n"), mapEnv(nil)); err != nil {
		t.Fatal(err)
	}
}

func TestParseClock(t *testing.T) {
	for in, want := range map[string]int{"00:00": 0, "07:30": 450, "08:00": 480, "23:59": 1439} {
		got, err := ParseClock(in)
		if err != nil || got != want {
			t.Errorf("ParseClock(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "7:30", "24:00", "12:60", "12:5", " 08:00", "08:00 ", "ab:cd",
		"08-00", "+8:00", "0800", "08:00:00"} {
		if _, err := ParseClock(bad); err == nil || !strings.Contains(err.Error(), "HH:MM") {
			t.Errorf("ParseClock(%q) must fail with a hint of HH:MM, got %v", bad, err)
		}
	}
}

func TestParseWindow(t *testing.T) {
	for in, want := range map[string]Window{
		"22:00-07:00": {Start: 1320, End: 420},
		"09:00-17:30": {Start: 540, End: 1050},
		"00:00-23:59": {Start: 0, End: 1439},
	} {
		got, err := ParseWindow(in)
		if err != nil || got != want {
			t.Errorf("ParseWindow(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "22:00", "22:00-", "-07:00", "22:00-7:00", "22:00 - 07:00",
		"22:00-07:00-08:00", "25:00-07:00"} {
		if _, err := ParseWindow(bad); err == nil {
			t.Errorf("ParseWindow(%q) must fail", bad)
		}
	}
	if _, err := ParseWindow("22:00-22:00"); err == nil || !strings.Contains(err.Error(), "the same") {
		t.Errorf("a start equal to the end must fail, got %v", err)
	}
}

func TestWindowContains(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 11, 19, h, m, 0, 0, time.UTC) }
	night := Window{Start: 22 * 60, End: 7 * 60} // 22:00-07:00 crosses midnight
	for _, c := range []struct {
		t    time.Time
		want bool
	}{
		{at(21, 59), false}, {at(22, 0), true}, {at(23, 30), true}, {at(0, 0), true},
		{at(6, 59), true}, {at(7, 0), false}, {at(12, 0), false},
	} {
		if got := night.Contains(c.t); got != c.want {
			t.Errorf("22:00-07:00 at %s: got %v, want %v", c.t.Format("15:04"), got, c.want)
		}
	}
	day := Window{Start: 9 * 60, End: 17*60 + 30} // 09:00-17:30
	for _, c := range []struct {
		t    time.Time
		want bool
	}{
		{at(8, 59), false}, {at(9, 0), true}, {at(17, 29), true}, {at(17, 30), false}, {at(23, 0), false},
	} {
		if got := day.Contains(c.t); got != c.want {
			t.Errorf("09:00-17:30 at %s: got %v, want %v", c.t.Format("15:04"), got, c.want)
		}
	}
	// Contains reads the clock in the location of t. 23:00 at UTC+7 is 16:00 UTC.
	plus7 := time.Date(2026, 11, 19, 23, 0, 0, 0, time.FixedZone("UTC+7", 7*3600))
	if !night.Contains(plus7) || night.Contains(plus7.UTC()) {
		t.Fatal("Contains must use the clock of the location of t")
	}
}

func TestValidateCronClockKeys(t *testing.T) {
	c := validBase()
	eq(t, c.Cron.QuietHours, "")
	eq(t, c.Cron.BriefAt, "")
	if err := c.Validate(); err != nil {
		t.Fatalf("the empty defaults turn the features off and are valid: %v", err)
	}
	c.Cron.QuietHours, c.Cron.BriefAt = "22:00-07:00", "08:00"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mut  func(*Config)
		want string
	}{
		{func(c *Config) { c.Cron.QuietHours = "22:00" }, "cron.quiet_hours"},
		{func(c *Config) { c.Cron.QuietHours = "22:00-22:00" }, "cron.quiet_hours"},
		{func(c *Config) { c.Cron.QuietHours = "10pm-7am" }, "cron.quiet_hours"},
		{func(c *Config) { c.Cron.BriefAt = "8:00" }, "cron.brief_at"},
		{func(c *Config) { c.Cron.BriefAt = "24:00" }, "cron.brief_at"},
	} {
		c := validBase()
		tc.mut(&c)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v", tc.want, err)
		}
	}
	got, err := Load(writeTemp(t, minimal+"cron:\n  quiet_hours: \"22:00-07:00\"\n  brief_at: \"08:00\"\n"), mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, got.Cron.QuietHours, "22:00-07:00")
	eq(t, got.Cron.BriefAt, "08:00")
}

// A window with the same start and end contains no time.
func TestWindowEmptyContainsNothing(t *testing.T) {
	for _, w := range []Window{{}, {Start: 600, End: 600}} {
		for _, h := range []int{0, 10, 23} {
			if w.Contains(time.Date(2026, 11, 19, h, 0, 0, 0, time.UTC)) {
				t.Errorf("%+v contains %02d:00", w, h)
			}
		}
	}
}

// TestValidateCompactAtTokens checks the range of context.compact_at_tokens: 0 turns the
// cap off, and another value must be from 50000 to 2147483647.
func TestValidateCompactAtTokens(t *testing.T) {
	for _, v := range []int{0, 50000, math.MaxInt32} {
		c := validBase()
		c.Context.CompactAtTokens = v
		if err := c.Validate(); err != nil {
			t.Errorf("context.compact_at_tokens = %d must be valid: %v", v, err)
		}
	}
	for _, v := range []int{1, 49999, -1} {
		c := validBase()
		c.Context.CompactAtTokens = v
		want := fmt.Sprintf("config: context.compact_at_tokens is %d. Use 0 to turn off the cap, or a value from 50000 to 2147483647.", v)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("context.compact_at_tokens = %d: got %v, want %q", v, err, want)
		}
	}
}
