package tools

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"text/template"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
)

// The defaults of execute_code. They follow the limits of the Hermes tool of the same name.
const (
	defaultScriptTimeoutS = 300
	defaultScriptStdout   = 50000
	// scriptStderrMax limits the stderr text that a failed script returns.
	scriptStderrMax = 10000
	// maxSockPath keeps the socket path below the sun_path limit (104 bytes on macOS).
	maxSockPath = 100
)

//go:embed tether_tools.py.tmpl
var tetherToolsTmpl string

var stubTemplate = template.Must(template.New("tether_tools.py").Parse(tetherToolsTmpl))

// ExecuteCodeTool is the execute_code tool, like the Hermes tool of the same name. It runs a Python
// script that can call some tools through the tether_tools module. Only the stdout of the
// script returns to the model.
type ExecuteCodeTool struct {
	Reg *Registry
	// ScratchRoot is the folder for the scratch folders of the runs (<data>/cache).
	ScratchRoot string
	// TimeoutS is the maximum run time (300). MaxCalls limits the tool calls of one script
	// (50). MaxStdout limits the returned stdout in bytes (50,000). Zero uses the default.
	TimeoutS, MaxCalls, MaxStdout int
	// Python is the python3 program (exec.LookPath("python3")). Empty refuses each call.
	Python string
	// DotEnv holds the .env values. The script gets no secret.
	DotEnv map[string]string
	// Passthrough returns the secret names that the script may keep. It may be nil.
	Passthrough func(env *Env) []string
	// InGateway is true in the gateway process. The script then gets GatewayEnv,
	// except in a run that cron started.
	InGateway bool
}

type executeCodeArgs struct {
	Code     string `json:"code"`
	TimeoutS int    `json:"timeout_s"`
}

// Name returns "execute_code".
func (t ExecuteCodeTool) Name() string { return "execute_code" }

// Toolset returns the code_execution toolset.
func (t ExecuteCodeTool) Toolset() string { return SetCodeExecution }

// ParallelSafe returns false, because a script can change shared state.
func (t ExecuteCodeTool) ParallelSafe() bool { return false }

