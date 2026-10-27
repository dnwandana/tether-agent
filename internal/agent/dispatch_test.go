package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// slowTool sleeps for d and returns its name and the call number.
type slowTool struct {
	name string
	safe bool
	d    time.Duration
}

func (s slowTool) Name() string       { return s.name }
func (s slowTool) Toolset() string    { return tools.SetFile }
func (s slowTool) ParallelSafe() bool { return s.safe }
func (s slowTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{Name: s.name, Description: "slow", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (s slowTool) Run(ctx context.Context, args json.RawMessage, _ *tools.Env) (string, error) {
	select {
	case <-time.After(s.d):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return s.name + ":" + string(args), nil
}

// agentWith returns an Agent with the tools ts and no provider.
func agentWith(t *testing.T, ts ...tools.Tool) *Agent {
	t.Helper()
	reg := tools.NewRegistry()
	for _, tool := range ts {
		reg.Register(tool)
	}
	cfg := config.Default()
	return &Agent{Tools: reg, Cfg: &cfg, SpillDir: t.TempDir()}
}

func env() *tools.Env { return &tools.Env{Values: map[string]any{}} }

func vis(names ...string) map[string]bool {
	out := map[string]bool{}
	for _, n := range names {
		out[n] = true
	}
	return out
}

func TestParallelOnlyWhenAllSafe(t *testing.T) {
	a := agentWith(t, slowTool{name: "read_file", safe: true, d: 200 * time.Millisecond})
	start := time.Now()
	calls := []provider.ToolCall{tc("1", "read_file", `{"n":1}`), tc("2", "read_file", `{"n":2}`), tc("3", "read_file", `{"n":3}`), tc("4", "read_file", `{"n":4}`)}
	res := a.runRound(ctx, calls, env(), vis("read_file"), &recSink{}, "")
	if time.Since(start) > 350*time.Millisecond || len(res) != 4 {
		t.Fatal("4 safe calls must overlap (concurrency 4)")
	}
	for i, r := range res {
		if r.Text != fmt.Sprintf(`read_file:{"n":%d}`, i+1) {
			t.Fatal("results must be in call order:", res)
		}
	}
}

func TestConcurrencyLimit(t *testing.T) {
	a := agentWith(t, slowTool{name: "read_file", safe: true, d: 100 * time.Millisecond})
	a.Cfg.Agent.ToolConcurrency = 2
	calls := []provider.ToolCall{tc("1", "read_file", `{}`), tc("2", "read_file", `{}`), tc("3", "read_file", `{}`), tc("4", "read_file", `{}`)}
	start := time.Now()
	a.runRound(ctx, calls, env(), vis("read_file"), &recSink{}, "")
	if d := time.Since(start); d < 190*time.Millisecond || d > 350*time.Millisecond {
		t.Fatal("4 calls with concurrency 2 take 2 slots of time:", d)
	}
}

func TestMixedRoundRunsInOrder(t *testing.T) {
	a := agentWith(t, slowTool{name: "read_file", safe: true, d: 100 * time.Millisecond}, slowTool{name: "write_file", safe: false, d: 100 * time.Millisecond})
	start := time.Now()
	a.runRound(ctx, []provider.ToolCall{tc("1", "read_file", `{}`), tc("2", "write_file", `{}`)}, env(), vis("read_file", "write_file"), &recSink{}, "")
	if time.Since(start) < 190*time.Millisecond {
		t.Fatal("mixed round must be sequential")
	}
}

func TestUnknownToolMakesRoundSequential(t *testing.T) {
	a := agentWith(t, slowTool{name: "read_file", safe: true, d: 100 * time.Millisecond})
	start := time.Now()
	res := a.runRound(ctx, []provider.ToolCall{tc("1", "read_file", `{}`), tc("2", "nope", `{}`), tc("3", "read_file", `{}`)}, env(), vis("read_file"), &recSink{}, "")
	if time.Since(start) < 190*time.Millisecond || !res[1].IsError || res[0].IsError || res[2].IsError {
		t.Fatal(res)
	}
}

// unsafeSink fails the test when two calls overlap, because sinks are not thread-safe.
type unsafeSink struct {
	nopSink
	busy   atomic.Bool
	failed atomic.Bool
}

func (u *unsafeSink) enter() {
	if !u.busy.CompareAndSwap(false, true) {
		u.failed.Store(true)
	}
	time.Sleep(5 * time.Millisecond)
	u.busy.Store(false)
}
func (u *unsafeSink) ToolStart(_, _ string)    { u.enter() }
func (u *unsafeSink) ToolEnd(_ string, _ bool) { u.enter() }

func TestParallelSinkCallsSerialized(t *testing.T) {
	a := agentWith(t, slowTool{name: "read_file", safe: true, d: 10 * time.Millisecond})
	s := &unsafeSink{}
	calls := []provider.ToolCall{tc("1", "read_file", `{}`), tc("2", "read_file", `{}`), tc("3", "read_file", `{}`), tc("4", "read_file", `{}`)}
	a.runRound(ctx, calls, env(), vis("read_file"), s, "")
	if s.failed.Load() {
		t.Fatal("sink calls overlap")
	}
}

func TestCancelledRoundGivesResultForEachCall(t *testing.T) {
	a := agentWith(t, slowTool{name: "write_file", d: time.Second})
	cctx, cancel := context.WithCancel(ctx)
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	res := a.runRound(cctx, []provider.ToolCall{tc("1", "write_file", `{}`), tc("2", "write_file", `{}`)}, env(), vis("write_file"), &recSink{}, "")
	if len(res) != 2 || !res[0].IsError || !res[1].IsError || !strings.Contains(res[1].Text, "cancelled") {
		t.Fatal(res)
	}
}

func TestFailureTracker(t *testing.T) {
	var f failureTracker
	for range 2 {
		if f.Observe(kindUnknownTool) {
			t.Fatal("2 failures do not stop")
		}
	}
	if f.Observe("") || f.Observe(kindUnknownTool) || f.Observe(kindInvalidJSON) || f.Observe(kindUnknownTool) {
		t.Fatal("a success or another kind resets the count")
	}
	if f.Observe(kindUnknownTool) || !f.Observe(kindUnknownTool) {
		t.Fatal("3 of the same kind in a row stop")
	}
	if !strings.Contains(f.Err().Error(), "the model called an unknown tool 3 times in a row") {
		t.Fatal(f.Err())
	}
}

func TestThreeSameFailuresEndTurn(t *testing.T) {
	fp := scriptedForever(toolCalls(tc("c", "nope", `{}`)))
	a, sess := newAgent(t, fp)
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "x", Toolsets: all(), Sink: &recSink{}})
	if err == nil || !strings.Contains(err.Error(), "the model called an unknown tool 3 times in a row") || len(fp.requests) != 3 {
		t.Fatal(res, err)
	}
	msgs := messages(t, a, sess.ID)
	if last := msgs[len(msgs)-1].Msg; last.Role != provider.RoleTool || !strings.Contains(last.Content, "unknown tool") {
		t.Fatal("each tool call keeps its result:", msgs)
	}
}

func TestThreeInvalidJSONEndTurn(t *testing.T) {
	fp := scriptedForever(toolCalls(tc("c", "echo", `{"s":`)))
	a, sess := newAgent(t, fp, echoTool{})
	_, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "x", Toolsets: all(), Sink: &recSink{}})
	if err == nil || !strings.Contains(err.Error(), "sent invalid JSON arguments 3 times in a row") || len(fp.requests) != 3 {
		t.Fatal(err)
	}
}

