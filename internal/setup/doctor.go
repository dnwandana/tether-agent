// Package setup holds the doctor checks, the interactive setup wizard, and the systemd
// unit writer.
package setup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dnwandana/tether-agent/internal/app"
	"github.com/dnwandana/tether-agent/internal/browser"
	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/mcp"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/telegram"
)

// The values of Check.Status.
const (
	StatusOK   = "ok"
	StatusWarn = "warn"
	StatusFail = "fail"
)

// checkTimeout limits each check that waits for the network or for a process.
const checkTimeout = 10 * time.Second

// minFreeBytes is the free disk space below which the disk check is a warning.
const minFreeBytes = 1 << 30

// Check is the result of one doctor check. Next is the action that fixes a warning or a
// failure.
type Check struct {
	Name   string
	Status string
	Detail string
	Next   string
}

// Doctor checks the install: the config, .env, the providers, Telegram, state.db, the host
// programs, the MCP servers, and the disk. It needs no user input, and it does not take
// the gateway lock, so it can run next to a running gateway.
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

	// freeBytes returns the free disk space of a folder. Tests replace it.
	freeBytes func(dir string) (uint64, error)
}

// Run runs every check in a fixed order, so the output is stable. A check that depends on
// the config is left out when the config does not load.
func (d *Doctor) Run(ctx context.Context) []Check {
	var out []Check
	dotenv, envCheck := d.checkDotEnv()
	cfg, cfgCheck := d.checkConfig(dotenv, envCheck)
	out = append(out, cfgCheck, envCheck)
	if cfg != nil {
		out = append(out, d.checkProviders(ctx, cfg)...)
		out = append(out, d.checkTelegram(ctx, cfg))
	}
	out = append(out, d.checkStateDB(ctx))
	hostCfg := cfg
	if hostCfg == nil {
		def := config.Default()
		hostCfg = &def
	}
	out = append(out, d.HostChecks(hostCfg)...)
	if cfg != nil {
		out = append(out, d.checkMCP(ctx, cfg, dotenv)...)
	}
	out = append(out, d.checkDisk())

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

// providerOrder returns the provider names: primary, fallback, and then the others in
// name order.
func providerOrder(cfg *config.Config) []string {
	var names []string
	for name := range cfg.Providers {
		if name != "primary" && name != "fallback" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, first := range []string{"fallback", "primary"} {
		if _, ok := cfg.Providers[first]; ok {
			names = append([]string{first}, names...)
		}
	}
	return names
}

// checkProviders sends a 1-token test call to each provider.
func (d *Doctor) checkProviders(ctx context.Context, cfg *config.Config) []Check {
	var out []Check
	for _, name := range providerOrder(cfg) {
		c := Check{Name: "provider " + name, Status: StatusOK}
		model, ms, err := TestProvider(ctx, name, cfg.Providers[name], d.hc())
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
// and the time of the call in milliseconds.
func TestProvider(ctx context.Context, name string, pc config.Provider, hc *http.Client) (model string, ms int64, err error) {
	p, err := provider.New(name, pc, hc)
	if err != nil {
		return "", 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
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
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	cl := &telegram.Client{Token: tg.BotToken, BaseURL: d.TelegramBase, HC: d.hc()}
	me, err := cl.GetMe(ctx)
	if err != nil {
		c.Status, c.Detail = StatusFail, "getMe failed: "+err.Error()
		c.Next = "check the bot token with @BotFather, then set TELEGRAM_BOT_TOKEN in .env"
		return c
	}
	c.Detail = "getMe works for @" + me.Username
	return c
}

// checkStateDB opens state.db, which also applies the missing migrations. A missing file
// is valid: the first run makes it.
func (d *Doctor) checkStateDB(ctx context.Context) Check {
	c := Check{Name: "state.db", Status: StatusOK}
	path := filepath.Join(d.DataDir, "state.db")
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		c.Detail = "does not exist yet; the first chat or gateway run makes it"
		return c
	}
	sdb, err := db.Open(path)
	if err != nil {
		c.Status, c.Detail = StatusFail, err.Error()
		c.Next = "stop the gateway, keep a copy of " + path + ", and run tether doctor again"
		return c
	}
	defer sdb.Close()
	var version int
	if err := sdb.QueryRowContext(ctx, "SELECT coalesce(max(version), 0) FROM schema_version").Scan(&version); err != nil {
		c.Status, c.Detail = StatusFail, "cannot read schema_version: "+err.Error()
		c.Next = "keep a copy of " + path + " and report the problem"
		return c
	}
	c.Detail = fmt.Sprintf("opens; the migrations are current (schema version %d)", version)
	return c
}

// HostChecks checks the optional host programs: Chrome, python3, pdftotext, ffmpeg, and the
// programs of the TTS and STT commands. A missing program is a warning, and the detail
// names the feature that is lost.
func (d *Doctor) HostChecks(cfg *config.Config) []Check {
	var out []Check

	chrome := Check{Name: "chrome", Status: StatusOK}
	if p, err := browser.DetectChrome(cfg.Browser.ChromePath, d.lookPath, d.getenv, runtime.GOOS); err != nil {
		chrome.Status = StatusWarn
		chrome.Detail = "not found, so the browser_* tools fail"
		chrome.Next = "install chromium (apt install chromium), or set browser.chrome_path"
	} else {
		chrome.Detail = p
	}
	out = append(out, chrome)

	out = append(out,
		d.program("python3", "run_script is off", "install python3 (apt install python3)"),
		d.program("pdftotext", "the agent cannot read the text of PDF files",
			"install poppler-utils (apt install poppler-utils)"))

	ffmpeg := d.program("ffmpeg", "Telegram voice replies go as audio files, and whisper-cli cannot convert voice notes",
		"install ffmpeg (apt install ffmpeg)")
	if ffmpeg.Status != StatusOK && !voiceOn(cfg) {
		ffmpeg = Check{Name: "ffmpeg", Status: StatusOK, Detail: "not found; Telegram voice is off, so nothing needs it"}
	}
	out = append(out, ffmpeg)

	if prog := commandProgram(cfg.Media.TTS); prog != "" {
		out = append(out, d.program(prog, "text_to_speech fails", installHint(prog)+", or change media.tts"))
	}
	if prog := commandProgram(cfg.Media.STT); prog != "" {
		out = append(out, d.program(prog, "transcribe_audio and the voice note transcripts fail",
			installHint(prog)+", or change media.stt"))
	}
	return out
}

// installHint returns the install action for the program of a media command.
func installHint(prog string) string {
	switch filepath.Base(prog) {
	case "edge-tts":
		return "install edge-tts (pipx install edge-tts)"
	case "whisper-cli":
		return "build whisper-cli from whisper.cpp and put it on PATH"
	default:
		return "install " + prog
	}
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
		cctx, cancel := context.WithTimeout(ctx, checkTimeout)
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

// checkDisk checks the free disk space of the data folder.
func (d *Doctor) checkDisk() Check {
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
	if n < minFreeBytes {
		c.Status = StatusWarn
		c.Next = "free some disk space; the logs, the cache, and state.db need room"
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
