package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/tools"
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

func callTool(t *testing.T, reg *tools.Registry, name, args string) tools.Result {
	t.Helper()
	tool, ok := reg.Get(name)
	if !ok {
		t.Fatalf("no tool %s", name)
	}
	return reg.Dispatch(ctx, name, json.RawMessage(args), &tools.Env{}, map[string]bool{tool.Name(): true})
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
					if res := reg.Dispatch(ctx, name, nil, &tools.Env{}, names); res.IsError &&
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
	m, f, _ := managerWithCache(t, []ToolDef{{Name: "search"}})
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
	f.mu.Lock()
	f.callErr = errors.New("mcp gh: search: rate limited")
	f.mu.Unlock()
	if res := callTool(t, reg, "mcp_gh_search", `{}`); !res.IsError || res.Text != "mcp gh: search: rate limited" || f.dialCount() != 2 {
		t.Fatal(res, f.dialCount())
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
