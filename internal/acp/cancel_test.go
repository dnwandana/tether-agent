package acp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
)

func hasMethod(t *testing.T, logPath, method string) bool {
	t.Helper()
	for _, e := range readFakeLog(t, logPath) {
		if e.Method == method {
			return true
		}
	}
	return false
}

// cancelOnProgress returns hooks that end the turn ctx at the first progress item.
func cancelOnProgress(cancel context.CancelFunc) Hooks {
	return Hooks{Progress: func(string, string) { cancel() }, ApprovalTimeout: time.Minute}
}

func TestCancelWaitValue(t *testing.T) {
	if cancelWait != 10*time.Second {
		t.Fatalf("cancelWait = %v, want 10s", cancelWait)
	}
	if ErrSessionClosed.Error() != "acp: the coding agent session was closed" {
		t.Fatalf("ErrSessionClosed = %q", ErrSessionClosed)
	}
}

func TestPromptCancelWaitsForCancelled(t *testing.T) {
	c, info, logPath := startFakeSession(t, `{"modes":["auto"],"turns":[
	  {"steps":[{"tool_call":{"id":"t1","kind":"read","title":"read a.go"}},{"wait_cancel":true}],"stop_reason":"cancelled"},
	  {"steps":[{"message":"second"}],"stop_reason":"end_turn"}]}`)
	ctx, cancel := context.WithCancel(context.Background())
	res, err := c.Prompt(ctx, info.ID, "task", cancelOnProgress(cancel))
	if err != nil || res.StopReason != "cancelled" || res.ToolCalls != 1 {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
	if !hasMethod(t, logPath, "session/cancel") {
		t.Fatal("no session/cancel in the fake log")
	}
	select {
	case <-c.Done():
		t.Fatal("a cancel that the agent obeys must not stop the process")
	default:
	}
	res, err = c.Prompt(context.Background(), info.ID, "next", Hooks{})
	if err != nil || res.StopReason != "end_turn" || res.Text != "second" {
		t.Fatalf("second turn = %+v, err = %v", res, err)
	}
}

func TestPromptCancelTimeoutStopsProcess(t *testing.T) {
	// The fake agent obeys session/cancel in each wait step. It cannot end the turn while
	// a permission request is open, so an Approve hook that ignores its ctx keeps the turn
	// open after the cancel.
	c, info, _ := startFakeSession(t, `{"modes":["auto"],"turns":[
	  {"steps":[{"permission":{"tool_call":{"id":"t1","kind":"execute","title":"ls","raw_input":{"command":"ls"}},`+permOptions+`}},
	   {"wait_cancel":true}],"stop_reason":"cancelled"}]}`)
	c.cancelWaitOverride = 300 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	h := Hooks{
		Approve: func(context.Context, security.ApprovalRequest) (security.Scope, error) {
			cancel()
			time.Sleep(1500 * time.Millisecond)
			return security.ScopeOnce, nil
		},
		ApprovalTimeout: time.Minute,
	}
	errc := make(chan error, 1)
	go func() {
		_, err := c.Prompt(ctx, info.ID, "task", h)
		errc <- err
	}()
	<-ctx.Done()
	time.Sleep(100 * time.Millisecond)
	if !c.Busy() {
		t.Fatal("the client must stay busy while it waits for the cancel")
	}
	select {
	case err := <-errc:
		if err == nil || !strings.Contains(err.Error(), "did not end the turn") {
			t.Fatalf("err = %v, want the cancel timeout error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Prompt did not return after the cancel wait")
	}
	waitFor(t, func() bool {
		select {
		case <-c.Done():
			return true
		default:
			return false
		}
	})
}

func TestPromptCancelAnswersOpenPermission(t *testing.T) {
	c, info, logPath := startFakeSession(t, `{"modes":["auto"],"turns":[
	  {"steps":[{"permission":{"tool_call":{"id":"t1","kind":"execute","title":"rm","raw_input":{"command":"rm -rf x"}},`+permOptions+`}},
	   {"wait_cancel":true}],"stop_reason":"cancelled"}]}`)
	ctx, cancel := context.WithCancel(context.Background())
	h := Hooks{
		Approve: func(actx context.Context, _ security.ApprovalRequest) (security.Scope, error) {
			cancel() // The user sends /stop while the approval shows.
			<-actx.Done()
			return security.ScopeTimeout, actx.Err()
		},
		ApprovalTimeout: time.Minute,
	}
	res, err := c.Prompt(ctx, info.ID, "task", h)
	if err != nil || res.StopReason != "cancelled" {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
	var outcome string
	for _, e := range readFakeLog(t, logPath) {
		if e.Outcome != "" {
			outcome = e.Outcome
		}
	}
	if outcome != "cancelled" {
		t.Fatalf("permission outcome = %q, want cancelled", outcome)
	}
}

func TestAbortBusyClient(t *testing.T) {
	c, info, _ := startFakeSession(t, `{"modes":["auto"],"turns":[
	  {"steps":[{"tool_call":{"id":"t1","kind":"read","title":"read a.go"}},{"sleep_ms":30000}],"stop_reason":"end_turn"}]}`)
	started := make(chan struct{})
	var once bool
	h := Hooks{Progress: func(string, string) {
		if !once {
			once = true
			close(started)
		}
	}}
	errc := make(chan error, 1)
	go func() {
		_, err := c.Prompt(context.Background(), info.ID, "task", h)
		errc <- err
	}()
	<-started
	begin := time.Now()
	c.Abort()
	select {
	case err := <-errc:
		if !errors.Is(err, ErrSessionClosed) {
			t.Fatalf("err = %v, want ErrSessionClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Prompt did not return after Abort")
	}
	if d := time.Since(begin); d > 4*time.Second {
		t.Fatalf("Abort took %v; it must not wait for the cancel", d)
	}
	<-c.Done()
}
