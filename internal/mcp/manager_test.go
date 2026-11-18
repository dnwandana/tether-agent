package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/tools"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeClock is a clock that moves only when the test calls Advance.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// fakeServer is the state of one fake MCP server across its connections.
type fakeServer struct {
	mu sync.Mutex
	// tools is the tool list that the next ListTools returns.
	tools []ToolDef
	// failDials makes the next dials fail.
	failDials int
	dials     int
	open      []*fakeConn
	// callErr is the error of the next Call. It is used one time.
	callErr error
	// A call of the tool "slow" sends on entered and then waits for proceed, when both are
	// not nil. When its connection closes during the wait, the call fails.
	entered, proceed chan struct{}
}

// fakeConn is one connection to a fakeServer.
type fakeConn struct {
	srv    *fakeServer
	mu     sync.Mutex
	closed bool
}

func (c *fakeConn) ListTools(context.Context) ([]ToolDef, error) {
	c.srv.mu.Lock()
	defer c.srv.mu.Unlock()
	return append([]ToolDef(nil), c.srv.tools...), nil
}

func (c *fakeConn) Call(_ context.Context, tool string, args json.RawMessage) (string, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return "", errors.New("mcp gh: call " + tool + ": connection closed")
	}
	c.srv.mu.Lock()
	entered, proceed := c.srv.entered, c.srv.proceed
	c.srv.mu.Unlock()
	if tool == "slow" && entered != nil && proceed != nil {
		entered <- struct{}{}
		<-proceed
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return "", errors.New("mcp gh: call " + tool + ": connection closed")
		}
	}
	c.srv.mu.Lock()
	err := c.srv.callErr
	c.srv.callErr = nil
	c.srv.mu.Unlock()
	if err != nil {
		return "", err
	}
	return tool + " " + string(args), nil
}

func (c *fakeConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func (s *fakeServer) dialCount() int { s.mu.Lock(); defer s.mu.Unlock(); return s.dials }

// openConns returns the number of connections that are not closed.
func (s *fakeServer) openConns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.open {
		c.mu.Lock()
		if !c.closed {
			n++
		}
		c.mu.Unlock()
	}
	return n
}

// testManager returns a Manager over fake servers, with a fake clock and no backoff wait.
func testManager(t *testing.T, servers map[string]config.MCPServer, fakes map[string]*fakeServer) (*Manager, *fakeClock, *[]time.Duration) {
	t.Helper()
	clk := &fakeClock{now: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
	var waits []time.Duration
	m := &Manager{
		Servers:  servers,
		CacheDir: filepath.Join(t.TempDir(), "cache", "mcp-tools"),
		Now:      clk.Now,
		Log:      slog.New(slog.DiscardHandler),
	}
	m.dial = func(_ context.Context, name string, _ config.MCPServer) (conn, error) {
		f := fakes[name]
		f.mu.Lock()
		defer f.mu.Unlock()
		f.dials++
		if f.failDials > 0 {
			f.failDials--
			return nil, fmt.Errorf("mcp %s: connection refused", name)
		}
		c := &fakeConn{srv: f}
		f.open = append(f.open, c)
		return c, nil
	}
	m.sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	t.Cleanup(m.Close)
	return m, clk, &waits
}

func ghServer(alwaysOn bool) map[string]config.MCPServer {
	s := config.DefaultMCPServer()
	s.Command = []string{"gh-mcp"}
	s.AlwaysOn = alwaysOn
	return map[string]config.MCPServer{"gh": s}
}

// writeCache writes the cache file of the server.
func writeCache(t *testing.T, m *Manager, server string, defs []ToolDef) {
	t.Helper()
	if err := m.writeCache(server, defs); err != nil {
		t.Fatal(err)
	}
}

// managerWithCache returns a Manager for the server "gh" with a cache file that holds
// defs, and the fake server.
func managerWithCache(t *testing.T, defs []ToolDef) (*Manager, *fakeServer, *fakeClock) {
	t.Helper()
	f := &fakeServer{tools: defs}
	m, clk, _ := testManager(t, ghServer(false), map[string]*fakeServer{"gh": f})
	writeCache(t, m, "gh", defs)
	return m, f, clk
}

// fakeGate is the approval gate of the MCP tests. It allows each call unless err is set. It
// records the MCP calls.
type fakeGate struct {
	mu    sync.Mutex
	err   error
	calls []security.MCPCall
}

func (g *fakeGate) CheckCommand(context.Context, *tools.Env, tools.CommandCheck) error { return nil }

func (g *fakeGate) CheckMCP(_ context.Context, _ *tools.Env, call security.MCPCall) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, call)
	return g.err
}

// callTool calls the tool with a gate that allows each call.
func callTool(t *testing.T, reg *tools.Registry, name, args string) tools.Result {
	t.Helper()
	return callToolWith(t, reg, name, args, &tools.Env{Gate: &fakeGate{}})
}

func callToolWith(t *testing.T, reg *tools.Registry, name, args string, env *tools.Env) tools.Result {
	t.Helper()
	tool, ok := reg.Get(name)
	if !ok {
		t.Fatalf("no tool %s", name)
	}
	return reg.Dispatch(ctx, name, json.RawMessage(args), env, map[string]bool{tool.Name(): true})
}

// chainErr has its own text and wraps err, as Client.Call returns it.
type chainErr struct {
	msg string
	err error
}

func (e chainErr) Error() string { return e.msg }
func (e chainErr) Unwrap() error { return e.err }

// setCallErr makes the next Call of f fail with err.
func setCallErr(f *fakeServer, err error) {
	f.mu.Lock()
	f.callErr = err
	f.mu.Unlock()
}

