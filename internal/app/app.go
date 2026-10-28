// Package app is the composition root. It loads the config, opens the log and the
// database, builds the providers, registers the tools, and gives the channels their parts.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/browser"
	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/ctxmgr"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/mcp"
	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/obs"
	"github.com/dnwandana/tether-agent/internal/pricing"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/skills"
	"github.com/dnwandana/tether-agent/internal/telegram"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// dataDirs are the folders that New creates in the data dir.
var dataDirs = []string{
	"cache/spill", "cache/media", "cache/screenshots", "logs", "memory", "skills", "scripts", "exports",
}

// CLISink writes the live output of a CLI turn. Tool lines are dim ANSI text.
type CLISink = gateway.CLISink

// Options sets how New starts the app.
type Options struct {
	// DataDir is the data folder. It is required.
	DataDir string
	// ConfigPath is the config file. Empty uses <DataDir>/config.yaml.
	ConfigPath string
	// LogStderr sends the logs to LogWriter instead of <DataDir>/logs/tether.log.
	LogStderr bool
	// LogLevel replaces log.level of the config when it is not empty.
	LogLevel string
	// LogWriter receives the logs with LogStderr. Nil uses os.Stderr.
	LogWriter io.Writer
	// Gateway builds the cron scheduler and the background delegation batches. Only the
	// gateway role sets it.
	Gateway bool
	// TelegramBaseURL replaces the Bot API root. Empty uses the real Bot API. Tests set it.
	TelegramBaseURL string
}

// App holds the shared parts of one process.
type App struct {
	Cfg      *config.Config
	DataDir  string
	DB       *db.DB
	Store    *sessions.Store
	Agent    *agent.Agent
	Registry *tools.Registry
	Log      *slog.Logger
	Redactor *security.Redactor
	DotEnv   map[string]string
	// HTTP is the shared client of the providers and the price sources.
	HTTP *http.Client
	// WebClient is the guarded client of the web tools.
	WebClient *http.Client
	// Prov is the default provider. Providers holds every provider entry by name.
	Prov      provider.Provider
	Providers map[string]provider.Provider
	Prices    *pricing.Service
	Memory    *memory.Store
	Skills    *tools.SkillCatalog
	Cron      *cron.Store
	// Browser is the Chrome manager. It starts Chrome on the first browser call.
	Browser *browser.Manager
	// Media builds the media tools. The speech-to-text of Telegram voice notes uses it too.
	Media *tools.MediaSet
	// WorkDir is the working folder of the tools: the folder where the process started.
	WorkDir string
	// Gate is the approval gate of the terminal tool.
	Gate *security.Gate
	// CLIAsk is the approval prompt and the clarify prompt of the CLI channel.
	CLIAsk *gateway.CLIAsk
	// MCP starts the MCP servers on demand.
	MCP     *mcp.Manager
	mcpStop context.CancelFunc
	// mcpReaper is closed when the idle reaper of MCP returns.
	mcpReaper chan struct{}
	// Sched is the cron scheduler. It is nil outside the gateway role.
	Sched *cron.Scheduler
	// Telegram is the Telegram channel. AddTelegram sets it in the gateway role.
	Telegram *telegram.Channel
	// Delegator runs the children of delegate_task.
	Delegator *agent.Delegator
	// Batches runs the background delegation batches. It is nil outside the gateway role,
	// because only the gateway router can deliver the results as a new turn.
	Batches *agent.Batches

	telegramBaseURL string

	logFile io.Closer
	gwOnce  sync.Once
	gw      *gateway.Gateway
	// values holds the Env.Values map of each session, so that the map stays the same
	// across turns.
	valuesMu sync.Mutex
	values   map[string]map[string]any
	// clock gives the time of the session retention. Nil uses time.Now. Tests set it.
	clock func() time.Time
}

