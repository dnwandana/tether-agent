package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

const secretNoteHead = "Bound secrets: the owner bound these secrets to commands. " +
	"A matching terminal command gets the value in its environment. You never see a value. " +
	"Write \"$NAME\" on the command line only when the program needs it there. " +
	"When a value is missing, ask the owner with request_secret."

var testBound = map[string][]string{"PYPI_TOKEN": {"twine upload", "uv publish"}, "GH_TOKEN": {"gh release", "gh pr create"}}

func TestSecretNotes(t *testing.T) {
	want := secretNoteHead + "\n  · GH_TOKEN: gh release, gh pr create\n  · PYPI_TOKEN: twine upload, uv publish"
	if got := SecretNotes(testBound); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if got := SecretNotes(nil); got != "" {
		t.Fatalf("got %q", got)
	}
	many := map[string][]string{}
	for i := range 41 {
		many[fmt.Sprintf("NAME_%02d", i)] = []string{"cmd"}
	}
	lines := strings.Split(SecretNotes(many), "\n")
	if len(lines) != 42 || lines[1] != "  · NAME_00: cmd" || lines[40] != "  · NAME_39: cmd" || lines[41] != "  · and 1 more" {
		t.Fatalf("%q", lines)
	}
}

func TestSessionPromptSecretNotes(t *testing.T) {
	entry := "- " + SecretNotes(testBound)
	for _, c := range []struct {
		name     string
		terminal bool
		bound    bool
		member   bool
		shared   bool
		mode     string
		want     bool
	}{
		{"owner with terminal", true, true, false, false, "", true},
		{"member prompt", true, true, true, false, "", false},
		{"no terminal", false, true, false, false, "", false},
		{"shared session", true, true, false, true, "", false},
		{"read-only", true, true, false, false, security.ModeReadOnly, false},
		{"no bound secret", true, false, false, false, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var ts []tools.Tool
			if c.terminal {
				ts = append(ts, newNamed("terminal", tools.SetTerminal))
			}
			a, sess := newAgent(t, scripted(), ts...)
			if c.bound {
				a.Cfg.ExecSecrets = map[string]config.ExecSecret{
					"PYPI_TOKEN": {Commands: testBound["PYPI_TOKEN"]}, "GH_TOKEN": {Commands: testBound["GH_TOKEN"]}}
			}
			a.SharedSession = func(*sessions.Session) bool { return c.shared }
			a.Prompts = NewPromptCache(a.SessionPrompt)
			key := keyFor(sess, 0)
			key.Member, key.Mode = c.member, c.mode
			p, _ := a.Prompts.Get(ctx, sess, key, "telegram")
			if got := strings.Contains(p, entry); got != c.want {
				t.Fatalf("entry in prompt: %v, want %v\n%s", got, c.want, p)
			}
		})
	}
}

func TestChildSecretNotes(t *testing.T) {
	note := SecretNotes(map[string][]string{"PYPI_TOKEN": {"twine upload"}})
	if p := ChildSystemPrompt("x", "ctx", note); !strings.Contains(p, "\nCONTEXT:\nctx\n\nTOOL NOTES:\n- "+note+"\n") {
		t.Fatal(p)
	}
	if p := ChildSystemPrompt("x", "", ""); strings.Contains(p, "TOOL NOTES:") {
		t.Fatal(p)
	}
	fp := scripted(text("ok"), text("ok"), text("ok"))
	a, parent := newAgent(t, fp, newNamed("terminal", tools.SetTerminal), echoTool{})
	a.Cfg.ExecSecrets = map[string]config.ExecSecret{"PYPI_TOKEN": {Commands: []string{"twine upload"}}}
	a.RunChild(ctx, ChildSpec{Goal: "x", ParentSession: parent, ParentToolsets: all()})
	if !strings.Contains(fp.requests[0].System, "TOOL NOTES:\n- "+note) {
		t.Fatal("a child with terminal gets the entry:", fp.requests[0].System)
	}
	// A child with no terminal gets no entry.
	a.RunChild(ctx, ChildSpec{Goal: "x", ParentSession: parent, ParentToolsets: all(), Toolsets: []string{tools.SetWeb}})
	if strings.Contains(fp.requests[1].System, "TOOL NOTES:") {
		t.Fatal(fp.requests[1].System)
	}
	// A child of a member turn gets no entry.
	a.RunChild(ctx, ChildSpec{Goal: "x", ParentSession: parent, ParentToolsets: all(), ParentEnv: &tools.Env{Member: true}})
	if strings.Contains(fp.requests[2].System, "TOOL NOTES:") {
		t.Fatal(fp.requests[2].System)
	}
}
