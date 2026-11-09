package config

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// ACPConfig sets the acp_delegate tool, which gives a coding task to an external coding agent
// over the Agent Client Protocol.
type ACPConfig struct {
	Enabled          bool                `yaml:"enabled"`
	IdleTimeoutS     int                 `yaml:"idle_timeout_s"`
	MaxSessions      int                 `yaml:"max_sessions"`
	TurnTimeoutS     int                 `yaml:"turn_timeout_s"`
	WorkdirAllowlist []string            `yaml:"workdir_allowlist"`
	Agents           map[string]ACPAgent `yaml:"agents"`
}

// ACPAgent is one coding agent entry of acp.agents.
type ACPAgent struct {
	Enabled          bool              `yaml:"enabled"`
	Command          []string          `yaml:"command"`
	Env              map[string]string `yaml:"env"`
	PermissionPreset string            `yaml:"permission_preset"`
	Model            string            `yaml:"model"`
	VersionPin       string            `yaml:"version_pin"`
	Modes            map[string]string `yaml:"modes"`
}

// The permission presets of a coding agent.
const (
	ACPPresetAll    = "all"
	ACPPresetRisky  = "risky"
	ACPPresetAuto   = "auto"
	ACPPresetFolder = "folder"
)

// ACPPresets lists the permission presets in the order strict to loose.
var ACPPresets = []string{ACPPresetAll, ACPPresetRisky, ACPPresetAuto, ACPPresetFolder}

// DefaultACPAgent returns the default values of an agent entry. Command, Env, and Modes stay
// empty, because the docs test cannot show a map default that is not empty.
func DefaultACPAgent() ACPAgent {
	return ACPAgent{Enabled: true, PermissionPreset: ACPPresetAuto}
}

// UnmarshalYAML decodes an agent entry over DefaultACPAgent, so that a key that the file does
// not set keeps its default. An unknown key is an error, because a typo would keep the looser
// default preset.
func (a *ACPAgent) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.MappingNode {
		known := acpAgentKeys()
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			if !slices.Contains(known, key.Value) {
				return &unknownKeyError{msg: fmt.Sprintf("acp.agents: the key %q at line %d is not known. Use one of: %s",
					key.Value, key.Line, strings.Join(known, ", "))}
			}
		}
	}
	type plain ACPAgent
	p := plain(DefaultACPAgent())
	if err := n.Decode(&p); err != nil {
		return err
	}
	*a = ACPAgent(p)
	return nil
}

// unknownKeyError tells that the file has a key that the config does not know. Load reports it
// without the "wrong type" text, because the type of the value is not the problem.
type unknownKeyError struct {
	msg string
}

func (e *unknownKeyError) Error() string { return e.msg }

// acpAgentKeys returns the sorted yaml keys of ACPAgent. It reads the struct tags, so a new
// field cannot miss the unknown-key check.
func acpAgentKeys() []string {
	t := reflect.TypeFor[ACPAgent]()
	keys := make([]string, 0, t.NumField())
	for f := range t.Fields() {
		tag, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if tag != "" && tag != "-" {
			keys = append(keys, tag)
		}
	}
	slices.Sort(keys)
	return keys
}

// ACPPresetRank returns the index of preset in ACPPresets, or -1 when the preset is not known.
// A higher rank is a looser preset.
func ACPPresetRank(preset string) int {
	return slices.Index(ACPPresets, preset)
}

// ACPCatalogEntry holds the built-in command and modes of a known coding agent.
type ACPCatalogEntry struct {
	Command []string
	Modes   map[string]string
}

// ACPCatalog returns the built-in coding agents. Each call returns a new copy, so a caller can
// change the result.
func ACPCatalog() map[string]ACPCatalogEntry {
	return map[string]ACPCatalogEntry{
		"claude-code": {
			Command: []string{"claude-agent-acp"},
			Modes: map[string]string{
				ACPPresetAll:    "default",
				ACPPresetRisky:  "acceptEdits",
				ACPPresetAuto:   "auto",
				ACPPresetFolder: "bypassPermissions",
			},
		},
		"codex": {
			Command: []string{"codex-acp"},
			Modes: map[string]string{
				ACPPresetAll:    "read-only",
				ACPPresetRisky:  "workspace-write",
				ACPPresetAuto:   "agent",
				ACPPresetFolder: "agent-full-access",
			},
		},
	}
}

// ModeFor returns the mode id of the agent for preset. ok is false when the agent has no mode,
// or an empty mode, for the preset.
func (a ACPAgent) ModeFor(preset string) (mode string, ok bool) {
	mode = a.Modes[preset]
	return mode, mode != ""
}

// ModeRank returns the rank of the loosest preset that maps to mode. It returns -1 when no
// preset maps to mode, so an unmapped mode such as plan counts as stricter than all.
func (a ACPAgent) ModeRank(mode string) int {
	rank := -1
	if mode == "" {
		return rank
	}
	for preset, m := range a.Modes {
		if m == mode {
			rank = max(rank, ACPPresetRank(preset))
		}
	}
	return rank
}

