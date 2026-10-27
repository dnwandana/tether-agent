package ctxmgr

import (
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// SelectRange returns the part of msgs that compaction summarizes, as msgs[start:end].
// It keeps the first protectFirst and the last protectLast messages. It
// moves the borders so that no assistant message with ToolCalls is separated from its
// RoleTool results. ok is false when fewer than 2 messages remain between the borders.
func SelectRange(msgs []provider.Message, protectFirst, protectLast int) (start, end int, ok bool) {
	start, end = max(protectFirst, 0), len(msgs)-max(protectLast, 0)
	if end-start < 2 {
		return 0, 0, false
	}
	// A tool result at the start border belongs to an assistant in the head. Keep it there.
	for start < end && msgs[start].Role == provider.RoleTool {
		start++
	}
	for end > start {
		// A tool result at the end border belongs to an assistant before the border.
		if end < len(msgs) && msgs[end].Role == provider.RoleTool {
			end--
			continue
		}
		// An assistant just before the border has its results in the tail.
		if hasToolCalls(msgs[end-1]) {
			end--
			continue
		}
		// A tool group just before the border moves into the tail with its assistant (Hermes
		// _align_boundary_backward).
		c := end - 1
		for c >= start && msgs[c].Role == provider.RoleTool {
			c--
		}
		if c < end-1 && c >= start && hasToolCalls(msgs[c]) {
			end = c
			continue
		}
		break
	}
	if end-start < 2 {
		return 0, 0, false
	}
	return start, end, true
}

func hasToolCalls(m provider.Message) bool {
	return m.Role == provider.RoleAssistant && len(m.ToolCalls) > 0
}

// BuildView returns the model view: the stored messages, where one RoleUser message that
// starts with SummaryPrefix replaces the messages that the latest compaction covers.
func BuildView(stored []sessions.StoredMessage, compactions []Compaction) []provider.Message {
	v := BuildStoredView(stored, compactions)
	out := make([]provider.Message, len(v))
	for i, sv := range v {
		out[i] = sv.Msg
	}
	return out
}

// BuildStoredView is BuildView with the stored id of each message. The summary has id 0.
func BuildStoredView(stored []sessions.StoredMessage, compactions []Compaction) []StoredView {
	out := make([]StoredView, 0, len(stored)+1)
	if len(compactions) == 0 {
		for _, m := range stored {
			out = append(out, StoredView{ID: m.ID, Msg: m.Msg})
		}
		return out
	}
	c := compactions[len(compactions)-1]
	placed := false
	for _, m := range stored {
		if m.ID >= c.FromMessageID && m.ID <= c.UptoMessageID {
			continue
		}
		if m.ID > c.UptoMessageID && !placed {
			out = append(out, summaryView(c))
			placed = true
		}
		out = append(out, StoredView{ID: m.ID, Msg: m.Msg})
	}
	if !placed {
		out = append(out, summaryView(c))
	}
	return out
}

func summaryView(c Compaction) StoredView {
	return StoredView{Msg: provider.Message{Role: provider.RoleUser, Content: SummaryPrefix + c.Summary}}
}
