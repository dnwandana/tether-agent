// Package app is the composition root. It loads the config, opens the log and the
// database, builds the providers, registers the tools, and gives the channels their parts.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dnwandana/tether-agent/internal/acp"
	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/browser"
	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/ctxmgr"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/goals"
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
	// Version is the version of the binary. The coding agents get it in clientInfo. Empty uses "dev".
	Version string
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
	// Models keeps the model lists for /model. It makes no call until the first
	// /model <entry>.
	Models *provider.ModelCache
	// WebClient is the guarded client of the web tools.
	WebClient *http.Client
	// Set holds the adapters, the shared Health, and the default chain.
	Set *provider.Set
	// Prov is the default chain. Providers holds every provider entry by name, with
	// "primary" set to the default chain.
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
	// ACP runs the coding agents of acp_delegate. It is nil when acp.enabled is false or
	// no agent is enabled.
	ACP *acp.Manager
	// closeACP closes the coding agent sessions of a Tether session for good. It is nil
	// with no ACP manager. Tests replace it.
	closeACP func(sessionID string)
	acpStop  context.CancelFunc
	// acpReaper is closed when the idle reaper of ACP returns.
	acpReaper chan struct{}
	// Sched is the cron scheduler. It is nil outside the gateway role.
	Sched *cron.Scheduler
	// Telegram is the Telegram channel. AddTelegram sets it in the gateway role.
	Telegram *telegram.Channel
	// Delegator runs the children of delegate_task.
	Delegator *agent.Delegator
	// Batches runs the background delegation batches. It is nil outside the gateway role,
	// because only the gateway router can deliver the results as a new turn.
	Batches *agent.Batches
	// Goals keeps the standing goal of each session and runs the step after each turn.
	Goals *goals.Manager
	// Procs records the background processes of the terminal tool. A process exit wakes a
	// goal that waits on it.
	Procs *tools.ProcRegistry

	telegramBaseURL string
	// terminal is the registered terminal tool. The goal gates run through it.
	terminal *tools.TerminalTool
	// recordUsage records the usage of a model call outside a turn in an aux session. An
	// error logs a warning and never fails the call.
	recordUsage provider.RecordUsage
	// gatewayRole is true when New got Options.Gateway.
	gatewayRole bool
	// cliWaker is the goal waker of the CLI that AttachCLI attached. It stays nil in the
	// gateway role.
	cliWaker atomic.Pointer[gateway.GoalWaker]

	logFile io.Closer
	gwOnce  sync.Once
	gw      *gateway.Gateway
	// values holds the Env.Values map of each session, so that the map stays the same
	// across turns.
	valuesMu sync.Mutex
	values   map[string]map[string]any
	// clock gives the time of the session retention. Nil uses time.Now. Tests set it.
	clock func() time.Time
	// restarts carries the stop request of /restart to the gateway loop.
	restarts chan StopRequest
	// stopping is true after the first stop request.
	stopping atomic.Bool
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
	a := &App{DataDir: dir, values: map[string]map[string]any{}, telegramBaseURL: opts.TelegramBaseURL,
		restarts: make(chan StopRequest, 1)}
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
	a.tightenDataModes(dir)
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
	a.tightenDBModes(dir, false)
	a.Store = sessions.New(a.DB)
	a.Store.Redactor = a.Redactor

	a.HTTP = &http.Client{}
	a.Models = provider.NewModelCache(a.HTTP)
	a.Models.TTL = time.Duration(a.Cfg.Provider.ModelCacheTTLS) * time.Second
	a.Models.ListTimeout = time.Duration(a.Cfg.Provider.ModelListTimeoutS) * time.Second
	if a.Set, err = provider.FromConfig(a.Cfg, a.HTTP, a.Log); err != nil {
		return nil, err
	}
	a.Prov, a.Providers = a.Set.Default, a.Set.All
	for _, name := range slices.Sorted(maps.Keys(a.Cfg.Providers)) {
		if reason := a.Cfg.Providers[name].Unusable; reason != "" {
			a.Log.Warn("provider entry is not usable", "name", name, "reason", reason)
		}
	}
	aux := &sessions.AuxLog{
		Store: a.Store,
		// a.Agent is nil here, so the closure reads it at call time.
		Meta: func(p provider.Provider, o provider.Result) sessions.MessageMeta { return a.Agent.Meta(p, o) },
	}
	a.recordUsage = func(ctx context.Context, sessionID, task string, p provider.Provider, o provider.Result) {
		if err := aux.Record(ctx, sessionID, task, p, o); err != nil {
			a.Log.Warn("usage: cannot record the usage of the side call", "task", task, "session", sessionID, "err", err.Error())
		}
	}
	a.Gate = security.NewGate(a.Cfg.Approvals, a.DB)
	a.Gate.SudoPasswordSet = a.DotEnv["SUDO_PASSWORD"] != "" || os.Getenv("SUDO_PASSWORD") != ""
	// The judge runs only in smart mode.
	a.Gate.Judge = security.ProviderJudge{
		Prov:    a.Prov,
		Record:  a.recordUsage,
		Timeout: time.Duration(a.Cfg.Approvals.SmartTimeoutS) * time.Second,
	}
	a.Gate.SmartScriptMaxChars = a.Cfg.Approvals.SmartScriptMaxChars
	a.Gate.Log = a.Log
	a.Gate.InGateway = opts.Gateway
	a.Gate.ScriptsDir = filepath.Join(dir, "scripts")
	// The command reads and removes the Always answers of the gate of this process.
	gateway.Register(gateway.ApprovalsCommand(a.Gate))
	// CLI.Run attaches the prompt to the REPL input. With no CLI, a question gets no answer.
	a.CLIAsk = &gateway.CLIAsk{}
	a.Prices = pricing.NewService(a.Cfg, filepath.Join(dir, "cache"), a.HTTP, a.DB, a.Log)
	if a.Memory, err = memory.Open(filepath.Join(dir, "memory"), a.Cfg.Memory.MemoryCharLimit, a.Cfg.Memory.UserCharLimit); err != nil {
		return nil, err
	}
	a.Memory.EntryMaxChars = a.Cfg.Memory.EntryMaxChars
	a.Memory.LockWait = time.Duration(a.Cfg.Memory.LockWaitS) * time.Second
	a.Memory.ReadMaxChars = a.Cfg.Memory.ReadMaxChars
	a.Memory.SearchMaxHits = a.Cfg.Memory.SearchMaxHits
	a.Skills = &tools.SkillCatalog{Dir: skillsDir, External: a.Cfg.Skills.ExternalDirs, Guard: a.Cfg.Skills.Guard}
	mcpServers := make([]string, 0, len(a.Cfg.MCP.Servers))
	for name := range a.Cfg.MCP.Servers {
		mcpServers = append(mcpServers, name)
	}
	// The config package cannot check the names, because the toolset names are in tools.
	for _, key := range slices.Sorted(maps.Keys(a.Cfg.Toolsets)) {
		for _, name := range a.Cfg.Toolsets[key] {
			if !tools.ValidToolset(name, mcpServers) {
				return nil, fmt.Errorf("config: toolsets.%s: unknown toolset %q", key, name)
			}
		}
	}
	a.Cron = &cron.Store{DB: a.DB, Loc: time.Local, ValidToolset: func(s string) bool { return tools.ValidToolset(s, mcpServers) }}
	if opts.Gateway {
		a.Sched = a.newScheduler()
	}

	a.Registry = tools.NewRegistry()
	a.gatewayRole = opts.Gateway
	a.Procs = tools.NewProcRegistry()
	a.Procs.Keep = time.Duration(a.Cfg.Terminal.ProcessKeepS) * time.Second
	a.registerTools(opts.Gateway)
	a.startMCP(ctx, cfgPath)
	if opts.Gateway {
		// Only the gateway can restart itself. The chat role has no /restart.
		drain := time.Duration(a.Cfg.Agent.RestartDrainTimeoutS) * time.Second
		gateway.Register(gateway.RestartCommand(a.requestRestart, drain))
	}

	a.Agent = &agent.Agent{
		Prov:       a.Prov,
		Providers:  a.Providers,
		Chains:     a.Set,
		Tools:      a.Registry,
		Store:      a.Store,
		DB:         a.DB,
		Cfg:        a.Cfg,
		Log:        a.Log,
		SpillDir:   filepath.Join(dir, "cache", "spill"),
		Redactor:   a.Redactor,
		Prices:     a.Prices,
		Memory:     a.Memory,
		Skills:     a.Skills,
		WorkDir:    a.WorkDir,
		MediaInbox: filepath.Join(dir, "cache", "media"),
		Compactor:  &ctxmgr.Compactor{Store: a.Store, DB: a.DB, Cfg: a.Cfg.Context, Log: a.Log},
	}
	// The frozen system prompt of each session. A /reload-mcp changes the MCP
	// generation, so the prompt is rebuilt.
	a.Agent.MCPGen = a.MCP.Generation
	a.Agent.Prompts = agent.NewPromptCache(a.Agent.SessionPrompt)
	// A compaction starts a review of the part that the summary replaces.
	a.Agent.Compactor.OnCompact = a.Agent.ReviewOnCompaction
	// The compaction summary records its usage in an aux session.
	a.Agent.Compactor.Record = a.recordUsage
	// The chat type and the topic of a Telegram session go into its system prompt.
	a.Agent.ChatInfo = telegram.ChatInfo(a.Cfg.Telegram, telegram.User{})
	a.startACP(ctx, opts.Version)
	a.registerDelegation(opts.Gateway)
	// The open items of the todo list go to the end of a compaction summary.
	a.Agent.Compactor.Todo = a.todoSnapshot
	a.Goals = goals.NewManager(a.goalDeps())
	// A process exit can lift the barrier of a goal that waits on that process.
	a.Procs.OnExit(func(sessionID string, _ int) { a.wakeGoal(context.Background(), sessionID) })
	return a, nil
}

