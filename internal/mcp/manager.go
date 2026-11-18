package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/tools"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The reconnect limits: maxDialTries is the number of dial tries of a cold call when
// the server entry gives 0. The call waits backoffBase × 2ⁿ between the tries.
const (
	maxDialTries = 5
	backoffBase  = 500 * time.Millisecond
)

// ErrServerList means that ReloadReport cannot read the server entries. The reload
// changes nothing.
var ErrServerList = errors.New("mcp: cannot read the server list")

// defaultIdleStop is the idle time of a server entry with no idle_stop_minutes.
const defaultIdleStop = 10 * time.Minute

// conn is one live connection to an MCP server. *Client implements it.
type conn interface {
	ListTools(ctx context.Context) ([]ToolDef, error)
	Call(ctx context.Context, tool string, args json.RawMessage) (string, error)
	Close() error
}

// Manager starts the MCP servers on demand and stops them when they are idle.
// At startup it registers the tools from the cached tool lists, so no server process
// starts. The first call to a tool of a server starts the server. It is safe for
// concurrent use.
type Manager struct {
	// Servers holds the server entries by name. Only the enabled entries are used.
	Servers map[string]config.MCPServer
	// CacheDir holds one tool list file per server: <data dir>/cache/mcp-tools.
	CacheDir string
	// Now returns the current time. Nil uses time.Now.
	Now func() time.Time
	Log *slog.Logger
	// BaseEnv and DotEnv make the environment of a stdio server (see Dial). A nil
	// BaseEnv uses os.Environ().
	BaseEnv []string
	DotEnv  map[string]string
	// LoadServers reads the server entries again for Reload. Nil keeps Servers.
	LoadServers func() (map[string]config.MCPServer, error)

	// dial and sleep are test seams. Nil uses Dial and a timer.
	dial  func(ctx context.Context, name string, s config.MCPServer) (conn, error)
	sleep func(ctx context.Context, d time.Duration) error

	gen atomic.Uint64
	// reloadMu makes sure that one Init or Reload runs at a time.
	reloadMu sync.Mutex

	mu      sync.Mutex
	servers map[string]*serverState
	// closing counts the connections that close in the background.
	closing sync.WaitGroup
}

// serverState is the run state of one server.
type serverState struct {
	name string
	// dialMu makes sure that one dial runs at a time.
	dialMu sync.Mutex

	mu       sync.Mutex
	cfg      config.MCPServer
	disabled bool
	conn     conn
	// inflight counts the calls in flight on each connection. dead holds the connections
	// that a call found dead. A dead connection closes when its last call ends.
	inflight map[conn]int
	dead     map[conn]bool
	lastUsed time.Time
	// defs is the latest known tool list. registered holds the names of the tools of defs
	// that are in the registry.
	defs       []ToolDef
	registered map[string]bool
	// filtered holds the raw names of the tools that the filter hides.
	filtered map[string]bool
}

// Init loads the tool list of each enabled server and registers one tool per entry in
// reg. A server with a cache file starts no process. A server with no cache
// file is started once to list its tools, and then stopped. An always_on server is
// started and stays connected. A server that fails does not stop the others: Init logs
// the failure and returns all failures joined.
func (m *Manager) Init(ctx context.Context, reg *tools.Registry) error {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	m.mu.Lock()
	m.servers = map[string]*serverState{}
	for name, s := range m.Servers {
		if s.Enabled {
			m.servers[name] = &serverState{name: name, cfg: s, registered: map[string]bool{}}
		}
	}
	states := m.sortedStates()
	m.mu.Unlock()

	errs := make([]error, len(states))
	var wg sync.WaitGroup
	for i, st := range states {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = m.initServer(ctx, st)
		}()
	}
	wg.Wait()
	m.apply(reg)
	return errors.Join(errs...)
}

