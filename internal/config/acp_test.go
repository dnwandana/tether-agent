package config

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestACPDefaults(t *testing.T) {
	c := Default().ACP
	eq(t, c.Enabled, false)
	eq(t, c.IdleTimeoutS, 300)
	eq(t, c.MaxSessions, 2)
	eq(t, c.TurnTimeoutS, 1800)
	if c.WorkdirAllowlist == nil || len(c.WorkdirAllowlist) != 0 {
		t.Fatalf("workdir_allowlist = %#v, want an empty list", c.WorkdirAllowlist)
	}
	if c.Agents == nil || len(c.Agents) != 0 {
		t.Fatalf("agents = %#v, want an empty map", c.Agents)
	}
	a := DefaultACPAgent()
	eq(t, a.Enabled, true)
	eq(t, a.PermissionPreset, "auto")
	eq(t, a.Model, "")
	eq(t, a.VersionPin, "")
	if len(a.Command) != 0 || len(a.Env) != 0 || len(a.Modes) != 0 {
		t.Fatalf("the default agent must have no command, env, or modes: %+v", a)
	}
}

func TestACPAgentDecodesOverDefaults(t *testing.T) {
	var c ACPConfig
	src := "agents:\n  claude-code:\n    model: opus\n  codex:\n    enabled: false\n    permission_preset: all\n"
	if err := yaml.Unmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	cc := c.Agents["claude-code"]
	eq(t, cc.Enabled, true)
	eq(t, cc.PermissionPreset, "auto")
	eq(t, cc.Model, "opus")
	cx := c.Agents["codex"]
	eq(t, cx.Enabled, false)
	eq(t, cx.PermissionPreset, "all")
}

// A typo in an agent entry must fail, because it would silently keep the looser default preset.
func TestACPAgentUnknownKey(t *testing.T) {
	var c ACPConfig
	err := yaml.Unmarshal([]byte("agents:\n  claude-code:\n    permision_preset: all\n"), &c)
	want := `acp.agents: the key "permision_preset" at line 3 is not known. Use one of: command, enabled, env, model, modes, permission_preset, version_pin`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestACPPresets(t *testing.T) {
	if !reflect.DeepEqual(ACPPresets, []string{"all", "risky", "auto", "folder"}) {
		t.Fatal(ACPPresets)
	}
	eq(t, ACPPresetAll, "all")
	eq(t, ACPPresetRisky, "risky")
	eq(t, ACPPresetAuto, "auto")
	eq(t, ACPPresetFolder, "folder")
	for p, want := range map[string]int{"all": 0, "risky": 1, "auto": 2, "folder": 3, "": -1, "yolo": -1, "Auto": -1} {
		if got := ACPPresetRank(p); got != want {
			t.Errorf("ACPPresetRank(%q) = %d, want %d", p, got, want)
		}
	}
}

func TestACPCatalog(t *testing.T) {
	want := map[string]ACPCatalogEntry{
		"claude-code": {Command: []string{"claude-agent-acp"},
			Modes: map[string]string{"all": "default", "risky": "acceptEdits", "auto": "auto", "folder": "bypassPermissions"}},
		"codex": {Command: []string{"codex-acp"},
			Modes: map[string]string{"all": "read-only", "risky": "workspace-write", "auto": "agent", "folder": "agent-full-access"}},
	}
	cat := ACPCatalog()
	if !reflect.DeepEqual(cat, want) {
		t.Fatalf("got %#v", cat)
	}
	cat["claude-code"].Command[0] = "x"
	cat["claude-code"].Modes["auto"] = "x"
	delete(cat, "codex")
	if !reflect.DeepEqual(ACPCatalog(), want) {
		t.Fatal("ACPCatalog must return a fresh copy")
	}
}

func TestACPAgentModes(t *testing.T) {
	cc := ACPAgent{Modes: ACPCatalog()["claude-code"].Modes}
	for preset, want := range map[string]string{"all": "default", "risky": "acceptEdits", "auto": "auto", "folder": "bypassPermissions"} {
		if got, ok := cc.ModeFor(preset); !ok || got != want {
			t.Errorf("ModeFor(%q) = %q, %v, want %q", preset, got, ok, want)
		}
	}
	if got, ok := cc.ModeFor("yolo"); ok || got != "" {
		t.Errorf("ModeFor(yolo) = %q, %v", got, ok)
	}
	if _, ok := (ACPAgent{}).ModeFor("auto"); ok {
		t.Error("an agent with no modes has no mode")
	}
	if _, ok := (ACPAgent{Modes: map[string]string{"auto": ""}}).ModeFor("auto"); ok {
		t.Error("an empty mode id is not a mode")
	}
	for mode, want := range map[string]int{"default": 0, "acceptEdits": 1, "auto": 2, "bypassPermissions": 3, "plan": -1, "": -1} {
		if got := cc.ModeRank(mode); got != want {
			t.Errorf("claude-code ModeRank(%q) = %d, want %d", mode, got, want)
		}
	}
	cx := ACPAgent{Modes: ACPCatalog()["codex"].Modes}
	for mode, want := range map[string]int{"read-only": 0, "workspace-write": 1, "agent": 2, "agent-full-access": 3, "plan": -1} {
		if got := cx.ModeRank(mode); got != want {
			t.Errorf("codex ModeRank(%q) = %d, want %d", mode, got, want)
		}
	}
	// Two presets map to one mode: the mode gets the rank of the looser preset.
	two := ACPAgent{Modes: map[string]string{"all": "ask", "risky": "ask", "auto": "free"}}
	eq(t, two.ModeRank("ask"), 1)
}

func TestACPEnabledAgents(t *testing.T) {
	c := ACPConfig{Agents: map[string]ACPAgent{
		"codex": {Enabled: true}, "claude-code": {Enabled: true}, "old": {Enabled: false},
	}}
	if got := c.EnabledAgents(); !reflect.DeepEqual(got, []string{"claude-code", "codex"}) {
		t.Fatal(got)
	}
	if got := (ACPConfig{}).EnabledAgents(); len(got) != 0 {
		t.Fatal(got)
	}
}
