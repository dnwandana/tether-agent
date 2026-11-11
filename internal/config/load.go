package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// varRef matches one ${NAME} reference in a string value.
var varRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Load reads the config file at path and returns the validated config.
//
// Load starts from Default, expands each ${VAR} in the string values of the
// file through env, and decodes the file over the defaults. A missing file
// is valid. After the decode, Load fills the acp block: each bare agent
// entry, the catalog command and modes, and "~/" in acp.workdir_allowlist.
// Then Load resolves the preset of each provider entry with
// ResolvePreset. An entry other than primary that cannot serve a call loads
// with its Unusable message set. If env is nil, Load uses os.LookupEnv.
func Load(path string, env func(string) (string, bool)) (*Config, error) {
	if env == nil {
		env = os.LookupEnv
	}
	c := Default()

	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("config: cannot read %s: %w", path, err)
	}
	var root yaml.Node
	if len(bytes.TrimSpace(data)) > 0 {
		if err := yaml.Unmarshal(data, &root); err != nil {
			return nil, fmt.Errorf("config: %s is not valid YAML: %w. Correct the file and try again", path, err)
		}
		if err := expand(&root, "", env); err != nil {
			return nil, err
		}
		if err := root.Decode(&c); err != nil {
			if uk, ok := errors.AsType[*unknownKeyError](err); ok {
				return nil, fmt.Errorf("config: %s: %w. Correct the file and try again", path, uk)
			}
			return nil, fmt.Errorf("config: %s has a value of the wrong type: %w. Correct the file and try again", path, err)
		}
	}
	// An empty file leaves root as the zero node, which has no bare agent entries.
	fillACP(&c.ACP, &root)

	// An empty telegram.bot_token falls back to $TELEGRAM_BOT_TOKEN. An
	// unset variable is not an error here, because Telegram is optional.
	if c.Telegram.BotToken == "" {
		if v, ok := env("TELEGRAM_BOT_TOKEN"); ok {
			c.Telegram.BotToken = v
		}
	}

	var errs []error
	for _, name := range sortedKeys(c.Providers) {
		p, err := ResolvePreset(name, c.Providers[name], env)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		c.Providers[name] = p
	}
	// An unknown or a removed preset leaves the entry with no format. Stop here, so that the
	// error does not also contain a format error for the same entry.
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if p, ok := c.Providers["primary"]; ok && p.Unusable != "" {
		errs = append(errs, errors.New(p.Unusable))
	}

	if err := c.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &c, nil
}

// ResolvePreset returns p with the values of its preset filled in. The value
// that p sets wins. The preset key env applies only when the base URL of p is
// the URL of the preset, so a preset key never goes to another host.
//
// ResolvePreset returns an error only for an unknown or a removed preset id. It sets
// Unusable and UnusableCause when the entry has no API key or no model. env is the
// lookup for the key env, and an env var with an empty value counts as not set.
func ResolvePreset(name string, p Provider, env func(string) (string, bool)) (Provider, error) {
	p.Unusable = ""
	p.UnusableCause = ""
	if p.Preset != "" {
		if next, removed := removedPresets[p.Preset]; removed {
			return p, fmt.Errorf("config: providers.%s.preset %q is removed. %s", name, p.Preset, next)
		}
		pre, ok := PresetByID(p.Preset)
		if !ok {
			return p, fmt.Errorf("config: providers.%s.preset %q is not known. Use one of: %s",
				name, p.Preset, strings.Join(PresetIDs(), ", "))
		}
		if p.Format == "" {
			p.Format = pre.Format
		}
		if p.BaseURL == "" {
			p.BaseURL = pre.BaseURL
		}
		if p.APIKey == "" {
			onPreset := sameURL(p.BaseURL, pre.BaseURL)
			if onPreset && env != nil {
				if v, ok := env(pre.KeyEnv); ok && v != "" {
					p.APIKey = v
				}
			}
			switch {
			case p.APIKey != "":
			case onPreset:
				p.Unusable = fmt.Sprintf("config: providers.%s needs an API key. Set %s in ~/.tether/.env, or set providers.%s.api_key",
					name, pre.KeyEnv, name)
				p.UnusableCause = UnusableNoKey
			default:
				p.Unusable = fmt.Sprintf("config: providers.%s needs an API key, because its base_url is not the URL of the preset. Set providers.%s.api_key",
					name, name)
				p.UnusableCause = UnusableNoKey
			}
		}
	}
	if p.Unusable == "" && p.Model == "" {
		p.Unusable = fmt.Sprintf("config: providers.%s.model is empty. Set the model name", name)
		p.UnusableCause = UnusableNoModel
	}
	return p, nil
}

