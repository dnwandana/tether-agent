package acp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	acpsdk "github.com/coder/acp-go-sdk"
)

// progressQueueSize is the size of the progress queue of one turn. A full queue drops items,
// because the SDK closes the connection when more than 1024 notifications wait.
const progressQueueSize = 64

// messageRingMax is the maximum size of the message text of one turn. The turn keeps the end.
const messageRingMax = 64 << 10

// cancelWait is the time that Prompt waits for the agent to end a cancelled turn.
const cancelWait = 10 * time.Second

// ErrSessionClosed is the error of a Prompt call that Abort ended.
var ErrSessionClosed = errors.New("acp: the coding agent session was closed")

// TurnResult is the result of one prompt turn.
type TurnResult struct {
	StopReason string
	Text       string
	ToolCalls  int
	Mode       string
}

type progressItem struct{ kind, title string }

// turnState implements turnHandler for one Prompt call.
type turnState struct {
	sid    string
	bridge *bridge
	queue  chan progressItem
	done   chan struct{}  // closed when the progress goroutine returns
	perms  sync.WaitGroup // the onPermission calls that run

	mu        sync.Mutex
	closed    bool
	text      []byte
	toolCalls int
	mode      string
}

func newTurnState(ctx context.Context, agentID, sessionID string, h Hooks) *turnState {
	return &turnState{
		sid:    sessionID,
		bridge: newBridge(ctx, agentID, h),
		queue:  make(chan progressItem, progressQueueSize),
		done:   make(chan struct{}),
	}
}

func (t *turnState) sessionID() string { return t.sid }

// onUpdate records one session update. It never blocks, because the SDK runs all
// notifications on one goroutine.
func (t *turnState) onUpdate(u update) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	switch u.Type {
	case "tool_call":
		t.toolCalls++
		kind := u.ToolKind
		if kind == "" {
			kind = "other"
		}
		select {
		case t.queue <- progressItem{kind: kind, title: u.Title}:
		default: // The queue is full. Drop the item.
		}
	case "agent_message_chunk":
		t.text = append(t.text, u.Text...)
		if len(t.text) > messageRingMax {
			end := t.text[len(t.text)-messageRingMax:]
			// Start at a rune start, so that the text stays valid UTF-8.
			i := 0
			for i < len(end) && !utf8.RuneStart(end[i]) {
				i++
			}
			t.text = t.text[:copy(t.text, end[i:])]
		}
	case "current_mode_update":
		t.mode = u.Mode
	}
}

// onPermission gives the request to the bridge of the turn. After finish, the answer is
// cancelled.
func (t *turnState) onPermission(ctx context.Context, in permissionInput) permissionOutcome {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return permissionOutcome{Cancelled: true}
	}
	t.perms.Add(1)
	t.mu.Unlock()
	defer t.perms.Done()
	return t.bridge.decide(ctx, t.sid, in)
}

// runProgress gives each queued item to progress until the queue closes. A nil progress
// drops the items.
func (t *turnState) runProgress(progress func(kind, title string)) {
	defer close(t.done)
	for it := range t.queue {
		if progress != nil {
			progress(it.kind, it.title)
		}
	}
}

// finish stops the updates of the turn and waits until the progress goroutine gives the
// last queued item. onUpdate sends only under t.mu with t.closed false, so no send reaches
// the closed queue. finish also waits for the permission requests that run, so no hook
// call comes after it. Call bridge.cancelAll first, so that these requests end at once.
func (t *turnState) finish() {
	t.mu.Lock()
	if !t.closed {
		t.closed = true
		close(t.queue)
	}
	t.mu.Unlock()
	t.perms.Wait()
	<-t.done
}

// result returns the result of the turn with stopReason.
func (t *turnState) result(stopReason string) TurnResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	return TurnResult{StopReason: stopReason, Text: string(t.text), ToolCalls: t.toolCalls, Mode: t.mode}
}

// Busy reports whether a Prompt call runs on the client.
func (c *Client) Busy() bool { return c.busy.Load() }

// LastUse returns the time when the last Prompt call ended, or the start time.
func (c *Client) LastUse() time.Time { return time.Unix(0, c.lastUse.Load()) }

