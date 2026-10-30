package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
)

func TestInjectIntoLastToolResult(t *testing.T) {
	msgs := []provider.Message{{Role: provider.RoleUser, Content: "go"}, {Role: provider.RoleTool, Content: "r1"}, {Role: provider.RoleAssistant}, {Role: provider.RoleTool, Content: "r2"}}
	out, ok := InjectSteer(msgs, "also grab prices")
	if !ok || out[3].Content != "r2\n\nUser guidance: also grab prices" || out[1].Content != "r1" {
		t.Fatal(out)
	}
	if msgs[3].Content != "r2" {
		t.Fatal("InjectSteer must not change its input")
	}
}

// Hermes test_pre_api_drain_finds_tool_msg_past_assistant.
func TestInjectFindsToolPastAssistant(t *testing.T) {
	msgs := []provider.Message{{Role: provider.RoleUser, Content: "do"}, {Role: provider.RoleTool, Content: "search results"}, {Role: provider.RoleAssistant, Content: "let me check"}}
	out, ok := InjectSteer(msgs, "change approach")
	if !ok || out[1].Content != "search results\n\nUser guidance: change approach" || out[2].Content != "let me check" {
		t.Fatal(out)
	}
}

func TestNoToolMessageKeepsPending(t *testing.T) {
	_, ok := InjectSteer([]provider.Message{{Role: provider.RoleUser, Content: "go"}}, "x")
	if ok {
		t.Fatal()
	}
	if _, ok := InjectSteer(nil, "x"); ok {
		t.Fatal()
	}
}

func TestMultipleSteersJoined(t *testing.T) {
	q := NewSteerQueue()
	q.Push("a")
	q.Push("b")
	if q.Pending() != "a\nb" || q.Drain() != "a\nb" || q.Drain() != "" || q.Pending() != "" {
		t.Fatal()
	}
}

// Hermes TestSteerAcceptance: empty and blank texts are rejected, others are trimmed.
func TestPushRejectsBlankAndTrims(t *testing.T) {
	q := NewSteerQueue()
	if q.Push("") || q.Push("   \n\t  ") || q.Pending() != "" {
		t.Fatal("blank text must be rejected")
	}
	if !q.Push("  hello world  \n") || q.Pending() != "hello world" {
		t.Fatal(q.Pending())
	}
	var nilQ *SteerQueue
	if nilQ.Push("x") || nilQ.Pending() != "" || nilQ.Drain() != "" {
		t.Fatal("a nil queue accepts nothing")
	}
}

// Hermes TestSteerThreadSafety: no text is lost under concurrent pushes.
func TestConcurrentPushKeepsAll(t *testing.T) {
	q := NewSteerQueue()
	const n = 200
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() { defer wg.Done(); q.Push(fmt.Sprintf("note-%d", i)) }()
	}
	wg.Wait()
	lines := strings.Split(q.Drain(), "\n")
	seen := map[string]bool{}
	for _, l := range lines {
		seen[l] = true
	}
	if len(lines) != n || len(seen) != n {
		t.Fatal(len(lines), len(seen))
	}
}

// The restash keeps the older text before a text that arrived after the drain.
func TestRestashKeepsOrder(t *testing.T) {
	q := NewSteerQueue()
	q.Push("old")
	got := q.Drain()
	q.Push("new")
	q.restash(got)
	if q.Pending() != "old\nnew" {
		t.Fatal(q.Pending())
	}
}

func TestLoopInjectsAfterToolBatchAndPersists(t *testing.T) {
	q := NewSteerQueue()
	fp := scripted(toolCalls(tc("c1", "echo", `{"s":"x"}`)), text("ok"))
	fp.onRequest = func(i int) {
		if i == 0 {
			q.Push("skip out-of-stock")
		}
	}
	a, sess := newAgent(t, fp, echoTool{})
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: &recSink{}, Steer: q})
	if err != nil || res.PendingSteer != "" {
		t.Fatal(res, err)
	}
	if !strings.Contains(lastToolContent(fp.requests[1]), "User guidance: skip out-of-stock") {
		t.Fatal("not injected")
	}
	msgs := messages(t, a, sess.ID)
	if len(msgs) != 4 || msgs[2].Msg.Content != "x\n\nUser guidance: skip out-of-stock" {
		t.Fatal("stored tool msg must carry it; no new message:", msgs)
	}
}

