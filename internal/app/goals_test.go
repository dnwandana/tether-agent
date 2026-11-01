package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/ctxmgr"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

func TestNewBuildsGoalManager(t *testing.T) {
	a := newApp(t)
	if a.Goals == nil || a.Procs == nil || a.Deps().Goals != a.Goals {
		t.Fatalf("%+v %+v", a.Goals, a.Procs)
	}
	tt, ok := a.Registry.Get("terminal")
	if !ok || tt.(*tools.TerminalTool).Procs != a.Procs {
		t.Fatal("the terminal tool must register its background processes")
	}
	if a.Agent.Compactor.Todo == nil {
		t.Fatal("the compactor needs the todo hook")
	}
	if a.Deps().SendNotice != nil {
		t.Fatal("the chat role has no Telegram notice path")
	}
}

func TestGoalTodoHooks(t *testing.T) {
	a := newApp(t)
	sess, err := a.Store.ActiveOrCreate(ctx, "cli:t", sessions.Session{Source: "cli", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Agent.Compactor.Todo(ctx, sess.ID); got != "" {
		t.Fatalf("no list gives no snapshot: %q", got)
	}
	raw := []json.RawMessage{
		json.RawMessage(`{"id":"1","content":"Find the flaky test","status":"completed"}`),
		json.RawMessage(`{"id":"2","content":"Run the cron tests 20 times","status":"pending"}`),
	}
	if err := tools.TodoStoreFor(a.NewEnv(sess, "cli").Values).Write(raw, false); err != nil {
		t.Fatal(err)
	}
	got := a.Agent.Compactor.Todo(ctx, sess.ID)
	if !strings.HasPrefix(got, ctxmgr.TodoSnapshotHeader) ||
		!strings.Contains(got, "- [ ] 2. Run the cron tests 20 times (pending)") || strings.Contains(got, "Find the flaky test") {
		t.Fatalf("%q", got)
	}
	open := a.todoOpen(ctx, sess.ID)
	if !strings.HasPrefix(open, "Open items of the agent's task list:") ||
		!strings.Contains(open, "- [ ] 2. Run the cron tests 20 times (pending)") {
		t.Fatalf("%q", open)
	}
}

// A gate runs through the terminal tool with no .env secret.
func TestGoalExecUsesCleanEnv(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, fakeProviderURL(t))
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("GATE_SECRET=s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := New(ctx, Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	sess, _ := a.Store.ActiveOrCreate(ctx, "cli:t", sessions.Session{Source: "cli", Kind: "chat"})
	out, code, err := a.goalExec(ctx, sess, "echo ${GATE_SECRET:-none}; exit 3", 10*time.Second)
	if err != nil || code != 3 || !strings.Contains(out, "none") || strings.Contains(out, "s3cret") {
		t.Fatal(out, code, err)
	}
}

// In the chat role, a process exit wakes the goal of the CLI. The barrier clears only after
// the CLI took the continuation.
func TestProcessExitWakesCLIGoal(t *testing.T) {
	a := newApp(t)
	cli := &gateway.CLI{Store: a.Store, Key: "cli:t", Deps: a.Deps(), Log: a.Log}
	a.AttachCLI(cli)
	sess, _ := a.Store.ActiveOrCreate(ctx, "cli:t", sessions.Session{Source: "cli", Kind: "chat"})
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	st, err := a.Goals.Update(ctx, sess.ID, func(st *goals.State, _ bool) error {
		*st = goals.NewState("build it", goals.Contract{}, 20, time.Now())
		st.WaitOnPID(pid, "build", time.Now())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	a.Procs.Add(sess.ID, pid, "true", "", time.Now())
	// The after-turn step arms the waker when the judge parks the goal. Wake ignores a
	// session that the waker never saw.
	cli.GoalWaker().Arm("cli:t", sess.ID, st)
	a.Procs.Exit(pid, 0, time.Now())
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, _, err := a.Goals.Load(ctx, sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st.WaitingOnPID == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the exit must wake the goal: %+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestGatewayRoleWiresGoals(t *testing.T) {
	url, _, _ := fakeBotAPI(t, true)
	a := gatewayApp(t, url, "telegram:\n  enabled: true\n  bot_token: T\n")
	if _, err := a.AddTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	d := a.Gateway().Deps
	if d.Goals != a.Goals || d.SendNotice == nil {
		t.Fatalf("%+v", d)
	}
}

// Housekeeping runs the goal restart step after the resume of the cut turns. A goal of a
// gateway that was down for 2 h pauses.
func TestHousekeepingPausesStaleGoal(t *testing.T) {
	a := gatewayApp(t, "", "")
	sess, err := a.Store.ActiveOrCreate(ctx, "telegram:dm:5",
		sessions.Session{Source: "telegram", ChatID: "5", UserID: "5", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Goals.Update(ctx, sess.ID, func(st *goals.State, _ bool) error {
		*st = goals.NewState("fix the cron tests", goals.Contract{}, 20, time.Now().Add(-3*time.Hour))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DB.KVSet(ctx, aliveAtKey, strconv.FormatInt(time.Now().Add(-2*time.Hour).UnixMilli(), 10)); err != nil {
		t.Fatal(err)
	}
	hctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	a.Housekeeping(hctx)
	st, _, err := a.Goals.Load(ctx, sess.ID)
	if err != nil || st.Status != goals.StatusPaused {
		t.Fatalf("%+v %v", st, err)
	}
}

// appendTodoResult saves one paired todo_list call and its result in the session.
func appendTodoResult(t *testing.T, a *App, sessionID, callID, content string) {
	t.Helper()
	call := provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: callID, Name: "todo_list", Args: "{}"}}}
	res := provider.Message{Role: provider.RoleTool, ToolCallID: callID, ToolName: "todo_list", Content: content}
	for _, m := range []provider.Message{call, res} {
		if _, err := a.Store.Append(ctx, sessionID, m, sessions.MessageMeta{}); err != nil {
			t.Fatal(err)
		}
	}
}

// A child keeps its todo list in its own Values map, so each compaction of a child reads the
// newest stored result and adds no entry to the values of the app.
func TestCompactorTodoOfSubagentReadsHistory(t *testing.T) {
	a := newApp(t)
	parent, err := a.Store.ActiveOrCreate(ctx, "cli:t", sessions.Session{Source: "cli", Kind: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := a.Store.Create(ctx, sessions.Session{Source: "cli", ParentID: parent.ID, Kind: agent.KindSubagent})
	if err != nil {
		t.Fatal(err)
	}
	appendTodoResult(t, a, child.ID, "c1", `{"todos":[{"id":"1","content":"First step","status":"pending"}],"revision":1}`)
	if got := a.Agent.Compactor.Todo(ctx, child.ID); !strings.Contains(got, "First step") {
		t.Fatalf("%q", got)
	}
	appendTodoResult(t, a, child.ID, "c2", `{"todos":[{"id":"2","content":"Second step","status":"pending"}],"revision":2}`)
	got := a.Agent.Compactor.Todo(ctx, child.ID)
	if !strings.Contains(got, "Second step") || strings.Contains(got, "First step") {
		t.Fatalf("a second compaction must read the newest list: %q", got)
	}
	a.valuesMu.Lock()
	_, kept := a.values[child.ID]
	a.valuesMu.Unlock()
	if kept {
		t.Fatal("a child compaction must not add an entry to the values of the app")
	}
}
