package acp

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
)

const fakeModes = `"mode_config":true,"modes":["default","acceptEdits","auto","bypassPermissions"]`

// twoTurns is a script with two short turns. extra adds keys, for example `,"resume":true`.
func twoTurns(extra string) string {
	return `{` + fakeModes + extra + `,"turns":[
	  {"steps":[{"message":"one"}],"stop_reason":"end_turn"},
	  {"steps":[{"message":"two"}],"stop_reason":"end_turn"}]}`
}

// fakeAgents returns the agent map with one enabled fake agent "claude-code".
func fakeAgents(t *testing.T, js string) (map[string]config.ACPAgent, string) {
	t.Helper()
	a, logPath := fakeAgentConfig(t, scriptFromJSON(t, js))
	a.Enabled, a.PermissionPreset = true, "auto"
	return map[string]config.ACPAgent{"claude-code": a}, logPath
}

func newTestManager(t *testing.T, max int, agents map[string]config.ACPAgent, dotenv map[string]string) (*Manager, *Store, string) {
	t.Helper()
	dataDir := t.TempDir()
	d, err := db.Open(filepath.Join(dataDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	st := NewStore(d)
	m := NewManager(ManagerOptions{
		Config: config.ACPConfig{Enabled: true, IdleTimeoutS: 300, MaxSessions: max, TurnTimeoutS: 1800,
			Agents: agents},
		DataDir: dataDir, DotEnv: dotenv, Store: st, ClientVersion: "test", Log: slog.New(slog.DiscardHandler),
	})
	t.Cleanup(func() {
		m.mu.Lock()
		var es []*entry
		for _, e := range m.entries {
			es = append(es, e)
		}
		m.mu.Unlock()
		for _, e := range es {
			e.client.Abort()
			_ = e.release()
		}
		m.bg.Wait()
		_ = d.Close()
	})
	return m, st, dataDir
}

func del(sid, cwd string) Delegation {
	return Delegation{SessionID: sid, Agent: "claude-code", CWD: cwd, Preset: "auto", Task: "fix it"}
}

func countMethod(t *testing.T, logPath, method string) int {
	t.Helper()
	n := 0
	for _, e := range readFakeLog(t, logPath) {
		if e.Method == method {
			n++
		}
	}
	return n
}

func liveClient(m *Manager, sid string) *Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.entries[sessionKey{sid, "claude-code"}]; e != nil {
		return e.client
	}
	return nil
}

func stopped(c *Client) bool {
	select {
	case <-c.Done():
		return true
	default:
		return false
	}
}

func TestDelegateNewThenReuse(t *testing.T) {
	ctx := context.Background()
	agents, logPath := fakeAgents(t, twoTurns(`,"resume":true,"close":true`))
	m, st, _ := newTestManager(t, 2, agents, nil)
	cwd := t.TempDir()
	out, err := m.Delegate(ctx, del("s1", cwd))
	if err != nil || out.Session != "new" || out.Text != "one" || out.StopReason != "end_turn" || len(out.Notes) != 0 {
		t.Fatalf("first = %+v, err = %v", out, err)
	}
	rec, ok, err := st.Get(ctx, "s1", "claude-code")
	if err != nil || !ok || rec.CWD != cwd || rec.ACPSessionID == "" {
		t.Fatalf("row = %+v, %v, %v", rec, ok, err)
	}
	out, err = m.Delegate(ctx, del("s1", cwd))
	if err != nil || out.Session != "reused" || out.Text != "two" {
		t.Fatalf("second = %+v, err = %v", out, err)
	}
	if n := countMethod(t, logPath, "session/new"); n != 1 {
		t.Fatalf("session/new count = %d, want 1", n)
	}
	// The mode is set before each prompt.
	if p, s := countMethod(t, logPath, "session/prompt"), countMethod(t, logPath, "session/set_config_option"); p != 2 || s < 2 {
		t.Fatalf("prompts = %d, mode sets = %d", p, s)
	}
}

func TestDelegateRestoresStoredSession(t *testing.T) {
	cases := []struct{ name, extra, want, method, note string }{
		{"resume", `,"resume":true,"load_session":true`, "resumed", "session/resume", ""},
		{"load", `,"load_session":true,"replay":[{"tool_call":{"id":"r1","kind":"read","title":"old read"}},{"message":"old"}]`,
			"loaded", "session/load", ""},
		{"none", ``, "new", "session/new", "the earlier context of the coding agent is lost"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			agents, logPath := fakeAgents(t, twoTurns(c.extra))
			m, _, _ := newTestManager(t, 2, agents, nil)
			cwd := t.TempDir()
			if _, err := m.Delegate(ctx, del("s1", cwd)); err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			e := m.entries[sessionKey{"s1", "claude-code"}]
			m.mu.Unlock()
			m.drop(e) // An idle stop keeps the row.
			progress := 0
			d := del("s1", cwd)
			d.Hooks.Progress = func(string, string) { progress++ }
			out, err := m.Delegate(ctx, d)
			if err != nil || out.Session != c.want || out.Text != "one" || progress != 0 {
				t.Fatalf("out = %+v, progress = %d, err = %v", out, progress, err)
			}
			if c.note != "" && (len(out.Notes) != 1 || out.Notes[0] != c.note) {
				t.Fatalf("notes = %q", out.Notes)
			}
			if countMethod(t, logPath, c.method) < 1 {
				t.Fatalf("no %s in the fake log", c.method)
			}
		})
	}
}

