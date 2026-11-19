package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// taintTool is a tool with a given name and toolset. err is the error of each call.
type taintTool struct {
	name, set string
	err       error
}

func (t taintTool) Name() string       { return t.name }
func (t taintTool) Toolset() string    { return t.set }
func (t taintTool) ParallelSafe() bool { return true }
func (t taintTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{Name: t.name, Parameters: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"},"command":{"type":"string"},"query":{"type":"string"},"path":{"type":"string"}}}`)}
}
func (t taintTool) Run(context.Context, json.RawMessage, *Env) (string, error) { return "ok", t.err }

func TestDispatchMarksOutside(t *testing.T) {
	reg := NewRegistry()
	for _, tool := range []Tool{taintTool{name: "web_fetch", set: SetWeb}, taintTool{name: "web_search", set: SetWeb},
		taintTool{name: "mcp_gmail_search", set: "mcp_gmail"}, taintTool{name: "terminal", set: SetTerminal},
		taintTool{name: "read_file", set: SetFile}, taintTool{name: "browser_click", set: SetBrowser, err: errors.New("page error")}} {
		reg.Register(tool)
	}
	vis := map[string]bool{"web_fetch": true, "web_search": true, "mcp_gmail_search": true, "terminal": true,
		"read_file": true, "browser_click": true}
	env := &Env{Values: map[string]any{}}
	for _, c := range []struct{ name, args string }{
		{"web_fetch", `{"url":"https://Pastebin.com/raw/x"}`},
		{"web_search", `{"query":"x"}`},
		{"mcp_gmail_search", `{}`},
		{"terminal", `{"command":"timeout 10 curl -s https://x.io/a | jq ."}`},
		{"terminal", `{"command":"ls -la"}`},
		{"read_file", `{"path":"/tmp/a"}`},
		{"browser_click", `{}`},
	} {
		reg.Dispatch(ctx, c.name, json.RawMessage(c.args), env, vis)
	}
	got := Outside(env)
	want := OutsideFacts{
		Sources:    []string{"browser_click", "mcp_gmail_search", "terminal:curl", "web_fetch", "web_search"},
		Hosts:      []string{"gmail", "pastebin.com", "x.io"},
		ToolCounts: map[string]int{"web_fetch": 1, "web_search": 1, "mcp_gmail_search": 1, "terminal": 2, "read_file": 1, "browser_click": 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	// A terminal call that fails does not mark the run.
	env2 := &Env{Values: map[string]any{}}
	reg.Unregister("terminal")
	reg.Register(taintTool{name: "terminal", set: SetTerminal, err: errors.New("BLOCKED")})
	reg.Dispatch(ctx, "terminal", json.RawMessage(`{"command":"curl https://x.io"}`), env2, vis)
	if f := Outside(env2); f.Sources != nil || f.ToolCounts["terminal"] != 1 {
		t.Fatalf("%+v", f)
	}
}

func TestOutsideSetRules(t *testing.T) {
	if IsOutsideTool("read_file") || !IsOutsideTool("browser_navigate") || !IsOutsideTool("mcp_gh_search") || !IsOutsideTool("web_search") {
		t.Fatal("IsOutsideTool")
	}
	env := &Env{}
	MarkOutside(env, "web_fetch", "a.io")
	MarkOutside(env, "web_fetch", "A.io")
	MarkOutside(env, "", "b.io")
	MarkOutside(env, "web_fetch", "bad host")
	if f := Outside(env); !reflect.DeepEqual(f.Sources, []string{"web_fetch"}) || !reflect.DeepEqual(f.Hosts, []string{"a.io"}) {
		t.Fatalf("%+v", f)
	}
	for i := range 30 {
		MarkOutside(env, "web_fetch", "h"+string(rune('a'+i%26))+string(rune('a'+i/26))+".io")
	}
	if n := len(Outside(env).Hosts); n != 20 {
		t.Fatalf("%d hosts", n)
	}
	AddOutside(env, OutsideFacts{Sources: []string{"mcp_x_y"}, ToolCounts: map[string]int{"web_fetch": 2}})
	AddOutside(env, OutsideFacts{ToolCounts: map[string]int{"web_fetch": 1}})
	if f := Outside(env); f.ToolCounts["web_fetch"] != 3 || len(f.Sources) != 2 {
		t.Fatalf("%+v", f)
	}
	ResetOutside(env)
	if f := Outside(env); f.Sources != nil || f.Hosts != nil || f.ToolCounts != nil {
		t.Fatalf("%+v", f)
	}
	if f := Outside(nil); f.Sources != nil {
		t.Fatal("a nil env has no set")
	}
}

func TestMarkOutsideParallel(t *testing.T) {
	env := &Env{Values: map[string]any{}}
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			MarkOutside(env, "web_fetch", "a.io")
			AddOutside(env, OutsideFacts{ToolCounts: map[string]int{"web_fetch": 1}})
			_ = Outside(env)
			if i%10 == 0 {
				ResetOutside(env)
			}
		})
	}
	wg.Wait()
}