// promptReply is the result of one SDK session/prompt call.
type promptReply struct {
	resp acpsdk.PromptResponse
	err  error
}

// Prompt sends text to the ACP session sessionID and waits for the end of the turn. The hooks
// get the progress items and the permission requests of this turn only.
//
// When ctx ends, Prompt sends session/cancel, answers the open permission requests with
// cancelled, and waits up to cancelWait for the agent to end the turn. When the agent does
// not end the turn in that time, Prompt stops the process.
func (c *Client) Prompt(ctx context.Context, sessionID, text string, h Hooks) (res TurnResult, err error) {
	c.busy.Store(true)
	defer func() {
		c.lastUse.Store(time.Now().UnixNano())
		c.busy.Store(false)
	}()

	ts := newTurnState(ctx, c.AgentID(), sessionID, h)
	go ts.runProgress(h.Progress)
	c.attach(ts)
	c.turn.Store(ts)
	var stopReason string
	defer func() {
		c.turn.CompareAndSwap(ts, nil)
		// Detach first, so that no new event reaches ts. cancelAll then answers a late
		// permission request with cancelled at once.
		c.attach(nil)
		ts.bridge.cancelAll()
		ts.finish()
		// After finish, the result has each update of the turn.
		res = ts.result(stopReason)
	}()

	// The SDK call gets its own ctx. When the ctx of the SDK call ends, the SDK returns at
	// once and drops the response, but a cancelled turn must wait for the stop reason.
	pctx, pcancel := context.WithCancel(context.WithoutCancel(ctx))
	defer pcancel()
	replies := make(chan promptReply, 1)
	go func() {
		resp, err := c.conn.Prompt(pctx, acpsdk.PromptRequest{
			SessionId: acpsdk.SessionId(sessionID),
			Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock(text)},
		})
		replies <- promptReply{resp: resp, err: err}
	}()

	// Each return keeps res empty. The deferred call above sets it, also the partial result
	// of an error.
	select {
	case r := <-replies:
		return res, c.promptEnd(r, &stopReason)
	case <-c.Done():
		return res, c.turnStoppedErr()
	case <-ctx.Done():
	}
	// A reply that came at the same time wins. A session/cancel after the reply can
	// cancel the next turn of the session.
	select {
	case r := <-replies:
		return res, c.promptEnd(r, &stopReason)
	default:
	}

	c.sendCancel(sessionID)
	ts.bridge.cancelAll()
	wait := cancelWait
	if c.cancelWaitOverride > 0 {
		wait = c.cancelWaitOverride
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case r := <-replies:
		return res, c.promptEnd(r, &stopReason)
	case <-c.Done():
		return res, c.turnStoppedErr()
	case <-timer.C:
	}
	pcancel()
	if err := c.Stop(); err != nil {
		c.log.Debug("acp: the stop after the cancel wait failed", "agent", c.agentID, "err", err)
	}
	stopReason = string(acpsdk.StopReasonCancelled)
	//lint:ignore ST1005 the text is a fixed user message with full sentences.
	return res, fmt.Errorf("acp: %s did not end the turn in %s after the cancel. Tether Agent stopped it.", c.AgentID(), wait)
}

// promptEnd maps the reply of the SDK prompt call. It sets *stopReason on success.
func (c *Client) promptEnd(r promptReply, stopReason *string) error {
	if c.aborted.Load() {
		// Abort can come after the agent ends the turn, but before Prompt returns.
		return ErrSessionClosed
	}
	if r.err != nil {
		var re *acpsdk.RequestError
		if errors.As(r.err, &re) && re.Code == codeAuthRequired {
			return &NotLoggedInError{Agent: c.AgentID()}
		}
		return fmt.Errorf("acp: %s: session/prompt: %w", c.AgentID(), r.err)
	}
	*stopReason = string(r.resp.StopReason)
	return nil
}

// turnStoppedErr returns the error of a Prompt call whose process stopped during the turn.
func (c *Client) turnStoppedErr() error {
	if c.aborted.Load() {
		return ErrSessionClosed
	}
	return fmt.Errorf("acp: %s: the process stopped during the turn", c.AgentID())
}
