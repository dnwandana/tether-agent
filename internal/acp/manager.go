package acp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/security"
)

const (
	// closeSessionWait is the limit of the session/close call when a session closes for good.
	closeSessionWait = 5 * time.Second
	// noteContextLost is the note when a call cannot use the earlier ACP session.
	noteContextLost = "the earlier context of the coding agent is lost"
)

// Delegation is one coding task for the ACP session of (SessionID, Agent).
type Delegation struct {
	SessionID, Agent, CWD, Model, Preset, Task string
	Hooks                                      Hooks
}

// Outcome is the result of one coding task. Session is new, reused, resumed, or loaded.
type Outcome struct {
	Session    string
	StopReason string
	Text       string
	ToolCalls  int
	Notes      []string
}

// ManagerOptions tells NewManager how to start and record the adapter processes.
type ManagerOptions struct {
	Config        config.ACPConfig
	DataDir       string
	DotEnv        map[string]string
	Store         *Store
	ClientVersion string
	Log           *slog.Logger
}

// sessionKey identifies one adapter process: a Tether session and an agent id.
type sessionKey struct{ SessionID, Agent string }

// entry is one live adapter process and its ACP session. Change broken and the whole info
// only under Manager.mu, because closeForGood reads them from another goroutine.
type entry struct {
	key     sessionKey
	client  *Client
	info    SessionInfo
	cwd     string
	release func() error // frees the lock file; a second call does nothing
	broken  bool         // the setup of the ACP session failed; the next call needs a new session
}

const (
	// defaultMaxSessions replaces an acp.max_sessions value below 1.
	defaultMaxSessions = 2
	// defaultIdleTimeout replaces an acp.idle_timeout_s value below the minimum.
	defaultIdleTimeout = 300 * time.Second
	// minIdleTimeoutS is the smallest acp.idle_timeout_s value that the manager uses.
	minIdleTimeoutS = 60
)

// BusyError is the error when each live adapter process is busy and the cap is full.
type BusyError struct{ Max int }

func (e *BusyError) Error() string {
	return fmt.Sprintf("acp: %d coding agent sessions are busy (acp.max_sessions). Wait for one to finish, or raise acp.max_sessions.", e.Max)
}

// Manager holds the live adapter processes of this tether process, one for each
// (Tether SessionID, agent id).
type Manager struct {
	o       ManagerOptions
	log     *slog.Logger
	now     func() time.Time // a test changes it to move the clock of ReapIdle
	mu      sync.Mutex
	entries map[sessionKey]*entry
	keys    map[sessionKey]*sync.Mutex // one setup and one turn at a time for each key
	bg      sync.WaitGroup             // the Close goroutines
	// starting holds each key whose process startClient started and whose entry is not in
	// entries yet. The value is true when Close ran for the session during the setup.
	starting map[sessionKey]bool
	closed   bool // StopAll ran; no new process starts
}

// NewManager returns a manager with no live process. It starts no process.
func NewManager(o ManagerOptions) *Manager {
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		o:        o,
		log:      log,
		now:      time.Now,
		entries:  map[sessionKey]*entry{},
		keys:     map[sessionKey]*sync.Mutex{},
		starting: map[sessionKey]bool{},
	}
}

// maxSessions returns the cap of live adapter processes.
func (m *Manager) maxSessions() int {
	if n := m.o.Config.MaxSessions; n >= 1 {
		return n
	}
	return defaultMaxSessions
}

// idleTimeout returns the idle time after which ReapIdle stops a process.
func (m *Manager) idleTimeout() time.Duration {
	if s := m.o.Config.IdleTimeoutS; s >= minIdleTimeoutS {
		return time.Duration(s) * time.Second
	}
	return defaultIdleTimeout
}

// takeIdle takes e out of the map when its client is idle and no call holds its key lock.
// It returns the locked key mutex, or nil when e stays. Call it with m.mu held. The caller
// must unlock the mutex after the stop, so that a call on the key waits for the stop and
// does not find the lock file still held.
func (m *Manager) takeIdle(e *entry) *sync.Mutex {
	if e.client.Busy() {
		return nil
	}
	l := m.keys[e.key]
	if l == nil || !l.TryLock() {
		// A call on the key runs its setup or is about to send its prompt.
		return nil
	}
	delete(m.entries, e.key)
	return l
}

