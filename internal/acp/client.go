package acp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/security"
)

const (
	protocolVersion  = 1
	codeAuthRequired = -32000
	stopStep         = time.Second // each wait of Stop
	stoppedWait      = 2 * time.Second
	cancelSendWait   = time.Second // the limit of one session/cancel send
)

// StartOptions tells Start which adapter to run and how.
type StartOptions struct {
	AgentID       string
	Agent         config.ACPAgent
	DotEnv        map[string]string
	Dir           string
	ClientVersion string
	Log           *slog.Logger
}

// Capabilities is what the agent reports in its initialize response.
type Capabilities struct {
	ProtocolVersion            int
	LoadSession, Resume, Close bool
	AgentVersion               string
}

// NotLoggedInError tells that the agent needs a login before it can work.
type NotLoggedInError struct{ Agent string }

// Error returns the text for the model.
func (e *NotLoggedInError) Error() string {
	return fmt.Sprintf("acp: %s is not logged in. Run \"claude\" on the host once and sign in, then send the task again.", e.Agent)
}

// option is one permission option of the agent.
type option struct{ ID, Name, Kind string }

// diffItem is one file change of a tool call.
type diffItem struct{ Path, OldText, NewText string }

// permissionInput is a permission request of the agent, with no SDK types.
type permissionInput struct {
	SessionID, ToolCallID, Kind, Title, Command, Plan string
	Locations                                         []string
	Diffs                                             []diffItem
	Options                                           []option
	// RawPaths holds the string values of rawInput.file_path and rawInput.notebook_path.
	// An adapter can send such a path with kind other and no location.
	RawPaths []string
}

// permissionOutcome is the answer to a permission request. An empty OptionID counts as
// cancelled.
type permissionOutcome struct {
	Cancelled bool
	OptionID  string
}

// update is a session update of the agent, with no SDK types. Type is tool_call,
// agent_message_chunk, current_mode_update, or other.
type update struct {
	SessionID, Type, ToolKind, Title, Text, Mode string
}

// turnHandler gets the updates and the permission requests of one ACP session while a turn
// runs.
type turnHandler interface {
	sessionID() string
	onUpdate(u update)
	onPermission(ctx context.Context, in permissionInput) permissionOutcome
}

// Client runs one adapter process and its ACP connection.
type Client struct {
	agentID string
	agent   config.ACPAgent
	log     *slog.Logger
	red     *security.Redactor
	cmd     *exec.Cmd
	pid     int
	stdin   *os.File // the parent end of the stdin pipe
	stdout  *os.File // the parent end of the stdout pipe
	stderr  *tailBuffer
	conn    *acpsdk.ClientSideConnection
	caps    Capabilities

	mu         sync.Mutex
	handler    turnHandler
	closeCause string // the cause of a connection close that the SDK logged
	protoErr   error  // set when a handler panics

	busy    atomic.Bool  // true while a Prompt call runs
	lastUse atomic.Int64 // Unix nanoseconds of the end of the last Prompt call, or of the start

	cancelWaitOverride time.Duration             // a test sets it; zero means cancelWait
	aborted            atomic.Bool               // Abort sets it before the stop
	turn               atomic.Pointer[turnState] // the turn of the running Prompt call, or nil

	exited   chan struct{} // closed when cmd.Wait returns
	exitErr  error         // the result of cmd.Wait; read it after exited closes
	done     chan struct{} // closed when Stop ends
	stopOnce sync.Once
}

// sdkClient implements acpsdk.Client. It keeps those methods off the API of Client.
type sdkClient struct{ c *Client }

var _ acpsdk.Client = sdkClient{}