func TestWriteCallNeedsGate(t *testing.T) {
	m, f, _ := managerWithCache(t, []ToolDef{{Name: "search", Description: "Search issues."},
		{Name: "create_issue", Description: "Create an issue. Needs a title."}})
	reg := tools.NewRegistry()
	m.Init(ctx, reg)
	if res := callToolWith(t, reg, "mcp_gh_search", `{}`, &tools.Env{}); res.IsError {
		t.Fatal("a read call runs with no gate:", res)
	}
	g := &fakeGate{err: errors.New("BLOCKED by user: MCP write (gh · create_issue).")}
	if res := callToolWith(t, reg, "mcp_gh_search", `{}`, &tools.Env{Gate: g}); res.IsError || len(g.calls) != 0 {
		t.Fatal("a read call does not go to the gate:", res, g.calls)
	}
	setCallErr(f, errors.New("the call reached the server"))
	res := callToolWith(t, reg, "mcp_gh_create_issue", `{}`, &tools.Env{})
	if !res.IsError || res.Text != "approval gate missing: the MCP call mcp_gh_create_issue cannot run without a gate" {
		t.Fatal(res)
	}
	res = callToolWith(t, reg, "mcp_gh_create_issue", `{"title":"x"}`, &tools.Env{Gate: g})
	if !res.IsError || res.Text != "BLOCKED by user: MCP write (gh · create_issue)." {
		t.Fatal(res)
	}
	f.mu.Lock()
	reached := f.callErr == nil
	f.mu.Unlock()
	if reached {
		t.Fatal("a refused call reached the server")
	}
	want := security.MCPCall{Server: "gh", Tool: "create_issue", Class: "write", ConfigHash: ConfigHash(m.Servers["gh"]),
		Summary: "Create an issue."}
	got := g.calls[0]
	if len(g.calls) != 1 || string(got.Args) != `{"title":"x"}` {
		t.Fatalf("%+v", g.calls)
	}
	got.Args = nil
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestMCPClassAndParallel(t *testing.T) {
	servers := ghServer(false)
	s := servers["gh"]
	s.SupportsParallelToolCalls = true
	servers["gh"] = s
	defs := []ToolDef{{Name: "search"}, {Name: "create_issue"}, {Name: "send_email"}, {Name: "create_refund"}}
	f := &fakeServer{tools: defs}
	m, _, _ := testManager(t, servers, map[string]*fakeServer{"gh": f})
	writeCache(t, m, "gh", defs)
	reg := tools.NewRegistry()
	m.Init(ctx, reg)
	for name, want := range map[string]struct {
		class    string
		parallel bool
	}{
		"mcp_gh_search":        {"read", true},
		"mcp_gh_create_issue":  {"write", false},
		"mcp_gh_send_email":    {"send", false},
		"mcp_gh_create_refund": {"human-only", false},
	} {
		tool, _ := reg.Get(name)
		mc, ok := tool.(tools.MCPClassed)
		if !ok || mc.MCPClass() != want.class || tool.ParallelSafe() != want.parallel {
			t.Errorf("%s: %v %v", name, ok, tool.ParallelSafe())
		}
	}
}

func TestNoProcessAtStartupWhenCached(t *testing.T) {
	m, f, _ := managerWithCache(t, []ToolDef{{Name: "search", Description: "Search issues.", InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)}})
	reg := tools.NewRegistry()
	if err := m.Init(ctx, reg); err != nil {
		t.Fatal(err)
	}
	if f.dialCount() != 0 {
		t.Fatal("cached → no dial")
	}
	tool, ok := reg.Get("mcp_gh_search")
	if !ok {
		t.Fatal("registered from cache")
	}
	s := tool.Schema()
	if tool.Toolset() != "mcp_gh" || s.Description != "Search issues." || !strings.Contains(string(s.Parameters), `"q"`) {
		t.Fatalf("%s %+v", tool.Toolset(), s)
	}
	if tool.ParallelSafe() {
		t.Fatal("parallel only with supports_parallel_tool_calls")
	}
}

