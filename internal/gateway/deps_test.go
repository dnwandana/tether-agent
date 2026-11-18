package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// progressTool sends one child tool start through env.Progress, like delegate_task does for
// the tool starts of its children.
type progressTool struct{}

func (progressTool) Name() string       { return "progress" }
func (progressTool) Toolset() string    { return tools.SetFile }
func (progressTool) ParallelSafe() bool { return false }
func (progressTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{Name: "progress", Description: "progress", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (progressTool) Run(_ context.Context, _ json.RawMessage, env *tools.Env) (string, error) {
	if env.Progress == nil {
		return "no progress", nil
	}
	env.Progress(1, "1/2", "read_file", "x")
	return "sent", nil
}

// progressDeps returns a Deps whose model calls the progress tool once.
func progressDeps(t *testing.T, newEnv func(*sessions.Session, string) *tools.Env) (*Deps, *sessions.Session) {
	t.Helper()
	fp := &fakeProv{script: []step{
		{text: "Let me ask.", calls: []provider.ToolCall{{ID: "c1", Name: "progress", Args: "{}"}}},
		{text: "done"},
	}}
	d := testDeps(t, fp)
	d.Agent.Tools.Register(progressTool{})
	d.NewEnv = newEnv
	sess, err := d.Store.Create(context.Background(), sessions.Session{Key: "cli:test", Source: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	return d, sess
}

func TestTurnSendsChildProgressToCLISink(t *testing.T) {
	d, sess := progressDeps(t, func(s *sessions.Session, _ string) *tools.Env {
		return &tools.Env{SessionID: s.ID, Values: map[string]any{}}
	})
	var out bytes.Buffer
	sink := &CLISink{W: &out}
	if _, err := d.Turn(context.Background(), sess, TurnInput{Text: "go", Channel: "cli", Sink: sink, Owner: true}); err != nil {
		t.Fatal(err)
	}
	sink.Finish()
	// The sink lock orders the line with the streamed text, and the line starts on a new line.
	if !strings.Contains(out.String(), "Let me ask.\n\x1b[2m🔧 progress...\x1b[0m\n\x1b[2m🔀 [1/2] read_file \"x\"\x1b[0m\n") {
		t.Fatalf("%q", out.String())
	}
}

func TestTurnKeepsChannelProgress(t *testing.T) {
	var got []string
	d, sess := progressDeps(t, func(s *sessions.Session, _ string) *tools.Env {
		return &tools.Env{SessionID: s.ID, Values: map[string]any{}, Progress: func(_ uint64, child, _, _ string) { got = append(got, child) }}
	})
	var out bytes.Buffer
	if _, err := d.Turn(context.Background(), sess, TurnInput{Text: "go", Channel: "cli", Sink: &CLISink{W: &out}, Owner: true}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || strings.Contains(out.String(), "🔀") {
		t.Fatalf("the progress function of the channel must stay: %q %q", got, out.String())
	}
}

// cacheProv answers "ok" with a fixed usage that has cache reads.
type cacheProv struct{}

func (cacheProv) Name() string  { return "primary" }
func (cacheProv) Model() string { return "fake-model" }
func (cacheProv) Stream(context.Context, provider.Request) (<-chan provider.Event, error) {
	ch := make(chan provider.Event, 2)
	ch <- provider.Event{Type: provider.EventText, Delta: "ok"}
	ch <- provider.Event{Type: provider.EventDone, FinishReason: "stop",
		Usage: &provider.Usage{InputTokens: 100, CacheReadTokens: 900, OutputTokens: 7}}
	close(ch)
	return ch, nil
}

func TestTurnEndLogHasCacheReadTokens(t *testing.T) {
	ctx := context.Background()
	d := providerDeps(t, sessions.New(openTestDB(t)), cacheProv{})
	var logs bytes.Buffer
	d.Log = slog.New(slog.NewTextHandler(&logs, nil))
	sess, err := d.Store.Create(ctx, sessions.Session{Key: "cli:test", Source: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Turn(ctx, sess, TurnInput{Text: "hi", Channel: "cli", Sink: &CLISink{W: io.Discard}, Owner: true}); err != nil {
		t.Fatal(err)
	}
	var end string
	for line := range strings.SplitSeq(logs.String(), "\n") {
		if strings.Contains(line, `msg="turn end"`) {
			end = line
		}
	}
	// input_tokens keeps its meaning: the input plus the cache read.
	for _, want := range []string{" input_tokens=1000 ", " cache_read_tokens=900 ", " output_tokens=7 "} {
		if !strings.Contains(end, want) {
			t.Fatalf("want %q in the turn end line: %q", want, end)
		}
	}
}

// plainTool is a tool with a name and no MCP class.
type plainTool struct{ name string }

func (p plainTool) Name() string     { return p.name }
func (plainTool) Toolset() string    { return tools.SetFile }
func (plainTool) ParallelSafe() bool { return false }
func (p plainTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{Name: p.name, Description: p.name, Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (plainTool) Run(context.Context, json.RawMessage, *tools.Env) (string, error) { return "ran", nil }

// classTool is a plainTool with an MCP class.
type classTool struct {
	plainTool
	class string
}

func (c classTool) MCPClass() string { return c.class }

func TestTurnReadOnlyHidesTools(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{security.ModeReadOnly, security.ModeFull, ""} {
		fp := &fakeProv{}
		d := testDeps(t, fp)
		for _, tl := range []tools.Tool{plainTool{"terminal"}, plainTool{"write_file"},
			classTool{plainTool{"mcp_gh_search"}, "read"}, classTool{plainTool{"mcp_gh_create_issue"}, "write"}} {
			d.Agent.Tools.Register(tl)
		}
		var scopes []string
		d.ModeOf = func(_ context.Context, scope string) string { scopes = append(scopes, scope); return mode }
		var env *tools.Env
		d.NewEnv = func(s *sessions.Session, _ string) *tools.Env {
			env = &tools.Env{SessionID: s.ID, Values: map[string]any{}}
			return env
		}
		var keys []agent.PromptKey
		d.Agent.Prompts = agent.NewPromptCache(func(_ context.Context, _ *sessions.Session, k agent.PromptKey, _ string) (string, time.Time) {
			keys = append(keys, k)
			return "P", time.Now()
		})
		sess, err := d.Store.Create(ctx, sessions.Session{Key: "cli:test", Source: "cli"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Turn(ctx, sess, TurnInput{Text: "hi", Channel: "cli", Sink: &CLISink{W: io.Discard}, Owner: true}); err != nil {
			t.Fatal(err)
		}
		names := map[string]bool{}
		for _, s := range fp.reqs[0].Tools {
			names[s.Name] = true
		}
		ro := mode == security.ModeReadOnly
		for _, denied := range []string{"terminal", "write_file", "mcp_gh_create_issue"} {
			if names[denied] == ro {
				t.Fatalf("mode %q: %s visible=%v", mode, denied, names[denied])
			}
		}
		if !names["read_file"] || !names["mcp_gh_search"] {
			t.Fatalf("mode %q: the allowed tools stay visible: %v", mode, names)
		}
		if len(scopes) != 1 || scopes[0] != "cli" || env.ModeScope != "cli" {
			t.Fatalf("mode %q: one read of the scope cli: %v %q", mode, scopes, env.ModeScope)
		}
		if len(keys) != 1 || keys[0].Mode != mode {
			t.Fatalf("mode %q: the prompt key holds the mode: %+v", mode, keys)
		}
	}
}
