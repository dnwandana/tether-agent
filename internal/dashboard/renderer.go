package dashboard

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// toolTextMax is the longest tool text in an event: 16 KB. "Show all" reads the saved row.
const toolTextMax = 16 << 10

// costTimeout limits the query of the cost footer.
const costTimeout = 5 * time.Second

// textStopped is the error text of a turn that /stop ended.
const textStopped = "The turn stopped."

// RendererDeps is what a renderer needs. Redact and Partial come from the redactor of the app.
// Rewrite and Context can be nil.
type RendererDeps struct {
	Hub       *Hub
	Turn      *TurnState
	Redact    func(string) string
	Partial   func(tail string) int
	TurnCost  func(ctx context.Context, sessionID string, since time.Time) (CostView, error)
	Rewrite   func(text string) (string, []FileRef)
	Context   func(ctx context.Context, sessionID string) *ContextView
	RequestID string
}

// renderer shows one turn, or one command reply, on the open pages. It holds the hub and the
// turn state only. The lock order is mu, the hub lock, then the turn state lock.
type renderer struct {
	d RendererDeps
	// onSession, when set, runs after the session of the turn is known and after the turn ends.
	// The channel sends the session event from it.
	onSession func()

	mu       sync.Mutex
	ctx      context.Context
	session  string
	start    time.Time
	begun    bool
	done     bool
	raw      string // raw text of the open segment
	sent     string // redacted text of the open segment that went out
	broken   bool   // the redacted text no longer extends sent
	streamed bool   // a delta came in this turn
	calls    map[string][]pendingCall
	tools    []*toolRec
	seq      int
}

// pendingCall is a ToolCall that waits for its ToolStart.
type pendingCall struct{ id, args string }

// toolRec is a tool line with its start time.
type toolRec struct {
	v     ToolView
	begin time.Time
}

var (
	_ gateway.Renderer            = (*renderer)(nil)
	_ gateway.ReplyRenderer       = (*renderer)(nil)
	_ gateway.HandleRenderer      = (*renderer)(nil)
	_ gateway.GoalLineRenderer    = (*renderer)(nil)
	_ gateway.TurnContextRenderer = (*renderer)(nil)
	_ gateway.SessionRenderer     = (*renderer)(nil)
	_ agent.PlanSink              = (*renderer)(nil)
	_ agent.ToolDetailSink        = (*renderer)(nil)
)

// NewRenderer returns a renderer for one turn or one command reply.
func NewRenderer(d RendererDeps) gateway.Renderer {
	if d.Redact == nil {
		d.Redact = func(s string) string { return s }
	}
	return &renderer{d: d, calls: map[string][]pendingCall{}}
}

// SetSession sets the session id that the cost footer reads.
func (r *renderer) SetSession(id string) {
	r.mu.Lock()
	changed := r.session != id
	r.session = id
	r.mu.Unlock()
	if changed && r.onSession != nil {
		r.onSession()
	}
}

// TurnContext starts the turn.
func (r *renderer) TurnContext(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ctx = ctx
	r.ensureLocked()
}

// ensureLocked starts the turn once. The caller holds r.mu.
func (r *renderer) ensureLocked() {
	if r.begun || r.done {
		return
	}
	r.begun = true
	r.start = time.Now()
	if r.d.Turn != nil {
		r.d.Turn.begin()
	}
}

// publishLocked sends e and runs apply on the turn state under the hub lock.
func (r *renderer) publishLocked(e Event, apply func(t *TurnState)) {
	r.d.Hub.publishWith(e, func() {
		if apply != nil && r.d.Turn != nil {
			apply(r.d.Turn)
		}
	})
}

// Delta adds streamed text.
func (r *renderer) Delta(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return
	}
	r.ensureLocked()
	r.streamed = true
	r.raw += text
	r.sendLocked(false)
}

