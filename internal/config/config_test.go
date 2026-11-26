package config

import (
	"reflect"
	"strings"
	"testing"
)

// eq fails the test when got is not equal to want.
func eq[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDefaultsMatchSpec(t *testing.T) {
	c := Default()
	eq(t, c.Agent.MaxTurns, 90)
	eq(t, c.Agent.ToolConcurrency, 4)
	eq(t, c.Context.CompactThreshold, 0.50)
	eq(t, c.Context.ProtectFirstN, 3)
	eq(t, c.Context.ProtectLastN, 20)
	eq(t, c.Context.SummaryTargetRatio, 0.20)
	eq(t, c.Context.ToolOutputMaxChars, 50000)
	eq(t, c.Context.SpillRetentionDays, 7)
	eq(t, c.Media.CacheRetentionDays, 7)
	eq(t, c.Memory.MemoryCharLimit, 2200)
	eq(t, c.Memory.UserCharLimit, 1375)
	eq(t, c.Memory.ReviewIntervalTurns, 10)
	eq(t, c.Skills.ReviewIntervalToolRounds, 10)
	eq(t, c.Skills.Guard, true)
	eq(t, c.Web.SearchBackend, "duckduckgo")
	eq(t, c.Web.FetchMaxURLs, 5)
	eq(t, c.Web.FetchMaxBytes, 750000)
	eq(t, c.Web.BlockPrivateNetworks, true)
	eq(t, c.Browser.IdleCloseMinutes, 10)
	eq(t, c.Terminal.DefaultTimeoutS, 120)
	eq(t, c.CodeExecution.TimeoutS, 300)
	eq(t, c.CodeExecution.MaxToolCalls, 50)
	eq(t, c.CodeExecution.MaxStdoutBytes, 50000)
	eq(t, c.Delegation.MaxConcurrentChildren, 5)
	eq(t, c.Delegation.MaxSpawnDepth, 1)
	eq(t, c.Delegation.MaxIterations, 50)
	eq(t, c.Delegation.ChildTimeoutS, 600)
	eq(t, c.Approvals.Mode, "manual")
	eq(t, c.Approvals.TimeoutS, 600)
	eq(t, c.Approvals.CronMode, "deny")
	eq(t, c.Toolsets["telegram"][0], "all")
	eq(t, c.Cron.MaxParallelJobs, 2)
	eq(t, c.Cron.MissedRuns, "skip")
	eq(t, c.Telegram.RequireMention, false)
	eq(t, c.Telegram.ExclusiveBotMentions, true)
	eq(t, c.Telegram.GroupSessionsPerUser, true)
	eq(t, c.Telegram.ThreadSessionsPerUser, false)
	eq(t, c.Telegram.Streaming.Transport, "auto")
	eq(t, c.Telegram.Streaming.EditIntervalS, 0.8)
	eq(t, c.Telegram.Streaming.BufferThreshold, 24)
	eq(t, c.Telegram.Streaming.Cursor, " ▉")
	eq(t, c.Telegram.Streaming.FreshFinalAfterS, 60)
	eq(t, c.Telegram.ToolProgress, "all")
	eq(t, c.Media.TTS.Command, "edge-tts --voice en-US-AriaNeural --text {text} --write-media {out}")
	eq(t, c.Media.STT.Command, "whisper-cli -m ~/models/ggml-base.bin -f {in} -otxt -of {out_base}")
	eq(t, c.Media.Image.Backend, "")
	eq(t, c.Pricing.Sources[0], "openrouter")
	eq(t, c.Pricing.Sources[1], "litellm")
	eq(t, c.Pricing.RefreshHours, 24)
	eq(t, c.Log.Level, "info")
	eq(t, c.Log.MaxSizeMB, 10)
	eq(t, c.Log.MaxFiles, 3)
}

func TestDefaultExtraKeys(t *testing.T) {
	c := Default()
	eq(t, len(c.Toolsets), 5)
	eq(t, c.Toolsets["dashboard"][0], "all")
	eq(t, strings.Join(c.Toolsets["member"], ","), "web,ask,todo")
	eq(t, c.Toolsets["cli"][0], "all")
	eq(t, c.Toolsets["cron"][0], "all")
	eq(t, c.Media.TTS.Backend, "command")
	eq(t, c.Media.STT.Backend, "command")
	eq(t, c.Telegram.Enabled, false)
	eq(t, c.Telegram.CleanupProgress, false)
	eq(t, c.Telegram.AllowAllUsers, false)
	// Hermes keeps 90 days of session history (sessions.retention_days).
	eq(t, c.Sessions.RetentionDays, 90)
	m := DefaultMCPServer()
	eq(t, m.TimeoutS, 120)
	eq(t, m.ConnectTimeoutS, 60)
	eq(t, m.IdleStopMinutes, 10)
	eq(t, m.Enabled, true)
	eq(t, m.AlwaysOn, false)
}

// TestDefaultSlicesAreFresh makes sure that two Default calls do not share
// slices or maps, so a caller can change one copy safely.
func TestDefaultSlicesAreFresh(t *testing.T) {
	a, b := Default(), Default()
	a.Toolsets["cli"][0] = "x"
	a.Pricing.Sources[0] = "x"
	eq(t, b.Toolsets["cli"][0], "all")
	eq(t, b.Pricing.Sources[0], "openrouter")
}

func TestValidateRetentionDays(t *testing.T) {
	c := Default()
	c.Providers["primary"] = Provider{Format: "openai"}
	c.Sessions.RetentionDays = 0
	if err := c.Validate(); err != nil {
		t.Fatalf("0 turns the retention off and is valid: %v", err)
	}
	c.Sessions.RetentionDays = -1
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "sessions.retention_days") {
		t.Fatalf("a negative value must name the key, got %v", err)
	}
}

