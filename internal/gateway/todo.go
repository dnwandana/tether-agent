package gateway

import (
	"context"
	"fmt"
	"strings"

	"github.com/dnwandana/tether-agent/internal/tools"
)

func init() {
	Register(Command{Name: "todo", Help: "Show the task list of this session", Run: cmdTodo, WhileBusy: true, MemberOK: true})
}

// todoNoList is the /todo reply for a session with no item.
const todoNoList = "No task list in this session."

// cmdTodo shows the task list of the current session. It makes no model call and
// changes nothing. It reads the list only through the locked store, because a
// todo_list call of a running turn can write the list at the same time.
func cmdTodo(ctx context.Context, c *CommandCtx, _ string) error {
	s, err := c.current(ctx)
	if err != nil {
		return err
	}
	// A new map makes LoadTodos read the list from history only.
	var values map[string]any
	if c.Deps.NewEnv != nil {
		if env := c.Deps.NewEnv(s, ""); env != nil {
			values = env.Values
		}
	}
	if values == nil {
		values = map[string]any{}
	}
	if _, err := tools.LoadTodos(ctx, c.store(), values, s.ID); err != nil {
		return err
	}
	// One Items call gives the list and the revision of the same write.
	items, revision, _ := tools.TodoStoreFor(values).Items()
	c.send(Reply{Text: todoReply(items, revision)})
	return nil
}

// todoReply returns the /todo text: a header line, then one line for each item in
// list order. Only the content is model text, so only the content gets the escape.
func todoReply(items []tools.TodoItem, revision int64) string {
	if len(items) == 0 {
		return todoNoList
	}
	done, total := tools.TodoSummary(items)
	parents := make(map[string]string, len(items))
	for _, it := range items {
		parents[it.ID] = it.Parent
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📋 Plan %d/%d · revision %d", done, total, revision)
	for _, it := range items {
		b.WriteByte('\n')
		b.WriteString(strings.Repeat("  ", tools.TodoLevel(parents, it.Parent, len(items))))
		b.WriteString(todoMarker(it.Status))
		b.WriteByte(' ')
		b.WriteString(EscapeMarkdown(it.Content))
	}
	return b.String()
}

// todoMarker returns the marker of an item status. An unknown status shows as pending.
func todoMarker(status string) string {
	switch status {
	case "completed":
		return "[x]"
	case "in_progress":
		return "[>]"
	case "cancelled":
		return "[~]"
	}
	return "[ ]"
}
