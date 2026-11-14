// Package config holds the Tether Agent configuration schema and its defaults.
//
// Default is the only place in the code base that sets a default value. The
// yaml tag of each field is the key name in config.yaml. The docs test reads
// these tags with reflection and compares them with docs/CONFIG.md, so keep them exact.
package config

import (
	"slices"

	"gopkg.in/yaml.v3"
)

// Config is the full content of config.yaml.
type Config struct {
	Providers     map[string]Provider `yaml:"providers"`
	Provider      ProviderConfig      `yaml:"provider"`
	Agent         AgentConfig         `yaml:"agent"`
	Context       ContextConfig       `yaml:"context"`
	Sessions      SessionsConfig      `yaml:"sessions"`
	Memory        MemoryConfig        `yaml:"memory"`
	Review        ReviewConfig        `yaml:"review"`
	Skills        SkillsConfig        `yaml:"skills"`
	Web           WebConfig           `yaml:"web"`
	Browser       BrowserConfig       `yaml:"browser"`
	Terminal      TerminalConfig      `yaml:"terminal"`
	CodeExecution CodeExecConfig      `yaml:"code_execution"`
	Files         FilesConfig         `yaml:"files"`
	Todo          TodoConfig          `yaml:"todo"`
	Delegation    DelegationConfig    `yaml:"delegation"`
	Approvals     ApprovalsConfig     `yaml:"approvals"`
	Goals         GoalsConfig         `yaml:"goals"`
	Toolsets      map[string][]string `yaml:"toolsets"`
	Cron          CronConfig          `yaml:"cron"`
	Gateway       GatewayConfig       `yaml:"gateway"`
	Telegram      TelegramConfig      `yaml:"telegram"`
	MCP           MCPConfig           `yaml:"mcp"`
	ACP           ACPConfig           `yaml:"acp"`
	Media         MediaConfig         `yaml:"media"`
	Pricing       PricingConfig       `yaml:"pricing"`
	Update        UpdateConfig        `yaml:"update"`
	Doctor        DoctorConfig        `yaml:"doctor"`
	Log           LogConfig           `yaml:"log"`
}

// Provider is one named model endpoint.
type Provider struct {
	// Preset is the id of a known endpoint. It fills an empty format, base_url, and api_key.
	Preset        string `yaml:"preset"`
	Format        string `yaml:"format"`
	BaseURL       string `yaml:"base_url"`
	APIKey        string `yaml:"api_key"`
	Model         string `yaml:"model"`
	Vision        bool   `yaml:"vision"`
	ContextWindow int    `yaml:"context_window"`
	PriceKey      string `yaml:"price_key"`
	// Effort is the default reasoning effort of the entry. "" sends no effort field.
	Effort string `yaml:"effort"`
	// Unusable holds the load error of an entry that cannot serve a call. Load sets it.
	Unusable string `yaml:"-"`
	// UnusableCause is the short cause of Unusable, for chat text. It is UnusableNoKey or
	// UnusableNoModel. Load sets it.
	UnusableCause string `yaml:"-"`
}

// The values of Provider.UnusableCause.
const (
	UnusableNoKey   = "no API key"
	UnusableNoModel = "no model in config.yaml"
)

// ProviderConfig sets the limits that apply to all entries of providers.
type ProviderConfig struct {
	// FailureSkipS is the time in seconds that the chain skips a failed entry.
	FailureSkipS int `yaml:"failure_skip_s"`
	// AnthropicMaxTokens is the max_tokens value of an anthropic call that sets no value.
	AnthropicMaxTokens int `yaml:"anthropic_max_tokens"`
	// ModelCacheTTLS is the time in seconds that /model keeps the model list of an entry.
	ModelCacheTTLS int `yaml:"model_cache_ttl_s"`
	// ModelListTimeoutS is the time limit of one model list call of /model, in seconds.
	ModelListTimeoutS int `yaml:"model_list_timeout_s"`
	// DefaultContextWindow is the context window in tokens when no other source gives one.
	DefaultContextWindow int `yaml:"default_context_window"`
}

