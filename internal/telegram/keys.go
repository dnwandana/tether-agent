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
	if isolatesUser(threadID, groupPerUser, threadPerUser) && userID != "" {
		parts = append(parts, userID)
	}
	return strings.Join(parts, ":")
}

// IsSharedKey reports whether the session key of a message holds no user part, so all
// users of the chat or the topic share one session. A DM key is never shared.
func IsSharedKey(kind, threadID, userID string, groupPerUser, threadPerUser bool) bool {
	if kind == "dm" {
		return false
	}
	return !(isolatesUser(threadID, groupPerUser, threadPerUser) && userID != "")
}

// isolatesUser reports whether a group key gets a user part. A topic is one shared
// session, unless threadPerUser is set.
func isolatesUser(threadID string, groupPerUser, threadPerUser bool) bool {
	if threadID != "" && !threadPerUser {
		return false
	}
	return groupPerUser
}