// presetOnURL returns the preset of p when p uses the URL of that preset. An
// empty base URL counts as the preset URL, because the resolution fills it.
func presetOnURL(p Provider) (Preset, bool) {
	pre, ok := PresetByID(p.Preset)
	if !ok || (p.BaseURL != "" && !sameURL(p.BaseURL, pre.BaseURL)) {
		return Preset{}, false
	}
	return pre, true
}

// QuirksOf returns the quirk flags of the preset of p. It returns nil when p
// has no preset or when p moves the preset to another host.
func QuirksOf(p Provider) []string {
	if pre, ok := presetOnURL(p); ok {
		return pre.Quirks
	}
	return nil
}

// EffortStyleOf returns the effort style of the preset of p, under the same
// rule as QuirksOf. Else it returns the standard style of the format of p:
// chat, responses, or anthropic-std.
func EffortStyleOf(p Provider) string {
	if pre, ok := presetOnURL(p); ok {
		return pre.EffortStyle
	}
	switch p.Format {
	case "openai":
		return "chat"
	case "responses":
		return "responses"
	case "anthropic":
		return "anthropic-std"
	}
	return ""
}

// sameURL compares two base URLs without a trailing slash.
func sameURL(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}

// sortedKeys returns the keys of m in sorted order.
func sortedKeys(m map[string]Provider) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// expand replaces each ${NAME} in the string scalars under n. It changes
// each scalar one time only, so an expanded value that contains "${" stays
// as it is. key is the dotted key path of n, for the error message.
func expand(n *yaml.Node, key string, env func(string) (string, bool)) error {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, ch := range n.Content {
			if err := expand(ch, key, env); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if key != "" {
				k = key + "." + k
			}
			if err := expand(n.Content[i+1], k, env); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for i, ch := range n.Content {
			if err := expand(ch, key+"["+strconv.Itoa(i)+"]", env); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		if n.ShortTag() != "!!str" || !strings.Contains(n.Value, "${") {
			return nil
		}
		var missing string
		n.Value = varRef.ReplaceAllStringFunc(n.Value, func(ref string) string {
			name := ref[2 : len(ref)-1]
			v, ok := env(name)
			if !ok && missing == "" {
				missing = name
			}
			return v
		})
		if missing != "" {
			return fmt.Errorf("config: ${%s} is not set (key %s). Set it in ~/.tether/.env or the environment", missing, key)
		}
	}
	// An alias node points to an anchored node that the walk already
	// expanded. Do not walk it again.
	return nil
}

// Validate checks the required keys and the allowed values of the enum keys.
// It returns all the problems that it finds, joined into one error.
func (c *Config) Validate() error {
	var errs []error
	if _, ok := c.Providers["primary"]; !ok {
		errs = append(errs, errors.New(`config: providers.primary is missing. Add a primary provider to config.yaml, or run "tether setup"`))
	}
	for _, name := range sortedKeys(c.Providers) {
		p := c.Providers[name]
		errs = append(errs, oneOf("providers."+name+".format", p.Format, "openai", "anthropic", "responses"))
		if p.Effort != "" && !slices.Contains(EffortLevels, p.Effort) {
			errs = append(errs, fmt.Errorf(`config: providers.%s.effort is %q, which is not a known value. Use "" or %s`,
				name, p.Effort, strings.Join(EffortLevels, " | ")))
		}
	}
	seen := map[string]bool{}
	for _, name := range c.Agent.Fallback {
		switch {
		case name == "primary":
			errs = append(errs, errors.New("config: agent.fallback must not contain primary"))
		case seen[name]:
			errs = append(errs, fmt.Errorf("config: agent.fallback names %q two times", name))
		default:
			if _, ok := c.Providers[name]; !ok {
				errs = append(errs, fmt.Errorf("config: agent.fallback names %q, which is not in providers", name))
			}
		}
		seen[name] = true
	}
	if c.Sessions.RetentionDays < 0 {
		errs = append(errs, fmt.Errorf("config: sessions.retention_days is %d, which is negative. Use 0 to keep all sessions, or a number of days", c.Sessions.RetentionDays))
	}
	if c.Media.CacheRetentionDays < 0 {
		errs = append(errs, fmt.Errorf("config: media.cache_retention_days is %d, which is negative. Use 0 to keep all media files, or a number of days", c.Media.CacheRetentionDays))
	}
	if c.Agent.RestartDrainTimeoutS < 0 {
		errs = append(errs, fmt.Errorf("config: agent.restart_drain_timeout_s is %d, which is negative. Use 0 to cancel the running turns at once, or a number of seconds", c.Agent.RestartDrainTimeoutS))
	}
	if c.Agent.ResumeFreshnessS < 0 {
		errs = append(errs, fmt.Errorf("config: agent.resume_freshness_s is %d, which is negative. Use 0 to turn off the resume, or a number of seconds", c.Agent.ResumeFreshnessS))
	}
	if c.Goals.MaxTurns < 1 {
		errs = append(errs, fmt.Errorf("config: goals.max_turns is %d. Use 1 or more turns", c.Goals.MaxTurns))
	}
	if c.Goals.JudgeTimeoutS < 1 {
		errs = append(errs, fmt.Errorf("config: goals.judge_timeout_s is %d. Use 1 or more seconds", c.Goals.JudgeTimeoutS))
	}
	if name := c.Goals.JudgeProvider; name != "" {
		if _, ok := c.Providers[name]; !ok {
			errs = append(errs, fmt.Errorf("config: goals.judge_provider names %q, which is not in providers", name))
		}
	}
	errs = append(errs, checkMCPNames(c.MCP.Servers)...)
	errs = append(errs, c.ACP.validate()...)
	errs = append(errs,
		oneOf("approvals.mode", c.Approvals.Mode, "manual", "smart", "off"),
		oneOf("approvals.cron_mode", c.Approvals.CronMode, "deny", "approve"),
		oneOf("cron.missed_runs", c.Cron.MissedRuns, "skip", "catchup"),
		oneOf("telegram.streaming.transport", c.Telegram.Streaming.Transport, "auto", "edit", "draft"),
		oneOf("telegram.tool_progress", c.Telegram.ToolProgress, "off", "new", "all", "verbose"),
	)
	return errors.Join(errs...)
}

// MCPSafeName returns s with each character that a provider does not accept in a tool name
// changed to "_". The MCP tool names and the names of the MCP tool cache files use it.
func MCPSafeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, s)
}

// checkMCPNames returns an error for each two MCP server names that have the same
// MCPSafeName when the case is ignored. Such servers share the tool names and the cache file.
// The check ignores the case, because the default file system of macOS ignores it.
func checkMCPNames(servers map[string]MCPServer) []error {
	var errs []error
	first := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(servers)) {
		key := strings.ToLower(MCPSafeName(name))
		if other, ok := first[key]; ok {
			errs = append(errs, fmt.Errorf(`config: the MCP server names %q and %q are the same when each character that is not a letter, a digit, "_", or "-" changes to "_" and the case is ignored. Change one of the names`, other, name))
			continue
		}
		first[key] = name
	}
	return errs
}

// oneOf returns an error that names key when value is not in allowed.
func oneOf(key, value string, allowed ...string) error {
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return fmt.Errorf("config: %s is %q, which is not a known value. Use %s", key, value, strings.Join(allowed, " | "))
}
