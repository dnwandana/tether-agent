package telegram

import "testing"

func TestSessionKeys(t *testing.T) {
	cases := []struct {
		kind, chat, thread, user string
		gpu, tpu                 bool
		want                     string
	}{
		{"dm", "42", "", "42", true, false, "telegram:dm:42"},
		{"dm", "42", "9", "42", true, false, "telegram:dm:42:9"},
		{"group", "-100", "", "7", true, false, "telegram:group:-100:7"},
		{"group", "-100", "", "7", false, false, "telegram:group:-100"},
		{"group", "-100", "17585", "7", true, false, "telegram:group:-100:17585"},
		{"group", "-100", "17585", "7", true, true, "telegram:group:-100:17585:7"},
		{"group", "-100", "1", "7", true, false, "telegram:group:-100:1"},
		// Hermes: thread_sessions_per_user only matters when
		// group_sessions_per_user is on.
		{"group", "-100", "17585", "7", false, true, "telegram:group:-100:17585"},
		// No sender id: one shared session for the chat.
		{"group", "-100", "", "", true, false, "telegram:group:-100"},
	}
	for _, c := range cases {
		if got := SessionKey(c.kind, c.chat, c.thread, c.user, c.gpu, c.tpu); got != c.want {
			t.Fatal(c, got)
		}
	}
}
