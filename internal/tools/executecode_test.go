package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// py returns the path of python3, or skips the test.
func py(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	return p
}

// pythonOrSkip skips the test when python3 is missing.
func pythonOrSkip(t *testing.T) { t.Helper(); py(t) }

// fetchStub is a web_fetch tool that returns the page of the first URL from pages.
type fetchStub struct{ pages map[string]string }

func fakeFetch(pages map[string]string) Tool { return fetchStub{pages: pages} }

func (fetchStub) Name() string       { return "web_fetch" }
func (fetchStub) Toolset() string    { return SetWeb }
func (fetchStub) ParallelSafe() bool { return true }
func (fetchStub) Schema() provider.ToolSchema {
	return provider.ToolSchema{Name: "web_fetch", Description: "Fetch pages.\nSecond line.", Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {"urls": {"type": "array"}, "max_chars": {"type": "integer"}, "from": {"type": "string"}},
  "required": ["urls"]
}`)}
}
func (f fetchStub) Run(_ context.Context, raw json.RawMessage, _ *Env) (string, error) {
	var a struct {
		URLs     []string `json:"urls"`
		MaxChars int      `json:"max_chars"`
		From     string   `json:"from"`
	}
	if err := json.Unmarshal(raw, &a); err != nil || len(a.URLs) == 0 {
		return "", toolErr("urls is required")
	}
	page, ok := f.pages[a.URLs[0]]
	if !ok {
		return "", toolErr("no page %s", a.URLs[0])
	}
	if a.MaxChars > 0 && a.MaxChars < len(page) {
		page = page[:a.MaxChars]
	}
	return page + a.From, nil
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// visEnv returns an Env whose turn makes the names visible. Its gate lets each command run.
func visEnv(names ...string) *Env {
	env := &Env{SessionID: "s1", Values: map[string]any{}, Gate: allowAll{}}
	SetTurnTools(env, nil, vis(names...))
	return env
}

func scriptTool(t *testing.T, reg *Registry) ExecuteCodeTool {
	t.Helper()
	return ExecuteCodeTool{Reg: reg, ScratchRoot: t.TempDir(), TimeoutS: 300, MaxCalls: 50, MaxStdout: 50000, Python: py(t)}
}

func code(src string) string { return mustJSON(map[string]string{"code": src}) }

func TestScriptCallsToolsOnlyStdoutReturns(t *testing.T) {
	pythonOrSkip(t)
	reg := regWith(fakeFetch(map[string]string{"a": "AAA", "b": "BBBB"}))
	tool := scriptTool(t, reg)
	src := "from tether_tools import web_fetch\nimport sys\nprint('noise', file=sys.stderr)\nprint(len(web_fetch(urls=['a'])) + len(web_fetch(['b'])))"
	out := runToolEnv(t, tool, code(src), visEnv("web_fetch"))
	if strings.TrimSpace(out) != "7" {
		t.Fatalf("%q", out)
	}
	entries, _ := os.ReadDir(tool.ScratchRoot)
	if len(entries) != 0 {
		t.Fatal("the scratch folder is removed after the run:", entries)
	}
}

func TestScriptStubKeywordArguments(t *testing.T) {
	pythonOrSkip(t)
	reg := regWith(fakeFetch(map[string]string{"a": "AAAA"}))
	src := "from tether_tools import web_fetch\nprint(web_fetch(['a'], max_chars=2))\nprint(web_fetch(['a'], **{'from': '!'}))\nprint(web_fetch.__doc__)"
	out := runToolEnv(t, scriptTool(t, reg), code(src), visEnv("web_fetch"))
	if out != "AA\nAAAA!\nFetch pages.\n" {
		t.Fatalf("%q", out)
	}
}

func TestScriptStubsOnlyForVisibleCallableTools(t *testing.T) {
	pythonOrSkip(t)
	reg := regWith(fakeFetch(nil), echoAs("web_search"), echoAs("memory"))
	src := "import tether_tools as tt\nprint(hasattr(tt, 'web_fetch'), hasattr(tt, 'web_search'), hasattr(tt, 'memory'))"
	out := runToolEnv(t, scriptTool(t, reg), code(src), visEnv("web_search", "memory"))
	if strings.TrimSpace(out) != "False True False" {
		t.Fatalf("%q", out)
	}
}

func TestTimeoutClampedAndEnforced(t *testing.T) {
	tool := ExecuteCodeTool{TimeoutS: 300}
	if tool.timeout(999) != 300*time.Second || tool.timeout(0) != 300*time.Second || tool.timeout(5) != 5*time.Second {
		t.Fatal("timeout_s above 300 is clamped")
	}
	if (ExecuteCodeTool{}).timeout(0) != 300*time.Second {
		t.Fatal("the default is 300 s")
	}
	pythonOrSkip(t)
	start := time.Now()
	out := runToolEnv(t, scriptTool(t, NewRegistry()), mustJSON(map[string]any{"code": "import time\nprint('before', flush=True)\ntime.sleep(30)", "timeout_s": 1}), visEnv())
	if time.Since(start) > 10*time.Second || !strings.Contains(out, "before") || !strings.Contains(out, "[timed out after 1s]") {
		t.Fatalf("%q", out)
	}
}

func TestStdoutCap50KB(t *testing.T) {
	pythonOrSkip(t)
	out := runToolEnv(t, scriptTool(t, NewRegistry()), code("print('x'*100000)"), visEnv())
	want := strings.Repeat("x", 50000) + "\n[stdout truncated at 50000 bytes]"
	if out != want {
		t.Fatalf("len %d, tail %q", len(out), out[max(0, len(out)-60):])
	}
}

func TestEnvHasNoSecrets(t *testing.T) {
	pythonOrSkip(t)
	t.Setenv("OPENAI_API_KEY", "sk-test-secret")
	t.Setenv("TETHER_TEST_PLAIN", "plain")
	src := "import os\nprint('OPENAI_API_KEY' in os.environ, os.environ.get('TETHER_TEST_PLAIN'), 'TETHER_RPC_SOCK' in os.environ, os.getcwd() == os.path.dirname(os.path.abspath(__file__)))"
	out := runToolEnv(t, scriptTool(t, NewRegistry()), code(src), visEnv())
	if strings.TrimSpace(out) != "False plain True True" {
		t.Fatalf("%q", out)
	}
}

func TestToolErrorRaisesInPython(t *testing.T) {
	pythonOrSkip(t)
	reg := regWith(fakeFetch(map[string]string{}), echoAs("memory"))
	src := `from tether_tools import web_fetch, _call, ToolError
for call in (lambda: _call("memory", {}), lambda: web_fetch(urls=["missing"])):
    try:
        call()
    except ToolError as e:
        print("ToolError:", e)`
	out := runToolEnv(t, scriptTool(t, reg), code(src), visEnv("web_fetch", "memory"))
	if !strings.Contains(out, `ToolError: tool "memory" is not callable from execute_code`) || !strings.Contains(out, "ToolError: no page missing") {
		t.Fatalf("%q", out)
	}
}

func TestScriptFailureShowsStderrAndExitCode(t *testing.T) {
	pythonOrSkip(t)
	out := runToolEnv(t, scriptTool(t, NewRegistry()), code("print('partial')\nraise SystemExit('boom')"), visEnv())
	if !strings.HasPrefix(out, "partial\n") || !strings.Contains(out, "--- stderr ---\nboom") || !strings.Contains(out, "[exit code: 1]") {
		t.Fatalf("%q", out)
	}
}

func TestScriptCallLimit(t *testing.T) {
	pythonOrSkip(t)
	tool := scriptTool(t, regWith(fakeFetch(map[string]string{"a": "A"})))
	tool.MaxCalls = 2
	src := `from tether_tools import web_fetch, ToolError
for i in range(3):
    try:
        print(web_fetch(urls=["a"]))
    except ToolError as e:
        print(e)`
	out := runToolEnv(t, tool, code(src), visEnv("web_fetch"))
	if out != "A\nA\ntool call limit (2) reached\n" {
		t.Fatalf("%q", out)
	}
}

func TestExecuteCodeGoesThroughTheGate(t *testing.T) {
	pythonOrSkip(t)
	marker := filepath.Join(t.TempDir(), "ran")
	src := fmt.Sprintf("open(%q, 'w').write('x')", marker)
	var seen []string
	env := visEnv()
	env.Gate = denyAll{reason: "BLOCKED by user: script", seen: &seen}
	out := runToolEnvErr(t, scriptTool(t, NewRegistry()), code(src), env)
	if !strings.Contains(out, "BLOCKED by user: script") || len(seen) != 1 || seen[0] != src {
		t.Fatal(out, seen)
	}
	env.Gate = nil
	if out := runToolEnvErr(t, scriptTool(t, NewRegistry()), code(src), env); !strings.Contains(out, "approval gate missing") {
		t.Fatal(out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a blocked script ran")
	}
}

func TestScriptRunCommandUsesTheGate(t *testing.T) {
	pythonOrSkip(t)
	reg := regWith(&TerminalTool{DefaultTimeout: 10 * time.Second})
	var seen []string
	env := visEnv("terminal")
	// The script itself passes the gate. Its terminal call is blocked.
	env.Gate = gateFunc(func(cmd string) error {
		seen = append(seen, cmd)
		if strings.HasPrefix(cmd, "rm ") {
			return fmt.Errorf("BLOCKED: recursive delete")
		}
		return nil
	})
	src := `from tether_tools import terminal, ToolError
try:
    terminal("rm -rf /tmp/nothing-here")
except ToolError as e:
    print(e)`
	out := runToolEnv(t, scriptTool(t, reg), code(src), env)
	if strings.TrimSpace(out) != "BLOCKED: recursive delete" || len(seen) != 2 {
		t.Fatalf("%q %q", out, seen)
	}
}

// gateFunc is an Approver from a function.
type gateFunc func(cmd string) error

func (g gateFunc) CheckCommand(_ context.Context, _ *Env, cmd string) error { return g(cmd) }

func TestLongScratchRootKeepsTheSocketShort(t *testing.T) {
	pythonOrSkip(t)
	root := t.TempDir()
	for range 3 {
		root = filepath.Join(root, strings.Repeat("d", 40))
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	tool := scriptTool(t, regWith(fakeFetch(map[string]string{"a": "AAA"})))
	tool.ScratchRoot = root
	out := runToolEnv(t, tool, code("from tether_tools import web_fetch\nprint(web_fetch(['a']))"), visEnv("web_fetch"))
	if strings.TrimSpace(out) != "AAA" {
		t.Fatalf("%q", out)
	}
}

func TestExecuteCodeArgsAndSchema(t *testing.T) {
	tool := ExecuteCodeTool{Python: "/usr/bin/python3"}
	if tool.Name() != "execute_code" || tool.Toolset() != SetCodeExecution || tool.ParallelSafe() {
		t.Fatal("name, toolset, or parallel flag")
	}
	if d := tool.Schema().Description; !strings.Contains(d, "tether_tools") || !strings.Contains(d, "web_fetch") {
		t.Fatal(d)
	}
	if out := runToolEnvErr(t, tool, `{"code":"  "}`, visEnv()); !strings.Contains(out, "code is required") {
		t.Fatal(out)
	}
	if out := runToolEnvErr(t, ExecuteCodeTool{}, `{"code":"print(1)"}`, visEnv()); !strings.Contains(out, "python3") {
		t.Fatal(out)
	}
}