// dropTaken stops each taken entry in parallel, keeps the rows, and unlocks the key mutexes.
func (m *Manager) dropTaken(es []*entry, locks []*sync.Mutex) {
	var wg sync.WaitGroup
	for i, e := range es {
		wg.Go(func() {
			defer locks[i].Unlock()
			m.drop(e)
		})
	}
	wg.Wait()
}

// keyLock returns the mutex of key. The map keeps each mutex, so that two calls on one key
// never use two different mutexes.
func (m *Manager) keyLock(key sessionKey) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.keys[key]
	if l == nil {
		l = &sync.Mutex{}
		m.keys[key] = l
	}
	return l
}

// redactor returns a redactor that masks the .env values and the env values of agent a.
func (m *Manager) redactor(a config.ACPAgent) *security.Redactor {
	return security.NewRedactor(SecretValues(m.o.DotEnv, a))
}

// startClient starts the adapter of key.Agent. The working folder of the process is cwd, the
// folder of its first ACP session.
//
// When the cap is full, startClient first stops the idle process with the oldest last use.
// When each live process is busy, it returns a *BusyError. On success, the caller must call
// settle once for key when the setup ends.
func (m *Manager) startClient(ctx context.Context, key sessionKey, cwd string) (*Client, error) {
	limit := m.maxSessions()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrSessionClosed
	}
	var (
		victim     *entry
		victimLock *sync.Mutex
	)
	if len(m.entries)+len(m.starting) >= limit {
		var cands []*entry
		for _, e := range m.entries {
			cands = append(cands, e)
		}
		slices.SortFunc(cands, func(a, b *entry) int {
			return a.client.LastUse().Compare(b.client.LastUse())
		})
		for _, e := range cands {
			if victimLock = m.takeIdle(e); victimLock != nil {
				victim = e
				break
			}
		}
		if victim == nil {
			m.mu.Unlock()
			return nil, &BusyError{Max: limit}
		}
	}
	// Count the process from now on, so that a call on another key cannot pass the cap.
	m.starting[key] = false
	m.mu.Unlock()
	if victim != nil {
		m.drop(victim)
		victimLock.Unlock()
	}

	c, err := Start(ctx, StartOptions{
		AgentID:       key.Agent,
		Agent:         m.o.Config.Agents[key.Agent],
		DotEnv:        m.o.DotEnv,
		Dir:           cwd,
		ClientVersion: m.o.ClientVersion,
		Log:           m.log,
	})
	if err != nil {
		m.settle(key, nil)
		return nil, err
	}
	return c, nil
}

// settle ends the setup of key that startClient started. A nil e means that the setup failed.
// Else settle puts e in the map of live entries and returns true. When Close or StopAll ran
// during the setup, settle does not put e in the map. It closes e for good after a Close and
// stops e after a StopAll, and it returns false.
func (m *Manager) settle(key sessionKey, e *entry) bool {
	m.mu.Lock()
	closedBySession := m.starting[key]
	delete(m.starting, key)
	shutdown := m.closed
	if e != nil && !closedBySession && !shutdown {
		m.entries[key] = e
		m.mu.Unlock()
		return true
	}
	m.mu.Unlock()
	switch {
	case e == nil:
	case closedBySession:
		m.closeForGood(e)
	default:
		m.drop(e)
	}
	return false
}

// setupErr returns ErrSessionClosed when Close or StopAll ran during the failed setup of key,
// else err. Call it before settle.
func (m *Manager) setupErr(key sessionKey, err error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.starting[key] || m.closed {
		return ErrSessionClosed
	}
	return err
}

// remove takes e out of the map when the map still holds e.
func (m *Manager) remove(e *entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries[e.key] == e {
		delete(m.entries, e.key)
	}
}

// drop stops the process of e and frees its lock. It keeps the row, so that the next call
// can resume the ACP session. It is safe to call more than once.
func (m *Manager) drop(e *entry) {
	m.remove(e)
	_ = e.client.Stop()
	m.releaseLock(e)
}