// AgentConfig sets the limits of the agent loop and the providers for vision and delegation.
type AgentConfig struct {
	MaxTurns           int    `yaml:"max_turns"`
	ToolConcurrency    int    `yaml:"tool_concurrency"`
	VisionProvider     string `yaml:"vision_provider"`
	DelegationProvider string `yaml:"delegation_provider"`
	// RestartDrainTimeoutS is the time in seconds that a stop waits for the running turns.
	RestartDrainTimeoutS int `yaml:"restart_drain_timeout_s"`
	// ResumeFreshnessS is the age in seconds up to which a cut turn continues at the next start.
	ResumeFreshnessS int `yaml:"resume_freshness_s"`
	// StreamIdleTimeoutS is the time in seconds that a model stream may send no data.
	StreamIdleTimeoutS int `yaml:"stream_idle_timeout_s"`
	// MaxSameToolFailures is the number of bad tool calls in a row that stops the turn.
	MaxSameToolFailures int `yaml:"max_same_tool_failures"`
	// ToolPreviewChars is the length limit of the argument preview in a tool status line.
	ToolPreviewChars int `yaml:"tool_preview_chars"`
	// Fallback is the ordered list of entries that serve a call when the entries before fail.
	// nil means that the key is absent. An empty list turns off the fallback.
	Fallback []string `yaml:"fallback"`
}

// FallbackList returns the effective fallback entries in order. A present
// agent.fallback key wins, also an empty list. Else an entry with the name
// "fallback" gives the old one-entry list. Else the list is nil.
func (c *Config) FallbackList() []string {
	if c.Agent.Fallback != nil {
		return slices.Clone(c.Agent.Fallback)
	}
	if _, ok := c.Providers["fallback"]; ok {
		return []string{"fallback"}
	}
	return nil
}

// ContextConfig sets the compaction and the tool output limits.
type ContextConfig struct {
	CompactThreshold   float64 `yaml:"compact_threshold"`
	ProtectFirstN      int     `yaml:"protect_first_n"`
	ProtectLastN       int     `yaml:"protect_last_n"`
	SummaryTargetRatio float64 `yaml:"summary_target_ratio"`
	ToolOutputMaxChars int     `yaml:"tool_output_max_chars"`
	SpillRetentionDays int     `yaml:"spill_retention_days"`
	// SummaryTimeoutS is the time limit of one compaction summary call, in seconds.
	SummaryTimeoutS int `yaml:"summary_timeout_s"`
	// SummaryMinTokens and SummaryMaxTokens bound the token budget of a compaction summary.
	SummaryMinTokens int `yaml:"summary_min_tokens"`
	SummaryMaxTokens int `yaml:"summary_max_tokens"`
	// CompactMaxFailures is the number of failed compactions in a row before the user is told.
	CompactMaxFailures int `yaml:"compact_max_failures"`
	// CompactMaxIneffective is the number of compactions in a row that leave the context over the threshold.
	CompactMaxIneffective int `yaml:"compact_max_ineffective"`
	// CompactNotice sends a notice to the user after each automatic compaction.
	CompactNotice bool `yaml:"compact_notice"`
}

// SessionsConfig sets the retention of the sessions in state.db.
type SessionsConfig struct {
	// RetentionDays is the age, by last activity, after which the gateway deletes a
	// cron, subagent, or review session. 0 keeps them all. Chat sessions always stay.
	RetentionDays int `yaml:"retention_days"`
	// SearchDefaultResults and SearchMaxResults set the result count of session_search.
	SearchDefaultResults int `yaml:"search_default_results"`
	SearchMaxResults     int `yaml:"search_max_results"`
	// ScrollDefaultWindow and ScrollMaxWindow set the message count of a session_search scroll.
	ScrollDefaultWindow int `yaml:"scroll_default_window"`
	ScrollMaxWindow     int `yaml:"scroll_max_window"`
}

