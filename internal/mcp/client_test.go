package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dnwandana/tether-agent/internal/config"
)

// ctx is the context for the client calls in the tests.
var ctx = context.Background()

var (
	serverOnce sync.Once
	serverBin  string
	serverErr  error
	serverDir  string
)

func TestMain(m *testing.M) {
	code := m.Run()
	if serverDir != "" {
		os.RemoveAll(serverDir)
	}
	os.Exit(code)
}

// buildTestServer builds testdata/echoserver once and returns the path of
// the binary.
func buildTestServer(t *testing.T) string {
	t.Helper()
	serverOnce.Do(func() {
		goBin, err := exec.LookPath("go")
		if err != nil {
			serverErr = err
			return
		}
		serverDir, serverErr = os.MkdirTemp("", "tether-mcp-test")
		if serverErr != nil {
			return
		}
		serverBin = filepath.Join(serverDir, "echoserver")
		cmd := exec.Command(goBin, "build", "-o", serverBin, "./testdata/echoserver")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			serverErr = errors.New(string(out))
		}
	})
	if errors.Is(serverErr, exec.ErrNotFound) {
		t.Skip("go is not on PATH")
	}
	if serverErr != nil {
		t.Fatal(serverErr)
	}
	return serverBin
}

// stdioServer returns the config entry for the test server.
func stdioServer(t *testing.T) config.MCPServer {
	s := config.DefaultMCPServer()
	s.Command = []string{buildTestServer(t)}
	s.TimeoutS, s.ConnectTimeoutS = 10, 10
	return s
}

func dial(t *testing.T, s config.MCPServer, dotenv map[string]string) *Client {
	t.Helper()
	c, err := Dial(ctx, "test", s, os.Environ(), dotenv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

type echoIn struct {
	Text string `json:"text"`
}

type sleepIn struct {
	Seconds float64 `json:"seconds"`
}

type none struct{}

// newTestServer returns an in-process MCP server with the tools echo, sleep,
// fail, mixed, and structured.
func newTestServer() *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "inproc", Version: "v0.0.1"}, nil)
	sdk.AddTool(s, &sdk.Tool{Name: "echo", Description: "Returns the text."},
		func(_ context.Context, _ *sdk.CallToolRequest, in echoIn) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: in.Text}}}, nil, nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "sleep", Description: "Waits."},
		func(ctx context.Context, _ *sdk.CallToolRequest, in sleepIn) (*sdk.CallToolResult, any, error) {
			select {
			case <-time.After(time.Duration(in.Seconds * float64(time.Second))):
			case <-ctx.Done():
			}
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "slept"}}}, nil, nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "fail", Description: "Fails."},
		func(context.Context, *sdk.CallToolRequest, none) (*sdk.CallToolResult, any, error) {
			return nil, nil, errors.New("upstream refused Authorization: Bearer sk-abcdefghijklmnopqrstuv")
		})
	sdk.AddTool(s, &sdk.Tool{Name: "mixed", Description: "Returns several parts."},
		func(context.Context, *sdk.CallToolRequest, none) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{
				&sdk.TextContent{Text: "one"},
				&sdk.ImageContent{Data: []byte("png"), MIMEType: "image/png"},
				&sdk.TextContent{Text: "two"},
			}}, nil, nil
		})
	type out struct {
		N int `json:"n"`
	}
	sdk.AddTool(s, &sdk.Tool{Name: "structured", Description: "Returns structured content only."},
		func(context.Context, *sdk.CallToolRequest, none) (*sdk.CallToolResult, out, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{}}, out{N: 7}, nil
		})
	return s
}

// httpServer starts the in-process server over HTTP. It records the
// X-Test header of the last request.
func httpServer(t *testing.T, sse bool) (*httptest.Server, func() string) {
	t.Helper()
	s := newTestServer()
	var h http.Handler
	if sse {
		h = sdk.NewSSEHandler(func(*http.Request) *sdk.Server { return s }, nil)
	} else {
		h = sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s }, nil)
	}
	var (
		mu  sync.Mutex
		hdr string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hdr = r.Header.Get("X-Test")
		mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, func() string { mu.Lock(); defer mu.Unlock(); return hdr }
}

