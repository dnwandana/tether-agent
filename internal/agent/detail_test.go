package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
)

// detailSink is a recSink that also records the ToolCall and ToolResult calls, in one list
// with ToolStart and ToolEnd.
type detailSink struct {
	recSink
	mu    sync.Mutex
	order []string
	durs  []time.Duration
}

func (d *detailSink) ToolCall(callID, name string, args json.RawMessage) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.order = append(d.order, "call "+callID+" "+name+" "+string(args))
}

func (d *detailSink) ToolStart(name, preview string) {
	d.recSink.ToolStart(name, preview)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.order = append(d.order, "start "+name)
}

func (d *detailSink) ToolEnd(name string, isError bool) {
	d.recSink.ToolEnd(name, isError)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.order = append(d.order, "end "+name)
}

func (d *detailSink) ToolResult(callID, text string, isError bool, dur time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.order = append(d.order, fmt.Sprintf("result %s %v %s", callID, isError, text))
	d.durs = append(d.durs, dur)
}

var _ ToolDetailSink = (*detailSink)(nil)

func TestToolDetailSinkGetsCallAndStoredResult(t *testing.T) {
	args := `{"s":"key ` + pypiValue + `"}`
	fp := scripted(toolCalls(tc("c1", "echo", args)), text("done"))
	a, sess := newAgent(t, fp, echoTool{})
	a.Redactor = security.NewNamedRedactor(map[string]string{"PYPI_TOKEN": pypiValue})
	sink := &detailSink{}
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: sink}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"call c1 echo " + args,
		"start echo",
		"end echo",
		"result c1 false key «PYPI_TOKEN»",
	}
	if strings.Join(sink.order, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%q", sink.order)
	}
	msgs := messages(t, a, sess.ID)
	if msgs[2].Msg.Role != provider.RoleTool || msgs[2].Msg.Content != "key «PYPI_TOKEN»" {
		t.Fatal("ToolResult must get the text of the saved row:", msgs[2].Msg.Content)
	}
	if len(sink.durs) != 1 || sink.durs[0] < 0 {
		t.Fatal(sink.durs)
	}
}

func TestToolDetailSinkInParallelRound(t *testing.T) {
	a := agentWith(t, slowTool{name: "read_file", safe: true, d: 20 * time.Millisecond})
	s := &detailSink{}
	calls := []provider.ToolCall{tc("1", "read_file", `{"n":1}`), tc("2", "read_file", `{"n":2}`), tc("3", "read_file", `{"n":3}`)}
	res, durs := a.runRoundTimed(ctx, calls, env(), vis("read_file"), s, "")
	if len(res) != 3 || len(durs) != 3 {
		t.Fatal(res, durs)
	}
	for i, d := range durs {
		if d < 20*time.Millisecond {
			t.Errorf("call %d: duration %v", i, d)
		}
	}
	// Each ToolCall comes just before its ToolStart, with no other call between them.
	for i, line := range s.order {
		if strings.HasPrefix(line, "call ") && (i+1 >= len(s.order) || s.order[i+1] != "start read_file") {
			t.Fatalf("%q", s.order)
		}
	}
	ids := map[string]bool{}
	for _, line := range s.order {
		if strings.HasPrefix(line, "call ") {
			ids[strings.Fields(line)[1]] = true
		}
	}
	if len(ids) != 3 {
		t.Fatalf("each call id once: %q", s.order)
	}
}

func TestPlainSinkGetsOldCallsOnly(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "echo", `{"s":"x"}`)), text("done"))
	a, sess := newAgent(t, fp, echoTool{})
	sink := &recSink{}
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: sink}); err != nil {
		t.Fatal(err)
	}
	if len(sink.starts) != 1 || len(sink.ends) != 1 {
		t.Fatalf("%+v", sink)
	}
	// A cancelled call gets no duration.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	_, durs := agentWith(t, echoTool{}).runRoundTimed(cctx, []provider.ToolCall{tc("1", "echo", `{}`)}, env(), vis("echo"), sink, "")
	if len(durs) != 1 || durs[0] != 0 {
		t.Fatal(durs)
	}
}