// New builds the app. It loads .env and the config, opens the log and the database,
// builds the providers, registers the tools, and creates the folders of dataDirs. It
// starts no Chrome. It starts an MCP server only in two cases: the server has no tool
// list cache, or its always_on is true. New lists the tools of the first type and then
// stops it. It keeps the second type running.
func New(ctx context.Context, opts Options) (_ *App, err error) {
	if opts.DataDir == "" {
		return nil, errors.New("app: the data folder is not set")
	}
	dir := opts.DataDir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("app: cannot make the data folder %s: %w", dir, err)
	}
	a := &App{DataDir: dir, values: map[string]map[string]any{}, telegramBaseURL: opts.TelegramBaseURL}
	defer func() {
		if err != nil {
			a.Close()
		}
	}()
	if a.DotEnv, err = security.LoadDotEnv(filepath.Join(dir, ".env")); err != nil {
		return nil, err
	}
	cfgPath := opts.ConfigPath
	if cfgPath == "" {
		cfgPath = filepath.Join(dir, "config.yaml")
	}
	if a.Cfg, err = config.Load(cfgPath, security.EnvLookup(a.DotEnv)); err != nil {
		return nil, err
	}
	for _, d := range dataDirs {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			return nil, fmt.Errorf("app: cannot make the folder %s: %w", d, err)
		}
	}
	a.Redactor = NewRedactor(a.DotEnv)
	if err := a.openLog(opts); err != nil {
		return nil, err
	}
	if a.WorkDir, err = os.Getwd(); err != nil {
		a.WorkDir = dir
	}

	skillsDir := filepath.Join(dir, "skills")
	if copied, err := skills.CopyMissing(skillsDir); err != nil {
		a.Log.Warn("bootstrap skills not copied", "dir", skillsDir, "err", err.Error())
	} else if len(copied) > 0 {
		a.Log.Info("bootstrap skills copied", "names", copied)
	}
	home, _ := os.UserHomeDir()
	// Call it one time with the full list, because each call replaces the rules.
	security.AddProtectedPathPatterns(security.ProtectedPaths(dir, home))

	if a.DB, err = db.Open(filepath.Join(dir, "state.db")); err != nil {
		return nil, err
	}
	a.Store = sessions.New(a.DB)
	a.Store.Redactor = a.Redactor

	a.HTTP = &http.Client{}
	if a.Prov, a.Providers, err = provider.FromConfig(a.Cfg, a.HTTP, a.Log); err != nil {
		return nil, err
	}
	a.Gate = security.NewGate(a.Cfg.Approvals, a.DB)
	a.Gate.SudoPasswordSet = a.DotEnv["SUDO_PASSWORD"] != "" || os.Getenv("SUDO_PASSWORD") != ""
	// The judge runs only in smart mode.
	a.Gate.Judge = security.ProviderJudge{Prov: a.Prov}
	a.Gate.Log = a.Log
	// CLI.Run attaches the prompt to the REPL input. With no CLI, a question gets no answer.
	a.CLIAsk = &gateway.CLIAsk{}
	a.Prices = pricing.NewService(a.Cfg, filepath.Join(dir, "cache"), a.HTTP, a.DB, a.Log)
	if a.Memory, err = memory.Open(filepath.Join(dir, "memory"), a.Cfg.Memory.MemoryCharLimit, a.Cfg.Memory.UserCharLimit); err != nil {
		return nil, err
	}
	a.Skills = &tools.SkillCatalog{Dir: skillsDir, External: a.Cfg.Skills.ExternalDirs, Guard: a.Cfg.Skills.Guard}
	mcpServers := make([]string, 0, len(a.Cfg.MCP.Servers))
	for name := range a.Cfg.MCP.Servers {
		mcpServers = append(mcpServers, name)
	}
	a.Cron = &cron.Store{DB: a.DB, Loc: time.Local, ValidToolset: func(s string) bool { return tools.ValidToolset(s, mcpServers) }}
	if opts.Gateway {
		a.Sched = a.newScheduler()
	}

	a.Registry = tools.NewRegistry()
	a.registerTools()
	a.startMCP(ctx, cfgPath)

	a.Agent = &agent.Agent{
		Prov:      a.Prov,
		Providers: a.Providers,
		Tools:     a.Registry,
		Store:     a.Store,
		DB:        a.DB,
		Cfg:       a.Cfg,
		Log:       a.Log,
		SpillDir:  filepath.Join(dir, "cache", "spill"),
		Redactor:  a.Redactor,
		Prices:    a.Prices,
		Memory:    a.Memory,
		Skills:    a.Skills,
		WorkDir:   a.WorkDir,
		Compactor: &ctxmgr.Compactor{Store: a.Store, DB: a.DB, Cfg: a.Cfg.Context, Log: a.Log},
	}
	// The frozen system prompt of each session. A /reload-mcp changes the MCP
	// generation, so the prompt is rebuilt.
	a.Agent.MCPGen = a.MCP.Generation
	a.Agent.Prompts = agent.NewPromptCache(a.Agent.SessionPrompt)
	// A compaction starts a review of the part that the summary replaces.
	a.Agent.Compactor.OnCompact = a.Agent.ReviewOnCompaction
	// The chat type and the topic of a Telegram session go into its system prompt.
	a.Agent.ChatInfo = telegram.ChatInfo(a.Cfg.Telegram, telegram.User{})
	a.registerDelegation(opts.Gateway)
	return a, nil
}