func TestDefaultRestartKeys(t *testing.T) {
	d := Default()
	if d.Agent.RestartDrainTimeoutS != 180 || d.Agent.ResumeFreshnessS != 3600 {
		t.Fatal(d.Agent)
	}
}

func TestValidateRejectsNegativeRestartKeys(t *testing.T) {
	c := Default()
	c.Providers["primary"] = Provider{Format: "openai"}
	c.Agent.RestartDrainTimeoutS = -5
	c.Agent.ResumeFreshnessS = -1
	err := c.Validate()
	want1 := "config: agent.restart_drain_timeout_s is -5, which is negative. Use 0 to cancel the running turns at once, or a number of seconds"
	want2 := "config: agent.resume_freshness_s is -1, which is negative. Use 0 to turn off the resume, or a number of seconds"
	if err == nil || !strings.Contains(err.Error(), want1) || !strings.Contains(err.Error(), want2) {
		t.Fatal(err)
	}
}

func TestValidateAcceptsZeroRestartKeys(t *testing.T) {
	c := Default()
	c.Providers["primary"] = Provider{Format: "openai"}
	c.Agent.RestartDrainTimeoutS, c.Agent.ResumeFreshnessS = 0, 0
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestGoalsDefaults(t *testing.T) {
	g := Default().Goals
	want := GoalsConfig{
		MaxTurns: 20, JudgeProvider: "", JudgeTimeoutS: 30,
		JudgeMaxTokens: 4096, JudgeMaxParseFailures: 3, JudgeMaxTransportFailures: 5,
		GateTimeoutS: 300, GateMaxRetries: 3, GateOutputTailChars: 3000,
		MaxBarrierWaitS: 1800, DraftObjectiveChars: 4000,
		JudgeChars: GoalsJudgeChars{Goal: 2000, Response: 4000, Contract: 2500, Subgoal: 2000, Todo: 2000, Process: 120},
	}
	if g != want {
		t.Fatalf("%+v", g)
	}
}

// TestNewKeyDefaults checks one new key of each section against the value in the code today.
func TestNewKeyDefaults(t *testing.T) {
	c := Default()
	eq(t, c.Cron.ScriptTimeoutS, 120)
	eq(t, c.Cron.AgentTimeoutS, 0)
	eq(t, c.Cron.ScriptOutputMaxChars, 50000)
	eq(t, c.Cron.ScriptStderrTailLines, 20)
	eq(t, c.Cron.TickS, 30)
	eq(t, c.Cron.GraceMinS, 120)
	eq(t, c.Cron.GraceMaxS, 7200)
	eq(t, c.Cron.RunHistory, 50)
	eq(t, c.Cron.FailStreakLimit, 10)
	eq(t, c.Agent.StreamIdleTimeoutS, 300)
	eq(t, c.Agent.MaxSameToolFailures, 3)
	eq(t, c.Agent.ToolPreviewChars, 40)
	eq(t, c.Review, ReviewConfig{MaxRounds: 16, MaxConcurrent: 2, MinUserTurns: 6})
	eq(t, c.Provider, ProviderConfig{FailureSkipS: 60, AnthropicMaxTokens: 8192, ModelCacheTTLS: 3600,
		ModelListTimeoutS: 10, DefaultContextWindow: 128000})
	eq(t, c.Context.SummaryTimeoutS, 300)
	eq(t, c.Context.SummaryMinTokens, 2000)
	eq(t, c.Context.SummaryMaxTokens, 12000)
	eq(t, c.Context.CompactMaxFailures, 3)
	eq(t, c.Context.CompactMaxIneffective, 2)
	eq(t, c.Context.CompactAtTokens, 200000)
	eq(t, c.Context.CompactNotice, true)
	eq(t, c.Memory.EntryMaxChars, 4000)
	eq(t, c.Memory.LockWaitS, 30)
	eq(t, c.Memory.ReadMaxChars, 32000)
	eq(t, c.Memory.SearchMaxHits, 8)
	eq(t, c.Memory.PromptMaxTopics, 40)
	eq(t, c.Skills.GitTimeoutS, 120)
	eq(t, c.Skills.SkillMDMaxChars, 100000)
	eq(t, c.Skills.SupportFileMaxBytes, 1048576)
	eq(t, c.Skills.ViewMaxLinkedFiles, 50)
	eq(t, c.Web.HTTPTimeoutS, 30)
	eq(t, c.Web.SearchDefaultResults, 5)
	eq(t, c.Web.SearchMaxResults, 10)
	eq(t, c.Browser.ActionTimeoutS, 30)
	eq(t, c.Browser.NavigateExcerptChars, 2000)
	eq(t, c.Browser.SettleS, 0.5)
	eq(t, c.Files, FilesConfig{ReadMaxLines: 2000, ReadMaxLineChars: 2000, PatchContextLines: 3})
	eq(t, c.Todo, TodoConfig{MaxItems: 256, MaxItemChars: 4000})
	eq(t, c.Terminal.ProcessKeepS, 3600)
	eq(t, c.CodeExecution.MaxStderrChars, 10000)
	eq(t, c.ACP.ResultMaxChars, 16000)
	eq(t, c.Approvals.SmartTimeoutS, 30)
	eq(t, c.Approvals.SmartScriptMaxChars, 8000)
	eq(t, DefaultMCPServer().MaxDialTries, 5)
	eq(t, c.Sessions.SearchDefaultResults, 5)
	eq(t, c.Sessions.SearchMaxResults, 20)
	eq(t, c.Sessions.ScrollDefaultWindow, 10)
	eq(t, c.Sessions.ScrollMaxWindow, 50)
	eq(t, c.Media.VisionMaxBytes, 10485760)
	eq(t, c.Media.ImageDownloadTimeoutS, 60)
	eq(t, c.Media.ImageSize, "1024x1024")
	eq(t, c.Media.TTS.TimeoutS, 0)
	eq(t, c.Media.STT.TimeoutS, 0)
	eq(t, c.Media.Image.TimeoutS, 0)
	eq(t, c.Telegram.APITimeoutS, 60)
	eq(t, c.Telegram.UploadTimeoutS, 300)
	eq(t, c.Telegram.AlbumWaitS, 0.8)
	eq(t, c.Telegram.RichMediaTimeoutS, 120)
	eq(t, c.Telegram.InlineTextMaxBytes, 102400)
	eq(t, c.Telegram.StickerDescribeTimeoutS, 30)
	eq(t, c.Telegram.VoiceConvertTimeoutS, 60)
	eq(t, c.Telegram.MenuMaxCommands, 60)
	eq(t, c.Telegram.StatusIntervalS, 1.5)
	eq(t, c.Telegram.PairingCodeTTLS, 3600)
	eq(t, c.Gateway.MaxQueuedMessages, 20)
	eq(t, c.Gateway.DebounceS, 0.35)
	eq(t, c.Gateway.DebounceMaxS, 1.0)
	eq(t, c.Gateway.ModelPageSize, 8)
	eq(t, c.Gateway.SessionPageSize, 8)
	eq(t, c.Gateway.HelpMaxSkills, 10)
	eq(t, c.Gateway.HousekeepingIntervalS, 86400)
	eq(t, c.Gateway.MediaDeliveryStrict, false)
	eq(t, len(c.Gateway.MediaAllowDirs), 0)
	eq(t, c.Gateway.MediaTrustRecentFiles, true)
	eq(t, c.Gateway.MediaTrustRecentS, 600)
	eq(t, c.Update, UpdateConfig{DownloadTimeoutS: 900, APITimeoutS: 30})
	eq(t, c.Doctor, DoctorConfig{CheckTimeoutS: 10, ACPCheckTimeoutS: 30, MinFreeBytes: 1073741824})
}

// TestOldConfigKeepsDefaults loads a file that sets only keys that existed before the new keys.
// Each new key must keep its default, so an old file loads with no change.
func TestOldConfigKeepsDefaults(t *testing.T) {
	body := minimal +
		"agent:\n  max_turns: 50\n" +
		"goals:\n  max_turns: 7\n" +
		"cron:\n  max_parallel_jobs: 3\n" +
		"telegram:\n  streaming:\n    edit_interval_s: 1.2\n" +
		"mcp:\n  servers:\n    fs:\n      command: [fs-mcp]\n"
	c, err := Load(writeTemp(t, body), mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	want.Providers = c.Providers
	want.Agent.MaxTurns = 50
	want.Goals.MaxTurns = 7
	want.Cron.MaxParallelJobs = 3
	want.Telegram.Streaming.EditIntervalS = 1.2
	fs := DefaultMCPServer()
	fs.Command = []string{"fs-mcp"}
	want.MCP.Servers = map[string]MCPServer{"fs": fs}
	if !reflect.DeepEqual(*c, want) {
		t.Fatalf("an old file must keep every new default.\ngot  %+v\nwant %+v", *c, want)
	}
	eq(t, c.MCP.Servers["fs"].MaxDialTries, 5)
}

func TestGoalsFromYAML(t *testing.T) {
	p := writeTemp(t, minimal+"  judge:\n    format: openai\n    base_url: u\n    api_key: k\n    model: m2\n"+
		"goals:\n  max_turns: 5\n  judge_provider: judge\n")
	c, err := Load(p, mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, c.Goals.MaxTurns, 5)
	eq(t, c.Goals.JudgeProvider, "judge")
	eq(t, c.Goals.JudgeTimeoutS, 30) // default kept
}

func TestCompactNoticeFromYAML(t *testing.T) {
	c, err := Load(writeTemp(t, minimal+"context:\n  compact_notice: false\n"), mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, c.Context.CompactNotice, false)
	eq(t, c.Context.CompactMaxIneffective, 2) // default kept
}

func TestValidateGoals(t *testing.T) {
	cases := []struct {
		mut  func(*Config)
		want string
	}{
		{func(c *Config) { c.Goals.MaxTurns = 0 }, "config: goals.max_turns is 0. Use 1 or more turns"},
		{func(c *Config) { c.Goals.JudgeTimeoutS = 0 }, "config: goals.judge_timeout_s is 0. Use 1 or more seconds"},
		{func(c *Config) { c.Goals.JudgeProvider = "nope" }, `config: goals.judge_provider names "nope", which is not in providers`},
	}
	for _, tc := range cases {
		c := Default()
		c.Providers["primary"] = Provider{Format: "openai"}
		tc.mut(&c)
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: got %v", tc.want, err)
		}
	}
	for _, name := range []string{"", "primary", "judge"} {
		c := Default()
		c.Providers["primary"] = Provider{Format: "openai"}
		c.Providers["judge"] = Provider{Format: "anthropic"}
		c.Goals.JudgeProvider = name
		if err := c.Validate(); err != nil {
			t.Fatalf("judge_provider %q must be valid: %v", name, err)
		}
	}
}

func TestDashboardDefaults(t *testing.T) {
	d := Default().Dashboard
	eq(t, d.Enabled, false)
	eq(t, d.Listen, "127.0.0.1:8787")
	eq(t, d.PublicURL, "")
	eq(t, d.SessionHours, 24)
	eq(t, d.MaxUploadMB, 20)
	eq(t, d.TLSCert, "")
	eq(t, d.TLSKey, "")
	eq(t, len(d.TrustedProxies), 0)
	eq(t, len(d.AllowedHosts), 0)
	if err := d.Check(); err != nil {
		t.Fatal("the default must pass the check:", err)
	}
}

func TestDashboardCheck(t *testing.T) {
	const wild = "is a wildcard address. Set dashboard.public_url or dashboard.allowed_hosts, because no browser sends this host"
	for _, c := range []struct {
		name string
		edit func(d *DashboardConfig)
		want string
	}{
		{"no port", func(d *DashboardConfig) { d.Listen = "8787" }, `dashboard.listen "8787" is not a host:port address`},
		{"bad port", func(d *DashboardConfig) { d.Listen = "127.0.0.1:http" },
			`dashboard.listen "127.0.0.1:http" has no port number from 0 to 65535`},
		{"big port", func(d *DashboardConfig) { d.Listen = "127.0.0.1:70000" },
			`dashboard.listen "127.0.0.1:70000" has no port number from 0 to 65535`},
		{"wildcard v4", func(d *DashboardConfig) { d.Listen = "0.0.0.0:8787" }, "dashboard.listen 0.0.0.0:8787 " + wild},
		{"wildcard v6", func(d *DashboardConfig) { d.Listen = "[::]:8787" }, "dashboard.listen [::]:8787 " + wild},
		{"wildcard empty", func(d *DashboardConfig) { d.Listen = ":8787" }, "dashboard.listen :8787 " + wild},
		{"public url no scheme", func(d *DashboardConfig) { d.PublicURL = "agent.example.com" },
			`dashboard.public_url "agent.example.com" must be an http or https URL with a host and no path`},
		{"public url path", func(d *DashboardConfig) { d.PublicURL = "https://agent.example.com/tether" },
			`dashboard.public_url "https://agent.example.com/tether" must be an http or https URL with a host and no path`},
		{"hours 0", func(d *DashboardConfig) { d.SessionHours = 0 }, "dashboard.session_hours must be from 1 to 720, not 0"},
		{"hours 721", func(d *DashboardConfig) { d.SessionHours = 721 }, "dashboard.session_hours must be from 1 to 720, not 721"},
		{"upload 0", func(d *DashboardConfig) { d.MaxUploadMB = 0 }, "dashboard.max_upload_mb must be 1 or more, not 0"},
		{"cert only", func(d *DashboardConfig) { d.TLSCert = "/c.pem" }, "dashboard.tls_cert and dashboard.tls_key must be set together"},
		{"key only", func(d *DashboardConfig) { d.TLSKey = "/k.pem" }, "dashboard.tls_cert and dashboard.tls_key must be set together"},
		{"proxy star", func(d *DashboardConfig) { d.TrustedProxies = []string{"*"} },
			`dashboard.trusted_proxies: "*" is not permitted. Name each proxy address`},
		{"proxy all v4", func(d *DashboardConfig) { d.TrustedProxies = []string{"10.0.0.1", "0.0.0.0/0"} },
			`dashboard.trusted_proxies: "0.0.0.0/0" is not permitted. Name each proxy address`},
		{"proxy all v6", func(d *DashboardConfig) { d.TrustedProxies = []string{"::/0"} },
			`dashboard.trusted_proxies: "::/0" is not permitted. Name each proxy address`},
		{"proxy name", func(d *DashboardConfig) { d.TrustedProxies = []string{"proxy.lan"} },
			`dashboard.trusted_proxies: "proxy.lan" is not an IP address or a CIDR range`},
	} {
		d := Default().Dashboard
		c.edit(&d)
		if err := d.Check(); err == nil || err.Error() != c.want {
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
	for name, edit := range map[string]func(d *DashboardConfig){
		"wildcard and public url": func(d *DashboardConfig) {
			d.Listen, d.PublicURL = "0.0.0.0:8787", "https://agent.example.com/"
		},
		"wildcard and hosts": func(d *DashboardConfig) {
			d.Listen, d.AllowedHosts = ":8787", []string{"agent.tail1234.ts.net"}
		},
		"vpn address":  func(d *DashboardConfig) { d.Listen = "10.8.0.1:8787" },
		"random port":  func(d *DashboardConfig) { d.Listen = "127.0.0.1:0" },
		"tls":          func(d *DashboardConfig) { d.TLSCert, d.TLSKey = "/c.pem", "/k.pem" },
		"proxies":      func(d *DashboardConfig) { d.TrustedProxies = []string{"10.0.0.1", "192.168.0.0/16", "::1"} },
		"hours limits": func(d *DashboardConfig) { d.SessionHours = 720 },
		"hours one":    func(d *DashboardConfig) { d.SessionHours = 1 },
	} {
		d := Default().Dashboard
		edit(&d)
		if err := d.Check(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDashboardOriginAndTLS(t *testing.T) {
	d := Default().Dashboard
	eq(t, d.TLS(), false)
	eq(t, d.Origin(), "http://127.0.0.1:8787")
	d.TLSCert = "/c.pem"
	eq(t, d.TLS(), false)
	d.TLSKey = "/k.pem"
	eq(t, d.TLS(), true)
	eq(t, d.Origin(), "https://127.0.0.1:8787")
	d.PublicURL = "https://agent.example.com/"
	eq(t, d.Origin(), "https://agent.example.com")
}

// A bad dashboard value is never a load error. The gateway checks it and runs without the listener.
func TestDashboardBadValueLoads(t *testing.T) {
	c, err := Load(writeTemp(t, minimal+"dashboard:\n  enabled: true\n  listen: nonsense\n  session_hours: 0\n"+
		"  trusted_proxies: ['*']\n  allowed_hosts: [agent.lan]\n"), mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, c.Dashboard.Enabled, true)
	eq(t, c.Dashboard.Listen, "nonsense")
	eq(t, c.Dashboard.AllowedHosts[0], "agent.lan")
	eq(t, c.Dashboard.MaxUploadMB, 20) // default kept
	if c.Dashboard.Check() == nil {
		t.Fatal("the check must refuse the values")
	}
}

func TestLearningDefaults(t *testing.T) {
	c := Default()
	eq(t, c.Memory.RulesCharLimit, 1000)
	eq(t, c.Skills.ReviewMaxChanges, 3)
	eq(t, c.Skills.HistoryKeep, 5)
	eq(t, c.Skills.Curator, CuratorConfig{Enabled: true, IntervalHours: 168, MinIdleHours: 2,
		StaleAfterDays: 14, ArchiveAfterDays: 30})
}

// A value of 0 or less uses the default. An archive time under the stale time uses the stale time.
func TestLearningZeroUsesDefault(t *testing.T) {
	c, err := Load(writeTemp(t, minimal+"memory:\n  rules_char_limit: 0\n"+
		"skills:\n  review_max_changes: -1\n  history_keep: 0\n"+
		"  curator:\n    enabled: false\n    interval_hours: 0\n    min_idle_hours: -5\n"+
		"    stale_after_days: 20\n    archive_after_days: 10\n"), mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, c.Memory.RulesCharLimit, 1000)
	eq(t, c.Skills.ReviewMaxChanges, 3)
	eq(t, c.Skills.HistoryKeep, 5)
	eq(t, c.Skills.Curator, CuratorConfig{Enabled: false, IntervalHours: 168, MinIdleHours: 2,
		StaleAfterDays: 20, ArchiveAfterDays: 20})
}

func TestLearningValuesFromYAML(t *testing.T) {
	c, err := Load(writeTemp(t, minimal+"memory:\n  rules_char_limit: 1500\n"+
		"skills:\n  review_max_changes: 5\n  history_keep: 2\n"+
		"  curator:\n    interval_hours: 24\n    stale_after_days: 7\n    archive_after_days: 60\n"), mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, c.Memory.RulesCharLimit, 1500)
	eq(t, c.Skills.ReviewMaxChanges, 5)
	eq(t, c.Skills.HistoryKeep, 2)
	eq(t, c.Skills.Curator, CuratorConfig{Enabled: true, IntervalHours: 24, MinIdleHours: 2,
		StaleAfterDays: 7, ArchiveAfterDays: 60})
}
