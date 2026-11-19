package agent

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/tools"
)

func TestTurnOutsideIsForOneTurn(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "web_fetch", `{}`)), text("done"), text("again"))
	a, sess := newAgent(t, fp, newNamed("web_fetch", tools.SetWeb))
	env := &tools.Env{SessionID: sess.ID, SessionKey: sess.Key, Values: map[string]any{}}
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "x", Toolsets: all(), Env: env})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Outside.Sources, []string{"web_fetch"}) || res.Outside.ToolCounts["web_fetch"] != 1 {
		t.Fatalf("%+v", res.Outside)
	}
	res, err = a.RunTurn(ctx, Turn{Session: sess, UserText: "y", Toolsets: all(), Env: env})
	if err != nil || res.Outside.Sources != nil || res.Outside.ToolCounts != nil {
		t.Fatalf("the next turn starts with an empty set: %+v %v", res.Outside, err)
	}
}

func TestTurnOutsideStartsWithTurnSources(t *testing.T) {
	rec := newNamed("rec", tools.SetFile)
	fp := scripted(toolCalls(tc("c1", "rec", `{}`)), text("done"))
	a, sess := newAgent(t, fp, rec)
	env := &tools.Env{SessionID: sess.ID, SessionKey: sess.Key, Values: map[string]any{}}
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "x", Toolsets: all(), Env: env, Outside: []string{"browser_navigate"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := tools.Outside(rec.seen).Sources; !reflect.DeepEqual(got, []string{"browser_navigate"}) {
		t.Fatalf("a tool of the turn sees the sources: %v", got)
	}
	if !reflect.DeepEqual(res.Outside.Sources, []string{"browser_navigate"}) {
		t.Fatalf("%+v", res.Outside)
	}
}

func TestTurnOutsideTakesQueuedFacts(t *testing.T) {
	fp := scripted(text("one"), text("two"))
	a, sess := newAgent(t, fp)
	a.queueOutside(sess.Key, tools.OutsideFacts{Sources: []string{"web_search"}, Hosts: []string{"x.io"}})
	a.queueOutside(sess.Key, tools.OutsideFacts{Sources: []string{"mcp_gh_search"}})
	a.queueOutside("", tools.OutsideFacts{Sources: []string{"web_fetch"}})
	env := &tools.Env{SessionID: sess.ID, SessionKey: sess.Key, Values: map[string]any{}}
	res, _ := a.RunTurn(ctx, Turn{Session: sess, UserText: "x", Toolsets: all(), Env: env})
	if !reflect.DeepEqual(res.Outside.Sources, []string{"mcp_gh_search", "web_search"}) || !reflect.DeepEqual(res.Outside.Hosts, []string{"x.io"}) {
		t.Fatalf("%+v", res.Outside)
	}
	res, _ = a.RunTurn(ctx, Turn{Session: sess, UserText: "y", Toolsets: all(), Env: env})
	if res.Outside.Sources != nil {
		t.Fatalf("the queued facts go to one turn only: %+v", res.Outside)
	}
}

func TestTurnOutsideAfterCancel(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "web_fetch", `{}`)), step{text: "half", stall: true})
	a, sess := newAgent(t, fp, newNamed("web_fetch", tools.SetWeb))
	env := &tools.Env{SessionID: sess.ID, SessionKey: sess.Key, Values: map[string]any{}}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	fp.onRequest = func(n int) {
		if n == 1 {
			time.AfterFunc(50*time.Millisecond, cancel)
		}
	}
	res, err := a.RunTurn(cctx, Turn{Session: sess, UserText: "x", Toolsets: all(), Env: env})
	if err == nil || !reflect.DeepEqual(res.Outside.Sources, []string{"web_fetch"}) {
		t.Fatalf("%+v %v", res.Outside, err)
	}
}
