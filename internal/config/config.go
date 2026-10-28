// Package config holds the Tether Agent configuration schema and its defaults.
//
// Default is the only place in the code base that sets a default value. The
// yaml tag of each field is the key name in config.yaml. The docs test reads
// these tags with reflection and compares them with docs/CONFIG.md, so keep them exact.
package config

import "gopkg.in/yaml.v3"

// Config is the full content of config.yaml.
type Config struct {
	Providers     map[string]Provider `yaml:"providers"`
	Agent         AgentConfig         `yaml:"agent"`
	Context       ContextConfig       `yaml:"context"`
	Sessions      SessionsConfig      `yaml:"sessions"`
	Memory        MemoryConfig        `yaml:"memory"`
	Skills        SkillsConfig        `yaml:"skills"`
	Web           WebConfig           `yaml:"web"`
	Browser       BrowserConfig       `yaml:"browser"`
	Terminal      TerminalConfig      `yaml:"terminal"`
	CodeExecution CodeExecConfig      `yaml:"code_execution"`
	Delegation    DelegationConfig    `yaml:"delegation"`
	Approvals     ApprovalsConfig     `yaml:"approvals"`
	Toolsets      map[string][]string `yaml:"toolsets"`
	Cron          CronConfig          `yaml:"cron"`
	Telegram      TelegramConfig      `yaml:"telegram"`
	MCP           MCPConfig           `yaml:"mcp"`
	Media         MediaConfig         `yaml:"media"`
	Pricing       PricingConfig       `yaml:"pricing"`
	Log           LogConfig           `yaml:"log"`
}

// Provider is one named model endpoint.
type Provider struct {
	Format        string `yaml:"format"`
	BaseURL       string `yaml:"base_url"`
	APIKey        string `yaml:"api_key"`
	Model         string `yaml:"model"`
	FlatRate      bool   `yaml:"flat_rate"`
	Vision        bool   `yaml:"vision"`
	ContextWindow int    `yaml:"context_window"`
	PriceKey      string `yaml:"price_key"`
}

// AgentConfig sets the limits of the agent loop and the providers for vision and delegation.
type AgentConfig struct {
	MaxTurns           int    `yaml:"max_turns"`
	ToolConcurrency    int    `yaml:"tool_concurrency"`
	VisionProvider     string `yaml:"vision_provider"`
	DelegationProvider string `yaml:"delegation_provider"`
}

// ContextConfig sets the compaction and the tool output limits.
type ContextConfig struct {
	CompactThreshold   float64 `yaml:"compact_threshold"`
	ProtectFirstN      int     `yaml:"protect_first_n"`
	ProtectLastN       int     `yaml:"protect_last_n"`
	SummaryTargetRatio float64 `yaml:"summary_target_ratio"`
	ToolOutputMaxChars int     `yaml:"tool_output_max_chars"`
	SpillRetentionDays int     `yaml:"spill_retention_days"`
}

// SessionsConfig sets the retention of the sessions in state.db.
type SessionsConfig struct {
	// RetentionDays is the age, by last activity, after which the gateway deletes a
	// cron, subagent, or review session. 0 keeps them all. Chat sessions always stay.
	RetentionDays int `yaml:"retention_days"`
}

// MemoryConfig sets the memory limits and the review interval.
type MemoryConfig struct {
	MemoryCharLimit     int `yaml:"memory_char_limit"`
	UserCharLimit       int `yaml:"user_char_limit"`
	ReviewIntervalTurns int `yaml:"review_interval_turns"`
}

// SkillsConfig sets the skill folders, the review interval, and the guard.
type SkillsConfig struct {
	ExternalDirs             []string `yaml:"external_dirs"`
	ReviewIntervalToolRounds int      `yaml:"review_interval_tool_rounds"`
	Guard                    bool     `yaml:"guard"`
}

// WebConfig sets the web_search and web_fetch tools.
type WebConfig struct {
	SearchBackend        string `yaml:"search_backend"`
	FetchMaxURLs         int    `yaml:"fetch_max_urls"`
	FetchMaxBytes        int    `yaml:"fetch_max_bytes"`
	BlockPrivateNetworks bool   `yaml:"block_private_networks"`
}

// BrowserConfig sets the Chrome manager.
type BrowserConfig struct {
	ChromePath       string `yaml:"chrome_path"`
	IdleCloseMinutes int    `yaml:"idle_close_minutes"`
}

// TerminalConfig sets the terminal tool.
type TerminalConfig struct {
	DefaultTimeoutS int      `yaml:"default_timeout_s"`
	EnvPassthrough  []string `yaml:"env_passthrough"`
}

// CodeExecConfig sets the execute_code tool limits.
type CodeExecConfig struct {
	TimeoutS       int `yaml:"timeout_s"`
	MaxToolCalls   int `yaml:"max_tool_calls"`
	MaxStdoutBytes int `yaml:"max_stdout_bytes"`
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
}

// CronConfig sets the cron scheduler.
type CronConfig struct {
	MaxParallelJobs int    `yaml:"max_parallel_jobs"`
	MissedRuns      string `yaml:"missed_runs"`
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
}

// MediaBackend selects a command template or an OpenAI-format provider.
// An empty Backend hides the tool.
type MediaBackend struct {
	Backend  string `yaml:"backend"`
	Command  string `yaml:"command"`
	Provider string `yaml:"provider"`
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
		Agent: AgentConfig{
			MaxTurns:        40,
			ToolConcurrency: 4,
		},
		Context: ContextConfig{
			CompactThreshold:   0.50,
			ProtectFirstN:      3,
			ProtectLastN:       20,
			SummaryTargetRatio: 0.20,
			ToolOutputMaxChars: 50000,
			SpillRetentionDays: 7,
		},
		Sessions: SessionsConfig{
			RetentionDays: 90,
		},
		Memory: MemoryConfig{
			MemoryCharLimit:     2200,
			UserCharLimit:       1375,
			ReviewIntervalTurns: 10,
		},
		Skills: SkillsConfig{
			ExternalDirs:             []string{},
			ReviewIntervalToolRounds: 10,
			Guard:                    true,
		},
		Web: WebConfig{
			SearchBackend:        "duckduckgo",
			FetchMaxURLs:         5,
			FetchMaxBytes:        750000,
			BlockPrivateNetworks: true,
		},
		Browser: BrowserConfig{
			IdleCloseMinutes: 10,
		},
		Terminal: TerminalConfig{
			DefaultTimeoutS: 120,
			EnvPassthrough:  []string{},
		},
		CodeExecution: CodeExecConfig{
			TimeoutS:       300,
			MaxToolCalls:   50,
			MaxStdoutBytes: 50000,
		},
		Delegation: DelegationConfig{
			MaxConcurrentChildren: 5,
			MaxSpawnDepth:         1,
			MaxIterations:         50,
			ChildTimeoutS:         600,
		},
		Approvals: ApprovalsConfig{
			Mode:     "manual",
			TimeoutS: 600,
			CronMode: "deny",
		},
		Toolsets: map[string][]string{
			"cli":      {"all"},
			"telegram": {"all"},
			"cron":     {"all"},
		},
		Cron: CronConfig{
			MaxParallelJobs: 2,
			MissedRuns:      "skip",
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
			ToolProgress: "all",
		},
		MCP: MCPConfig{
			Servers: map[string]MCPServer{},
		},
		Media: MediaConfig{
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
		Log: LogConfig{
			Level:     "info",
			MaxSizeMB: 10,
			MaxFiles:  3,
		},
	}
}
