package telegram

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/tools"
)

// planTodo returns one item with no parent.
func planTodo(id, content, status string) tools.TodoItem {
	return tools.TodoItem{ID: id, Content: content, Status: status}
}

// planExampleTexts are the item texts of the plan examples.
var planExampleTexts = [5]string{
	"Locate the 't3 codes' session in General channel, inspect the cut-off messages",
	"Correlate with gateway logs at send time (telegram errors)",
	"Read source: Telegram send/chunking path, find root cause",
	"Compare with Hermes reference implementation",
	"Write findings to ~/reports/tether/ + terse summary",
}

// planExample returns the five example items with the IDs "1" to "5" and the statuses.
func planExample(statuses ...string) []tools.TodoItem {
	var items []tools.TodoItem
	for i, st := range statuses {
		items = append(items, planTodo(string(rune('1'+i)), planExampleTexts[i], st))
	}
	return items
}

func TestPlanMessage(t *testing.T) {
	x := planExampleTexts
	cases := []struct {
		name          string
		before, after []tools.TodoItem
		want          string
		done          bool
	}{
		{name: "1 equal lists",
			before: []tools.TodoItem{planTodo("1", "A", "in_progress"), planTodo("2", "B", "pending")},
			after:  []tools.TodoItem{planTodo("1", "A", "in_progress"), planTodo("2", "B", "pending")}},
		{name: "1 two empty lists"},
		{name: "2 empty after",
			before: []tools.TodoItem{planTodo("1", "A", "pending")}},
		{name: "3 the start list of the example",
			after: planExample("in_progress", "pending", "pending", "pending", "pending"),
			want:  "📋 Plan 0/5\n[>] " + x[0] + "\n[ ] " + x[1] + "\n[ ] " + x[2] + "\n[ ] " + x[3] + "\n[ ] " + x[4]},
		{name: "3 the old plan is finished",
			before: []tools.TodoItem{planTodo("1", "A", "completed")},
			after:  []tools.TodoItem{planTodo("1", "A", "completed"), planTodo("2", "B", "pending")},
			want:   "📋 Plan 1/2\n[x] A\n[ ] B"},
		{name: "3 reused IDs with new texts",
			before: []tools.TodoItem{planTodo("1", "A", "completed"), planTodo("2", "B", "in_progress"), planTodo("3", "C", "pending")},
			after:  []tools.TodoItem{planTodo("1", "X", "pending"), planTodo("2", "Y", "pending"), planTodo("3", "Z", "pending")},
			want:   "📋 Plan 0/3\n[ ] X\n[ ] Y\n[ ] Z"},
		{name: "3 nesting and one line",
			after: []tools.TodoItem{
				planTodo("1", "A", "pending"),
				{ID: "2", Content: "B", Status: "pending", Parent: "1"},
				{ID: "3", Content: "C\n  more", Status: "pending", Parent: "2"},
			},
			want: "📋 Plan 0/3\n[ ] A\n  [ ] B\n    [ ] C more"},
		{name: "4 the done list of the example",
			before: planExample("completed", "completed", "completed", "in_progress", "pending"),
			after:  planExample("completed", "completed", "completed", "cancelled", "completed"),
			want:   "📋 Plan done 4/4\n[x] " + x[0] + "\n[x] " + x[1] + "\n[x] " + x[2] + "\n[~] " + x[3] + "\n[x] " + x[4],
			done:   true},
		{name: "4 the last open item closes",
			before: []tools.TodoItem{planTodo("1", "A", "completed"), planTodo("2", "B", "in_progress")},
			after:  []tools.TodoItem{planTodo("1", "A", "completed"), planTodo("2", "B", "completed")},
			want:   "📋 Plan done 2/2\n[x] A\n[x] B", done: true},
		{name: "5 a plan written and closed in one call",
			after: []tools.TodoItem{planTodo("1", "A", "completed"), planTodo("2", "B", "cancelled")},
			want:  "📋 Plan done 1/1\n[x] A\n[~] B", done: true},
		{name: "5 a closed plan that shares no pair",
			before: []tools.TodoItem{planTodo("1", "A", "completed")},
			after:  []tools.TodoItem{planTodo("1", "X", "completed")},
			want:   "📋 Plan done 1/1\n[x] X", done: true},
		{name: "6 cancelled to completed in a finished plan",
			before: []tools.TodoItem{planTodo("1", "A", "completed"), planTodo("2", "B", "cancelled")},
			after:  []tools.TodoItem{planTodo("1", "A", "completed"), planTodo("2", "B", "completed")}},
		{name: "6 a text fix in a finished plan",
			before: []tools.TodoItem{planTodo("1", "A", "completed"), planTodo("2", "B", "completed")},
			after:  []tools.TodoItem{planTodo("1", "A", "completed"), planTodo("2", "B2", "completed")}},
		{name: "7 the change message of the example",
			before: planExample("completed", "completed", "in_progress", "pending", "pending"),
			after: append(planExample("completed", "completed", "completed", "cancelled", "pending"),
				planTodo("6", "Add a regression test for the flood wait", "pending")),
			want: "✅ 3/5 " + x[2] + "\n🚫 " + x[3] + "\n➕ Add a regression test for the flood wait"},
		{name: "7 the groups keep their order",
			before: []tools.TodoItem{planTodo("1", "A", "in_progress"), planTodo("2", "B", "pending"), planTodo("3", "C", "pending")},
			after: []tools.TodoItem{planTodo("4", "D", "pending"), planTodo("1", "A", "completed"),
				planTodo("2", "B", "cancelled"), planTodo("3", "C", "in_progress")},
			want: "✅ 1/3 A\n🚫 B\n➕ D"},
		{name: "7 the count goes up line by line",
			before: []tools.TodoItem{planTodo("1", "Z", "completed"), planTodo("2", "A", "in_progress"),
				planTodo("3", "B", "pending"), planTodo("4", "C", "pending"), planTodo("5", "D", "pending")},
			after: []tools.TodoItem{planTodo("1", "Z", "completed"), planTodo("2", "A", "completed"),
				planTodo("3", "B", "completed"), planTodo("4", "C", "pending"), planTodo("5", "D", "pending")},
			want: "✅ 2/5 A\n✅ 3/5 B"},
		{name: "7 the same pairs with new statuses",
			before: []tools.TodoItem{planTodo("1", "A", "in_progress"), planTodo("2", "B", "pending"), planTodo("3", "C", "pending")},
			after:  []tools.TodoItem{planTodo("1", "A", "completed"), planTodo("2", "B", "completed"), planTodo("3", "C", "in_progress")},
			want:   "✅ 1/3 A\n✅ 2/3 B"},
		{name: "7 a new completed item gets only a plus line",
			before: []tools.TodoItem{planTodo("1", "A", "in_progress"), planTodo("2", "B", "pending")},
			after:  []tools.TodoItem{planTodo("1", "A", "completed"), planTodo("2", "B", "pending"), planTodo("3", "C", "completed")},
			want:   "✅ 2/3 A\n➕ C"},
		{name: "7 the n-th duplicate ID matches the n-th item",
			before: []tools.TodoItem{planTodo("?", "A", "completed"), planTodo("?", "B", "pending"), planTodo("1", "C", "pending")},
			after:  []tools.TodoItem{planTodo("?", "A", "completed"), planTodo("?", "B", "completed"), planTodo("1", "C", "pending")},
			want:   "✅ 2/3 B"},
		{name: "7 an extra duplicate ID is new",
			before: []tools.TodoItem{planTodo("?", "A", "pending")},
			after:  []tools.TodoItem{planTodo("?", "A", "pending"), planTodo("?", "B", "pending")},
			want:   "➕ B"},
		{name: "7 a change line is one line",
			before: []tools.TodoItem{planTodo("1", "A", "pending"), planTodo("2", "B", "pending")},
			after:  []tools.TodoItem{planTodo("1", "A\n  done", "completed"), planTodo("2", "B", "pending")},
			want:   "✅ 1/2 A done"},
		{name: "8 an item starts",
			before: []tools.TodoItem{planTodo("1", "A", "pending"), planTodo("2", "B", "pending")},
			after:  []tools.TodoItem{planTodo("1", "A", "in_progress"), planTodo("2", "B", "pending")}},
		{name: "8 an item ID goes away",
			before: []tools.TodoItem{planTodo("1", "A", "pending"), planTodo("2", "B", "pending")},
			after:  []tools.TodoItem{planTodo("1", "A", "pending")}},
		{name: "8 a text edit",
			before: []tools.TodoItem{planTodo("1", "A", "pending"), planTodo("2", "B", "pending")},
			after:  []tools.TodoItem{planTodo("1", "A2", "pending"), planTodo("2", "B", "pending")}},
		{name: "8 a new order",
			before: []tools.TodoItem{planTodo("1", "A", "pending"), planTodo("2", "B", "pending")},
			after:  []tools.TodoItem{planTodo("2", "B", "pending"), planTodo("1", "A", "pending")}},
		{name: "8 a new parent",
			before: []tools.TodoItem{planTodo("1", "A", "pending"), planTodo("2", "B", "pending")},
			after:  []tools.TodoItem{planTodo("1", "A", "pending"), {ID: "2", Content: "B", Status: "pending", Parent: "1"}}},
		{name: "8 a closed item opens again",
			before: []tools.TodoItem{planTodo("1", "A", "completed"), planTodo("2", "B", "pending")},
			after:  []tools.TodoItem{planTodo("1", "A", "pending"), planTodo("2", "B", "pending")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, done := planMessage(c.before, c.after)
			if text != c.want || done != c.done {
				t.Fatalf("got %q, %v; want %q, %v", text, done, c.want, c.done)
			}
		})
	}
}

