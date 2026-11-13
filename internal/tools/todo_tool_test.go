package tools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// todoResult is the decoded result of one todo_list call.
type todoResult struct {
	Todos    []TodoItem     `json:"todos"`
	Revision int64          `json:"revision"`
	Summary  map[string]int `json:"summary"`
}

// callTodo runs todo_list through a registry with the Values map of env.
func callTodo(t *testing.T, env *Env, args string) (todoResult, Result) {
	t.Helper()
	r := NewRegistry()
	r.Register(TodoTool{})
	res := r.Dispatch(context.Background(), "todo_list", json.RawMessage(args), env, map[string]bool{"todo_list": true})
	var out todoResult
	if !res.IsError {
		if err := json.Unmarshal([]byte(res.Text), &out); err != nil {
			t.Fatalf("%v: %s", err, res.Text)
		}
	}
	return out, res
}

func TestTodoToolWriteReadMerge(t *testing.T) {
	env := &Env{Values: map[string]any{}}
	out, res := callTodo(t, env, `{"todos":[{"id":"a","content":"First","status":"pending"},{"id":"b","content":"Second","status":"in_progress"}]}`)
	if res.IsError || todoIDs(out.Todos) != "b,a" || out.Revision != 1 {
		t.Fatal(res.Text)
	}
	if !strings.HasPrefix(res.Text, `{"todos":[`) ||
		!strings.Contains(res.Text, `"revision":1,"summary":{"total":2,"pending":1,"in_progress":1,"completed":0,"cancelled":0}}`) {
		t.Fatal("the result shape:", res.Text)
	}
	read, _ := callTodo(t, env, `{}`)
	if todoIDs(read.Todos) != "b,a" || read.Revision != 1 || read.Summary["total"] != 2 {
		t.Fatalf("read: %+v", read)
	}
	merged, _ := callTodo(t, env, `{"todos":[{"id":"a","status":"completed"}],"merge":true}`)
	if merged.Todos[1].Status != "completed" || merged.Todos[1].Content != "First" || merged.Revision != 2 || merged.Summary["completed"] != 1 {
		t.Fatalf("merge: %+v", merged)
	}
	if _, res := callTodo(t, env, `{"todos":[]}`); !strings.Contains(res.Text, `"todos":[]`) {
		t.Fatal("an empty list must be [] :", res.Text)
	}
}

func TestTodoToolArgumentShapes(t *testing.T) {
	env := &Env{Values: map[string]any{}}
	out, res := callTodo(t, env, `{"todos":"[{\"id\":\"t1\",\"content\":\"Do A\",\"status\":\"pending\"},{\"id\":\"t2\",\"content\":\"Do B\",\"status\":\"in_progress\"}]"}`)
	if res.IsError || out.Summary["total"] != 2 {
		t.Fatal("a JSON string must parse:", res.Text)
	}
	if _, res := callTodo(t, env, `{"todos":42}`); !res.IsError || res.Text != "todos must be a list, got number" {
		t.Fatal(res.Text)
	}
	if _, res := callTodo(t, env, `{"todos":"not json"}`); !res.IsError || res.Text != "todos must be a list, got string" {
		t.Fatal(res.Text)
	}
	if _, res := callTodo(t, env, `{"todos":[{"id":"1","content":"","status":"pending"}]}`); !res.IsError ||
		!strings.Contains(res.Text, "empty or missing content") || strings.Contains(res.Text, `"todos"`) {
		t.Fatal(res.Text)
	}
	if out, _ := callTodo(t, env, `{}`); out.Summary["total"] != 2 {
		t.Fatal("a failed write must keep the list:", out)
	}
	bad, _ := callTodo(t, &Env{Values: map[string]any{}}, `{"todos":["bad","also bad"]}`)
	if bad.Summary["total"] != 2 || bad.Summary["pending"] != 2 {
		t.Fatalf("%+v", bad)
	}
	_, res = callTodo(t, &Env{Values: map[string]any{}}, `{"todos":[{"id":"1","content":"a <b> & c","status":"pending"}]}`)
	if !strings.Contains(res.Text, "a <b> & c") {
		t.Fatal("no HTML escape:", res.Text)
	}
}

func TestTodoToolOneListForEachValuesMap(t *testing.T) {
	values := map[string]any{}
	callTodo(t, &Env{Values: values}, `{"todos":[{"id":"1","content":"Parent task","status":"pending"}]}`)
	if out, _ := callTodo(t, &Env{Values: values}, `{}`); len(out.Todos) != 1 {
		t.Fatal("the same session must see its list")
	}
	if out, _ := callTodo(t, &Env{Values: map[string]any{}}, `{}`); len(out.Todos) != 0 {
		t.Fatal("a child has its own list")
	}
}

func TestTodoToolSchemaAndToolset(t *testing.T) {
	tool := TodoTool{}
	if tool.Name() != "todo_list" || tool.Toolset() != SetTodo || SetTodo != "todo" || tool.ParallelSafe() {
		t.Fatal(tool.Name(), tool.Toolset(), tool.ParallelSafe())
	}
	sc := tool.Schema()
	if sc.Name != "todo_list" || !strings.HasPrefix(sc.Description, "Track a task list for multi-step work (3+ steps).") ||
		!strings.Contains(sc.Description, "the current list.\nList order is priority.") ||
		!strings.HasSuffix(sc.Description, "Always returns the full current list.") {
		t.Fatal(sc.Description)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(sc.Parameters, &schema); err != nil || len(schema.Required) != 0 ||
		schema.Properties["todos"] == nil || schema.Properties["merge"] == nil {
		t.Fatal(err, string(sc.Parameters))
	}
	if !slices.Contains(AllToolsets(), SetTodo) || !ValidToolset("todo", nil) {
		t.Fatal("the todo toolset must be in AllToolsets")
	}
}

func TestTodoToolUsesEnvLimits(t *testing.T) {
	env := &Env{Values: map[string]any{}, Todo: TodoLimits{MaxItems: 2}}
	out, res := callTodo(t, env, `{"todos":[{"id":"a","content":"A","status":"pending"},{"id":"b","content":"B","status":"pending"},{"id":"c","content":"C","status":"pending"}]}`)
	if res.IsError || todoIDs(out.Todos) != "a,b" {
		t.Fatal(res.Text)
	}
}
