package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
)

// defaultMaxOutputBytes is the in-memory capture limit of one command. The
// agent loop truncates the result further and spills the full capture.
const defaultMaxOutputBytes = 8 << 20

// waitDelay is the time that Exec waits for the output pipes to close after
// bash exits. A child that bash left in the background can hold the pipes open.
const waitDelay = 2 * time.Second

// ExecTool is the run_command tool.
type ExecTool struct {
	// DefaultTimeout applies when the call gives no timeout_s.
	DefaultTimeout time.Duration
	// DotEnv holds the .env values. ChildEnv removes these names from the child.
	DotEnv map[string]string
	// Passthrough returns the secret names that the child may keep. It may be nil.
	Passthrough func(env *Env) []string
	// SpillDir is the folder for the logs of background commands (cache/spill).
	SpillDir string
	// MaxOutputBytes limits the captured output. Zero uses 8 MiB. Exec keeps
	// the head and the tail and drops the middle.
	MaxOutputBytes int
}

type execArgs struct {
	Command    string `json:"command"`
	TimeoutS   int    `json:"timeout_s"`
	Workdir    string `json:"workdir"`
	Background bool   `json:"background"`
}

// Name returns "run_command".
func (t *ExecTool) Name() string { return "run_command" }

// Toolset returns the terminal toolset.
func (t *ExecTool) Toolset() string { return SetTerminal }

// ParallelSafe returns false, because commands can change shared state.
func (t *ExecTool) ParallelSafe() bool { return false }

