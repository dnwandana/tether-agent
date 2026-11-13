package gateway

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

func TestTodoCommand(t *testing.T) {
	c, sent := interactiveCtx(t)
	values := map[string]any{}
	c.Deps.NewEnv = func(*sessions.Session, string) *tools.Env { return &tools.Env{Values: values} }
	run(t, c, "/todo")
	if r := lastSent(t, sent); r.Text != "No task list in this session." {
		t.Fatalf("got:\n%s", r.Text)
	}
	store := tools.TodoStoreFor(values)
	raw := []json.RawMessage{
		json.RawMessage(`{"id":"1","content":"Fix the *cron* tests","status":"in_progress"}`),
		json.RawMessage(`{"id":"2","content":"Find the flaky_test","status":"completed","parent":"1"}`),
		json.RawMessage(`{"id":"3","content":"Run [all] 20 times","status":"pending","parent":"1"}`),
		json.RawMessage(`{"id":"4","content":"Old idea","status":"cancelled"}`),
	}
	if err := store.Write(raw, false); err != nil {
		t.Fatal(err)
	}
	_, rev, _ := store.Items()
	run(t, c, "/todo")
	want := fmt.Sprintf("📋 Plan 1/3 · revision %d\n"+
		"[>] Fix the \\*cron\\* tests\n"+
		"  [x] Find the flaky\\_test\n"+
		"  [ ] Run \\[all\\] 20 times\n"+
		"[~] Old idea", rev)
	if r := lastSent(t, sent); r.Text != want || r.Buttons != nil {
		t.Fatalf("got:\n%s\nwant:\n%s", r.Text, want)
	}
	cmd := Commands()["todo"]
	if !cmd.WhileBusy || cmd.Help != "Show the task list of this session" {
		t.Fatalf("%+v", cmd)
	}
}

// A /todo that comes while a todo_list call of the turn writes the list reads a whole
// list and does not crash the process.
func TestTodoCommandDuringWrite(t *testing.T) {
	c, sent := interactiveCtx(t)
	values := map[string]any{}
	c.Deps.NewEnv = func(*sessions.Session, string) *tools.Env { return &tools.Env{Values: values} }
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 200 {
			raw := []json.RawMessage{json.RawMessage(fmt.Sprintf(`{"id":"1","content":"step %d","status":"pending"}`, i))}
			if err := tools.TodoStoreFor(values).Write(raw, false); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for range 200 {
		run(t, c, "/todo")
	}
	<-done
	run(t, c, "/todo")
	if r := lastSent(t, sent); r.Text != "📋 Plan 0/1 · revision 200\n[ ] step 199" {
		t.Fatalf("got:\n%s", r.Text)
	}
}

// An empty list after a write is the same as no list.
func TestTodoCommandEmptyList(t *testing.T) {
	c, sent := interactiveCtx(t)
	values := map[string]any{}
	c.Deps.NewEnv = func(*sessions.Session, string) *tools.Env { return &tools.Env{Values: values} }
	if err := tools.TodoStoreFor(values).Write(nil, false); err != nil {
		t.Fatal(err)
	}
	run(t, c, "/todo")
	if r := lastSent(t, sent); r.Text != "No task list in this session." {
		t.Fatalf("got:\n%s", r.Text)
	}
}

// /todo sets the configured limits on the store of the session before the restore, so a
// restore before the first todo_list call does not cut the list to the default limit.
func TestTodoCommandSetsConfiguredLimits(t *testing.T) {
	c, _ := interactiveCtx(t)
	values := map[string]any{}
	c.Deps.NewEnv = func(*sessions.Session, string) *tools.Env {
		return &tools.Env{Values: values, Todo: tools.TodoLimits{MaxItems: 300}}
	}
	run(t, c, "/todo")
	raw := make([]json.RawMessage, 300)
	for i := range raw {
		raw[i] = json.RawMessage(fmt.Sprintf(`{"id":"%d","content":"step %d","status":"pending"}`, i+1, i+1))
	}
	store := tools.TodoStoreFor(values)
	if err := store.Write(raw, false); err != nil {
		t.Fatal(err)
	}
	if items, _, _ := store.Items(); len(items) != 300 {
		t.Fatalf("the store keeps the configured 300 items, got %d", len(items))
	}
}