// A steer that arrives after the tool batch is drained just before the next model call.
// The stored tool message is updated, so the transcript matches what the model saw.
func TestPreCallDrainUpdatesStoredToolMessage(t *testing.T) {
	q := NewSteerQueue()
	fp := scripted(toolCalls(tc("c1", "echo", `{"s":"x"}`)), text("ok"))
	a, sess := newAgent(t, fp, echoTool{})
	// The hook runs after the tool batch and before the next model call.
	a.beforeCall = func(round int) {
		if round == 2 {
			q.Push("late note")
		}
	}
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: &recSink{}, Steer: q}); err != nil {
		t.Fatal(err)
	}
	if got := lastToolContent(fp.requests[1]); got != "x\n\nUser guidance: late note" {
		t.Fatal(got)
	}
	if got := messages(t, a, sess.ID)[2].Msg.Content; got != "x\n\nUser guidance: late note" {
		t.Fatal(got)
	}
}

func TestPreCallDrainBeforeFirstToolKeepsPending(t *testing.T) {
	// Hermes test_pre_api_drain_restashes_when_no_tool_message: a steer before the first
	// tool result waits for the first tool batch.
	q := NewSteerQueue()
	q.Push("early steer")
	fp := scripted(toolCalls(tc("c1", "echo", `{"s":"x"}`)), text("ok"))
	a, sess := newAgent(t, fp, echoTool{})
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: &recSink{}, Steer: q}); err != nil {
		t.Fatal(err)
	}
	if lastToolContent(fp.requests[0]) != "" || fp.requests[0].Messages[0].Content != "go" {
		t.Fatal("no injection before a tool message exists")
	}
	if got := lastToolContent(fp.requests[1]); got != "x\n\nUser guidance: early steer" {
		t.Fatal(got)
	}
}

func TestSteerNeverTouchesEarlierTurn(t *testing.T) {
	// The tool message of an earlier turn must not change, because that breaks the prompt
	// cache and changes the old transcript.
	fp := scripted(toolCalls(tc("c1", "echo", `{"s":"old"}`)), text("one"), text("two"))
	a, sess := newAgent(t, fp, echoTool{})
	a.RunTurn(ctx, Turn{Session: sess, UserText: "first", Toolsets: all(), Sink: &recSink{}})
	q := NewSteerQueue()
	q.Push("for turn two")
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "second", Toolsets: all(), Sink: &recSink{}, Steer: q})
	if err != nil || res.PendingSteer != "for turn two" {
		t.Fatal(res, err)
	}
	if got := messages(t, a, sess.ID)[2].Msg.Content; got != "old" {
		t.Fatal(got)
	}
}

func TestUndeliveredSteerReturned(t *testing.T) {
	q := NewSteerQueue()
	fp := scripted(text("plain answer"))
	fp.onRequest = func(int) { q.Push("follow-up") }
	a, sess := newAgent(t, fp)
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "hi", Sink: &recSink{}, Steer: q})
	if err != nil || res.PendingSteer != "follow-up" || q.Pending() != "" {
		t.Fatal(res, err)
	}
	if len(messages(t, a, sess.ID)) != 2 {
		t.Fatal("a steer never inserts a message")
	}
}

func TestUndeliveredSteerReturnedAfterError(t *testing.T) {
	q := NewSteerQueue()
	fp := scripted(step{text: "x", err: errors.New("boom")})
	fp.onRequest = func(int) { q.Push("follow-up") }
	a, sess := newAgent(t, fp)
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "hi", Sink: &recSink{}, Steer: q})
	if err == nil || res.PendingSteer != "follow-up" {
		t.Fatal(res, err)
	}
}

func TestStopCancelsTurn(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "write_file", `{}`), tc("c2", "write_file", `{}`)), text("never"))
	a, sess := newAgent(t, fp, slowTool{name: "write_file", d: 5 * time.Second})
	cctx, cancel := context.WithCancel(ctx)
	q := NewSteerQueue()
	go func() { time.Sleep(100 * time.Millisecond); q.Push("dropped"); cancel() }()
	start := time.Now()
	res, err := a.RunTurn(cctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: &recSink{}, Steer: q})
	if !errors.Is(err, context.Canceled) || time.Since(start) > 2*time.Second {
		t.Fatal(res, err)
	}
	if len(fp.requests) != 1 {
		t.Fatal("no model call after stop")
	}
	// The turn returns the steer that it did not deliver. The gateway drops it for /stop.
	if res.PendingSteer != "dropped" || q.Pending() != "" {
		t.Fatalf("got %q, queue %q", res.PendingSteer, q.Pending())
	}
	msgs := messages(t, a, sess.ID) // user, assistant(2 calls), tool, tool
	if len(msgs) != 4 || msgs[2].Msg.ToolCallID != "c1" || msgs[3].Msg.ToolCallID != "c2" || !strings.Contains(msgs[3].Msg.Content, "cancelled") {
		t.Fatal("each call keeps a result after a stop:", msgs)
	}
}
