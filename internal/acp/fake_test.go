package acp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/dnwandana/tether-agent/internal/config"
)

// fakeScript is the script of testdata/fakeagent. Keep it the same as the
// script type in testdata/fakeagent/main.go.
type fakeScript struct {
	ProtocolVersion int               `json:"protocol_version,omitempty"`
	LoadSession     bool              `json:"load_session,omitempty"`
	Resume          bool              `json:"resume,omitempty"`
	Close           bool              `json:"close,omitempty"`
	ModeConfig      bool              `json:"mode_config,omitempty"`
	Modes           []string          `json:"modes,omitempty"`
	ModeGroups      bool              `json:"mode_groups,omitempty"`
	EffectiveMode   map[string]string `json:"effective_mode,omitempty"`
	Models          []string          `json:"models,omitempty"`
	AuthRequired    bool              `json:"auth_required,omitempty"`
	SessionID       string            `json:"session_id,omitempty"`
	Replay          []fakeStep        `json:"replay,omitempty"`
	Turns           []fakeTurn        `json:"turns,omitempty"`
	AfterTurn       []fakeStep        `json:"after_turn,omitempty"`
	ExitOnEOF       bool              `json:"exit_on_eof,omitempty"`
	SpawnGrandchild bool              `json:"spawn_grandchild,omitempty"`
	Stderr          string            `json:"stderr,omitempty"`
	CrashOnPrompt   bool              `json:"crash_on_prompt,omitempty"`
}

type fakeTurn struct {
	Steps      []fakeStep `json:"steps"`
	StopReason string     `json:"stop_reason,omitempty"`
}

type fakeToolCall struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind,omitempty"`
	Title     string         `json:"title,omitempty"`
	RawInput  map[string]any `json:"raw_input,omitempty"`
	Locations []string       `json:"locations,omitempty"`
	Diff      *fakeDiff      `json:"diff,omitempty"`
}

type fakeDiff struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

type fakeOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type fakePermission struct {
	ToolCall fakeToolCall `json:"tool_call"`
	Options  []fakeOption `json:"options"`
}

// fakeStep is one action of the fake agent. Set exactly one field.
type fakeStep struct {
	ToolCall    *fakeToolCall   `json:"tool_call,omitempty"`
	Message     string          `json:"message,omitempty"`
	Permission  *fakePermission `json:"permission,omitempty"`
	SleepMS     int             `json:"sleep_ms,omitempty"`
	WaitCancel  bool            `json:"wait_cancel,omitempty"`
	Flood       int             `json:"flood,omitempty"`
	NilToolCall bool            `json:"nil_tool_call,omitempty"`
	ModeUpdate  string          `json:"mode_update,omitempty"`
}

// fakeEvent is one line of the log of the fake agent.
type fakeEvent struct {
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Outcome string          `json:"outcome"`
	PID     int             `json:"pid"`
}

var (
	fakeOnce sync.Once
	fakeBin  string
	fakeErr  error
	fakeDir  string
)

func TestMain(m *testing.M) {
	code := m.Run()
	if fakeDir != "" {
		os.RemoveAll(fakeDir)
	}
	os.Exit(code)
}

// buildFakeAgent builds testdata/fakeagent once and returns the path of the binary.
func buildFakeAgent(t *testing.T) string {
	t.Helper()
	fakeOnce.Do(func() {
		goBin, err := exec.LookPath("go")
		if err != nil {
			fakeErr = err
			return
		}
		fakeDir, fakeErr = os.MkdirTemp("", "tether-acp-test")
		if fakeErr != nil {
			return
		}
		fakeBin = filepath.Join(fakeDir, "fakeagent")
		cmd := exec.Command(goBin, "build", "-o", fakeBin, "./testdata/fakeagent")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			fakeErr = errors.New(string(out))
		}
	})
	if errors.Is(fakeErr, exec.ErrNotFound) {
		t.Skip("go is not on PATH")
	}
	if fakeErr != nil {
		t.Fatal(fakeErr)
	}
	return fakeBin
}