// MemoryConfig sets the memory limits and the review interval.
type MemoryConfig struct {
	MemoryCharLimit     int `yaml:"memory_char_limit"`
	UserCharLimit       int `yaml:"user_char_limit"`
	ReviewIntervalTurns int `yaml:"review_interval_turns"`
	// EntryMaxChars is the maximum length of one memory entry, in characters.
	EntryMaxChars int `yaml:"entry_max_chars"`
	// LockWaitS is the maximum wait for the memory file lock, in seconds.
	LockWaitS int `yaml:"lock_wait_s"`
	// ReadMaxChars is the maximum length of a memory read result, in characters.
	ReadMaxChars int `yaml:"read_max_chars"`
	// SearchMaxHits is the maximum number of hits of a memory search.
	SearchMaxHits int `yaml:"search_max_hits"`
	// PromptMaxTopics is the maximum number of note topics in the system prompt.
	PromptMaxTopics int `yaml:"prompt_max_topics"`
}

// ReviewConfig sets the background memory and skill review.
type ReviewConfig struct {
	MaxRounds     int `yaml:"max_rounds"`
	MaxConcurrent int `yaml:"max_concurrent"`
	MinUserTurns  int `yaml:"min_user_turns"`
}

// SkillsConfig sets the skill folders, the review interval, and the guard.
type SkillsConfig struct {
	ExternalDirs             []string `yaml:"external_dirs"`
	ReviewIntervalToolRounds int      `yaml:"review_interval_tool_rounds"`
	Guard                    bool     `yaml:"guard"`
	// GitTimeoutS is the time limit of each git call of tether skills, in seconds.
	GitTimeoutS int `yaml:"git_timeout_s"`
	// SkillMDMaxChars is the maximum length of a SKILL.md file that skill_manage writes.
	SkillMDMaxChars int `yaml:"skill_md_max_chars"`
	// SupportFileMaxBytes is the maximum size of a support file that skill_manage writes.
	SupportFileMaxBytes int `yaml:"support_file_max_bytes"`
	// ViewMaxLinkedFiles is the maximum number of support files that skill_view lists.
	ViewMaxLinkedFiles int `yaml:"view_max_linked_files"`
}

// WebConfig sets the web_search and web_fetch tools.
type WebConfig struct {
	SearchBackend        string `yaml:"search_backend"`
	FetchMaxURLs         int    `yaml:"fetch_max_urls"`
	FetchMaxBytes        int    `yaml:"fetch_max_bytes"`
	BlockPrivateNetworks bool   `yaml:"block_private_networks"`
	// HTTPTimeoutS is the time limit of one HTTP request, in seconds.
	HTTPTimeoutS int `yaml:"http_timeout_s"`
	// SearchDefaultResults and SearchMaxResults set the result count of web_search.
	SearchDefaultResults int `yaml:"search_default_results"`
	SearchMaxResults     int `yaml:"search_max_results"`
}

// BrowserConfig sets the Chrome manager.
type BrowserConfig struct {
	ChromePath       string `yaml:"chrome_path"`
	IdleCloseMinutes int    `yaml:"idle_close_minutes"`
	// ActionTimeoutS is the time limit of each browser action, in seconds.
	ActionTimeoutS int `yaml:"action_timeout_s"`
	// NavigateExcerptChars is the length of the page excerpt that browser_navigate returns.
	NavigateExcerptChars int `yaml:"navigate_excerpt_chars"`
	// SettleS is the wait after a page is ready, in seconds.
	SettleS float64 `yaml:"settle_s"`
}

// TerminalConfig sets the terminal tool.
type TerminalConfig struct {
	DefaultTimeoutS int      `yaml:"default_timeout_s"`
	EnvPassthrough  []string `yaml:"env_passthrough"`
	// ProcessKeepS is the time in seconds that the program keeps the record of an exited process.
	ProcessKeepS int `yaml:"process_keep_s"`
}

// CodeExecConfig sets the execute_code tool limits.
type CodeExecConfig struct {
	TimeoutS       int `yaml:"timeout_s"`
	MaxToolCalls   int `yaml:"max_tool_calls"`
	MaxStdoutBytes int `yaml:"max_stdout_bytes"`
	// MaxStderrChars is the maximum stderr text that a failed script returns, in characters.
	MaxStderrChars int `yaml:"max_stderr_chars"`
}

// FilesConfig sets the limits of the file tools.
type FilesConfig struct {
	ReadMaxLines      int `yaml:"read_max_lines"`
	ReadMaxLineChars  int `yaml:"read_max_line_chars"`
	PatchContextLines int `yaml:"patch_context_lines"`
}

