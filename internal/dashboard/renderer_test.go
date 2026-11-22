package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/tools"
)

const testSecret = "Zq7Lm4Np9Rt2Vx8K"

// costCalls records the calls of TurnCost.
type costCalls struct {
	mu       sync.Mutex
	sessions []string
}

// rendererFixture returns a renderer over a new hub and turn state, and the events of one page.
func rendererFixture(t *testing.T) (gateway.Renderer, *TurnState, <-chan sseEvent, *costCalls) {
	t.Helper()
	h := NewHub(1 << 20)
	turn := NewTurnState()
	red := security.NewNamedRedactor(map[string]string{"API_KEY": testSecret})
	costs := &costCalls{}
	r := NewRenderer(RendererDeps{Hub: h, Turn: turn, Redact: red.Redact, Partial: red.PartialExact, RequestID: "r1",
		TurnCost: func(ctx context.Context, sessionID string, since time.Time) (CostView, error) {
			costs.mu.Lock()
			costs.sessions = append(costs.sessions, sessionID)
			costs.mu.Unlock()
			if since.IsZero() || ctx.Err() != nil {
				return CostView{}, errors.New("bad call")
			}
			return CostView{Model: "gpt-5", Provider: "primary", Calls: 2, In: 1200, Cached: 800, Out: 80, USD: 0.0123, Seconds: 3.5}, nil
		}})
	ev, _ := sseClient(t, h, func() Snapshot { return Snapshot{Turn: turn.View()} })
	nextEvent(t, ev) // hello
	waitStreams(t, h, 1)
	return r, turn, ev, costs
}

