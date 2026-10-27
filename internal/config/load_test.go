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
