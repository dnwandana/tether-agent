package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
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
	errs = append(errs, c.checkLimits()...)
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

// maxSeconds is the maximum of each time key in seconds (30 days). It keeps
// time.Duration(n) * time.Second from an overflow.
const maxSeconds = 2592000

// intRange returns an error that names key when v is not from lo to hi.
func intRange(key string, v, lo, hi int) error {
	if v >= lo && v <= hi {
		return nil
	}
	return fmt.Errorf("config: %s is %d. Use a value from %d to %d", key, v, lo, hi)
}

// secondsFloat returns an error that names key when v is not finite, not above 0, or above
// maxSeconds.
func secondsFloat(key string, v float64) error {
	if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) || v > maxSeconds {
		return fmt.Errorf("config: %s is %g. Use a number above 0 and up to %d", key, v, maxSeconds)
	}
	return nil
}

// orderedInt returns an error when va is more than vb.
func orderedInt(a string, va int, b string, vb int) error {
	if va <= vb {
		return nil
	}
	return fmt.Errorf("config: %s is %d, which is more than %s (%d). Make %s smaller or %s larger", a, va, b, vb, a, b)
}

// orderedFloat returns an error when va is more than vb.
func orderedFloat(a string, va float64, b string, vb float64) error {
	if va <= vb {
		return nil
	}
	return fmt.Errorf("config: %s is %g, which is more than %s (%g). Make %s smaller or %s larger", a, va, b, vb, a, b)
}

// imageSize returns an error when v is not in the form <W>x<H> with W and H of 1 or more.
func imageSize(v string) error {
	parts := strings.Split(v, "x")
	if len(parts) == 2 {
		w, errW := strconv.Atoi(parts[0])
		h, errH := strconv.Atoi(parts[1])
		if errW == nil && errH == nil && w >= 1 && h >= 1 {
			return nil
		}
	}
	return fmt.Errorf("config: media.image_size is %q. Use the form <W>x<H>, for example 1024x1024", v)
}