// Start starts the adapter and sends initialize. ctx limits the handshake only. The process
// runs until Stop, also after ctx ends.
func Start(ctx context.Context, o StartOptions) (*Client, error) {
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	red := security.NewRedactor(SecretValues(o.DotEnv, o.Agent))
	if len(o.Agent.Command) == 0 {
		//lint:ignore ST1005 the text is a fixed user message with full sentences.
		return nil, fmt.Errorf("acp: cannot start %s: no command. Run tether doctor.", o.AgentID)
	}
	startErr := func(err error) error {
		//lint:ignore ST1005 the text is a fixed user message with full sentences.
		return fmt.Errorf("acp: cannot start %s: %s. Run tether doctor.", o.AgentID, red.Redact(err.Error()))
	}
	env := BuildEnv(os.Environ(), o.DotEnv, o.Agent)
	prog, err := FindProgram(env, o.Agent.Command[0])
	if err != nil {
		return nil, startErr(err)
	}

	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, startErr(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		closeFiles(inR, inW)
		return nil, startErr(err)
	}
	agentID := o.AgentID
	tail, err := newTailBuffer(func(l string) {
		log.Debug("acp: stderr", "agent", agentID, "line", red.Redact(l))
	})
	if err != nil {
		closeFiles(inR, inW, outR, outW)
		return nil, startErr(err)
	}

	// Files, not StdinPipe or StdoutPipe: cmd.Wait closes those pipes while the SDK reads.
	cmd := exec.Command(prog, o.Agent.Command[1:]...)
	cmd.Env = env
	cmd.Dir = o.Dir
	cmd.Stdin = inR
	cmd.Stdout = outW
	cmd.Stderr = tail.w
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		closeFiles(inR, inW, outR, outW)
		tail.closeWriter()
		return nil, startErr(err)
	}
	closeFiles(inR, outW)
	tail.closeWriter()

	c := &Client{
		agentID: o.AgentID,
		agent:   o.Agent,
		log:     log,
		red:     red,
		cmd:     cmd,
		pid:     cmd.Process.Pid,
		stdin:   inW,
		stdout:  outR,
		stderr:  tail,
		exited:  make(chan struct{}),
		done:    make(chan struct{}),
	}
	c.lastUse.Store(time.Now().UnixNano())
	go func() {
		c.exitErr = cmd.Wait()
		close(c.exited)
		// A stop after an exit kills what is left of the group.
		go c.Stop()
	}()

	// The SDK reads at once. The gate holds the first read until the logger is set, so that
	// the reader goroutine never sees the logger change.
	ready := make(chan struct{})
	c.conn = acpsdk.NewClientSideConnection(sdkClient{c}, c.stdin, gatedReader{r: c.stdout, ready: ready})
	c.conn.SetLogger(slog.New(closeWatch{c: c, next: log.Handler()}))
	close(ready)
	go func() {
		// A connection that the SDK closes is a stopped process.
		<-c.conn.Done()
		c.Stop()
	}()

	resp, err := c.conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{
			Fs:       acpsdk.FileSystemCapabilities{ReadTextFile: false, WriteTextFile: false},
			Terminal: false,
		},
		ClientInfo: &acpsdk.Implementation{Name: "tether", Version: o.ClientVersion},
	})
	if err != nil {
		e := c.callErr(err)
		_ = c.Stop()
		return nil, e
	}
	if int(resp.ProtocolVersion) != protocolVersion {
		_ = c.Stop()
		//lint:ignore ST1005 the text is a fixed user message with full sentences.
		return nil, fmt.Errorf("acp: %s uses ACP protocol version %d. Tether Agent supports only version 1.", o.AgentID, resp.ProtocolVersion)
	}
	ac := resp.AgentCapabilities
	c.caps = Capabilities{
		ProtocolVersion: int(resp.ProtocolVersion),
		LoadSession:     ac.LoadSession,
		Resume:          ac.SessionCapabilities.Resume != nil,
		Close:           ac.SessionCapabilities.Close != nil,
	}
	if resp.AgentInfo != nil {
		c.caps.AgentVersion = resp.AgentInfo.Version
	}
	return c, nil
}

// closeFiles closes each file and ignores the errors.
func closeFiles(fs ...*os.File) {
	for _, f := range fs {
		_ = f.Close()
	}
}

// gatedReader blocks each read until ready closes.
type gatedReader struct {
	r     io.Reader
	ready <-chan struct{}
}

func (g gatedReader) Read(p []byte) (int, error) {
	<-g.ready
	return g.r.Read(p)
}

// Capabilities returns what the agent reported in its initialize response.
func (c *Client) Capabilities() Capabilities { return c.caps }

// AgentID returns the agent id from the config.
func (c *Client) AgentID() string { return c.agentID }

// Done returns a channel that closes when Stop ends.
func (c *Client) Done() <-chan struct{} { return c.done }

// StderrTail returns the last lines of the stderr of the adapter, with the secrets masked.
func (c *Client) StderrTail(lines int) string {
	return c.red.Redact(c.stderr.lastLines(lines))
}

// attach sets the handler of the running turn. A nil h removes the handler.
func (c *Client) attach(h turnHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handler = h
}

