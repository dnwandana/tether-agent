package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/dashboard"
	"github.com/dnwandana/tether-agent/internal/gateway"
)

// routesApp returns a gateway App with the dashboard added and its listener running on a
// free port. It returns the base URL and a signed-in GET function that gives the status code.
func routesApp(t *testing.T) (*App, string, func(path string) int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	a := gatewayApp(t, "", fmt.Sprintf("dashboard:\n  enabled: true\n  listen: %s\n", addr))
	ch, err := a.AddDashboard(ctx, DashboardFuncs{})
	if err != nil || ch == nil {
		t.Fatal(ch, err)
	}
	work, stopWork := context.WithCancel(ctx)
	ch.SetTurnContext(work)
	go func() { _ = ch.Run(work, a.Gateway()) }()
	t.Cleanup(func() {
		stopWork()
		_ = ch.Shutdown(ctx)
	})
	_, cookie, header, err := dashboard.NewStore(a.DB).CreateSession(ctx, "test", "ua", "127.0.0.1", time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + addr
	get := func(path string) int {
		t.Helper()
		var last error
		for range 50 {
			req, _ := http.NewRequest("GET", base+path, nil)
			req.AddCookie(&http.Cookie{Name: dashboard.CookieName, Value: cookie})
			req.Header.Set(dashboard.HeaderName, header)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				last = err
				time.Sleep(40 * time.Millisecond)
				continue
			}
			resp.Body.Close()
			return resp.StatusCode
		}
		t.Fatal("no answer:", last)
		return 0
	}
	return a, base, get
}

func TestDashboardRoutesRegistered(t *testing.T) {
	_, _, get := routesApp(t)
	for _, path := range []string{
		"/api/bootstrap", "/api/sessions", "/api/search?q=x", "/api/brief", "/api/jobs", "/api/held",
		"/api/goals", "/api/security", "/api/security/log", "/api/devices", "/api/skills",
		"/api/skills/catalog", "/api/memory", "/api/usage", "/api/usage/daily", "/api/models",
		"/api/doctor", "/api/mcp", "/api/runtime", "/api/config", "/api/backup", "/api/pairing", "/api/setup",
	} {
		code := get(path)
		if code == http.StatusNotFound || code == http.StatusUnauthorized {
			t.Errorf("GET %s: status %d", path, code)
		}
	}
}

func TestDashboardFilesLinkMediaTags(t *testing.T) {
	a := dashApp(t)
	dir := filepath.Join(a.DataDir, "cache", "media")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(p, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := a.Dashboard.Send(ctx, gateway.Target{Source: "dashboard", ChatID: dashboard.OwnerChatID},
		gateway.Outbound{Text: "done", Files: []string{p}})
	if err != nil {
		t.Fatal(err)
	}
	n := a.Dashboard.Hub().Notices()
	if len(n) == 0 || !strings.Contains(n[len(n)-1].Text, "(/api/files/") {
		t.Fatalf("%+v", n)
	}
}

func TestBootstrapCommandRows(t *testing.T) {
	a := dashApp(t)
	rows := a.bootCommands()
	have := map[string]bool{}
	for i, r := range rows {
		if !strings.HasPrefix(r.Name, "/") {
			t.Fatalf("%q has no slash", r.Name)
		}
		if i > 0 && rows[i-1].Name >= r.Name {
			t.Fatalf("not sorted: %q then %q", rows[i-1].Name, r.Name)
		}
		have[r.Name] = true
	}
	for _, want := range []string{"/usage", "/approve", "/deny"} {
		if !have[want] {
			t.Errorf("missing %s", want)
		}
	}
}

func TestBootCommandsLeaveOutNotice(t *testing.T) {
	a := dashApp(t)
	prev, had := gateway.Commands()["notice"]
	gateway.Register(gateway.Command{Name: "notice", Help: "Answer a change notice",
		Run: func(context.Context, *gateway.CommandCtx, string) error { return nil }})
	t.Cleanup(func() {
		if had {
			gateway.Register(prev)
		} else {
			gateway.Unregister("notice")
		}
	})
	usage := false
	for _, r := range a.bootCommands() {
		if r.Name == "/notice" {
			t.Fatal("/notice is in the palette")
		}
		if r.Name == "/usage" {
			usage = true
		}
	}
	if !usage {
		t.Fatal("/usage is missing")
	}
}
