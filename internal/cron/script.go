package cron

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dnwandana/tether-agent/internal/ctxmgr"
	"github.com/dnwandana/tether-agent/internal/security"
)

const (
	// DefaultScriptTimeout is the time limit of a script run.
	DefaultScriptTimeout = 120 * time.Second
	// ScriptOutputMaxChars is the maximum number of stdout characters that a script result
	// keeps. ctxmgr.Truncate writes the full stdout to a spill file.
	ScriptOutputMaxChars = 50_000
	// scriptStdoutMaxBytes stops a runaway script from filling the memory. ctxmgr.Truncate
	// then cuts the kept text to ScriptOutputMaxChars.
	scriptStdoutMaxBytes = 8 << 20
	// scriptStderrMaxBytes is the part of stderr that the run keeps for the tail.
	scriptStderrMaxBytes = 64 << 10
	// scriptTailLines is the number of stderr lines in a failure alert.
	scriptTailLines = 20
	// scriptWaitDelay is the time that Wait gives to a child that keeps a pipe open.
	scriptWaitDelay = 2 * time.Second
)

// ScriptResult is the result of one script run.
type ScriptResult struct {
	// Stdout is the stdout of the script, cut to ScriptOutputMaxChars.
	Stdout string
	// StderrTail is the last 20 lines of stderr.
	StderrTail string
	// ExitCode is the exit status. It is -1 when the script did not start or a signal stopped it.
	ExitCode int
	TimedOut bool
	// Timeout is the time limit that applied to the run.
	Timeout time.Duration
}

// ResolveScript returns the absolute path of a script. A relative path resolves under
// scriptsDir. The file must exist, and its real path must stay in scriptsDir (Hermes).
// A script job runs with no approval, so a path outside scriptsDir is refused.
func ResolveScript(path, scriptsDir string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("cron: script path is empty")
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	dir, err := filepath.Abs(scriptsDir)
	if err != nil {
		return "", fmt.Errorf("cron: scripts folder %q: %w", scriptsDir, err)
	}
	p := path
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	p = filepath.Clean(p)
	fi, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("cron: script %s does not exist. Put the script in %s", p, dir)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("cron: script %s is not a file", p)
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("cron: scripts folder %s: %w", dir, err)
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("cron: script %s: %w", p, err)
	}
	if rel, err := filepath.Rel(realDir, real); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("cron: script %s is outside the scripts folder %s. Put the script in that folder", p, dir)
	}
	return p, nil
}

