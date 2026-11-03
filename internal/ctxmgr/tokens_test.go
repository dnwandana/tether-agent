package ctxmgr

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// msg returns a user message with n characters.
func msg(n int) provider.Message {
	return provider.Message{Role: provider.RoleUser, Content: strings.Repeat("a", n)}
}

// msgs returns the messages of view.
func msgs(view []StoredView) []provider.Message {
	out := make([]provider.Message, len(view))
	for i, v := range view {
		out[i] = v.Msg
	}
	return out
}

func TestEstimateCharsOver4(t *testing.T) {
	n := EstimateChars([]provider.Message{{Role: provider.RoleUser, Content: strings.Repeat("a", 400)}})
	if n != (400+16)/4 {
		t.Fatal(n)
	}
}

func TestEstimateCountsToolArgsAndBytes(t *testing.T) {
	m := provider.Message{Role: provider.RoleAssistant, Content: "é", ToolCalls: []provider.ToolCall{{Args: strings.Repeat("x", 10)}, {Args: "yy"}}}
	// "é" is 2 bytes.
	if n := EstimateChars([]provider.Message{m}); n != (2+10+2+16)/4 {
		t.Fatal(n)
	}
	if EstimateChars(nil) != 0 {
		t.Fatal()
	}
}

func TestViewSizeUsesProviderCountPlusNew(t *testing.T) {
	view := []StoredView{{1, msg(400)}, {2, msg(400)}, {3, msg(800)}}
	if got := ViewSize(5000, 2, 1000, view); got != 5000+(800+16)/4 {
		t.Fatal(got)
	}
	if got := ViewSize(0, 0, 0, view); got != EstimateChars(msgs(view)) {
		t.Fatal(got)
	}
	// With no provider count, the system prompt and the tool schemas count too.
	if got := ViewSize(0, 0, 1000, view); got != 1000+EstimateChars(msgs(view)) {
		t.Fatal(got)
	}
	// A summary message of a compaction has id 0. It is older than the provider count.
	view = append([]StoredView{{0, msg(4000)}}, view...)
	if got := ViewSize(5000, 2, 1000, view); got != 5000+(800+16)/4 {
		t.Fatal(got)
	}
}

func TestEstimateFixedCountsSystemAndSchemas(t *testing.T) {
	schemas := []provider.ToolSchema{{Name: "ab", Description: "cdef", Parameters: []byte(`{"x":1}`)}}
	if got := EstimateFixed(strings.Repeat("s", 400), schemas); got != (400+2+4+7)/4 {
		t.Fatal(got)
	}
	if EstimateFixed("", nil) != 0 {
		t.Fatal()
	}
}

func TestOverThreshold50(t *testing.T) {
	if OverThreshold(64000, 128000, 0.50) || !OverThreshold(64001, 128000, 0.50) {
		t.Fatal()
	}
}

func TestEstimateCountsReasoning(t *testing.T) {
	plain := provider.Message{Role: provider.RoleAssistant, Content: "abcd"}
	withText := plain
	withText.Reasoning = &provider.Reasoning{Text: strings.Repeat("x", 400)}
	withItems := plain
	withItems.Reasoning = &provider.Reasoning{Output: []json.RawMessage{json.RawMessage(strings.Repeat("y", 800))}}
	base := EstimateChars([]provider.Message{plain})
	if EstimateChars([]provider.Message{withText}) != base+100 {
		t.Fatal("text")
	}
	if EstimateChars([]provider.Message{withItems}) != base+200 {
		t.Fatal("items")
	}
}
