package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// ctx is the context for the tool calls in the tests.
var ctx = context.Background()

// fakeTool is a configurable Tool for the registry tests.
type fakeTool struct {
	name   string
	set    string
	panics bool
	err    error
	out    string
	// params is the JSON schema of the arguments. Empty means {"type":"object"}.
	params string
}

func (f fakeTool) Name() string       { return f.name }
func (f fakeTool) Toolset() string    { return f.set }
func (f fakeTool) ParallelSafe() bool { return true }
func (f fakeTool) Schema() provider.ToolSchema {
	params := f.params
	if params == "" {
		params = `{"type":"object"}`
	}
	return provider.ToolSchema{Name: f.name, Description: "fake", Parameters: json.RawMessage(params)}
}
func (f fakeTool) Run(_ context.Context, _ json.RawMessage, _ *Env) (string, error) {
	if f.panics {
		panic("boom")
	}
	return f.out, f.err
}

// newEnv returns an Env for one test session.
func newEnv(t *testing.T) *Env {
	t.Helper()
	return &Env{WorkDir: t.TempDir(), Log: slog.Default(), Values: map[string]any{}}
}

// dispatch registers tool in a new registry and calls it through Dispatch.
func dispatch(t *testing.T, tool Tool, args string, env *Env) Result {
	t.Helper()
	r := NewRegistry()
	r.Register(tool)
	return r.Dispatch(ctx, tool.Name(), json.RawMessage(args), env, map[string]bool{tool.Name(): true})
}

// runTool runs tool with a new Env and fails the test on an error result.
func runTool(t *testing.T, tool Tool, args string) string {
	t.Helper()
	return runToolEnv(t, tool, args, newEnv(t))
}

// runToolErr runs tool with a new Env and fails the test unless the result is an error.
func runToolErr(t *testing.T, tool Tool, args string) string {
	t.Helper()
	return runToolEnvErr(t, tool, args, newEnv(t))
}

// runToolEnv runs tool with env and fails the test on an error result.
func runToolEnv(t *testing.T, tool Tool, args string, env *Env) string {
	t.Helper()
	res := dispatch(t, tool, args, env)
	if res.IsError {
		t.Fatalf("%s returned an error: %s", tool.Name(), res.Text)
	}
	return res.Text
}

// runToolEnvErr runs tool with env and fails the test unless the result is an error.
func runToolEnvErr(t *testing.T, tool Tool, args string, env *Env) string {
	t.Helper()
	res := dispatch(t, tool, args, env)
	if !res.IsError {
		t.Fatalf("%s must return an error, got: %s", tool.Name(), res.Text)
	}
	return res.Text
}

// eventually fails the test when cond is not true within 5 seconds.
func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the condition did not become true in 5s")
}

func TestDispatchUnknownTool(t *testing.T) {
	r := NewRegistry()
	res := r.Dispatch(ctx, "nope", nil, &Env{}, nil)
	if !res.IsError || !strings.Contains(res.Text, `unknown tool "nope"`) {
		t.Fatal(res)
	}
}

func TestDispatchHiddenTool(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "terminal", set: "terminal"})
	res := r.Dispatch(ctx, "terminal", json.RawMessage(`{}`), &Env{}, map[string]bool{"read_file": true})
	if !res.IsError || !strings.Contains(res.Text, "not available in this session") {
		t.Fatal(res)
	}
}

func TestDispatchNilVisibleHidesAll(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "x", set: "file"})
	res := r.Dispatch(ctx, "x", json.RawMessage(`{}`), &Env{}, nil)
	if !res.IsError || !strings.Contains(res.Text, "not available in this session") {
		t.Fatal(res)
	}
}

func TestDispatchInvalidJSON(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "x", set: "file"})
	res := r.Dispatch(ctx, "x", json.RawMessage(`{bad`), &Env{}, map[string]bool{"x": true})
	if !res.IsError || !strings.Contains(res.Text, "invalid JSON arguments") {
		t.Fatal(res)
	}
}

func TestDispatchEmptyArgsIsEmptyObject(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "x", set: "file", out: "ok"})
	res := r.Dispatch(ctx, "x", nil, &Env{}, map[string]bool{"x": true})
	if res.IsError || res.Text != "ok" {
		t.Fatal(res)
	}
}

func TestDispatchRecoversPanic(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "boom", set: "file", panics: true})
	res := r.Dispatch(ctx, "boom", json.RawMessage(`{}`), &Env{Log: slog.Default()}, map[string]bool{"boom": true})
	if !res.IsError || !strings.Contains(res.Text, "tool boom failed") {
		t.Fatal(res)
	}
}