// RunScriptSpill runs the script at path. A .sh or .bash file runs with bash, and other files
// run with python3. The script runs in workdir, or in its own folder when workdir is empty.
// A nil env gives the process environment without secrets. A timeout of 0 means 120 s. On
// timeout, the function kills the full process group. A stdout above ScriptOutputMaxChars goes
// to a spill file in spillDir. An empty spillDir means <temp>/tether-spill.
func RunScriptSpill(ctx context.Context, path, workdir string, env []string, timeout time.Duration, spillDir string) ScriptResult {
	if timeout <= 0 {
		timeout = DefaultScriptTimeout
	}
	res := ScriptResult{ExitCode: -1, Timeout: timeout}
	if env == nil {
		env = security.ChildEnv(os.Environ(), nil, nil)
	}
	if workdir == "" {
		workdir = filepath.Dir(path)
	}
	if spillDir == "" {
		spillDir = filepath.Join(os.TempDir(), "tether-spill")
	}
	interp, err := interpreter(path)
	if err != nil {
		res.StderrTail = err.Error()
		return res
	}

	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(tctx, interp, path)
	cmd.Dir = workdir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = scriptWaitDelay
	stdout := &capBuffer{max: scriptStdoutMaxBytes}
	stderr := &tailBuffer{max: scriptStderrMaxBytes}
	cmd.Stdout, cmd.Stderr = stdout, stderr

	if err := cmd.Start(); err != nil {
		res.StderrTail = fmt.Sprintf("cron: start script %s: %v", path, err)
		return res
	}
	err = cmd.Wait()
	if errors.Is(err, exec.ErrWaitDelay) {
		// A child in the group still holds a pipe. Stop the group so that no process stays.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	res.TimedOut = errors.Is(tctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	errText := stderr.String()
	if ctx.Err() != nil {
		errText += "\ncron: the script run was canceled"
	}
	res.StderrTail = lastLines(errText, scriptTailLines)

	out := stdout.String()
	if stdout.cut {
		out += fmt.Sprintf("\n[stdout was cut at %d bytes]\n", scriptStdoutMaxBytes)
	}
	capped, terr := ctxmgr.Truncate(out, ScriptOutputMaxChars, spillDir)
	if terr != nil {
		// Without a spill file, keep the head only. The marker must not name a file.
		capped = ctxmgr.SanitizeUTF8(out)
		if r := []rune(capped); len(r) > ScriptOutputMaxChars {
			capped = string(r[:ScriptOutputMaxChars]) + "\n[stdout was cut: the spill file could not be written]\n"
		}
	}
	res.Stdout = capped
	return res
}

// ScriptJobMessage returns the message of a script job. Non-empty stdout goes out word for
// word. Empty stdout after exit 0 sends nothing. A failure or a timeout always sends an
// alert with the tail of stderr, or the tail of stdout when stderr is empty.
func ScriptJobMessage(j Job, r ScriptResult) (text string, send bool) {
	if r.TimedOut || r.ExitCode != 0 {
		var b strings.Builder
		if r.TimedOut {
			fmt.Fprintf(&b, "⚠ Cron job %s failed (timed out after %s)", j.Name, seconds(r.Timeout))
		} else {
			fmt.Fprintf(&b, "⚠ Cron job %s failed (exit %d)", j.Name, r.ExitCode)
		}
		tail := strings.TrimRight(r.StderrTail, "\n")
		label := "stderr"
		if strings.TrimSpace(tail) == "" && strings.TrimSpace(r.Stdout) != "" {
			// With no stderr, the end of stdout is the only clue to the cause.
			tail, label = strings.TrimRight(lastLines(r.Stdout, scriptTailLines), "\n"), "stdout"
		}
		if strings.TrimSpace(tail) != "" {
			fmt.Fprintf(&b, "\n%s:\n%s", label, tail)
		}
		return b.String(), true
	}
	if strings.TrimSpace(r.Stdout) == "" {
		return "", false
	}
	return r.Stdout, true
}

func interpreter(path string) (string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".sh", ".bash":
		if p, err := exec.LookPath("bash"); err == nil {
			return p, nil
		}
		if _, err := os.Stat("/bin/bash"); err == nil {
			return "/bin/bash", nil
		}
		return "", fmt.Errorf("cron: cannot run %s: bash is not on PATH", filepath.Base(path))
	default:
		p, err := exec.LookPath("python3")
		if err != nil {
			return "", fmt.Errorf("cron: cannot run %s: python3 is not on PATH. Install python3, or use a .sh script",
				filepath.Base(path))
		}
		return p, nil
	}
}

// seconds formats d as "120s", or with the Duration format when d is not whole seconds.
func seconds(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%ds", int64(d/time.Second))
	}
	return d.String()
}

// lastLines returns the last n lines of s.
func lastLines(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	i := len(s)
	for range n {
		j := strings.LastIndexByte(s[:i], '\n')
		if j < 0 {
			return s + "\n"
		}
		i = j
	}
	return s[i+1:] + "\n"
}

// capBuffer keeps the first max bytes and drops the rest.
type capBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
	cut bool
}

func (c *capBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := c.max - c.buf.Len(); room < len(p) {
		c.cut = true
		if room > 0 {
			c.buf.Write(p[:room])
		}
		return len(p), nil
	}
	c.buf.Write(p)
	return len(p), nil
}

func (c *capBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// tailBuffer keeps the last max bytes.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
