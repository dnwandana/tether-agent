package ctxmgr

import "github.com/dnwandana/tether-agent/internal/provider"

// msgOverhead is the number of characters that stand for the role and the JSON of one message.
const msgOverhead = 16

// charsPerToken is the ratio of the token estimate.
const charsPerToken = 4

// StoredView is one message of the model view with its stored id. A message that is not
// stored, for example a compaction summary, has id 0.
type StoredView struct {
	ID  int64
	Msg provider.Message
}

// EstimateChars returns the token estimate of msgs: the characters of the content and of
// the tool call arguments, plus msgOverhead for each message, divided by 4.
func EstimateChars(msgs []provider.Message) int {
	n := 0
	for _, m := range msgs {
		// Bytes, not runes: non-Latin text has more tokens per rune, like typical BPE ratios.
		n += len(m.Content) + msgOverhead
		for _, c := range m.ToolCalls {
			n += len(c.Args)
		}
	}
	return n / charsPerToken
}

// ViewSize returns the size of the model view in tokens: the prompt tokens that
// the provider gave for the last response, plus the estimate of each message whose id is
// greater than lastPromptAtMessage. With no provider count (lastPromptTokens == 0), it
// estimates the full view.
func ViewSize(lastPromptTokens int, lastPromptAtMessage int64, view []StoredView) int {
	if lastPromptTokens <= 0 {
		all := make([]provider.Message, len(view))
		for i, v := range view {
			all[i] = v.Msg
		}
		return EstimateChars(all)
	}
	var newer []provider.Message
	for _, v := range view {
		if v.ID > lastPromptAtMessage {
			newer = append(newer, v.Msg)
		}
	}
	return lastPromptTokens + EstimateChars(newer)
}

// OverThreshold reports whether size is above threshold × window.
func OverThreshold(size, window int, threshold float64) bool {
	return float64(size) > threshold*float64(window)
}
