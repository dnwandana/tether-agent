package telegram

import (
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
)

func dmFrom(user string) Normalized {
	return Normalized{Kind: KindDM, ChatID: user, UserID: user}
}

func groupFrom(chat, user string) Normalized {
	return Normalized{Kind: KindGroup, ChatID: chat, UserID: user}
}

var notPaired = func(string) bool { return false }

func TestAuthOrder(t *testing.T) {
	c := config.Default().Telegram
	if Authorize(c, dmFrom("5"), notPaired) != AuthPair {
		t.Fatal("no allowlists → pair in DM")
	}
	if Authorize(c, groupFrom("-100", "5"), notPaired) != AuthIgnore {
		t.Fatal("no allowlists → deny in group")
	}
	c.AllowedUsers = []string{"5"}
	if Authorize(c, dmFrom("5"), notPaired) != AuthAllow {
		t.Fatal("allowlisted")
	}
	if Authorize(c, groupFrom("-100", "5"), notPaired) != AuthAllow {
		t.Fatal("allowed_users works in groups too")
	}
	if Authorize(c, dmFrom("6"), notPaired) != AuthIgnore {
		t.Fatal("allowlist set → unknown ignored")
	}
	if Authorize(c, groupFrom("-100", "6"), notPaired) != AuthIgnore {
		t.Fatal("groups never pair")
	}
	c.GroupAllowedChats = []string{"*"}
	if Authorize(c, groupFrom("-100", "6"), notPaired) != AuthAllow {
		t.Fatal("* allows any group")
	}
	if Authorize(c, dmFrom("6"), notPaired) != AuthIgnore {
		t.Fatal("group_allowed_chats does not open DMs")
	}
}

func TestGroupAllowedChatsWithoutUser(t *testing.T) {
	c := config.Default().Telegram
	c.GroupAllowedChats = []string{"-100"}
	// An anonymous admin post has no sender id. The chat allowlist still
	// applies (Hermes).
	if Authorize(c, groupFrom("-100", ""), notPaired) != AuthAllow {
		t.Fatal("chat allowlist without a sender")
	}
	if Authorize(c, groupFrom("-200", ""), notPaired) != AuthIgnore {
		t.Fatal("other chat")
	}
	c.AllowAllUsers = true
	if Authorize(c, groupFrom("-200", ""), notPaired) != AuthIgnore {
		t.Fatal("no sender id is denied after the chat check")
	}
}

func TestPairApprovedAllowed(t *testing.T) {
	c := config.Default().Telegram
	c.AllowedUsers = []string{"5"}
	approved := func(id string) bool { return id == "6" }
	if Authorize(c, dmFrom("6"), approved) != AuthAllow {
		t.Fatal("pair-approved user in DM")
	}
	if Authorize(c, groupFrom("-100", "6"), approved) != AuthAllow {
		t.Fatal("pair-approved user in group")
	}
	if Authorize(c, dmFrom("7"), approved) != AuthIgnore {
		t.Fatal("other user")
	}
	if Authorize(c, dmFrom("6"), nil) != AuthIgnore {
		t.Fatal("nil pairing check")
	}
}

func TestAllowAllUsers(t *testing.T) {
	c := config.Default().Telegram
	c.AllowAllUsers = true
	if Authorize(c, dmFrom("77"), notPaired) != AuthAllow || Authorize(c, groupFrom("-1", "78"), notPaired) != AuthAllow {
		t.Fatal("allow_all_users")
	}
}

func TestExplicitDMBehavior(t *testing.T) {
	c := config.Default().Telegram
	c.AllowedUsers = []string{"5"}
	c.UnauthorizedDMBehavior = "pair"
	if Authorize(c, dmFrom("6"), notPaired) != AuthPair {
		t.Fatal("explicit pair with allowlist")
	}
	if Authorize(c, groupFrom("-100", "6"), notPaired) != AuthIgnore {
		t.Fatal("groups never pair, even with explicit pair")
	}
	c = config.Default().Telegram
	c.UnauthorizedDMBehavior = "ignore"
	if Authorize(c, dmFrom("6"), notPaired) != AuthIgnore {
		t.Fatal("explicit ignore with no allowlist")
	}
	// Any allowlist, also a group one, changes the DM default to ignore.
	c = config.Default().Telegram
	c.GroupAllowedUsers = []string{"9"}
	if Authorize(c, dmFrom("6"), notPaired) != AuthIgnore {
		t.Fatal("group allowlist → ignore in DM")
	}
}

func TestGroupAllowedUsers(t *testing.T) {
	c := config.Default().Telegram
	c.GroupAllowedUsers = []string{"6"}
	if Authorize(c, groupFrom("-100", "6"), notPaired) != AuthAllow || Authorize(c, groupFrom("-200", "6"), notPaired) != AuthAllow {
		t.Fatal("group_allowed_users in any group")
	}
	if Authorize(c, dmFrom("6"), notPaired) != AuthIgnore {
		t.Fatal("group_allowed_users does not open DMs")
	}
	c.GroupAllowedUsers = []string{"*"}
	if Authorize(c, groupFrom("-100", "8"), notPaired) != AuthAllow {
		t.Fatal("* in group_allowed_users")
	}
	c = config.Default().Telegram
	c.AllowedUsers = []string{" 5 "}
	if Authorize(c, dmFrom("5"), notPaired) != AuthAllow {
		t.Fatal("ids are trimmed")
	}
}
