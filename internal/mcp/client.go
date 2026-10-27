// Package mcp connects to MCP servers over stdio, Streamable HTTP, or SSE.
// It uses the official go-sdk client. It has no OAuth and no sampling.
package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/security"
)

// The defaults for a server entry with no timeouts. They match
// config.DefaultMCPServer.
const (
	defaultCallTimeout    = 120 * time.Second
	defaultConnectTimeout = 60 * time.Second
	// terminateWait is the time that a stdio server gets to exit after its
	// stdin closes. Then it gets SIGTERM, and then SIGKILL.
	terminateWait = 2 * time.Second
	// stderrTail is the number of stderr bytes that a connect error shows.
	stderrTail = 2048
)

// ToolDef is one tool of an MCP server. InputSchema is a JSON schema.
type ToolDef struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// Client is one connection to one MCP server. It is safe for concurrent use.
type Client struct {
	name    string
	srv     config.MCPServer
	session *sdk.ClientSession
	red     *security.Redactor
	stop    context.CancelFunc // ends the life of the transport
	cmd     *exec.Cmd          // stdio only
	stderr  *tailBuffer        // stdio only

	closeOnce sync.Once
	closeErr  error
}

// Dial connects to the server s and finishes the MCP handshake within
// s.ConnectTimeoutS. A stdio server gets security.ChildEnv(baseEnv, dotenv,
// nil) plus its own s.Env. The values of dotenv, s.Env, and s.Headers are
// masked in all error text.
func Dial(ctx context.Context, name string, s config.MCPServer, baseEnv []string, dotenv map[string]string) (*Client, error) {
	c := &Client{name: name, srv: s, red: security.NewRedactor(secretValues(s, dotenv))}
	t, err := c.transport(baseEnv, dotenv)
	if err != nil {
		return nil, fmt.Errorf("mcp %s: %w", name, err)
	}

	// The SSE transport keeps the context of Connect for its stream, so the
	// transport gets a context that lives until Close. The connect timeout
	// stops that context only while the connect runs.
	life, stop := context.WithCancel(context.WithoutCancel(ctx))
	c.stop = stop
	timeout := seconds(s.ConnectTimeoutS, defaultConnectTimeout)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	unwatch := context.AfterFunc(cctx, stop)

	client := sdk.NewClient(&sdk.Implementation{Name: "tether", Version: "v1"},
		&sdk.ClientOptions{Capabilities: &sdk.ClientCapabilities{}})
	session, err := client.Connect(cctx, lifeTransport{t, life}, nil)
	if err == nil && !unwatch() {
		session.Close()
		err = cctx.Err()
	}
	if c.stderr != nil {
		c.stderr.closeWriter()
	}
	if err != nil {
		c.cleanup()
		if cctx.Err() != nil && ctx.Err() == nil {
			err = fmt.Errorf("connect timed out after %s", timeout)
		}
		return nil, fmt.Errorf("mcp %s: %s", name, c.red.Redact(c.withStderr(err.Error())))
	}
	c.session = session
	return c, nil
}

// transport returns the go-sdk transport for the entry.
func (c *Client) transport(baseEnv []string, dotenv map[string]string) (sdk.Transport, error) {
	s := c.srv
	switch {
	case len(s.Command) > 0 && s.URL != "":
		return nil, errors.New("set command or url, not both")
	case len(s.Command) > 0:
		cmd := exec.Command(s.Command[0], s.Command[1:]...)
		cmd.Env = childEnv(baseEnv, dotenv, s.Env)
		// A new process group lets Close stop the children of the server too.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		tail, err := newTailBuffer()
		if err != nil {
			return nil, err
		}
		cmd.Stderr = tail.w
		c.cmd, c.stderr = cmd, tail
		return &sdk.CommandTransport{Command: cmd, TerminateDuration: terminateWait}, nil
	case s.URL == "":
		return nil, errors.New("set command (stdio) or url (HTTP)")
	}
	hc := &http.Client{Transport: headerTransport{headers: s.Headers, base: http.DefaultTransport}}
	switch strings.ToLower(s.Transport) {
	case "", "http", "streamable", "streamable_http", "streamable-http":
		return &sdk.StreamableClientTransport{Endpoint: s.URL, HTTPClient: hc}, nil
	case "sse":
		return &sdk.SSEClientTransport{Endpoint: s.URL, HTTPClient: hc}, nil
	default:
		return nil, fmt.Errorf("transport must be \"\" (Streamable HTTP) or \"sse\", not %q", s.Transport)
	}
}

