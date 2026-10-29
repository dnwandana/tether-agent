package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/sessions"
)

func TestRestartReplies(t *testing.T) {
	const drain = 180 * time.Second
	for _, c := range []struct {
		st    RestartState
		drain time.Duration
		want  string
	}{
		{RestartState{Busy: 1}, drain, "⏳ **Restarting**\n1 turn(s) still run. The gateway waits for them, for up to 3 min, and then restarts."},
		{RestartState{Busy: 2}, drain, "⏳ **Restarting**\n2 turn(s) still run. The gateway waits for them, for up to 3 min, and then restarts."},
		{RestartState{Busy: 2}, 0, "⏳ **Restarting**\n2 turn(s) still run. The gateway stops them now and restarts."},
		{RestartState{}, drain, "♻ **Restarting**\nThe gateway restarts now."},
		{RestartState{}, 0, "♻ **Restarting**\nThe gateway restarts now."},
		{RestartState{Already: true}, drain, "⏳ **Restart in progress**\nThe gateway already stops. Wait for it."},
	} {
		cc, sent := interactiveCtx(t)
		var log []string
		cc.Send = func(r Reply) { *sent = append(*sent, r); log = append(log, "reply") }
		registerTestCommand(t, RestartCommand(func(context.Context, string, Target) (RestartState, func()) {
			if c.st.Already {
				return c.st, nil
			}
			return c.st, func() { log = append(log, "start") }
		}, c.drain))
		run(t, cc, "/restart")
		if got := lastSent(t, sent).Text; got != c.want {
			t.Fatalf("%+v: got %q", c.st, got)
		}
		want := "reply,start"
		if c.st.Already {
			want = "reply"
		}
		if strings.Join(log, ",") != want {
			t.Fatalf("%+v: order %v", c.st, log)
		}
	}
}

func TestRestartTargetFromSession(t *testing.T) {
	cc, _ := interactiveCtx(t)
	cc.Session = &sessions.Session{Source: "telegram", ChatID: "42", ThreadID: "7"}
	var key string
	var to Target
	registerTestCommand(t, RestartCommand(func(_ context.Context, k string, tg Target) (RestartState, func()) {
		key, to = k, tg
		return RestartState{Already: true}, nil
	}, time.Minute))
	run(t, cc, "/restart")
	if key != cc.Key || to != (Target{Source: "telegram", ChatID: "42", ThreadID: "7"}) {
		t.Fatal(key, to)
	}
}

func TestRestartNilSessionGivesZeroTarget(t *testing.T) {
	cc, _ := interactiveCtx(t)
	cc.Session = nil
	var to Target
	registerTestCommand(t, RestartCommand(func(_ context.Context, _ string, tg Target) (RestartState, func()) {
		to = tg
		return RestartState{Already: true}, nil
	}, time.Minute))
	run(t, cc, "/restart")
	if to != (Target{}) {
		t.Fatal(to)
	}
}

func TestRestartRunsWhileBusy(t *testing.T) {
	cmd := RestartCommand(nil, time.Minute)
	if !cmd.WhileBusy || cmd.Name != "restart" {
		t.Fatalf("%+v", cmd)
	}
}

func TestHelpHasRestart(t *testing.T) {
	i := strings.Index(helpTelegram, "**Setup**")
	if i < 0 || !strings.Contains(helpTelegram[i:], "/restart · restart the gateway") {
		t.Fatal(helpTelegram)
	}
}
