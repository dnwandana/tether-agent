package acp

import (
	"context"
	"encoding/json"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
)

// scriptFromJSON decodes a fake-agent script from its JSON form.
func scriptFromJSON(t *testing.T, js string) fakeScript {
	t.Helper()
	var s fakeScript
	if err := json.Unmarshal([]byte(js), &s); err != nil {
		t.Fatalf("bad fake script: %v", err)
	}
	return s
}

// startFakeSession starts the fake agent with the script js and opens one session.
func startFakeSession(t *testing.T, js string) (*Client, SessionInfo, string) {
	t.Helper()
	a, logPath := fakeAgentConfig(t, scriptFromJSON(t, js))
	dir := t.TempDir()
	c, err := Start(context.Background(), StartOptions{AgentID: "claude-code", Agent: a, Dir: dir,
		ClientVersion: "test", Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Stop() })
	info, err := c.NewSession(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return c, info, logPath
}

// waitFor fails the test when cond is not true within 5 seconds.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the condition is not true after 5 s")
}

// raceEnabled reports whether the test binary has the race detector.
func raceEnabled() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range bi.Settings {
		if s.Key == "-race" {
			return s.Value == "true"
		}
	}
	return false
}

// recorder records the hook calls of one turn.
type recorder struct {
	mu        sync.Mutex
	progress  []string
	approvals int
}

func (r *recorder) hooks(scope security.Scope) Hooks {
	return Hooks{
		Progress: func(kind, title string) {
			r.mu.Lock()
			r.progress = append(r.progress, kind+" "+title)
			r.mu.Unlock()
		},
		Approve: func(context.Context, security.ApprovalRequest) (security.Scope, error) {
			r.mu.Lock()
			r.approvals++
			r.mu.Unlock()
			return scope, nil
		},
		ApprovalTimeout: time.Minute,
	}
}

func (r *recorder) snapshot() ([]string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.progress...), r.approvals
}

const permOptions = `"options":[{"id":"allow-once","name":"Allow","kind":"allow_once"},` +
	`{"id":"allow-with-updates","name":"Always","kind":"allow_always"},` +
	`{"id":"reject","name":"Reject","kind":"reject_once"}]`

func TestPromptSizes(t *testing.T) {
	if progressQueueSize != 64 {
		t.Fatalf("progressQueueSize = %d, want 64", progressQueueSize)
	}
	if messageRingMax != 64<<10 {
		t.Fatalf("messageRingMax = %d, want 65536", messageRingMax)
	}
}

func TestPromptResult(t *testing.T) {
	c, info, logPath := startFakeSession(t, `{"modes":["default","acceptEdits","auto","bypassPermissions"],
	 "turns":[{"steps":[
	  {"tool_call":{"id":"t1","kind":"read","title":"read a.go"}},
	  {"tool_call":{"id":"t2","kind":"","title":"think"}},
	  {"permission":{"tool_call":{"id":"t3","kind":"execute","title":"go test","raw_input":{"command":"go test ./..."}},`+permOptions+`}},
	  {"mode_update":"acceptEdits"},
	  {"sleep_ms":300},
	  {"message":"all "},
	  {"message":"done"}],"stop_reason":"end_turn"}]}`)
	var r recorder
	before := time.Now()
	type out struct {
		res TurnResult
		err error
	}
	ch := make(chan out, 1)
	go func() {
		res, err := c.Prompt(context.Background(), info.ID, "fix the bug", r.hooks(security.ScopeOnce))
		ch <- out{res, err}
	}()
	waitFor(t, c.Busy)
	o := <-ch
	if o.err != nil {
		t.Fatal(o.err)
	}
	want := TurnResult{StopReason: "end_turn", Text: "all done", ToolCalls: 2, Mode: "acceptEdits"}
	if o.res != want {
		t.Fatalf("result = %+v, want %+v", o.res, want)
	}
	// Prompt empties the progress queue before it returns. An empty kind is "other".
	progress, approvals := r.snapshot()
	if strings.Join(progress, "|") != "read read a.go|other think" || approvals != 1 {
		t.Fatalf("progress = %q, approvals = %d", progress, approvals)
	}
	if c.Busy() || c.LastUse().Before(before) {
		t.Fatalf("busy = %v, last use = %v", c.Busy(), c.LastUse())
	}
	var outcome string
	for _, e := range readFakeLog(t, logPath) {
		if e.Outcome != "" {
			outcome = e.Outcome
		}
	}
	if outcome != "selected:allow-once" {
		t.Fatalf("permission outcome = %q", outcome)
	}
}