func TestFirstRunListsThenDisconnects(t *testing.T) {
	f := &fakeServer{tools: []ToolDef{{Name: "search"}, {Name: "get.issue"}}}
	m, _, _ := testManager(t, ghServer(false), map[string]*fakeServer{"gh": f})
	reg := tools.NewRegistry()
	if err := m.Init(ctx, reg); err != nil {
		t.Fatal(err)
	}
	m.closing.Wait()
	if f.dialCount() != 1 || f.openConns() != 0 || m.connected("gh") {
		t.Fatal("one dial, then disconnect:", f.dialCount(), f.openConns())
	}
	defs, err := m.readCache("gh")
	if err != nil || len(defs) != 2 {
		t.Fatal(defs, err)
	}
	info, err := os.Stat(filepath.Join(m.CacheDir, "gh.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal(info, err)
	}
	// A name that a provider does not accept is changed.
	if _, ok := reg.Get("mcp_gh_get_issue"); !ok {
		t.Fatal("sanitized name")
	}
	if _, ok := reg.Get("mcp_gh_search"); !ok {
		t.Fatal("search")
	}
	// An empty schema becomes an object schema.
	tool, _ := reg.Get("mcp_gh_search")
	if string(tool.Schema().Parameters) != `{"type":"object"}` {
		t.Fatal(string(tool.Schema().Parameters))
	}
}

func TestColdStartOnFirstCallAndIdleStop(t *testing.T) {
	m, f, clk := managerWithCache(t, []ToolDef{{Name: "search"}})
	reg := tools.NewRegistry()
	if err := m.Init(ctx, reg); err != nil {
		t.Fatal(err)
	}
	res := callTool(t, reg, "mcp_gh_search", `{"q":"bug"}`)
	if res.IsError || res.Text != `search {"q":"bug"}` || f.dialCount() != 1 {
		t.Fatal(res, f.dialCount())
	}
	callTool(t, reg, "mcp_gh_search", `{}`)
	if f.dialCount() != 1 {
		t.Fatal("the warm connection is used again")
	}
	clk.Advance(9 * time.Minute)
	m.ReapIdle()
	if !m.connected("gh") {
		t.Fatal("9 min: still connected")
	}
	clk.Advance(2 * time.Minute)
	m.ReapIdle()
	m.closing.Wait()
	if m.connected("gh") || f.openConns() != 0 {
		t.Fatal("idle stop after 10 min")
	}
	callTool(t, reg, "mcp_gh_search", `{}`)
	if f.dialCount() != 2 {
		t.Fatal("a call after the idle stop starts the server again")
	}
}

func TestIdleStopUsesServerSetting(t *testing.T) {
	servers := ghServer(false)
	s := servers["gh"]
	s.IdleStopMinutes = 1
	servers["gh"] = s
	f := &fakeServer{tools: []ToolDef{{Name: "search"}}}
	m, clk, _ := testManager(t, servers, map[string]*fakeServer{"gh": f})
	reg := tools.NewRegistry()
	m.Init(ctx, reg)
	callTool(t, reg, "mcp_gh_search", `{}`)
	clk.Advance(61 * time.Second)
	m.ReapIdle()
	if m.connected("gh") {
		t.Fatal("idle_stop_minutes: 1")
	}
}

func TestAlwaysOnStaysConnected(t *testing.T) {
	f := &fakeServer{tools: []ToolDef{{Name: "search"}}}
	m, clk, _ := testManager(t, ghServer(true), map[string]*fakeServer{"gh": f})
	writeCache(t, m, "gh", f.tools)
	reg := tools.NewRegistry()
	if err := m.Init(ctx, reg); err != nil {
		t.Fatal(err)
	}
	if f.dialCount() != 1 || !m.connected("gh") {
		t.Fatal("always_on → dialed at Init")
	}
	clk.Advance(time.Hour)
	m.ReapIdle()
	if !m.connected("gh") {
		t.Fatal("always_on stays connected")
	}
	m.Close()
	if f.openConns() != 0 {
		t.Fatal("Close closes all connections")
	}
}

func TestDisabledServerIgnored(t *testing.T) {
	servers := ghServer(false)
	s := servers["gh"]
	s.Enabled = false
	servers["gh"] = s
	f := &fakeServer{tools: []ToolDef{{Name: "search"}}}
	m, _, _ := testManager(t, servers, map[string]*fakeServer{"gh": f})
	reg := tools.NewRegistry()
	if err := m.Init(ctx, reg); err != nil || f.dialCount() != 0 || len(reg.All()) != 0 {
		t.Fatal(err, f.dialCount(), len(reg.All()))
	}
}

func TestToolListChangeUpdatesCache(t *testing.T) {
	m, f, _ := managerWithCache(t, []ToolDef{{Name: "search"}})
	reg := tools.NewRegistry()
	m.Init(ctx, reg)
	f.mu.Lock()
	f.tools = []ToolDef{{Name: "search"}, {Name: "create"}}
	f.mu.Unlock()
	callTool(t, reg, "mcp_gh_search", `{}`)
	defs, err := m.readCache("gh")
	if err != nil || len(defs) != 2 {
		t.Fatal("the cache has the new list:", defs, err)
	}
	if _, ok := reg.Get("mcp_gh_create"); ok {
		t.Fatal("the new list applies from the next session")
	}
}

func TestReloadBumpsGeneration(t *testing.T) {
	m, f, _ := managerWithCache(t, []ToolDef{{Name: "search"}})
	reg := tools.NewRegistry()
	m.Init(ctx, reg)
	if m.Generation() != 0 {
		t.Fatal(m.Generation())
	}
	f.mu.Lock()
	f.tools = []ToolDef{{Name: "search"}, {Name: "create"}}
	f.mu.Unlock()
	if err := m.Reload(ctx, reg); err != nil {
		t.Fatal(err)
	}
	if m.Generation() != 1 {
		t.Fatal(m.Generation())
	}
	if defs, _ := m.readCache("gh"); len(defs) != 2 {
		t.Fatal("reload lists the tools again", defs)
	}
	m.closing.Wait()
	if m.connected("gh") || f.openConns() != 0 {
		t.Fatal("reload does not keep an on-demand server running")
	}
	st := m.Stats()
	if st.Servers != 1 || st.Tools != 2 || st.Pending != 0 {
		t.Fatalf("%+v", st)
	}
}

func TestReloadReadsServerList(t *testing.T) {
	m, f, _ := managerWithCache(t, []ToolDef{{Name: "search"}})
	reg := tools.NewRegistry()
	m.Init(ctx, reg)
	m.LoadServers = func() (map[string]config.MCPServer, error) {
		s := ghServer(false)["gh"]
		s.Enabled = false
		return map[string]config.MCPServer{"gh": s}, nil
	}
	if err := m.Reload(ctx, reg); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("mcp_gh_search"); ok || len(reg.All()) != 0 || f.dialCount() != 0 {
		t.Fatal("reload removes the tools of a disabled server", reg.All(), f.dialCount())
	}
	if st := m.Stats(); st.Servers != 0 || st.Tools != 0 {
		t.Fatalf("%+v", st)
	}
}

func TestReloadBadServerListChangesNothing(t *testing.T) {
	m, f, _ := managerWithCache(t, []ToolDef{{Name: "search"}})
	reg := tools.NewRegistry()
	m.Init(ctx, reg)
	m.LoadServers = func() (map[string]config.MCPServer, error) { return nil, errors.New("bad yaml") }
	_, err := m.ReloadReport(ctx, reg)
	if !errors.Is(err, ErrServerList) || !strings.Contains(err.Error(), "bad yaml") {
		t.Fatal(err)
	}
	if m.Generation() != 0 || toolNames(reg) != "mcp_gh_search" || f.dialCount() != 0 {
		t.Fatal("a bad server list changes nothing", m.Generation(), toolNames(reg), f.dialCount())
	}
}

func TestReloadAppliesToolChanges(t *testing.T) {
	m, f, _ := managerWithCache(t, []ToolDef{{Name: "search", Description: "old"}, {Name: "close"}})
	reg := tools.NewRegistry()
	reg.Register(builtin{"read_file"})
	m.Init(ctx, reg)
	f.mu.Lock()
	f.tools = []ToolDef{{Name: "search", Description: "new"}, {Name: "create"}}
	f.mu.Unlock()
	res, err := m.ReloadReport(ctx, reg)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolNames(reg); got != "mcp_gh_create,mcp_gh_search,read_file" {
		t.Fatal("reload adds new tools and removes old tools at once:", got)
	}
	if tool, _ := reg.Get("mcp_gh_search"); tool.Schema().Description != "new" {
		t.Fatal("reload applies a changed description")
	}
	if r := callTool(t, reg, "mcp_gh_create", `{}`); r.IsError || r.Text != "create {}" {
		t.Fatal(r)
	}
	if fmt.Sprint(res.Reloaded, res.Added, res.Removed) != "[gh] [] []" || res.Servers != 1 || res.Tools != 2 {
		t.Fatalf("%+v", res)
	}
}

func TestReloadAddsAndRemovesServers(t *testing.T) {
	gh := &fakeServer{tools: []ToolDef{{Name: "search"}}}
	fs := &fakeServer{tools: []ToolDef{{Name: "read"}, {Name: "write"}}}
	fakes := map[string]*fakeServer{"gh": gh, "fs": fs}
	m, _, _ := testManager(t, ghServer(false), fakes)
	reg := tools.NewRegistry()
	if err := m.Init(ctx, reg); err != nil {
		t.Fatal(err)
	}
	gen := m.Generation()
	m.LoadServers = func() (map[string]config.MCPServer, error) {
		s := ghServer(false)["gh"]
		s.Command = []string{"fs-mcp"}
		return map[string]config.MCPServer{"fs": s}, nil
	}
	res, err := m.ReloadReport(ctx, reg)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolNames(reg); got != "mcp_fs_read,mcp_fs_write" {
		t.Fatal(got)
	}
	if fmt.Sprint(res.Reloaded, res.Added, res.Removed) != "[] [fs] [gh]" || res.Servers != 1 || res.Tools != 2 {
		t.Fatalf("%+v", res)
	}
	if m.Generation() != gen+1 {
		t.Fatal("reload changes the generation, so the prompt is built again")
	}
	if st := m.Stats(); st.Servers != 1 || st.Tools != 2 || st.Pending != 0 {
		t.Fatalf("%+v", st)
	}
	// The server comes back.
	m.LoadServers = func() (map[string]config.MCPServer, error) {
		all := ghServer(false)
		all["fs"] = all["gh"]
		return all, nil
	}
	if res, err = m.ReloadReport(ctx, reg); err != nil {
		t.Fatal(err)
	}
	if got := toolNames(reg); got != "mcp_fs_read,mcp_fs_write,mcp_gh_search" {
		t.Fatal(got)
	}
	if fmt.Sprint(res.Reloaded, res.Added, res.Removed) != "[fs] [gh] []" || res.Tools != 3 {
		t.Fatalf("%+v", res)
	}
}

func TestReloadKeepsToolsOfFailedServer(t *testing.T) {
	m, f, _ := managerWithCache(t, []ToolDef{{Name: "search"}})
	reg := tools.NewRegistry()
	m.Init(ctx, reg)
	f.mu.Lock()
	f.failDials = 1
	f.tools = []ToolDef{{Name: "create"}}
	f.mu.Unlock()
	if _, err := m.ReloadReport(ctx, reg); err == nil {
		t.Fatal("a server that does not start is an error")
	}
	if got := toolNames(reg); got != "mcp_gh_search" {
		t.Fatal("the last known list stays:", got)
	}
}

func TestReloadSkipsNameOfOtherTool(t *testing.T) {
	m, f, _ := managerWithCache(t, []ToolDef{{Name: "search"}})
	reg := tools.NewRegistry()
	reg.Register(builtin{"mcp_gh_create"})
	m.Init(ctx, reg)
	f.mu.Lock()
	f.tools = []ToolDef{{Name: "search"}, {Name: "create"}}
	f.mu.Unlock()
	res, err := m.ReloadReport(ctx, reg)
	if err != nil {
		t.Fatal(err)
	}
	if tool, _ := reg.Get("mcp_gh_create"); tool.Toolset() != "file" {
		t.Fatal("reload does not replace a tool that is not from the server")
	}
	if fmt.Sprint(res.Skipped) != "[mcp_gh_create]" || res.Tools != 1 || m.Stats().Pending != 1 {
		t.Fatalf("%+v %+v", res, m.Stats())
	}
}

// TestReloadWhileTurnsRun runs with -race: reloads change the tools while other
// goroutines list and call them.
func TestReloadWhileTurnsRun(t *testing.T) {
	m, f, _ := managerWithCache(t, []ToolDef{{Name: "search"}})
	reg := tools.NewRegistry()
	reg.Register(builtin{"read_file"})
	m.Init(ctx, reg)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				names, schemas := reg.Visible(map[string]bool{"mcp": true, "file": true}, nil)
				if !names["read_file"] || len(schemas) != len(names) {
					t.Error("a reader lost a tool", names)
					return
				}
				for name := range names {
					if res := reg.Dispatch(ctx, name, nil, &tools.Env{Gate: &fakeGate{}}, names); res.IsError &&
						!strings.Contains(res.Text, "unknown tool") && !strings.Contains(res.Text, "not configured") &&
						!strings.Contains(res.Text, "disabled") && !strings.Contains(res.Text, "connection closed") {
						t.Error(res.Text)
						return
					}
				}
			}
		}()
	}
	for i := range 30 {
		f.mu.Lock()
		f.tools = []ToolDef{{Name: "search"}, {Name: fmt.Sprintf("t%d", i)}}
		f.mu.Unlock()
		if _, err := m.ReloadReport(ctx, reg); err != nil {
			t.Error(err)
		}
	}
	close(stop)
	wg.Wait()
	if got := toolNames(reg); got != "mcp_gh_search,mcp_gh_t29,read_file" {
		t.Fatal(got)
	}
}