// sendLocked sends the part of the redacted segment text that is new. Unless flush is true, it
// holds back the tail that can be the start of a secret.
func (r *renderer) sendLocked(flush bool) {
	if r.broken {
		return
	}
	cut := len(r.raw)
	if !flush && r.d.Partial != nil {
		cut -= min(cut, max(r.d.Partial(r.raw), 0))
	}
	red := r.d.Redact(r.raw[:cut])
	if !strings.HasPrefix(red, r.sent) {
		r.broken = true
		return
	}
	more := red[len(r.sent):]
	if more == "" {
		return
	}
	r.sent = red
	r.publishLocked(Event{Name: "delta", Data: more}, func(t *TurnState) { t.setText(red) })
}

// Boundary ends the open segment.
func (r *renderer) Boundary() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return
	}
	r.ensureLocked()
	r.sendLocked(true)
	r.raw, r.sent, r.broken = "", "", false
	r.tools = nil
	r.publishLocked(Event{Name: "boundary", Data: struct{}{}}, func(t *TurnState) { t.newSegment() })
}

// ToolCall keeps the call id and the redacted arguments until ToolStart.
func (r *renderer) ToolCall(callID, name string, args json.RawMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[name] = append(r.calls[name], pendingCall{id: callID, args: r.d.Redact(string(args))})
}

// ToolStart shows a running tool line.
func (r *renderer) ToolStart(name, preview string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return
	}
	r.ensureLocked()
	var c pendingCall
	if q := r.calls[name]; len(q) > 0 {
		c, r.calls[name] = q[0], q[1:]
	} else {
		r.seq++
		c.id = fmt.Sprintf("t%d", r.seq)
	}
	rec := &toolRec{begin: time.Now(), v: ToolView{Call: c.id, Name: name, Emoji: tools.Emoji(name),
		Preview: r.d.Redact(preview), Args: c.args, State: "running"}}
	r.tools = append(r.tools, rec)
	v := rec.v
	r.publishLocked(Event{Name: "tool.start", Data: v}, func(t *TurnState) { t.addTool(v) })
}

// ToolEnd marks the oldest running tool of that name as done or failed.
func (r *renderer) ToolEnd(name string, isError bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.tools {
		if rec.v.Name == name && rec.v.State == "running" {
			rec.v.State = "done"
			if isError {
				rec.v.State = "error"
			}
			rec.v.DurationMS = time.Since(rec.begin).Milliseconds()
			v := rec.v
			r.publishLocked(Event{Name: "tool.end", Data: v}, func(t *TurnState) { t.setTool(v) })
			return
		}
	}
}

// ToolResult sets the text of the tool line with that call id.
func (r *renderer) ToolResult(callID, text string, isError bool, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.tools {
		if rec.v.Call != callID {
			continue
		}
		rec.v.Text, rec.v.Cut = cutText(r.d.Redact(text), toolTextMax)
		rec.v.State = "done"
		if isError {
			rec.v.State = "error"
		}
		rec.v.DurationMS = d.Milliseconds()
		v := rec.v
		r.publishLocked(Event{Name: "tool.end", Data: v}, func(t *TurnState) { t.setTool(v) })
		return
	}
}

// Plan shows the task list.
func (r *renderer) Plan(before, after []tools.TodoItem) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return
	}
	r.ensureLocked()
	b, a := planItems(before, r.d.Redact), planItems(after, r.d.Redact)
	r.publishLocked(Event{Name: "plan", Data: map[string][]PlanItem{"before": b, "after": a}},
		func(t *TurnState) { t.setPlan(a) })
}

// GoalLine shows the goal line of a continuation turn.
func (r *renderer) GoalLine(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publishLocked(Event{Name: "goal", Data: map[string]string{"line": r.d.Redact(line)}}, nil)
}

// Notice shows a short status text.
func (r *renderer) Notice(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publishLocked(Event{Name: "notice", Data: Notice{Text: r.d.Redact(text), At: time.Now()}}, nil)
}

// Ack sends a text at once.
func (r *renderer) Ack(text string) { r.AckReply(gateway.Reply{Text: text}) }

// AckReply sends a command reply with its buttons.
func (r *renderer) AckReply(rep gateway.Reply) { r.sendAck(newAckID(), "reply", rep.Text, rep.Buttons) }