// closeForGood closes the ACP session of e, stops its process, deletes its row, and frees
// its lock. A busy client gets Abort, so that its running turn ends with ErrSessionClosed.
func (m *Manager) closeForGood(e *entry) {
	m.remove(e)
	m.mu.Lock()
	broken, id := e.broken, e.info.ID
	m.mu.Unlock()
	c := e.client
	if !c.Busy() && !broken && c.Capabilities().Close && !clientStopped(c) {
		ctx, cancel := context.WithTimeout(context.Background(), closeSessionWait)
		if err := c.CloseSession(ctx, id); err != nil {
			m.log.Debug("acp: session/close failed", "agent", e.key.Agent, "err", err)
		}
		cancel()
	}
	// Abort also stops an idle client. It marks the client, so that a Prompt call that
	// starts after this point returns ErrSessionClosed.
	c.Abort()
	m.deleteRow(context.Background(), e.key)
	m.releaseLock(e)
}

// releaseLock frees the lock file of e and logs an error.
func (m *Manager) releaseLock(e *entry) {
	if err := e.release(); err != nil {
		m.log.Warn("acp: cannot free the session lock", "agent", e.key.Agent, "err", err)
	}
}

// deleteRow deletes the row of key and logs an error. The delete runs also when ctx ended.
func (m *Manager) deleteRow(ctx context.Context, key sessionKey) {
	if err := m.o.Store.Delete(context.WithoutCancel(ctx), key.SessionID, key.Agent); err != nil {
		m.log.Warn("acp: cannot delete the session row", "agent", key.Agent, "err", err)
	}
}

// putRow writes the row of e and logs an error. The write runs also when ctx ended.
func (m *Manager) putRow(ctx context.Context, e *entry, created time.Time) {
	now := time.Now()
	if created.IsZero() {
		created = now
	}
	err := m.o.Store.Put(context.WithoutCancel(ctx), Record{
		SessionID: e.key.SessionID, Agent: e.key.Agent, ACPSessionID: e.info.ID, CWD: e.cwd,
		CreatedAt: created, UpdatedAt: now,
	})
	if err != nil {
		m.log.Warn("acp: cannot save the session row", "agent", e.key.Agent, "err", err)
	}
}

// clientStopped reports whether the process or the connection of c stopped. When only the
// connection stopped, it waits for the stop to end, so that the exit status and the last
// stderr lines are complete.
func clientStopped(c *Client) bool {
	select {
	case <-c.Done():
		return true
	case <-c.conn.Done():
		waitClosed(c.Done(), stoppedWait)
		return true
	default:
		return false
	}
}

// Delegate runs one coding task in the ACP session of (d.SessionID, d.Agent). It starts,
// resumes, loads, or creates the session first.
func (m *Manager) Delegate(ctx context.Context, d Delegation) (Outcome, error) {
	a, ok := m.o.Config.Agents[d.Agent]
	if !ok || !a.Enabled {
		//lint:ignore ST1005 the text is a fixed user message with full sentences.
		return Outcome{}, fmt.Errorf("acp: agent %q is unknown or disabled.", d.Agent)
	}
	preset := d.Preset
	if preset == "" {
		preset = a.PermissionPreset
	}
	mode, ok := a.ModeFor(preset)
	if !ok {
		//lint:ignore ST1005 the text is a fixed user message with full sentences.
		return Outcome{}, fmt.Errorf("acp: agent %s has no mode for permission_preset %q. Add it to acp.agents.%s.modes.",
			d.Agent, preset, d.Agent)
	}
	model := d.Model
	if model == "" {
		model = a.Model
	}

	key := sessionKey{SessionID: d.SessionID, Agent: d.Agent}
	kl := m.keyLock(key)
	kl.Lock()
	defer kl.Unlock()
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return Outcome{}, ErrSessionClosed
	}

	e, kind, notes, err := m.session(ctx, key, a, d.CWD)
	if err != nil {
		return Outcome{}, err
	}
	note, err := e.client.Configure(ctx, &e.info, model, mode)
	if err != nil {
		return Outcome{}, m.configureErr(ctx, e, err)
	}
	if note != "" {
		notes = append(notes, note)
	}

	res, err := e.client.Prompt(ctx, e.info.ID, d.Task, d.Hooks)
	if err != nil {
		return Outcome{}, m.promptErr(ctx, e, err)
	}
	return Outcome{Session: kind, StopReason: res.StopReason, Text: res.Text, ToolCalls: res.ToolCalls, Notes: notes}, nil
}