// builtin is a tool that is not from an MCP server.
type builtin struct{ name string }

func (b builtin) Name() string       { return b.name }
func (b builtin) Toolset() string    { return "file" }
func (b builtin) ParallelSafe() bool { return true }
func (b builtin) Schema() provider.ToolSchema {
	return provider.ToolSchema{Name: b.name, Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (b builtin) Run(context.Context, json.RawMessage, *tools.Env) (string, error) { return "ok", nil }

// toolNames returns the names in reg, joined with commas.
func toolNames(reg *tools.Registry) string {
	var names []string
	for _, tool := range reg.All() {
		names = append(names, tool.Name())
	}
	return strings.Join(names, ",")
}

func TestReconnectBackoffMax5(t *testing.T) {
	m, f, _ := managerWithCache(t, []ToolDef{{Name: "search"}})
	var slept []time.Duration
	m.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	reg := tools.NewRegistry()
	m.Init(ctx, reg)
	f.mu.Lock()
	f.failDials = 5
	f.mu.Unlock()
	res := callTool(t, reg, "mcp_gh_search", `{}`)
	if !res.IsError || !strings.Contains(res.Text, "MCP server gh is not reachable after 5 tries") {
		t.Fatal(res)
	}
	if f.dialCount() != 5 {
		t.Fatal(f.dialCount())
	}
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second}
	if fmt.Sprint(slept) != fmt.Sprint(want) {
		t.Fatal(slept)
	}
	// The fourth try works.
	f.mu.Lock()
	f.failDials = 3
	f.mu.Unlock()
	if res := callTool(t, reg, "mcp_gh_search", `{}`); res.IsError {
		t.Fatal(res)
	}
}

func TestDeadConnectionRedials(t *testing.T) {
	const mayHaveRun = "The mcp_gh_create_issue call may have run. Do not send it again. Check the result first, or tell the owner."
	m, f, _ := managerWithCache(t, []ToolDef{{Name: "search"}, {Name: "create_issue"}})
	reg := tools.NewRegistry()
	m.Init(ctx, reg)
	callTool(t, reg, "mcp_gh_search", `{}`)
	// The server process dies.
	f.mu.Lock()
	f.open[0].Close()
	f.mu.Unlock()
	if res := callTool(t, reg, "mcp_gh_search", `{"a":1}`); res.IsError || f.dialCount() != 2 {
		t.Fatal(res, f.dialCount())
	}
	// A tool error does not redial.
	setCallErr(f, errors.New("mcp gh: search: rate limited"))
	if res := callTool(t, reg, "mcp_gh_search", `{}`); !res.IsError || res.Text != "mcp gh: search: rate limited" || f.dialCount() != 2 {
		t.Fatal(res, f.dialCount())
	}
	// A write call redials when the call did not run.
	setCallErr(f, errors.New("mcp gh: call create_issue: connection closed"))
	if res := callTool(t, reg, "mcp_gh_create_issue", `{}`); res.IsError || res.Text != "create_issue {}" || f.dialCount() != 3 {
		t.Fatal(res, f.dialCount())
	}
	// A timeout never redials. A write call gets the "may have run" text.
	setCallErr(f, chainErr{"mcp gh: call create_issue timed out after 2m0s", context.DeadlineExceeded})
	if res := callTool(t, reg, "mcp_gh_create_issue", `{}`); !res.IsError || res.Text != mayHaveRun || f.dialCount() != 3 {
		t.Fatal(res, f.dialCount())
	}
	setCallErr(f, chainErr{"mcp gh: call search timed out after 2m0s", context.DeadlineExceeded})
	if res := callTool(t, reg, "mcp_gh_search", `{}`); !res.IsError || res.Text != "mcp gh: call search timed out after 2m0s" || f.dialCount() != 3 {
		t.Fatal(res, f.dialCount())
	}
	// A JSON-RPC error did not run. It does not redial, and the model gets its text.
	setCallErr(f, chainErr{"mcp gh: call create_issue: invalid params", &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid params"}})
	if res := callTool(t, reg, "mcp_gh_create_issue", `{}`); !res.IsError || res.Text != "mcp gh: call create_issue: invalid params" || f.dialCount() != 3 {
		t.Fatal(res, f.dialCount())
	}
	// A reset after the write redials for a read call, as before.
	setCallErr(f, errors.New("mcp gh: call search: read tcp: connection reset by peer"))
	if res := callTool(t, reg, "mcp_gh_search", `{}`); res.IsError || f.dialCount() != 4 {
		t.Fatal(res, f.dialCount())
	}
	// A reset after the write does not redial for a write call.
	setCallErr(f, errors.New("mcp gh: call create_issue: read tcp: connection reset by peer"))
	if res := callTool(t, reg, "mcp_gh_create_issue", `{}`); !res.IsError || res.Text != mayHaveRun || f.dialCount() != 4 {
		t.Fatal(res, f.dialCount())
	}
}

// TestPendingCallMayHaveRun checks the errors that end a call already in flight. With the
// streamable HTTP transport, one failure of the connection ends each pending call, and the
// server can have run such a call. Only a read call redials and goes again.
func TestPendingCallMayHaveRun(t *testing.T) {
	m, f, _ := managerWithCache(t, []ToolDef{{Name: "search"}, {Name: "create_issue"},
		{Name: "send_email"}, {Name: "send_money"}})
	reg := tools.NewRegistry()
	m.Init(ctx, reg)
	callTool(t, reg, "mcp_gh_search", `{}`)
	rejected := &jsonrpc.Error{Code: -32005, Message: "rejected by transport"}
	pending := func(tool string) map[string]error {
		return map[string]error{
			"session missing": chainErr{"mcp gh: call " + tool + ": failed to reconnect: session not found",
				fmt.Errorf("failed to connect (session ID: x): %w", sdk.ErrSessionMissing)},
			"refused text":  errors.New("mcp gh: call " + tool + ": failed to reconnect (session ID: x): dial tcp: connection refused"),
			"refused errno": chainErr{"mcp gh: call " + tool + ": failed to reconnect", syscall.ECONNREFUSED},
			"rejected eof": chainErr{"mcp gh: call " + tool + ": POST: rejected by transport: EOF",
				fmt.Errorf("POST: %w: %w", rejected, io.EOF)},
		}
	}
	classes := map[string]Class{"create_issue": ClassWrite, "send_email": ClassSend, "send_money": ClassHumanOnly}
	for tool, want := range classes {
		if c := Classify(tool, "gh", false, nil); c != want {
			t.Fatal(tool, c)
		}
	}
	// A second try of a call that is not read would get no error. Thus the "may have run"
	// text shows that the call ran one time.
	for _, tool := range []string{"create_issue", "send_email", "send_money"} {
		for name, err := range pending(tool) {
			setCallErr(f, err)
			want := "The mcp_gh_" + tool + " call may have run. Do not send it again. Check the result first, or tell the owner."
			res := callToolWith(t, reg, "mcp_gh_"+tool, `{}`, &tools.Env{Gate: &fakeGate{}})
			if !res.IsError || res.Text != want {
				t.Fatalf("%s %s: %+v", tool, name, res)
			}
		}
	}
	// The connection of a call that may have run is closed. This call dials again.
	callTool(t, reg, "mcp_gh_search", `{}`)
	dials := f.dialCount()
	for name, err := range pending("search") {
		setCallErr(f, err)
		if res := callTool(t, reg, "mcp_gh_search", `{}`); res.IsError || res.Text != "search {}" || f.dialCount() != dials+1 {
			t.Fatalf("%s: %+v %d", name, res, f.dialCount())
		}
		dials = f.dialCount()
	}
	// A rejected request with no known cause gives the text to the model.
	setCallErr(f, chainErr{"mcp gh: call create_issue: POST: rejected by transport: bad header", rejected})
	if res := callTool(t, reg, "mcp_gh_create_issue", `{}`); !res.IsError || res.Text != "mcp gh: call create_issue: POST: rejected by transport: bad header" || f.dialCount() != dials {
		t.Fatal(res, f.dialCount())
	}
	// A closed connection and a broken pipe prove that the request was not written.
	setCallErr(f, chainErr{"mcp gh: call create_issue: connection closed", fmt.Errorf("%w: calling tools/call", sdk.ErrConnectionClosed)})
	if res := callTool(t, reg, "mcp_gh_create_issue", `{}`); res.IsError || res.Text != "create_issue {}" || f.dialCount() != dials+1 {
		t.Fatal(res, f.dialCount())
	}
	setCallErr(f, chainErr{"mcp gh: call create_issue: write |1: broken pipe", syscall.EPIPE})
	if res := callTool(t, reg, "mcp_gh_create_issue", `{}`); res.IsError || res.Text != "create_issue {}" || f.dialCount() != dials+2 {
		t.Fatal(res, f.dialCount())
	}
}

func TestDeadConnectionKeepsOtherCall(t *testing.T) {
	m, f, _ := managerWithCache(t, []ToolDef{{Name: "search"}, {Name: "slow"}})
	reg := tools.NewRegistry()
	m.Init(ctx, reg)
	f.mu.Lock()
	f.entered, f.proceed = make(chan struct{}), make(chan struct{})
	f.mu.Unlock()
	// The cleanup lets the slow call end when the test fails early.
	proceed := sync.OnceFunc(func() { close(f.proceed) })
	t.Cleanup(proceed)
	slow := make(chan tools.Result, 1)
	go func() { slow <- callTool(t, reg, "mcp_gh_slow", `{}`) }()
	<-f.entered
	// A different call on the same connection gets a transient error of the transport.
	f.mu.Lock()
	f.callErr = errors.New("mcp gh: call search: read tcp: connection reset by peer")
	f.mu.Unlock()
	if res := callTool(t, reg, "mcp_gh_search", `{}`); res.IsError || f.dialCount() != 2 {
		t.Fatal(res, f.dialCount())
	}
	m.closing.Wait()
	f.mu.Lock()
	first := f.open[0]
	f.mu.Unlock()
	first.mu.Lock()
	closed := first.closed
	first.mu.Unlock()
	if closed {
		t.Fatal("the dead-connection error of one call closed the connection of a call in flight")
	}
	proceed()
	if res := <-slow; res.IsError || res.Text != "slow {}" {
		t.Fatal(res)
	}
	if f.dialCount() != 2 {
		t.Fatal("the call in flight was sent again on a new connection:", f.dialCount())
	}
	// The last call on the dead connection closes it.
	m.closing.Wait()
	if f.openConns() != 1 {
		t.Fatal("open connections:", f.openConns())
	}
}

func TestOneDialAtATime(t *testing.T) {
	m, f, _ := managerWithCache(t, []ToolDef{{Name: "search"}})
	reg := tools.NewRegistry()
	m.Init(ctx, reg)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); callTool(t, reg, "mcp_gh_search", `{}`) }()
	}
	wg.Wait()
	if f.dialCount() != 1 {
		t.Fatal("concurrent cold calls share one dial:", f.dialCount())
	}
}

