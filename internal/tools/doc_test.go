package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/setup"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// docTool is a tool with only a name, a toolset, and a schema.
type docTool struct {
	name, set string
}

func (d docTool) Name() string    { return d.name }
func (d docTool) Toolset() string { return d.set }
func (d docTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{Name: d.name, Description: "The " + d.name + " tool.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)}
}
func (docTool) ParallelSafe() bool { return true }
func (docTool) Run(context.Context, json.RawMessage, *tools.Env) (string, error) {
	return "", nil
}

func TestGeneratedGroupsAndSchemas(t *testing.T) {
	r := tools.NewRegistry()
	r.Register(docTool{name: "web_fetch", set: "web"})
	r.Register(docTool{name: "terminal", set: "terminal"})
	r.Register(docTool{name: "execute_code", set: "code_execution"})
	md := tools.GenerateMarkdown(r)
	code, term, web := strings.Index(md, "\n## code_execution"), strings.Index(md, "\n## terminal"), strings.Index(md, "\n## web")
	if code < 0 || !(code < term && term < web) {
		t.Fatal("the toolsets are not sorted:\n" + md)
	}
	for _, want := range []string{"### web_fetch", "The web_fetch tool.", "```json\n{\n  \"type\": \"object\",", "python3"} {
		if !strings.Contains(md, want) {
			t.Fatalf("no %q in\n%s", want, md)
		}
	}
}

func TestGeneratedDocIsCurrent(t *testing.T) {
	// app.New sets the protected paths of its temporary data folder. Reset them for the
	// other tests in this binary.
	t.Cleanup(func() { security.AddProtectedPathPatterns(nil) })
	r, done, err := setup.ToolsDocRegistry(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	// The registry has every built-in tool, also without python3 and Chrome on this host.
	for _, name := range []string{"execute_code", "view_image", "browser_navigate", "delegate_task",
		"text_to_speech", "transcribe_audio", "generate_image", "cronjob"} {
		if _, ok := r.Get(name); !ok {
			t.Fatalf("the doc registry has no %s", name)
		}
	}
	want := tools.GenerateMarkdown(r)
	got, err := os.ReadFile("../../docs/TOOLS.md")
	if err != nil || string(got) != want {
		t.Fatal("docs/TOOLS.md is out of date: run go run ./cmd/tether gen-tools-doc > docs/TOOLS.md")
	}
}
