package security

import "testing"

func TestIsPrivateTarget(t *testing.T) {
	owner := func(id int64) bool { return id == 42 }
	private := []string{" -1001234 "}
	for _, c := range []struct {
		name, source, chat string
		want               bool
	}{
		{"cli", "cli", "", true},
		{"local", "local", "", true},
		{"dashboard", "dashboard", "owner", true},
		{"dashboard with no chat", "dashboard", "", true},
		{"owner private chat", "telegram", "42", true},
		{"member private chat", "telegram", "77", false},
		{"group", "telegram", "-1009999", false},
		{"owner-only forum group in private_chats", "telegram", "-1001234", true},
		{"chat id that is not a number", "telegram", "abc", false},
		{"unknown source", "slack", "42", false},
	} {
		if got := IsPrivateTarget(c.source, c.chat, owner, private); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if IsPrivateTarget("telegram", "42", nil, nil) {
		t.Fatal("a nil ownerChat must count as false")
	}
}