// registerDelegation registers delegate_task over the agent. Only the gateway
// role runs background batches: the router puts their results on the parent key as a new
// turn. In the chat role, background: true returns an error, and the model runs the
// tasks in the foreground.
func (a *App) registerDelegation(gatewayRole bool) {
	a.Delegator = &agent.Delegator{Agent: a.Agent}
	if gatewayRole {
		a.Batches = &agent.Batches{
			DB:  a.DB,
			Log: a.Log,
			Enqueue: func(ctx context.Context, key, text string) error {
				return a.Gateway().Enqueue(ctx, key, text)
			},
			// RecoverLost reports only the batches of the channels of this process.
			Owns: func(key string) bool {
				src, _, _ := strings.Cut(key, ":")
				_, ok := a.Gateway().Channels[src]
				return ok
			},
		}
		a.Delegator.Batches = a.Batches
	}
	d := a.Cfg.Delegation
	tool := tools.DelegateTool{
		Runner:        a.Delegator,
		MaxConcurrent: d.MaxConcurrentChildren,
		MaxDepth:      d.MaxSpawnDepth,
		MaxIterations: d.MaxIterations,
		TimeoutS:      d.ChildTimeoutS,
	}
	if a.Batches != nil {
		tool.Background = a.Delegator
	}
	a.RegisterTool(tool)
}

// newScheduler builds the cron scheduler of the gateway role. AddTelegram sets
// the send function and the home channel of its deliverer.
func (a *App) newScheduler() *cron.Scheduler {
	cfg := a.Cfg
	return &cron.Scheduler{
		Store: a.Cron,
		Runner: &cron.Runner{
			Store:      a.Store,
			ScriptsDir: filepath.Join(a.DataDir, "scripts"),
			Turn:       a.cronTurn,
			Skills:     a.Skills.Load,
			Env:        security.ChildEnv(os.Environ(), a.DotEnv, cfg.Terminal.EnvPassthrough),
			SpillDir:   filepath.Join(a.DataDir, "cache", "spill"),
			Log:        a.Log,
		},
		Deliver:     &cron.Deliverer{Redact: a.Redactor.Redact, Log: a.Log},
		MaxParallel: cfg.Cron.MaxParallelJobs,
		Missed:      cfg.Cron.MissedRuns,
		Log:         a.Log,
	}
}

// cronTurn runs the agent turn of a cron job. The kind "cron" makes the gate apply
// approvals.cron_mode. Env.Ask stays nil, so no user gets a question.
func (a *App) cronTurn(ctx context.Context, t cron.CronTurn) (string, error) {
	env := a.NewEnv(t.Session, "cron")
	env.Kind = "cron"
	if t.Workdir != "" {
		env.WorkDir = t.Workdir
	}
	// Each run has a new session, so free its per-session state after the turn.
	defer a.ForgetSession(t.Session.ID)
	res, err := a.Agent.RunTurn(ctx, agent.Turn{
		Session:     t.Session,
		UserText:    t.Text,
		Toolsets:    tools.Resolve(a.Cfg.Toolsets["cron"], t.Toolsets, nil),
		RemoveTools: t.RemoveTools,
		Env:         env,
	})
	return res.Final, err
}

