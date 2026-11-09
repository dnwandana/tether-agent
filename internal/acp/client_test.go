package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/dnwandana/tether-agent/internal/config"
)

// startFake starts the fake agent with script s and stops it at the end of the test.
func startFake(t *testing.T, s fakeScript, mod ...func(*StartOptions)) (*Client, string) {
	t.Helper()
	a, logPath := fakeAgentConfig(t, s)
	o := StartOptions{AgentID: "test-agent", Agent: a, Dir: t.TempDir(), ClientVersion: "test",
		Log: slog.New(slog.DiscardHandler)}
	for _, f := range mod {
		f(&o)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Start(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Stop() })
	return c, logPath
}

// recordHandler is a turnHandler that keeps what it gets.
type recordHandler struct {
	sid           string
	outcome       permissionOutcome
	panicOnUpdate bool

	mu      sync.Mutex
	updates []update
	perms   []permissionInput
}

func (h *recordHandler) sessionID() string { return h.sid }

func (h *recordHandler) onUpdate(u update) {
	if h.panicOnUpdate {
		panic("test panic")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.updates = append(h.updates, u)
}

func (h *recordHandler) onPermission(_ context.Context, in permissionInput) permissionOutcome {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.perms = append(h.perms, in)
	return h.outcome
}

func (h *recordHandler) got() ([]update, []permissionInput) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]update(nil), h.updates...), append([]permissionInput(nil), h.perms...)
}

// processAlive reports whether pid runs. A zombie counts as dead.
func processAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return false
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true // no /proc (darwin): kill said that the process exists
	}
	i := bytes.LastIndexByte(b, ')')
	return !(i >= 0 && i+2 < len(b) && b[i+2] == 'Z')
}

func TestStartHandshake(t *testing.T) {
	c, logPath := startFake(t, fakeScript{LoadSession: true, Resume: true, Close: true})
	want := Capabilities{ProtocolVersion: 1, LoadSession: true, Resume: true, Close: true, AgentVersion: "0.0.1"}
	if got := c.Capabilities(); got != want {
		t.Fatalf("Capabilities() = %+v, want %+v", got, want)
	}
	if c.AgentID() != "test-agent" {
		t.Fatalf("AgentID() = %q", c.AgentID())
	}
	evs := fakeEventsOf(readFakeLog(t, logPath), "initialize")
	if len(evs) != 1 {
		t.Fatalf("initialize events = %d", len(evs))
	}
	var p struct {
		ProtocolVersion int                            `json:"protocolVersion"`
		ClientInfo      struct{ Name, Version string } `json:"clientInfo"`
		Caps            struct {
			Fs struct {
				Read  bool `json:"readTextFile"`
				Write bool `json:"writeTextFile"`
			} `json:"fs"`
			Terminal bool `json:"terminal"`
		} `json:"clientCapabilities"`
	}
	if err := json.Unmarshal(evs[0].Params, &p); err != nil {
		t.Fatal(err)
	}
	if p.ProtocolVersion != 1 || p.ClientInfo.Name != "tether" || p.ClientInfo.Version != "test" ||
		p.Caps.Fs.Read || p.Caps.Fs.Write || p.Caps.Terminal {
		t.Fatalf("initialize params = %s", evs[0].Params)
	}
}

func TestStartRejectsProtocolVersion(t *testing.T) {
	a, _ := fakeAgentConfig(t, fakeScript{ProtocolVersion: 2, ExitOnEOF: true})
	_, err := Start(context.Background(), StartOptions{AgentID: "test-agent", Agent: a, Dir: t.TempDir(),
		Log: slog.New(slog.DiscardHandler)})
	if err == nil || !strings.Contains(err.Error(), "protocol version 2") {
		t.Fatalf("Start() error = %v", err)
	}
}

func TestStartUnknownProgram(t *testing.T) {
	a := config.ACPAgent{Enabled: true, Command: []string{filepath.Join(t.TempDir(), "no-such-agent")}}
	_, err := Start(context.Background(), StartOptions{AgentID: "test-agent", Agent: a, Dir: t.TempDir(),
		Log: slog.New(slog.DiscardHandler)})
	if err == nil || !strings.HasPrefix(err.Error(), "acp: cannot start test-agent: ") ||
		!strings.HasSuffix(err.Error(), ". Run tether doctor.") {
		t.Fatalf("Start() error = %v", err)
	}
}