// configureErr handles the error of a Configure call on e and returns the error for the
// model. Only a mode that is not stricter makes the ACP session unusable. Another failed
// call did not change the ACP session, so the entry and the row stay.
func (m *Manager) configureErr(ctx context.Context, e *entry, err error) error {
	switch {
	case e.client.aborted.Load():
		// Close or a stop at shutdown ended the call. They handle e and the row.
		return ErrSessionClosed
	case ctx.Err() != nil:
		// /stop or the turn timeout ended the call. The next call can use the session.
		return err
	case e.client.fatalErr(err):
		if _, ok := errors.AsType[*NotLoggedInError](err); ok {
			return &NotLoggedInError{Agent: e.key.Agent}
		}
		// A crash keeps the row, so that the next call can resume the ACP session.
		m.drop(e)
		red := m.redactor(m.o.Config.Agents[e.key.Agent])
		return errors.New(red.Redact(e.client.stoppedErr().Error()))
	}
	if _, ok := errors.AsType[*modeNotStricterError](err); ok {
		// The ACP session can run with a looser mode than the user allowed. Do not use it
		// again: the next call starts a new one.
		m.mu.Lock()
		e.broken = true
		m.mu.Unlock()
		m.deleteRow(ctx, e.key)
	}
	return err
}

// promptErr handles the error of a Prompt call on e and returns the error for the model.
func (m *Manager) promptErr(ctx context.Context, e *entry, err error) error {
	red := m.redactor(m.o.Config.Agents[e.key.Agent])
	var nl *NotLoggedInError
	switch {
	case errors.Is(err, ErrSessionClosed):
		// Close or a stop at shutdown ended the turn. They handle e.
		return err
	case errors.As(err, &nl):
		return &NotLoggedInError{Agent: e.key.Agent}
	case clientStopped(e.client) && ctx.Err() != nil:
		// The agent did not end the cancelled turn in time, and Prompt stopped it. The ACP
		// session can be in any state, so the next call starts a new one.
		m.deleteRow(ctx, e.key)
		m.drop(e)
		return errors.New(red.Redact(err.Error()))
	case clientStopped(e.client):
		// A crash keeps the row, so that the next call can resume the ACP session.
		m.drop(e)
		return errors.New(red.Redact(e.client.stoppedErr().Error()))
	default:
		return errors.New(red.Redact(err.Error()))
	}
}

// session returns the live entry for key with a usable ACP session.
func (m *Manager) session(ctx context.Context, key sessionKey, a config.ACPAgent, cwd string) (e *entry, kind string, notes []string, err error) {
	m.mu.Lock()
	e = m.entries[key]
	m.mu.Unlock()
	if e != nil && clientStopped(e.client) {
		m.drop(e)
		e = nil
	}
	if e != nil {
		if e.cwd != cwd {
			m.closeForGood(e)
		} else {
			m.mu.Lock()
			broken := e.broken
			m.mu.Unlock()
			if !broken {
				return e, "reused", nil, nil
			}
			info, err := e.client.NewSession(ctx, cwd)
			if err != nil {
				return nil, "", nil, err
			}
			m.mu.Lock()
			e.info, e.broken = info, false
			m.mu.Unlock()
			m.putRow(ctx, e, time.Time{})
			return e, "new", []string{noteContextLost}, nil
		}
	}

	release, err := AcquireSessionLock(m.o.DataDir, key.SessionID, key.Agent)
	if err != nil {
		return nil, "", nil, err
	}
	release = sync.OnceValue(release)
	rec, found, err := m.o.Store.Get(ctx, key.SessionID, key.Agent)
	if err != nil {
		m.log.Warn("acp: cannot read the session row; a new session starts", "agent", key.Agent, "err", err)
		found = false
	}

	c, err := m.startClient(ctx, key, cwd)
	if err != nil {
		_ = release()
		return nil, "", nil, m.startErr(key.Agent, a, err)
	}
	e = &entry{key: key, client: c, cwd: cwd, release: release}

	if found && rec.CWD == cwd {
		caps := c.Capabilities()
		var (
			info     SessionInfo
			restored string
			rerr     error
		)
		switch {
		case caps.Resume:
			restored = "resumed"
			info, rerr = c.ResumeSession(ctx, rec.ACPSessionID, cwd)
		case caps.LoadSession:
			restored = "loaded"
			info, rerr = c.LoadSession(ctx, rec.ACPSessionID, cwd)
		}
		if restored != "" && rerr == nil {
			e.info = info
			m.putRow(ctx, e, rec.CreatedAt)
			if !m.settle(key, e) {
				return nil, "", nil, ErrSessionClosed
			}
			return e, restored, nil, nil
		}
		if rerr != nil && (c.fatalErr(rerr) || ctx.Err() != nil) {
			// A login error, a stopped process, or a cancel does not show that the stored
			// session is bad. Keep the row, so that a later call can resume it.
			err := m.setupErr(key, rerr)
			_ = c.Stop()
			_ = release()
			m.settle(key, nil)
			return nil, "", nil, err
		}
		if rerr != nil {
			m.log.Debug("acp: cannot open the stored session; a new session starts",
				"agent", key.Agent, "err", rerr)
		}
		m.deleteRow(ctx, key)
		notes = append(notes, noteContextLost)
	}

	info, err := c.NewSession(ctx, cwd)
	if err != nil {
		err = m.setupErr(key, err)
		_ = c.Stop()
		_ = release()
		m.settle(key, nil)
		return nil, "", nil, err
	}
	e.info = info
	m.putRow(ctx, e, time.Time{})
	if !m.settle(key, e) {
		return nil, "", nil, ErrSessionClosed
	}
	return e, "new", notes, nil
}