// checkLimits returns one error for each new limit key that is out of range or out of order.
func (c *Config) checkLimits() []error {
	i32, imax := math.MaxInt32, math.MaxInt
	var errs []error
	add := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	add(intRange("cron.script_timeout_s", c.Cron.ScriptTimeoutS, 1, maxSeconds))
	add(intRange("cron.agent_timeout_s", c.Cron.AgentTimeoutS, 0, maxSeconds))
	add(intRange("cron.script_output_max_chars", c.Cron.ScriptOutputMaxChars, 1000, i32))
	add(intRange("cron.script_stderr_tail_lines", c.Cron.ScriptStderrTailLines, 1, i32))
	add(intRange("cron.tick_s", c.Cron.TickS, 1, maxSeconds))
	add(intRange("cron.grace_min_s", c.Cron.GraceMinS, 1, maxSeconds))
	add(intRange("cron.grace_max_s", c.Cron.GraceMaxS, 1, maxSeconds))
	add(orderedInt("cron.grace_min_s", c.Cron.GraceMinS, "cron.grace_max_s", c.Cron.GraceMaxS))
	add(intRange("agent.stream_idle_timeout_s", c.Agent.StreamIdleTimeoutS, 1, maxSeconds))
	add(intRange("agent.max_same_tool_failures", c.Agent.MaxSameToolFailures, 1, i32))
	add(intRange("agent.tool_preview_chars", c.Agent.ToolPreviewChars, 4, i32))
	add(intRange("review.max_rounds", c.Review.MaxRounds, 1, i32))
	add(intRange("review.max_concurrent", c.Review.MaxConcurrent, 1, i32))
	add(intRange("review.min_user_turns", c.Review.MinUserTurns, 1, i32))
	add(intRange("provider.failure_skip_s", c.Provider.FailureSkipS, 1, maxSeconds))
	add(intRange("provider.anthropic_max_tokens", c.Provider.AnthropicMaxTokens, 1, i32))
	add(intRange("provider.model_cache_ttl_s", c.Provider.ModelCacheTTLS, 1, maxSeconds))
	add(intRange("provider.model_list_timeout_s", c.Provider.ModelListTimeoutS, 1, maxSeconds))
	add(intRange("provider.default_context_window", c.Provider.DefaultContextWindow, 1000, i32))
	add(intRange("context.summary_timeout_s", c.Context.SummaryTimeoutS, 1, maxSeconds))
	add(intRange("context.summary_min_tokens", c.Context.SummaryMinTokens, 100, i32))
	add(intRange("context.summary_max_tokens", c.Context.SummaryMaxTokens, 100, i32))
	add(orderedInt("context.summary_min_tokens", c.Context.SummaryMinTokens, "context.summary_max_tokens", c.Context.SummaryMaxTokens))
	add(intRange("context.compact_max_failures", c.Context.CompactMaxFailures, 1, i32))
	add(intRange("context.compact_max_ineffective", c.Context.CompactMaxIneffective, 1, i32))
	add(intRange("goals.judge_max_tokens", c.Goals.JudgeMaxTokens, 256, i32))
	add(intRange("goals.judge_max_parse_failures", c.Goals.JudgeMaxParseFailures, 1, i32))
	add(intRange("goals.judge_max_transport_failures", c.Goals.JudgeMaxTransportFailures, 1, i32))
	add(intRange("goals.gate_timeout_s", c.Goals.GateTimeoutS, 1, maxSeconds))
	add(intRange("goals.gate_max_retries", c.Goals.GateMaxRetries, 1, i32))
	add(intRange("goals.gate_output_tail_chars", c.Goals.GateOutputTailChars, 100, i32))
	add(intRange("goals.max_barrier_wait_s", c.Goals.MaxBarrierWaitS, 1, maxSeconds))
	add(intRange("goals.draft_objective_chars", c.Goals.DraftObjectiveChars, 100, i32))
	jc := c.Goals.JudgeChars
	add(intRange("goals.judge_chars.goal", jc.Goal, 1, i32))
	add(intRange("goals.judge_chars.response", jc.Response, 1, i32))
	add(intRange("goals.judge_chars.contract", jc.Contract, 1, i32))
	add(intRange("goals.judge_chars.subgoal", jc.Subgoal, 1, i32))
	add(intRange("goals.judge_chars.todo", jc.Todo, 1, i32))
	add(intRange("goals.judge_chars.process", jc.Process, 1, i32))
	add(intRange("memory.entry_max_chars", c.Memory.EntryMaxChars, 100, i32))
	add(intRange("memory.lock_wait_s", c.Memory.LockWaitS, 1, maxSeconds))
	add(intRange("memory.read_max_chars", c.Memory.ReadMaxChars, 1000, i32))
	add(intRange("memory.search_max_hits", c.Memory.SearchMaxHits, 1, i32))
	add(intRange("memory.prompt_max_topics", c.Memory.PromptMaxTopics, 1, i32))
	add(intRange("skills.git_timeout_s", c.Skills.GitTimeoutS, 1, maxSeconds))
	add(intRange("skills.skill_md_max_chars", c.Skills.SkillMDMaxChars, 1000, i32))
	add(intRange("skills.support_file_max_bytes", c.Skills.SupportFileMaxBytes, 1024, imax))
	add(intRange("skills.view_max_linked_files", c.Skills.ViewMaxLinkedFiles, 1, i32))
	add(intRange("web.http_timeout_s", c.Web.HTTPTimeoutS, 1, maxSeconds))
	add(intRange("web.search_default_results", c.Web.SearchDefaultResults, 1, i32))
	add(intRange("web.search_max_results", c.Web.SearchMaxResults, 1, i32))
	add(orderedInt("web.search_default_results", c.Web.SearchDefaultResults, "web.search_max_results", c.Web.SearchMaxResults))
	add(intRange("browser.action_timeout_s", c.Browser.ActionTimeoutS, 1, maxSeconds))
	add(intRange("browser.navigate_excerpt_chars", c.Browser.NavigateExcerptChars, 100, i32))
	add(secondsFloat("browser.settle_s", c.Browser.SettleS))
	if c.Browser.SettleS > 0 && c.Browser.SettleS <= maxSeconds && c.Browser.SettleS >= float64(c.Browser.ActionTimeoutS) {
		errs = append(errs, fmt.Errorf("config: browser.settle_s is %g, which is not less than browser.action_timeout_s (%d). Make browser.settle_s smaller", c.Browser.SettleS, c.Browser.ActionTimeoutS))
	}
	add(intRange("files.read_max_lines", c.Files.ReadMaxLines, 1, i32))
	add(intRange("files.read_max_line_chars", c.Files.ReadMaxLineChars, 100, i32))
	add(intRange("files.patch_context_lines", c.Files.PatchContextLines, 1, i32))
	add(intRange("todo.max_items", c.Todo.MaxItems, 1, i32))
	add(intRange("todo.max_item_chars", c.Todo.MaxItemChars, 100, i32))
	add(intRange("terminal.process_keep_s", c.Terminal.ProcessKeepS, 1, maxSeconds))
	add(intRange("code_execution.max_stderr_chars", c.CodeExecution.MaxStderrChars, 100, i32))
	add(intRange("approvals.smart_timeout_s", c.Approvals.SmartTimeoutS, 1, maxSeconds))
	add(intRange("approvals.smart_script_max_chars", c.Approvals.SmartScriptMaxChars, 100, i32))
	for _, name := range slices.Sorted(maps.Keys(c.MCP.Servers)) {
		add(intRange("mcp.servers."+name+".max_dial_tries", c.MCP.Servers[name].MaxDialTries, 1, 10))
	}
	add(intRange("sessions.search_default_results", c.Sessions.SearchDefaultResults, 1, i32))
	add(intRange("sessions.search_max_results", c.Sessions.SearchMaxResults, 1, i32))
	add(orderedInt("sessions.search_default_results", c.Sessions.SearchDefaultResults, "sessions.search_max_results", c.Sessions.SearchMaxResults))
	add(intRange("sessions.scroll_default_window", c.Sessions.ScrollDefaultWindow, 1, i32))
	add(intRange("sessions.scroll_max_window", c.Sessions.ScrollMaxWindow, 1, i32))
	add(orderedInt("sessions.scroll_default_window", c.Sessions.ScrollDefaultWindow, "sessions.scroll_max_window", c.Sessions.ScrollMaxWindow))
	add(intRange("media.vision_max_bytes", c.Media.VisionMaxBytes, 1024, imax))
	add(intRange("media.image_download_timeout_s", c.Media.ImageDownloadTimeoutS, 1, maxSeconds))
	add(imageSize(c.Media.ImageSize))
	add(intRange("media.tts.timeout_s", c.Media.TTS.TimeoutS, 0, maxSeconds))
	add(intRange("media.stt.timeout_s", c.Media.STT.TimeoutS, 0, maxSeconds))
	add(intRange("media.image.timeout_s", c.Media.Image.TimeoutS, 0, maxSeconds))
	add(intRange("telegram.api_timeout_s", c.Telegram.APITimeoutS, 1, maxSeconds))
	add(intRange("telegram.upload_timeout_s", c.Telegram.UploadTimeoutS, 1, maxSeconds))
	add(secondsFloat("telegram.album_wait_s", c.Telegram.AlbumWaitS))
	add(intRange("telegram.rich_media_timeout_s", c.Telegram.RichMediaTimeoutS, 1, maxSeconds))
	add(intRange("telegram.inline_text_max_bytes", c.Telegram.InlineTextMaxBytes, 1024, imax))
	add(intRange("telegram.sticker_describe_timeout_s", c.Telegram.StickerDescribeTimeoutS, 1, maxSeconds))
	add(intRange("telegram.voice_convert_timeout_s", c.Telegram.VoiceConvertTimeoutS, 1, maxSeconds))
	add(intRange("telegram.menu_max_commands", c.Telegram.MenuMaxCommands, 22, 100))
	add(secondsFloat("telegram.status_interval_s", c.Telegram.StatusIntervalS))
	add(intRange("telegram.pairing_code_ttl_s", c.Telegram.PairingCodeTTLS, 1, maxSeconds))
	add(intRange("gateway.max_queued_messages", c.Gateway.MaxQueuedMessages, 1, i32))
	add(secondsFloat("gateway.debounce_s", c.Gateway.DebounceS))
	add(secondsFloat("gateway.debounce_max_s", c.Gateway.DebounceMaxS))
	add(orderedFloat("gateway.debounce_s", c.Gateway.DebounceS, "gateway.debounce_max_s", c.Gateway.DebounceMaxS))
	add(intRange("gateway.model_page_size", c.Gateway.ModelPageSize, 1, i32))
	add(intRange("gateway.session_page_size", c.Gateway.SessionPageSize, 1, i32))
	add(intRange("gateway.help_max_skills", c.Gateway.HelpMaxSkills, 1, i32))
	add(intRange("gateway.housekeeping_interval_s", c.Gateway.HousekeepingIntervalS, 60, maxSeconds))
	add(intRange("gateway.media_trust_recent_s", c.Gateway.MediaTrustRecentS, 0, maxSeconds))
	for _, p := range c.Gateway.MediaAllowDirs {
		if !filepath.IsAbs(expandHome(p)) {
			errs = append(errs, fmt.Errorf("config: gateway.media_allow_dirs has %q, which is not an absolute path. Use a path that starts with / or ~/", p))
		}
	}
	add(intRange("update.download_timeout_s", c.Update.DownloadTimeoutS, 1, maxSeconds))
	add(intRange("update.api_timeout_s", c.Update.APITimeoutS, 1, maxSeconds))
	add(intRange("doctor.check_timeout_s", c.Doctor.CheckTimeoutS, 1, maxSeconds))
	add(intRange("doctor.acp_check_timeout_s", c.Doctor.ACPCheckTimeoutS, 1, maxSeconds))
	add(intRange("doctor.min_free_bytes", c.Doctor.MinFreeBytes, 1, imax))
	return errs
}