// ListTools returns all tools of the server. It follows the pages of the
// list. The call uses the call timeout.
func (c *Client) ListTools(ctx context.Context) ([]ToolDef, error) {
	ctx, cancel := context.WithTimeout(ctx, seconds(c.srv.TimeoutS, defaultCallTimeout))
	defer cancel()
	var defs []ToolDef
	for t, err := range c.session.Tools(ctx, nil) {
		if err != nil {
			return nil, c.callErr(ctx, "list tools", err)
		}
		schema, err := json.Marshal(t.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("mcp %s: tool %s: bad input schema: %w", c.name, t.Name, err)
		}
		defs = append(defs, ToolDef{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	return defs, nil
}

// Call runs the tool with the JSON object args. Empty args send {}. It
// returns the text parts of the result, joined with newlines. A result with
// isError set, and each other failure, returns an error with the credentials
// masked.
func (c *Client) Call(ctx context.Context, tool string, args json.RawMessage) (string, error) {
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	if !json.Valid(args) {
		return "", fmt.Errorf("mcp %s: call %s: the arguments are not valid JSON", c.name, tool)
	}
	ctx, cancel := context.WithTimeout(ctx, seconds(c.srv.TimeoutS, defaultCallTimeout))
	defer cancel()
	res, err := c.session.CallTool(ctx, &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return "", c.callErr(ctx, "call "+tool, err)
	}
	out := resultText(res)
	if res.IsError {
		if out == "" {
			out = "the MCP tool returned an error"
		}
		return "", fmt.Errorf("mcp %s: %s: %s", c.name, tool, c.red.Redact(out))
	}
	return out, nil
}

// Close ends the session. For stdio, it stops the server and all processes
// in its process group. A second call returns the result of the first.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		if c.session != nil {
			err := c.session.Close()
			var exit *exec.ExitError
			if err != nil && !errors.As(err, &exit) {
				c.closeErr = fmt.Errorf("mcp %s: close: %s", c.name, c.red.Redact(err.Error()))
			}
		}
		c.cleanup()
	})
	return c.closeErr
}

// cleanup stops the transport context and kills what is left of the stdio
// process group.
func (c *Client) cleanup() {
	if c.stop != nil {
		c.stop()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
		if c.cmd.ProcessState == nil {
			// The handshake failed before the transport waited for the
			// process. Wait here, so that no zombie stays.
			_ = c.cmd.Wait()
		}
	}
	if c.stderr != nil {
		c.stderr.closeWriter()
	}
}

// callErr turns a failed request into an error with the credentials masked.
func (c *Client) callErr(ctx context.Context, what string, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("mcp %s: %s timed out after %s", c.name, what, seconds(c.srv.TimeoutS, defaultCallTimeout))
	}
	return fmt.Errorf("mcp %s: %s: %s", c.name, what, c.red.Redact(err.Error()))
}

// withStderr adds the last stderr lines of a stdio server to msg.
func (c *Client) withStderr(msg string) string {
	if c.stderr == nil {
		return msg
	}
	if tail := strings.TrimSpace(c.stderr.String()); tail != "" {
		return msg + " (stderr: " + tail + ")"
	}
	return msg
}

