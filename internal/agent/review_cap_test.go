package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// countTool adds one to n for each run and returns "ok".
type countTool struct {
	name, set string
	n         *atomic.Int32
}

func (c countTool) Name() string       { return c.name }
func (c countTool) Toolset() string    { return c.set }
func (c countTool) ParallelSafe() bool { return false }
func (c countTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{Name: c.name, Description: c.name, Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (c countTool) Run(context.Context, json.RawMessage, *tools.Env) (string, error) {
	c.n.Add(1)
	return "ok", nil
}

// capAgent returns an Agent with a counting skill_manage tool and the real memory tool.
func capAgent(t *testing.T, fp *fakeProv) (*Agent, *sessions.Session, *memory.Store, *atomic.Int32) {
	t.Helper()
	m, err := memory.Open(t.TempDir(), 2200, 1375, 1000)
	if err != nil {
		t.Fatal(err)
	}
	m.History = &memHistory{}
	n := new(atomic.Int32)
	a, sess := newAgent(t, fp, countTool{name: "skill_manage", set: tools.SetSkills, n: n},
		tools.MemoryTool{Store: m, Quotes: acceptQuotes{}})
	a.Memory = m
	return a, sess, m, n
}

func capCalls(n int) []provider.ToolCall {
	var out []provider.ToolCall
	for i := range n {
		out = append(out, tc(fmt.Sprintf("s%d", i), "skill_manage", `{"action":"patch","name":"a"}`))
	}
	return out
}

// capToolText returns the content of the tool message with id.
func capToolText(req provider.Request, id string) string {
	for _, m := range req.Messages {
		if m.Role == provider.RoleTool && m.ToolCallID == id {
			return m.Content
		}
	}
	return "<none>"
}

func capRefusal(n int) string {
	return fmt.Sprintf("A review makes at most %d skill changes. Stop, and keep the most useful changes.", n)
}

func TestReviewRefusesTheFourthSkillChange(t *testing.T) {
	fp := scripted(text("ok"))
	a, capSess, m, n := capAgent(t, fp)
	userTurn(t, a, capSess, "hi")
	calls := append(capCalls(5), tc("m1", "memory", `{"action":"add","target":"memory","content":"x"}`))
	fp.script = []step{toolCalls(calls...), text("done")}
	fp.requests = nil
	if _, err := a.RunReview(ctx, ReviewRequest{Session: capSess, System: "SYS", Memory: true, Skills: true}); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 3 {
		t.Fatal("the tool must run 3 times, ran", n.Load())
	}
	req := fp.requests[1]
	for _, id := range []string{"s3", "s4"} {
		if got := capToolText(req, id); got != capRefusal(3) {
			t.Fatal(id, got)
		}
	}
	if got := capToolText(req, "s2"); got != "ok" {
		t.Fatal(got)
	}
	es, _ := m.ReadEntries("memory")
	if len(es) != 1 || es[0] != "x" {
		t.Fatal("the memory add must be saved:", es)
	}
}

func TestReviewCapCountsOverRounds(t *testing.T) {
	fp := scripted(text("ok"))
	a, capSess, _, n := capAgent(t, fp)
	userTurn(t, a, capSess, "hi")
	r1, r2 := capCalls(2), capCalls(2)
	r2[0].ID, r2[1].ID = "t0", "t1"
	fp.script = []step{toolCalls(r1...), toolCalls(r2...), text("done")}
	fp.requests = nil
	if _, err := a.RunReview(ctx, ReviewRequest{Session: capSess, System: "SYS", Skills: true}); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 3 {
		t.Fatal("ran", n.Load())
	}
	req := fp.requests[2]
	if capToolText(req, "t0") != "ok" || capToolText(req, "t1") != capRefusal(3) {
		t.Fatal(capToolText(req, "t0"), capToolText(req, "t1"))
	}
}

func TestReviewCapFromConfig(t *testing.T) {
	for _, tt := range []struct{ cfg, want int }{{1, 1}, {0, 3}} {
		fp := scripted(text("ok"))
		a, capSess, _, n := capAgent(t, fp)
		a.Cfg.Skills.ReviewMaxChanges = tt.cfg
		userTurn(t, a, capSess, "hi")
		fp.script = []step{toolCalls(capCalls(5)...), text("done")}
		fp.requests = nil
		if _, err := a.RunReview(ctx, ReviewRequest{Session: capSess, System: "SYS", Skills: true}); err != nil {
			t.Fatal(err)
		}
		if int(n.Load()) != tt.want {
			t.Fatalf("cfg %d: ran %d", tt.cfg, n.Load())
		}
		if got := capToolText(fp.requests[1], "s4"); got != capRefusal(tt.want) {
			t.Fatal(got)
		}
	}
}

func TestChatTurnHasNoSkillCap(t *testing.T) {
	fp := scripted(toolCalls(capCalls(5)...), text("done"))
	a, capSess, _, n := capAgent(t, fp)
	userTurn(t, a, capSess, "hi")
	if n.Load() != 5 {
		t.Fatal("ran", n.Load())
	}
}

func TestCallGateRefusesBeforeRun(t *testing.T) {
	a := agentWith(t, echoTool{})
	gate := func(c provider.ToolCall) string {
		if c.Name == "echo" {
			return "no"
		}
		return ""
	}
	res := a.runRound(withCallGate(ctx, gate), []provider.ToolCall{tc("1", "echo", `{"s":"hi"}`)}, env(), vis("echo"), &recSink{}, "")
	if len(res) != 1 || res[0].Text != "no" || !res[0].IsError {
		t.Fatal(res)
	}
	if strings.Contains(res[0].Text, "hi") {
		t.Fatal("the tool ran")
	}
	if callGate(ctx) != nil {
		t.Fatal("a plain context has no gate")
	}
}
