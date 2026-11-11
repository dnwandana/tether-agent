package goals

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// defaultJudgeTimeout is the judge timeout when the config gives 0 or less.
const defaultJudgeTimeout = 30 * time.Second

// Deps holds the functions that app injects, because goals cannot import gateway, agent, or
// app.
type Deps struct {
	// Store keeps the goal rows.
	Store *Store
	// Sessions keeps the hidden judge sessions. Nil records no judge call.
	Sessions *sessions.Store
	// Cfg returns the goals config. Nil uses the defaults.
	Cfg func() config.GoalsConfig
	// ProviderFor returns the provider chain of a session.
	ProviderFor func(*sessions.Session) provider.Provider
	// ChainFor returns the provider chain that starts at a config entry.
	ChainFor func(entry string) provider.Provider
	// Meta gives the meta of a judge reply. Nil uses the stamp of the result with no price.
	Meta func(provider.Provider, provider.Result) sessions.MessageMeta
	// RunningBatches returns the number of background delegate_task batches of a key.
	RunningBatches func(key string) int
	// Procs is the background process registry. Nil finds no process.
	Procs *tools.ProcRegistry
	// Exec runs a gate command for a session.
	Exec func(ctx context.Context, sess *sessions.Session, command string, timeout time.Duration) (string, int, error)
	// TodoOpen returns the open items of the todo list of a session, or "".
	TodoOpen func(ctx context.Context, sessionID string) string
	// Alive reports if a pid runs. Nil uses PIDAlive.
	Alive func(pid int) bool
	// Now returns the time. Nil uses time.Now.
	Now func() time.Time
	// Log receives the warnings. Nil uses slog.Default.
	Log *slog.Logger
	// InGateway and SudoPasswordSet feed the gate check before each run, as they feed the
	// approval gate.
	InGateway       bool
	SudoPasswordSet bool
}

// cacheEntry is the last known revision, epoch, and status of the goal of a session.
type cacheEntry struct {
	rev, epoch int64
	status     string
}

// Manager is the one entry point of the gateway and the CLI to the goal state. It keeps a
// cache so that the stale check of a queued continuation makes no database call.
type Manager struct {
	d        Deps
	judgeLog *JudgeLog

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// NewManager returns a manager over the deps d.
func NewManager(d Deps) *Manager {
	return &Manager{
		d:        d,
		judgeLog: &JudgeLog{Sessions: d.Sessions, Meta: d.Meta},
		cache:    map[string]cacheEntry{},
	}
}

// now returns the time of the deps, or the real time.
func (m *Manager) now() time.Time {
	if m.d.Now != nil {
		return m.d.Now()
	}
	return time.Now()
}

// cfg returns the goals config of the deps, or the defaults.
func (m *Manager) cfg() config.GoalsConfig {
	if m.d.Cfg != nil {
		return m.d.Cfg()
	}
	return config.Default().Goals
}

// logger returns the logger of the deps, or the default logger.
func (m *Manager) logger() *slog.Logger {
	if m.d.Log != nil {
		return m.d.Log
	}
	return slog.Default()
}

// remember puts the state of a row in the cache. A revision of 0 means no row. It keeps an
// entry with a higher revision, so a slow reader cannot put an old epoch over a new one.
func (m *Manager) remember(sessionID string, st State, rev int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rev == 0 {
		delete(m.cache, sessionID)
		return
	}
	if cur, ok := m.cache[sessionID]; ok && cur.rev > rev {
		return
	}
	m.cache[sessionID] = cacheEntry{rev: rev, epoch: st.Epoch, status: st.Status}
}

// forget removes the cache entry of a session.
func (m *Manager) forget(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cache, sessionID)
}

// Load returns the goal of a session. The bool is false when the session has no goal row.
func (m *Manager) Load(ctx context.Context, sessionID string) (State, bool, error) {
	st, rev, err := m.d.Store.Get(ctx, sessionID)
	if err != nil {
		return State{}, false, err
	}
	m.remember(sessionID, st, rev)
	return st, rev != 0, nil
}

// Update reads the goal of a session, calls f, and writes the result with a compare-and-set.
// After a conflict it calls f once more on the new row. An error from f writes nothing, and
// Update returns the state as f left it.
func (m *Manager) Update(ctx context.Context, sessionID string, f func(st *State, exists bool) error) (State, error) {
	for attempt := 0; ; attempt++ {
		st, rev, err := m.d.Store.Get(ctx, sessionID)
		if err != nil {
			return State{}, err
		}
		m.remember(sessionID, st, rev)
		if err := f(&st, rev != 0); err != nil {
			return st, err
		}
		newRev, err := m.d.Store.Put(ctx, sessionID, st, rev)
		if errors.Is(err, ErrConflict) && attempt == 0 {
			continue // another writer changed the row; run f again on the new row
		}
		if err != nil {
			return State{}, err
		}
		m.remember(sessionID, st, newRev)
		return st, nil
	}
}

// Delete removes the goal row of a session and its cache entry.
func (m *Manager) Delete(ctx context.Context, sessionID string) error {
	if err := m.d.Store.Delete(ctx, sessionID); err != nil {
		return err
	}
	m.forget(sessionID)
	return nil
}

// Active returns the active goals of all sessions and puts each one in the cache.
func (m *Manager) Active(ctx context.Context) ([]ActiveGoal, error) {
	act, err := m.d.Store.Active(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range act {
		m.remember(a.SessionID, a.State, a.Revision)
	}
	return act, nil
}

// IsCurrent reports if the goal of a session is active with the given epoch. It reads only the
// cache, because the gateway calls it for each queued continuation.
func (m *Manager) IsCurrent(sessionID string, epoch int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.cache[sessionID]
	return ok && e.status == StatusActive && e.epoch == epoch
}

// JudgeProvider returns the chain of goals.judge_provider when it is set. Else it returns the
// provider chain of the session.
func (m *Manager) JudgeProvider(sess *sessions.Session) provider.Provider {
	if entry := m.cfg().JudgeProvider; entry != "" && m.d.ChainFor != nil {
		return m.d.ChainFor(entry)
	}
	return m.d.ProviderFor(sess)
}

// Judge returns a judge with the timeout of the config. Its Record writes each call to the
// judge session of st. A failed record logs a warning and does not stop the step.
func (m *Manager) Judge(sess *sessions.Session, st *State) *Judge {
	timeout := time.Duration(m.cfg().JudgeTimeoutS) * time.Second
	if timeout <= 0 {
		timeout = defaultJudgeTimeout
	}
	j := &Judge{Timeout: timeout}
	if m.d.Sessions != nil {
		j.Record = func(ctx context.Context, prompt string, prov provider.Provider, out provider.Result) {
			if err := m.judgeLog.Record(ctx, sess, st, prompt, prov, out); err != nil {
				m.logger().Warn("goals: cannot record the judge call", "session", sess.ID, "err", err.Error())
			}
		}
	}
	return j
}

// WaitEnv returns the environment of the barrier checks for a key.
func (m *Manager) WaitEnv(key string) WaitEnv {
	env := WaitEnv{
		Alive:   m.d.Alive,
		Proc:    func(int) (tools.ProcInfo, bool) { return tools.ProcInfo{}, false },
		Batches: func() int { return 0 },
		Now:     m.now(),
	}
	if env.Alive == nil {
		env.Alive = PIDAlive
	}
	if m.d.Procs != nil {
		env.Proc = m.d.Procs.Get
	}
	if m.d.RunningBatches != nil {
		env.Batches = func() int { return m.d.RunningBatches(key) }
	}
	return env
}