// resultText joins the parts of a tool result. A result with only
// structured content returns that content as JSON.
func resultText(res *sdk.CallToolResult) string {
	var parts []string
	for _, p := range res.Content {
		switch p := p.(type) {
		case *sdk.TextContent:
			if p.Text != "" {
				parts = append(parts, p.Text)
			}
		case *sdk.ImageContent:
			parts = append(parts, fmt.Sprintf("[image: %s, %d bytes, not shown]", p.MIMEType, base64.StdEncoding.DecodedLen(len(p.Data))))
		case *sdk.AudioContent:
			parts = append(parts, fmt.Sprintf("[audio: %s, not shown]", p.MIMEType))
		case *sdk.ResourceLink:
			parts = append(parts, "[resource: "+p.URI+"]")
		case *sdk.EmbeddedResource:
			if p.Resource != nil && p.Resource.Text != "" {
				parts = append(parts, p.Resource.Text)
			} else if p.Resource != nil {
				parts = append(parts, "[resource: "+p.Resource.URI+"]")
			}
		}
	}
	if len(parts) == 0 && res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			return string(b)
		}
	}
	return strings.Join(parts, "\n")
}

// childEnv returns the stdio server env: the base env with no secrets, then
// the own env of the entry. An own value replaces a base value of the same
// name.
func childEnv(baseEnv []string, dotenv, own map[string]string) []string {
	env := security.ChildEnv(baseEnv, dotenv, nil)
	if len(own) == 0 {
		return env
	}
	env = slices.DeleteFunc(env, func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		_, ok := own[name]
		return ok
	})
	for _, k := range slices.Sorted(maps.Keys(own)) {
		env = append(env, k+"="+own[k])
	}
	return env
}

// secretValues returns the values that the error text must not show.
func secretValues(s config.MCPServer, dotenv map[string]string) []string {
	var vals []string
	for _, m := range []map[string]string{dotenv, s.Env, s.Headers} {
		for _, v := range m {
			vals = append(vals, v)
			// "Bearer <token>" headers: also mask the token alone.
			if _, tok, ok := strings.Cut(v, " "); ok {
				vals = append(vals, tok)
			}
		}
	}
	return vals
}

func seconds(n int, def time.Duration) time.Duration {
	if n <= 0 {
		return def
	}
	return time.Duration(n) * time.Second
}

// lifeTransport gives the inner transport the context life instead of the
// context of Connect.
type lifeTransport struct {
	sdk.Transport
	life context.Context
}

func (t lifeTransport) Connect(context.Context) (sdk.Connection, error) {
	return t.Transport.Connect(t.life)
}

// headerTransport adds the configured headers to each request.
type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
}

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if len(t.headers) > 0 {
		r = r.Clone(r.Context())
		for k, v := range t.headers {
			r.Header.Set(k, v)
		}
	}
	return t.base.RoundTrip(r)
}

// tailBuffer keeps the last stderrTail bytes that a stdio server writes to
// stderr. The server gets the write end of an os.Pipe, so that exec does not
// wait for the stderr of the children of the server.
type tailBuffer struct {
	w *os.File

	mu       sync.Mutex
	buf      []byte
	wOnce    sync.Once
	finished chan struct{}
}

func newTailBuffer() (*tailBuffer, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	t := &tailBuffer{w: w, finished: make(chan struct{})}
	go func() {
		defer close(t.finished)
		defer r.Close()
		b := make([]byte, 1024)
		for {
			n, err := r.Read(b)
			t.mu.Lock()
			t.buf = append(t.buf, b[:n]...)
			if len(t.buf) > stderrTail {
				t.buf = t.buf[len(t.buf)-stderrTail:]
			}
			t.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return t, nil
}

// closeWriter closes the parent copy of the write end. The child keeps its
// own copy.
func (t *tailBuffer) closeWriter() {
	t.wOnce.Do(func() { t.w.Close() })
}

func (t *tailBuffer) String() string {
	// Give the reader a moment to take the last bytes of a dead process.
	select {
	case <-t.finished:
	case <-time.After(100 * time.Millisecond):
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