// EnabledAgents returns the sorted ids of the enabled agents. The tool schema uses this order,
// so the prompt cache sees the same bytes.
func (c ACPConfig) EnabledAgents() []string {
	var ids []string
	for id, a := range c.Agents {
		if a.Enabled {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// IsSecretEnvName reports whether the environment variable name holds a secret. A name is a
// secret if it ends in _KEY, _TOKEN, or _SECRET, or if it contains PASSWORD. The check ignores
// case. config cannot import security, so this is a copy of the name rule of
// security.IsSecretName without the dotenv part. Keep the two rules the same.
func IsSecretEnvName(name string) bool {
	u := strings.ToUpper(name)
	return strings.HasSuffix(u, "_KEY") ||
		strings.HasSuffix(u, "_TOKEN") ||
		strings.HasSuffix(u, "_SECRET") ||
		strings.Contains(u, "PASSWORD")
}

// expandHome replaces "~" and a leading "~/" with the home folder of the user. It returns p
// when the home folder is not known.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// nullACPAgents returns the names of the acp.agents entries that have no value in the file.
// yaml.v3 does not call UnmarshalYAML for a null node, so such an entry decodes to the zero
// ACPAgent. A missing level gives nil.
func nullACPAgents(root *yaml.Node) []string {
	if root == nil || root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil
	}
	agents := mappingValue(mappingValue(root.Content[0], "acp"), "agents")
	if agents == nil || agents.Kind != yaml.MappingNode {
		return nil
	}
	var names []string
	for i := 0; i+1 < len(agents.Content); i += 2 {
		v := agents.Content[i+1]
		if v.Kind == yaml.ScalarNode && v.ShortTag() == "!!null" {
			names = append(names, agents.Content[i].Value)
		}
	}
	return names
}

// mappingValue returns the value node of key in the mapping node n, or nil when n is not a
// mapping or has no such key.
func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// fillACP completes the acp block after the decode. A bare agent entry gets DefaultACPAgent,
// a built-in agent gets the catalog command and the catalog mode of each preset that the file
// does not set, and each "~/" path in the allowlist becomes absolute.
func fillACP(c *ACPConfig, root *yaml.Node) {
	for _, name := range nullACPAgents(root) {
		if c.Agents == nil {
			c.Agents = map[string]ACPAgent{}
		}
		c.Agents[name] = DefaultACPAgent()
	}
	catalog := ACPCatalog()
	for id, a := range c.Agents {
		e, ok := catalog[id]
		if !ok {
			continue
		}
		if len(a.Command) == 0 {
			a.Command = e.Command
		}
		// Merge the modes key by key. A preset that the file does not set gets the catalog mode,
		// else ModeRank cannot rank a loose catalog mode. A key that the file sets wins, also
		// with an empty value.
		modes := e.Modes
		maps.Copy(modes, a.Modes)
		a.Modes = modes
		c.Agents[id] = a
	}
	for i, p := range c.WorkdirAllowlist {
		c.WorkdirAllowlist[i] = expandHome(p)
	}
}

// validate returns one error for each problem in the acp block. It checks every agent entry,
// also a disabled entry and also when acp.enabled is false. The agents go in sorted id order,
// so the error order is stable.
func (c ACPConfig) validate() []error {
	var errs []error
	if c.IdleTimeoutS < 60 {
		errs = append(errs, fmt.Errorf("config: acp.idle_timeout_s is %d. Use 60 or more seconds", c.IdleTimeoutS))
	}
	if c.MaxSessions < 1 {
		errs = append(errs, fmt.Errorf("config: acp.max_sessions is %d. Use 1 or more", c.MaxSessions))
	}
	if c.TurnTimeoutS < 60 {
		errs = append(errs, fmt.Errorf("config: acp.turn_timeout_s is %d. Use 60 or more seconds", c.TurnTimeoutS))
	}
	for i, p := range c.WorkdirAllowlist {
		if !filepath.IsAbs(p) {
			errs = append(errs, fmt.Errorf("config: acp.workdir_allowlist[%d] is %q, which is not an absolute path. Use an absolute path, or a path that starts with ~/", i, p))
		}
	}
	presets := strings.Join(ACPPresets, " | ")
	catalog := ACPCatalog()
	for _, id := range slices.Sorted(maps.Keys(c.Agents)) {
		a := c.Agents[id]
		if err := oneOf("acp.agents."+id+".permission_preset", a.PermissionPreset, ACPPresets...); err != nil {
			errs = append(errs, err)
		}
		for _, key := range slices.Sorted(maps.Keys(a.Modes)) {
			if ACPPresetRank(key) < 0 {
				errs = append(errs, fmt.Errorf("config: acp.agents.%s.modes has the key %q, which is not a preset. Use %s", id, key, presets))
			}
		}
		if _, ok := catalog[id]; !ok {
			if len(a.Command) == 0 {
				errs = append(errs, fmt.Errorf("config: acp.agents.%s.command is empty. Set the adapter program, because %s is not a built-in agent", id, id))
			}
			if len(a.Modes) == 0 {
				errs = append(errs, fmt.Errorf("config: acp.agents.%s.modes is empty. Set the mode id for each preset, because %s is not a built-in agent", id, id))
			}
		}
		for _, name := range slices.Sorted(maps.Keys(a.Env)) {
			if IsSecretEnvName(name) {
				errs = append(errs, fmt.Errorf("config: acp.agents.%s.env has the secret name %s. Remove it. The adapter uses the login of the host user, and a key makes it bill the API", id, name))
			}
		}
	}
	return errs
}
