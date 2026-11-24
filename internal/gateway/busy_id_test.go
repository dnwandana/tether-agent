package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/sessions"
)

// busyDashboard starts a slow turn on the dashboard key and returns the gateway and the
// session of the running turn.
func busyDashboard(t *testing.T) (*Gateway, string) {
	t.Helper()
	g, fp := gatewayWithSlowProvider(t, 2*time.Second)
	f := &recFactory{}
	in := Inbound{Key: "dashboard:owner", Source: "dashboard", ChatID: "owner", SenderID: "owner",
		Text: "first", IsText: true, Owner: true}
	done := make(chan struct{})
	go func() { _ = g.Handle(context.Background(), in, f.new); close(done) }()
	waitFor(t, func() bool { _, ok := g.Running("dashboard:owner"); return ok })
	waitFor(t, func() bool { return len(fp.lastUserTexts()) == 1 })
	t.Cleanup(func() {
		if rs, ok := g.Running("dashboard:owner"); ok {
			rs.Cancel()
		}
		<-done
		g.Wait(5 * time.Second)
	})
	return g, g.mustRun(t, "dashboard:owner").SessionID
}

// dashCommand sends text as a dashboard command and returns the first reply text.
func dashCommand(t *testing.T, g *Gateway, text string) string {
	t.Helper()
	r := &replyRenderer{}
	in := Inbound{Key: "dashboard:owner", Source: "dashboard", ChatID: "owner", SenderID: "owner",
		Text: text, IsText: true, Owner: true}
	if err := g.Handle(context.Background(), in, func() Renderer { return r }); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.replies) > 0 {
		return r.replies[0].Text
	}
	if len(r.acks) > 0 {
		return r.acks[0]
	}
	t.Fatal("no reply to", text)
	return ""
}

const busyPrefix = "⏳ **Busy**\n"

// A rename and an archive of another dashboard session run during the turn.
func TestBusyRenameArchiveOtherSession(t *testing.T) {
	g, _ := busyDashboard(t)
	other, err := g.Deps.Store.Create(context.Background(), sessions.Session{Key: "dashboard:owner",
		Source: "dashboard", ChatID: "owner", Kind: "chat", Title: "Old notes"})
	if err != nil {
		t.Fatal(err)
	}
	if got := dashCommand(t, g, "/rename "+other.ID+" Trip budget"); got != `✏️ **Renamed to "Trip budget"**` {
		t.Fatalf("got:\n%s", got)
	}
	if got := dashCommand(t, g, "/archive "+other.ID); !strings.HasPrefix(got, `🗄 **Archived "Trip budget"**`) {
		t.Fatalf("got:\n%s", got)
	}
	if got := dashCommand(t, g, "/unarchive "+other.ID); got != `🗄 **Unarchived "Trip budget"**` {
		t.Fatalf("got:\n%s", got)
	}
}

// The session of the running turn, a command with no id, /fork, and /delete stay refused.
func TestBusyRefusesTheRunningSession(t *testing.T) {
	g, running := busyDashboard(t)
	other, _ := g.Deps.Store.Create(context.Background(), sessions.Session{Key: "dashboard:owner",
		Source: "dashboard", ChatID: "owner", Kind: "chat", Title: "Old notes"})
	for _, text := range []string{
		"/rename " + running + " New title",
		"/archive " + running,
		"/rename Plain title",
		"/archive",
		"/fork",
		"/delete " + other.ID,
	} {
		if got := dashCommand(t, g, text); !strings.HasPrefix(got, busyPrefix) {
			t.Errorf("%s: got:\n%s", text, got)
		}
	}
}

// Outside the dashboard, an id is not read, so the busy rule does not change.
func TestBusyIDOnlyOnDashboard(t *testing.T) {
	g, _, _, _ := busyGateway(t)
	other, _ := g.Deps.Store.Create(context.Background(), sessions.Session{Key: "k", Source: "telegram", Title: "Old"})
	r := &replyRenderer{}
	_ = g.Handle(context.Background(), inb("k", "/rename "+other.ID+" New"), func() Renderer { return r })
	if len(r.replies) != 1 || !strings.HasPrefix(r.replies[0].Text, busyPrefix) {
		t.Fatalf("%+v %q", r.replies, r.acks)
	}
}