// goalDeps returns the deps of the goal manager. Only the gateway role runs background
// batches, so the chat role counts no batch.
func (a *App) goalDeps() goals.Deps {
	d := goals.Deps{
		Store: &goals.Store{
			DB:             a.DB,
			GateTimeoutS:   a.Cfg.Goals.GateTimeoutS,
			GateMaxRetries: a.Cfg.Goals.GateMaxRetries,
		},
		// A goal retry waits as long as the chain skips a failed entry.
		RetryDelay:  time.Duration(a.Cfg.Provider.FailureSkipS) * time.Second,
		Sessions:    a.Store,
		Cfg:         func() config.GoalsConfig { return a.Cfg.Goals },
		ProviderFor: a.Agent.ProviderFor,
		ChainFor:    func(entry string) provider.Provider { return a.Set.ChainFor(entry, "") },
		Meta:        a.Agent.Meta,
		Procs:       a.Procs,
		Exec:        a.goalExec,
		TodoOpen:    a.todoOpen,
		Alive:       goals.PIDAlive,
		Now:         time.Now,
		Log:         a.Log,

		InGateway:       a.Gate.InGateway,
		SudoPasswordSet: a.Gate.SudoPasswordSet,
	}
	if a.gatewayRole {
		d.RunningBatches = a.Batches.Running
	}
	return d
}

