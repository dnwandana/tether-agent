package acp

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
)

// longTurn is a script whose first turn runs for 30 s after one progress item.
const longTurn = `{` + fakeModes + `,"resume":true,"turns":[
  {"steps":[{"tool_call":{"id":"t1","kind":"read","title":"read a.go"}},{"sleep_ms":30000}],"stop_reason":"end_turn"}]}`

// startBusy runs Delegate for sid in a goroutine and returns when its first progress item
// arrives. The channel gets the error of Delegate.
func startBusy(t *testing.T, m *Manager, agent, sid string) <-chan error {
	t.Helper()
	started := make(chan struct{}, 1)
	d := del(sid, t.TempDir())
	d.Agent = agent
	d.Hooks.Progress = func(string, string) {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	errc := make(chan error, 1)
	go func() {
		_, err := m.Delegate(context.Background(), d)
		errc <- err
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the turn did not start")
	}
	return errc
}

func lockFree(t *testing.T, dataDir, sid string) bool {
	t.Helper()
	rel, err := AcquireSessionLock(dataDir, sid, "claude-code")
	if err != nil {
		return false
	}
	_ = rel()
	return true
}

func TestBusyErrorText(t *testing.T) {
	want := "acp: 2 coding agent sessions are busy (acp.max_sessions). Wait for one to finish, or raise acp.max_sessions."
	if got := (&BusyError{Max: 2}).Error(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCapStopsOldestIdle(t *testing.T) {
	ctx := context.Background()
	agents, _ := fakeAgents(t, twoTurns(`,"resume":true`))
	m, st, dataDir := newTestManager(t, 2, agents, nil)
	for _, sid := range []string{"s1", "s2"} {
		if _, err := m.Delegate(ctx, del(sid, t.TempDir())); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond) // s1 has the oldest last use.
	}
	old, keep := liveClient(m, "s1"), liveClient(m, "s2")
	out, err := m.Delegate(ctx, del("s3", t.TempDir()))
	if err != nil || out.Session != "new" {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	waitFor(t, func() bool { return stopped(old) })
	if stopped(keep) {
		t.Fatal("the cap stopped the newer idle process")
	}
	if _, ok, _ := st.Get(ctx, "s1", "claude-code"); !ok {
		t.Fatal("the cap stop must keep the row")
	}
	if !lockFree(t, dataDir, "s1") {
		t.Fatal("the cap stop must release the lock")
	}
}

func TestCapAllBusy(t *testing.T) {
	agents, _ := fakeAgents(t, longTurn)
	m, _, _ := newTestManager(t, 1, agents, nil)
	_ = startBusy(t, m, "claude-code", "s1")
	_, err := m.Delegate(context.Background(), del("s2", t.TempDir()))
	var be *BusyError
	want := "acp: 1 coding agent sessions are busy (acp.max_sessions). Wait for one to finish, or raise acp.max_sessions."
	if !errors.As(err, &be) || be.Max != 1 || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestReapIdle(t *testing.T) {
	ctx := context.Background()
	agents, logPath := fakeAgents(t, twoTurns(`,"resume":true`))
	m, st, dataDir := newTestManager(t, 2, agents, nil)
	cwd := t.TempDir()
	if _, err := m.Delegate(ctx, del("s1", cwd)); err != nil {
		t.Fatal(err)
	}
	old := liveClient(m, "s1")
	m.ReapIdle() // Not idle for 300 s yet.
	if stopped(old) {
		t.Fatal("ReapIdle stopped a fresh process")
	}
	m.now = func() time.Time { return time.Now().Add(301 * time.Second) }
	m.ReapIdle()
	waitFor(t, func() bool { return stopped(old) })
	if _, ok, _ := st.Get(ctx, "s1", "claude-code"); !ok || !lockFree(t, dataDir, "s1") {
		t.Fatal("the reaper must keep the row and release the lock")
	}
	out, err := m.Delegate(ctx, del("s1", cwd))
	if err != nil || out.Session != "resumed" {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	if countMethod(t, logPath, "session/resume") != 1 {
		t.Fatal("no session/resume after the reap")
	}
}

func TestReapIdleSkipsBusy(t *testing.T) {
	agents, _ := fakeAgents(t, longTurn)
	m, _, _ := newTestManager(t, 2, agents, nil)
	_ = startBusy(t, m, "claude-code", "s1")
	m.now = func() time.Time { return time.Now().Add(time.Hour) }
	m.ReapIdle()
	if stopped(liveClient(m, "s1")) {
		t.Fatal("ReapIdle stopped a busy process")
	}
}

func TestBusySorted(t *testing.T) {
	a1, _ := fakeAgentConfig(t, scriptFromJSON(t, longTurn))
	a2, _ := fakeAgentConfig(t, scriptFromJSON(t, longTurn))
	a1.Enabled, a1.PermissionPreset = true, "auto"
	a2.Enabled, a2.PermissionPreset = true, "auto"
	m, _, _ := newTestManager(t, 2, map[string]config.ACPAgent{"codex": a2, "claude-code": a1}, nil)
	if got := m.Busy(); len(got) != 0 {
		t.Fatalf("Busy() = %q before a turn", got)
	}
	_ = startBusy(t, m, "codex", "s1")
	_ = startBusy(t, m, "claude-code", "s2")
	if got := m.Busy(); !reflect.DeepEqual(got, []string{"claude-code", "codex"}) {
		t.Fatalf("Busy() = %q", got)
	}
}

func TestStopAll(t *testing.T) {
	ctx := context.Background()
	agents, _ := fakeAgents(t, longTurn)
	m, st, _ := newTestManager(t, 2, agents, nil)
	errc := startBusy(t, m, "claude-code", "s1")
	busy := liveClient(m, "s1")
	// s2 becomes idle: its turn ends when its process stops, so use a short script.
	idleAgents, _ := fakeAgents(t, twoTurns(``))
	m.o.Config.Agents["idle"] = idleAgents["claude-code"]
	d := del("s2", t.TempDir())
	d.Agent = "idle"
	if _, err := m.Delegate(ctx, d); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	idle := m.entries[sessionKey{"s2", "idle"}].client
	m.mu.Unlock()
	begin := time.Now()
	m.StopAll()
	if el := time.Since(begin); el > 4*time.Second {
		t.Fatalf("StopAll took %v", el)
	}
	if !stopped(busy) || !stopped(idle) {
		t.Fatal("StopAll left a process alive")
	}
	if err := <-errc; !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("busy Delegate err = %v, want ErrSessionClosed", err)
	}
	for _, k := range []sessionKey{{"s1", "claude-code"}, {"s2", "idle"}} {
		if _, ok, _ := st.Get(ctx, k.SessionID, k.Agent); !ok {
			t.Fatalf("StopAll must keep the row of %v", k)
		}
	}
	if _, err := m.Delegate(ctx, del("s3", t.TempDir())); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("Delegate after StopAll: err = %v, want ErrSessionClosed", err)
	}
}

// TestCloseDuringSetup checks that a Close that runs while a process starts stops that
// process and deletes its row, so that nothing of the session stays after Close.
func TestCloseDuringSetup(t *testing.T) {
	ctx := context.Background()
	agents, _ := fakeAgents(t, twoTurns(`,"resume":true`))
	m, st, dataDir := newTestManager(t, 2, agents, nil)
	key := sessionKey{"s1", "claude-code"}
	release, err := AcquireSessionLock(dataDir, key.SessionID, key.Agent)
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.startClient(ctx, key, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &entry{key: key, client: c, cwd: t.TempDir(), release: release, info: SessionInfo{ID: "x"}}
	m.putRow(ctx, e, time.Time{})
	m.Close("s1")
	if m.settle(key, e) {
		t.Fatal("settle added an entry of a closed session")
	}
	if !stopped(c) || liveClient(m, "s1") != nil {
		t.Fatal("the process of a closed setup must stop and stay out of the map")
	}
	waitFor(t, func() bool {
		_, ok, _ := st.Get(ctx, "s1", "claude-code")
		return !ok
	})
	if !lockFree(t, dataDir, "s1") {
		t.Fatal("a closed setup must release the lock")
	}
	m.mu.Lock()
	n := len(m.starting)
	m.mu.Unlock()
	if n != 0 {
		t.Fatalf("starting = %d after settle, want 0", n)
	}
}
