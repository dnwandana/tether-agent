package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/dashboard"
	"github.com/dnwandana/tether-agent/internal/db"
)

// writeDashConfig writes a config.yaml with a primary provider and the yaml text extra.
func writeDashConfig(t *testing.T, dir, extra string) {
	t.Helper()
	cfg := "providers:\n  primary:\n    format: openai\n    base_url: http://127.0.0.1:1\n    api_key: k\n    model: m\n" +
		"pricing:\n  sources: []\n" + extra
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

// withDashTerminal makes stdioIsTerminal return v for the test.
func withDashTerminal(t *testing.T, v bool) {
	t.Helper()
	old := stdioIsTerminal
	stdioIsTerminal = func() bool { return v }
	t.Cleanup(func() { stdioIsTerminal = old })
}

// dashStore opens the state.db of dir.
func dashStore(t *testing.T, dir string) *dashboard.Store {
	t.Helper()
	d, err := db.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return dashboard.NewStore(d)
}

const codeRE = `[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}`

func TestDashboardLinkNeedsTerminal(t *testing.T) {
	withDashTerminal(t, false)
	dir := t.TempDir()
	writeDashConfig(t, dir, "dashboard:\n  enabled: true\n")
	var out, errb bytes.Buffer
	if c := run([]string{"dashboard", "link", "--data-dir", dir}, &out, &errb); c != 1 ||
		errb.String() != "dashboard link: run this command in a terminal on the host.\n" || out.Len() != 0 {
		t.Fatalf("%d %q %q", c, out.String(), errb.String())
	}
	if c := run([]string{"dashboard", "signout-all", "--data-dir", dir}, &out, &errb); c != 1 ||
		!strings.HasSuffix(errb.String(), "dashboard signout-all: run this command in a terminal on the host.\n") {
		t.Fatalf("%d %q", c, errb.String())
	}
}

func TestDashboardLinkOff(t *testing.T) {
	withDashTerminal(t, true)
	dir := t.TempDir()
	writeDashConfig(t, dir, "")
	var out, errb bytes.Buffer
	if c := run([]string{"dashboard", "link", "--data-dir", dir}, &out, &errb); c != 1 ||
		errb.String() != "dashboard: off. Set dashboard.enabled: true and restart the gateway.\n" {
		t.Fatalf("%d %q", c, errb.String())
	}
	writeDashConfig(t, dir, "dashboard:\n  enabled: true\n  session_hours: 0\n")
	errb.Reset()
	if c := run([]string{"dashboard", "link", "--data-dir", dir}, &out, &errb); c != 1 ||
		errb.String() != "dashboard link: dashboard.session_hours must be from 1 to 720, not 0\n" {
		t.Fatalf("%d %q", c, errb.String())
	}
}

func TestDashboardLinkPrintsAWorkingCode(t *testing.T) {
	withDashTerminal(t, true)
	for _, c := range []struct{ name, yaml, prefix string }{
		{"public url", "dashboard:\n  enabled: true\n  public_url: https://agent.example.com/\n", "https://agent.example.com/#code="},
		{"tls", "dashboard:\n  enabled: true\n  listen: 10.8.0.1:8787\n  tls_cert: /c.pem\n  tls_key: /k.pem\n", "https://10.8.0.1:8787/#code="},
		{"loopback", "dashboard:\n  enabled: true\n", "http://127.0.0.1:8787/#code="},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeDashConfig(t, dir, c.yaml)
			var out, errb bytes.Buffer
			if code := run([]string{"dashboard", "link", "--data-dir", dir}, &out, &errb); code != 0 {
				t.Fatal(code, errb.String())
			}
			m := regexp.MustCompile(`^` + regexp.QuoteMeta(c.prefix) + `(` + codeRE + `)\nThe code works once and expires in 5 minutes\.\n$`).
				FindStringSubmatch(out.String())
			if m == nil {
				t.Fatalf("%q", out.String())
			}
			s := dashStore(t, dir)
			if err := s.ConsumeCode(context.Background(), m[1], time.Now()); err != nil {
				t.Fatal("the printed code must work:", err)
			}
		})
	}
}

func TestDashboardLinkWildcard(t *testing.T) {
	withDashTerminal(t, true)
	dir := t.TempDir()
	writeDashConfig(t, dir, "dashboard:\n  enabled: true\n  listen: 0.0.0.0:8787\n  allowed_hosts: [agent.lan]\n")
	var out, errb bytes.Buffer
	if c := run([]string{"dashboard", "link", "--data-dir", dir}, &out, &errb); c != 0 {
		t.Fatal(c, errb.String())
	}
	want := `^/#code=` + codeRE + "\n" +
		regexp.QuoteMeta("dashboard.listen is a wildcard address. Put the address that your browser uses in front of this path.") +
		"\nThe code works once and expires in 5 minutes\\.\n$"
	if !regexp.MustCompile(want).MatchString(out.String()) {
		t.Fatalf("%q", out.String())
	}
}

func TestDashboardDevicesAndSignoutAll(t *testing.T) {
	dir := dataDirWithDB(t)
	s := dashStore(t, dir)
	ctx := context.Background()
	for _, label := range []string{"Chrome on Android", "Firefox on Linux"} {
		if _, _, _, err := s.CreateSession(ctx, label, "ua", "10.8.0.5", time.Hour, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	withDashTerminal(t, false)
	var out, errb bytes.Buffer
	if c := run([]string{"dashboard", "devices", "--data-dir", dir}, &out, &errb); c != 0 {
		t.Fatal(c, errb.String())
	}
	for _, want := range []string{"DEVICE", "IP", "LAST SEEN", "EXPIRES", "Chrome on Android", "Firefox on Linux", "10.8.0.5"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("%q misses %q", out.String(), want)
		}
	}
	withDashTerminal(t, true)
	out.Reset()
	if c := run([]string{"dashboard", "signout-all", "--data-dir", dir}, &out, &errb); c != 0 ||
		out.String() != "Ended 2 device sessions. Each device must sign in again.\n" {
		t.Fatalf("%d %q %q", c, out.String(), errb.String())
	}
	out.Reset()
	if c := run([]string{"dashboard", "devices", "--data-dir", dir}, &out, &errb); c != 0 ||
		out.String() != "No device is signed in.\n" {
		t.Fatalf("%d %q", c, out.String())
	}
}

func TestDashboardBadUsage(t *testing.T) {
	var out, errb bytes.Buffer
	missing := filepath.Join(t.TempDir(), "none")
	if run([]string{"dashboard"}, &out, &errb) != 2 || run([]string{"dashboard", "frob"}, &out, &errb) != 2 ||
		run([]string{"dashboard", "link", "x"}, &out, &errb) != 2 {
		t.Fatal(errb.String())
	}
	errb.Reset()
	if c := run([]string{"dashboard", "devices", "--data-dir", missing}, &out, &errb); c != 1 || !strings.Contains(errb.String(), "state.db") {
		t.Fatal(c, errb.String())
	}
}
