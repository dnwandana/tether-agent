package tools

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
)

// handleFixture returns a terminal tool with a handle store, an env of the root session
// "root1", and the name and the value of one handle of that root.
func handleFixture(t *testing.T) (*TerminalTool, *Env, string, string) {
	t.Helper()
	h := security.NewHandles()
	tok := "ghp_" + strings.Repeat("b", 36)
	name := strings.TrimPrefix(h.Swap("root1", tok, security.NewNamedRedactor(nil)), "$")
	env := newEnv(t)
	env.SessionID = "root1"
	return &TerminalTool{DefaultTimeout: time.Minute, Handles: h}, env, name, tok
}

// echoHandle returns the args of a command that prints the handle name.
func echoHandle(name string) string {
	b, _ := json.Marshal(map[string]string{"command": `echo "${` + name + `}"`})
	return string(b)
}

func TestHandleGivenWithNoOutsideContent(t *testing.T) {
	tool, env, name, tok := handleFixture(t)
	g, _, asks := newPinGate(true, nil)
	env.Gate = g
	if out := runToolEnv(t, tool, echoHandle(name), env); out != tok+"\n" || len(*asks) != 0 {
		t.Fatalf("%q, %d asks", out, len(*asks))
	}
	// A child of the root uses the same handles.
	env.SessionID, env.RootSessionID = "child", "root1"
	if out := runToolEnv(t, tool, echoHandle(name), env); out != tok+"\n" {
		t.Fatalf("%q", out)
	}
}

func TestHandleAfterOutsideContent(t *testing.T) {
	for _, c := range []struct {
		name string
		gate func() (Approver, *[]PinRequest)
		want func(handle, tok string) string
		asks int
	}{
		{"allow", func() (Approver, *[]PinRequest) { g, _, a := newPinGate(true, nil); return g, a },
			func(_, tok string) string { return tok + "\n" }, 1},
		{"deny", func() (Approver, *[]PinRequest) { g, _, a := newPinGate(false, nil); return g, a },
			func(h, _ string) string { return "\n[" + h + " was not given: the owner did not allow it]" }, 1},
		{"no prompter", func() (Approver, *[]PinRequest) { g, _, a := newPinGate(false, security.ErrNoPrompter); return g, a },
			func(h, _ string) string { return "\n[" + h + " was not given: this run read outside content]" }, 1},
		{"no pinner", func() (Approver, *[]PinRequest) { return allowAll{}, &[]PinRequest{} },
			func(h, _ string) string { return "\n[" + h + " was not given: this run read outside content]" }, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			tool, env, name, tok := handleFixture(t)
			g, asks := c.gate()
			env.Gate = g
			MarkOutside(env, "web_fetch", "example.com")
			if out := runToolEnv(t, tool, echoHandle(name), env); out != c.want(name, tok) {
				t.Fatalf("got %q\nwant %q", out, c.want(name, tok))
			}
			if len(*asks) != c.asks {
				t.Fatalf("%d asks", len(*asks))
			}
			if c.asks == 1 {
				a := (*asks)[0]
				if !reflect.DeepEqual(a.Handles, []string{name}) || !reflect.DeepEqual(a.Sources, []string{"web_fetch"}) ||
					a.Command != `echo "${`+name+`}"` || a.Name != "" {
					t.Fatalf("%+v", a)
				}
			}
		})
	}
}

func TestHandleUnknownAndMember(t *testing.T) {
	tool, env, name, _ := handleFixture(t)
	g, _, _ := newPinGate(true, nil)
	env.Gate = g
	want := "\n[TMP_SECRET_999999 is unknown. Handles end when the session ends or the gateway restarts.]"
	if out := runToolEnv(t, tool, `{"command":"echo $TMP_SECRET_999999"}`, env); out != want {
		t.Fatalf("got %q", out)
	}
	env.Member = true
	if out := runToolEnv(t, tool, echoHandle(name), env); out != "\n["+name+" was not given: this run cannot use bound secrets]" {
		t.Fatalf("got %q", out)
	}
}