// fakeAgentConfig writes s to a file and returns an agent entry that starts
// the fake agent with that script, and the path of its log.
func fakeAgentConfig(t *testing.T, s fakeScript) (config.ACPAgent, string) {
	t.Helper()
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "script.json")
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "events.jsonl")
	a := config.ACPAgent{
		Enabled:          true,
		Command:          []string{buildFakeAgent(t)},
		Env:              map[string]string{"TETHER_FAKEAGENT_SCRIPT": scriptPath, "TETHER_FAKEAGENT_LOG": logPath},
		PermissionPreset: "auto",
		Modes:            config.ACPCatalog()["claude-code"].Modes,
	}
	return a, logPath
}

// readFakeLog returns the events in the log. A missing log gives no events.
func readFakeLog(t *testing.T, path string) []fakeEvent {
	t.Helper()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var evs []fakeEvent
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var e fakeEvent
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("fake log line %q: %v", sc.Text(), err)
		}
		evs = append(evs, e)
	}
	return evs
}

// fakeEventsOf returns the events of one method, in log order.
func fakeEventsOf(evs []fakeEvent, method string) []fakeEvent {
	var out []fakeEvent
	for _, e := range evs {
		if e.Method == method {
			out = append(out, e)
		}
	}
	return out
}

// pollFakeLog waits up to 5 s until the log has n events of method.
func pollFakeLog(t *testing.T, path, method string, n int) []fakeEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		evs := fakeEventsOf(readFakeLog(t, path), method)
		if len(evs) >= n {
			return evs
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake log has %d %q events, want %d", len(evs), method, n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestFakeAgentAnswersHandshake(t *testing.T) {
	a, logPath := fakeAgentConfig(t, fakeScript{ExitOnEOF: true, SpawnGrandchild: true, Stderr: "fake stderr line"})
	cmd := exec.Command(a.Command[0])
	cmd.Env = append(os.Environ(),
		"TETHER_FAKEAGENT_SCRIPT="+a.Env["TETHER_FAKEAGENT_SCRIPT"], "TETHER_FAKEAGENT_LOG="+logPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// The kill of the group also stops the grandchild.
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })

	enc, dec := json.NewEncoder(stdin), json.NewDecoder(stdout)
	call := func(id int, method string, params, result any) {
		t.Helper()
		if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		var resp struct {
			Result json.RawMessage      `json:"result"`
			Error  *acpsdk.RequestError `json:"error"`
		}
		if err := dec.Decode(&resp); err != nil {
			t.Fatal(err)
		}
		if resp.Error != nil {
			t.Fatalf("%s: %v", method, resp.Error)
		}
		if err := json.Unmarshal(resp.Result, result); err != nil {
			t.Fatal(err)
		}
	}
	var init acpsdk.InitializeResponse
	call(1, "initialize", acpsdk.InitializeRequest{ProtocolVersion: acpsdk.ProtocolVersionNumber}, &init)
	if init.ProtocolVersion != 1 || init.AgentInfo == nil || init.AgentInfo.Version != "0.0.1" {
		t.Fatalf("initialize = %+v", init)
	}
	var sess acpsdk.NewSessionResponse
	call(2, "session/new", acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}}, &sess)
	if sess.SessionId != "fake-session-1" || sess.Modes == nil || sess.Modes.CurrentModeId != "default" {
		t.Fatalf("session/new = %+v", sess)
	}

	stdin.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the fake agent did not exit 0 on EOF: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the fake agent did not exit on EOF")
	}
	if !strings.Contains(stderr.String(), "fake stderr line") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	evs := readFakeLog(t, logPath)
	var methods []string
	for _, e := range evs {
		methods = append(methods, e.Method)
	}
	if got := strings.Join(methods, ","); got != "grandchild,initialize,session/new" {
		t.Fatalf("log methods = %s", got)
	}
	if evs[0].PID <= 0 {
		t.Fatalf("grandchild pid = %d", evs[0].PID)
	}
}
