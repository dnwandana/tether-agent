package sessions

import (
	"context"
	"reflect"
	"testing"

	"github.com/dnwandana/tether-agent/internal/provider"
)

func TestLatestToolResultsPairsAndOrders(t *testing.T) {
	ctx := context.Background()
	st, sess := newStoreSession(t)
	add := func(m provider.Message) {
		t.Helper()
		if _, err := st.Append(ctx, sess.ID, m, MessageMeta{}); err != nil {
			t.Fatal(err)
		}
	}
	call := func(id, name string) provider.Message {
		return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: id, Name: name, Args: "{}"}}}
	}
	result := func(id, name, content string) provider.Message {
		return provider.Message{Role: provider.RoleTool, ToolCallID: id, ToolName: name, Content: content}
	}
	add(call("c1", "todo_list"))
	add(result("c1", "todo_list", "A"))
	add(result("c9", "todo_list", "F")) // a forged row: no call has the id c9
	add(call("c3", "read_file"))
	add(result("c3", "todo_list", "N")) // the call has another tool name
	add(call("c2", "todo_list"))
	add(result("c2", "todo_list", "B"))
	add(result("c2", "read_file", "R"))
	got, err := st.LatestToolResults(ctx, sess.ID, "todo_list", 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []ToolResultRow{{"B", true}, {"N", false}, {"F", false}, {"A", true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	if got, _ := st.LatestToolResults(ctx, sess.ID, "todo_list", 2); len(got) != 2 || got[0].Content != "B" {
		t.Fatalf("limit: %+v", got)
	}
	if got, err := st.LatestToolResults(ctx, sess.ID, "todo_list", 0); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	if got, err := st.LatestToolResults(ctx, "nope", "todo_list", 10); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
}
