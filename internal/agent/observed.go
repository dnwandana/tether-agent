package agent

import (
	"strings"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// ObservedHeader starts the context-only block of observed group messages (Hermes
// _OBSERVED_GROUP_CONTEXT_HEADER).
const ObservedHeader = "[Observed Telegram group context - context only, not requests]"

// CurrentHeader comes after the observed block, before the text of the addressed message
// (Hermes _CURRENT_ADDRESSED_MESSAGE_HEADER).
const CurrentHeader = "[Current addressed message - answer only this unless it explicitly asks you to use the observed context]"

// isObserved reports whether m is an observed group message: a user message that the
// agent did not answer.
func isObserved(m sessions.StoredMessage) bool {
	return m.Meta.Observed && m.Msg.Role == provider.RoleUser
}

// withObserved returns the replay of view (Hermes _build_gateway_agent_history and
// _wrap_current_message_with_observed_context). The observed messages do not replay as
// user turns, because the model must not take old group talk as open requests. The
// observed messages before the message with id currentID go into one context-only block
// at the start of that message. The block is only in the request; the store keeps the
// message without it. Observed messages after currentID go into the block of the next
// turn.
func withObserved(view []sessions.StoredMessage, currentID int64) []sessions.StoredMessage {
	var observed []string
	out := make([]sessions.StoredMessage, 0, len(view))
	for _, m := range view {
		if !isObserved(m) {
			out = append(out, m)
			continue
		}
		if s := strings.TrimSpace(m.Msg.Content); s != "" && m.ID < currentID {
			observed = append(observed, s)
		}
	}
	if len(observed) == 0 {
		return out
	}
	prefix := ObservedHeader + "\n" + strings.Join(observed, "\n") + "\n\n" + CurrentHeader + "\n"
	for i := range out {
		if out[i].ID == currentID {
			out[i].Msg.Content = prefix + out[i].Msg.Content
			break
		}
	}
	return out
}

// lastAddressed returns the id of the last stored user message that is not observed, or 0.
func lastAddressed(view []sessions.StoredMessage) int64 {
	for i := len(view) - 1; i >= 0; i-- {
		m := view[i]
		if m.ID != 0 && m.Msg.Role == provider.RoleUser && !m.Meta.Observed {
			return m.ID
		}
	}
	return 0
}