// initServer loads the tool list of one server from the cache, or from the server.
func (m *Manager) initServer(ctx context.Context, st *serverState) error {
	defs, cacheErr := m.readCache(st.name)
	st.mu.Lock()
	st.defs = defs
	alwaysOn := st.cfg.AlwaysOn
	st.mu.Unlock()
	if cacheErr == nil && !alwaysOn {
		return nil
	}
	if cacheErr != nil && !errors.Is(cacheErr, os.ErrNotExist) {
		m.logger().Warn("mcp: the tool cache is not valid; the server is listed again", "server", st.name, "err", cacheErr.Error())
	}
	st.dialMu.Lock()
	defer st.dialMu.Unlock()
	c, err := m.dialer()(ctx, st.name, st.config())
	if err != nil {
		m.logger().Warn("mcp: cannot start the server", "server", st.name, "err", err.Error())
		return err
	}
	m.refreshList(ctx, st, c)
	if alwaysOn {
		st.mu.Lock()
		st.conn, st.lastUsed = c, m.now()
		st.mu.Unlock()
		return nil
	}
	m.closeLater(c)
	return nil
}

// apply makes the MCP tools in reg match the tool lists of the enabled servers, in one
// registry change: it removes each tool of m from reg, and it adds one tool for each entry
// of an enabled server. A tool whose name is already in reg is skipped with a warning. It
// returns the count of MCP tools in reg, the skipped names, the count of tools that the
// filter hides, and the filter warnings.
func (m *Manager) apply(reg *tools.Registry) applyResult {
	var skipped []string
	var res applyResult
	var remove []string
	for _, t := range reg.All() {
		if mt, ok := t.(*mcpTool); ok && mt.m == m {
			remove = append(remove, mt.name)
		}
	}
	var add []tools.Tool
	owner := map[string]*serverState{}
	for _, st := range m.states() {
		st.mu.Lock()
		st.registered = map[string]bool{}
		st.filtered = map[string]bool{}
		if !st.disabled {
			kept, hidden, warns := CheckFilter(st.cfg, st.defs)
			keptSet := map[string]bool{}
			for _, d := range kept {
				keptSet[d.Name] = true
			}
			for _, d := range st.defs {
				if !keptSet[d.Name] {
					st.filtered[d.Name] = true
				}
			}
			if len(st.cfg.Tools.Include) > 0 || len(st.cfg.Tools.Exclude) > 0 {
				if res.hidden == nil {
					res.hidden = map[string]int{}
				}
				res.hidden[st.name] = hidden
			}
			for _, w := range warns {
				m.logger().Warn("mcp: tools config", "server", st.name, "warning", w)
				res.warnings = append(res.warnings, st.name+": "+w)
			}
			hash := ConfigHash(st.cfg)
			for _, d := range kept {
				name := ToolName(st.name, d.Name)
				if owner[name] != nil {
					skipped = append(skipped, name)
					m.logger().Warn("mcp: a tool with this name exists already; the MCP tool is skipped", "server", st.name, "tool", d.Name, "name", name)
					continue
				}
				owner[name] = st
				class, ignored := finalClass(st.name, d, st.cfg.Tools.Class)
				if ignored {
					w := fmt.Sprintf("tools.class entry %q is ignored, because the tool is human-only", d.Name)
					m.logger().Warn("mcp: tools config", "server", st.name, "warning", w)
					res.warnings = append(res.warnings, st.name+": "+w)
				}
				add = append(add, &mcpTool{m: m, server: st.name, tool: d.Name, name: name, def: d, parallel: st.cfg.SupportsParallelToolCalls, class: class, hash: hash})
			}
		}
		st.mu.Unlock()
	}
	clash := reg.Replace(remove, add)
	for _, name := range clash {
		m.logger().Warn("mcp: a tool with this name exists already; the MCP tool is skipped", "server", owner[name].name, "name", name)
		delete(owner, name)
	}
	for name, st := range owner {
		st.mu.Lock()
		st.registered[name] = true
		st.mu.Unlock()
	}
	res.count, res.skipped = len(owner), append(skipped, clash...)
	return res
}

// applyResult is the result of apply.
type applyResult struct {
	count    int
	skipped  []string
	hidden   map[string]int
	warnings []string
}

// finalClass returns the class of the tool d of a server. A human-only rule wins. Then
// Classify gives the class. A human-only result stays. Else an override entry sets the
// class. ignored is true when an override entry exists for a human-only tool.
func finalClass(server string, d ToolDef, override map[string]string) (c Class, ignored bool) {
	if security.IsHumanOnlyMCP(server, d.Name) {
		c = ClassHumanOnly
	} else {
		c = Classify(d.Name, server, d.ReadOnly, d.Destructive)
	}
	v, has := override[d.Name]
	if c == ClassHumanOnly {
		return c, has
	}
	if has {
		return Class(v), false
	}
	return c, false
}

