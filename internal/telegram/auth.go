package telegram

import (
	"slices"

	"github.com/dnwandana/tether-agent/internal/config"
)

// AuthAction is the result of Authorize.
type AuthAction string

// The authorization results.
const (
	AuthAllow  AuthAction = "allow"  // answer the message
	AuthIgnore AuthAction = "ignore" // drop the message with no reply
	AuthPair   AuthAction = "pair"   // reply with a pairing code (DM only)
)

// Authorize decides if the sender of a message may use the bot (Hermes
// _is_user_authorized). The checks run in this order:
//
//  1. group_allowed_chats for a group message ("*" allows each group)
//  2. allow_all_users
//  3. the users that pairing approved
//  4. allowed_users, and group_allowed_users in a group
//
// With no allowlist and allow_all_users off, nobody is allowed. An unknown
// group sender is ignored. An unknown DM sender gets unauthorized_dm_behavior.
// When that is empty, the default is "ignore" if any allowlist is set, else
// "pair". isPairApproved can be nil.
func Authorize(cfg config.TelegramConfig, n Normalized, isPairApproved func(userID string) bool) AuthAction {
	if authorized(cfg, n, isPairApproved) {
		return AuthAllow
	}
	if n.Kind != KindDM {
		return AuthIgnore
	}
	switch cfg.UnauthorizedDMBehavior {
	case "pair":
		return AuthPair
	case "":
		if anyAllowlist(cfg) {
			return AuthIgnore
		}
		return AuthPair
	default:
		return AuthIgnore
	}
}

func authorized(cfg config.TelegramConfig, n Normalized, isPairApproved func(string) bool) bool {
	isGroup := n.Kind == KindGroup
	// The chat allowlist also applies to a message with no sender id, for
	// example an anonymous admin post.
	if isGroup && n.ChatID != "" {
		chats := trimmed(cfg.GroupAllowedChats)
		if slices.Contains(chats, "*") || slices.Contains(chats, n.ChatID) {
			return true
		}
	}
	if n.UserID == "" {
		return false
	}
	if cfg.AllowAllUsers {
		return true
	}
	if isPairApproved != nil && isPairApproved(n.UserID) {
		return true
	}
	// group_allowed_users applies only in groups. It does not open DMs.
	ids := trimmed(cfg.AllowedUsers)
	if isGroup {
		ids = append(ids, trimmed(cfg.GroupAllowedUsers)...)
	}
	return slices.Contains(ids, "*") || slices.Contains(ids, n.UserID)
}

// anyAllowlist reports whether the config restricts access with a list.
func anyAllowlist(cfg config.TelegramConfig) bool {
	return len(trimmed(cfg.AllowedUsers)) > 0 ||
		len(trimmed(cfg.GroupAllowedUsers)) > 0 ||
		len(trimmed(cfg.GroupAllowedChats)) > 0
}