func TestPromptMessageRing(t *testing.T) {
	big := strings.Repeat("a", 70*1024)
	c, info, _ := startFakeSession(t, `{"modes":["auto"],"turns":[{"steps":[
	  {"message":"`+big+`"},{"message":"END"}],"stop_reason":"end_turn"}]}`)
	res, err := c.Prompt(context.Background(), info.ID, "loop", Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Text) > 64<<10 || len(res.Text) < 64<<10-4 || !strings.HasSuffix(res.Text, "END") {
		t.Fatalf("text has %d bytes, suffix %q", len(res.Text), res.Text[len(res.Text)-3:])
	}
}

// A progress item or a permission request after the end of a turn must not reach the hooks
// of that turn.
func TestPromptLateEventsSkipFinishedTurn(t *testing.T) {
	c, info, logPath := startFakeSession(t, `{"modes":["auto"],
	 "turns":[{"steps":[
	  {"tool_call":{"id":"t1","kind":"read","title":"read a.go"}},
	  {"message":"done"}],"stop_reason":"end_turn"}],
	 "after_turn":[
	  {"tool_call":{"id":"t2","kind":"edit","title":"late edit"}},
	  {"permission":{"tool_call":{"id":"t3","kind":"execute","title":"late run","raw_input":{"command":"ls"}},`+permOptions+`}}]}`)
	var r recorder
	if _, err := c.Prompt(context.Background(), info.ID, "task", r.hooks(security.ScopeOnce)); err != nil {
		t.Fatal(err)
	}
	var outcome string
	waitFor(t, func() bool {
		for _, e := range readFakeLog(t, logPath) {
			if e.Outcome != "" {
				outcome = e.Outcome
				return true
			}
		}
		return false
	})
	if outcome != "cancelled" {
		t.Fatalf("late permission outcome = %q, want cancelled", outcome)
	}
	time.Sleep(100 * time.Millisecond)
	progress, approvals := r.snapshot()
	if strings.Join(progress, "|") != "read read a.go" || approvals != 0 {
		t.Fatalf("late events reached the hooks: progress = %q, approvals = %d", progress, approvals)
	}
}

// 2,000 fast tool_call updates must not block the SDK reader or close the connection.
func TestPromptFloodDoesNotBlock(t *testing.T) {
	if raceEnabled() {
		// The race detector makes the SDK decode of each update slower than its reader. Then
		// more than 1024 updates wait and the SDK closes the connection, also with no turn
		// handler.
		t.Skip("the SDK cannot decode 2,000 fast updates in time with the race detector")
	}
	c, info, _ := startFakeSession(t, `{"modes":["auto"],"turns":[{"steps":[
	  {"flood":2000},{"message":"ok"}],"stop_reason":"end_turn"}]}`)
	var mu sync.Mutex
	calls := 0
	h := Hooks{Progress: func(string, string) {
		time.Sleep(time.Millisecond) // A slow consumer fills the queue.
		mu.Lock()
		calls++
		mu.Unlock()
	}}
	res, err := c.Prompt(context.Background(), info.ID, "flood", h)
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != "end_turn" || res.Text != "ok" || res.ToolCalls != 2000 {
		t.Fatalf("result = %+v", res)
	}
	mu.Lock()
	n := calls
	mu.Unlock()
	if n < 1 || n > 2000 {
		t.Fatalf("progress calls = %d", n)
	}
	select {
	case <-c.Done():
		t.Fatal("the flood stopped the process")
	default:
	}
}