// current returns the handler when it is for the ACP session sid, else nil.
func (c *Client) current(sid string) turnHandler {
	c.mu.Lock()
	h := c.handler
	c.mu.Unlock()
	if h == nil || h.sessionID() != sid {
		return nil
	}
	return h
}

// Stop stops the adapter and each process in its process group. It is safe to
// call more than once and from more than one goroutine.
func (c *Client) Stop() error {
	c.stopOnce.Do(func() {
		defer close(c.done)
		_ = c.stdin.Close() // claude-agent-acp exits on EOF
		if !waitClosed(c.exited, stopStep) {
			_ = syscall.Kill(-c.pid, syscall.SIGTERM)
			waitClosed(c.exited, stopStep)
		}
		// Always kill the group: a child of the adapter can stay in the
		// group after the leader exits.
		_ = syscall.Kill(-c.pid, syscall.SIGKILL)
		<-c.exited
		_ = c.stdout.Close()
	})
	<-c.done
	return nil
}

// Abort stops the client at once. It is safe during Prompt: it sends session/cancel, answers
// the open permission requests with cancelled, and stops the process. The running Prompt
// returns ErrSessionClosed. Abort does not wait for the cancel.
func (c *Client) Abort() {
	c.aborted.Store(true)
	if ts := c.turn.Load(); ts != nil {
		c.sendCancel(ts.sid)
		ts.bridge.cancelAll()
	}
	if err := c.Stop(); err != nil {
		c.log.Debug("acp: the stop after an abort failed", "agent", c.agentID, "err", err)
	}
}

// sendCancel sends session/cancel for the ACP session sid. The send waits up to
// cancelSendWait, so that a full pipe does not block the caller.
func (c *Client) sendCancel(sid string) {
	ctx, cancel := context.WithTimeout(context.Background(), cancelSendWait)
	defer cancel()
	if err := c.conn.Cancel(ctx, acpsdk.CancelNotification{SessionId: acpsdk.SessionId(sid)}); err != nil {
		c.log.Debug("acp: cannot send session/cancel", "agent", c.agentID, "session", sid,
			"err", c.red.Redact(err.Error()))
	}
}

// waitClosed waits up to d for ch to close.
func waitClosed(ch <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ch:
		return true
	case <-t.C:
		return false
	}
}

// callErr turns an error of an SDK call into the error text for the model.
func (c *Client) callErr(err error) error {
	if err == nil {
		return nil
	}
	var re *acpsdk.RequestError
	if errors.As(err, &re) && re.Code == codeAuthRequired {
		return &NotLoggedInError{Agent: c.agentID}
	}
	select {
	case <-c.conn.Done():
		// Wait for the exit status and the last stderr lines.
		waitClosed(c.done, stoppedWait)
		return c.stoppedErr()
	default:
	}
	return fmt.Errorf("acp: %s: %s", c.agentID, c.red.Redact(err.Error()))
}

// stoppedErr returns the error text for a process or a connection that stopped.
func (c *Client) stoppedErr() error {
	c.mu.Lock()
	protoErr, cause := c.protoErr, c.closeCause
	c.mu.Unlock()
	if protoErr != nil {
		return protoErr
	}
	if strings.Contains(cause, "token too long") {
		return fmt.Errorf("acp: %s sent a message of 10 MiB or more", c.agentID)
	}
	status := "the connection closed"
	if strings.Contains(cause, "notification queue overflow") {
		status = "more than 1024 updates waited"
	} else {
		select {
		case <-c.exited:
			status = "exit status 0"
			if c.exitErr != nil {
				status = c.exitErr.Error()
			}
		default:
		}
	}
	tail := c.StderrTail(40)
	if tail == "" {
		tail = "(none)"
	}
	return fmt.Errorf("acp: %s stopped: %s. Last output: %s", c.agentID, status, tail)
}

// closeWatch is the slog.Handler of the SDK logger. The SDK does not return the cause of a
// connection close. It only logs it, so closeWatch keeps the cause for stoppedErr.
type closeWatch struct {
	c    *Client
	next slog.Handler
}

func (w closeWatch) Enabled(context.Context, slog.Level) bool { return true }

