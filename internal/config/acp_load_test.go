package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A bare entry is a null node, and yaml.v3 does not call UnmarshalYAML for it.
func TestLoadACPBareEntryGetsDefaultsAndCatalog(t *testing.T) {
	c, err := Load(writeTemp(t, minimal+"acp:\n  agents:\n    claude-code:\n    codex: ~\n"), mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	cc := c.ACP.Agents["claude-code"]
	eq(t, cc.Enabled, true)
	eq(t, cc.PermissionPreset, "auto")
	if !reflect.DeepEqual(cc.Command, []string{"claude-agent-acp"}) {
		t.Fatal(cc.Command)
	}
	if !reflect.DeepEqual(cc.Modes, map[string]string{"all": "default", "risky": "acceptEdits", "auto": "auto", "folder": "bypassPermissions"}) {
		t.Fatal(cc.Modes)
	}
	cx := c.ACP.Agents["codex"]
	eq(t, cx.Enabled, true)
	if !reflect.DeepEqual(cx.Command, []string{"codex-acp"}) || cx.Modes["auto"] != "agent" {
		t.Fatalf("%+v", cx)
	}
}

func TestLoadACPFileValueWins(t *testing.T) {
	src := minimal + "acp:\n  agents:\n    claude-code:\n      command: [/opt/node/bin/claude-agent-acp]\n" +
		"      modes: {auto: acceptEdits}\n      enabled: false\n"
	c, err := Load(writeTemp(t, src), mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	cc := c.ACP.Agents["claude-code"]
	eq(t, cc.Enabled, false)
	if !reflect.DeepEqual(cc.Command, []string{"/opt/node/bin/claude-agent-acp"}) {
		t.Fatal(cc.Command)
	}
	if !reflect.DeepEqual(cc.Modes, map[string]string{"all": "default", "risky": "acceptEdits", "auto": "acceptEdits", "folder": "bypassPermissions"}) {
		t.Fatal(cc.Modes)
	}
}

// A modes block that sets only some presets keeps the catalog modes of the other presets. Else
// a loose catalog mode has no preset, and ModeRank gives it the rank of a strict mode.
func TestLoadACPPartialModesKeepCatalog(t *testing.T) {
	src := minimal + "acp:\n  agents:\n    claude-code:\n      modes: {all: plan, folder: \"\"}\n"
	c, err := Load(writeTemp(t, src), mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	cc := c.ACP.Agents["claude-code"]
	want := map[string]string{"all": "plan", "risky": "acceptEdits", "auto": "auto", "folder": ""}
	if !reflect.DeepEqual(cc.Modes, want) {
		t.Fatalf("got %v, want %v", cc.Modes, want)
	}
	eq(t, cc.ModeRank("auto"), ACPPresetRank(ACPPresetAuto))
	if _, ok := cc.ModeFor(ACPPresetFolder); ok {
		t.Fatal("an empty mode in the file must stay empty")
	}
	// The catalog must not change through the merged map.
	if ACPCatalog()["claude-code"].Modes["all"] != "default" {
		t.Fatal("the catalog changed")
	}

	src = minimal + "acp:\n  agents:\n    codex:\n      modes: {auto: agent}\n"
	c, err = Load(writeTemp(t, src), mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, c.ACP.Agents["codex"].ModeRank("agent-full-access"), ACPPresetRank(ACPPresetFolder))
}

func TestLoadACPExpandsHomeInAllowlist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := minimal + "acp:\n  workdir_allowlist: [\"~/src\", /srv/code, \"${PROJ}/x\"]\n"
	c, err := Load(writeTemp(t, src), mapEnv(map[string]string{"PROJ": "/data"}))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(home, "src"), "/srv/code", "/data/x"}
	if !reflect.DeepEqual(c.ACP.WorkdirAllowlist, want) {
		t.Fatalf("got %v, want %v", c.ACP.WorkdirAllowlist, want)
	}
}

// An unknown key is not a value of the wrong type, so the error does not say so.
func TestLoadACPUnknownKeyFails(t *testing.T) {
	path := writeTemp(t, minimal+"acp:\n  agents:\n    claude-code:\n      permision_preset: all\n")
	_, err := Load(path, mapEnv(nil))
	if err == nil {
		t.Fatal("want an error")
	}
	want := "config: " + path + `: acp.agents: the key "permision_preset" at line 6 is not known. Use one of: ` +
		strings.Join(acpAgentKeys(), ", ") + ". Correct the file and try again"
	if err.Error() != want {
		t.Fatalf("got  %q\nwant %q", err, want)
	}
}

// A modes key that is not a preset still fails after the merge with the catalog modes.
func TestLoadACPUnknownModeKeyFails(t *testing.T) {
	_, err := Load(writeTemp(t, minimal+"acp:\n  agents:\n    claude-code:\n      modes: {fast: x}\n"), mapEnv(nil))
	if err == nil || !strings.Contains(err.Error(), `acp.agents.claude-code.modes has the key "fast", which is not a preset`) {
		t.Fatal(err)
	}
}

func TestValidateACP(t *testing.T) {
	cases := []struct {
		mut  func(*ACPConfig)
		want string
	}{
		{func(c *ACPConfig) { c.IdleTimeoutS = 59 }, "config: acp.idle_timeout_s is 59. Use 60 or more seconds"},
		{func(c *ACPConfig) { c.MaxSessions = 0 }, "config: acp.max_sessions is 0. Use 1 or more"},
		{func(c *ACPConfig) { c.TurnTimeoutS = 10 }, "config: acp.turn_timeout_s is 10. Use 60 or more seconds"},
		{func(c *ACPConfig) { c.WorkdirAllowlist = []string{"/srv", "src"} },
			`config: acp.workdir_allowlist[1] is "src", which is not an absolute path. Use an absolute path, or a path that starts with ~/`},
		{func(c *ACPConfig) { a := DefaultACPAgent(); a.PermissionPreset = "yolo"; c.Agents["claude-code"] = a },
			`config: acp.agents.claude-code.permission_preset is "yolo", which is not a known value. Use all | risky | auto | folder`},
		{func(c *ACPConfig) {
			a := DefaultACPAgent()
			a.Modes = map[string]string{"fast": "x"}
			c.Agents["claude-code"] = a
		},
			`config: acp.agents.claude-code.modes has the key "fast", which is not a preset. Use all | risky | auto | folder`},
		{func(c *ACPConfig) { c.Agents["gemini"] = DefaultACPAgent() },
			"config: acp.agents.gemini.command is empty. Set the adapter program, because gemini is not a built-in agent"},
		{func(c *ACPConfig) { c.Agents["gemini"] = DefaultACPAgent() },
			"config: acp.agents.gemini.modes is empty. Set the mode id for each preset, because gemini is not a built-in agent"},
		{func(c *ACPConfig) {
			a := DefaultACPAgent()
			a.Env = map[string]string{"ANTHROPIC_API_KEY": "${ANTHROPIC_API_KEY}"}
			c.Agents["claude-code"] = a
		}, "config: acp.agents.claude-code.env has the secret name ANTHROPIC_API_KEY. Remove it. The adapter uses the login of the host user, and a key makes it bill the API"},
	}
	for _, tc := range cases {
		c := Default()
		c.Providers["primary"] = Provider{Format: "openai"}
		tc.mut(&c.ACP)
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("want %q, got %v", tc.want, err)
		}
	}
	// A valid block: a catalog agent with no command, a custom agent with both keys, and a
	// non-secret env name.
	c := Default()
	c.Providers["primary"] = Provider{Format: "openai"}
	c.ACP.Enabled = true
	c.ACP.WorkdirAllowlist = []string{"/srv/code"}
	cc := DefaultACPAgent()
	cc.Env = map[string]string{"CLAUDE_CONFIG_DIR": "/home/u/.claude-work"}
	c.ACP.Agents["claude-code"] = cc
	g := DefaultACPAgent()
	g.Command, g.Modes = []string{"gemini-acp"}, map[string]string{"auto": "auto_edit"}
	c.ACP.Agents["gemini"] = g
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestIsSecretEnvName(t *testing.T) {
	for name, want := range map[string]bool{
		"ANTHROPIC_API_KEY": true, "CLAUDE_CODE_OAUTH_TOKEN": true, "gh_token": true, "MY_SECRET": true,
		"DB_PASSWORD_FILE": true, "password": true,
		"CLAUDE_CONFIG_DIR": false, "PATH": false, "KEYRING": false, "CODEX_HOME": false, "TOKENIZER": false,
	} {
		if got := IsSecretEnvName(name); got != want {
			t.Errorf("IsSecretEnvName(%q) = %v, want %v", name, got, want)
		}
	}
}
