package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/tools"
)

const pypiValue = "pypi-AgEIcHlwaS5vcmcCJGQ5ZTk2"

func TestToolTextSwapsTerminalResults(t *testing.T) {
	a, _ := newAgent(t, scripted())
	a.Handles = security.NewHandles()
	a.Redactor = security.NewNamedRedactor(map[string]string{"PYPI_TOKEN": pypiValue})
	tok := "ghp_" + strings.Repeat("a", 36)
	got := a.toolText("terminal", "root1", tools.Result{Text: "token " + tok + " and " + pypiValue})
	if !regexp.MustCompile(`^token \$TMP_SECRET_[0-9]+ and «PYPI_TOKEN»$`).MatchString(got) {
		t.Fatalf("got %q", got)
	}
	if v := a.Handles.Values("root1"); len(v) != 1 || v[0] != tok {
		t.Fatalf("values %q", v)
	}
	// A web result gets the mask and no handle.
	if web := a.toolText("web_fetch", "root1", tools.Result{Text: "token " + tok}); strings.Contains(web, "TMP_SECRET") ||
		strings.Contains(web, tok) {
		t.Fatalf("got %q", web)
	}
	// A background log read gets the handle.
	if got := a.toolText(bgLogTool, "root1", tools.Result{Text: tok}); !strings.HasPrefix(got, "$TMP_SECRET_") {
		t.Fatalf("got %q", got)
	}
}

func TestToolTextSpillHoldsSwappedText(t *testing.T) {
	a, _ := newAgent(t, scripted())
	a.Handles = security.NewHandles()
	a.Redactor = security.NewNamedRedactor(nil)
	a.Cfg.Context.ToolOutputMaxChars = 200
	tok := "ghp_" + strings.Repeat("d", 36)
	got := a.toolText("terminal", "root2", tools.Result{Text: tok + "\n" + strings.Repeat("line of output\n", 100)})
	entries, err := os.ReadDir(a.SpillDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("spill files %v, err %v", entries, err)
	}
	b, err := os.ReadFile(filepath.Join(a.SpillDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), tok) || !strings.Contains(string(b), "$TMP_SECRET_") || strings.Contains(got, tok) {
		t.Fatalf("spill %q\nresult %q", b, got)
	}
}

func TestHandleToolFindsBackgroundLogReads(t *testing.T) {
	a, _ := newAgent(t, scripted())
	env := &tools.Env{WorkDir: a.SpillDir}
	path := func(p string) string { return `{"path":"` + p + `"}` }
	for _, c := range []struct {
		call provider.ToolCall
		want string
	}{
		{provider.ToolCall{Name: "terminal", Args: `{"command":"gh auth token"}`}, "terminal"},
		{provider.ToolCall{Name: "read_file", Args: path(filepath.Join(a.SpillDir, "bg-4242.log"))}, bgLogTool},
		{provider.ToolCall{Name: "read_file", Args: path("bg-7.log")}, bgLogTool},
		{provider.ToolCall{Name: "read_file", Args: path(filepath.Join(a.SpillDir, "web_fetch-ab12.txt"))}, "read_file"},
		{provider.ToolCall{Name: "read_file", Args: path(filepath.Join(t.TempDir(), "bg-1.log"))}, "read_file"},
		{provider.ToolCall{Name: "web_fetch", Args: `{"url":"https://example.com"}`}, "web_fetch"},
	} {
		if got := a.handleTool(c.call, env); got != c.want {
			t.Errorf("%s %s: got %q, want %q", c.call.Name, c.call.Args, got, c.want)
		}
	}
}

func TestBeforeToolTextRunsOncePerRound(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "echo", `{"s":"x"}`), tc("c2", "echo", `{"s":"y"}`)), text("done"))
	a, sess := newAgent(t, fp, echoTool{})
	n := 0
	a.BeforeToolText = func() { n++ }
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), System: "SYS"}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("got %d calls, want 1", n)
	}
}