// ToolName returns the registry name of an MCP tool: mcp_<server>_<tool>. A character
// that a provider does not accept in a tool name becomes "_" (see config.MCPSafeName).
func ToolName(server, tool string) string {
	return "mcp_" + config.MCPSafeName(server) + "_" + config.MCPSafeName(tool)
}

// mayHaveRunText is the error text for a call that may have reached the server.
func mayHaveRunText(server, tool string) string {
	return "The " + ToolName(server, tool) + " call may have run. Do not send it again. Check the result first, or tell the owner."
}

// call runs one tool call. A cold server starts first. A call that did not run because the
// connection is dead dials again and tries one more time. A call that is not read never
// goes again when it may have run.
func (m *Manager) call(ctx context.Context, server, tool string, class Class, args json.RawMessage) (string, error) {
	st := m.state(server)
	if st == nil {
		return "", fmt.Errorf("MCP server %s is not configured", server)
	}
	for attempt := 0; ; attempt++ {
		c, err := m.acquire(ctx, st)
		if err != nil {
			return "", err
		}
		out, err := c.Call(ctx, tool, args)
		f := faultNone
		if err != nil && ctx.Err() == nil {
			f = callFault(server, tool, class, err)
		}
		m.release(st, c, f == faultNotRun || f == faultBroken)
		if f == faultNone || f == faultOther {
			return out, err
		}
		retry := attempt == 0 && (f == faultNotRun || (f == faultBroken && class == ClassRead))
		if !retry {
			if class != ClassRead && (f == faultBroken || f == faultTimeout) {
				return "", fmt.Errorf("%w: %s", tools.ErrToolError, mayHaveRunText(server, tool))
			}
			return out, err
		}
		m.logger().Warn("mcp: the connection is dead; the server is started again", "server", server, "err", err.Error())
	}
}

// acquire returns a live connection of st and counts one call in flight. It dials when
// the server is cold, with backoff, as many times as the tries of the server entry.
func (m *Manager) acquire(ctx context.Context, st *serverState) (conn, error) {
	if c, err := m.take(st); c != nil || err != nil {
		return c, err
	}
	st.dialMu.Lock()
	defer st.dialMu.Unlock()
	if c, err := m.take(st); c != nil || err != nil {
		return c, err
	}
	var last error
	tries := st.dialTries()
	for try := 1; try <= tries; try++ {
		if try > 1 {
			if err := m.wait(ctx, backoffBase<<(try-2)); err != nil {
				return nil, err
			}
		}
		c, err := m.dialer()(ctx, st.name, st.config())
		if err == nil {
			m.refreshList(ctx, st, c)
			st.mu.Lock()
			st.conn, st.lastUsed = c, m.now()
			st.use(c)
			st.mu.Unlock()
			m.logger().Info("mcp: server started", "server", st.name, "tries", try)
			return c, nil
		}
		last = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		m.logger().Warn("mcp: cannot start the server", "server", st.name, "try", try, "err", err.Error())
	}
	return nil, fmt.Errorf("%w: MCP server %s is not reachable after %d tries: %v", tools.ErrToolError, st.name, tries, last)
}

// take returns the live connection of st and counts one call in flight, or nil.
func (m *Manager) take(st *serverState) (conn, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.disabled {
		return nil, fmt.Errorf("%w: MCP server %s is disabled", tools.ErrToolError, st.name)
	}
	if st.conn == nil {
		return nil, nil
	}
	st.use(st.conn)
	st.lastUsed = m.now()
	return st.conn, nil
}

// use counts one call in flight on c. The caller holds st.mu.
func (st *serverState) use(c conn) {
	if st.inflight == nil {
		st.inflight = map[conn]int{}
	}
	st.inflight[c]++
}