// AddTelegram checks the bot token with getMe, builds the Telegram channel, and adds it to
// the gateway. It also gives the cron deliverer its send function and its home channel.
// Only the gateway role calls it: the chat role never polls Telegram.
func (a *App) AddTelegram(ctx context.Context) (*telegram.Channel, error) {
	cfg := a.Cfg.Telegram
	if strings.TrimSpace(cfg.BotToken) == "" {
		return nil, errors.New("telegram: telegram.enabled is true, but no bot token is set. " +
			"Set telegram.bot_token in config.yaml, or set TELEGRAM_BOT_TOKEN in .env")
	}
	c := &telegram.Client{Token: cfg.BotToken, BaseURL: a.telegramBaseURL, Log: a.Log}
	me, err := c.GetMe(ctx)
	if err != nil {
		return nil, fmt.Errorf("telegram: the bot token check (getMe) failed, so check telegram.bot_token: %w", err)
	}
	ffmpeg, _ := exec.LookPath("ffmpeg")
	// Static stickers go to the vision model. Nil Describe gives the fallback note.
	var describe func(ctx context.Context, path, prompt string) (string, error)
	if vp, ok := tools.VisionProvider(a.Cfg, a.Providers); ok {
		describe = func(ctx context.Context, path, prompt string) (string, error) {
			return tools.DescribeImage(ctx, vp, path, prompt)
		}
	}
	ch := &telegram.Channel{
		Cfg:  cfg,
		C:    c,
		DB:   a.DB,
		Pair: &telegram.Pairing{DB: a.DB},
		Inbox: &telegram.Inbox{
			C:        c,
			MediaDir: filepath.Join(a.DataDir, "cache", "media"),
			// Voice notes go to the speech-to-text backend.
			Transcribe: a.Media.TranscribeFunc(),
			Describe:   describe,
			Stickers:   &telegram.KVStickers{DB: a.DB, Log: a.Log},
			Log:        a.Log,
		},
		Out: &telegram.Sender{C: c, ConvertVoice: telegram.FFmpegToOpus(ffmpeg)},
		// Media sets the local files that an answer can send (a port of Hermes
		// validate_media_delivery_path). A nil policy sends no file.
		Media: telegram.NewMediaPolicy(a.DataDir, security.EnvLookup(a.DotEnv), a.Log),
		Bot:   me,
		Log:   a.Log,
		// The approval message shows the wait of the gate. Zero config means the gate default.
		UI: &telegram.Interactions{C: c, Log: a.Log, ApprovalTimeout: approvalWait(a.Cfg.Approvals.TimeoutS)},
	}
	if a.Cron != nil {
		// The migration log reads the cron jobs only when a chat moves, not at start.
		ch.CronChats = func(ctx context.Context) ([]string, error) {
			jobs, err := a.Cron.List(ctx)
			if err != nil {
				return nil, err
			}
			return cronChatIDs(jobs), nil
		}
	}
	a.Telegram = ch
	a.Agent.ChatInfo = telegram.ChatInfo(cfg, me)
	a.Gateway().Channels[ch.Name()] = ch
	if a.Sched != nil {
		a.Sched.Deliver.Send = func(ctx context.Context, to cron.Target, text string, files []string) error {
			return ch.Send(ctx, gateway.Target{Source: "telegram", ChatID: to.ChatID, ThreadID: to.ThreadID},
				gateway.Outbound{Text: text, Files: files})
		}
		a.Sched.Deliver.Home = func(ctx context.Context, forCron bool) (cron.Target, bool) {
			t, ok := telegram.HomeTarget(ctx, a.DB, a.Cfg.Telegram, forCron)
			return cron.Target(t), ok
		}
	}
	a.Log.Info("telegram bot checked", "bot", me.Username)
	return ch, nil
}

// cronChatIDs returns the Telegram chat ids of jobs. A deliver of "telegram:<chat>[:<thread>]"
// gives <chat>. A deliver of "" or "origin" gives the origin chat of a Telegram job.
func cronChatIDs(jobs []cron.Job) []string {
	var ids []string
	for _, j := range jobs {
		switch {
		case strings.HasPrefix(j.Deliver, "telegram:"):
			chat, _, _ := strings.Cut(strings.TrimPrefix(j.Deliver, "telegram:"), ":")
			if chat != "" {
				ids = append(ids, chat)
			}
		case (j.Deliver == "" || j.Deliver == "origin") && j.Origin.Source == "telegram" && j.Origin.ChatID != "":
			ids = append(ids, j.Origin.ChatID)
		}
	}
	return ids
}

