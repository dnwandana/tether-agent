package acp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
)

func execInput(title string) permissionInput {
	return permissionInput{SessionID: "acp-1", ToolCallID: "t-" + title, Kind: "execute", Title: title,
		Command: "go test ./...", Options: claudeOptions}
}

func approveWith(scope security.Scope, err error) Hooks {
	return Hooks{Approve: func(context.Context, security.ApprovalRequest) (security.Scope, error) { return scope, err }}
}

func TestBridgeMapsApproveResult(t *testing.T) {
	allow, reject, cancelled := permissionOutcome{OptionID: "allow-once"}, permissionOutcome{OptionID: "reject"}, permissionOutcome{Cancelled: true}
	for _, tc := range []struct {
		name  string
		scope security.Scope
		err   error
		want  permissionOutcome
	}{
		{"once", security.ScopeOnce, nil, allow},
		{"session", security.ScopeSession, nil, allow},
		{"deny", security.ScopeDeny, nil, reject},
		{"own timeout", security.ScopeTimeout, nil, reject},
		{"prompter error", security.ScopeDeny, errors.New("send failed"), reject},
		{"ctx canceled", security.ScopeTimeout, context.Canceled, cancelled},
		{"ctx deadline", security.ScopeTimeout, context.DeadlineExceeded, cancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBridge(context.Background(), "claude-code", approveWith(tc.scope, tc.err), nil)
			if got := b.decide(context.Background(), "acp-1", execInput("x")); got != tc.want {
				t.Fatalf("decide() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestBridgeSendsTheRequest(t *testing.T) {
	var got security.ApprovalRequest
	b := newBridge(context.Background(), "claude-code", Hooks{Approve: func(_ context.Context, r security.ApprovalRequest) (security.Scope, error) {
		got = r
		return security.ScopeOnce, nil
	}}, nil)
	b.decide(context.Background(), "acp-1", execInput("Run go test"))
	if got.PatternID != "acp:claude-code:acp-1:execute:go" || !got.NoAlways || got.Summary != "Run go test" ||
		got.Description != "claude-code wants to run a command" {
		t.Fatalf("request = %+v", got)
	}
}

func TestBridgeNilApproveRejects(t *testing.T) {
	b := newBridge(context.Background(), "claude-code", Hooks{}, nil)
	if got := b.decide(context.Background(), "acp-1", execInput("x")); got != (permissionOutcome{OptionID: "reject"}) {
		t.Fatalf("decide() = %+v", got)
	}
}

// blockApprove waits for the end of its ctx, like a Prompter with no answer.
func blockApprove(calls *atomic.Int32) Hooks {
	return Hooks{Approve: func(ctx context.Context, _ security.ApprovalRequest) (security.Scope, error) {
		calls.Add(1)
		<-ctx.Done()
		return security.ScopeTimeout, ctx.Err()
	}}
}

func TestBridgeApproveCtxEnds(t *testing.T) {
	t.Run("turn ctx", func(t *testing.T) {
		turnCtx, endTurn := context.WithCancel(context.Background())
		var calls atomic.Int32
		b := newBridge(turnCtx, "claude-code", blockApprove(&calls), nil)
		time.AfterFunc(50*time.Millisecond, endTurn)
		if got := b.decide(context.Background(), "acp-1", execInput("x")); !got.Cancelled {
			t.Fatalf("decide() = %+v, want cancelled", got)
		}
	})
	t.Run("handler ctx", func(t *testing.T) {
		hctx, cancel := context.WithCancel(context.Background())
		var calls atomic.Int32
		b := newBridge(context.Background(), "claude-code", blockApprove(&calls), nil)
		time.AfterFunc(50*time.Millisecond, cancel)
		if got := b.decide(hctx, "acp-1", execInput("x")); !got.Cancelled {
			t.Fatalf("decide() = %+v, want cancelled", got)
		}
	})
}

func TestBridgeOneApprovalAtATime(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	var active, maxActive int
	var order []string
	b := newBridge(context.Background(), "claude-code", Hooks{Approve: func(_ context.Context, r security.ApprovalRequest) (security.Scope, error) {
		mu.Lock()
		active++
		maxActive = max(maxActive, active)
		order = append(order, "start:"+r.Summary)
		mu.Unlock()
		<-release
		mu.Lock()
		active--
		order = append(order, "end:"+r.Summary)
		mu.Unlock()
		return security.ScopeOnce, nil
	}}, nil)
	var wg sync.WaitGroup
	for _, title := range []string{"first", "second"} {
		wg.Go(func() {
			if got := b.decide(context.Background(), "acp-1", execInput(title)); got.OptionID != "allow-once" {
				t.Errorf("%s: decide() = %+v", title, got)
			}
		})
	}
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	if len(order) != 1 {
		t.Errorf("before the first answer: %v, want one open prompt", order)
	}
	mu.Unlock()
	close(release)
	wg.Wait()
	if maxActive != 1 || len(order) != 4 || !strings.HasPrefix(order[1], "end:") || !strings.HasPrefix(order[2], "start:") {
		t.Fatalf("order = %v, max open = %d", order, maxActive)
	}
}

func TestBridgeCancelAll(t *testing.T) {
	var calls atomic.Int32
	b := newBridge(context.Background(), "claude-code", blockApprove(&calls), nil)
	results := make(chan permissionOutcome, 2)
	for range 2 {
		go func() { results <- b.decide(context.Background(), "acp-1", execInput("x")) }()
	}
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Approve was not called")
		}
		time.Sleep(10 * time.Millisecond)
	}
	b.cancelAll()
	for range 2 {
		select {
		case got := <-results:
			if !got.Cancelled {
				t.Fatalf("decide() = %+v, want cancelled", got)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("a request did not end after cancelAll")
		}
	}
	if got := b.decide(context.Background(), "acp-1", execInput("y")); !got.Cancelled {
		t.Fatalf("decide() after cancelAll = %+v", got)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("Approve calls = %d, want 1", n)
	}
}

func TestBridgeCancelWinsOverLateAllow(t *testing.T) {
	var b *bridge
	b = newBridge(context.Background(), "claude-code", Hooks{Approve: func(context.Context, security.ApprovalRequest) (security.Scope, error) {
		b.cancelAll()
		return security.ScopeOnce, nil
	}}, nil)
	if got := b.decide(context.Background(), "acp-1", execInput("x")); !got.Cancelled {
		t.Fatalf("decide() = %+v, want cancelled", got)
	}
}

func TestBridgeAskFallback(t *testing.T) {
	long := strings.Repeat("x", 60)
	opts := []option{
		{ID: "always", Name: "Always allow", Kind: "allow_always"},
		{ID: "once", Name: "Allow\nonce *now*", Kind: "allow_once"},
		{ID: "never", Name: "Reject always", Kind: "reject_always"},
		{ID: "long", Name: long, Kind: "allow_once"},
	}
	in := permissionInput{Kind: "other", Title: "Strange\nrequest", Options: opts}
	wantChoices := []string{"Allow once *now*", "Reject always", strings.Repeat("x", 40)}
	ask := func(answer string, err error, gotChoices *[]string, gotTimeout *time.Duration, gotText *string) Hooks {
		return Hooks{ApprovalTimeout: 10 * time.Minute, Ask: func(_ context.Context, text string, choices []string, timeout time.Duration) (string, error) {
			*gotText, *gotChoices, *gotTimeout = text, choices, timeout
			return answer, err
		}}
	}
	for _, tc := range []struct {
		name   string
		answer string
		err    error
		want   permissionOutcome
	}{
		{"choice", "Allow once *now*", nil, permissionOutcome{OptionID: "once"}},
		{"cut choice", strings.Repeat("x", 40), nil, permissionOutcome{OptionID: "long"}},
		{"no answer", "", nil, permissionOutcome{OptionID: "never"}},
		{"free text", "yes please", nil, permissionOutcome{OptionID: "never"}},
		{"allow_always name", "Always allow", nil, permissionOutcome{OptionID: "never"}},
		{"error", "", errors.New("no input"), permissionOutcome{OptionID: "never"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var choices []string
			var timeout time.Duration
			var text string
			b := newBridge(context.Background(), "claude-code", ask(tc.answer, tc.err, &choices, &timeout, &text), nil)
			if got := b.decide(context.Background(), "acp-1", in); got != tc.want {
				t.Fatalf("decide() = %+v, want %+v", got, tc.want)
			}
			if !reflect.DeepEqual(choices, wantChoices) || timeout != 10*time.Minute ||
				text != "claude-code asks for a permission: Strange request" {
				t.Fatalf("Ask got %q, %v, %s", text, choices, timeout)
			}
		})
	}
	t.Run("nil Ask", func(t *testing.T) {
		b := newBridge(context.Background(), "claude-code", Hooks{}, nil)
		if got := b.decide(context.Background(), "acp-1", in); got != (permissionOutcome{OptionID: "never"}) {
			t.Fatalf("decide() = %+v", got)
		}
	})
	t.Run("no reject option", func(t *testing.T) {
		noReject := permissionInput{Kind: "other", Options: []option{
			{ID: "always", Name: "Always", Kind: "allow_always"}, {ID: "once", Name: "Once", Kind: "allow_once"}}}
		var choices []string
		var timeout time.Duration
		var text string
		b := newBridge(context.Background(), "claude-code", ask("", nil, &choices, &timeout, &text), nil)
		if got := b.decide(context.Background(), "acp-1", noReject); !got.Cancelled {
			t.Fatalf("decide() = %+v, want cancelled", got)
		}
	})
	t.Run("turn ends during Ask", func(t *testing.T) {
		turnCtx, endTurn := context.WithCancel(context.Background())
		b := newBridge(turnCtx, "claude-code", Hooks{Ask: func(ctx context.Context, _ string, _ []string, _ time.Duration) (string, error) {
			endTurn()
			<-ctx.Done()
			return "", ctx.Err()
		}}, nil)
		if got := b.decide(context.Background(), "acp-1", in); !got.Cancelled {
			t.Fatalf("decide() = %+v, want cancelled", got)
		}
	})
}

// cmdInput is an execute request for command with the claude options.
func cmdInput(command string) permissionInput {
	in := execInput("x")
	in.Command = command
	return in
}

// scopePrompter answers each approval with answer and counts the prompts.
type scopePrompter struct {
	answer security.Scope
	n      int
}

func (p *scopePrompter) PromptApproval(context.Context, security.ApprovalRequest) (security.Scope, error) {
	p.n++
	return p.answer, nil
}

func TestBridgeHardlineRejects(t *testing.T) {
	var approves int
	var events []string
	var buf bytes.Buffer
	h := Hooks{
		Approve: func(context.Context, security.ApprovalRequest) (security.Scope, error) {
			approves++
			return security.ScopeOnce, nil
		},
		Progress: func(kind, title string) { events = append(events, kind+"|"+title) },
	}
	b := newBridge(context.Background(), "claude-code", h, slog.New(slog.NewTextHandler(&buf, nil)))
	if got := b.decide(context.Background(), "acp-1", cmdInput("rm -rf /")); got != (permissionOutcome{OptionID: "reject"}) {
		t.Fatalf("decide() = %+v", got)
	}
	if approves != 0 || !slices.Equal(events, []string{"blocked|hardline: recursive delete of root filesystem"}) {
		t.Fatal(approves, events)
	}
	if s := buf.String(); !strings.Contains(s, "level=WARN") || !strings.Contains(s, "recursive_delete_root_filesystem") {
		t.Fatal(s)
	}
}

func TestBridgeHardlineAfterSessionGrant(t *testing.T) {
	g := &security.Gate{Timeout: time.Second}
	p := &scopePrompter{answer: security.ScopeSession}
	b := newBridge(context.Background(), "claude-code", Hooks{Approve: func(ctx context.Context, r security.ApprovalRequest) (security.Scope, error) {
		r.SessionID = "s1"
		return g.AskACP(ctx, r, p)
	}}, nil)
	ctx := context.Background()
	if got := b.decide(ctx, "acp-1", cmdInput("rm notes.txt")); got.OptionID != "allow-once" || p.n != 1 {
		t.Fatal(got, p.n)
	}
	// The program-word grant covers a plain rm.
	if got := b.decide(ctx, "acp-1", cmdInput("rm old.txt")); got.OptionID != "allow-once" || p.n != 1 {
		t.Fatal(got, p.n)
	}
	// A dangerous rm asks each time.
	if got := b.decide(ctx, "acp-1", cmdInput("rm -r build")); got.OptionID != "allow-once" || p.n != 2 {
		t.Fatal(got, p.n)
	}
	// A hardline rm is refused with no prompt, also after the grant.
	if got := b.decide(ctx, "acp-1", cmdInput("rm -rf ~")); got != (permissionOutcome{OptionID: "reject"}) || p.n != 2 {
		t.Fatal(got, p.n)
	}
}

func TestBridgeHardlineSkipsAsk(t *testing.T) {
	var asks int
	b := newBridge(context.Background(), "claude-code", Hooks{Ask: func(context.Context, string, []string, time.Duration) (string, error) {
		asks++
		return "Allow", nil
	}}, nil)
	in := permissionInput{Kind: "execute", Command: "mkfs.ext4 /dev/sdb1", Options: []option{
		{ID: "always", Name: "Allow", Kind: "allow_always"}, {ID: "never", Name: "Reject", Kind: "reject_always"}}}
	if got := b.decide(context.Background(), "acp-1", in); got != (permissionOutcome{OptionID: "never"}) || asks != 0 {
		t.Fatal(got, asks)
	}
	in.Options = []option{{ID: "always", Name: "Allow", Kind: "allow_always"}}
	if got := b.decide(context.Background(), "acp-1", in); !got.Cancelled || asks != 0 {
		t.Fatal(got, asks)
	}
}

func TestBridgeDangerousCommandAsksWithNoSession(t *testing.T) {
	var got security.ApprovalRequest
	b := newBridge(context.Background(), "claude-code", Hooks{Approve: func(_ context.Context, r security.ApprovalRequest) (security.Scope, error) {
		got = r
		return security.ScopeOnce, nil
	}}, nil)
	if out := b.decide(context.Background(), "acp-1", cmdInput("rm -r build")); out.OptionID != "allow-once" {
		t.Fatal(out)
	}
	if !got.NoSession || got.Description != "claude-code wants to run a command (dangerous: recursive delete)" {
		t.Fatalf("%+v", got)
	}
}

func TestTurnStateSendsTheHardlineEvent(t *testing.T) {
	var events []string
	h := Hooks{Progress: func(kind, title string) { events = append(events, kind+"|"+title) }}
	ts := newTurnState(context.Background(), "claude-code", "acp-1", h, nil)
	go ts.runProgress(h.Progress)
	if got := ts.onPermission(context.Background(), cmdInput("rm -rf /")); got != (permissionOutcome{OptionID: "reject"}) {
		t.Fatal(got)
	}
	ts.finish()
	if !slices.Equal(events, []string{"blocked|hardline: recursive delete of root filesystem"}) {
		t.Fatal(events)
	}
}

func TestBridgeHardlineAnyKind(t *testing.T) {
	for _, kind := range []string{"other", ""} {
		var approves, asks int
		b := newBridge(context.Background(), "claude-code", Hooks{
			Approve: func(context.Context, security.ApprovalRequest) (security.Scope, error) {
				approves++
				return security.ScopeOnce, nil
			},
			Ask: func(context.Context, string, []string, time.Duration) (string, error) {
				asks++
				return "Allow", nil
			},
		}, nil)
		in := cmdInput("mkfs.ext4 /dev/sda1")
		in.Kind = kind
		if got := b.decide(context.Background(), "acp-1", in); got != (permissionOutcome{OptionID: "reject"}) || approves != 0 || asks != 0 {
			t.Errorf("kind %q: decide() = %+v, approves %d, asks %d", kind, got, approves, asks)
		}
	}
}

func TestBridgeDangerousOtherKindAsksWithNoSession(t *testing.T) {
	var got security.ApprovalRequest
	b := newBridge(context.Background(), "claude-code", Hooks{Approve: func(_ context.Context, r security.ApprovalRequest) (security.Scope, error) {
		got = r
		return security.ScopeOnce, nil
	}}, nil)
	in := cmdInput("rm -r build")
	in.Kind = "other"
	if out := b.decide(context.Background(), "acp-1", in); out.OptionID != "allow-once" {
		t.Fatal(out)
	}
	if !got.NoSession || !strings.Contains(got.Description, "(dangerous: recursive delete)") {
		t.Fatalf("%+v", got)
	}
}
