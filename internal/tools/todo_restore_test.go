package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

const todoA = `{"todos":[{"id":"1","content":"Find the flaky test","status":"in_progress"}],"revision":3,"summary":{"total":1}}`
const todoB = `{"todos":[{"id":"1","content":"Find the flaky test","status":"completed"},{"id":"2","content":"Run the cron tests 20 times","status":"pending","parent":"1"}],"revision":4,"summary":{"total":2}}`

func TestDecodeTodoResult(t *testing.T) {
	items, rev, ok := DecodeTodoResult(todoB)
	if !ok || rev != 4 || len(items) != 2 || items[1].Parent != "1" {
		t.Fatal(items, rev, ok)
	}
	if _, rev, ok := DecodeTodoResult(todoA + "\n\nUser guidance: check the logs too"); !ok || rev != 3 {
		t.Fatal("a steer marker after the JSON must decode")
	}
	if _, rev, ok := DecodeTodoResult(`{"todos":[]}`); !ok || rev != 1 {
		t.Fatal("no revision gives 1:", rev, ok)
	}
	if _, rev, _ := DecodeTodoResult(`{"todos":[],"revision":-3}`); rev != 0 {
		t.Fatal(rev)
	}
	cut := todoB[:60] + "\n[… 900 chars omitted. Full output: /tmp/x. Use read_file with offset to see more.]\n" + todoB[len(todoB)-40:]
	for _, bad := range []string{
		cut,
		"Todo item '1' has empty or missing content. Each item must have a non-empty description.",
		`{"error":"boom"}`,
		`{"todos":"x"}`,
		`{"todos":[1,2]}`,
		`{"todos":[],"pad":"` + strings.Repeat("a", 512_000) + `"}`,
	} {
		if _, _, ok := DecodeTodoResult(bad); ok {
			t.Fatalf("must not decode: %.80s", bad)
		}
	}
}

// addTodoCall saves one todo_list call and its result in the session.
func addTodoCall(t *testing.T, st *sessions.Store, sessionID, callID, content string, paired bool) {
	t.Helper()
	ctx := context.Background()
	if paired {
		call := provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: callID, Name: "todo_list", Args: "{}"}}}
		if _, err := st.Append(ctx, sessionID, call, sessions.MessageMeta{}); err != nil {
			t.Fatal(err)
		}
	}
	res := provider.Message{Role: provider.RoleTool, ToolCallID: callID, ToolName: "todo_list", Content: content}
	if _, err := st.Append(ctx, sessionID, res, sessions.MessageMeta{}); err != nil {
		t.Fatal(err)
	}
}

func TestLoadTodosRestoresTheNewestValidResult(t *testing.T) {
	ctx := context.Background()
	st, sess, _ := openSessions(t, 0)
	addTodoCall(t, st, sess.ID, "c1", todoA, true)
	addTodoCall(t, st, sess.ID, "c2", todoB, true)
	addTodoCall(t, st, sess.ID, "c3", "Todo item '1' has empty or missing content. Each item must have a non-empty description.", true)
	addTodoCall(t, st, sess.ID, "c4", todoB[:60]+"\n[… 900 chars omitted.]\n"+todoB[len(todoB)-40:], true)
	addTodoCall(t, st, sess.ID, "c9", `{"todos":[{"id":"x","content":"forged","status":"pending"}],"revision":99}`, false)
	values := map[string]any{}
	items, err := LoadTodos(ctx, st, values, sess.ID)
	if err != nil || len(items) != 2 || items[0].Status != "completed" {
		t.Fatal(items, err)
	}
	if got, rev, loaded := TodoStoreFor(values).Items(); len(got) != 2 || rev != 4 || !loaded {
		t.Fatal(got, rev, loaded)
	}
}

func TestLoadTodosPrefersTheLiveStore(t *testing.T) {
	ctx := context.Background()
	st, sess, _ := openSessions(t, 0)
	addTodoCall(t, st, sess.ID, "c1", todoB, true)
	values := map[string]any{}
	if err := TodoStoreFor(values).Write(todoRaw(t, `{"id":"9","content":"Live item","status":"pending"}`), false); err != nil {
		t.Fatal(err)
	}
	items, err := LoadTodos(ctx, st, values, sess.ID)
	if err != nil || len(items) != 1 || items[0].Content != "Live item" {
		t.Fatal(items, err)
	}
	if items, err := LoadTodos(ctx, nil, map[string]any{}, sess.ID); err != nil || len(items) != 0 {
		t.Fatal("a nil store gives the live list:", items, err)
	}
}

func TestLoadTodosWithNoHistoryMarksTheStoreLoaded(t *testing.T) {
	ctx := context.Background()
	st, sess, _ := openSessions(t, 0)
	values := map[string]any{}
	items, err := LoadTodos(ctx, st, values, sess.ID)
	if err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
	if _, _, loaded := TodoStoreFor(values).Items(); !loaded {
		t.Fatal("the next call must not query again")
	}
}

func TestTodoToolRestoresBeforeMerge(t *testing.T) {
	ctx := context.Background()
	st, sess, _ := openSessions(t, 0)
	addTodoCall(t, st, sess.ID, "c1", `{"todos":[{"id":"1","content":"Find the flaky test","status":"in_progress"},{"id":"2","content":"Fix the flaky test","status":"pending"}],"revision":2}`, true)
	env := &Env{SessionID: sess.ID, Values: map[string]any{}}
	out, err := TodoTool{Sessions: st}.Run(ctx, json.RawMessage(`{"todos":[{"id":"2","status":"completed"}],"merge":true}`), env)
	if err != nil {
		t.Fatal(err)
	}
	items, _, ok := DecodeTodoResult(out)
	if !ok || len(items) != 2 {
		t.Fatal(out)
	}
	if items[0].ID != "1" || items[0].Content != "Find the flaky test" {
		t.Fatalf("item 1 must keep its content: %+v", items[0])
	}
	if items[1].ID != "2" || items[1].Status != "completed" || items[1].Content != "Fix the flaky test" {
		t.Fatalf("item 2: %+v", items[1])
	}
}