// release ends one call in flight on c. When dead is true, c is no longer the live
// connection of st, so the next call dials again. Other calls can still use c, so a dead
// connection closes only when its last call ends.
func (m *Manager) release(st *serverState, c conn, dead bool) {
	st.mu.Lock()
	st.lastUsed = m.now()
	if dead {
		if st.conn == c {
			st.conn = nil
		}
		if st.dead == nil {
			st.dead = map[conn]bool{}
		}
		st.dead[c] = true
	}
	st.inflight[c]--
	last := st.inflight[c] <= 0
	closeNow := last && st.dead[c]
	if last {
		delete(st.inflight, c)
		delete(st.dead, c)
	}
	st.mu.Unlock()
	if closeNow {
		m.closeLater(c)
	}
}

// refreshList lists the tools on a new connection. When the list changed, it updates the
// cache. The registry keeps the old list until the next /reload-mcp or start, so
// the tool list does not change in the middle of a conversation.
func (m *Manager) refreshList(ctx context.Context, st *serverState, c conn) {
	defs, err := c.ListTools(ctx)
	if err != nil {
		m.logger().Warn("mcp: cannot list the tools", "server", st.name, "err", err.Error())
		return
	}
	st.mu.Lock()
	same := sameDefs(st.defs, defs)
	first := st.defs == nil
	st.defs = defs
	st.mu.Unlock()
	if same && !first {
		return
	}
	if err := m.writeCache(st.name, defs); err != nil {
		m.logger().Warn("mcp: cannot write the tool cache", "server", st.name, "err", err.Error())
		return
	}
	if !first {
		m.logger().Info("mcp: the tool list changed; /reload-mcp or the next start applies the new list", "server", st.name, "tools", len(defs))
	}
}

// ReapIdle stops each server that is not always_on and had no call for its
// idle_stop_minutes (default 10). The app calls it each minute.
func (m *Manager) ReapIdle() {
	now := m.now()
	for _, st := range m.states() {
		st.mu.Lock()
		idle := time.Duration(st.cfg.IdleStopMinutes) * time.Minute
		if idle <= 0 {
			idle = defaultIdleStop
		}
		var c conn
		if st.conn != nil && !st.cfg.AlwaysOn && st.inflight[st.conn] == 0 && now.Sub(st.lastUsed) >= idle {
			c, st.conn = st.conn, nil
		}
		st.mu.Unlock()
		if c != nil {
			m.logger().Info("mcp: idle server stopped", "server", st.name)
			m.closeLater(c)
		}
	}
}

// ReloadResult is the result of ReloadReport.
type ReloadResult struct {
	// Reloaded holds the servers that were enabled before and after the reload. Added holds
	// the servers that are enabled only after it, and Removed the servers that were enabled
	// only before it. Each list is sorted.
	Reloaded, Added, Removed []string
	// Servers is the count of enabled servers. Tools is the count of MCP tools in the registry.
	Servers, Tools int
	// Skipped holds the MCP tool names that are not in the registry, because a different
	// tool has the name.
	Skipped []string
	// Hidden holds the count of tools that tools.include and tools.exclude hide, for each
	// enabled server that has one of the two lists. It is nil when no server has a filter.
	Hidden map[string]int
	// Warnings holds the filter entries that match no tool and the ignored class overrides.
	// Each warning starts with "<server>: ".
	Warnings []string
}

// Reload is ReloadReport without the result.
func (m *Manager) Reload(ctx context.Context, reg *tools.Registry) error {
	_, err := m.ReloadReport(ctx, reg)
	return err
}