func TestCacheOfOtherServerIgnored(t *testing.T) {
	s := config.DefaultMCPServer()
	s.Command = []string{"ab-mcp"}
	f := &fakeServer{tools: []ToolDef{{Name: "right"}}}
	m, _, _ := testManager(t, map[string]config.MCPServer{"a_b": s}, map[string]*fakeServer{"a_b": f})
	// The server "a b" wrote the same cache file before a rename.
	writeCache(t, m, "a b", []ToolDef{{Name: "wrong"}})
	if _, err := m.readCache("a_b"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the cache of another server must count as no cache:", err)
	}
	reg := tools.NewRegistry()
	if err := m.Init(ctx, reg); err != nil {
		t.Fatal(err)
	}
	if got := toolNames(reg); got != "mcp_a_b_right" || f.dialCount() != 1 {
		t.Fatal(got, f.dialCount())
	}
	if defs, err := m.readCache("a_b"); err != nil || len(defs) != 1 || defs[0].Name != "right" {
		t.Fatal(defs, err)
	}
}

func TestNameClashSkipped(t *testing.T) {
	m, _, _ := managerWithCache(t, []ToolDef{{Name: "search"}, {Name: "search"}})
	reg := tools.NewRegistry()
	if err := m.Init(ctx, reg); err != nil {
		t.Fatal(err)
	}
	if len(reg.All()) != 1 {
		t.Fatal(len(reg.All()))
	}
}