// goalExec runs a gate command of the goal of sess through the terminal tool. The gate gets
// the folder of the process and the child environment with no .env secrets.
func (a *App) goalExec(ctx context.Context, sess *sessions.Session, command string, timeout time.Duration) (string, int, error) {
	return a.terminal.Exec(ctx, a.NewEnv(sess, sess.Source), command, "", timeout)
}

// todoItems returns the todo list of a session. A load error logs a warning and gives no
// items.
func (a *App) todoItems(ctx context.Context, sessionID string) []tools.TodoItem {
	values, err := a.todoValues(ctx, sessionID)
	if err != nil {
		a.Log.Warn("todo: cannot read the session of the task list", "session", sessionID, "err", err.Error())
		return nil
	}
	// Set the limits before the restore, because a restore can come before the first
	// todo_list call of the process.
	tools.TodoStoreWith(values, a.todoLimits())
	items, err := tools.LoadTodos(ctx, a.Store, values, sessionID)
	if err != nil {
		a.Log.Warn("todo: cannot restore the task list from the history", "session", sessionID, "err", err.Error())
		return nil
	}
	return items
}

// todoLimits returns the limits of the todo list from todo.max_items and todo.max_item_chars.
func (a *App) todoLimits() tools.TodoLimits {
	return tools.TodoLimits{MaxItems: a.Cfg.Todo.MaxItems, MaxChars: a.Cfg.Todo.MaxItemChars}
}

// todoValues returns the Values map that holds the todo list of a session. A delegate_task
// child keeps its list in the Values map of its own run, not in a.values. Thus a child gets a
// new empty map each time: the load reads the newest stored result and adds no a.values entry.
func (a *App) todoValues(ctx context.Context, sessionID string) (map[string]any, error) {
	a.valuesMu.Lock()
	v, ok := a.values[sessionID]
	a.valuesMu.Unlock()
	if ok {
		return v, nil
	}
	sess, err := a.Store.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if sess.Kind == agent.KindSubagent {
		return map[string]any{}, nil
	}
	return a.sessionValues(sessionID), nil
}

// todoSnapshot returns the open items of the todo list for a compaction summary, or "".
func (a *App) todoSnapshot(ctx context.Context, sessionID string) string {
	return tools.FormatTodoSnapshot(a.todoItems(ctx, sessionID))
}

// todoOpen returns the open items of the todo list for the goal judge, or "".
func (a *App) todoOpen(ctx context.Context, sessionID string) string {
	return tools.FormatOpenTodos(a.todoItems(ctx, sessionID), 2000)
}

// wakeGoal checks the barrier of the goal of sessionID after an event, for example a
// process exit or a batch result. The gateway role uses the waker of the gateway. The chat
// role uses the waker of the attached CLI, and does nothing when no CLI is attached.
func (a *App) wakeGoal(ctx context.Context, sessionID string) {
	if a.gatewayRole {
		a.Gateway().GoalWaker().Wake(ctx, sessionID)
		return
	}
	if w := a.cliWaker.Load(); w != nil {
		w.Wake(ctx, sessionID)
	}
}