func (w closeWatch) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "connection closed" || strings.HasPrefix(r.Message, "failed to queue notification") {
		var cause string
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "cause" || a.Key == "err" {
				if v := a.Value.String(); v != "" {
					cause = v
					return false
				}
			}
			return true
		})
		if cause != "" {
			w.c.mu.Lock()
			if w.c.closeCause == "" {
				w.c.closeCause = cause
			}
			w.c.mu.Unlock()
		}
	}
	if !w.next.Enabled(ctx, slog.LevelDebug) {
		return nil
	}
	nr := slog.NewRecord(r.Time, slog.LevelDebug, r.Message, r.PC)
	nr.AddAttrs(slog.String("agent", w.c.agentID))
	r.Attrs(func(a slog.Attr) bool {
		// A raw line of the agent can hold a secret value.
		nr.AddAttrs(slog.String(a.Key, w.c.red.Redact(a.Value.String())))
		return true
	})
	return w.next.Handle(ctx, nr)
}

func (w closeWatch) WithAttrs(as []slog.Attr) slog.Handler {
	return closeWatch{c: w.c, next: w.next.WithAttrs(as)}
}

func (w closeWatch) WithGroup(name string) slog.Handler {
	return closeWatch{c: w.c, next: w.next.WithGroup(name)}
}

// recoverHandler stops a panic in an SDK handler. The SDK runs handlers in
// its own goroutines, and a panic there would stop Tether Agent.
func (c *Client) recoverHandler(method string) {
	if v := recover(); v != nil {
		c.log.Error("acp: a handler panicked", "agent", c.agentID, "method", method,
			"panic", fmt.Sprint(v), "stack", string(debug.Stack()))
		c.mu.Lock()
		if c.protoErr == nil {
			//lint:ignore ST1005 the text is a fixed user message with full sentences.
			c.protoErr = fmt.Errorf("acp: %s sent a message that Tether Agent cannot read.", c.agentID)
		}
		c.mu.Unlock()
		go c.Stop()
	}
}

// SessionUpdate gives a session update to the handler of the running turn. It must not
// block, because the SDK runs all notifications on one goroutine.
func (s sdkClient) SessionUpdate(_ context.Context, n acpsdk.SessionNotification) error {
	defer s.c.recoverHandler(acpsdk.ClientMethodSessionUpdate)
	u := update{SessionID: string(n.SessionId), Type: "other"}
	switch up := n.Update; {
	case up.ToolCall != nil:
		u.Type = "tool_call"
		u.ToolKind = string(up.ToolCall.Kind)
		u.Title = up.ToolCall.Title
	case up.AgentMessageChunk != nil && up.AgentMessageChunk.Content.Text != nil:
		u.Type = "agent_message_chunk"
		u.Text = up.AgentMessageChunk.Content.Text.Text
	case up.CurrentModeUpdate != nil:
		u.Type = "current_mode_update"
		u.Mode = string(up.CurrentModeUpdate.CurrentModeId)
	}
	h := s.c.current(u.SessionID)
	if h == nil {
		s.c.log.Debug("acp: an update came with no turn", "agent", s.c.agentID,
			"session", u.SessionID, "type", u.Type)
		return nil
	}
	h.onUpdate(u)
	return nil
}

