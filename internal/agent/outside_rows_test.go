package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/dnwandana/tether-agent/internal/ctxmgr"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// outsideTool marks source as outside text when source is not empty, and returns "ok".
type outsideTool struct{ name, set, source string }

func (o outsideTool) Name() string       { return o.name }
func (o outsideTool) Toolset() string    { return o.set }
func (o outsideTool) ParallelSafe() bool { return false }
func (o outsideTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{Name: o.name, Description: o.name, Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (o outsideTool) Run(_ context.Context, _ json.RawMessage, env *tools.Env) (string, error) {
	if o.source != "" {
		tools.MarkOutside(env, o.source, "")
	}
	return "ok", nil
}

// outsideRows returns the stored messages of the session with their outside flags.
func outsideRows(t *testing.T, a *Agent, sessID string) []sessions.StoredMessage {
	t.Helper()
	return messages(t, a, sessID)
}

func TestOutsideOnOutsideToolRow(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "web_fetch", `{}`)), text("done"))
	a, sess := newAgent(t, fp, newNamed("web_fetch", tools.SetWeb))
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	for _, m := range outsideRows(t, a, sess.ID) {
		want := []string(nil)
		if m.Msg.Role == provider.RoleTool {
			want = []string{"web_fetch"}
		}
		if !reflect.DeepEqual(m.Meta.Outside, want) {
			t.Fatal(m.Msg.Role, m.Meta.Outside)
		}
	}
}

func TestRoundSourcesOnLastToolRow(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "delegate_task", `{}`), tc("c2", "echo", `{"s":"x"}`)), text("done"))
	a, sess := newAgent(t, fp, outsideTool{name: "delegate_task", set: tools.SetDelegation, source: "subagent"}, echoTool{})
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, m := range outsideRows(t, a, sess.ID) {
		if m.Msg.Role == provider.RoleTool {
			got[m.Msg.ToolName] = m.Meta.Outside
		}
	}
	if got["delegate_task"] != nil || !reflect.DeepEqual(got["echo"], []string{"subagent"}) {
		t.Fatal(got)
	}
}

func TestTerminalSourceIsStored(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "terminal", `{}`)), text("done"))
	a, sess := newAgent(t, fp, outsideTool{name: "terminal", set: tools.SetTerminal, source: "terminal:curl"})
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	for _, m := range outsideRows(t, a, sess.ID) {
		if m.Msg.Role == provider.RoleTool && !reflect.DeepEqual(m.Meta.Outside, []string{"terminal:curl"}) {
			t.Fatal(m.Meta.Outside)
		}
	}
}

func TestOldSourceIsNotNewInARound(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "delegate_task", `{}`)), text("done"))
	a, sess := newAgent(t, fp, outsideTool{name: "delegate_task", set: tools.SetDelegation, source: "subagent"})
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: &recSink{}, Outside: []string{"subagent"}}); err != nil {
		t.Fatal(err)
	}
	for _, m := range outsideRows(t, a, sess.ID) {
		want := []string(nil)
		if m.Msg.Role == provider.RoleUser {
			want = []string{"subagent"}
		}
		if !reflect.DeepEqual(m.Meta.Outside, want) {
			t.Fatal(m.Msg.Role, m.Meta.Outside)
		}
	}
}

func TestUserRowGetsTurnOutside(t *testing.T) {
	fp := scripted(text("one"), text("two"))
	a, sess := newAgent(t, fp)
	a.queueOutside(sess.Key, tools.OutsideFacts{Sources: []string{"mcp_github_search"}})
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "a", Toolsets: all(), Sink: &recSink{}, Outside: []string{"web_fetch"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "b", Toolsets: all(), Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	var users [][]string
	for _, m := range outsideRows(t, a, sess.ID) {
		if m.Msg.Role == provider.RoleUser {
			users = append(users, m.Meta.Outside)
		}
	}
	if len(users) != 2 || !reflect.DeepEqual(users[0], []string{"mcp_github_search", "web_fetch"}) || users[1] != nil {
		t.Fatal(users)
	}
}

func TestViewKeepsOutside(t *testing.T) {
	a, sess := newAgent(t, scripted())
	seedHistory(t, a, sess.ID, 6)
	rows := messages(t, a, sess.ID)
	for i, src := range map[int]string{1: "web_fetch", 2: "mcp_x", 4: "terminal:curl"} {
		if _, err := a.DB.ExecContext(ctx, `UPDATE messages SET outside = ? WHERE id = ?`, src, rows[i].ID); err != nil {
			t.Fatal(err)
		}
	}
	flags := func(v []sessions.StoredMessage) [][]string {
		var out [][]string
		for _, m := range v {
			out = append(out, m.Meta.Outside)
		}
		return out
	}
	v, err := a.viewStored(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{nil, {"web_fetch"}, {"mcp_x"}, nil, {"terminal:curl"}, nil}
	if !reflect.DeepEqual(flags(v), want) {
		t.Fatal(flags(v))
	}
	if _, err := a.DB.ExecContext(ctx, `INSERT INTO compactions (session_id, from_message_id, upto_message_id, summary)
		VALUES (?, ?, ?, 'SUM')`, sess.ID, rows[1].ID, rows[2].ID); err != nil {
		t.Fatal(err)
	}
	v, err = a.viewStored(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	want = [][]string{nil, {"mcp_x", "web_fetch"}, nil, {"terminal:curl"}, nil}
	if v[1].Msg.Content != ctxmgr.SummaryPrefix+"SUM" || !reflect.DeepEqual(flags(v), want) {
		t.Fatalf("%q %v", v[1].Msg.Content, flags(v))
	}
}
