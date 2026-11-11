package telegram

import (
	"strings"
	"testing"
)

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

func TestIsSharedKey(t *testing.T) {
	for _, tc := range []struct {
		kind, thread, user          string
		groupPerUser, threadPerUser bool
		want                        bool
	}{
		{"dm", "", "5", true, true, false},
		{"dm", "3", "5", false, false, false},
		{"group", "", "5", true, false, false},
		{"group", "", "5", false, false, true},
		{"group", "", "", true, false, true},
		{"group", "17", "5", true, false, true},
		{"group", "17", "5", true, true, false},
		{"group", "17", "5", false, true, true},
	} {
		got := IsSharedKey(tc.kind, tc.thread, tc.user, tc.groupPerUser, tc.threadPerUser)
		key := SessionKey(tc.kind, "-100", tc.thread, tc.user, tc.groupPerUser, tc.threadPerUser)
		hasUser := tc.user != "" && strings.HasSuffix(key, ":"+tc.user)
		if got != tc.want || tc.kind != "dm" && got == hasUser {
			t.Errorf("%+v: got %v, key %s", tc, got, key)
		}
	}
}