// ReloadReport reads the server entries again (LoadServers), stops all servers, and lists
// the tools of each enabled server again. It updates the caches, and it starts the
// always_on servers. Then it changes reg in one step: it adds the new tools, it updates the
// changed tools, and it removes the tools of removed or disabled servers. A server that
// does not start keeps its last known tools. ReloadReport increments Generation, so each
// session builds its system prompt again. When LoadServers fails, ReloadReport changes
// nothing and returns an error that wraps ErrServerList.
//
// A turn that runs keeps its tool list until it ends. A call of that turn to a removed tool
// returns an error.
func (m *Manager) ReloadReport(ctx context.Context, reg *tools.Registry) (ReloadResult, error) {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	var res ReloadResult
	if m.LoadServers != nil {
		servers, err := m.LoadServers()
		if err != nil {
			return res, fmt.Errorf("%w: %w", ErrServerList, err)
		}
		m.mu.Lock()
		m.Servers = servers
		m.mu.Unlock()
	}
	defer m.gen.Add(1)
	before := m.enabled()
	m.stopAll()

	m.mu.Lock()
	if m.servers == nil {
		m.servers = map[string]*serverState{}
	}
	var active []*serverState
	for name, st := range m.servers {
		s, ok := m.Servers[name]
		st.mu.Lock()
		st.disabled = !ok || !s.Enabled
		if ok {
			st.cfg = s
		}
		st.mu.Unlock()
		if ok && s.Enabled {
			active = append(active, st)
		}
	}
	for name, s := range m.Servers {
		if _, ok := m.servers[name]; !ok && s.Enabled {
			st := &serverState{name: name, cfg: s, registered: map[string]bool{}}
			m.servers[name] = st
			active = append(active, st)
		}
	}
	m.mu.Unlock()

	errs := make([]error, len(active))
	var wg sync.WaitGroup
	for i, st := range active {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = m.relist(ctx, st)
		}()
	}
	wg.Wait()
	ar := m.apply(reg)
	res.Tools, res.Skipped, res.Hidden, res.Warnings = ar.count, ar.skipped, ar.hidden, ar.warnings

	after := m.enabled()
	res.Servers = len(after)
	for _, name := range slices.Sorted(maps.Keys(after)) {
		if before[name] {
			res.Reloaded = append(res.Reloaded, name)
		} else {
			res.Added = append(res.Added, name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(before)) {
		if !after[name] {
			res.Removed = append(res.Removed, name)
		}
	}
	return res, errors.Join(errs...)
}

// enabled returns the names of the servers that are not disabled.
func (m *Manager) enabled() map[string]bool {
	out := map[string]bool{}
	for _, st := range m.states() {
		st.mu.Lock()
		if !st.disabled {
			out[st.name] = true
		}
		st.mu.Unlock()
	}
	return out
}

// relist starts the server, lists its tools, and writes the cache. The server stays
// connected only when it is always_on.
func (m *Manager) relist(ctx context.Context, st *serverState) error {
	st.dialMu.Lock()
	defer st.dialMu.Unlock()
	c, err := m.dialer()(ctx, st.name, st.config())
	if err != nil {
		m.logger().Warn("mcp: reload: cannot start the server", "server", st.name, "err", err.Error())
		return err
	}
	defs, err := c.ListTools(ctx)
	if err != nil {
		m.closeLater(c)
		return err
	}
	if err := m.writeCache(st.name, defs); err != nil {
		m.logger().Warn("mcp: cannot write the tool cache", "server", st.name, "err", err.Error())
	}
	st.mu.Lock()
	st.defs = defs
	keep := st.cfg.AlwaysOn
	if keep {
		st.conn, st.lastUsed = c, m.now()
	}
	st.mu.Unlock()
	if !keep {
		m.closeLater(c)
	}
	return nil
}

// Stats are the counts of the enabled servers and their tools.
type Stats struct {
	Servers, Tools int
	// Pending counts the tools in the tool lists that are not in the registry: a list that
	// changed after the last reload, or a name that a different tool has.
	Pending int
}

// Stats returns the counts of the enabled servers and their tools.
func (m *Manager) Stats() Stats {
	var s Stats
	for _, st := range m.states() {
		st.mu.Lock()
		if !st.disabled {
			s.Servers++
			s.Tools += len(st.defs)
			for _, d := range st.defs {
				if !st.registered[ToolName(st.name, d.Name)] && !st.filtered[d.Name] {
					s.Pending++
				}
			}
		}
		st.mu.Unlock()
	}
	return s
}

// Generation returns the MCP generation. Each Reload increments it. It is the
// Agent.MCPGen of the prompt key.
func (m *Manager) Generation() uint64 { return m.gen.Load() }

// Close stops all servers and waits until they have stopped.
func (m *Manager) Close() {
	m.stopAll()
	m.closing.Wait()
}

// stopAll closes all live connections in the background.
func (m *Manager) stopAll() {
	for _, st := range m.states() {
		st.mu.Lock()
		c := st.conn
		st.conn = nil
		st.mu.Unlock()
		if c != nil {
			m.closeLater(c)
		}
	}
}

// closeLater closes c in its own goroutine, because a stdio server can take some seconds
// to stop. Close waits for it.
func (m *Manager) closeLater(c conn) {
	m.closing.Add(1)
	go func() {
		defer m.closing.Done()
		if err := c.Close(); err != nil {
			m.logger().Warn("mcp: close", "err", err.Error())
		}
	}()
}

// connected reports whether the server has a live connection.
func (m *Manager) connected(server string) bool {
	st := m.state(server)
	if st == nil {
		return false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.conn != nil
}

func (m *Manager) state(name string) *serverState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.servers[name]
}

// states returns the server states in name order.
func (m *Manager) states() []*serverState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sortedStates()
}

