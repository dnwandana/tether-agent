package gateway

import (
	"testing"

	"github.com/dnwandana/tether-agent/internal/sessions"
)

func TestScopeFor(t *testing.T) {
	for _, c := range []struct{ source, chat, thread, want string }{
		{"cli", "", "", "cli"},
		{"cli", "5", "9", "cli"},
		{"telegram", "5", "", "telegram:5:0"},
		{"telegram", "-100123", "1", "telegram:-100123:1"}, // the General topic
		{"telegram", "-100123", "17585", "telegram:-100123:17585"},
	} {
		if got := ScopeFor(c.source, c.chat, c.thread); got != c.want {
			t.Errorf("ScopeFor(%q, %q, %q) = %q, want %q", c.source, c.chat, c.thread, got, c.want)
		}
	}
}

func TestModeScope(t *testing.T) {
	for _, c := range []struct {
		s    *sessions.Session
		want string
	}{
		{nil, ""},
		{&sessions.Session{Source: "cli", Key: "cli:t"}, "cli"},
		{&sessions.Session{Source: "telegram", ChatID: "5"}, "telegram:5:0"},
		// A per-user group session and a topic session of the same topic share one scope.
		{&sessions.Session{Source: "telegram", ChatID: "-100", ThreadID: "7", UserID: "42"}, "telegram:-100:7"},
		{&sessions.Session{Source: "telegram", ChatID: "-100", ThreadID: "7"}, "telegram:-100:7"},
		// A cron run has no scope.
		{&sessions.Session{Source: "cron", Kind: "cron", Key: "cron:j1"}, ""},
		{&sessions.Session{Source: "telegram"}, ""},
	} {
		if got := ModeScope(c.s); got != c.want {
			t.Errorf("ModeScope(%+v) = %q, want %q", c.s, got, c.want)
		}
	}
}
