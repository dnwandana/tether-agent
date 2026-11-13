// Package setup holds the doctor checks, the interactive setup wizard, and the systemd
// unit writer.
package setup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
	"syscall"
	"time"

	"github.com/dnwandana/tether-agent/internal/acp"
	"github.com/dnwandana/tether-agent/internal/app"
	"github.com/dnwandana/tether-agent/internal/browser"
	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/hostos"
	"github.com/dnwandana/tether-agent/internal/mcp"
	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/obs"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/telegram"
	"github.com/dnwandana/tether-agent/internal/tools"
	"github.com/dnwandana/tether-agent/internal/update"
	"gopkg.in/yaml.v3"
)

// The values of Check.Status.
const (
	StatusOK   = "ok"
	StatusWarn = "warn"
	StatusFail = "fail"
)

// checkTimeout limits each check that waits for the network or for a process. It is the
// fallback of doctor.check_timeout_s. The setup wizard always uses it.
const checkTimeout = 10 * time.Second

// acpCheckTimeout limits the check of one coding agent. A start of claude, or a first npx
// download, takes longer than checkTimeout. It is the fallback of doctor.acp_check_timeout_s.
const acpCheckTimeout = 30 * time.Second

// minFreeBytes is the free disk space below which the disk check is a warning. It is the
// fallback of doctor.min_free_bytes.
const minFreeBytes = 1 << 30

// secondsOr returns n seconds as a duration, or def when n is 0 or less.
func secondsOr(n int, def time.Duration) time.Duration {
	if n <= 0 {
		return def
	}
	return time.Duration(n) * time.Second
}

// Check is the result of one doctor check. Next is the action that fixes a warning or a
// failure.
type Check struct {
	Name   string
	Status string
	Detail string
	Next   string
}

// Doctor checks the install: the config, .env, the providers, Telegram, state.db, the host
// programs, the MCP servers, the disk, the data folder mode, the blocked memory entries, and the
// newest release. It needs no user input, and
// it does not take the gateway lock, so it can run next to a running gateway.
type Doctor struct {
	DataDir    string
	ConfigPath string
	// HC is the client of the provider calls and the Bot API calls. Nil uses a new client.
	HC *http.Client
	// LookPath finds a host program. Nil uses exec.LookPath.
	LookPath func(string) (string, error)
	// TelegramBase replaces the Bot API root. Empty uses the real Bot API.
	TelegramBase string
	// Getenv reads the environment for the Chrome detection. Nil uses os.Getenv.
	Getenv func(string) string
	// Version is the version of this binary (main.version). It feeds the release check.
	Version string
	// ReleaseAPI replaces the GitHub API base of the release check. Empty uses the real API.
	ReleaseAPI string
	// UnitPaths are the unit files that the unit check reads. Nil uses the user unit path, then
	// the system unit path.
	UnitPaths []string

	// freeBytes returns the free disk space of a folder. Tests replace it.
	freeBytes func(dir string) (uint64, error)
	// modesKept reports whether the file system of dir keeps Unix modes. Tests replace it.
	// Nil uses probeModes.
	modesKept func(dir string) bool
	// probeACP starts one coding agent. Tests replace it. Nil uses acp.Probe.
	probeACP func(ctx context.Context, agentID string, a config.ACPAgent, dotenv map[string]string) (acp.ProbeResult, error)
}