// AttachCLI keeps the goal waker of c, so that an event wakes a goal of the CLI. The app does
// not build the CLI.
func (a *App) AttachCLI(c *gateway.CLI) {
	a.cliWaker.Store(c.GoalWaker())
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
			Enqueue: func(ctx context.Context, key, text string, owner bool) error {
				if err := a.Gateway().EnqueueAs(ctx, key, text, owner); err != nil {
					return err
				}
				// A batch result can lift the barrier of a goal that waits on the batches.
				if sess, err := a.Store.Active(ctx, key); err == nil && sess != nil {
					a.wakeGoal(ctx, sess.ID)
				}
				return nil
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
	// The tool exists only with a manager. A nil *acp.Manager in the interface is not nil.
	if a.ACP != nil {
		a.RegisterTool(tools.NewACPDelegateTool(a.Cfg.ACP, a.ACP, approvalWait(a.Cfg.Approvals.TimeoutS)))
	}
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
			Script: cron.ScriptLimits{
				Timeout:         time.Duration(cfg.Cron.ScriptTimeoutS) * time.Second,
				OutputMaxChars:  cfg.Cron.ScriptOutputMaxChars,
				StderrTailLines: cfg.Cron.ScriptStderrTailLines,
			},
			AgentTimeout: time.Duration(cfg.Cron.AgentTimeoutS) * time.Second,
		},
		Tick:        time.Duration(cfg.Cron.TickS) * time.Second,
		GraceMin:    time.Duration(cfg.Cron.GraceMinS) * time.Second,
		GraceMax:    time.Duration(cfg.Cron.GraceMaxS) * time.Second,
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
		Session:  t.Session,
		UserText: t.Text,
		Toolsets: tools.Resolve(a.Cfg.Toolsets["cron"], t.Toolsets, nil),
		// The default toolsets.cron is [all], so remove acp_delegate. The tool also refuses a cron run.
		RemoveTools: append(slices.Clone(t.RemoveTools), "acp_delegate"),
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
	// One pace for all chats keeps the sends and edits of each chat inside the Telegram limits.
	c := &telegram.Client{Token: cfg.BotToken, BaseURL: a.telegramBaseURL, Log: a.Log, Pace: telegram.NewChatPace(),
		CallTimeout:   time.Duration(cfg.APITimeoutS) * time.Second,
		UploadTimeout: time.Duration(cfg.UploadTimeoutS) * time.Second}
	me, err := c.GetMe(ctx)
	if err != nil {
		return nil, fmt.Errorf("telegram: the bot token check (getMe) failed, so check telegram.bot_token: %w", err)
	}
	ffmpeg, _ := exec.LookPath("ffmpeg")
	// Static stickers go to the vision model. Nil Describe gives the fallback note.
	describe := a.stickerDescriber()
	ch := &telegram.Channel{
		Cfg:  cfg,
		C:    c,
		DB:   a.DB,
		Pair: &telegram.Pairing{DB: a.DB, CodeTTL: time.Duration(cfg.PairingCodeTTLS) * time.Second},
		Inbox: &telegram.Inbox{
			C:        c,
			MediaDir: a.Agent.MediaInbox,
			// Voice notes go to the speech-to-text backend.
			Transcribe: a.Media.TranscribeFunc(),
			Describe:   describe,
			Stickers:   &telegram.KVStickers{DB: a.DB, Log: a.Log},
			Log:        a.Log,

			RichMediaLimit:     time.Duration(cfg.RichMediaTimeoutS) * time.Second,
			InlineTextMaxBytes: cfg.InlineTextMaxBytes,
			DescribeTimeout:    time.Duration(cfg.StickerDescribeTimeoutS) * time.Second,
		},
		Out: &telegram.Sender{C: c, ConvertVoice: telegram.FFmpegToOpusWithin(ffmpeg,
			time.Duration(cfg.VoiceConvertTimeoutS)*time.Second)},
		ToolPreviewChars: a.Cfg.Agent.ToolPreviewChars,
		// Media sets the local files that an answer can send (a port of Hermes
		// validate_media_delivery_path). The gateway.media_* keys set the policy.
		// A set TETHER_MEDIA_* variable wins. A nil policy sends no file.
		Media: telegram.NewMediaPolicyWith(a.DataDir, security.EnvLookup(a.DotEnv), telegram.MediaPolicyConfig{
			Strict:       a.Cfg.Gateway.MediaDeliveryStrict,
			AllowDirs:    a.Cfg.Gateway.MediaAllowDirs,
			TrustRecent:  a.Cfg.Gateway.MediaTrustRecentFiles,
			TrustRecentS: a.Cfg.Gateway.MediaTrustRecentS,
		}, a.Log),
		Bot: me,
		Log: a.Log,
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

// stickerDescriber returns the function that describes a static sticker with the vision
// model. The call has no session, so its usage goes to the aux session with no parent.
// It returns nil when no vision provider exists.
func (a *App) stickerDescriber() func(ctx context.Context, path, prompt string) (string, error) {
	vp, ok := tools.VisionProvider(a.Cfg, a.Providers)
	if !ok {
		return nil
	}
	return func(ctx context.Context, path, prompt string) (string, error) {
		return tools.DescribeImageWithin(ctx, vp, path, prompt, a.recordUsage, int64(a.Cfg.Media.VisionMaxBytes))
	}
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

// acpReapEvery is the time between two checks for idle coding agent processes. Tests change it.
var acpReapEvery = time.Minute

// startACP builds the manager of the coding agents and starts its idle reaper. It starts
// no adapter process: the first acp_delegate call starts one.
func (a *App) startACP(ctx context.Context, version string) {
	if !a.Cfg.ACP.Enabled {
		return
	}
	if len(a.Cfg.ACP.EnabledAgents()) == 0 {
		a.Log.Warn("acp: acp.enabled is true, but no agent is enabled, so acp_delegate is not registered")
		return
	}
	if version == "" {
		version = "dev"
	}
	a.ACP = acp.NewManager(acp.ManagerOptions{
		Config:        a.Cfg.ACP,
		DataDir:       a.DataDir,
		DotEnv:        a.DotEnv,
		Store:         acp.NewStore(a.DB),
		ClientVersion: version,
		Log:           a.Log,
	})
	a.closeACP = a.ACP.Close
	// The reaper has its own ticker. A stop of an adapter takes up to 2 s, and that must
	// not delay the MCP reap.
	actx, stop := context.WithCancel(context.WithoutCancel(ctx))
	a.acpStop = stop
	m, done := a.ACP, make(chan struct{})
	a.acpReaper = done
	go func() {
		defer close(done)
		t := time.NewTicker(acpReapEvery)
		defer t.Stop()
		for {
			select {
			case <-actx.Done():
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

// tightenDataModes removes the group and other bits from the data folder and from the
// state.db files. A folder that MkdirAll made before with 0755 keeps its mode, and the
// database holds every message. A chmod error is a warning, because some file systems
// have no Unix modes.
func (a *App) tightenDataModes(dir string) {
	if fi, err := os.Stat(dir); err == nil && fi.Mode().Perm()&0o077 != 0 {
		old := fmt.Sprintf("%04o", fi.Mode().Perm())
		if err := os.Chmod(dir, 0o700); err != nil {
			a.Log.Warn("data folder: cannot change the mode to 0700; run chmod 700 on the folder", "dir", dir, "old_mode", old, "err", err.Error())
		} else {
			a.Log.Warn("data folder: mode changed to 0700", "dir", dir, "old_mode", old)
		}
	}
	a.tightenDBModes(dir, true)
}

// tightenDBModes removes the group and other bits from the state.db files. It runs
// before and after db.Open, because SQLite creates the files with the umask mode at the
// first start. A file that SQLite made in this run is not a user mistake, so the second
// call (report is false) logs no mode change. It checks only the known names and walks no folder.
func (a *App) tightenDBModes(dir string, report bool) {
	for _, name := range []string{"state.db", "state.db-wal", "state.db-shm"} {
		path := filepath.Join(dir, name)
		fi, err := os.Stat(path)
		if err != nil || fi.Mode().Perm()&0o077 == 0 {
			continue
		}
		old := fmt.Sprintf("%04o", fi.Mode().Perm())
		if err := os.Chmod(path, 0o600); err != nil {
			a.Log.Warn("database file: cannot change the mode to 0600; run chmod 600 on the file", "file", path, "old_mode", old, "err", err.Error())
		} else if report {
			a.Log.Warn("database file: mode changed to 0600", "file", path, "old_mode", old)
		}
	}
}

// registerTools registers the built-in tools that need no channel. registerDelegation
// adds delegate_task.
func (a *App) registerTools(gatewayRole bool) {
	cfg := a.Cfg
	spill := filepath.Join(a.DataDir, "cache", "spill")
	home, _ := os.UserHomeDir()
	protected := security.ProtectedPaths(a.DataDir, home)
	a.WebClient = tools.GuardedClient(cfg.Web.BlockPrivateNetworks, time.Duration(cfg.Web.HTTPTimeoutS)*time.Second, 3)
	imageWait := time.Duration(cfg.Media.ImageDownloadTimeoutS) * time.Second
	a.terminal = &tools.TerminalTool{
		DefaultTimeout: time.Duration(cfg.Terminal.DefaultTimeoutS) * time.Second,
		DotEnv:         a.DotEnv,
		// A child keeps only the secret names of terminal.env_passthrough.
		Passthrough: func(*tools.Env) []string { return skillPassthrough(cfg) },
		SpillDir:    spill,
		InGateway:   gatewayRole,
		Procs:       a.Procs,
	}
	a.RegisterTool(a.terminal)
	a.RegisterTool(tools.ReadFileTool{MaxLines: cfg.Files.ReadMaxLines, MaxLineChars: cfg.Files.ReadMaxLineChars})
	// With Protected nil, the file tools refuse the protected paths of env.DataDir.
	a.RegisterTool(tools.WriteFileTool{})
	a.RegisterTool(tools.PatchTool{ContextLines: cfg.Files.PatchContextLines})
	a.RegisterTool(&tools.WebFetchTool{Client: a.WebClient, MaxURLs: cfg.Web.FetchMaxURLs, MaxBytes: int64(cfg.Web.FetchMaxBytes)})
	a.RegisterTool(&tools.WebSearchTool{Client: a.WebClient, DefaultResults: cfg.Web.SearchDefaultResults, MaxResults: cfg.Web.SearchMaxResults})
	a.RegisterTool(tools.MemoryTool{Store: a.Memory, MaxChars: cfg.Context.ToolOutputMaxChars})
	a.RegisterTool(tools.ClarifyTool{Timeout: time.Duration(cfg.Approvals.TimeoutS) * time.Second})
	a.RegisterTool(tools.SessionSearchTool{
		Store: a.Store, Cron: a.Cron, MaxChars: cfg.Context.ToolOutputMaxChars,
		DefaultResults: cfg.Sessions.SearchDefaultResults, MaxResults: cfg.Sessions.SearchMaxResults,
		DefaultWindow: cfg.Sessions.ScrollDefaultWindow, MaxWindow: cfg.Sessions.ScrollMaxWindow,
	})
	// Sessions lets the first call after a restart restore the list from the history.
	a.RegisterTool(tools.TodoTool{Sessions: a.Store})
	a.RegisterTool(tools.SkillViewTool{Catalog: a.Skills, MaxLinkedFiles: cfg.Skills.ViewMaxLinkedFiles, EnvStatus: func(names []string) []string { return envStatus(cfg, a.DotEnv, names) }})
	a.RegisterTool(tools.SkillsListTool{Catalog: a.Skills})
	a.RegisterTool(tools.SkillManageTool{Mgr: &skills.Manager{Dir: a.Skills.Dir, Guard: cfg.Skills.Guard,
		SkillMDMaxChars: cfg.Skills.SkillMDMaxChars, SupportFileMaxBytes: cfg.Skills.SupportFileMaxBytes}, Catalog: a.Skills})
	cfgBrowser := cfg.Browser
	a.Browser = &browser.Manager{
		ProfileDir: filepath.Join(a.DataDir, "cache", "chrome-profile"),
		IdleClose:  time.Duration(cfgBrowser.IdleCloseMinutes) * time.Minute,
		Log:        a.Log,
	}
	// New does not look for Chrome. The first browser call does.
	tools.RegisterBrowserTools(a.Registry, &tools.BrowserTools{
		M:             a.Browser,
		ActionTimeout: time.Duration(cfgBrowser.ActionTimeoutS) * time.Second,
		ExcerptChars:  cfgBrowser.NavigateExcerptChars,
		Settle:        time.Duration(cfgBrowser.SettleS * float64(time.Second)),
		ShotDir:       filepath.Join(a.DataDir, "cache", "screenshots"),
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
		Download:  tools.GuardedClient(true, imageWait, 3),
		Env:       security.ChildEnv(os.Environ(), a.DotEnv, cfg.Terminal.EnvPassthrough),
		Log:       a.Log,
		Protected: protected,
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
		Tick:            time.Duration(cfg.Cron.TickS) * time.Second,
		GraceMin:        time.Duration(cfg.Cron.GraceMinS) * time.Second,
		GraceMax:        time.Duration(cfg.Cron.GraceMaxS) * time.Second,
	})
	// view_image exists only when a vision provider exists.
	if vp, ok := tools.VisionProvider(cfg, a.Providers); ok {
		// The image download has its own client, so web.http_timeout_s does not cut it.
		a.RegisterTool(tools.VisionTool{
			Prov:            vp,
			Client:          tools.GuardedClient(cfg.Web.BlockPrivateNetworks, imageWait, 3),
			MaxBytes:        int64(cfg.Media.VisionMaxBytes),
			DownloadTimeout: imageWait,
			Record:          a.recordUsage,
			Protected:       protected,
		})
	}
	// execute_code needs python3. Without it, the tool is hidden and doctor reports it.
	if python, err := exec.LookPath("python3"); err == nil {
		a.RegisterTool(tools.ExecuteCodeTool{
			Reg:            a.Registry,
			ScratchRoot:    filepath.Join(a.DataDir, "cache"),
			TimeoutS:       cfg.CodeExecution.TimeoutS,
			MaxCalls:       cfg.CodeExecution.MaxToolCalls,
			MaxStdout:      cfg.CodeExecution.MaxStdoutBytes,
			Python:         python,
			DotEnv:         a.DotEnv,
			Passthrough:    func(*tools.Env) []string { return skillPassthrough(cfg) },
			InGateway:      gatewayRole,
			MaxStderrChars: cfg.CodeExecution.MaxStderrChars,
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
	return a.NewTurnEnv(sess, channel, "")
}

// NewTurnEnv returns the tool context of a session on a channel for a turn of userID. On
// Telegram, the clarify prompt of the turn binds to userID. userID is "" for a turn with
// no user.
func (a *App) NewTurnEnv(sess *sessions.Session, channel, userID string) *tools.Env {
	env := &tools.Env{
		Channel: channel,
		WorkDir: a.WorkDir,
		DataDir: a.DataDir,
		Log:     a.Log,
		Todo:    a.todoLimits(),
	}
	gate := tools.GateApprover{Gate: a.Gate}
	switch {
	case channel == "cli":
		if a.CLIAsk != nil {
			gate.Prompter = a.CLIAsk
			env.Ask = a.CLIAsk
		}
		// gateway.Deps.Turn sends the "🔀 [i/n] tool" lines of the delegate_task children and
		// the "🔀 [agent] kind" lines of the acp_delegate tool calls through the CLI sink.
	case channel == "telegram" && sess != nil && a.Telegram != nil:
		ch := a.Telegram
		if chat, err := strconv.ParseInt(sess.ChatID, 10, 64); err == nil && ch.UI != nil {
			ui := ch.UI.For(chat, sess.ThreadID, userID)
			gate.Prompter = ui
			env.Ask = ui
		}
		key := sess.Key
		env.Progress = func(batch uint64, child, tool, preview string) {
			ch.Progress(key, batch, child, tool, preview)
		}
	}
	env.Gate = gate
	if sess == nil {
		env.Values = map[string]any{}
		return env
	}
	env.SessionID, env.SessionKey, env.Kind = sess.ID, sess.Key, sess.Kind
	env.Values = a.sessionValues(sess.ID)
	return env
}

// sessionValues returns the Values map of the tool context of a session. It makes the map
// when it is missing.
func (a *App) sessionValues(sessionID string) map[string]any {
	a.valuesMu.Lock()
	defer a.valuesMu.Unlock()
	v, ok := a.values[sessionID]
	if !ok {
		v = map[string]any{}
		a.values[sessionID] = v
	}
	return v
}

// Deps returns the parts that the channels and the slash commands use.
func (a *App) Deps() *gateway.Deps {
	d := &gateway.Deps{
		Cfg:        a.Cfg,
		Store:      a.Store,
		Agent:      a.Agent,
		Providers:  a.Providers,
		Models:     a.Models,
		Log:        a.Log,
		NewEnv:     a.NewEnv,
		NewTurnEnv: a.NewTurnEnv,
		CLIAsk:     a.CLIAsk,
		// /new and /reset start a review of the session that they replace, and
		// then free the state of that session.
		BeforeNew: a.beforeNew,
		// /delete frees the state of the session that it removes.
		AfterDelete: a.afterDelete,
		// /stop, /new, and /reset cancel the background delegation children of the key.
		CancelKey: a.cancelKey,
		ToolLine: func(tool, preview string) string {
			return telegram.ToolLineWithin("all", tool, preview, "", a.Cfg.Agent.ToolPreviewChars)
		},
		Goals: a.Goals,
		// The skill commands read the skill folders at the start of a turn, never in New.
		Skills: a.Skills.Load,
		// The skill commands show the status of each env name of a skill.
		EnvStatus: func(names []string) []string { return envStatus(a.Cfg, a.DotEnv, names) },
	}
	// A goal notice with no turn goes to Telegram with its buttons. AddTelegram sets
	// a.Telegram before its first call of Gateway, so the gateway gets this path.
	if a.Telegram != nil {
		d.SendNotice = a.Telegram.SendNotice
		// A resumed turn with no sender gets the owner flag of its session from the
		// current allowlists. AddTelegram sets a.Telegram before its first call of
		// Gateway, so the gateway gets the hook.
		pair := a.Telegram.Pair
		d.OwnerOf = telegram.OwnerOf(a.Cfg.Telegram, func(uid string) bool {
			return pair != nil && pair.IsApproved(context.Background(), "telegram", uid)
		})
	}
	return d
}

// beforeNew starts the review of the session that /new or /reset replaces. The review
// takes the prompt and the tool context of the session before the call returns, so the
// state of the session can go after it.
func (a *App) beforeNew(ctx context.Context, old *sessions.Session) {
	a.Agent.ReviewBeforeNew(ctx, old)
	a.ForgetSession(old.ID)
	// Close returns at once, so the reply to /new does not wait.
	if a.closeACP != nil {
		a.closeACP(old.ID)
	}
}

// afterDelete frees the state of a deleted session and closes its coding agent sessions.
// ForgetSession does not close them, because it also runs after each cron run and for each
// pruned session.
func (a *App) afterDelete(sessionID string) {
	a.ForgetSession(sessionID)
	if a.closeACP != nil {
		a.closeACP(sessionID)
	}
}

// ForgetSession frees the state that the process keeps for a session: the Values map of
// its tool context, the "This session" answers of the approval gate, the frozen
// system prompt, and the review data. Call it when a session ends: after /new, /reset, /delete, and a cron run.
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
	if a.Agent != nil {
		// The review data keeps the frozen prompt, the schemas, and the notice function.
		a.Agent.Reviews.Forget(sessionID)
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

// StopRequest asks the gateway loop to stop.
type StopRequest struct {
	// Reason is the stop reason: "signal", "reload", or "restart".
	Reason string
	// Key is the session key that sent /restart. That chat gets no stop notice, because
	// the command reply tells it already.
	Key string
}

// RestartNotifyKey is the kv key of the chat that sent /restart. The value is the JSON of
// restartNotify. The next gateway tells that chat that it is back.
const RestartNotifyKey = "gateway.restart_notify"

// restartNotify is the value of RestartNotifyKey.
type restartNotify struct {
	Source   string `json:"source"`
	ChatID   string `json:"chat_id"`
	ThreadID string `json:"thread_id"`
	// At is the time of the request, in Unix ms.
	At int64 `json:"at"`
}

// Restarts returns the channel of the /restart requests. It has a buffer of one request.
func (a *App) Restarts() <-chan StopRequest { return a.restarts }

// MarkStopping marks the start of a stop. It returns true for the first caller only. After
// it, a /restart starts no new stop.
func (a *App) MarkStopping() bool { return a.stopping.CompareAndSwap(false, true) }

// codingTasks returns the agent ids of the running acp_delegate calls. It returns nil with
// no ACP manager.
func (a *App) codingTasks() []string {
	if a.ACP == nil {
		return nil
	}
	return a.ACP.Busy()
}

// requestRestart is the RestartRequest of /restart. It saves the chat of key for the next
// gateway and counts the busy keys. The returned start func sends the stop request.
func (a *App) requestRestart(ctx context.Context, key string, to gateway.Target) (gateway.RestartState, func()) {
	if a.stopping.Load() {
		return gateway.RestartState{Already: true}, nil
	}
	b, err := json.Marshal(restartNotify{Source: to.Source, ChatID: to.ChatID, ThreadID: to.ThreadID, At: time.Now().UnixMilli()})
	if err == nil {
		err = a.DB.KVSet(ctx, RestartNotifyKey, string(b))
	}
	if err != nil {
		a.Log.Warn("gateway: cannot save the chat of /restart, so it gets no message after the restart",
			"key", key, "err", err.Error())
	}
	st := gateway.RestartState{Busy: len(a.Gateway().BusyKeys()), CodingTasks: a.codingTasks()}
	return st, func() {
		// A second /restart at the same time sends nothing.
		if !a.MarkStopping() {
			return
		}
		select {
		case a.restarts <- StopRequest{Reason: "restart", Key: key}:
		default:
		}
	}
}

// housekeepingEvery is the time between two housekeeping runs. It is the fallback of
// gateway.housekeeping_interval_s.
const housekeepingEvery = 24 * time.Hour

// housekeepingInterval returns the time between two housekeeping runs.
func (a *App) housekeepingInterval() time.Duration {
	if s := a.Cfg.Gateway.HousekeepingIntervalS; s > 0 {
		return time.Duration(s) * time.Second
	}
	return housekeepingEvery
}

// aliveAtKey is the kv key of the gateway heartbeat, in Unix ms.
const aliveAtKey = "gateway.alive_at"

// heartbeatEvery is the time between two writes of the gateway heartbeat. Tests change it.
var heartbeatEvery = 60 * time.Second

// AliveAt returns the last heartbeat of a gateway. It returns false when no gateway wrote
// a heartbeat, or when the value is not a number.
func (a *App) AliveAt(ctx context.Context) (time.Time, bool) {
	v, ok, err := a.DB.KVGet(ctx, aliveAtKey)
	if err != nil || !ok {
		return time.Time{}, false
	}
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.UnixMilli(ms), true
}

// WriteAliveAt writes the current time as the gateway heartbeat. The next gateway uses the
// last heartbeat as the time of the cut, so a turn marker of a long stop does not resume.
func (a *App) WriteAliveAt(ctx context.Context) {
	if err := a.DB.KVSet(ctx, aliveAtKey, strconv.FormatInt(time.Now().UnixMilli(), 10)); err != nil {
		a.Log.Warn("gateway: cannot save the heartbeat", "err", err.Error())
	}
}

// Housekeeping removes the spill files that are older than context.spill_retention_days,
// the media files that are older than media.cache_retention_days, and the automatic
// sessions that are older than sessions.retention_days. It runs at once,
// and then each `gateway.housekeeping_interval_s` in a goroutine until ctx ends. At once, it also resumes the turns that
// a restart cut and reports the lost background delegations. Then it writes the gateway
// heartbeat, and again each heartbeatEvery until ctx ends.
func (a *App) Housekeeping(ctx context.Context) {
	a.cleanSpill()
	a.cleanMedia()
	a.pruneSessions(ctx)
	// Read the heartbeat of the last gateway before this process writes its first one.
	cut, _ := a.AliveAt(ctx)
	// The cut turns resume before the lost batches are reported. The turn of a lost-batch
	// notice writes the marker of its key, so it replaces the queued texts of the marker.
	freshness := time.Duration(a.Cfg.Agent.ResumeFreshnessS) * time.Second
	outcomes, err := a.Gateway().ResumeInterrupted(ctx, cut, freshness)
	if err != nil {
		a.Log.Warn("housekeeping: cannot resume the cut turns", "err", err.Error())
	}
	// The goal restart step reads the outcomes, so a goal turn that resumed gets no second
	// continuation.
	if err := a.Gateway().ResumeGoals(ctx, cut, freshness, outcomes); err != nil {
		a.Log.Warn("housekeeping: cannot restart the goals", "err", err.Error())
	}
	// A restart loses the running background children. Each parent key gets a notice turn.
	if a.Batches != nil {
		if err := a.Batches.RecoverLost(ctx); err != nil {
			a.Log.Warn("housekeeping: cannot report all lost background delegations", "err", err.Error())
		}
	}
	a.WriteAliveAt(ctx)
	every := heartbeatEvery
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if ctx.Err() == nil {
					a.WriteAliveAt(ctx)
				}
			}
		}
	}()
	go func() {
		t := time.NewTicker(a.housekeepingInterval())
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.cleanSpill()
				a.cleanMedia()
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

// cleanMedia removes the files in cache/media and cache/screenshots that are older than
// media.cache_retention_days, and logs the result. 0 keeps all files.
func (a *App) cleanMedia() {
	days := a.Cfg.Media.CacheRetentionDays
	if days <= 0 {
		return
	}
	now := time.Now
	if a.clock != nil {
		now = a.clock
	}
	total := 0
	for _, dir := range []string{
		filepath.Join(a.DataDir, "cache", "media"),
		filepath.Join(a.DataDir, "cache", "screenshots"),
	} {
		n, err := ctxmgr.CleanupSpill(dir, time.Duration(days)*24*time.Hour, now())
		if err != nil {
			a.Log.Warn("housekeeping: cannot remove all old media files", "dir", dir, "err", err.Error())
		}
		total += n
	}
	if total > 0 {
		a.Log.Info("housekeeping: old media files removed", "count", total, "retention_days", days)
	}
}

// Close stops Chrome, the MCP servers, and the coding agents, and closes the database and the log file.
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
	// The ACP reaper must return before the adapters stop. StopAll keeps the
	// acp_sessions rows, so the next call resumes the context.
	if a.acpStop != nil {
		a.acpStop()
		a.acpStop = nil
	}
	if a.acpReaper != nil {
		<-a.acpReaper
		a.acpReaper = nil
	}
	// a.ACP stays set: the gateway can read it at the same time, through codingTasks. A
	// second StopAll does nothing, and Busy is empty after StopAll.
	if a.ACP != nil {
		a.ACP.StopAll()
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
	return security.NewRedactor(security.KnownSecretValues(dotenv, os.Environ()))
}