// mcpReapEvery is the time between two checks for idle MCP servers. Tests change it.
var mcpReapEvery = time.Minute

// startMCP makes the MCP manager, registers the MCP tools from the tool list cache, adds
// /reload-mcp, and starts the idle reaper. A server that fails to load only logs a warning.
func (a *App) startMCP(ctx context.Context, cfgPath string) {
	a.MCP = &mcp.Manager{
		Servers:  a.Cfg.MCP.Servers,
		CacheDir: filepath.Join(a.DataDir, "cache", "mcp-tools"),
		Log:      a.Log,
		BaseEnv:  os.Environ(),
		DotEnv:   a.DotEnv,
		LoadServers: func() (map[string]config.MCPServer, error) {
			c, err := config.Load(cfgPath, security.EnvLookup(a.DotEnv))
			if err != nil {
				return nil, err
			}
			return c.MCP.Servers, nil
		},
	}
	if err := a.MCP.Init(ctx, a.Registry); err != nil {
		a.Log.Warn("mcp: some servers did not load, so their tools are missing", "err", err.Error())
	}
	gateway.Register(gateway.ReloadMCPCommand(a.MCP))
	mctx, stop := context.WithCancel(context.WithoutCancel(ctx))
	a.mcpStop = stop
	// The goroutine keeps its own copy of the manager, because Close sets a.MCP to nil.
	m, done := a.MCP, make(chan struct{})
	a.mcpReaper = done
	go func() {
		defer close(done)
		t := time.NewTicker(mcpReapEvery)
		defer t.Stop()
		for {
			select {
			case <-mctx.Done():
				return
			case <-t.C:
				m.ReapIdle()
			}
		}
	}()
}

// openLog opens the rotating log file, or LogWriter with LogStderr.
func (a *App) openLog(opts Options) error {
	var w io.Writer = opts.LogWriter
	if w == nil {
		w = os.Stderr
	}
	if !opts.LogStderr {
		rw, err := obs.NewRotatingWriter(filepath.Join(a.DataDir, "logs", "tether.log"), a.Cfg.Log.MaxSizeMB, a.Cfg.Log.MaxFiles)
		if err != nil {
			return err
		}
		a.logFile = rw
		w = rw
	}
	level := a.Cfg.Log.Level
	if opts.LogLevel != "" {
		level = opts.LogLevel
	}
	a.Log = obs.NewLogger(w, level, a.Redactor)
	return nil
}

