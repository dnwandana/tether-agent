package tools

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// todoDescription is the Hermes text of tools/todo_tool.py with no change. The
// item shape and the merge rules are only in the parameter schema.
const todoDescription = "Track a task list for multi-step work (3+ steps). Use for complex tasks " +
	"with 3+ steps or when the user provides multiple tasks. " +
	"For 'all N items' tasks, enumerate every instance as its own checklist " +
	"item so none are silently dropped. " +
	"Call with no parameters to read the current list.\n" +
	"List order is priority. Only ONE item in_progress at a time. " +
	"Break large phases into subtasks via parent. " +
	"Mark an item completed only after the work is verified done, never " +
	"based on intent. If something fails, cancel it and add a revised " +
	"item. Always returns the full current list."

// todoParameters is the Hermes parameter schema of tools/todo_tool.py.
const todoParameters = `{
  "type": "object",
  "properties": {
    "todos": {
      "type": "array",
      "description": "Task items to write.",
      "items": {
        "type": "object",
        "properties": {
          "id": {"type": "string"},
          "content": {"type": "string", "description": "Task description"},
          "status": {"type": "string", "enum": ["pending", "in_progress", "completed", "cancelled"]},
          "parent": {"type": "string", "description": "Optional id of another item, making this a nested subtask. Omit for top-level."}
        },
        "required": ["id", "content", "status"]
      }
    },
    "merge": {
      "type": "boolean",
      "description": "true: update existing items by id, add new ones. false (default): replace the entire list with a fresh plan.",
      "default": false
    }
  },
  "required": []
}`

// TodoTool is the todo_list tool. It keeps the task list of the session in the
// TodoStore of Env.Values, so a child of delegate_task has its own list.
// Sessions lets the first call restore the list from history. A nil Sessions
// restores nothing.
type TodoTool struct {
	Sessions *sessions.Store
}

type todoArgs struct {
	Todos json.RawMessage `json:"todos"`
	Merge bool            `json:"merge"`
}

// todoSummary counts the items. The field order is the key order of Hermes.
type todoSummary struct {
	Total      int `json:"total"`
	Pending    int `json:"pending"`
	InProgress int `json:"in_progress"`
	Completed  int `json:"completed"`
	Cancelled  int `json:"cancelled"`
}

// todoOutput is the result of one call, in the key order of Hermes.
type todoOutput struct {
	Todos    []TodoItem  `json:"todos"`
	Revision int64       `json:"revision"`
	Summary  todoSummary `json:"summary"`
}

// Name returns "todo_list".
func (TodoTool) Name() string { return "todo_list" }

// Toolset returns the todo toolset.
func (TodoTool) Toolset() string { return SetTodo }

// ParallelSafe returns false, because each call writes the list.
func (TodoTool) ParallelSafe() bool { return false }

// Schema returns the tool schema for the model. The text is the Hermes text.
func (TodoTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{
		Name:        "todo_list",
		Description: todoDescription,
		Parameters:  json.RawMessage(todoParameters),
	}
}

// Run writes the list when the call has todos, and reads it when not. It
// always returns the full list with its revision and a summary.
func (t TodoTool) Run(ctx context.Context, raw json.RawMessage, env *Env) (string, error) {
	var a todoArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	var (
		values map[string]any
		lim    TodoLimits
	)
	if env != nil {
		values = env.Values
		lim = env.Todo
	}
	store := TodoStoreWith(values, lim)
	if _, _, loaded := store.Items(); !loaded && t.Sessions != nil && env != nil {
		// Restore the list first, so a merge after a restart keeps the old
		// items. A query error does not fail the call: the call then works on
		// the live list, like a session with no history.
		_, _ = LoadTodos(ctx, t.Sessions, env.Values, env.SessionID)
	}
	todos := bytes.TrimSpace(a.Todos)
	if len(todos) > 0 && string(todos) != "null" {
		todos = unwrapTodoString(todos)
		if todos[0] != '[' {
			return "", toolErr("todos must be a list, got %s", jsonKind(todos))
		}
		var items []json.RawMessage
		if err := json.Unmarshal(todos, &items); err != nil {
			return "", toolErr("invalid arguments: %v", err)
		}
		if err := store.Write(items, a.Merge); err != nil {
			return "", err
		}
	}
	items, revision, _ := store.Items()
	return encodeTodoOutput(items, revision)
}

// unwrapTodoString returns the JSON value in a JSON string, because a model
// can send the list as a string. A string that is not valid JSON stays as it
// is, so the kind check reports a string.
func unwrapTodoString(v []byte) []byte {
	if v[0] != '"' {
		return v
	}
	var s string
	if json.Unmarshal(v, &s) != nil {
		return v
	}
	inner := bytes.TrimSpace([]byte(s))
	if len(inner) == 0 || !json.Valid(inner) {
		return v
	}
	return inner
}

// jsonKind returns the kind name of the JSON value v for an error text.
func jsonKind(v []byte) string {
	switch v[0] {
	case '"':
		return "string"
	case '{':
		return "object"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	}
	return "number"
}

// encodeTodoOutput writes the result with no HTML escape, like the
// ensure_ascii=False text of Hermes. An empty list is [], because the restore
// from history needs a list.
func encodeTodoOutput(items []TodoItem, revision int64) (string, error) {
	if items == nil {
		items = []TodoItem{}
	}
	sum := todoSummary{Total: len(items)}
	for _, it := range items {
		switch it.Status {
		case todoPending:
			sum.Pending++
		case todoInProgress:
			sum.InProgress++
		case todoCompleted:
			sum.Completed++
		case todoCancelled:
			sum.Cancelled++
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(todoOutput{Todos: items, Revision: revision, Summary: sum}); err != nil {
		return "", err
	}
	return string(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))), nil
}
