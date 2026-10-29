package agent

import (
	"reflect"
	"testing"

	"github.com/dnwandana/tether-agent/internal/ctxmgr"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// stored wraps the messages as stored rows with the ids 1..n.
func stored(ms ...provider.Message) []sessions.StoredMessage {
	out := make([]sessions.StoredMessage, len(ms))
	for i, m := range ms {
		out[i] = sessions.StoredMessage{ID: int64(i + 1), Msg: m}
	}
	return out
}

func user(s string) provider.Message { return provider.Message{Role: provider.RoleUser, Content: s} }

func assistant(s string) provider.Message {
	return provider.Message{Role: provider.RoleAssistant, Content: s}
}

// asst returns an assistant message with one tool call for each id.
func asst(ids ...string) provider.Message {
	m := provider.Message{Role: provider.RoleAssistant}
	for _, id := range ids {
		m.ToolCalls = append(m.ToolCalls, provider.ToolCall{ID: id, Name: "terminal", Args: "{}"})
	}
	return m
}

func res(id string) provider.Message {
	return provider.Message{Role: provider.RoleTool, ToolCallID: id, ToolName: "terminal", Content: "ok"}
}

// wantSynthetic fails the test when m is not the synthetic result for id.
func wantSynthetic(t *testing.T, m sessions.StoredMessage, id string) {
	t.Helper()
	w := provider.Message{Role: provider.RoleTool, ToolCallID: id, ToolName: "terminal", Content: missingToolResult}
	if m.ID != 0 || !reflect.DeepEqual(m.Msg, w) {
		t.Fatalf("want the synthetic result for %s, got %+v", id, m)
	}
}

func TestRepairNoGap(t *testing.T) {
	in := stored(user("a"), asst("c1"), res("c1"), assistant("done"))
	got := repairToolCalls(in)
	if !reflect.DeepEqual(got, in) || &got[0] != &in[0] {
		t.Fatal(got)
	}
}

func TestRepairGapAtTheEnd(t *testing.T) {
	got := repairToolCalls(stored(user("a"), asst("c1", "c2")))
	if len(got) != 4 {
		t.Fatal(got)
	}
	wantSynthetic(t, got[2], "c1")
	wantSynthetic(t, got[3], "c2")
}

func TestRepairGapInTheMiddle(t *testing.T) {
	got := repairToolCalls(stored(user("a"), asst("c1"), user("[System note: x]"), assistant("ok")))
	if len(got) != 5 {
		t.Fatal(got)
	}
	wantSynthetic(t, got[2], "c1")
	if got[3].Msg.Content != "[System note: x]" || got[3].ID != 3 {
		t.Fatal(got[3])
	}
}

func TestRepairPartialResults(t *testing.T) {
	got := repairToolCalls(stored(asst("c1", "c2", "c3"), res("c1"), res("c3")))
	if len(got) != 4 || got[1].Msg.ToolCallID != "c1" || got[2].Msg.ToolCallID != "c3" {
		t.Fatal(got)
	}
	wantSynthetic(t, got[3], "c2")
}

func TestViewStoredRepairsWithCompaction(t *testing.T) {
	a, sess := newAgent(t, scripted())
	seedHistory(t, a, sess.ID, 4)
	if _, err := a.Store.Append(ctx, sess.ID, asst("c1"), sessions.MessageMeta{}); err != nil {
		t.Fatal(err)
	}
	before := messages(t, a, sess.ID)
	if _, err := a.DB.ExecContext(ctx, `INSERT INTO compactions (session_id, from_message_id, upto_message_id, summary)
		VALUES (?, ?, ?, 'SUM')`, sess.ID, before[0].ID, before[3].ID); err != nil {
		t.Fatal(err)
	}
	v, err := a.viewStored(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 3 || v[0].Msg.Content != ctxmgr.SummaryPrefix+"SUM" {
		t.Fatalf("%+v", v)
	}
	wantSynthetic(t, v[2], "c1")
	if after := messages(t, a, sess.ID); !reflect.DeepEqual(after, before) {
		t.Fatal("the stored rows changed")
	}
}