// sortedStates returns the server states in name order. The caller holds m.mu.
func (m *Manager) sortedStates() []*serverState {
	out := make([]*serverState, 0, len(m.servers))
	for _, name := range slices.Sorted(maps.Keys(m.servers)) {
		out = append(out, m.servers[name])
	}
	return out
}

func (st *serverState) config() config.MCPServer {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.cfg
}

// dialTries returns the number of dial tries of the server entry, or maxDialTries when
// the entry gives 0.
func (st *serverState) dialTries() int {
	if n := st.config().MaxDialTries; n > 0 {
		return n
	}
	return maxDialTries
}

func (m *Manager) dialer() func(ctx context.Context, name string, s config.MCPServer) (conn, error) {
	if m.dial != nil {
		return m.dial
	}
	return func(ctx context.Context, name string, s config.MCPServer) (conn, error) {
		env := m.BaseEnv
		if env == nil {
			env = os.Environ()
		}
		c, err := dialWithLog(ctx, name, s, env, m.DotEnv, m.logger())
		if err != nil {
			return nil, err
		}
		return c, nil
	}
}

// wait waits d, or until ctx ends.
func (m *Manager) wait(ctx context.Context, d time.Duration) error {
	if m.sleep != nil {
		return m.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) logger() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.Default()
}

// fault is the class of a failed call.
type fault int

const (
	faultNone    fault = iota
	faultNotRun        // the call did not reach the server; a new connection can help
	faultBroken        // the connection broke after the write; the call may have run
	faultTimeout       // the call timed out; the call may have run
	faultOther         // a JSON-RPC error or a tool error; the model gets the text
)

// codeRejected is the JSON-RPC code of the "rejected by transport" error. The streamable
// HTTP client wraps each failed HTTP request in it, so the cause below it decides the fault.
const codeRejected = -32005

// callFault classifies a call error. It checks the error chain first. The text decides
// only for an error with no known cause in the chain, and only for the request prefix
// "mcp <server>: call <tool>". An error result of the tool has no "call", so a tool error
// never starts a second call.
//
// Only a closed connection or a broken pipe proves that the request was not written. A lost
// session or a refused reconnect can also end a call that the server already got, because
// the streamable HTTP client ends each pending call with the error of the connection. Thus
// such an error is faultNotRun only for a read call. For other classes, it is faultBroken.
func callFault(server, tool string, class Class, err error) fault {
	lost := faultNotRun
	if class != ClassRead {
		lost = faultBroken
	}
	var rpc *jsonrpc.Error
	switch {
	case errors.Is(err, sdk.ErrConnectionClosed), errors.Is(err, syscall.EPIPE):
		return faultNotRun
	case errors.Is(err, sdk.ErrSessionMissing), errors.Is(err, syscall.ECONNREFUSED):
		return lost
	case errors.As(err, &rpc) && rpc.Code != codeRejected:
		if rpc.Code == -32003 || rpc.Code == -32004 {
			return faultNotRun
		}
		return faultOther
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, syscall.ECONNRESET):
		return faultBroken
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return faultTimeout
	}
	prefix := "mcp " + server + ": call " + tool
	msg, ok := strings.CutPrefix(err.Error(), prefix)
	if !ok {
		return faultOther
	}
	msg = strings.ToLower(msg)
	has := func(list ...string) bool {
		for _, s := range list {
			if strings.Contains(msg, s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("connection closed", "client is closing", "server is closing", "broken pipe"):
		return faultNotRun
	case has("session not found", "connection refused"):
		return lost
	case has("connection reset", "eof"):
		return faultBroken
	case has("timed out"):
		return faultTimeout
	}
	return faultOther
}

// cacheFile is the JSON shape of a tool cache file.
type cacheFile struct {
	Server string       `json:"server"`
	Tools  []cachedTool `json:"tools"`
}

type cachedTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	ReadOnly    bool            `json:"read_only,omitempty"`
	Destructive *bool           `json:"destructive,omitempty"`
}