// Schema returns the tool schema for the model.
func (t *ExecTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{
		Name: "run_command",
		Description: "Run a shell command with bash -c on the host. The result is stdout and stderr together, " +
			"followed by the exit code when it is not 0. Set background to true for servers and long jobs: " +
			"the tool then returns the pid and a log path at once. Read the log with read_file and stop the process with kill.",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "The shell command to run."},
    "timeout_s": {"type": "integer", "description": "The timeout in seconds. The default is ` + strconv.Itoa(int(t.defaultTimeout().Seconds())) + `."},
    "workdir": {"type": "string", "description": "The working folder. A relative path starts at the session working folder."},
    "background": {"type": "boolean", "description": "Start the command in the background and return its pid and log path at once."}
  },
  "required": ["command"]
}`),
	}
}

func (t *ExecTool) defaultTimeout() time.Duration {
	if t.DefaultTimeout > 0 {
		return t.DefaultTimeout
	}
	return 120 * time.Second
}

// Run runs the command of the call.
func (t *ExecTool) Run(ctx context.Context, raw json.RawMessage, env *Env) (string, error) {
	var a execArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Command) == "" {
		return "", toolErr("command is required")
	}
	// The gate runs before each start, also in background mode. With no gate, the
	// command does not run (fail closed).
	if env == nil || env.Gate == nil {
		return "", toolErr("approval gate missing: run_command cannot run a command without the approval gate")
	}
	if err := env.Gate.CheckCommand(ctx, env, a.Command); err != nil {
		return "", toolErr("%s", err.Error())
	}
	if a.Background {
		return t.startBackground(env, a.Command, a.Workdir)
	}
	timeout := t.defaultTimeout()
	if a.TimeoutS > 0 {
		timeout = time.Duration(a.TimeoutS) * time.Second
	}
	out, _, err := t.Exec(ctx, env, a.Command, a.Workdir, timeout)
	return out, err
}

// Exec runs command with bash -c in its own process group and waits for it.
// The output is stdout and stderr in one buffer, followed by
// "[exit code: N]" when N is not 0. A timeout kills the whole process group
// and adds "[timed out after Ns]". The exit code is -1 after a timeout or a
// signal. Exec returns an error when the command cannot start or when ctx
// ends before the command.
func (t *ExecTool) Exec(ctx context.Context, env *Env, command, workdir string, timeout time.Duration) (output string, exitCode int, err error) {
	dir, err := resolveWorkdir(env, workdir)
	if err != nil {
		return "", -1, err
	}
	buf := newCapBuffer(t.MaxOutputBytes)
	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = dir
	cmd.Env = t.childEnv(env)
	cmd.Stdout = buf
	cmd.Stderr = buf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = waitDelay
	if err := cmd.Start(); err != nil {
		return "", -1, toolErr("cannot start bash: %v", err)
	}
	pgid := cmd.Process.Pid

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	if timeout <= 0 {
		timeout = t.defaultTimeout()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var waitErr error
	timedOut := false
	select {
	case waitErr = <-done:
	case <-timer.C:
		timedOut = true
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		waitErr = <-done
	case <-ctx.Done():
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-done
		return buf.String(), -1, fmt.Errorf("command stopped: %w", ctx.Err())
	}

	var b strings.Builder
	b.WriteString(buf.String())
	exitCode = 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	switch {
	case timedOut:
		exitCode = -1
		appendNote(&b, fmt.Sprintf("[timed out after %s]", formatSeconds(timeout)))
	case exitCode == -1:
		if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			appendNote(&b, fmt.Sprintf("[killed by signal %s]", ws.Signal()))
		} else if waitErr != nil && !errors.Is(waitErr, exec.ErrWaitDelay) {
			appendNote(&b, fmt.Sprintf("[wait failed: %v]", waitErr))
		}
	case exitCode != 0:
		appendNote(&b, fmt.Sprintf("[exit code: %d]", exitCode))
	}
	if b.Len() == 0 {
		b.WriteString("[no output]")
	}
	return b.String(), exitCode, nil
}

// startBackground starts command in a new session with its output in
// SpillDir/bg-<pid>.log, and returns the pid and the log path as JSON. A
// goroutine reaps the process.
func (t *ExecTool) startBackground(env *Env, command, workdir string) (string, error) {
	dir, err := resolveWorkdir(env, workdir)
	if err != nil {
		return "", err
	}
	spill := t.SpillDir
	if spill == "" && env != nil && env.DataDir != "" {
		spill = filepath.Join(env.DataDir, "cache", "spill")
	}
	if spill == "" {
		return "", toolErr("background mode needs a spill folder, and none is configured")
	}
	if err := os.MkdirAll(spill, 0o700); err != nil {
		return "", toolErr("cannot create the spill folder %s: %v", spill, err)
	}
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	tmpPath := filepath.Join(spill, "bg-start-"+hex.EncodeToString(rnd[:])+".log")
	logFile, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", toolErr("cannot create the log file: %v", err)
	}
	// The child gets its own copy of the log descriptor and no other parent
	// descriptor, so it never holds the stdout or stderr of the agent open.
	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = dir
	cmd.Env = t.childEnv(env)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	startErr := cmd.Start()
	logFile.Close()
	if startErr != nil {
		os.Remove(tmpPath)
		return "", toolErr("cannot start bash: %v", startErr)
	}
	pid := cmd.Process.Pid
	logPath := filepath.Join(spill, fmt.Sprintf("bg-%d.log", pid))
	if err := os.Rename(tmpPath, logPath); err != nil {
		logPath = tmpPath
	}
	go func() { _ = cmd.Wait() }()
	b, _ := json.Marshal(struct {
		PID     int    `json:"pid"`
		LogPath string `json:"log_path"`
	}{pid, logPath})
	return string(b), nil
}

// GateApprover is the Approver of run_command. It checks each command with Gate and asks
// the user through Prompter. A nil Prompter blocks each dangerous command.
type GateApprover struct {
	Gate     *security.Gate
	Prompter security.Prompter
}

// CheckCommand builds the approval request from env and returns nil when command may
// run. Otherwise it returns the reason for the model.
func (g GateApprover) CheckCommand(ctx context.Context, env *Env, command string) error {
	if g.Gate == nil {
		return errors.New("approval gate missing: the command cannot run without the approval gate")
	}
	req := security.ApprovalRequest{Command: command}
	if env != nil {
		req.SessionID, req.SessionKey, req.Kind, req.RootKind = env.SessionID, env.SessionKey, env.Kind, env.RootKind
		req.ChildName = childName(env)
	}
	return g.Gate.Check(ctx, req, g.Prompter)
}

// ResetSession removes the "This session" answers of sessionID from the gate. A run with
// its own session calls it when the run ends, so the gate does not keep the answers.
func (g GateApprover) ResetSession(sessionID string) {
	if g.Gate != nil {
		g.Gate.ResetSession(sessionID)
	}
}

// SessionResetter is an Approver that can remove the session answers of a run.
type SessionResetter interface {
	ResetSession(sessionID string)
}

// childName returns the child name in env.Values. Parallel-safe tools can change
// env.Values at the same time, so the read holds valuesMu.
func childName(env *Env) string {
	valuesMu.Lock()
	defer valuesMu.Unlock()
	name, _ := env.Values[childNameKey].(string)
	return name
}

// childNameKey is the Env.Values key of the child name (agent.ChildNameKey).
const childNameKey = "child_name"

// childEnv returns the environment for a child process, without secrets.
func (t *ExecTool) childEnv(env *Env) []string {
	var pass []string
	if t.Passthrough != nil {
		pass = t.Passthrough(env)
	}
	return security.ChildEnv(os.Environ(), t.DotEnv, pass)
}

// resolveWorkdir returns the folder for a command. A relative workdir starts
// at env.WorkDir. An empty workdir is env.WorkDir.
func resolveWorkdir(env *Env, workdir string) (string, error) {
	base := ""
	if env != nil {
		base = env.WorkDir
	}
	dir := workdir
	if dir == "" {
		dir = base
	} else {
		dir = resolvePath(env, expandHome(dir))
	}
	if dir == "" {
		return "", nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", toolErr("workdir %s does not exist", dir)
	}
	if !info.IsDir() {
		return "", toolErr("workdir %s is not a folder", dir)
	}
	return dir, nil
}

// appendNote adds note on its own line.
func appendNote(b *strings.Builder, note string) {
	if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
		b.WriteByte('\n')
	}
	b.WriteString(note)
}

// formatSeconds returns d as whole seconds, for example "1s".
func formatSeconds(d time.Duration) string {
	return strconv.FormatInt(int64(d.Round(time.Second)/time.Second), 10) + "s"
}

// capBuffer is a thread-safe writer that keeps the first and the last bytes
// of the output and counts the dropped bytes between them.
type capBuffer struct {
	mu      sync.Mutex
	headMax int
	tailMax int
	head    []byte
	tail    []byte // ring buffer
	tailPos int
	tailLen int
	dropped int64
}

func newCapBuffer(max int) *capBuffer {
	if max <= 0 {
		max = defaultMaxOutputBytes
	}
	return &capBuffer{headMax: max / 2, tailMax: max - max/2}
}

// Write keeps p in the head until the head is full, then in the tail ring.
func (c *capBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	if room := c.headMax - len(c.head); room > 0 {
		k := min(room, len(p))
		c.head = append(c.head, p[:k]...)
		p = p[k:]
	}
	if len(p) == 0 || c.tailMax == 0 {
		c.dropped += int64(len(p))
		return n, nil
	}
	if c.tail == nil {
		c.tail = make([]byte, c.tailMax)
	}
	if len(p) > c.tailMax {
		c.dropped += int64(len(p) - c.tailMax)
		p = p[len(p)-c.tailMax:]
	}
	for _, by := range p {
		if c.tailLen == c.tailMax {
			c.dropped++
		} else {
			c.tailLen++
		}
		c.tail[c.tailPos] = by
		c.tailPos = (c.tailPos + 1) % c.tailMax
	}
	return n, nil
}

// String returns the kept output, with a note in place of the dropped bytes.
func (c *capBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	b.Write(c.head)
	if c.dropped > 0 {
		fmt.Fprintf(&b, "\n[… %d bytes of output omitted …]\n", c.dropped)
	}
	if c.tailLen > 0 {
		start := (c.tailPos - c.tailLen + c.tailMax) % c.tailMax
		if start+c.tailLen <= c.tailMax {
			b.Write(c.tail[start : start+c.tailLen])
		} else {
			b.Write(c.tail[start:])
			b.Write(c.tail[:c.tailLen-(c.tailMax-start)])
		}
	}
	return b.String()
}