func TestDispatchRecoversPanicWithNilLog(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "boom", set: "file", panics: true})
	res := r.Dispatch(ctx, "boom", json.RawMessage(`{}`), nil, map[string]bool{"boom": true})
	if !res.IsError || res.Text != "tool boom failed: internal error" {
		t.Fatal(res)
	}
}

func TestDispatchErrorBecomesResult(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "x", set: "file", err: fmt.Errorf("%w: file not found", ErrToolError)})
	res := r.Dispatch(ctx, "x", json.RawMessage(`{}`), &Env{}, map[string]bool{"x": true})
	if !res.IsError || res.Text != "file not found" {
		t.Fatal(res)
	}
}

func TestDispatchOtherErrorNamesTool(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "x", set: "file", err: errors.New("disk full")})
	res := r.Dispatch(ctx, "x", json.RawMessage(`{}`), &Env{}, map[string]bool{"x": true})
	if !res.IsError || res.Text != "tool x failed: disk full" {
		t.Fatal(res)
	}
}

func TestDispatchSanitizesInvalidUTF8(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "cat", set: "file", out: "ELF\x7f\xff\xfe\x00ok"})
	res := r.Dispatch(ctx, "cat", json.RawMessage(`{}`), &Env{}, map[string]bool{"cat": true})
	if res.IsError || !utf8.ValidString(res.Text) || !strings.HasSuffix(res.Text, "\x00ok") {
		t.Fatalf("%q", res.Text)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "x", set: "file"})
	defer func() {
		if recover() == nil {
			t.Fatal("a duplicate name must panic")
		}
	}()
	r.Register(fakeTool{name: "x", set: "web"})
}

func TestAllSortedAndGet(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "b", set: "file"})
	r.Register(fakeTool{name: "a", set: "file"})
	all := r.All()
	if len(all) != 2 || all[0].Name() != "a" || all[1].Name() != "b" {
		t.Fatal(all)
	}
	if _, ok := r.Get("a"); !ok {
		t.Fatal("Get must find a")
	}
	if _, ok := r.Get("c"); ok {
		t.Fatal("Get must not find c")
	}
}

func TestUnregister(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "a", set: "file"})
	if !r.Unregister("a") {
		t.Fatal("Unregister must report the removed tool")
	}
	if r.Unregister("a") {
		t.Fatal("Unregister of a missing tool must report false")
	}
	if _, ok := r.Get("a"); ok || len(r.All()) != 0 {
		t.Fatal("the tool is still in the registry")
	}
	res := r.Dispatch(ctx, "a", json.RawMessage(`{}`), &Env{}, map[string]bool{"a": true})
	if !res.IsError || !strings.Contains(res.Text, `unknown tool "a"`) {
		t.Fatal(res)
	}
	// The name is free again.
	r.Register(fakeTool{name: "a", set: "web"})
}

func TestReplace(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "keep", set: "file"})
	r.Register(fakeTool{name: "old", set: "mcp_x"})
	r.Register(fakeTool{name: "same", set: "mcp_x", out: "v1"})
	skipped := r.Replace([]string{"old", "same", "missing"}, []Tool{
		fakeTool{name: "same", set: "mcp_x", out: "v2"},
		fakeTool{name: "new", set: "mcp_x"},
		fakeTool{name: "keep", set: "mcp_x"},
		fakeTool{name: "new", set: "mcp_y"},
	})
	if len(skipped) != 2 || skipped[0] != "keep" || skipped[1] != "new" {
		t.Fatal("a name that is in the registry is skipped:", skipped)
	}
	var names []string
	for _, tl := range r.All() {
		names = append(names, tl.Name()+":"+tl.Toolset())
	}
	if strings.Join(names, ",") != "keep:file,new:mcp_x,same:mcp_x" {
		t.Fatal(names)
	}
	res := r.Dispatch(ctx, "same", nil, &Env{}, map[string]bool{"same": true})
	if res.Text != "v2" {
		t.Fatal("Replace swaps the tool:", res)
	}
}

// TestRegistryConcurrentChanges runs with -race. Readers must see a full set: the
// tools "x1" and "x2" change in one Replace, so a reader sees both or none.
func TestRegistryConcurrentChanges(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "base", set: "file", out: "ok"})
	stop := make(chan struct{})
	done := make(chan error, 3)
	go func() {
		defer func() { done <- nil }()
		pair := []Tool{fakeTool{name: "x1", set: "mcp_x"}, fakeTool{name: "x2", set: "mcp_x"}}
		for i := 0; i < 2000; i++ {
			if i%2 == 0 {
				r.Replace(nil, pair)
			} else {
				r.Replace([]string{"x1", "x2"}, nil)
			}
			r.Unregister("tmp")
			r.Register(fakeTool{name: "tmp", set: "file"})
		}
		close(stop)
	}()
	for range 2 {
		go func() {
			for {
				select {
				case <-stop:
					done <- nil
					return
				default:
				}
				names, _ := r.Visible(map[string]bool{"file": true, "mcp": true}, nil)
				if names["x1"] != names["x2"] {
					done <- fmt.Errorf("a reader saw half of a Replace: %v", names)
					return
				}
				if res := r.Dispatch(ctx, "base", nil, &Env{}, map[string]bool{"base": true}); res.Text != "ok" {
					done <- fmt.Errorf("dispatch: %v", res)
					return
				}
				r.Dispatch(ctx, "x1", nil, &Env{}, map[string]bool{"x1": true})
				r.Get("tmp")
			}
		}()
	}
	for range 3 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