// Run runs every check in a fixed order, so the output is stable. A check that depends on
// the config is left out when the config does not load.
func (d *Doctor) Run(ctx context.Context) []Check {
	var out []Check
	dotenv, envCheck := d.checkDotEnv()
	cfg, cfgCheck := d.checkConfig(dotenv, envCheck)
	out = append(out, cfgCheck)
	if raw, err := os.ReadFile(d.configPath()); err == nil {
		out = append(out, flatRateChecks(raw)...)
	}
	out = append(out, envCheck)
	if cfg != nil {
		out = append(out, d.checkProviders(ctx, cfg)...)
		out = append(out, d.checkTelegram(ctx, cfg))
	}
	out = append(out, d.checkStateDB(ctx)...)
	hostCfg := cfg
	if hostCfg == nil {
		def := config.Default()
		hostCfg = &def
	}
	out = append(out, d.checkDataDir(), d.checkMemory(hostCfg.Memory.EntryMaxChars))
	out = append(out, d.HostChecks(hostCfg)...)
	if c, ok := d.checkUnit(hostCfg.Agent.RestartDrainTimeoutS); ok {
		out = append(out, c)
	}
	if cfg != nil {
		out = append(out, d.checkMCP(ctx, cfg, dotenv)...)
	}
	if cfg != nil && cfg.ACP.Enabled {
		out = append(out, d.checkACP(ctx, cfg, dotenv)...)
	}
	out = append(out, d.checkDisk(uint64(max(hostCfg.Doctor.MinFreeBytes, 0))))
	// The release check needs no config, so it also runs when the config does not load.
	out = append(out, d.checkRelease(ctx, secondsOr(hostCfg.Doctor.CheckTimeoutS, checkTimeout)))

	red := app.NewRedactor(dotenv)
	for i := range out {
		out[i].Detail = red.Redact(out[i].Detail)
		out[i].Next = red.Redact(out[i].Next)
	}
	return out
}

// HasFail reports whether any check failed.
func HasFail(checks []Check) bool {
	return slices.ContainsFunc(checks, func(c Check) bool { return c.Status == StatusFail })
}

