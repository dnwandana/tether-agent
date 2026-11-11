package config

import (
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
	eq(t, len(c.Toolsets), 3)
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
	if g != (GoalsConfig{MaxTurns: 20, JudgeProvider: "", JudgeTimeoutS: 30}) {
		t.Fatalf("%+v", g)
	}
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
