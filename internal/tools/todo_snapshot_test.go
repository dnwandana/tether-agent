package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/ctxmgr"
)

func snapItem(id, status, parent string) TodoItem {
	return TodoItem{ID: id, Content: "task " + id, Status: status, Parent: parent}
}

func TestFormatTodoSnapshotNested(t *testing.T) {
	got := FormatTodoSnapshot([]TodoItem{snapItem("wp1", "in_progress", ""), snapItem("t1", "pending", "wp1"), snapItem("t2", "pending", "wp1")})
	want := ctxmgr.TodoSnapshotHeader + "\n- [>] wp1. task wp1 (in_progress)\n  - [ ] t1. task t1 (pending)\n  - [ ] t2. task t2 (pending)"
	if got != want {
		t.Fatalf("%q", got)
	}
	got = FormatTodoSnapshot([]TodoItem{snapItem("wp1", "completed", ""), snapItem("t1", "pending", "wp1")})
	if !strings.Contains(got, "- [x] wp1. task wp1 (completed)") || !strings.Contains(got, "\n  - [ ] t1. task t1 (pending)") {
		t.Fatalf("a parent of an open item stays: %q", got)
	}
	got = FormatTodoSnapshot([]TodoItem{snapItem("wp1", "completed", ""), snapItem("t1", "completed", "wp1"), snapItem("wp2", "pending", "")})
	if strings.Contains(got, "wp1") || strings.Contains(got, "t1") || !strings.Contains(got, "wp2") {
		t.Fatalf("a finished subtree goes: %q", got)
	}
}

func TestFormatTodoSnapshotOnlyOpenItems(t *testing.T) {
	got := FormatTodoSnapshot([]TodoItem{
		{ID: "1", Content: "Do thing", Status: "completed"},
		{ID: "2", Content: "Next", Status: "pending"},
		{ID: "3", Content: "Working", Status: "in_progress"},
	})
	if strings.Contains(got, "[x]") || strings.Contains(got, "Do thing") || !strings.Contains(got, "[ ] 2. Next") || !strings.Contains(got, "[>] 3. Working") {
		t.Fatalf("%q", got)
	}
	if got := FormatTodoSnapshot([]TodoItem{snapItem("a", "completed", ""), snapItem("a1", "cancelled", "a")}); got != "" {
		t.Fatalf("a finished list adds nothing: %q", got)
	}
	if FormatTodoSnapshot(nil) != "" {
		t.Fatal("no list adds nothing")
	}
}

func TestFormatTodoSnapshotIsBounded(t *testing.T) {
	s := TodoStoreFor(map[string]any{})
	long, _ := json.Marshal(map[string]string{"id": "1", "content": strings.Repeat("A", 50001), "status": "pending"})
	if err := s.Write([]json.RawMessage{long}, false); err != nil {
		t.Fatal(err)
	}
	items, _, _ := s.Items()
	if n := utf8.RuneCountInString(FormatTodoSnapshot(items)); n >= TodoMaxContent+200 {
		t.Fatal(n)
	}
}

func TestFormatOpenTodos(t *testing.T) {
	items := []TodoItem{
		{ID: "1", Content: "Find the flaky test", Status: "completed"},
		{ID: "2", Content: "Replace the real clock with a fake clock", Status: "in_progress"},
		{ID: "3", Content: "Run the cron tests 20 times", Status: "pending"},
	}
	want := "Open items of the agent's task list:\n" +
		"- [>] 2. Replace the real clock with a fake clock (in_progress)\n" +
		"- [ ] 3. Run the cron tests 20 times (pending)"
	if got := FormatOpenTodos(items, 2000); got != want {
		t.Fatalf("%q", got)
	}
	if got := FormatOpenTodos(items[:1], 2000); got != "" {
		t.Fatalf("%q", got)
	}
	if got := FormatOpenTodos(items, 40); utf8.RuneCountInString(got) != 40 || !strings.HasPrefix(got, "Open items") {
		t.Fatalf("%q", got)
	}
}
