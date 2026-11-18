package gateway

import "github.com/dnwandana/tether-agent/internal/sessions"

// ScopeFor returns the scope of a chat or a topic: "cli" for the CLI, else
// "telegram:<chatID>:<threadID>", with "0" for an empty threadID. All users of a group topic
// share one scope.
func ScopeFor(source, chatID, threadID string) string {
	if source == "cli" {
		return "cli"
	}
	if threadID == "" {
		threadID = "0"
	}
	return source + ":" + chatID + ":" + threadID
}

// ModeScope returns the scope of the chat or the topic of s. It reads Source, ChatID, and
// ThreadID, never the session key. A nil session, a session with no chat, and a cron run
// give "".
func ModeScope(s *sessions.Session) string {
	switch {
	case s == nil:
		return ""
	case s.Source == "cli":
		return "cli"
	case s.Source == "telegram" && s.ChatID != "":
		return ScopeFor(s.Source, s.ChatID, s.ThreadID)
	}
	return ""
}