// TodoConfig sets the limits of the todo list.
type TodoConfig struct {
	MaxItems     int `yaml:"max_items"`
	MaxItemChars int `yaml:"max_item_chars"`
}

// DelegationConfig sets the child run limits.
type DelegationConfig struct {
	MaxConcurrentChildren int `yaml:"max_concurrent_children"`
	MaxSpawnDepth         int `yaml:"max_spawn_depth"`
	MaxIterations         int `yaml:"max_iterations"`
	ChildTimeoutS         int `yaml:"child_timeout_s"`
}

// ApprovalsConfig sets the approval gate.
type ApprovalsConfig struct {
	Mode     string `yaml:"mode"`
	TimeoutS int    `yaml:"timeout_s"`
	CronMode string `yaml:"cron_mode"`
	// SmartTimeoutS is the time limit of one smart judge call, in seconds.
	SmartTimeoutS int `yaml:"smart_timeout_s"`
	// SmartScriptMaxChars is the number of script characters that the smart judge gets.
	SmartScriptMaxChars int `yaml:"smart_script_max_chars"`
}

// GoalsConfig sets the /goal loop.
type GoalsConfig struct {
	// MaxTurns is the turn budget of a new goal.
	MaxTurns int `yaml:"max_turns"`
	// JudgeProvider is the provider entry of the goal judge and of /goal draft. "" uses the
	// chain of the session.
	JudgeProvider string `yaml:"judge_provider"`
	// JudgeTimeoutS is the timeout of one judge call or draft call, in seconds.
	JudgeTimeoutS int `yaml:"judge_timeout_s"`
	// JudgeMaxTokens is the output limit of one judge call, in tokens.
	JudgeMaxTokens int `yaml:"judge_max_tokens"`
	// JudgeMaxParseFailures is the number of unparsable judge replies in a row that pauses a goal.
	JudgeMaxParseFailures int `yaml:"judge_max_parse_failures"`
	// JudgeMaxTransportFailures is the number of failed judge calls in a row that pauses a goal.
	JudgeMaxTransportFailures int `yaml:"judge_max_transport_failures"`
	// GateTimeoutS is the time limit of one gate run, in seconds.
	GateTimeoutS int `yaml:"gate_timeout_s"`
	// GateMaxRetries is the number of failed gate runs before the goal pauses.
	GateMaxRetries int `yaml:"gate_max_retries"`
	// GateOutputTailChars is the number of output characters that a gate run keeps.
	GateOutputTailChars int `yaml:"gate_output_tail_chars"`
	// MaxBarrierWaitS is the longest time that a barrier holds a goal, in seconds.
	MaxBarrierWaitS int `yaml:"max_barrier_wait_s"`
	// DraftObjectiveChars is the maximum length of the objective in a draft prompt.
	DraftObjectiveChars int `yaml:"draft_objective_chars"`
	// JudgeChars sets the cut length of each part of the judge prompt.
	JudgeChars GoalsJudgeChars `yaml:"judge_chars"`
}

// GoalsJudgeChars sets the length, in characters, at which the judge prompt cuts each part.
type GoalsJudgeChars struct {
	Goal     int `yaml:"goal"`
	Response int `yaml:"response"`
	Contract int `yaml:"contract"`
	Subgoal  int `yaml:"subgoal"`
	Todo     int `yaml:"todo"`
	Process  int `yaml:"process"`
}

// CronConfig sets the cron scheduler.
type CronConfig struct {
	MaxParallelJobs int    `yaml:"max_parallel_jobs"`
	MissedRuns      string `yaml:"missed_runs"`
	// ScriptTimeoutS is the time limit of one cron script run, in seconds.
	ScriptTimeoutS int `yaml:"script_timeout_s"`
	// AgentTimeoutS is the time limit of one cron agent turn, in seconds. 0 means no limit.
	AgentTimeoutS int `yaml:"agent_timeout_s"`
	// ScriptOutputMaxChars is the number of stdout characters that a script result keeps.
	ScriptOutputMaxChars int `yaml:"script_output_max_chars"`
	// ScriptStderrTailLines is the number of last stderr lines in the alert of a failed script.
	ScriptStderrTailLines int `yaml:"script_stderr_tail_lines"`
	// TickS is the time between two checks for due jobs, in seconds.
	TickS int `yaml:"tick_s"`
	// GraceMinS and GraceMaxS bound the grace window of a late recurring run, in seconds.
	GraceMinS int `yaml:"grace_min_s"`
	GraceMaxS int `yaml:"grace_max_s"`
}