func TestManagerWithRealStdioServer(t *testing.T) {
	s := stdioServer(t)
	m := &Manager{Servers: map[string]config.MCPServer{"echo": s}, CacheDir: t.TempDir(), BaseEnv: os.Environ(),
		Log: slog.New(slog.DiscardHandler)}
	t.Cleanup(m.Close)
	reg := tools.NewRegistry()
	if err := m.Init(ctx, reg); err != nil {
		t.Fatal(err)
	}
	res := callTool(t, reg, "mcp_echo_echo", `{"text":"hi"}`)
	if res.IsError || res.Text != "hi" {
		t.Fatal(res)
	}
}

// dialTriesManager returns a manager whose server gh has max_dial_tries set to tries.
func dialTriesManager(t *testing.T, tries int) (*Manager, *fakeServer, *[]time.Duration, *tools.Registry) {
	t.Helper()
	servers := ghServer(false)
	s := servers["gh"]
	s.MaxDialTries = tries
	servers["gh"] = s
	f := &fakeServer{tools: []ToolDef{{Name: "search"}}}
	m, _, waits := testManager(t, servers, map[string]*fakeServer{"gh": f})
	writeCache(t, m, "gh", f.tools)
	reg := tools.NewRegistry()
	if err := m.Init(ctx, reg); err != nil {
		t.Fatal(err)
	}
	return m, f, waits, reg
}