// bufEnv returns an Env whose log goes to a text handler on the returned buffer.
func bufEnv() (*Env, *bytes.Buffer) {
	var buf bytes.Buffer
	return &Env{Log: slog.New(slog.NewTextHandler(&buf, nil))}, &buf
}

func TestDispatchUnknownArgsNoteOnSuccess(t *testing.T) {
	env, buf := bufEnv()
	res := dispatch(t, fakeTool{name: "f", out: "done", params: abSchema}, `{"a":"x","x":"secret-value"}`, env)
	want := "done\n\n" + unknownArgsNote([]string{"x"}, []string{"a", "b"})
	if res.IsError || res.Text != want {
		t.Fatalf("result = %+v, want text %q", res, want)
	}
	log := buf.String()
	for _, s := range []string{"level=WARN", `msg="tools: the call has unknown arguments"`, "tool=f", "keys=x"} {
		if !strings.Contains(log, s) {
			t.Fatalf("log %q does not contain %q", log, s)
		}
	}
	if strings.Contains(log, "secret-value") {
		t.Fatalf("log %q contains an argument value", log)
	}
}

func TestDispatchUnknownArgsKeysAreSorted(t *testing.T) {
	env, buf := bufEnv()
	res := dispatch(t, fakeTool{name: "f", out: "done", params: abSchema}, `{"z":1,"a":"x","y":2}`, env)
	if !strings.HasSuffix(res.Text, unknownArgsNote([]string{"y", "z"}, []string{"a", "b"})) {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(buf.String(), "keys=y,z") {
		t.Fatalf("log = %q", buf.String())
	}
}

func TestDispatchUnknownArgsNoteOnError(t *testing.T) {
	env, _ := bufEnv()
	res := dispatch(t, fakeTool{name: "f", err: toolErr("boom"), params: abSchema}, `{"args":{"a":"x"}}`, env)
	want := "boom\n\n" + unknownArgsNote([]string{"args"}, []string{"a", "b"})
	if !res.IsError || res.Text != want {
		t.Fatalf("result = %+v, want text %q", res, want)
	}
}

func TestDispatchUnknownArgsEmptyTextGetsNoteAlone(t *testing.T) {
	env, _ := bufEnv()
	res := dispatch(t, fakeTool{name: "f", params: abSchema}, `{"x":1}`, env)
	if want := unknownArgsNote([]string{"x"}, []string{"a", "b"}); res.IsError || res.Text != want {
		t.Fatalf("result = %+v, want text %q", res, want)
	}
}

func TestDispatchNoNoteForCleanOrEmptyArgs(t *testing.T) {
	for _, args := range []string{`{"a":"x"}`, ""} {
		env, buf := bufEnv()
		res := dispatch(t, fakeTool{name: "f", out: "done", params: abSchema}, args, env)
		if res.IsError || res.Text != "done" {
			t.Fatalf("args %q: result = %+v", args, res)
		}
		if buf.Len() != 0 {
			t.Fatalf("args %q: log = %q", args, buf.String())
		}
	}
}

func TestDispatchNoNoteForPanic(t *testing.T) {
	env, _ := bufEnv()
	res := dispatch(t, fakeTool{name: "f", panics: true, params: abSchema}, `{"x":1}`, env)
	if !res.IsError || res.Text != "tool f failed: internal error" {
		t.Fatalf("result = %+v", res)
	}
}

func TestDispatchScriptCallNoteOnlyOnError(t *testing.T) {
	env, buf := bufEnv()
	env.ScriptCall = true
	res := dispatch(t, fakeTool{name: "f", out: "done", params: abSchema}, `{"x":1}`, env)
	if res.IsError || res.Text != "done" {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(buf.String(), `msg="tools: the call has unknown arguments"`) {
		t.Fatalf("log = %q", buf.String())
	}
	res = dispatch(t, fakeTool{name: "f", err: toolErr("boom"), params: abSchema}, `{"x":1}`, env)
	if !res.IsError || !strings.HasSuffix(res.Text, "\n\n"+unknownArgsNote([]string{"x"}, []string{"a", "b"})) {
		t.Fatalf("result = %+v", res)
	}
}