// startErr returns the error for a failed start of agent. The errors of Start already have
// the user text, so they stay as they are.
func (m *Manager) startErr(agent string, a config.ACPAgent, err error) error {
	var nl *NotLoggedInError
	if errors.As(err, &nl) || strings.HasPrefix(err.Error(), "acp: ") {
		return err
	}
	//lint:ignore ST1005 the text is a fixed user message with full sentences.
	return fmt.Errorf("acp: cannot start %s: %s. Run tether doctor.", agent, m.redactor(a).Redact(err.Error()))
}

// Close closes each ACP session of the Tether session sessionID for good. It returns at once.
func (m *Manager) Close(sessionID string) {
	m.mu.Lock()
	var es []*entry
	for k, e := range m.entries {
		if k.SessionID == sessionID {
			es = append(es, e)
			delete(m.entries, k)
		}
	}
	// A setup that runs now closes its session for good when it ends. See settle.
	for k := range m.starting {
		if k.SessionID == sessionID {
			m.starting[k] = true
		}
	}
	// StopAll already waited for m.bg. A new Add after that wait is not safe, so a Close
	// after StopAll runs without m.bg.
	track := !m.closed
	if track {
		m.bg.Add(1)
	}
	m.mu.Unlock()
	go func() {
		if track {
			defer m.bg.Done()
		}
		for _, e := range es {
			m.closeForGood(e)
		}
		// A row with no live process goes too.
		for id := range m.o.Config.Agents {
			m.deleteRow(context.Background(), sessionKey{SessionID: sessionID, Agent: id})
		}
	}()
}

// ReapIdle stops each process that is idle for acp.idle_timeout_s. It keeps the rows, so the
// next call resumes the session. It skips busy processes. The app calls it each minute.
func (m *Manager) ReapIdle() {
	limit := m.idleTimeout()
	m.mu.Lock()
	now := m.now()
	var (
		es    []*entry
		locks []*sync.Mutex
	)
	for _, e := range m.entries {
		if now.Sub(e.client.LastUse()) < limit {
			continue
		}
		if l := m.takeIdle(e); l != nil {
			es, locks = append(es, e), append(locks, l)
		}
	}
	m.mu.Unlock()
	m.dropTaken(es, locks)
}

// StopAll stops each process at shutdown. It aborts the busy ones and keeps the rows. After
// StopAll, Delegate returns ErrSessionClosed.
func (m *Manager) StopAll() {
	m.mu.Lock()
	m.closed = true
	es := make([]*entry, 0, len(m.entries))
	for k, e := range m.entries {
		es = append(es, e)
		delete(m.entries, k)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, e := range es {
		wg.Go(func() {
			// Abort also stops an idle client. It marks the client, so that a call that
			// is about to send its prompt gets ErrSessionClosed.
			e.client.Abort()
			m.releaseLock(e)
		})
	}
	wg.Wait()
	m.bg.Wait()
}

// Busy returns the sorted agent ids of the busy processes, one id for each busy process.
func (m *Manager) Busy() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for _, e := range m.entries {
		if e.client.Busy() {
			ids = append(ids, e.key.Agent)
		}
	}
	sort.Strings(ids)
	return ids
}