func TestDialTriesFromServerEntry(t *testing.T) {
	_, f, waits, reg := dialTriesManager(t, 2)
	f.mu.Lock()
	f.failDials = 9
	f.mu.Unlock()
	res := callTool(t, reg, "mcp_gh_search", `{}`)
	if !res.IsError || !strings.Contains(res.Text, "MCP server gh is not reachable after 2 tries") {
		t.Fatal(res)
	}
	if f.dialCount() != 2 {
		t.Fatal(f.dialCount())
	}
	if fmt.Sprint(*waits) != fmt.Sprint([]time.Duration{500 * time.Millisecond}) {
		t.Fatal(*waits)
	}
}

func TestDialTriesZeroUsesDefault(t *testing.T) {
	_, f, _, reg := dialTriesManager(t, 0)
	f.mu.Lock()
	f.failDials = 9
	f.mu.Unlock()
	res := callTool(t, reg, "mcp_gh_search", `{}`)
	if !res.IsError || !strings.Contains(res.Text, "MCP server gh is not reachable after 5 tries") {
		t.Fatal(res)
	}
	if f.dialCount() != 5 {
		t.Fatal(f.dialCount())
	}
}

func TestCacheKeepsHints(t *testing.T) {
	yes, no := true, false
	defs := []ToolDef{
		{Name: "lookup", ReadOnly: true},
		{Name: "wipe", Destructive: &yes},
		{Name: "keep", Destructive: &no},
		{Name: "plain"},
	}
	m, _, _ := managerWithCache(t, defs)
	got, err := m.readCache("gh")
	if err != nil || len(got) != 4 {
		t.Fatal(got, err)
	}
	if !got[0].ReadOnly || got[0].Destructive != nil ||
		got[1].Destructive == nil || !*got[1].Destructive ||
		got[2].Destructive == nil || *got[2].Destructive ||
		got[3].ReadOnly || got[3].Destructive != nil {
		t.Fatalf("%+v", got)
	}
}

func TestSameDefsComparesHints(t *testing.T) {
	yes, yes2, no := true, true, false
	base := ToolDef{Name: "t", Description: "d", InputSchema: json.RawMessage(`{}`)}
	ro, dy, dy2, dn := base, base, base, base
	ro.ReadOnly = true
	dy.Destructive = &yes
	dy2.Destructive = &yes2
	dn.Destructive = &no
	for i, tc := range []struct {
		a, b ToolDef
		same bool
	}{
		{base, base, true},
		{base, ro, false},
		{base, dy, false},
		{base, dn, false},
		{dy, dn, false},
		{dy, dy2, true}, // two pointers to equal values
	} {
		if got := sameDefs([]ToolDef{tc.a}, []ToolDef{tc.b}); got != tc.same {
			t.Errorf("case %d: sameDefs = %v, want %v", i, got, tc.same)
		}
	}
}