func TestStdioListAndCall(t *testing.T) {
	c := dial(t, stdioServer(t), nil)
	defs, err := c.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var echo *ToolDef
	for i := range defs {
		if defs[i].Name == "echo" {
			echo = &defs[i]
		}
	}
	if len(defs) != 5 || echo == nil || echo.Description != "Returns the text." || !strings.Contains(string(echo.InputSchema), `"text"`) {
		t.Fatal(defs)
	}
	out, err := c.Call(ctx, "echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil || out != "hi" {
		t.Fatal(out, err)
	}
}

func TestStdioEnvStripsSecretsKeepsOwnEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-leak")
	t.Setenv("MY_DOTENV_NAME", "dotenv-value")
	s := stdioServer(t)
	s.Env = map[string]string{"GITHUB_TOKEN": "ghp_own", "HOME": "/custom/home"}
	c := dial(t, s, map[string]string{"MY_DOTENV_NAME": "dotenv-value"})
	out, err := c.Call(ctx, "env", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Do not print the env on failure: it holds the env of the test run.
	lines := strings.Split(out, "\n")
	if strings.Contains(out, "sk-leak") || strings.Contains(out, "dotenv-value") {
		t.Fatal("a secret reached the MCP server")
	}
	if !slices.Contains(lines, "GITHUB_TOKEN=ghp_own") {
		t.Fatal("the own env of the entry is missing")
	}
	homes := slices.DeleteFunc(slices.Clone(lines), func(l string) bool { return !strings.HasPrefix(l, "HOME=") })
	if len(homes) != 1 || homes[0] != "HOME=/custom/home" {
		t.Fatal("own env must replace the base value", homes)
	}
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "PATH=") }) {
		t.Fatal("PATH missing")
	}
}

func TestHTTPTransport(t *testing.T) {
	srv, hdr := httpServer(t, false)
	s := config.DefaultMCPServer()
	s.URL = srv.URL
	s.Headers = map[string]string{"X-Test": "hello"}
	c := dial(t, s, nil)
	defs, err := c.ListTools(ctx)
	if err != nil || len(defs) != 5 {
		t.Fatal(defs, err)
	}
	out, err := c.Call(ctx, "echo", json.RawMessage(`{"text":"over http"}`))
	if err != nil || out != "over http" {
		t.Fatal(out, err)
	}
	if hdr() != "hello" {
		t.Fatal("headers not sent", hdr())
	}
}

func TestSSETransport(t *testing.T) {
	srv, hdr := httpServer(t, true)
	s := config.DefaultMCPServer()
	s.URL, s.Transport = srv.URL, "sse"
	s.ConnectTimeoutS = 1
	s.Headers = map[string]string{"X-Test": "sse"}
	c := dial(t, s, nil)
	// The SSE stream must stay open after the connect timeout ends.
	time.Sleep(1200 * time.Millisecond)
	out, err := c.Call(ctx, "echo", json.RawMessage(`{"text":"over sse"}`))
	if err != nil || out != "over sse" {
		t.Fatal(out, err)
	}
	if hdr() != "sse" {
		t.Fatal("headers not sent", hdr())
	}
}

func TestCallTimeout(t *testing.T) {
	s := stdioServer(t)
	s.TimeoutS = 1
	c := dial(t, s, nil)
	start := time.Now()
	_, err := c.Call(ctx, "sleep", json.RawMessage(`{"seconds":3}`))
	if err == nil || !strings.Contains(err.Error(), "timed out after 1s") {
		t.Fatal(err)
	}
	if time.Since(start) > 2500*time.Millisecond {
		t.Fatal("the timeout did not stop the call", time.Since(start))
	}
	// The connection still works after a timeout.
	if out, err := c.Call(ctx, "echo", json.RawMessage(`{"text":"after"}`)); err != nil || out != "after" {
		t.Fatal(out, err)
	}
}