func (m *Manager) cachePath(server string) string {
	return filepath.Join(m.CacheDir, config.MCPSafeName(server)+".json")
}

// readCache returns the cached tool list of the server. A missing file returns an error
// that wraps os.ErrNotExist. A file of a different server also returns such an error, because
// two server names can have the same file name.
func (m *Manager) readCache(server string) ([]ToolDef, error) {
	b, err := os.ReadFile(m.cachePath(server))
	if err != nil {
		return nil, err
	}
	var f cacheFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", m.cachePath(server), err)
	}
	if f.Server != server {
		return nil, fmt.Errorf("%s holds the tools of the server %q: %w", m.cachePath(server), f.Server, os.ErrNotExist)
	}
	defs := make([]ToolDef, 0, len(f.Tools))
	for _, t := range f.Tools {
		defs = append(defs, ToolDef(t))
	}
	return defs, nil
}

// writeCache writes the tool list of the server atomically, with mode 0600.
func (m *Manager) writeCache(server string, defs []ToolDef) error {
	f := cacheFile{Server: server, Tools: make([]cachedTool, 0, len(defs))}
	for _, d := range defs {
		f.Tools = append(f.Tools, cachedTool(d))
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.CacheDir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(m.CacheDir, ".mcp-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), m.cachePath(server))
}

// sameDefs reports whether two tool lists are equal.
func sameDefs(a, b []ToolDef) bool {
	return slices.EqualFunc(a, b, func(x, y ToolDef) bool {
		return x.Name == y.Name && x.Description == y.Description && string(x.InputSchema) == string(y.InputSchema) &&
			x.ReadOnly == y.ReadOnly && sameHint(x.Destructive, y.Destructive)
	})
}

// sameHint reports whether two optional hints are equal. It compares the values.
func sameHint(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// mcpTool is one tool of an MCP server in the registry. A read call runs with no gate.
// Each other call goes through the approval gate. apply sets class
// (the final class) and hash (the ConfigHash of the server entry).
type mcpTool struct {
	m                  *Manager
	server, tool, name string
	def                ToolDef
	parallel           bool
	class              Class
	hash               string
}

func (t *mcpTool) Name() string       { return t.name }
func (t *mcpTool) Toolset() string    { return tools.SetMCP + "_" + t.server }
func (t *mcpTool) ParallelSafe() bool { return t.parallel && t.class == ClassRead }

// MCPClass returns the final class of the tool.
func (t *mcpTool) MCPClass() string { return string(t.class) }

// firstSentence returns the first line of s, cut after the first ". ".
func firstSentence(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i+1]
	}
	return strings.TrimSpace(s)
}

func (t *mcpTool) Schema() provider.ToolSchema {
	params := t.def.InputSchema
	if len(params) == 0 || string(params) == "null" || string(params) == "{}" {
		params = json.RawMessage(`{"type":"object"}`)
	}
	return provider.ToolSchema{Name: t.name, Description: t.def.Description, Parameters: params}
}

func (t *mcpTool) Run(ctx context.Context, args json.RawMessage, env *tools.Env) (string, error) {
	if t.class != ClassRead {
		var ap tools.MCPApprover
		if env != nil && env.Gate != nil {
			ap, _ = env.Gate.(tools.MCPApprover)
		}
		if ap == nil {
			return "", fmt.Errorf("%w: approval gate missing: the MCP call %s cannot run without a gate", tools.ErrToolError, t.name)
		}
		call := security.MCPCall{Server: t.server, Tool: t.tool, Class: string(t.class), ConfigHash: t.hash,
			Summary: firstSentence(t.def.Description), Args: args}
		if err := ap.CheckMCP(ctx, env, call); err != nil {
			return "", fmt.Errorf("%w: %s", tools.ErrToolError, err.Error())
		}
	}
	out, err := t.m.call(ctx, t.server, t.tool, t.class, args)
	if err != nil && !errors.Is(err, tools.ErrToolError) {
		// The client error text is for the model, and it has no credentials.
		err = fmt.Errorf("%w: %s", tools.ErrToolError, err.Error())
	}
	return out, err
}