// registerTools registers the built-in tools that need no channel. registerDelegation
// adds delegate_task.
func (a *App) registerTools() {
	cfg := a.Cfg
	spill := filepath.Join(a.DataDir, "cache", "spill")
	a.WebClient = tools.GuardedClient(cfg.Web.BlockPrivateNetworks, 30*time.Second, 3)
	a.RegisterTool(&tools.TerminalTool{
		DefaultTimeout: time.Duration(cfg.Terminal.DefaultTimeoutS) * time.Second,
		DotEnv:         a.DotEnv,
		// The skill RequiredEnv names of the session reach the child too.
		Passthrough: func(env *tools.Env) []string {
			return append(slices.Clone(cfg.Terminal.EnvPassthrough), tools.PassthroughFor(env)...)
		},
		SpillDir: spill,
	})
	a.RegisterTool(tools.ReadFileTool{})
	// With Protected nil, the file tools refuse the protected paths of env.DataDir.
	a.RegisterTool(tools.WriteFileTool{})
	a.RegisterTool(tools.PatchTool{})
	a.RegisterTool(&tools.WebFetchTool{Client: a.WebClient, MaxURLs: cfg.Web.FetchMaxURLs, MaxBytes: int64(cfg.Web.FetchMaxBytes)})
	a.RegisterTool(&tools.WebSearchTool{Client: a.WebClient})
	a.RegisterTool(tools.MemoryTool{Store: a.Memory})
	a.RegisterTool(tools.ClarifyTool{Timeout: time.Duration(cfg.Approvals.TimeoutS) * time.Second})
	a.RegisterTool(tools.SessionSearchTool{Store: a.Store, MaxChars: cfg.Context.ToolOutputMaxChars})
	a.RegisterTool(tools.SkillViewTool{Catalog: a.Skills})
	a.RegisterTool(tools.SkillsListTool{Catalog: a.Skills})
	a.RegisterTool(tools.SkillManageTool{Mgr: &skills.Manager{Dir: a.Skills.Dir, Guard: cfg.Skills.Guard}, Catalog: a.Skills})
	cfgBrowser := cfg.Browser
	a.Browser = &browser.Manager{
		ProfileDir: filepath.Join(a.DataDir, "cache", "chrome-profile"),
		IdleClose:  time.Duration(cfgBrowser.IdleCloseMinutes) * time.Minute,
		Log:        a.Log,
	}
	// New does not look for Chrome. The first browser call does.
	tools.RegisterBrowserTools(a.Registry, &tools.BrowserTools{
		M:       a.Browser,
		ShotDir: filepath.Join(a.DataDir, "cache", "screenshots"),
		DetectErr: sync.OnceValue(func() error {
			p, err := browser.DetectChrome(cfgBrowser.ChromePath, exec.LookPath, os.Getenv, runtime.GOOS)
			if err != nil {
				return err
			}
			a.Browser.ChromePath = p
			return nil
		}),
	})
	a.Media = &tools.MediaSet{
		Cfg:       cfg.Media,
		Providers: cfg.Providers,
		Dir:       filepath.Join(a.DataDir, "cache", "media"),
		HC:        a.HTTP,
		// An image URL in an endpoint response comes from the network, so the client is guarded.
		Download: tools.GuardedClient(true, 60*time.Second, 3),
		Env:      security.ChildEnv(os.Environ(), a.DotEnv, cfg.Terminal.EnvPassthrough),
		Log:      a.Log,
	}
	for _, t := range a.Media.Tools() {
		a.RegisterTool(t)
	}
	// The chat role has no scheduler: run makes the job due, and the gateway runs it.
	var runNow func(context.Context, string) error
	if a.Sched != nil {
		runNow = a.Sched.RunNow
	}
	a.RegisterTool(tools.CronJobTool{
		Store:           a.Cron,
		RunNow:          runNow,
		CurrentProvider: a.currentProvider,
		Sessions:        a.Store,
		ScriptsDir:      filepath.Join(a.DataDir, "scripts"),
		MissedRuns:      cfg.Cron.MissedRuns,
	})
	// view_image exists only when a vision provider exists.
	if vp, ok := tools.VisionProvider(cfg, a.Providers); ok {
		a.RegisterTool(tools.VisionTool{Prov: vp, Client: a.WebClient, MaxBytes: 10 << 20})
	}
	// execute_code needs python3. Without it, the tool is hidden and doctor reports it.
	if python, err := exec.LookPath("python3"); err == nil {
		a.RegisterTool(tools.ExecuteCodeTool{
			Reg:         a.Registry,
			ScratchRoot: filepath.Join(a.DataDir, "cache"),
			TimeoutS:    cfg.CodeExecution.TimeoutS,
			MaxCalls:    cfg.CodeExecution.MaxToolCalls,
			MaxStdout:   cfg.CodeExecution.MaxStdoutBytes,
			Python:      python,
			DotEnv:      a.DotEnv,
			Passthrough: func(env *tools.Env) []string {
				return append(slices.Clone(cfg.Terminal.EnvPassthrough), tools.PassthroughFor(env)...)
			},
		})
	}
}

// RegisterTool adds t to the registry.
func (a *App) RegisterTool(t tools.Tool) { a.Registry.Register(t) }

// currentProvider returns the provider name of the model override of the session, else
// "primary".
func (a *App) currentProvider(env *tools.Env) string {
	if env == nil || env.SessionID == "" {
		return "primary"
	}
	s, err := a.Store.Get(context.Background(), env.SessionID)
	if err != nil || s.ModelOverride == "" {
		return "primary"
	}
	name, _, _ := strings.Cut(s.ModelOverride, ":")
	if _, ok := a.Providers[name]; !ok {
		return "primary"
	}
	return name
}