func TestDelegateOtherCWDClosesOld(t *testing.T) {
	ctx := context.Background()
	agents, logPath := fakeAgents(t, twoTurns(`,"resume":true,"close":true`))
	m, st, _ := newTestManager(t, 2, agents, nil)
	cwd1, cwd2 := t.TempDir(), t.TempDir()
	if _, err := m.Delegate(ctx, del("s1", cwd1)); err != nil {
		t.Fatal(err)
	}
	old := liveClient(m, "s1")
	out, err := m.Delegate(ctx, del("s1", cwd2))
	if err != nil || out.Session != "new" {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	waitFor(t, func() bool { return stopped(old) })
	if countMethod(t, logPath, "session/close") != 1 {
		t.Fatal("the old session was not closed")
	}
	if rec, ok, _ := st.Get(ctx, "s1", "claude-code"); !ok || rec.CWD != cwd2 {
		t.Fatalf("row = %+v", rec)
	}
}

func TestDelegateLockedByOtherProcess(t *testing.T) {
	agents, _ := fakeAgents(t, twoTurns(``))
	m, _, dataDir := newTestManager(t, 2, agents, nil)
	release, err := AcquireSessionLock(dataDir, "s1", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = m.Delegate(context.Background(), del("s1", t.TempDir()))
	if !errors.Is(err, ErrSessionLocked) {
		t.Fatalf("err = %v, want ErrSessionLocked", err)
	}
	if liveClient(m, "s1") != nil {
		t.Fatal("a locked key must not start a process")
	}
}

// failedConfigure is the state after one good call and one call that fails in Configure.
type failedConfigure struct {
	m            *Manager
	st           *Store
	logPath, cwd string
	acpID        string // the ACP session id of the good call
	err          error  // the error of the failed call
}

// configureFails runs one good call, then a call that edit changes and that fails in
// Configure. The failed call uses ctx.
func configureFails(t *testing.T, ctx context.Context, js string, edit func(*Delegation)) failedConfigure {
	t.Helper()
	agents, logPath := fakeAgents(t, js)
	m, st, _ := newTestManager(t, 2, agents, nil)
	cwd := t.TempDir()
	if _, err := m.Delegate(context.Background(), del("s1", cwd)); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := st.Get(context.Background(), "s1", "claude-code")
	if err != nil || !ok {
		t.Fatalf("row = %+v, %v, %v", rec, ok, err)
	}
	d := del("s1", cwd)
	edit(&d)
	_, err = m.Delegate(ctx, d)
	return failedConfigure{m: m, st: st, logPath: logPath, cwd: cwd, acpID: rec.ACPSessionID, err: err}
}

// checkSessionKept checks that the failed call kept the row and the live entry, and that
// the next call reuses the ACP session.
func checkSessionKept(t *testing.T, f failedConfigure) {
	t.Helper()
	ctx := context.Background()
	if rec, ok, _ := f.st.Get(ctx, "s1", "claude-code"); !ok || rec.ACPSessionID != f.acpID {
		t.Fatalf("row = %+v, %v; the failed call must keep the row", rec, ok)
	}
	if liveClient(f.m, "s1") == nil {
		t.Fatal("the failed call must keep the live entry")
	}
	out, err := f.m.Delegate(ctx, del("s1", f.cwd))
	if err != nil || out.Session != "reused" || out.Text != "two" || len(out.Notes) != 0 {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	if n := countMethod(t, f.logPath, "session/new"); n != 1 {
		t.Fatalf("session/new count = %d, want 1", n)
	}
}

func TestDelegateModeErrorKeepsSession(t *testing.T) {
	f := configureFails(t, context.Background(),
		`{"mode_config":true,"modes":["default","acceptEdits","auto"],"resume":true,"turns":[
		  {"steps":[{"message":"one"}],"stop_reason":"end_turn"},
		  {"steps":[{"message":"two"}],"stop_reason":"end_turn"}]}`,
		func(d *Delegation) { d.Preset = "folder" }) // bypassPermissions is not offered.
	if me, ok := errors.AsType[*ModeError](f.err); !ok || me.Mode != "bypassPermissions" {
		t.Fatalf("err = %v, want a ModeError for bypassPermissions", f.err)
	}
	checkSessionKept(t, f)
}

func TestDelegateRejectedModelKeepsSession(t *testing.T) {
	f := configureFails(t, context.Background(),
		`{`+fakeModes+`,"models":["opus","sonnet"],"turns":[
		  {"steps":[{"message":"one"}],"stop_reason":"end_turn"},
		  {"steps":[{"message":"two"}],"stop_reason":"end_turn"}]}`,
		func(d *Delegation) { d.Model = "gpt-x" })
	want := `acp: agent claude-code does not accept model "gpt-x" (offered: opus, sonnet).`
	if f.err == nil || f.err.Error() != want {
		t.Fatalf("err = %v, want %q", f.err, want)
	}
	checkSessionKept(t, f)
}

func TestDelegateConfigureCancelKeepsSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := configureFails(t, ctx, twoTurns(``), func(*Delegation) {})
	if f.err == nil {
		t.Fatal("a call with an ended context must fail")
	}
	checkSessionKept(t, f)
}

func TestDelegateModeNotStricterDeletesRow(t *testing.T) {
	ctx := context.Background()
	f := configureFails(t, ctx,
		`{`+fakeModes+`,"effective_mode":{"acceptEdits":"bypassPermissions"},"turns":[
		  {"steps":[{"message":"one"}],"stop_reason":"end_turn"},
		  {"steps":[{"message":"two"}],"stop_reason":"end_turn"}]}`,
		func(d *Delegation) { d.Preset = "risky" })
	want := `acp: agent claude-code reports mode "bypassPermissions" after the change to "acceptEdits". That mode is not stricter, so Tether Agent does not send the task.`
	if _, ok := errors.AsType[*modeNotStricterError](f.err); !ok || f.err.Error() != want {
		t.Fatalf("err = %v, want %q", f.err, want)
	}
	if _, ok, _ := f.st.Get(ctx, "s1", "claude-code"); ok {
		t.Fatal("a mode that is not stricter must delete the row")
	}
	out, err := f.m.Delegate(ctx, del("s1", f.cwd))
	if err != nil || out.Session != "new" || out.Text != "two" ||
		len(out.Notes) != 1 || out.Notes[0] != "the earlier context of the coding agent is lost" {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	if n := countMethod(t, f.logPath, "session/new"); n != 2 {
		t.Fatalf("session/new count = %d, want 2", n)
	}
}

func TestDelegateResumeLoginErrorKeepsRow(t *testing.T) {
	ctx := context.Background()
	agents, logPath := fakeAgents(t, twoTurns(`,"resume":true,"auth_required":true`))
	m, st, _ := newTestManager(t, 2, agents, nil)
	cwd := t.TempDir()
	rec := Record{SessionID: "s1", Agent: "claude-code", ACPSessionID: "old", CWD: cwd}
	if err := st.Put(ctx, rec); err != nil {
		t.Fatal(err)
	}
	var nl *NotLoggedInError
	if _, err := m.Delegate(ctx, del("s1", cwd)); !errors.As(err, &nl) {
		t.Fatalf("err = %v, want a NotLoggedInError", err)
	}
	if got, ok, _ := st.Get(ctx, "s1", "claude-code"); !ok || got.ACPSessionID != "old" {
		t.Fatalf("row = %+v, %v; a login error must keep the stored session", got, ok)
	}
	if n := countMethod(t, logPath, "session/new"); n != 0 {
		t.Fatalf("session/new count = %d, want 0", n)
	}
	if liveClient(m, "s1") != nil {
		t.Fatal("a failed resume must not keep a process")
	}
}

func TestDelegateNoModeForPreset(t *testing.T) {
	agents, _ := fakeAgents(t, twoTurns(``))
	a := agents["claude-code"]
	a.Modes = map[string]string{"auto": "auto"}
	agents["claude-code"] = a
	m, _, _ := newTestManager(t, 2, agents, nil)
	d := del("s1", t.TempDir())
	d.Preset = "risky"
	_, err := m.Delegate(context.Background(), d)
	want := `acp: agent claude-code has no mode for permission_preset "risky". Add it to acp.agents.claude-code.modes.`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if liveClient(m, "s1") != nil {
		t.Fatal("no process must start")
	}
}

func TestDelegateErrorTexts(t *testing.T) {
	ctx := context.Background()
	t.Run("not logged in", func(t *testing.T) {
		agents, _ := fakeAgents(t, `{`+fakeModes+`,"auth_required":true}`)
		m, st, _ := newTestManager(t, 2, agents, nil)
		_, err := m.Delegate(ctx, del("s1", t.TempDir()))
		var nl *NotLoggedInError
		want := `acp: claude-code is not logged in. Run "claude" on the host once and sign in, then send the task again.`
		if !errors.As(err, &nl) || err.Error() != want {
			t.Fatalf("err = %v", err)
		}
		if _, ok, _ := st.Get(ctx, "s1", "claude-code"); ok {
			t.Fatal("no row must exist")
		}
	})
	t.Run("cannot start", func(t *testing.T) {
		agents, _ := fakeAgents(t, twoTurns(``))
		a := agents["claude-code"]
		a.Command = []string{filepath.Join(t.TempDir(), "no-such-agent")}
		agents["claude-code"] = a
		m, _, _ := newTestManager(t, 2, agents, nil)
		_, err := m.Delegate(ctx, del("s1", t.TempDir()))
		if err == nil || !strings.HasPrefix(err.Error(), "acp: cannot start claude-code: ") ||
			!strings.HasSuffix(err.Error(), ". Run tether doctor.") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("crash", func(t *testing.T) {
		agents, _ := fakeAgents(t, `{`+fakeModes+`,"crash_on_prompt":true,"stderr":"boom token-value-12345678",
		  "turns":[{"steps":[{"message":"one"}],"stop_reason":"end_turn"}]}`)
		m, st, _ := newTestManager(t, 2, agents, map[string]string{"OPENAI_API_KEY": "token-value-12345678"})
		_, err := m.Delegate(ctx, del("s1", t.TempDir()))
		if err == nil || !strings.HasPrefix(err.Error(), "acp: claude-code stopped: ") ||
			!strings.Contains(err.Error(), "Last output: ") || !strings.Contains(err.Error(), "boom") ||
			strings.Contains(err.Error(), "token-value-12345678") {
			t.Fatalf("err = %v", err)
		}
		if _, ok, _ := st.Get(ctx, "s1", "claude-code"); !ok {
			t.Fatal("a crash keeps the row")
		}
		if liveClient(m, "s1") != nil {
			t.Fatal("a crashed process must leave the map")
		}
	})
}

func TestCloseEndsSessionForGood(t *testing.T) {
	ctx := context.Background()
	agents, logPath := fakeAgents(t, twoTurns(`,"resume":true,"close":true`))
	m, st, dataDir := newTestManager(t, 2, agents, nil)
	if _, err := m.Delegate(ctx, del("s1", t.TempDir())); err != nil {
		t.Fatal(err)
	}
	// A row with no live process is deleted too.
	if err := st.Put(ctx, Record{SessionID: "s2", Agent: "claude-code", ACPSessionID: "x", CWD: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	old := liveClient(m, "s1")
	begin := time.Now()
	m.Close("s1")
	m.Close("s2")
	if d := time.Since(begin); d > 200*time.Millisecond {
		t.Fatalf("Close took %v; it must return at once", d)
	}
	waitFor(t, func() bool { return stopped(old) })
	waitFor(t, func() bool {
		_, ok1, _ := st.Get(ctx, "s1", "claude-code")
		_, ok2, _ := st.Get(ctx, "s2", "claude-code")
		return !ok1 && !ok2
	})
	if countMethod(t, logPath, "session/close") != 1 {
		t.Fatal("an idle session with the close capability gets session/close")
	}
	waitFor(t, func() bool {
		rel, err := AcquireSessionLock(dataDir, "s1", "claude-code")
		if err != nil {
			return false
		}
		_ = rel()
		return true
	})
}

func TestCloseBusySession(t *testing.T) {
	agents, _ := fakeAgents(t, `{`+fakeModes+`,"turns":[
	  {"steps":[{"tool_call":{"id":"t1","kind":"read","title":"read a.go"}},{"sleep_ms":30000}],"stop_reason":"end_turn"}]}`)
	m, st, _ := newTestManager(t, 2, agents, nil)
	started := make(chan struct{}, 1)
	d := del("s1", t.TempDir())
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
	<-started
	m.Close("s1")
	select {
	case err := <-errc:
		if !errors.Is(err, ErrSessionClosed) {
			t.Fatalf("err = %v, want ErrSessionClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not end the busy turn")
	}
	waitFor(t, func() bool {
		_, ok, _ := st.Get(context.Background(), "s1", "claude-code")
		return !ok
	})
}
