package telegram

import "strings"

// SessionKey returns the session key for a Telegram message. It
// has the shape of Hermes build_session_key, without the "agent:main:" prefix.
//
// A DM key holds the chat id and the thread id. A group key holds the chat id,
// the thread id, and the user id when per-user sessions apply. A topic is one
// shared session, unless threadPerUser is set.
func SessionKey(kind, chatID, threadID, userID string, groupPerUser, threadPerUser bool) string {
	parts := []string{"telegram", kind}
	if chatID != "" {
		parts = append(parts, chatID)
	}
	if threadID != "" {
		parts = append(parts, threadID)
	}
	if kind == "dm" {
		return strings.Join(parts, ":")
	}
	isolate := groupPerUser
	if threadID != "" && !threadPerUser {
		isolate = false
	}
	if isolate && userID != "" {
		parts = append(parts, userID)
	}
	return strings.Join(parts, ":")
}