// NewEnv returns the tool context of a session on a channel. The Values map is the same
// for a session across turns, because the read tracker of the file tools lives there.
// The terminal tool checks each command with the approval gate. On the CLI, the gate and
// the clarify tool ask on the terminal. On Telegram, they ask with buttons in the chat of the
// session. Other channels, for example cron, have no prompt, so the gate blocks each
// dangerous command that needs a question.
func (a *App) NewEnv(sess *sessions.Session, channel string) *tools.Env {
	env := &tools.Env{
		Channel: channel,
		WorkDir: a.WorkDir,
		DataDir: a.DataDir,
		Log:     a.Log,
	}
	gate := tools.GateApprover{Gate: a.Gate}
	switch {
	case channel == "cli":
		if a.CLIAsk != nil {
			gate.Prompter = a.CLIAsk
			env.Ask = a.CLIAsk
		}
		// gateway.Deps.Turn sends the "🔀 [i/n] tool" lines of the delegate_task children
		// through the CLI sink.
	case channel == "telegram" && sess != nil && a.Telegram != nil:
		ch := a.Telegram
		if chat, err := strconv.ParseInt(sess.ChatID, 10, 64); err == nil && ch.UI != nil {
			ui := ch.UI.For(chat, sess.ThreadID)
			gate.Prompter = ui
			env.Ask = ui
		}
		key := sess.Key
		env.Progress = func(line string) { ch.Progress(key, line) }
	}
	env.Gate = gate
	if sess == nil {
		env.Values = map[string]any{}
		return env
	}
	env.SessionID, env.SessionKey, env.Kind = sess.ID, sess.Key, sess.Kind
	a.valuesMu.Lock()
	v, ok := a.values[sess.ID]
	if !ok {
		v = map[string]any{}
		a.values[sess.ID] = v
	}
	a.valuesMu.Unlock()
	env.Values = v
	return env
}

// Deps returns the parts that the channels and the slash commands use.
func (a *App) Deps() *gateway.Deps {
	return &gateway.Deps{
		Cfg:       a.Cfg,
		Store:     a.Store,
		Agent:     a.Agent,
		Providers: a.Providers,
		Log:       a.Log,
		NewEnv:    a.NewEnv,
		CLIAsk:    a.CLIAsk,
		// /new and /reset start a review of the session that they replace, and
		// then free the state of that session.
		BeforeNew: a.beforeNew,
		// /delete frees the state of the session that it removes.
		AfterDelete: a.ForgetSession,
		// /stop, /new, and /reset cancel the background delegation children of the key.
		CancelKey: a.cancelKey,
		ToolLine:  func(tool, preview string) string { return telegram.ToolLine("all", tool, preview, "") },
	}
}

// beforeNew starts the review of the session that /new or /reset replaces. The review
// takes the prompt and the tool context of the session before the call returns, so the
// state of the session can go after it.
func (a *App) beforeNew(ctx context.Context, old *sessions.Session) {
	a.Agent.ReviewBeforeNew(ctx, old)
	a.ForgetSession(old.ID)
}

// ForgetSession frees the state that the process keeps for a session: the Values map of
// its tool context, the "This session" answers of the approval gate, and the frozen
// system prompt. Call it when a session ends: after /new, /reset, /delete, and a cron run.
func (a *App) ForgetSession(sessionID string) {
	a.valuesMu.Lock()
	delete(a.values, sessionID)
	a.valuesMu.Unlock()
	if a.Gate != nil {
		a.Gate.ResetSession(sessionID)
	}
	if a.Agent != nil && a.Agent.Prompts != nil {
		a.Agent.Prompts.Forget(sessionID)
	}
}

// cancelKey cancels the background delegation children of key.
func (a *App) cancelKey(key string) {
	if a.Batches != nil {
		a.Batches.CancelKey(key)
	}
}

// Gateway returns the gateway router of the process. Each call returns the same router.
// The channels are added by the gateway role.
func (a *App) Gateway() *gateway.Gateway {
	a.gwOnce.Do(func() {
		a.gw = &gateway.Gateway{Deps: a.Deps(), Channels: map[string]gateway.Channel{}, Log: a.Log}
	})
	return a.gw
}