// commandText returns the command text of a rawInput "command" value. A string gives itself.
// A list of strings gives the items joined with one space. Each other value gives "".
func commandText(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		parts := make([]string, 0, len(c))
		for _, it := range c {
			s, ok := it.(string)
			if !ok {
				return ""
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// RequestPermission gives a permission request to the handler of the running turn. With no
// handler, or after a panic, the answer is cancelled.
func (s sdkClient) RequestPermission(ctx context.Context, p acpsdk.RequestPermissionRequest) (resp acpsdk.RequestPermissionResponse, err error) {
	resp = acpsdk.RequestPermissionResponse{Outcome: acpsdk.RequestPermissionOutcome{
		Cancelled: &acpsdk.RequestPermissionOutcomeCancelled{Outcome: "cancelled"},
	}}
	defer s.c.recoverHandler(acpsdk.ClientMethodSessionRequestPermission)
	tc := p.ToolCall
	in := permissionInput{SessionID: string(p.SessionId), ToolCallID: string(tc.ToolCallId)}
	if tc.Kind != nil {
		in.Kind = string(*tc.Kind)
	}
	if tc.Title != nil {
		in.Title = *tc.Title
	}
	if m, ok := tc.RawInput.(map[string]any); ok {
		in.Command = commandText(m["command"])
		if v, ok := m["plan"].(string); ok {
			in.Plan = v
		}
		for _, k := range []string{"file_path", "notebook_path"} {
			if v, ok := m[k].(string); ok && v != "" {
				in.RawPaths = append(in.RawPaths, v)
			}
		}
	}
	for _, l := range tc.Locations {
		if l.Path != "" {
			in.Locations = append(in.Locations, l.Path)
		}
	}
	for _, ct := range tc.Content {
		if ct.Diff == nil {
			continue
		}
		d := diffItem{Path: ct.Diff.Path, NewText: ct.Diff.NewText}
		if ct.Diff.OldText != nil {
			d.OldText = *ct.Diff.OldText
		}
		in.Diffs = append(in.Diffs, d)
	}
	for _, o := range p.Options {
		in.Options = append(in.Options, option{ID: string(o.OptionId), Name: o.Name, Kind: string(o.Kind)})
	}
	h := s.c.current(in.SessionID)
	if h == nil {
		return resp, nil
	}
	out := h.onPermission(ctx, in)
	if out.Cancelled || out.OptionID == "" {
		return resp, nil
	}
	return acpsdk.RequestPermissionResponse{Outcome: acpsdk.RequestPermissionOutcome{
		Selected: &acpsdk.RequestPermissionOutcomeSelected{OptionId: acpsdk.PermissionOptionId(out.OptionID), Outcome: "selected"},
	}}, nil
}

// ReadTextFile is not supported. The client does not offer the fs capability.
func (s sdkClient) ReadTextFile(context.Context, acpsdk.ReadTextFileRequest) (acpsdk.ReadTextFileResponse, error) {
	defer s.c.recoverHandler(acpsdk.ClientMethodFsReadTextFile)
	return acpsdk.ReadTextFileResponse{}, acpsdk.NewMethodNotFound(acpsdk.ClientMethodFsReadTextFile)
}

// WriteTextFile is not supported. The client does not offer the fs capability.
func (s sdkClient) WriteTextFile(context.Context, acpsdk.WriteTextFileRequest) (acpsdk.WriteTextFileResponse, error) {
	defer s.c.recoverHandler(acpsdk.ClientMethodFsWriteTextFile)
	return acpsdk.WriteTextFileResponse{}, acpsdk.NewMethodNotFound(acpsdk.ClientMethodFsWriteTextFile)
}

// CreateTerminal is not supported. The client does not offer the terminal capability.
func (s sdkClient) CreateTerminal(context.Context, acpsdk.CreateTerminalRequest) (acpsdk.CreateTerminalResponse, error) {
	defer s.c.recoverHandler(acpsdk.ClientMethodTerminalCreate)
	return acpsdk.CreateTerminalResponse{}, acpsdk.NewMethodNotFound(acpsdk.ClientMethodTerminalCreate)
}

// TerminalOutput is not supported. The client does not offer the terminal capability.
func (s sdkClient) TerminalOutput(context.Context, acpsdk.TerminalOutputRequest) (acpsdk.TerminalOutputResponse, error) {
	defer s.c.recoverHandler(acpsdk.ClientMethodTerminalOutput)
	return acpsdk.TerminalOutputResponse{}, acpsdk.NewMethodNotFound(acpsdk.ClientMethodTerminalOutput)
}

// ReleaseTerminal is not supported. The client does not offer the terminal capability.
func (s sdkClient) ReleaseTerminal(context.Context, acpsdk.ReleaseTerminalRequest) (acpsdk.ReleaseTerminalResponse, error) {
	defer s.c.recoverHandler(acpsdk.ClientMethodTerminalRelease)
	return acpsdk.ReleaseTerminalResponse{}, acpsdk.NewMethodNotFound(acpsdk.ClientMethodTerminalRelease)
}

// WaitForTerminalExit is not supported. The client does not offer the terminal capability.
func (s sdkClient) WaitForTerminalExit(context.Context, acpsdk.WaitForTerminalExitRequest) (acpsdk.WaitForTerminalExitResponse, error) {
	defer s.c.recoverHandler(acpsdk.ClientMethodTerminalWaitForExit)
	return acpsdk.WaitForTerminalExitResponse{}, acpsdk.NewMethodNotFound(acpsdk.ClientMethodTerminalWaitForExit)
}

// KillTerminal is not supported. The client does not offer the terminal capability.
func (s sdkClient) KillTerminal(context.Context, acpsdk.KillTerminalRequest) (acpsdk.KillTerminalResponse, error) {
	defer s.c.recoverHandler(acpsdk.ClientMethodTerminalKill)
	return acpsdk.KillTerminalResponse{}, acpsdk.NewMethodNotFound(acpsdk.ClientMethodTerminalKill)
}
