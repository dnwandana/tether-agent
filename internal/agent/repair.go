package agent

import (
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// missingToolResult is the content of a result that repairToolCalls adds.
const missingToolResult = "[The gateway stopped before this tool call returned a result.]"

// repairToolCalls returns the history with a synthetic result for each tool call that has
// none. A hard kill can leave a call without a result, and both providers reject such a
// history. Hermes repairs its transcript in the same way. The synthetic message has id 0,
// and the function does not change the input or the store. It returns msgs when nothing is
// missing.
func repairToolCalls(msgs []sessions.StoredMessage) []sessions.StoredMessage {
	var out []sessions.StoredMessage
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		var missing []provider.ToolCall
		end := i + 1
		if m.Msg.Role == provider.RoleAssistant && len(m.Msg.ToolCalls) > 0 {
			have := map[string]bool{}
			for end < len(msgs) && msgs[end].Msg.Role == provider.RoleTool {
				have[msgs[end].Msg.ToolCallID] = true
				end++
			}
			for _, tc := range m.Msg.ToolCalls {
				if !have[tc.ID] {
					missing = append(missing, tc)
				}
			}
		}
		if len(missing) > 0 && out == nil {
			out = make([]sessions.StoredMessage, i, len(msgs)+len(missing))
			copy(out, msgs[:i])
		}
		if out != nil {
			out = append(out, msgs[i:end]...)
			for _, tc := range missing {
				out = append(out, sessions.StoredMessage{Msg: provider.Message{
					Role: provider.RoleTool, ToolCallID: tc.ID, ToolName: tc.Name, Content: missingToolResult,
				}})
			}
		}
		i = end - 1
	}
	if out == nil {
		return msgs
	}
	return out
}