// AckReplyHandle sends a busy ack and returns a handle to edit it.
func (r *renderer) AckReplyHandle(rep gateway.Reply) gateway.AckHandle {
	id := newAckID()
	r.sendAck(id, "busy", rep.Text, rep.Buttons)
	return &ackHandle{r: r, id: id, msgID: int(ackSeq.Add(1))}
}

func (r *renderer) sendAck(id, kind, text string, buttons [][]gateway.Button) {
	v := AckView{ID: id, Kind: kind, Text: r.d.Redact(text), RequestID: r.d.RequestID}
	for _, row := range buttons {
		var out []Button
		for _, b := range row {
			out = append(out, Button{Text: r.d.Redact(b.Text), Data: b.Data})
		}
		v.Buttons = append(v.Buttons, out)
	}
	r.d.Hub.Publish(Event{Name: "ack", Data: v})
}

// newAckID returns a random ack id.
func newAckID() string {
	b := make([]byte, 16)
	_, _ = crand.Read(b)
	return hex.EncodeToString(b)
}

// ackHandle is the handle of a busy ack. Close sends the new text once.
type ackHandle struct {
	r     *renderer
	id    string
	msgID int
	once  sync.Once
}

// ackSeq gives each ack handle a MsgID that is unique in the process.
var ackSeq atomic.Int64

// MsgID returns the number of the ack.
func (h *ackHandle) MsgID() int { return h.msgID }

// Close sends the new text with no buttons. It runs once.
func (h *ackHandle) Close(text string) <-chan struct{} {
	h.once.Do(func() {
		if text != "" {
			h.r.d.Hub.Publish(Event{Name: "ack", Data: AckView{ID: h.id, Kind: "busy",
				Text: h.r.d.Redact(text), RequestID: h.r.d.RequestID}})
		}
	})
	ch := make(chan struct{})
	close(ch)
	return ch
}

// Done ends the turn. It runs once.
func (r *renderer) Done(final string, err error) {
	r.mu.Lock()
	if r.done {
		r.mu.Unlock()
		return
	}
	r.done = true
	r.sendLocked(true)
	if !r.streamed && final != "" {
		text := final
		if r.d.Rewrite != nil {
			text, _ = r.d.Rewrite(final)
		}
		if red := r.d.Redact(text); red != "" {
			r.publishLocked(Event{Name: "delta", Data: red}, func(t *TurnState) { t.setText(red) })
		}
	}
	session, start, parent := r.session, r.start, r.ctx
	r.mu.Unlock()

	if parent == nil {
		parent = context.Background()
	}
	if start.IsZero() {
		start = time.Now()
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), costTimeout)
	defer cancel()
	done := TurnDone{}
	if r.d.TurnCost != nil {
		if c, cerr := r.d.TurnCost(ctx, session, start); cerr == nil {
			done.Cost = c
		}
	}
	if r.d.Context != nil {
		done.Context = r.d.Context(ctx, session)
	}
	switch {
	case err == nil:
	case errors.Is(err, context.Canceled):
		done.Error = textStopped
	default:
		done.Error = r.d.Redact(err.Error())
	}
	r.d.Hub.publishWith(Event{Name: "turn.done", Data: done}, func() {
		if r.d.Turn != nil {
			r.d.Turn.end()
		}
	})
	if r.onSession != nil {
		r.onSession()
	}
}

// cutText cuts s at a rune boundary to at most max bytes. cut is true when s was longer.
func cutText(s string, max int) (out string, cut bool) {
	if len(s) <= max {
		return s, false
	}
	n := max
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

// planItems maps the todo list to PlanItem values. Parent is the index of the parent item, or -1.
func planItems(items []tools.TodoItem, redact func(string) string) []PlanItem {
	index := make(map[string]int, len(items))
	for i, it := range items {
		index[it.ID] = i
	}
	out := make([]PlanItem, 0, len(items))
	for _, it := range items {
		p := -1
		if i, ok := index[it.Parent]; ok && it.Parent != "" {
			p = i
		}
		out = append(out, PlanItem{Text: redact(it.Content), State: it.Status, Parent: p})
	}
	return out
}