// housekeepingEvery is the time between two housekeeping runs.
const housekeepingEvery = 24 * time.Hour

// Housekeeping removes the spill files that are older than context.spill_retention_days,
// and the automatic sessions that are older than sessions.retention_days. It runs at once,
// and then each day in a goroutine until ctx ends.
func (a *App) Housekeeping(ctx context.Context) {
	a.cleanSpill()
	a.pruneSessions(ctx)
	// A restart loses the running background children. Each parent key gets a notice turn.
	if a.Batches != nil {
		if err := a.Batches.RecoverLost(ctx); err != nil {
			a.Log.Warn("housekeeping: cannot report all lost background delegations", "err", err.Error())
		}
	}
	go func() {
		t := time.NewTicker(housekeepingEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.cleanSpill()
				a.pruneSessions(ctx)
			}
		}
	}()
}

// pruneSessions deletes the cron, subagent, and review sessions whose last activity is
// older than sessions.retention_days, and frees their state in the process. 0 keeps all.
func (a *App) pruneSessions(ctx context.Context) {
	days := a.Cfg.Sessions.RetentionDays
	if days <= 0 || a.Store == nil {
		return
	}
	now := time.Now
	if a.clock != nil {
		now = a.clock
	}
	ids, err := a.Store.PruneAutomatic(ctx, now().Add(-time.Duration(days)*24*time.Hour))
	for _, id := range ids {
		a.ForgetSession(id)
	}
	if err != nil {
		a.Log.Warn("housekeeping: cannot delete all old automatic sessions; the next run tries again", "err", err.Error())
	}
	if len(ids) > 0 {
		a.Log.Info("housekeeping: old automatic sessions deleted", "count", len(ids), "retention_days", days)
	}
}

// cleanSpill removes the old spill files and logs the result.
func (a *App) cleanSpill() {
	days := a.Cfg.Context.SpillRetentionDays
	if days <= 0 {
		days = config.Default().Context.SpillRetentionDays
	}
	dir := filepath.Join(a.DataDir, "cache", "spill")
	n, err := ctxmgr.CleanupSpill(dir, time.Duration(days)*24*time.Hour, time.Now())
	if err != nil {
		a.Log.Warn("housekeeping: cannot remove all old spill files", "dir", dir, "err", err.Error())
	}
	if n > 0 {
		a.Log.Info("housekeeping: old spill files removed", "count", n)
	}
}

// Close stops Chrome and the MCP servers, and closes the database and the log file.
func (a *App) Close() error {
	var errs []error
	if a.mcpStop != nil {
		a.mcpStop()
		a.mcpStop = nil
	}
	// The reaper must return before the servers close.
	if a.mcpReaper != nil {
		<-a.mcpReaper
		a.mcpReaper = nil
	}
	if a.MCP != nil {
		a.MCP.Close()
		a.MCP = nil
	}
	if a.Browser != nil {
		a.Browser.Close()
		a.Browser = nil
	}
	if a.DB != nil {
		errs = append(errs, a.DB.Close())
		a.DB = nil
	}
	if a.logFile != nil {
		errs = append(errs, a.logFile.Close())
		a.logFile = nil
	}
	return errors.Join(errs...)
}

// approvalWait returns the time that the approval gate waits for an answer. It uses the
// same default as security.Gate when timeoutS is zero or less.
func approvalWait(timeoutS int) time.Duration {
	if timeoutS <= 0 {
		return security.DefaultApprovalTimeout
	}
	return time.Duration(timeoutS) * time.Second
}

// NewRedactor returns a redactor that masks the .env values and the values of the secret
// variables in the process environment.
func NewRedactor(dotenv map[string]string) *security.Redactor {
	var vals []string
	for _, v := range dotenv {
		vals = append(vals, v)
	}
	for _, kv := range os.Environ() {
		if name, v, ok := strings.Cut(kv, "="); ok && security.IsSecretName(name, dotenv) {
			vals = append(vals, v)
		}
	}
	return security.NewRedactor(vals)
}