func toolView(t *testing.T, e sseEvent, name string) ToolView {
	t.Helper()
	var v ToolView
	if e.Name != name || json.Unmarshal(e.Data, &v) != nil {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
	return v
}

func TestRendererDeltaRedaction(t *testing.T) {
	r, turn, ev, costs := rendererFixture(t)
	r.(gateway.SessionRenderer).SetSession("s1")
	r.(gateway.TurnContextRenderer).TurnContext(context.Background())
	r.Delta("key Zq7L")
	r.Delta("m4Np9Rt2Vx8K ok")
	if v := turn.View(); v == nil || !v.Running || v.Text != "key «API_KEY» ok" {
		t.Fatalf("%+v", v)
	}
	r.Done("key "+testSecret+" ok", nil)
	var text strings.Builder
	var done TurnDone
	for {
		e := nextEvent(t, ev)
		if strings.Contains(string(e.Data), "Zq7L") {
			t.Fatalf("a part of the secret left the process: %s %s", e.Name, e.Data)
		}
		if e.Name == "delta" {
			var d struct{ Text string }
			_ = json.Unmarshal(e.Data, &d)
			text.WriteString(d.Text)
			continue
		}
		if e.Name != "turn.done" || json.Unmarshal(e.Data, &done) != nil {
			t.Fatalf("%s %s", e.Name, e.Data)
		}
		break
	}
	if text.String() != "key «API_KEY» ok" {
		t.Fatalf("%q", text.String())
	}
	if done.Error != "" || done.Cost.Calls != 2 || done.Cost.USD != 0.0123 || done.Cost.Cached != 800 || done.MessageID != 0 {
		t.Fatalf("%+v", done)
	}
	if turn.View() != nil {
		t.Fatal("the turn state must end with turn.done")
	}
	if len(costs.sessions) != 1 || costs.sessions[0] != "s1" {
		t.Fatal(costs.sessions)
	}
	r.Done("again", nil)
	expectQuiet(t, ev, 100*time.Millisecond)
}

func TestRendererToolLines(t *testing.T) {
	r, turn, ev, _ := rendererFixture(t)
	r.(gateway.TurnContextRenderer).TurnContext(context.Background())
	d := r.(agent.ToolDetailSink)
	d.ToolCall("c1", "terminal", json.RawMessage(`{"command":"echo `+testSecret+`"}`))
	r.ToolStart("terminal", "echo "+testSecret)
	start := toolView(t, nextEvent(t, ev), "tool.start")
	if start != (ToolView{Call: "c1", Name: "terminal", Emoji: "💻", Preview: "echo «API_KEY»",
		Args: `{"command":"echo «API_KEY»"}`, State: "running"}) {
		t.Fatalf("%+v", start)
	}
	r.ToolEnd("terminal", false)
	if end := toolView(t, nextEvent(t, ev), "tool.end"); end.Call != "c1" || end.State != "done" || end.Text != "" {
		t.Fatalf("%+v", end)
	}
	long := strings.Repeat("é", 10000) // 20000 bytes
	d.ToolResult("c1", long, false, 1500*time.Millisecond)
	res := toolView(t, nextEvent(t, ev), "tool.end")
	if res.Call != "c1" || res.State != "done" || res.DurationMS != 1500 || !res.Cut || res.Text != strings.Repeat("é", 8192) ||
		!utf8.ValidString(res.Text) {
		t.Fatalf("%+v", res.Call)
	}
	if v := turn.View(); len(v.Tools) != 1 || v.Tools[0] != res {
		t.Fatalf("%+v", v.Tools)
	}
	// A failed call, and a ToolStart with no ToolCall.
	d.ToolCall("c2", "patch", json.RawMessage(`{}`))
	r.ToolStart("patch", "")
	nextEvent(t, ev)
	r.ToolEnd("patch", true)
	if e := toolView(t, nextEvent(t, ev), "tool.end"); e.Call != "c2" || e.State != "error" {
		t.Fatalf("%+v", e)
	}
	d.ToolResult("c2", "boom", true, 0)
	if e := toolView(t, nextEvent(t, ev), "tool.end"); e.Text != "boom" || e.State != "error" || e.Cut {
		t.Fatalf("%+v", e)
	}
	r.ToolStart("read_file", "a.go")
	if e := toolView(t, nextEvent(t, ev), "tool.start"); e.Call == "" || e.Emoji != tools.Emoji("read_file") {
		t.Fatalf("%+v", e)
	}
	r.Delta("text")
	nextEvent(t, ev)
	r.Boundary()
	if e := nextEvent(t, ev); e.Name != "boundary" || string(e.Data) != "{}" {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
	if v := turn.View(); v == nil || v.Text != "" || len(v.Tools) != 0 || !v.Running {
		t.Fatalf("a boundary starts a new segment: %+v", v)
	}
}

func TestRendererAcks(t *testing.T) {
	r, turn, ev, _ := rendererFixture(t)
	r.(gateway.ReplyRenderer).AckReply(gateway.Reply{Text: "⏸ Paused **nightly-backup**.",
		Buttons: [][]gateway.Button{{{Text: "▶ Resume", Data: "/jobs resume 12"}}}})
	var a AckView
	if e := nextEvent(t, ev); e.Name != "ack" || json.Unmarshal(e.Data, &a) != nil {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
	if a.ID == "" || a.Kind != "reply" || a.RequestID != "r1" || a.Text != "⏸ Paused **nightly-backup**." ||
		len(a.Buttons) != 1 || a.Buttons[0][0] != (Button{Text: "▶ Resume", Data: "/jobs resume 12"}) {
		t.Fatalf("%+v", a)
	}
	r.Ack("key " + testSecret)
	if e := nextEvent(t, ev); !strings.Contains(string(e.Data), `"text":"key «API_KEY»"`) {
		t.Fatalf("%s", e.Data)
	}
	if turn.View() != nil {
		t.Fatal("a command reply must not start a turn")
	}
	hd := r.(gateway.HandleRenderer).AckReplyHandle(gateway.Reply{Text: "⏳ Queued"})
	var busy AckView
	_ = json.Unmarshal(nextEvent(t, ev).Data, &busy)
	if busy.Kind != "busy" || busy.Text != "⏳ Queued" || hd.MsgID() == 0 {
		t.Fatalf("%+v", busy)
	}
	<-hd.Close("✔ Done")
	var closed AckView
	_ = json.Unmarshal(nextEvent(t, ev).Data, &closed)
	if closed.ID != busy.ID || closed.Text != "✔ Done" || closed.Buttons != nil {
		t.Fatalf("%+v", closed)
	}
	<-hd.Close("again")
	expectQuiet(t, ev, 100*time.Millisecond)
}

func TestRendererPlanGoalNotice(t *testing.T) {
	r, turn, ev, _ := rendererFixture(t)
	r.(gateway.TurnContextRenderer).TurnContext(context.Background())
	r.(agent.PlanSink).Plan(nil, []tools.TodoItem{
		{ID: "1", Content: "Read " + testSecret, Status: "in_progress"},
		{ID: "2", Content: "Sub", Status: "pending", Parent: "1"},
	})
	var plan struct{ Before, After []PlanItem }
	if e := nextEvent(t, ev); e.Name != "plan" || json.Unmarshal(e.Data, &plan) != nil {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
	want := []PlanItem{{Text: "Read «API_KEY»", State: "in_progress", Parent: -1}, {Text: "Sub", State: "pending", Parent: 0}}
	if len(plan.Before) != 0 || len(plan.After) != 2 || plan.After[0] != want[0] || plan.After[1] != want[1] {
		t.Fatalf("%+v", plan)
	}
	if v := turn.View(); len(v.Plan) != 2 || v.Plan[1] != want[1] {
		t.Fatalf("%+v", v.Plan)
	}
	r.(gateway.GoalLineRenderer).GoalLine("↻ Goal 8/20: reason")
	if e := nextEvent(t, ev); e.Name != "goal" || string(e.Data) != `{"line":"↻ Goal 8/20: reason"}` {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
	r.Notice("🗜 Context compacted")
	var n Notice
	if e := nextEvent(t, ev); e.Name != "notice" || json.Unmarshal(e.Data, &n) != nil || n.Text != "🗜 Context compacted" {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
}

func TestRendererDoneTexts(t *testing.T) {
	for _, c := range []struct {
		name       string
		final      string
		err        error
		wantDelta  string
		wantErrTxt string
	}{
		{"fixed text", "⚠️ This turn used all 90 tool rounds.", nil, "⚠️ This turn used all 90 tool rounds.", ""},
		{"error", "", errors.New("the model call failed: key " + testSecret), "", "the model call failed: key «API_KEY»"},
		{"stopped", "", context.Canceled, "", "The turn stopped."},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, _, ev, _ := rendererFixture(t)
			r.(gateway.TurnContextRenderer).TurnContext(context.Background())
			r.Done(c.final, c.err)
			e := nextEvent(t, ev)
			if c.wantDelta != "" {
				if e.Name != "delta" || !strings.Contains(string(e.Data), c.wantDelta) {
					t.Fatalf("%s %s", e.Name, e.Data)
				}
				e = nextEvent(t, ev)
			}
			var done TurnDone
			if e.Name != "turn.done" || json.Unmarshal(e.Data, &done) != nil || done.Error != c.wantErrTxt {
				t.Fatalf("%s %s", e.Name, e.Data)
			}
		})
	}
}

func TestTurnStateQueue(t *testing.T) {
	ts := NewTurnState()
	ts.SetQueue([]QueueItem{{Kind: "message", Preview: "hello"}})
	if ts.View() != nil {
		t.Fatal("no turn runs")
	}
	r := NewRenderer(RendererDeps{Hub: NewHub(1 << 10), Turn: ts, Redact: func(s string) string { return s },
		Partial:  func(string) int { return 0 },
		TurnCost: func(context.Context, string, time.Time) (CostView, error) { return CostView{}, nil }})
	r.(gateway.TurnContextRenderer).TurnContext(context.Background())
	v := ts.View()
	if v == nil || len(v.Queue) != 1 || v.Queue[0].Preview != "hello" {
		t.Fatalf("%+v", v)
	}
	v.Queue[0].Preview = "changed"
	if ts.View().Queue[0].Preview != "hello" {
		t.Fatal("View must return a copy")
	}
}

func TestRendererDoneRewrite(t *testing.T) {
	h := NewHub(1 << 20)
	turn := NewTurnState()
	ev, _ := sseClient(t, h, func() Snapshot { return Snapshot{Turn: turn.View()} })
	nextEvent(t, ev)
	waitStreams(t, h, 1)
	r := NewRenderer(RendererDeps{Hub: h, Turn: turn, Redact: func(s string) string { return s },
		Partial:  func(string) int { return 0 },
		TurnCost: func(context.Context, string, time.Time) (CostView, error) { return CostView{}, nil },
		Rewrite: func(s string) (string, []FileRef) {
			return "rewritten:" + s, []FileRef{{ID: "f1", Name: "a.png", Kind: "image"}}
		},
		Context: func(context.Context, string) *ContextView { return &ContextView{Tokens: 5, Window: 10} }})
	r.(gateway.TurnContextRenderer).TurnContext(context.Background())
	r.Done("MEDIA:/x/a.png", nil)
	if e := nextEvent(t, ev); e.Name != "delta" || !strings.Contains(string(e.Data), "rewritten:MEDIA:/x/a.png") {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
	var done TurnDone
	if e := nextEvent(t, ev); e.Name != "turn.done" || json.Unmarshal(e.Data, &done) != nil || done.Context == nil || done.Context.Tokens != 5 {
		t.Fatalf("%s %s", e.Name, e.Data)
	}
}
