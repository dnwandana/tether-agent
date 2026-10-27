package tools

import (
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/memory"
)

// openStore returns a memory store in a temp folder with the default char limits.
func openStore(t *testing.T) *memory.Store {
	t.Helper()
	s, err := memory.Open(t.TempDir(), 2200, 1375)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestToolSaysFrozenUntilNextPromptBuild(t *testing.T) {
	s := openStore(t)
	out := runTool(t, MemoryTool{Store: s}, `{"action":"add","target":"user","content":"The user uses Go"}`)
	if !strings.Contains(out, "shows in the system prompt from the next session or prompt rebuild") {
		t.Fatal(out)
	}
	if s.Profile() != "The user uses Go" {
		t.Fatalf("%q", s.Profile())
	}
}

func TestMemoryToolErrors(t *testing.T) {
	s := openStore(t)
	tool := MemoryTool{Store: s}
	if out := runToolErr(t, tool, `{"action":"remove","target":"memory","old_text":"x"}`); !strings.Contains(out, "no entry matches") {
		t.Fatal(out)
	}
	if out := runToolErr(t, tool, `{"action":"add","target":"other","content":"x"}`); !strings.Contains(out, "unknown target") {
		t.Fatal(out)
	}
	if out := runToolErr(t, MemoryTool{}, `{"action":"add","target":"memory","content":"x"}`); !strings.Contains(out, "not available") {
		t.Fatal(out)
	}
}

func TestMemoryToolSchema(t *testing.T) {
	tool := MemoryTool{}
	if tool.Name() != "memory" || tool.Toolset() != SetMemory || tool.ParallelSafe() {
		t.Fatal(tool.Name(), tool.Toolset())
	}
	sch := string(tool.Schema().Parameters)
	for _, w := range []string{`"enum": ["add", "replace", "remove"]`, `"enum": ["memory", "user"]`} {
		if !strings.Contains(sch, w) {
			t.Fatalf("schema has no %s", w)
		}
	}
}