// Schema returns the tool schema for the model.
func (t ExecuteCodeTool) Schema() provider.ToolSchema {
	timeout := positiveOr(t.TimeoutS, defaultScriptTimeoutS)
	return provider.ToolSchema{
		Name: "execute_code",
		Description: "Run a Python script that calls tools in a loop, and get back only what the script prints. " +
			"Use it for mechanical multi-step work: many fetches, file edits in a loop, or filtering large tool output. " +
			"The intermediate tool results never enter your context.\n\n" +
			"Import the tools from the tether_tools module, for example:\n" +
			"  from tether_tools import web_fetch, ToolError\n" +
			"  page = web_fetch(urls=['https://example.com'])\n" +
			"Each function takes the same arguments as the tool, returns the result text, and raises ToolError " +
			"when the call fails. The callable tools are " + strings.Join(ScriptCallableTools, ", ") +
			", when they are available in this session.\n\n" +
			fmt.Sprintf("LIMITS: %d s, %d tool calls, and %d bytes of stdout. The script runs in a scratch folder "+
				"without secrets in its environment. Print only the result that you need.",
				timeout, positiveOr(t.MaxCalls, defaultRPCMaxCalls), positiveOr(t.MaxStdout, defaultScriptStdout)),
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "code": {"type": "string", "description": "The Python 3 script."},
    "timeout_s": {"type": "integer", "description": "The timeout in seconds. The maximum and the default is ` + fmt.Sprint(timeout) + `."}
  },
  "required": ["code"]
}`),
	}
}

// timeout returns the run time limit for the requested timeout_s. A value above TimeoutS
// is clamped.
func (t ExecuteCodeTool) timeout(requested int) time.Duration {
	limit := positiveOr(t.TimeoutS, defaultScriptTimeoutS)
	if requested <= 0 || requested > limit {
		requested = limit
	}
	return time.Duration(requested) * time.Second
}

// Run checks the script with the approval gate, then runs it.
func (t ExecuteCodeTool) Run(ctx context.Context, raw json.RawMessage, env *Env) (string, error) {
	var a executeCodeArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Code) == "" {
		return "", toolErr("code is required")
	}
	if t.Python == "" {
		return "", toolErr("python3 is not installed, so execute_code cannot run. Use the tools directly")
	}
	// A script can start processes without the terminal tool, so the gate checks the whole
	// script text first. With no gate, the script does not run (fail closed).
	if env == nil || env.Gate == nil {
		return "", toolErr("approval gate missing: execute_code cannot run a script without the approval gate")
	}
	if err := env.Gate.CheckCommand(ctx, env, a.Code); err != nil {
		return "", toolErr("%s", err.Error())
	}

	if err := os.MkdirAll(t.ScratchRoot, 0o700); err != nil {
		return "", toolErr("cannot create the scratch root %s: %v", t.ScratchRoot, err)
	}
	dir, err := os.MkdirTemp(t.ScratchRoot, "rs-")
	if err != nil {
		return "", toolErr("cannot create the scratch folder: %v", err)
	}
	defer os.RemoveAll(dir)

	_, names := TurnTools(env)
	stub, err := t.stubModule(names)
	if err != nil {
		return "", toolErr("cannot write the tether_tools module: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tether_tools.py"), stub, 0o600); err != nil {
		return "", toolErr("cannot write the tether_tools module: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte(a.Code), 0o600); err != nil {
		return "", toolErr("cannot write the script: %v", err)
	}

	sockDir, cleanup, err := socketDir(dir)
	if err != nil {
		return "", toolErr("cannot create the socket folder: %v", err)
	}
	defer cleanup()
	srv := &RPCServer{Reg: t.Reg, Env: env, Visible: names, MaxCalls: t.MaxCalls, Ctx: ctx}
	sock, stop, err := srv.Listen(sockDir)
	if err != nil {
		return "", toolErr("%v", err)
	}
	defer stop()

	return t.runPython(ctx, env, dir, sock, t.timeout(a.TimeoutS))
}

// socketDir returns the folder for the socket. When dir would give a socket path that is
// too long for a Unix socket, it makes a private folder under /tmp. cleanup removes that
// folder.
func socketDir(dir string) (string, func(), error) {
	if len(filepath.Join(dir, rpcSockName)) <= maxSockPath {
		return dir, func() {}, nil
	}
	short, err := os.MkdirTemp("/tmp", "tether-rs-")
	if err != nil {
		return "", nil, err
	}
	return short, func() { os.RemoveAll(short) }, nil
}

// runPython runs main.py in dir and returns the result text: the stdout, cut at MaxStdout,
// and notes for a timeout or a failure.
func (t ExecuteCodeTool) runPython(ctx context.Context, env *Env, dir, sock string, timeout time.Duration) (string, error) {
	maxOut := positiveOr(t.MaxStdout, defaultScriptStdout)
	stdout := &headBuffer{max: maxOut}
	stderr := &headBuffer{max: scriptStderrMax}
	cmd := exec.Command(t.Python, "main.py")
	cmd.Dir = dir
	cmd.Env = append(t.childEnv(env),
		"TETHER_RPC_SOCK="+sock,
		"PYTHONDONTWRITEBYTECODE=1",
		"PYTHONIOENCODING=utf-8",
		"PYTHONUTF8=1",
	)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = waitDelay
	if err := cmd.Start(); err != nil {
		return "", toolErr("cannot start %s: %v", t.Python, err)
	}
	pgid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	timedOut := false
	select {
	case <-done:
	case <-timer.C:
		timedOut = true
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-done
	case <-ctx.Done():
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-done
		return "", fmt.Errorf("script stopped: %w", ctx.Err())
	}

	var b strings.Builder
	b.WriteString(stdout.String())
	if stdout.total > maxOut {
		appendNote(&b, fmt.Sprintf("[stdout truncated at %d bytes]", maxOut))
	}
	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	switch {
	case timedOut:
		appendNote(&b, fmt.Sprintf("[timed out after %s]", formatSeconds(timeout)))
	case exitCode != 0:
		if s := strings.TrimRight(stderr.String(), "\n"); s != "" {
			appendNote(&b, "--- stderr ---\n"+s)
		}
		appendNote(&b, fmt.Sprintf("[exit code: %d]", exitCode))
	}
	if b.Len() == 0 {
		b.WriteString("[no output]")
	}
	return b.String(), nil
}

// childEnv returns the environment of the script, without secrets.
func (t ExecuteCodeTool) childEnv(env *Env) []string {
	var pass []string
	if t.Passthrough != nil {
		pass = t.Passthrough(env)
	}
	out := security.ChildEnv(os.Environ(), t.DotEnv, pass)
	if t.InGateway && env.RunRootKind() != "cron" {
		out = append(out, GatewayEnv)
	}
	return out
}

// stubFunc is one function of the tether_tools module.
type stubFunc struct {
	Name, Signature, Doc string
}

// pyIdent matches a Python identifier that can be a parameter name.
var pyIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// pyKeywords cannot be parameter names. A tool argument with such a name goes through
// **kwargs.
var pyKeywords = map[string]bool{
	"False": true, "None": true, "True": true, "and": true, "as": true, "assert": true, "async": true,
	"await": true, "break": true, "class": true, "continue": true, "def": true, "del": true, "elif": true,
	"else": true, "except": true, "finally": true, "for": true, "from": true, "global": true, "if": true,
	"import": true, "in": true, "is": true, "lambda": true, "nonlocal": true, "not": true, "or": true,
	"pass": true, "raise": true, "return": true, "try": true, "while": true, "with": true, "yield": true,
	"kwargs": true,
}

// stubModule returns the tether_tools module with one function for each tool that is in
// ScriptCallableTools, registered, and in visible.
func (t ExecuteCodeTool) stubModule(visible map[string]bool) ([]byte, error) {
	var funcs []stubFunc
	for _, name := range ScriptCallableTools {
		if !visible[name] || t.Reg == nil {
			continue
		}
		tool, ok := t.Reg.Get(name)
		if !ok {
			continue
		}
		s := tool.Schema()
		funcs = append(funcs, stubFunc{Name: name, Signature: pySignature(s.Parameters), Doc: pyDoc(s.Description)})
	}
	var b bytes.Buffer
	if err := stubTemplate.Execute(&b, funcs); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// pySignature returns the Python parameters for a JSON schema: the required properties
// first, then the optional properties with the default None, then **kwargs.
func pySignature(schema json.RawMessage) string {
	var s struct {
		Properties orderedKeys `json:"properties"`
		Required   []string    `json:"required"`
	}
	_ = json.Unmarshal(schema, &s)
	var req, opt []string
	for _, k := range s.Properties {
		if !pyIdent.MatchString(k) || pyKeywords[k] {
			continue
		}
		if slices.Contains(s.Required, k) {
			req = append(req, k)
		} else {
			opt = append(opt, k+"=None")
		}
	}
	return strings.Join(append(append(req, opt...), "**kwargs"), ", ")
}

// pyDoc returns the first line of a tool description for a Python docstring.
func pyDoc(desc string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(desc), "\n")
	line = strings.NewReplacer(`\`, `\\`, `"""`, `'''`).Replace(line)
	if utf8.RuneCountInString(line) > 200 {
		line = string([]rune(line)[:200]) + "..."
	}
	return line
}

// orderedKeys decodes the keys of a JSON object in their order.
type orderedKeys []string

func (o *orderedKeys) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok != json.Delim('{') {
		return errors.New("properties is not an object")
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}
		k, _ := key.(string)
		*o = append(*o, k)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return err
		}
	}
	return nil
}

// headBuffer keeps the first max bytes that it receives and counts all bytes.
type headBuffer struct {
	mu    sync.Mutex
	max   int
	buf   []byte
	total int
}

func (h *headBuffer) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.total += len(p)
	if room := h.max - len(h.buf); room > 0 {
		h.buf = append(h.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// String returns the kept bytes. A cut in the middle of a rune drops the partial rune.
func (h *headBuffer) String() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	b := h.buf
	if h.total > len(b) {
		for i := 0; i < utf8.UTFMax-1 && len(b) > 0; i++ {
			if r, size := utf8.DecodeLastRune(b); r != utf8.RuneError || size > 1 {
				break
			}
			b = b[:len(b)-1]
		}
	}
	return string(b)
}