// GatewayConfig sets the gateway: the message queue, the command lists, the housekeeping, and
// the local files that an answer can send.
type GatewayConfig struct {
	MaxQueuedMessages     int      `yaml:"max_queued_messages"`
	DebounceS             float64  `yaml:"debounce_s"`
	DebounceMaxS          float64  `yaml:"debounce_max_s"`
	ModelPageSize         int      `yaml:"model_page_size"`
	SessionPageSize       int      `yaml:"session_page_size"`
	HelpMaxSkills         int      `yaml:"help_max_skills"`
	HousekeepingIntervalS int      `yaml:"housekeeping_interval_s"`
	MediaDeliveryStrict   bool     `yaml:"media_delivery_strict"`
	MediaAllowDirs        []string `yaml:"media_allow_dirs"`
	MediaTrustRecentFiles bool     `yaml:"media_trust_recent_files"`
	MediaTrustRecentS     int      `yaml:"media_trust_recent_s"`
}

// TelegramConfig sets the Telegram channel.
type TelegramConfig struct {
	Enabled                         bool              `yaml:"enabled"`
	BotToken                        string            `yaml:"bot_token"`
	AllowedUsers                    []string          `yaml:"allowed_users"`
	AllowAllUsers                   bool              `yaml:"allow_all_users"`
	GroupAllowedUsers               []string          `yaml:"group_allowed_users"`
	GroupAllowedChats               []string          `yaml:"group_allowed_chats"`
	AllowedChats                    []string          `yaml:"allowed_chats"`
	FreeResponseChats               []string          `yaml:"free_response_chats"`
	RequireMention                  bool              `yaml:"require_mention"`
	ExclusiveBotMentions            bool              `yaml:"exclusive_bot_mentions"`
	MentionPatterns                 []string          `yaml:"mention_patterns"`
	GuestMode                       bool              `yaml:"guest_mode"`
	AllowedTopics                   []string          `yaml:"allowed_topics"`
	IgnoredThreads                  []string          `yaml:"ignored_threads"`
	GroupSessionsPerUser            bool              `yaml:"group_sessions_per_user"`
	ThreadSessionsPerUser           bool              `yaml:"thread_sessions_per_user"`
	ObserveUnmentionedGroupMessages bool              `yaml:"observe_unmentioned_group_messages"`
	UnauthorizedDMBehavior          string            `yaml:"unauthorized_dm_behavior"`
	HomeChat                        string            `yaml:"home_chat"`
	HomeThread                      string            `yaml:"home_thread"`
	CronThread                      string            `yaml:"cron_thread"`
	Streaming                       TelegramStreaming `yaml:"streaming"`
	ToolProgress                    string            `yaml:"tool_progress"`
	CleanupProgress                 bool              `yaml:"cleanup_progress"`
	APITimeoutS                     int               `yaml:"api_timeout_s"`
	UploadTimeoutS                  int               `yaml:"upload_timeout_s"`
	AlbumWaitS                      float64           `yaml:"album_wait_s"`
	RichMediaTimeoutS               int               `yaml:"rich_media_timeout_s"`
	InlineTextMaxBytes              int               `yaml:"inline_text_max_bytes"`
	StickerDescribeTimeoutS         int               `yaml:"sticker_describe_timeout_s"`
	VoiceConvertTimeoutS            int               `yaml:"voice_convert_timeout_s"`
	MenuMaxCommands                 int               `yaml:"menu_max_commands"`
	StatusIntervalS                 float64           `yaml:"status_interval_s"`
	PairingCodeTTLS                 int               `yaml:"pairing_code_ttl_s"`
}

// TelegramStreaming sets how the Telegram channel streams an answer.
type TelegramStreaming struct {
	Transport        string  `yaml:"transport"`
	EditIntervalS    float64 `yaml:"edit_interval_s"`
	BufferThreshold  int     `yaml:"buffer_threshold"`
	Cursor           string  `yaml:"cursor"`
	FreshFinalAfterS float64 `yaml:"fresh_final_after_s"`
}

