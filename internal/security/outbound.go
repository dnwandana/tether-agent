package security

import (
	"strconv"
	"strings"
)

// IsPrivateTarget reports whether a send to chatID of source stays with the owner: the CLI,
// the dashboard, a local save, a chat id of privateChats, or a private chat for which ownerChat is true.
// A nil ownerChat counts as false.
func IsPrivateTarget(source, chatID string, ownerChat func(int64) bool, privateChats []string) bool {
	switch source {
	case "cli", "local", "dashboard":
		return true
	case "telegram":
	default:
		return false
	}
	chatID = strings.TrimSpace(chatID)
	for _, c := range privateChats {
		if strings.TrimSpace(c) == chatID && chatID != "" {
			return true
		}
	}
	id, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil || id <= 0 || ownerChat == nil {
		return false
	}
	return ownerChat(id)
}