func TestUnsupportedClientMethods(t *testing.T) {
	s := sdkClient{c: &Client{agentID: "test-agent"}}
	ctx := context.Background()
	calls := map[string]func() error{
		"read":    func() error { _, err := s.ReadTextFile(ctx, acpsdk.ReadTextFileRequest{}); return err },
		"write":   func() error { _, err := s.WriteTextFile(ctx, acpsdk.WriteTextFileRequest{}); return err },
		"create":  func() error { _, err := s.CreateTerminal(ctx, acpsdk.CreateTerminalRequest{}); return err },
		"output":  func() error { _, err := s.TerminalOutput(ctx, acpsdk.TerminalOutputRequest{}); return err },
		"release": func() error { _, err := s.ReleaseTerminal(ctx, acpsdk.ReleaseTerminalRequest{}); return err },
		"wait":    func() error { _, err := s.WaitForTerminalExit(ctx, acpsdk.WaitForTerminalExitRequest{}); return err },
		"kill":    func() error { _, err := s.KillTerminal(ctx, acpsdk.KillTerminalRequest{}); return err },
	}
	for name, call := range calls {
		var re *acpsdk.RequestError
		if err := call(); !errors.As(err, &re) || re.Code != -32601 {
			t.Errorf("%s: error = %v, want code -32601", name, err)
		}
	}
}

func TestInboundDispatch(t *testing.T) {
	ctx := context.Background()
	c := &Client{agentID: "test-agent", log: slog.New(slog.DiscardHandler)}
	s := sdkClient{c: c}
	opts := []acpsdk.PermissionOption{
		{OptionId: "allow-once", Name: "Allow", Kind: acpsdk.PermissionOptionKindAllowOnce},
		{OptionId: "reject", Name: "Reject", Kind: acpsdk.PermissionOptionKindRejectOnce},
	}
	req := acpsdk.RequestPermissionRequest{SessionId: "s1", Options: opts, ToolCall: acpsdk.ToolCallUpdate{
		ToolCallId: "t1", Kind: acpsdk.Ptr(acpsdk.ToolKindExecute), Title: new("Run tests"),
		RawInput:  map[string]any{"command": "go test ./...", "plan": "the plan"},
		Locations: []acpsdk.ToolCallLocation{{Path: "/w/a.go"}},
		Content:   []acpsdk.ToolCallContent{acpsdk.ToolDiffContent("/w/a.go", "new", "old")},
	}}
	cancelled := func(name string) {
		t.Helper()
		resp, err := s.RequestPermission(ctx, req)
		if err != nil || resp.Outcome.Cancelled == nil {
			t.Fatalf("%s: outcome = %+v, %v; want cancelled", name, resp.Outcome, err)
		}
	}
	cancelled("no handler")
	c.attach(&recordHandler{sid: "other"})
	cancelled("other session")

	h := &recordHandler{sid: "s1", outcome: permissionOutcome{OptionID: "allow-once"}}
	c.attach(h)
	resp, err := s.RequestPermission(ctx, req)
	if err != nil || resp.Outcome.Selected == nil || resp.Outcome.Selected.OptionId != "allow-once" {
		t.Fatalf("outcome = %+v, %v", resp.Outcome, err)
	}
	// A tool call with only an id has nil fields. The handler must not panic.
	bare := acpsdk.RequestPermissionRequest{SessionId: "s1", Options: opts, ToolCall: acpsdk.ToolCallUpdate{ToolCallId: "t2"}}
	if _, err := s.RequestPermission(ctx, bare); err != nil {
		t.Fatal(err)
	}
	_, perms := h.got()
	wantOpts := []option{{"allow-once", "Allow", "allow_once"}, {"reject", "Reject", "reject_once"}}
	want := []permissionInput{
		{SessionID: "s1", ToolCallID: "t1", Kind: "execute", Title: "Run tests", Command: "go test ./...",
			Plan: "the plan", Locations: []string{"/w/a.go"},
			Diffs: []diffItem{{Path: "/w/a.go", OldText: "old", NewText: "new"}}, Options: wantOpts},
		{SessionID: "s1", ToolCallID: "t2", Options: wantOpts},
	}
	if !reflect.DeepEqual(perms, want) {
		t.Fatalf("permissions = %+v\nwant %+v", perms, want)
	}

	for _, n := range []acpsdk.SessionNotification{
		{SessionId: "s1", Update: acpsdk.StartToolCall("t3", "Read a.go", acpsdk.WithStartKind(acpsdk.ToolKindRead))},
		{SessionId: "s1", Update: acpsdk.UpdateAgentMessageText("hello")},
		{SessionId: "s1", Update: acpsdk.SessionUpdate{CurrentModeUpdate: &acpsdk.SessionCurrentModeUpdate{CurrentModeId: "plan"}}},
		{SessionId: "s1", Update: acpsdk.UpdateAgentThoughtText("think")},
		{SessionId: "other", Update: acpsdk.UpdateAgentMessageText("dropped")},
	} {
		if err := s.SessionUpdate(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	ups, _ := h.got()
	wantUps := []update{
		{SessionID: "s1", Type: "tool_call", ToolKind: "read", Title: "Read a.go"},
		{SessionID: "s1", Type: "agent_message_chunk", Text: "hello"},
		{SessionID: "s1", Type: "current_mode_update", Mode: "plan"},
		{SessionID: "s1", Type: "other"},
	}
	if !reflect.DeepEqual(ups, wantUps) {
		t.Fatalf("updates = %+v\nwant %+v", ups, wantUps)
	}
	c.attach(nil)
	cancelled("detached")
}

func TestHandlerPanicStopsProcess(t *testing.T) {
	c, _ := startFake(t, fakeScript{ExitOnEOF: true})
	c.attach(&recordHandler{sid: "s1", panicOnUpdate: true})
	err := sdkClient{c: c}.SessionUpdate(context.Background(),
		acpsdk.SessionNotification{SessionId: "s1", Update: acpsdk.UpdateAgentMessageText("x")})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the client did not stop after the panic")
	}
	want := "acp: test-agent sent a message that Tether Agent cannot read."
	if got := c.stoppedErr(); got == nil || got.Error() != want {
		t.Fatalf("stoppedErr() = %v, want %q", got, want)
	}
}

func TestCrashShowsStderrTail(t *testing.T) {
	c, _ := startFake(t, fakeScript{Stderr: "boom: the fake agent failed", CrashOnPrompt: true})
	_, err := c.conn.Prompt(context.Background(), acpsdk.PromptRequest{SessionId: "s1",
		Prompt: []acpsdk.ContentBlock{acpsdk.TextBlock("hi")}})
	if err == nil {
		t.Fatal("Prompt() returned no error")
	}
	got := c.callErr(err).Error()
	if !strings.HasPrefix(got, "acp: test-agent stopped: exit status 1. Last output: ") ||
		!strings.Contains(got, "boom: the fake agent failed") {
		t.Fatalf("callErr() = %q", got)
	}
}

func TestNotLoggedIn(t *testing.T) {
	c, _ := startFake(t, fakeScript{AuthRequired: true})
	_, err := c.conn.NewSession(context.Background(),
		acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}})
	var nl *NotLoggedInError
	if !errors.As(c.callErr(err), &nl) {
		t.Fatalf("callErr() = %v, want *NotLoggedInError", c.callErr(err))
	}
	want := `acp: test-agent is not logged in. Run "claude" on the host once and sign in, then send the task again.`
	if nl.Error() != want {
		t.Fatalf("Error() = %q", nl.Error())
	}
}