// Format returns one line per check: the symbol, the name, the detail, and "→ <next>"
// when the check has a next action.
func Format(checks []Check) string {
	var b strings.Builder
	for _, c := range checks {
		sym := "✓"
		switch c.Status {
		case StatusWarn:
			sym = "⚠"
		case StatusFail:
			sym = "✗"
		}
		fmt.Fprintf(&b, "%s %s: %s", sym, c.Name, c.Detail)
		if c.Next != "" {
			fmt.Fprintf(&b, " → %s", c.Next)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func (d *Doctor) configPath() string {
	if d.ConfigPath != "" {
		return d.ConfigPath
	}
	return filepath.Join(d.DataDir, "config.yaml")
}

func (d *Doctor) hc() *http.Client {
	if d.HC != nil {
		return d.HC
	}
	return &http.Client{}
}

func (d *Doctor) lookPath(name string) (string, error) {
	if d.LookPath != nil {
		return d.LookPath(name)
	}
	return exec.LookPath(name)
}

func (d *Doctor) getenv(name string) string {
	if d.Getenv != nil {
		return d.Getenv(name)
	}
	return os.Getenv(name)
}

// checkDotEnv loads .env. A missing file is valid. A file that other users can read fails.
func (d *Doctor) checkDotEnv() (map[string]string, Check) {
	path := filepath.Join(d.DataDir, ".env")
	c := Check{Name: ".env", Status: StatusOK}
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		c.Detail = "no .env file; the config has no secrets from .env"
		return map[string]string{}, c
	case err != nil:
		c.Status, c.Detail, c.Next = StatusFail, err.Error(), "make "+path+" readable by your user"
		return map[string]string{}, c
	case info.Mode().Perm()&0o077 != 0:
		c.Status = StatusFail
		c.Detail = fmt.Sprintf("mode %04o lets other users read the secrets", info.Mode().Perm())
		c.Next = "chmod 600 " + path
		return map[string]string{}, c
	}
	dotenv, err := security.LoadDotEnv(path)
	if err != nil {
		c.Status, c.Detail, c.Next = StatusFail, err.Error(), "correct the line in "+path
		return map[string]string{}, c
	}
	c.Detail = fmt.Sprintf("mode %04o, %d values", info.Mode().Perm(), len(dotenv))
	return dotenv, c
}

// checkConfig loads the config with the .env values. It returns nil when the config does
// not load.
func (d *Doctor) checkConfig(dotenv map[string]string, envCheck Check) (*config.Config, Check) {
	path := d.configPath()
	c := Check{Name: "config", Status: StatusOK}
	cfg, err := config.Load(path, security.EnvLookup(dotenv))
	if err != nil {
		c.Status, c.Detail = StatusFail, err.Error()
		switch {
		case envCheck.Status == StatusFail:
			c.Next = "correct .env first (see the .env check), then run tether doctor again"
		case strings.Contains(err.Error(), "providers.primary is missing"):
			c.Next = "run tether setup"
		default:
			c.Next = "correct " + path + " or .env, then run tether doctor again"
		}
		return nil, c
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		c.Detail = path + " does not exist; the defaults apply"
	} else {
		c.Detail = "loaded " + path
	}
	return cfg, c
}

// flatRateChecks returns one warning for each providers.<name> entry that has the removed key
// flat_rate. The config struct has no field for the key, so the check reads the raw YAML.
func flatRateChecks(raw []byte) []Check {
	var doc struct {
		Providers map[string]map[string]any `yaml:"providers"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	var out []Check
	for _, name := range slices.Sorted(maps.Keys(doc.Providers)) {
		if _, ok := doc.Providers[name]["flat_rate"]; !ok {
			continue
		}
		out = append(out, Check{
			Name:   "config",
			Status: StatusWarn,
			Detail: "providers." + name + ".flat_rate is removed. The usage report counts every provider in dollars. Delete the key from config.yaml.",
			Next:   "delete the flat_rate line under providers." + name + " in config.yaml",
		})
	}
	return out
}

// providerOrder returns the provider names: primary, then the fallback list in order, and
// then the other entries in name order.
func providerOrder(cfg *config.Config) []string {
	var names []string
	seen := map[string]bool{}
	add := func(name string) {
		if _, ok := cfg.Providers[name]; ok && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	add("primary")
	for _, name := range cfg.FallbackList() {
		add(name)
	}
	rest := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		rest = append(rest, name)
	}
	slices.Sort(rest)
	for _, name := range rest {
		add(name)
	}
	return names
}

// checkProviders sends a 1-token test call to each provider. An unusable entry fails with
// its load message and gets no test call. The other entries still get their checks.
func (d *Doctor) checkProviders(ctx context.Context, cfg *config.Config) []Check {
	var out []Check
	for _, name := range providerOrder(cfg) {
		pc := cfg.Providers[name]
		c := Check{Name: "provider " + name, Status: StatusOK}
		if pc.Preset != "" {
			c.Name += " (" + pc.Preset + ")"
		}
		if pc.Unusable != "" {
			c.Status, c.Detail = StatusFail, pc.Unusable
			c.Next = fmt.Sprintf("set the API key and the model of providers.%s, or remove the entry from config.yaml", name)
			out = append(out, c)
			continue
		}
		model, ms, err := TestProvider(ctx, name, pc, d.hc(), secondsOr(cfg.Doctor.CheckTimeoutS, checkTimeout), cfg.Provider.AnthropicMaxTokens)
		if err != nil {
			c.Status = StatusFail
			c.Detail = fmt.Sprintf("the test call to provider %s failed: %v", name, err)
			c.Next = fmt.Sprintf("check providers.%s.base_url, api_key, and model in config.yaml", name)
		} else {
			c.Detail = fmt.Sprintf("model %s answered in %d ms", model, ms)
		}
		out = append(out, c)
	}
	return out
}

// TestProvider sends a 1-token test call to the provider entry. It returns the model name
// and the time of the call in milliseconds. The timeout limits the call. A timeout of 0 or
// less gives checkTimeout. The anthropicMaxTokens value goes to provider.NewWith.
func TestProvider(ctx context.Context, name string, pc config.Provider, hc *http.Client, timeout time.Duration, anthropicMaxTokens int) (model string, ms int64, err error) {
	p, err := provider.NewWith(name, pc, hc, anthropicMaxTokens)
	if err != nil {
		return "", 0, err
	}
	if timeout <= 0 {
		timeout = checkTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	ch, err := p.Stream(ctx, provider.Request{
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: "Reply with OK."}},
		MaxTokens: 1,
	})
	if err != nil {
		return "", 0, err
	}
	if _, err := provider.Collect(ch, nil); err != nil {
		return "", 0, err
	}
	return p.Model(), time.Since(start).Milliseconds(), nil
}

// checkTelegram calls getMe when Telegram is on.
func (d *Doctor) checkTelegram(ctx context.Context, cfg *config.Config) Check {
	c := Check{Name: "telegram", Status: StatusOK}
	tg := cfg.Telegram
	if !tg.Enabled {
		c.Detail = "disabled (telegram.enabled is false)"
		return c
	}
	if strings.TrimSpace(tg.BotToken) == "" {
		c.Status, c.Detail = StatusFail, "telegram.enabled is true, but no bot token is set"
		c.Next = "set TELEGRAM_BOT_TOKEN in .env, or run tether setup"
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, secondsOr(cfg.Doctor.CheckTimeoutS, checkTimeout))
	defer cancel()
	cl := &telegram.Client{Token: tg.BotToken, BaseURL: d.TelegramBase, HC: d.hc()}
	me, err := cl.GetMe(ctx)
	if err != nil {
		c.Status, c.Detail = StatusFail, "getMe failed: "+err.Error()
		c.Next = "check the bot token with @BotFather, then set TELEGRAM_BOT_TOKEN in .env"
		return c
	}
	if needsAllGroupMessages(tg) && !me.CanReadAllGroupMessages {
		c.Status = StatusWarn
		c.Detail = "getMe works for @" + me.Username + ", but privacy mode is on. In a group, the bot gets only commands and mentions, but the config needs every group message."
		c.Next = "turn off privacy mode with /setprivacy in @BotFather, then remove the bot from each group and add it again"
		return c
	}
	c.Detail = "getMe works for @" + me.Username
	return c
}

// needsAllGroupMessages reports whether the config needs the plain group messages that
// BotFather privacy mode blocks. A text that matches a mention pattern is plain text, not a
// mention, so privacy mode also blocks it.
func needsAllGroupMessages(tg config.TelegramConfig) bool {
	return !tg.RequireMention || len(tg.FreeResponseChats) > 0 ||
		tg.ObserveUnmentionedGroupMessages || len(tg.MentionPatterns) > 0
}

// checkStateDB opens state.db, which also applies the missing migrations. A missing file
// is valid: the first run makes it. When a stored model answer exists, it also gives the
// "last answer" check.
func (d *Doctor) checkStateDB(ctx context.Context) []Check {
	c := Check{Name: "state.db", Status: StatusOK}
	path := filepath.Join(d.DataDir, "state.db")
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		c.Detail = "does not exist yet; the first chat or gateway run makes it"
		return []Check{c}
	}
	sdb, err := db.Open(path)
	if err != nil {
		c.Status, c.Detail = StatusFail, err.Error()
		c.Next = "stop the gateway, keep a copy of " + path + ", and run tether doctor again"
		return []Check{c}
	}
	defer sdb.Close()
	var version int
	if err := sdb.QueryRowContext(ctx, "SELECT coalesce(max(version), 0) FROM schema_version").Scan(&version); err != nil {
		c.Status, c.Detail = StatusFail, "cannot read schema_version: "+err.Error()
		c.Next = "keep a copy of " + path + " and report the problem"
		return []Check{c}
	}
	c.Detail = fmt.Sprintf("opens; the migrations are current (schema version %d)", version)
	out := []Check{c}
	// A failed read does not fail doctor. The check is only information.
	if l, ok, err := obs.ReadLastAnswer(ctx, sdb); err == nil && ok {
		out = append(out, lastAnswerCheck(l, time.Since(l.At)))
	}
	return out
}

// lastAnswerCheck shows the provider and the model of the newest stored answer. An answer
// from an entry other than the first entry of the turn is a warning, because the first
// entry failed at that time. The first entry is the pinned entry, else primary.
func lastAnswerCheck(l obs.LastAnswer, age time.Duration) Check {
	c := Check{Name: "last answer", Status: StatusOK,
		Detail: l.Provider + " · " + l.Model + ", " + obs.Ago(age)}
	switch {
	case l.Provider != l.PinnedEntry():
		c.Status = StatusWarn
		c.Next = `the primary failed at that time; see "provider fallback" in the gateway log`
	case l.Pin != "":
		c.Detail += " (the session pins the entry " + l.PinnedEntry() + ")"
	}
	return c
}

// HostChecks checks the optional host programs: Chrome, python3, pdftotext, git, ffmpeg, and
// the programs of the TTS and STT commands. A missing program is a warning, and the detail
// names the feature that is lost.
func (d *Doctor) HostChecks(cfg *config.Config) []Check {
	var out []Check

	chrome := Check{Name: "chrome", Status: StatusOK}
	if p, err := browser.DetectChrome(cfg.Browser.ChromePath, d.lookPath, d.getenv, runtime.GOOS); err != nil {
		chrome.Status = StatusWarn
		chrome.Detail = "not found, so the browser_* tools fail"
		chrome.Next = hostos.Hint("chrome") + ", or set browser.chrome_path"
	} else {
		chrome.Detail = p
	}
	out = append(out, chrome)

	out = append(out,
		d.program("python3", "execute_code is off", hostos.Hint("python3")),
		d.program("pdftotext", "the agent cannot read the text of PDF files", hostos.Hint("pdftotext")),
		d.program("git", "skills install and update cannot fetch from a git URL", hostos.Hint("git")))

	ffmpeg := d.program("ffmpeg", "Telegram voice replies go as audio files, and whisper-cli cannot convert voice notes",
		hostos.Hint("ffmpeg"))
	if ffmpeg.Status != StatusOK && !voiceOn(cfg) {
		ffmpeg = Check{Name: "ffmpeg", Status: StatusOK, Detail: "not found; Telegram voice is off, so nothing needs it"}
	}
	out = append(out, ffmpeg)

	if prog := commandProgram(cfg.Media.TTS); prog != "" {
		out = append(out, d.program(prog, "text_to_speech fails", hostos.Hint(filepath.Base(prog))+", or change media.tts"))
	}
	if prog := commandProgram(cfg.Media.STT); prog != "" {
		out = append(out, d.program(prog, "transcribe_audio and the voice note transcripts fail",
			hostos.Hint(filepath.Base(prog))+", or change media.stt"))
	}
	return out
}

// program checks one host program. lost names the feature that a missing program stops.
func (d *Doctor) program(name, lost, install string) Check {
	p, err := d.lookPath(name)
	if err != nil || p == "" {
		return Check{Name: name, Status: StatusWarn, Detail: "not found, so " + lost, Next: install}
	}
	return Check{Name: name, Status: StatusOK, Detail: p}
}

// voiceOn reports whether Telegram can get or send voice notes: Telegram is on and a
// speech backend is set.
func voiceOn(cfg *config.Config) bool {
	return cfg.Telegram.Enabled && (cfg.Media.TTS.Backend != "" || cfg.Media.STT.Backend != "")
}

// commandProgram returns the program of a command template backend, or "" for another
// backend.
func commandProgram(b config.MediaBackend) string {
	if b.Backend != "command" {
		return ""
	}
	fields := strings.Fields(b.Command)
	if len(fields) == 0 {
		return ""
	}
	prog := fields[0]
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(prog, "~/") {
		prog = filepath.Join(home, prog[2:])
	}
	return prog
}

// checkMCP starts each enabled MCP server and lists its tools. The servers stop again
// before Run returns.
func (d *Doctor) checkMCP(ctx context.Context, cfg *config.Config, dotenv map[string]string) []Check {
	names := make([]string, 0, len(cfg.MCP.Servers))
	for name := range cfg.MCP.Servers {
		names = append(names, name)
	}
	slices.Sort(names)
	var (
		out    []Check
		closes sync.WaitGroup
	)
	for _, name := range names {
		s := cfg.MCP.Servers[name]
		c := Check{Name: "mcp " + name, Status: StatusOK}
		if !s.Enabled {
			c.Detail = "disabled, not checked"
			out = append(out, c)
			continue
		}
		next := fmt.Sprintf("check mcp.servers.%s in config.yaml, or set enabled: false", name)
		cctx, cancel := context.WithTimeout(ctx, secondsOr(cfg.Doctor.CheckTimeoutS, checkTimeout))
		cl, err := mcp.Dial(cctx, name, s, os.Environ(), dotenv)
		if err != nil {
			cancel()
			c.Status, c.Detail, c.Next = StatusFail, "cannot start: "+err.Error(), next
			out = append(out, c)
			continue
		}
		defs, err := cl.ListTools(cctx)
		cancel()
		// A stdio server can take a few seconds to stop, so the stops run together.
		closes.Add(1)
		go func() {
			defer closes.Done()
			_ = cl.Close()
		}()
		if err != nil {
			c.Status, c.Detail, c.Next = StatusFail, "starts, but cannot list its tools: "+err.Error(), next
		} else {
			c.Detail = fmt.Sprintf("starts and lists %d tools", len(defs))
		}
		out = append(out, c)
	}
	closes.Wait()
	return out
}

// checkACP probes each enabled coding agent and checks acp.workdir_allowlist. The
// adapters stop again before it returns.
func (d *Doctor) checkACP(ctx context.Context, cfg *config.Config, dotenv map[string]string) []Check {
	probe := d.probeACP
	if probe == nil {
		probe = acp.Probe
	}
	var out []Check
	ids := cfg.ACP.EnabledAgents()
	if len(ids) == 0 {
		out = append(out, Check{Name: "acp agents", Status: StatusWarn,
			Detail: "acp.enabled is true, but no agent is enabled",
			Next:   "add an agent to acp.agents, for example claude-code"})
	}
	for _, id := range ids {
		a := cfg.ACP.Agents[id]
		pctx, cancel := context.WithTimeout(ctx, secondsOr(cfg.Doctor.ACPCheckTimeoutS, acpCheckTimeout))
		r, err := probe(pctx, id, a, dotenv)
		cancel()
		out = append(out, acpAgentChecks(id, a, r, err)...)
	}

	allow := Check{Name: "acp allowlist", Status: StatusOK}
	if len(cfg.ACP.WorkdirAllowlist) == 0 {
		allow.Status = StatusWarn
		allow.Detail = "acp.workdir_allowlist is empty, so acp_delegate refuses each call"
		allow.Next = "add the folder of your projects to acp.workdir_allowlist"
	} else {
		allow.Detail = strings.Join(cfg.ACP.WorkdirAllowlist, ", ")
	}
	return append(out, allow)
}

// acpAgentChecks returns the checks of one coding agent from its probe result. The rows stop
// at the first failed step that blocks the next steps.
func acpAgentChecks(id string, a config.ACPAgent, r acp.ProbeResult, err error) []Check {
	prefix := "acp " + id + " "
	runByHand := "run the command of acp.agents." + id + " by hand to see its error"
	errText := func() string {
		if err == nil {
			return "unknown error"
		}
		return err.Error()
	}

	if r.Program == "" {
		return []Check{{Name: prefix + "command", Status: StatusFail,
			Detail: "cannot find the program: " + errText(), Next: acpInstallText(id)}}
	}
	out := []Check{{Name: prefix + "command", Status: StatusOK, Detail: r.Program}}

	if len(a.Command) > 0 && !strings.ContainsRune(a.Command[0], '/') && underHome(r.Program) {
		out = append(out, Check{Name: prefix + "path", Status: StatusWarn,
			Detail: r.Program + " comes from the PATH of your shell",
			Next:   "set an absolute command; the tether service does not have your shell PATH"})
	}

	if r.NodeVersion != "" {
		c := Check{Name: prefix + "node", Status: StatusOK, Detail: "node " + r.NodeVersion}
		if nodeMajor(r.NodeVersion) < 22 {
			c.Status = StatusFail
			c.Detail = "node " + r.NodeVersion + " is too old"
			c.Next = "install Node 22 or later"
		}
		out = append(out, c)
	}

	if r.ProtocolVersion == 0 {
		return append(out, Check{Name: prefix + "handshake", Status: StatusFail,
			Detail: "cannot start: " + errText(), Next: runByHand})
	}
	version := r.AgentVersion
	if version == "" {
		version = "unknown"
	}
	hs := Check{Name: prefix + "handshake", Status: StatusOK,
		Detail: fmt.Sprintf("protocol %d, agent version %s", r.ProtocolVersion, version)}
	if a.VersionPin != "" && a.VersionPin != r.AgentVersion {
		hs.Status = StatusWarn
		hs.Detail += " (version_pin " + a.VersionPin + ")"
		hs.Next = "install version " + a.VersionPin + " of the adapter, or change acp.agents." + id + ".version_pin"
	}
	out = append(out, hs)

	sess := Check{Name: prefix + "session", Status: StatusOK}
	switch {
	case r.SessionOK:
		sess.Detail = "session start ok (the login is checked at the first prompt)"
	case r.NotLoggedIn:
		sess.Status, sess.Detail = StatusFail, "not logged in"
		sess.Next = "run claude once as the user that runs tether, and sign in"
	default:
		sess.Status, sess.Detail, sess.Next = StatusFail, "session start failed: "+errText(), runByHand
	}
	return append(out, sess)
}

// acpInstallText returns the next action for an adapter program that is not found.
func acpInstallText(id string) string {
	switch id {
	case "claude-code":
		return "install @agentclientprotocol/claude-agent-acp, or set acp.agents.claude-code.command"
	case "codex":
		return "install codex-acp, or set acp.agents.codex.command"
	}
	return "install the adapter of " + id + ", or set acp.agents." + id + ".command"
}

// underHome reports whether path is in the home folder of the user.
func underHome(path string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	rel, err := filepath.Rel(home, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../") && !filepath.IsAbs(rel)
}

// nodeMajor returns the major version of a node version text such as "v22.3.0". A text that
// does not parse gives 0, which counts as too old.
func nodeMajor(v string) int {
	major, _, _ := strings.Cut(strings.TrimPrefix(v, "v"), ".")
	n, err := strconv.Atoi(major)
	if err != nil {
		return 0
	}
	return n
}

// checkDisk checks the free disk space of the data folder. A minFree of 0 gives minFreeBytes.
func (d *Doctor) checkDisk(minFree uint64) Check {
	if minFree == 0 {
		minFree = minFreeBytes
	}
	c := Check{Name: "disk", Status: StatusOK}
	dir := d.DataDir
	// The data folder can be missing before the first run. Use the nearest parent.
	for {
		if _, err := os.Stat(dir); err == nil || filepath.Dir(dir) == dir {
			break
		}
		dir = filepath.Dir(dir)
	}
	free := d.freeBytes
	if free == nil {
		free = statfsFree
	}
	n, err := free(dir)
	if err != nil {
		c.Status, c.Detail, c.Next = StatusWarn, "cannot read the free space: "+err.Error(), "check the disk of "+dir
		return c
	}
	c.Detail = fmt.Sprintf("%.1f GB free in %s", float64(n)/(1<<30), dir)
	if n < minFree {
		c.Status = StatusWarn
		c.Next = "free some disk space; the logs, the cache, and state.db need room"
	}
	return c
}

// checkMemory lists the entries of memory.md and user_profile.md that the memory threat scan
// blocks. The prompt shows a BLOCKED line in place of such an entry. entryMax is
// memory.entry_max_chars, so the check blocks the same entries as the prompt. A value of 0 or
// less uses the default limit.
func (d *Doctor) checkMemory(entryMax int) Check {
	c := Check{Name: "memory", Status: StatusOK}
	dir := filepath.Join(d.DataDir, "memory")
	// Open makes the folder, so look first.
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		c.Detail = "no memory yet"
		return c
	}
	st, err := memory.Open(dir, 0, 0)
	if err != nil {
		c.Status, c.Detail, c.Next = StatusWarn, "cannot open the memory store: "+err.Error(), "check the folder "+dir
		return c
	}
	var items []string
	var firstID string
	for _, t := range []struct{ target, file string }{
		{memory.TargetMemory, "memory.md"},
		{memory.TargetUser, "user_profile.md"},
	} {
		entries, err := st.ReadEntries(t.target)
		if err != nil {
			c.Status, c.Detail, c.Next = StatusWarn, "cannot read "+t.file+": "+err.Error(), "check the file "+filepath.Join(dir, t.file)
			return c
		}
		for _, e := range entries {
			if _, blocked := tools.MemoryBlockedLineWithin(e, entryMax); !blocked {
				continue
			}
			id := tools.MemoryEntryID(e)
			if firstID == "" {
				firstID = id
			}
			items = append(items, t.file+" "+id+" ("+strings.Join(tools.MemoryThreatIDsWithin(e, entryMax), ", ")+")")
		}
	}
	if len(items) == 0 {
		c.Detail = "no blocked entries"
		return c
	}
	c.Status = StatusWarn
	noun := "entries"
	if len(items) == 1 {
		noun = "entry"
	}
	c.Detail = fmt.Sprintf("%d blocked %s: %s", len(items), noun, strings.Join(items, "; "))
	c.Next = "ask the agent to remove each entry with the memory tool, for example: remove " + firstID
	return c
}

// checkDataDir warns when other users can read the data folder. app.New also tightens the
// mode. The check gives a warning, not a failure, because some file systems have no Unix modes.
func (d *Doctor) checkDataDir() Check {
	c := Check{Name: "data folder", Status: StatusOK}
	info, err := os.Stat(d.DataDir)
	if errors.Is(err, fs.ErrNotExist) {
		c.Detail = "not created yet"
		return c
	}
	if err != nil {
		c.Status, c.Detail, c.Next = StatusWarn, "cannot read the data folder: "+err.Error(), "check the permissions of "+filepath.Dir(d.DataDir)
		return c
	}
	perm := info.Mode().Perm()
	if perm&0o077 == 0 {
		c.Detail = fmt.Sprintf("mode %04o", perm)
		return c
	}
	c.Status = StatusWarn
	kept := d.modesKept
	if kept == nil {
		kept = probeModes
	}
	if kept(d.DataDir) {
		c.Detail = fmt.Sprintf("mode %04o: other users can read the data folder", perm)
		c.Next = "chmod 700 " + d.DataDir
		return c
	}
	c.Detail = fmt.Sprintf("mode %04o; the file system does not keep Unix modes", perm)
	c.Next = "move the data folder to a Linux file system and set TETHER_HOME to it"
	return c
}

// probeModes reports whether the file system of dir keeps Unix modes. It returns false only
// when a file keeps no mode of 0600 after a chmod.
func probeModes(dir string) bool {
	f, err := os.CreateTemp(dir, ".doctor-mode-*")
	if err != nil {
		return true
	}
	name := f.Name()
	_ = f.Close()
	defer os.Remove(name)
	if err := os.Chmod(name, 0o600); err != nil {
		return true
	}
	info, err := os.Stat(name)
	if err != nil {
		return true
	}
	return info.Mode().Perm() == 0o600
}

// checkRelease compares Version with the newest release on GitHub. A dev build sends no
// request. A failure is a warning, because doctor must not fail without the network.
// A timeout of 0 or less gives checkTimeout.
func (d *Doctor) checkRelease(ctx context.Context, timeout time.Duration) Check {
	if timeout <= 0 {
		timeout = checkTimeout
	}
	c := Check{Name: "release", Status: StatusOK}
	if update.IsDev(d.Version) {
		c.Detail = "dev build; the release check is off"
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cl := update.Client{HC: d.hc(), APIBase: d.ReleaseAPI, UserAgent: "tether-agent/" + d.Version}
	rel, err := cl.Latest(ctx)
	if err != nil {
		c.Status, c.Detail = StatusWarn, "cannot reach GitHub: "+err.Error()
		c.Next = "check the network, or run tether update --check later"
		return c
	}
	cmp, err := update.Compare(d.Version, rel.Version)
	if err != nil {
		c.Status = StatusWarn
		c.Detail = fmt.Sprintf("cannot compare %s with %s: %v", d.Version, rel.Version, err)
		return c
	}
	switch {
	case cmp < 0:
		c.Status = StatusWarn
		c.Detail = fmt.Sprintf("%s is available; you run %s (released %s)",
			rel.Version, d.Version, rel.PublishedAt.Format("2006-01-02"))
		c.Next = "run tether update"
	case cmp == 0:
		c.Detail = d.Version + " is the newest release"
	default:
		c.Detail = fmt.Sprintf("%s is newer than the newest release, %s", d.Version, rel.Version)
	}
	return c
}

// statfsFree returns the free bytes of the file system of dir for a normal user.
func statfsFree(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