func TestHiddenToolCountsAsUnknown(t *testing.T) {
	fp := scriptedForever(toolCalls(tc("c", "echo", `{"s":"x"}`)))
	a, sess := newAgent(t, fp, echoTool{})
	_, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "x", Toolsets: map[string]bool{tools.SetWeb: true}, Sink: &recSink{}})
	if err == nil || !strings.Contains(err.Error(), "unknown tool 3 times") {
		t.Fatal(err)
	}
}

func TestToolErrorDoesNotCount(t *testing.T) {
	// A call that the tool rejects is a valid call. It must not stop the turn.
	fp := scripted(toolCalls(tc("c1", "echo", `[]`)), toolCalls(tc("c2", "echo", `[]`)), toolCalls(tc("c3", "echo", `[]`)), text("done"))
	a, sess := newAgent(t, fp, echoTool{})
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "x", Toolsets: all(), Sink: &recSink{}})
	if err != nil || res.Final != "done" {
		t.Fatal(res, err)
	}
}

func TestParallelResultsSavedInCallOrder(t *testing.T) {
	st := slowTool{name: "read_file", safe: true, d: 0}
	fp := scripted(toolCalls(tc("a", "read_file", `{"n":1}`), tc("b", "read_file", `{"n":2}`), tc("c", "read_file", `{"n":3}`)), text("done"))
	a, sess := newAgent(t, fp, st)
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "x", Toolsets: all(), Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	msgs := messages(t, a, sess.ID)
	for i, id := range []string{"a", "b", "c"} {
		if m := msgs[2+i].Msg; m.ToolCallID != id || m.Content != fmt.Sprintf(`read_file:{"n":%d}`, i+1) {
			t.Fatal(msgs)
		}
	}
}
