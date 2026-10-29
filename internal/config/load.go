package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
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
// is valid. If env is nil, Load uses os.LookupEnv.
func Load(path string, env func(string) (string, bool)) (*Config, error) {
	if env == nil {
		env = os.LookupEnv
	}
	c := Default()

	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("config: cannot read %s: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) > 0 {
		var root yaml.Node
		if err := yaml.Unmarshal(data, &root); err != nil {
			return nil, fmt.Errorf("config: %s is not valid YAML: %w. Correct the file and try again", path, err)
		}
		if err := expand(&root, "", env); err != nil {
			return nil, err
		}
		if err := root.Decode(&c); err != nil {
			return nil, fmt.Errorf("config: %s has a value of the wrong type: %w. Correct the file and try again", path, err)
		}
	}

	// An empty telegram.bot_token falls back to $TELEGRAM_BOT_TOKEN. An
	// unset variable is not an error here, because Telegram is optional.
	if c.Telegram.BotToken == "" {
		if v, ok := env("TELEGRAM_BOT_TOKEN"); ok {
			c.Telegram.BotToken = v
		}
	}

	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
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
	names := make([]string, 0, len(c.Providers))
	for name := range c.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		errs = append(errs, oneOf("providers."+name+".format", c.Providers[name].Format, "openai", "anthropic"))
	}
	if c.Sessions.RetentionDays < 0 {
		errs = append(errs, fmt.Errorf("config: sessions.retention_days is %d, which is negative. Use 0 to keep all sessions, or a number of days", c.Sessions.RetentionDays))
	}
	if c.Agent.RestartDrainTimeoutS < 0 {
		errs = append(errs, fmt.Errorf("config: agent.restart_drain_timeout_s is %d, which is negative. Use 0 to cancel the running turns at once, or a number of seconds", c.Agent.RestartDrainTimeoutS))
	}
	if c.Agent.ResumeFreshnessS < 0 {
		errs = append(errs, fmt.Errorf("config: agent.resume_freshness_s is %d, which is negative. Use 0 to turn off the resume, or a number of seconds", c.Agent.ResumeFreshnessS))
	}
	errs = append(errs,
		oneOf("approvals.mode", c.Approvals.Mode, "manual", "smart", "off"),
		oneOf("approvals.cron_mode", c.Approvals.CronMode, "deny", "approve"),
		oneOf("cron.missed_runs", c.Cron.MissedRuns, "skip", "catchup"),
		oneOf("telegram.streaming.transport", c.Telegram.Streaming.Transport, "auto", "edit", "draft"),
		oneOf("telegram.tool_progress", c.Telegram.ToolProgress, "off", "new", "all", "verbose"),
	)
	return errors.Join(errs...)
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