// MCPConfig holds the MCP servers by name.
type MCPConfig struct {
	Servers map[string]MCPServer `yaml:"servers"`
}

// MCPServer is one MCP server entry. Set Command for stdio, or URL for HTTP.
type MCPServer struct {
	Command                   []string          `yaml:"command"`
	Env                       map[string]string `yaml:"env"`
	URL                       string            `yaml:"url"`
	Transport                 string            `yaml:"transport"`
	Headers                   map[string]string `yaml:"headers"`
	TimeoutS                  int               `yaml:"timeout_s"`
	ConnectTimeoutS           int               `yaml:"connect_timeout_s"`
	SupportsParallelToolCalls bool              `yaml:"supports_parallel_tool_calls"`
	AlwaysOn                  bool              `yaml:"always_on"`
	IdleStopMinutes           int               `yaml:"idle_stop_minutes"`
	Enabled                   bool              `yaml:"enabled"`
	MaxDialTries              int               `yaml:"max_dial_tries"`
}

// UnmarshalYAML decodes an MCP server entry over DefaultMCPServer, so that
// a key that the file does not set keeps its default.
func (s *MCPServer) UnmarshalYAML(n *yaml.Node) error {
	type plain MCPServer
	p := plain(DefaultMCPServer())
	if err := n.Decode(&p); err != nil {
		return err
	}
	*s = MCPServer(p)
	return nil
}

// MediaConfig sets the media backends.
type MediaConfig struct {
	TTS   MediaBackend `yaml:"tts"`
	STT   MediaBackend `yaml:"stt"`
	Image MediaBackend `yaml:"image"`
	// CacheRetentionDays is the age, by modification time, after which the gateway deletes a file in cache/media and cache/screenshots. 0 keeps all files.
	CacheRetentionDays int `yaml:"cache_retention_days"`
	// VisionMaxBytes is the maximum size of one image for vision, in bytes.
	VisionMaxBytes int `yaml:"vision_max_bytes"`
	// ImageDownloadTimeoutS is the time limit of one image download, in seconds.
	ImageDownloadTimeoutS int `yaml:"image_download_timeout_s"`
	// ImageSize is the {size} value of the command backend of generate_image.
	ImageSize string `yaml:"image_size"`
}

// MediaBackend selects a command template or an OpenAI-format provider.
// An empty Backend hides the tool.
type MediaBackend struct {
	Backend  string `yaml:"backend"`
	Command  string `yaml:"command"`
	Provider string `yaml:"provider"`
	// TimeoutS is the time limit of one call, in seconds. 0 uses the default of the backend.
	TimeoutS int `yaml:"timeout_s"`
}

// PricingConfig sets the price sources and the manual price overrides.
type PricingConfig struct {
	Sources      []string                 `yaml:"sources"`
	RefreshHours int                      `yaml:"refresh_hours"`
	Overrides    map[string]PriceOverride `yaml:"overrides"`
}

// PriceOverride is a manual price for one model, in USD per 1M tokens.
type PriceOverride struct {
	Input     float64 `yaml:"input"`
	Output    float64 `yaml:"output"`
	CacheRead float64 `yaml:"cache_read"`
}

// UpdateConfig sets the time limits of tether update.
type UpdateConfig struct {
	DownloadTimeoutS int `yaml:"download_timeout_s"`
	APITimeoutS      int `yaml:"api_timeout_s"`
}

// DoctorConfig sets the limits of the tether doctor checks.
type DoctorConfig struct {
	CheckTimeoutS    int `yaml:"check_timeout_s"`
	ACPCheckTimeoutS int `yaml:"acp_check_timeout_s"`
	MinFreeBytes     int `yaml:"min_free_bytes"`
}

// LogConfig sets the rotating log file.
type LogConfig struct {
	Level     string `yaml:"level"`
	MaxSizeMB int    `yaml:"max_size_mb"`
	MaxFiles  int    `yaml:"max_files"`
}