func TestStopKillsGrandchildAfterEOFExit(t *testing.T) {
	c, logPath := startFake(t, fakeScript{ExitOnEOF: true, SpawnGrandchild: true})
	pid := pollFakeLog(t, logPath, "grandchild", 1)[0].PID
	if pid <= 0 || !processAlive(pid) {
		t.Fatalf("the grandchild %d does not run before the stop", pid)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	// The leader exited on EOF by itself. So the group kill came after its exit.
	if c.exitErr != nil {
		t.Fatalf("the leader did not exit on EOF: %v", c.exitErr)
	}
	deadline := time.Now().Add(3 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("the grandchild %d is alive after Stop", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStopWithoutEOFExit(t *testing.T) {
	c, _ := startFake(t, fakeScript{})
	start := time.Now()
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Stop() took %s", d)
	}
	select {
	case <-c.Done():
	default:
		t.Fatal("Done() is open after Stop")
	}
	if c.exitErr == nil {
		t.Fatal("the leader exited with status 0, want a signal")
	}
}

func TestStderrTailRedactsSecrets(t *testing.T) {
	secret := "tok-1234567890abcdefXYZ"
	c, _ := startFake(t, fakeScript{Stderr: "line one\nleaked " + secret}, func(o *StartOptions) {
		o.DotEnv = map[string]string{"MY_TOKEN": secret}
	})
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(c.StderrTail(40), "line one") {
		if time.Now().After(deadline) {
			t.Fatalf("StderrTail(40) = %q", c.StderrTail(40))
		}
		time.Sleep(20 * time.Millisecond)
	}
	tail := c.StderrTail(1)
	if strings.Contains(tail, secret) || !strings.HasPrefix(tail, "leaked ") || strings.Contains(tail, "line one") {
		t.Fatalf("StderrTail(1) = %q", tail)
	}
}

func TestTailBufferKeepsLastBytesAndLines(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	tb, err := newTailBuffer(func(l string) { mu.Lock(); lines = append(lines, l); mu.Unlock() })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tb.w.WriteString("a\nb\nc\n" + strings.Repeat("x", 70<<10) + "\nlast\n"); err != nil {
		t.Fatal(err)
	}
	tb.closeWriter()
	<-tb.finished
	if n := len(tb.String()); n != stderrMax {
		t.Fatalf("len(String()) = %d, want %d", n, stderrMax)
	}
	if got := tb.lastLines(1); got != "last" {
		t.Fatalf("lastLines(1) = %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) < 4 || lines[0] != "a" || lines[1] != "b" || lines[2] != "c" || lines[len(lines)-1] != "last" {
		t.Fatalf("lines = %d, first %q", len(lines), lines[:min(3, len(lines))])
	}
}
