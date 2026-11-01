package tools

import (
	"strings"

	"github.com/dnwandana/tether-agent/internal/ctxmgr"
)

// openTodosHeader starts the todo block of the goal judge.
const openTodosHeader = "Open items of the agent's task list:"

// todoMarkers are the Hermes status markers of the snapshot lines.
var todoMarkers = map[string]string{
	"completed":   "[x]",
	"in_progress": "[>]",
	"pending":     "[ ]",
	"cancelled":   "[~]",
}

// FormatTodoSnapshot returns the task list for the end of a compaction summary: the
// header and the open items with their parents (Hermes format_for_injection). It returns
// "" when no item is open.
func FormatTodoSnapshot(items []TodoItem) string {
	lines := openTodoLines(items)
	if len(lines) == 0 {
		return ""
	}
	return ctxmgr.TodoSnapshotHeader + "\n" + strings.Join(lines, "\n")
}

// FormatOpenTodos returns the open items of the task list under a short header, for the
// goal judge. It returns "" when no item is open. When max is more than 0, it cuts the
// text to max runes.
func FormatOpenTodos(items []TodoItem, max int) string {
	lines := openTodoLines(items)
	if len(lines) == 0 {
		return ""
	}
	text := openTodosHeader + "\n" + strings.Join(lines, "\n")
	if max > 0 {
		if r := []rune(text); len(r) > max {
			text = string(r[:max])
		}
	}
	return text
}

// openTodoLines returns the lines of the pending and in_progress items. A finished item
// stays when it has an open child, so a subtask keeps its context. A child line has two
// more spaces of indent than its parent.
func openTodoLines(items []TodoItem) []string {
	children := map[string][]TodoItem{}
	for _, it := range items {
		if it.Parent != "" {
			children[it.Parent] = append(children[it.Parent], it)
		}
	}
	// visited stops a parent cycle in a list that did not go through the store.
	visited := map[string]bool{}
	var render func(it TodoItem, depth int, out *[]string) bool
	render = func(it TodoItem, depth int, out *[]string) bool {
		if visited[it.ID] {
			return false
		}
		visited[it.ID] = true
		var kids []string
		openKid := false
		for _, kid := range children[it.ID] {
			if render(kid, depth+1, &kids) {
				openKid = true
			}
		}
		keep := it.Status == "pending" || it.Status == "in_progress" || openKid
		if keep {
			marker, ok := todoMarkers[it.Status]
			if !ok {
				marker = "[?]"
			}
			*out = append(*out, strings.Repeat("  ", depth)+"- "+marker+" "+it.ID+". "+it.Content+" ("+it.Status+")")
			*out = append(*out, kids...)
		}
		return keep
	}
	var lines []string
	for _, it := range items {
		if it.Parent == "" {
			render(it, 0, &lines)
		}
	}
	return lines
}
