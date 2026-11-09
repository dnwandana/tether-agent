package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
)

// fakeMethods returns the methods in the log of the fake agent, and the cwd of session/new.
func fakeMethods(t *testing.T, logPath string) (methods []string, cwd string) {
	t.Helper()
	for _, e := range readFakeLog(t, logPath) {
		methods = append(methods, e.Method)
		if e.Method == "session/new" {
			var p struct {
				Cwd string `json:"cwd"`
			}
			if err := json.Unmarshal(e.Params, &p); err != nil {
				t.Fatal(err)
			}
			cwd = p.Cwd
		}
	}
	return methods, cwd
}

func TestProbeStartsAndClosesSession(t *testing.T) {
	a, logPath := fakeAgentConfig(t, fakeScript{Close: true, SessionID: "s-probe"})
	r, err := Probe(context.Background(), "claude-code", a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Program != a.Command[0] || r.ProtocolVersion != 1 || !r.SessionOK || r.NotLoggedIn || r.NodeVersion != "" {
		t.Fatalf("%+v", r)
	}
	methods, cwd := fakeMethods(t, logPath)
	for _, m := range []string{"initialize", "session/new", "session/close"} {
		if !slices.Contains(methods, m) {
			t.Fatalf("no %s in %v", m, methods)
		}
	}
	if slices.Contains(methods, "session/prompt") {
		t.Fatal("Probe must send no prompt")
	}
	if !filepath.IsAbs(cwd) {
		t.Fatalf("session/new must get an absolute temp folder: %q", cwd)
	}
	if _, err := os.Stat(cwd); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("Probe must remove the temp folder:", err)
	}
}

func TestProbeSkipsCloseWithoutCapability(t *testing.T) {
	a, logPath := fakeAgentConfig(t, fakeScript{})
	r, err := Probe(context.Background(), "claude-code", a, nil)
	if err != nil || !r.SessionOK {
		t.Fatal(r, err)
	}
	if methods, _ := fakeMethods(t, logPath); slices.Contains(methods, "session/close") {
		t.Fatal("no session/close when the agent does not offer it")
	}
}

func TestProbeNotLoggedIn(t *testing.T) {
	a, _ := fakeAgentConfig(t, fakeScript{AuthRequired: true})
	r, err := Probe(context.Background(), "claude-code", a, nil)
	if _, ok := errors.AsType[*NotLoggedInError](err); !ok {
		t.Fatalf("want *NotLoggedInError, got %v", err)
	}
	if !r.NotLoggedIn || r.SessionOK || r.ProtocolVersion != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestProbeMissingProgram(t *testing.T) {
	a := config.ACPAgent{Enabled: true, Command: []string{"tether-no-such-acp-agent"},
		Env: map[string]string{"PATH": t.TempDir()}}
	r, err := Probe(context.Background(), "claude-code", a, nil)
	if err == nil || r.Program != "" || r.ProtocolVersion != 0 {
		t.Fatal(r, err)
	}
}

// A node shim: the first line names node, so Probe runs node --version with the same env.
// The fake node prints a version, or it runs the fake agent.
func TestProbeReadsNodeVersion(t *testing.T) {
	a, _ := fakeAgentConfig(t, fakeScript{})
	dir := t.TempDir()
	node := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo v20.11.1; exit 0; fi\nexec '" + a.Command[0] + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "node"), []byte(node), 0o755); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(dir, "claude-agent-acp")
	if err := os.WriteFile(shim, []byte("#!/usr/bin/env node\n// fake shim\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The folder of an absolute command comes first in PATH, so env finds the fake node.
	a.Command = []string{shim}
	r, err := Probe(context.Background(), "claude-code", a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.NodeVersion != "v20.11.1" || r.Program != shim || !r.SessionOK {
		t.Fatalf("%+v", r)
	}
}

func TestIsNodeScript(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		body string
		want bool
	}{
		{"#!/usr/bin/env node\nx", true},
		{"#!/usr/local/bin/node\nx", true},
		{"#!/usr/bin/env nodemon\nx", false},
		{"#!/bin/sh\nnode x", false},
		{"\x7fELF\x02\x01", false},
	} {
		p := filepath.Join(dir, "p")
		if err := os.WriteFile(p, []byte(c.body), 0o755); err != nil {
			t.Fatal(err)
		}
		if got := isNodeScript(p); got != c.want {
			t.Errorf("%q: got %v", c.body, got)
		}
	}
	if isNodeScript(filepath.Join(dir, "missing")) {
		t.Error("a missing file is not a node script")
	}
}
