package ctxmgr

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// plainConversation returns n messages that alternate user and assistant, with no tool calls.
func plainConversation(n int) []provider.Message {
	msgs := make([]provider.Message, n)
	for i := range msgs {
		role := provider.RoleUser
		if i%2 == 1 {
			role = provider.RoleAssistant
		}
		msgs[i] = provider.Message{Role: role, Content: fmt.Sprint("m", i)}
	}
	return msgs
}

// withToolPairAt replaces msgs[at] with an assistant message that has n tool calls, and
// replaces the next n messages with the tool results.
func withToolPairAt(msgs []provider.Message, at, n int) []provider.Message {
	calls := make([]provider.ToolCall, n)
	for i := range calls {
		calls[i] = provider.ToolCall{ID: fmt.Sprint("c", at, "_", i), Name: "terminal", Args: "{}"}
	}
	msgs[at] = provider.Message{Role: provider.RoleAssistant, ToolCalls: calls}
	for i := range n {
		msgs[at+1+i] = provider.Message{Role: provider.RoleTool, ToolCallID: calls[i].ID, ToolName: "terminal", Content: "ok"}
	}
	return msgs
}

func TestProtectsFirst3Last20(t *testing.T) {
	msgs := plainConversation(60)
	s, e, ok := SelectRange(msgs, 3, 20)
	if !ok || s != 3 || e != 40 {
		t.Fatal(s, e)
	}
}

func TestStartBorderNotInsideToolPair(t *testing.T) {
	// index 2 = assistant with 2 tool calls, 3..4 = tool results
	msgs := withToolPairAt(plainConversation(60), 2, 2)
	s, _, _ := SelectRange(msgs, 3, 20)
	if s != 5 {
		t.Fatal("start must move past the whole pair", s)
	}
}

func TestEndBorderNotInsideToolPair(t *testing.T) {
	// The pair at index 39 (assistant) + 40,41 (tool) would be split by end=40.
	msgs := withToolPairAt(plainConversation(60), 39, 2)
	_, e, _ := SelectRange(msgs, 3, 20)
	if e != 39 {
		t.Fatal("end must move before the assistant", e)
	}
}

func TestEndBorderAfterToolGroup(t *testing.T) {
	// The pair at 39..41 ends just before end=42. Hermes pulls the border to the assistant,
	// so the tail starts with the whole group.
	msgs := withToolPairAt(plainConversation(62), 39, 2)
	_, e, _ := SelectRange(msgs, 3, 20)
	if e != 39 {
		t.Fatal("end must move before the assistant", e)
	}
}

func TestEndBorderAfterAssistantWithCalls(t *testing.T) {
	// The assistant at 39 is the last summarized message, and its results start the tail.
	msgs := withToolPairAt(plainConversation(60), 39, 1)
	_, e, _ := SelectRange(msgs, 3, 20)
	if e != 39 {
		t.Fatal(e)
	}
}

func TestTooShortNoCompaction(t *testing.T) {
	if _, _, ok := SelectRange(plainConversation(24), 3, 20); ok {
		t.Fatal()
	}
	if _, _, ok := SelectRange(plainConversation(25), 3, 20); !ok {
		t.Fatal("2 messages between the borders are enough")
	}
	if _, _, ok := SelectRange(nil, 3, 20); ok {
		t.Fatal()
	}
	if _, _, ok := SelectRange(plainConversation(10), 30, 30); ok {
		t.Fatal()
	}
}

func TestToolGroupsFillTheMiddle(t *testing.T) {
	// The only messages between the borders form a tool group, so the borders meet.
	msgs := withToolPairAt(plainConversation(26), 3, 2)
	if s, e, ok := SelectRange(msgs, 3, 20); ok {
		t.Fatal(s, e)
	}
}

// TestRangeKeepsPairsWhole checks on random conversations that no border separates a
// tool result from its assistant message.
func TestRangeKeepsPairsWhole(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for iter := range 2000 {
		var msgs []provider.Message
		n := 5 + r.IntN(80)
		for len(msgs) < n {
			if r.IntN(3) == 0 {
				k := 1 + r.IntN(4)
				msgs = append(msgs, make([]provider.Message, k+1)...)
				withToolPairAt(msgs, len(msgs)-k-1, k)
				continue
			}
			msgs = append(msgs, provider.Message{Role: provider.RoleUser, Content: "u"})
		}
		first, last := r.IntN(6), r.IntN(25)
		s, e, ok := SelectRange(msgs, first, last)
		if !ok {
			continue
		}
		if s < first || e > len(msgs)-last || e-s < 2 {
			t.Fatal(iter, s, e, len(msgs))
		}
		owner := func(i int) int { // index of the assistant that owns the tool result i
			for i >= 0 && msgs[i].Role == provider.RoleTool {
				i--
			}
			return i
		}
		for _, b := range []int{s, e} {
			if b < len(msgs) && msgs[b].Role == provider.RoleTool && owner(b) < b {
				t.Fatalf("iter %d: border %d splits a tool group (s=%d e=%d)", iter, b, s, e)
			}
		}
		if msgs[e-1].Role == provider.RoleAssistant && len(msgs[e-1].ToolCalls) > 0 {
			t.Fatalf("iter %d: assistant with calls at e-1=%d", iter, e-1)
		}
	}
}