func TestPlanMessageLeavesOutTheCancelledItemsFromTheCount(t *testing.T) {
	text, done := planMessage(nil, []tools.TodoItem{
		planTodo("1", "a", "completed"),
		planTodo("2", "b", "cancelled"),
		planTodo("3", "c", "pending"),
	})
	if text != "📋 Plan 1/2\n[x] a\n[~] b\n[ ] c" || done {
		t.Fatalf("%q %v", text, done)
	}
}

// bigPlan returns n items with 4000-character contents, the IDs "1" to "n", and the status.
func bigPlan(n int, status string) []tools.TodoItem {
	var items []tools.TodoItem
	for i := 1; i <= n; i++ {
		items = append(items, planTodo(fmt.Sprint(i), strings.Repeat("x", 4000), status))
	}
	return items
}

func TestPlanMessageLengthLimit(t *testing.T) {
	long := strings.Repeat("x", 4000)

	// The start list: 13 + 1 + 4 + 4000 + 10 = 4028 units. A second item does not fit.
	text, _ := planMessage(nil, bigPlan(256, "pending"))
	if want := "📋 Plan 0/256\n[ ] " + long + "\n+255 more"; text != want {
		t.Fatalf("start list: len %d, %q", UTF16Len(text), text[:min(len(text), 40)])
	}

	// The done list: the header with one item is 4035 units, so only the header stays.
	text, done := planMessage(nil, bigPlan(256, "completed"))
	if !done || UTF16Len(text) > statusTextLimit || !strings.HasPrefix(text, "📋 Plan done 256/256\n") ||
		!strings.HasSuffix(text, "\n+256 more") {
		t.Fatalf("done list: len %d, %q", UTF16Len(text), text[:min(len(text), 40)])
	}

	// The change message gives up its last lines in the same way.
	before := []tools.TodoItem{planTodo("0", "A", "pending")}
	after := append([]tools.TodoItem{planTodo("0", "A", "pending")}, bigPlan(255, "pending")...)
	text, _ = planMessage(before, after)
	if want := "➕ " + long + "\n+254 more"; text != want {
		t.Fatalf("change message: len %d, %q", UTF16Len(text), text[:min(len(text), 40)])
	}

	// One line that is longer than the limit is cut.
	after = []tools.TodoItem{planTodo("0", "A", "pending"), planTodo("1", strings.Repeat("y", 5000), "pending")}
	text, _ = planMessage(before, after)
	if want := "➕ " + strings.Repeat("y", statusTextLimit-2); text != want {
		t.Fatalf("long line: len %d", UTF16Len(text))
	}
}