func TestErrorTextRedacted(t *testing.T) {
	srv, _ := httpServer(t, false)
	s := config.DefaultMCPServer()
	s.URL = srv.URL
	c := dial(t, s, nil)
	_, err := c.Call(ctx, "fail", nil)
	if err == nil || strings.Contains(err.Error(), "sk-abcdefghijklmnopqrstuv") || !strings.Contains(err.Error(), "upstream refused") {
		t.Fatal(err)
	}
	// The stdio server gives the same error.
	c2 := dial(t, stdioServer(t), nil)
	_, err = c2.Call(ctx, "fail", nil)
	if err == nil || strings.Contains(err.Error(), "sk-abcdefghijklmnopqrstuv") {
		t.Fatal(err)
	}
}

func TestErrorTextRedactsOwnValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key "+r.Header.Get("X-Api"), http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	s := config.DefaultMCPServer()
	s.URL = srv.URL
	s.ConnectTimeoutS = 5
	s.Headers = map[string]string{"X-Api": "plainsecretvalue123"}
	_, err := Dial(ctx, "bad", s, nil, nil)
	if err == nil || strings.Contains(err.Error(), "plainsecretvalue123") {
		t.Fatal(err)
	}
}

func TestContentParts(t *testing.T) {
	srv, _ := httpServer(t, false)
	s := config.DefaultMCPServer()
	s.URL = srv.URL
	c := dial(t, s, nil)
	out, err := c.Call(ctx, "mixed", nil)
	if err != nil || !strings.HasPrefix(out, "one\n") || !strings.HasSuffix(out, "\ntwo") || !strings.Contains(out, "image/png") {
		t.Fatal(out, err)
	}
	out, err = c.Call(ctx, "structured", nil)
	if err != nil || strings.ReplaceAll(out, " ", "") != `{"n":7}` {
		t.Fatal(out, err)
	}
}

func TestConnectTimeout(t *testing.T) {
	// A stdio command that never answers the handshake.
	s := config.DefaultMCPServer()
	s.Command = []string{"sleep", "30"}
	s.ConnectTimeoutS = 1
	start := time.Now()
	_, err := Dial(ctx, "slow", s, os.Environ(), nil)
	if err == nil || !strings.Contains(err.Error(), "timed out after 1s") {
		t.Fatal(err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("connect timeout too slow", time.Since(start))
	}
}

func TestDialErrors(t *testing.T) {
	for name, s := range map[string]config.MCPServer{
		"none":      {},
		"both":      {Command: []string{"x"}, URL: "http://127.0.0.1:1"},
		"transport": {URL: "http://127.0.0.1:1", Transport: "websocket"},
		"missing":   {Command: []string{"/no/such/binary-tether"}},
	} {
		if c, err := Dial(ctx, name, s, nil, nil); err == nil {
			c.Close()
			t.Fatal(name)
		}
	}
}

func TestCloseKillsProcessGroup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses /proc")
	}
	c, err := Dial(ctx, "spawn", stdioServer(t), os.Environ(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Call(ctx, "spawn", nil)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(out)
	if err != nil {
		t.Fatal(out)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal("second Close", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, err := os.ReadFile("/proc/" + out + "/stat")
		// A missing entry or a zombie means the process is dead.
		if err != nil || procState(string(b)) == "Z" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child %d of the MCP server is still alive", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := c.Call(ctx, "echo", json.RawMessage(`{"text":"x"}`)); err == nil {
		t.Fatal("call after Close must fail")
	}
}

// procState returns the state field of /proc/<pid>/stat.
func procState(stat string) string {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 || i+2 >= len(stat) {
		return ""
	}
	return stat[i+2 : i+3]
}

func TestDialErrorShowsStderrRedacted(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	s := config.DefaultMCPServer()
	s.Command = []string{"sh", "-c", "echo boom token=$X >&2; exit 3"}
	s.Env = map[string]string{"X": "plainsecretvalue123"}
	s.ConnectTimeoutS = 5
	_, err := Dial(ctx, "sh", s, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "boom") || strings.Contains(err.Error(), "plainsecretvalue123") {
		t.Fatal(err)
	}
}