// DefaultMCPServer returns the defaults for one MCP server entry.
func DefaultMCPServer() MCPServer {
	return MCPServer{
		Env:             map[string]string{},
		Headers:         map[string]string{},
		TimeoutS:        120,
		ConnectTimeoutS: 60,
		IdleStopMinutes: 10,
		Enabled:         true,
		MaxDialTries:    5,
	}
}

// Default returns every config default. Each call returns new maps
// and slices, so a caller can change the result safely.
//
// The default of telegram.bot_token is "". Load uses $TELEGRAM_BOT_TOKEN
// when the value is still empty after the file is decoded.
func Default() Config {
	return Config{
		Providers: map[string]Provider{},
		Provider: ProviderConfig{
			FailureSkipS:         60,
			AnthropicMaxTokens:   8192,
			ModelCacheTTLS:       3600,
			ModelListTimeoutS:    10,
			DefaultContextWindow: 128000,
		},
		Agent: AgentConfig{
			MaxTurns:             90,
			ToolConcurrency:      4,
			RestartDrainTimeoutS: 180,
			ResumeFreshnessS:     3600,
			StreamIdleTimeoutS:   300,
			MaxSameToolFailures:  3,
			ToolPreviewChars:     40,
		},
		Context: ContextConfig{
			CompactThreshold:      0.50,
			ProtectFirstN:         3,
			ProtectLastN:          20,
			SummaryTargetRatio:    0.20,
			ToolOutputMaxChars:    50000,
			SpillRetentionDays:    7,
			SummaryTimeoutS:       300,
			SummaryMinTokens:      2000,
			SummaryMaxTokens:      12000,
			CompactMaxFailures:    3,
			CompactMaxIneffective: 2,
			CompactNotice:         true,
		},
		Sessions: SessionsConfig{
			RetentionDays:        90,
			SearchDefaultResults: 5,
			SearchMaxResults:     20,
			ScrollDefaultWindow:  10,
			ScrollMaxWindow:      50,
		},
		Memory: MemoryConfig{
			MemoryCharLimit:     2200,
			UserCharLimit:       1375,
			ReviewIntervalTurns: 10,
			EntryMaxChars:       4000,
			LockWaitS:           30,
			ReadMaxChars:        32000,
			SearchMaxHits:       8,
			PromptMaxTopics:     40,
		},
		Review: ReviewConfig{MaxRounds: 16, MaxConcurrent: 2, MinUserTurns: 6},
		Skills: SkillsConfig{
			ExternalDirs:             []string{},
			ReviewIntervalToolRounds: 10,
			Guard:                    true,
			GitTimeoutS:              120,
			SkillMDMaxChars:          100000,
			SupportFileMaxBytes:      1048576,
			ViewMaxLinkedFiles:       50,
		},
		Web: WebConfig{
			SearchBackend:        "duckduckgo",
			FetchMaxURLs:         5,
			FetchMaxBytes:        750000,
			BlockPrivateNetworks: true,
			HTTPTimeoutS:         30,
			SearchDefaultResults: 5,
			SearchMaxResults:     10,
		},
		Browser: BrowserConfig{
			IdleCloseMinutes:     10,
			ActionTimeoutS:       30,
			NavigateExcerptChars: 2000,
			SettleS:              0.5,
		},
		Terminal: TerminalConfig{
			DefaultTimeoutS: 120,
			EnvPassthrough:  []string{},
			ProcessKeepS:    3600,
		},
		CodeExecution: CodeExecConfig{
			TimeoutS:       300,
			MaxToolCalls:   50,
			MaxStdoutBytes: 50000,
			MaxStderrChars: 10000,
		},
		Files: FilesConfig{ReadMaxLines: 2000, ReadMaxLineChars: 2000, PatchContextLines: 3},
		Todo:  TodoConfig{MaxItems: 256, MaxItemChars: 4000},
		Delegation: DelegationConfig{
			MaxConcurrentChildren: 5,
			MaxSpawnDepth:         1,
			MaxIterations:         50,
			ChildTimeoutS:         600,
		},
		Approvals: ApprovalsConfig{
			Mode:                "manual",
			TimeoutS:            600,
			CronMode:            "deny",
			SmartTimeoutS:       30,
			SmartScriptMaxChars: 8000,
		},
		Goals: GoalsConfig{
			MaxTurns:                  20,
			JudgeTimeoutS:             30,
			JudgeMaxTokens:            4096,
			JudgeMaxParseFailures:     3,
			JudgeMaxTransportFailures: 5,
			GateTimeoutS:              300,
			GateMaxRetries:            3,
			GateOutputTailChars:       3000,
			MaxBarrierWaitS:           1800,
			DraftObjectiveChars:       4000,
			JudgeChars:                GoalsJudgeChars{Goal: 2000, Response: 4000, Contract: 2500, Subgoal: 2000, Todo: 2000, Process: 120},
		},
		Toolsets: map[string][]string{
			"cli":      {"all"},
			"telegram": {"all"},
			"cron":     {"all"},
			"member":   {"web", "ask", "todo"},
		},
		Cron: CronConfig{
			MaxParallelJobs:       2,
			MissedRuns:            "skip",
			ScriptTimeoutS:        120,
			ScriptOutputMaxChars:  50000,
			ScriptStderrTailLines: 20,
			TickS:                 30,
			GraceMinS:             120,
			GraceMaxS:             7200,
		},
		Gateway: GatewayConfig{
			MaxQueuedMessages:     20,
			DebounceS:             0.35,
			DebounceMaxS:          1.0,
			ModelPageSize:         8,
			SessionPageSize:       8,
			HelpMaxSkills:         10,
			HousekeepingIntervalS: 86400,
			MediaAllowDirs:        []string{},
			MediaTrustRecentFiles: true,
			MediaTrustRecentS:     600,
		},
		Telegram: TelegramConfig{
			AllowedUsers:         []string{},
			GroupAllowedUsers:    []string{},
			GroupAllowedChats:    []string{},
			AllowedChats:         []string{},
			FreeResponseChats:    []string{},
			ExclusiveBotMentions: true,
			MentionPatterns:      []string{},
			AllowedTopics:        []string{},
			IgnoredThreads:       []string{},
			GroupSessionsPerUser: true,
			Streaming: TelegramStreaming{
				Transport:        "auto",
				EditIntervalS:    0.8,
				BufferThreshold:  24,
				Cursor:           " ▉",
				FreshFinalAfterS: 60,
			},
			ToolProgress:            "all",
			APITimeoutS:             60,
			UploadTimeoutS:          300,
			AlbumWaitS:              0.8,
			RichMediaTimeoutS:       120,
			InlineTextMaxBytes:      102400,
			StickerDescribeTimeoutS: 30,
			VoiceConvertTimeoutS:    60,
			MenuMaxCommands:         60,
			StatusIntervalS:         1.5,
			PairingCodeTTLS:         3600,
		},
		MCP: MCPConfig{
			Servers: map[string]MCPServer{},
		},
		ACP: ACPConfig{
			IdleTimeoutS:     300,
			MaxSessions:      2,
			TurnTimeoutS:     1800,
			ResultMaxChars:   16000,
			WorkdirAllowlist: []string{},
			Agents:           map[string]ACPAgent{},
		},
		Media: MediaConfig{
			CacheRetentionDays:    7,
			VisionMaxBytes:        10485760,
			ImageDownloadTimeoutS: 60,
			ImageSize:             "1024x1024",
			TTS: MediaBackend{
				Backend: "command",
				Command: "edge-tts --voice en-US-AriaNeural --text {text} --write-media {out}",
			},
			STT: MediaBackend{
				Backend: "command",
				Command: "whisper-cli -m ~/models/ggml-base.bin -f {in} -otxt -of {out_base}",
			},
		},
		Pricing: PricingConfig{
			Sources:      []string{"openrouter", "litellm"},
			RefreshHours: 24,
			Overrides:    map[string]PriceOverride{},
		},
		Update: UpdateConfig{DownloadTimeoutS: 900, APITimeoutS: 30},
		Doctor: DoctorConfig{CheckTimeoutS: 10, ACPCheckTimeoutS: 30, MinFreeBytes: 1073741824},
		Log: LogConfig{
			Level:     "info",
			MaxSizeMB: 10,
			MaxFiles:  3,
		},
	}
}