func TestHintChangeUpdatesCache(t *testing.T) {
	m, f, _ := managerWithCache(t, []ToolDef{{Name: "search"}})
	reg := tools.NewRegistry()
	m.Init(ctx, reg)
	f.mu.Lock()
	f.tools = []ToolDef{{Name: "search", ReadOnly: true}}
	f.mu.Unlock()
	callTool(t, reg, "mcp_gh_search", `{}`)
	defs, err := m.readCache("gh")
	if err != nil || len(defs) != 1 || !defs[0].ReadOnly {
		t.Fatal("a change in the hints alone rewrites the cache:", defs, err)
	}
}

// classOf returns the final class of the MCP tool name in reg.
func classOf(t *testing.T, reg *tools.Registry, name string) Class {
	t.Helper()
	tool, ok := reg.Get(name)
	if !ok {
		t.Fatalf("no tool %s", name)
	}
	return tool.(*mcpTool).class
}

func TestFilterHidesTools(t *testing.T) {
	defs := []ToolDef{{Name: "search_issues"}, {Name: "list_repos"}, {Name: "delete_repository"}, {Name: "admin_purge"}}
	servers := ghServer(false)
	s := servers["gh"]
	s.Tools.Include = []string{"search_*", "list_*", "delete_*", "nomatch_*"}
	s.Tools.Exclude = []string{"delete_*", "gone"}
	s.Tools.Class = map[string]string{"missing_tool": "read"}
	servers["gh"] = s
	f := &fakeServer{tools: defs}
	m, _, _ := testManager(t, servers, map[string]*fakeServer{"gh": f})
	writeCache(t, m, "gh", defs)
	reg := tools.NewRegistry()
	if err := m.Init(ctx, reg); err != nil {
		t.Fatal(err)
	}
	if got := toolNames(reg); got != "mcp_gh_list_repos,mcp_gh_search_issues" {
		t.Fatal("the filter hides the tools at registration:", got)
	}
	if st := m.Stats(); st.Tools != 4 || st.Pending != 0 {
		t.Fatalf("a filtered tool is not pending: %+v", st)
	}
	res, err := m.ReloadReport(ctx, reg)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`gh: tools.include entry "nomatch_*" matches no tool`,
		`gh: tools.exclude entry "gone" matches no tool`,
		`gh: tools.class entry "missing_tool" matches no tool`,
	}
	if res.Tools != 2 || len(res.Skipped) != 0 || !reflect.DeepEqual(res.Hidden, map[string]int{"gh": 2}) ||
		fmt.Sprint(res.Warnings) != fmt.Sprint(want) {
		t.Fatalf("%+v", res)
	}
	// A server with no filter has no Hidden entry.
	m2, _ := managerWithCacheNoFilter(t)
	res2, _ := m2.ReloadReport(ctx, tools.NewRegistry())
	if res2.Hidden != nil || res2.Warnings != nil {
		t.Fatalf("%+v", res2)
	}
}

// managerWithCacheNoFilter returns a Manager for the server "gh" with no tools block.
func managerWithCacheNoFilter(t *testing.T) (*Manager, *fakeServer) {
	t.Helper()
	m, f, _ := managerWithCache(t, []ToolDef{{Name: "search"}})
	return m, f
}

func TestFinalClass(t *testing.T) {
	security.SetHumanOnlyRules([]string{"mcp:gh.search_issues"})
	t.Cleanup(func() { security.SetHumanOnlyRules(nil) })
	defs := []ToolDef{
		{Name: "search_issues"},
		{Name: "create_refund"},
		{Name: "summarize", ReadOnly: true},
		{Name: "summarize_all"},
		{Name: "post_note"},
	}
	servers := ghServer(false)
	s := servers["gh"]
	s.Tools.Class = map[string]string{"summarize_all": "read", "create_refund": "read", "post_note": "write"}
	servers["gh"] = s
	f := &fakeServer{tools: defs}
	m, _, _ := testManager(t, servers, map[string]*fakeServer{"gh": f})
	writeCache(t, m, "gh", defs)
	reg := tools.NewRegistry()
	res, err := m.ReloadReport(ctx, reg)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]Class{
		"mcp_gh_search_issues": ClassHumanOnly, // the raise wins over the read verb
		"mcp_gh_create_refund": ClassHumanOnly, // an override cannot lower rule 1
		"mcp_gh_summarize":     ClassRead,      // the hint of a configured server
		"mcp_gh_summarize_all": ClassRead,      // the override
		"mcp_gh_post_note":     ClassWrite,     // the override
	} {
		if got := classOf(t, reg, name); got != want {
			t.Errorf("%s: got %s, want %s", name, got, want)
		}
	}
	want := []string{`gh: tools.class entry "create_refund" is ignored, because the tool is human-only`}
	if fmt.Sprint(res.Warnings) != fmt.Sprint(want) {
		t.Fatalf("%q", res.Warnings)
	}
	tool, _ := reg.Get("mcp_gh_post_note")
	if tool.(*mcpTool).hash != ConfigHash(s) {
		t.Fatal("the tool keeps the config hash of its server")
	}
}

func TestOldCacheWithNoHintsIsWrite(t *testing.T) {
	f := &fakeServer{tools: []ToolDef{{Name: "summarize", ReadOnly: true}}}
	m, _, _ := testManager(t, ghServer(false), map[string]*fakeServer{"gh": f})
	// A cache file of an older version has no hint fields.
	if err := os.MkdirAll(m.CacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.cachePath("gh"), []byte(`{"server":"gh","tools":[{"name":"summarize"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry()
	if err := m.Init(ctx, reg); err != nil {
		t.Fatal(err)
	}
	if got := classOf(t, reg, "mcp_gh_summarize"); got != ClassWrite {
		t.Fatal("a tool with no hints fails closed to write:", got)
	}
	// The next reload lists the hints, and the class becomes read.
	if _, err := m.ReloadReport(ctx, reg); err != nil {
		t.Fatal(err)
	}
	if got := classOf(t, reg, "mcp_gh_summarize"); got != ClassRead {
		t.Fatal("after the reload:", got)
	}
}
