package app

import (
	"context"
	"os"
	"os/exec"
	"runtime/debug"
	"sort"
	"strconv"
	"time"

	"github.com/dnwandana/tether-agent/internal/acp"
	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/dashboard"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/media"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/skills"
	"github.com/dnwandana/tether-agent/internal/telegram"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// bootFileBytes is the largest file of one upload, in bytes.
const bootFileBytes = 20 << 20

// dashboardRoutes builds the page APIs, the bootstrap route, and the file links of the
// dashboard. It builds one Tasks value for the skills, system, and operations pages, so one
// task of each kind runs at a time. It does no network work.
func (a *App) dashboardRoutes(ch *dashboard.Channel, f DashboardFuncs) []dashboard.Routes {
	redact := a.Redactor.Redact
	now := time.Now
	tasks := dashboard.NewTasks(ch.Hub(), ch.WorkCtx)
	files := dashboard.NewFiles(a.bootMediaPolicy())
	ch.SetFiles(files)
	devices := dashboard.NewStore(a.DB)

	deps := dashboard.Deps{Cfg: a.Cfg.Dashboard, DB: a.DB, Sessions: a.Store, Redact: redact, Log: a.Log}
	ffmpeg, _ := exec.LookPath("ffmpeg")
	up := dashboard.UploadDeps{
		MediaDir:        a.Agent.MediaInbox,
		Limits:          media.Limits{MaxBytes: bootFileBytes, InlineTextMax: a.Cfg.Telegram.InlineTextMaxBytes},
		FFmpeg:          ffmpeg,
		MaxRequestBytes: int64(a.Cfg.Dashboard.MaxUploadMB) << 20,
	}
	if a.Media != nil {
		up.STT = a.Media.TranscribeFunc()
	}

	jobs := dashboard.JobsDeps{
		Jobs: a.Cron, Held: &cron.HeldStore{DB: a.DB}, Goals: &goals.Store{DB: a.DB}, Sessions: a.Store,
		DB: a.DB, TelegramOn: a.Telegram != nil, PostHeld: a.sendHeld, EditCard: a.editHeldCard,
		Publish: ch.Hub().Publish, Redact: redact, Now: now, Log: a.Log,
	}
	if t, ok := a.Registry.Get("cronjob"); ok {
		jobs.Tool = t.Run
	}

	usage := dashboard.UsageDeps{DB: a.DB, Sessions: a.Store, Cron: a.Cron, Cfg: a.Cfg, Now: now}
	if a.Set != nil && a.Set.Health != nil {
		usage.Health = a.Set.Health.Snapshot
	}
	usage.Fallback = func() provider.FallbackStatus {
		st, _ := provider.StatusOf(a.Prov)
		return st
	}

	sys := dashboard.SystemSources{Doctor: f.Doctor, Tail: nil}
	if a.MCP != nil {
		sys.MCP = a.MCP.ServerStatus
	}
	if a.Batches != nil {
		sys.Batches = func(ctx context.Context, limit int) ([]agent.BatchInfo, error) {
			return a.Batches.List(ctx, limit)
		}
	}
	if a.ACP != nil {
		sys.ACP = func() []acp.SessionInfo { return a.ACP.Sessions() }
	}
	if a.Procs != nil {
		sys.Procs = func() []tools.ProcInfo { return a.Procs.All() }
		sys.Tail = a.Procs.Tail
	}
	sys.Browser = func() bool { return a.Browser != nil && a.Browser.Running() }

	ops := dashboard.OpsOptions{
		DataDir:     a.DataDir,
		ConfigPath:  a.cfgPath,
		Version:     a.bootVersion(),
		DotEnv:      func() (map[string]string, error) { return a.DotEnv.Snapshot(), nil },
		Redact:      redact,
		Tasks:       tasks,
		UpdateCheck: f.UpdateCheck,
		UpdateRun:   f.UpdateRun,
	}

	routes := []dashboard.Routes{
		dashboard.NewBootstrap(dashboard.BootstrapDeps{
			Commands: a.bootCommands, Skills: a.bootSkills, Version: a.bootVersion(),
			MaxUploadMB: a.Cfg.Dashboard.MaxUploadMB, TLS: a.Cfg.Dashboard.TLS(),
		}),
		dashboard.NewCommandAPI(ch, redact),
		dashboard.NewMessages(ch, up),
		files,
		&dashboard.SessionsAPI{D: deps, Files: files, ChatTitle: a.bootChatTitle},
		dashboard.NewTasksAPI(tasks),
		dashboard.NewJobsAPI(jobs),
		dashboard.NewGoalsAPI(&goals.Store{DB: a.DB}, a.Store, redact, now),
		dashboard.NewSecurityAPI(a.Gate, a.secretsDeps(), a.bootSecurityFixed(), devices, now),
		dashboard.NewSkillsAPI(a.DataDir, a.Cfg, ch.Cards(), tasks, dashboard.CronSkillUsers(a.Cron)),
		dashboard.NewUsageAPI(usage),
		dashboard.NewSystemAPI(sys, tasks, redact),
		dashboard.NewOpsAPI(ops),
		dashboard.NewAccessAPI(f.Pairing, f.SetupState),
	}
	if a.Memory != nil {
		routes = append(routes, dashboard.NewMemoryAPI(a.Memory, redact))
	}
	return routes
}

// bootMediaPolicy returns the media policy of Telegram, or a new one from the same config
// when Telegram is off.
func (a *App) bootMediaPolicy() *media.Policy {
	if a.Telegram != nil && a.Telegram.Media != nil {
		return a.Telegram.Media
	}
	return media.NewPolicy(media.PolicyConfig{
		DataDir:      a.DataDir,
		Lookup:       security.EnvLookup(a.DotEnv.Snapshot()),
		Log:          a.Log,
		Strict:       a.Cfg.Gateway.MediaDeliveryStrict,
		AllowDirs:    a.Cfg.Gateway.MediaAllowDirs,
		TrustRecent:  a.Cfg.Gateway.MediaTrustRecentFiles,
		TrustRecentS: a.Cfg.Gateway.MediaTrustRecentS,
	})
}

// bootVersion returns Options.Version. Without it, it returns the version of the build info,
// or "dev".
func (a *App) bootVersion() string {
	if a.version != "" {
		return a.version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

// bootCommands lists the slash commands of the palette, sorted by name. /approve and
// /deny are local commands of the dashboard cards.
func (a *App) bootCommands() []dashboard.CommandRow {
	all := gateway.Commands()
	rows := make([]dashboard.CommandRow, 0, len(all)+2)
	for name, c := range all {
		rows = append(rows, dashboard.CommandRow{Name: "/" + name, Description: c.Help})
	}
	for _, r := range []dashboard.CommandRow{
		{Name: "/approve", Description: "Allow the open approval: /approve [session|always] [all]"},
		{Name: "/deny", Description: "Deny the open approval: /deny [all]"},
	} {
		dup := false
		for _, e := range rows {
			if e.Name == r.Name {
				dup = true
			}
		}
		if !dup {
			rows = append(rows, r)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

// bootSkills lists one /<slug> row for each skill.
func (a *App) bootSkills() []dashboard.CommandRow {
	rows := []dashboard.CommandRow{}
	if a.Skills == nil {
		return rows
	}
	list, _, err := a.Skills.Load()
	if err != nil {
		return rows
	}
	for _, s := range list {
		if slug := skills.CommandSlug(s.Name); slug != "" {
			rows = append(rows, dashboard.CommandRow{Name: "/" + slug, Description: s.Description})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

// bootChatTitle reads the title cache of the Telegram chats. It makes no getChat call.
func (a *App) bootChatTitle(chatID, _ string) (chat, topic string, ok bool) {
	if a.Telegram == nil || a.titles == nil {
		return "", "", false
	}
	id, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return "", "", false
	}
	c, found := a.titles.cached(id)
	if !found {
		return "", "", false
	}
	switch {
	case c.Title != "":
		return c.Title, "", true
	case c.FirstName != "":
		return c.FirstName, "", true
	case c.Username != "":
		return c.Username, "", true
	}
	return "", "", false
}

// cached returns the chat from the cache only. It never calls getChat.
func (c *chatTitles) cached(id int64) (chat telegram.Chat, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[id]
	return e.chat, ok
}

// bootSecurityFixed builds the view-only lists of the Security page from the running config.
func (a *App) bootSecurityFixed() dashboard.SecurityFixed {
	home, _ := os.UserHomeDir()
	fx := dashboard.SecurityFixed{
		ProtectedPaths: security.ProtectedPaths(a.DataDir, home),
		HumanOnly:      a.Cfg.Approvals.HumanOnly,
		MCPClasses:     map[string]map[string]int{},
		ACPPresets:     config.ACPPresets,
		ACPWorkdirs:    a.Cfg.ACP.WorkdirAllowlist,
		Toolsets:       a.Cfg.Toolsets,
	}
	if a.MCP != nil {
		for _, s := range a.MCP.ServerStatus() {
			fx.MCPClasses[s.Name] = s.Classes
		}
	}
	return fx
}
