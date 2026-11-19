package agent

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

func TestChildEnvCopiesRunFields(t *testing.T) {
	parent := &tools.Env{SessionID: "p1", Kind: "cron", UserID: "5", JobName: "n", JobPurpose: "p", DraftOnly: true}
	child := childEnv(parent, &sessions.Session{ID: "c1", Kind: KindSubagent})
	if child.UserID != "5" || child.JobName != "n" || child.JobPurpose != "p" || !child.DraftOnly || child.RootSessionID != "p1" {
		t.Fatalf("%+v", child)
	}
	grand := childEnv(child, &sessions.Session{ID: "c2", Kind: KindSubagent})
	if grand.RootSessionID != "p1" || !grand.DraftOnly {
		t.Fatalf("%+v", grand)
	}
}

func TestChildInheritsAndReturnsSources(t *testing.T) {
	rec := newNamed("rec", tools.SetFile)
	fp := scripted(toolCalls(tc("c1", "rec", `{}`), tc("c2", "web_search", `{}`)), text("done"))
	a, parent := newAgent(t, fp, rec, newNamed("web_search", tools.SetWeb))
	parentEnv := &tools.Env{SessionID: parent.ID, SessionKey: parent.Key, Values: map[string]any{}}
	tools.MarkOutside(parentEnv, "web_fetch", "pastebin.com")
	r := a.RunChild(ctx, ChildSpec{Goal: "x", ParentSession: parent, ParentToolsets: all(), Name: "1/1", ParentEnv: parentEnv})
	if r.Status != ChildOK {
		t.Fatalf("%+v", r)
	}
	// rec.seen shares the Values map of the child env, so it shows the set at the end of the turn.
	if got := tools.Outside(rec.seen).Sources; !slices.Contains(got, "web_fetch") {
		t.Fatalf("the child starts with the sources of the parent: %v", got)
	}
	if !reflect.DeepEqual(r.Outside.Sources, []string{"web_fetch", "web_search"}) {
		t.Fatalf("%+v", r.Outside)
	}
	if lite := liteResult(r); !reflect.DeepEqual(lite.Outside, r.Outside) {
		t.Fatalf("%+v", lite)
	}
}

func TestBackgroundChildQueuesSources(t *testing.T) {
	a, dl := delegatorAgent(t, scripted(text("batch turn")))
	dl.runChild = func(context.Context, ChildSpec) ChildResult {
		return ChildResult{Status: ChildOK, Summary: "child done", Outside: tools.OutsideFacts{Sources: []string{"web_fetch"}}}
	}
	q := &enqueued{}
	dl.Batches.Enqueue = q.fn
	parent, _ := a.Store.Create(ctx, sessionsFor("telegram:dm:1"))
	env := &tools.Env{SessionID: parent.ID, SessionKey: parent.Key, Values: map[string]any{}}
	if _, err := dl.StartBackground(ctx, env, []tools.ChildSpecLite{{Goal: "a", ParentEnv: env}}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return len(q.list()) == 1 })
	turnEnv := &tools.Env{SessionID: parent.ID, SessionKey: parent.Key, Values: map[string]any{}}
	res, err := a.RunTurn(ctx, Turn{Session: parent, UserText: "[Background delegation finished]", Toolsets: all(), Env: turnEnv})
	if err != nil || !reflect.DeepEqual(res.Outside.Sources, []string{"web_fetch"}) {
		t.Fatalf("%+v %v", res.Outside, err)
	}
}
