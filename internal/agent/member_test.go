package agent

import (
	"slices"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

func schemaList(req provider.Request) []string {
	var out []string
	for _, s := range req.Tools {
		out = append(out, s.Name)
	}
	slices.Sort(out)
	return out
}

func TestMemberDeny(t *testing.T) {
	got := memberDeny(map[string]bool{"web_fetch": true, "clarify": true})
	if want := "This tool is for the owner only. A member can use these tools: clarify, web_fetch."; got != want {
		t.Fatalf("got %q", got)
	}
	got = memberDeny(nil)
	if want := "This tool is for the owner only. A member can use these tools: none."; got != want {
		t.Fatalf("got %q", got)
	}
}

// A member turn of a shared session keeps all schemas, refuses an owner tool, and gives
// the member set to SetTurnTools.
func TestSharedMemberTurnRefusesOwnerTool(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "echo", `{"s":"hi"}`)), text("done"))
	web := newNamed("web_probe", tools.SetWeb)
	a, sess := newAgent(t, fp, echoTool{}, web)
	env := &tools.Env{SessionID: sess.ID, Values: map[string]any{}, Member: true}
	_, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Env: env,
		Member: true, Shared: true, MemberTools: map[string]bool{tools.SetWeb: true}})
	if err != nil {
		t.Fatal(err)
	}
	if got := schemaList(fp.requests[0]); !slices.Equal(got, []string{"echo", "web_probe"}) {
		t.Fatalf("schemas: %v", got)
	}
	want := "This tool is for the owner only. A member can use these tools: web_probe."
	if got := lastToolContent(fp.requests[1]); !strings.Contains(got, want) {
		t.Fatalf("got %q", got)
	}
	sets, names := tools.TurnTools(env)
	if len(sets) != 1 || !sets["web"] || len(names) != 1 || !names["web_probe"] {
		t.Fatalf("sets %v names %v", sets, names)
	}
}

// An empty member set refuses each tool call.
func TestSharedMemberTurnEmptySetFailsClosed(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "echo", `{"s":"hi"}`)), text("done"))
	a, sess := newAgent(t, fp, echoTool{})
	_, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Member: true, Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := lastToolContent(fp.requests[1]); !strings.Contains(got, "A member can use these tools: none.") {
		t.Fatalf("got %q", got)
	}
}

// An owner turn of a shared session runs the tool.
func TestSharedOwnerTurnRunsTool(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "echo", `{"s":"hi"}`)), text("done"))
	a, sess := newAgent(t, fp, echoTool{})
	_, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := lastToolContent(fp.requests[1]); !strings.Contains(got, "hi") {
		t.Fatalf("got %q", got)
	}
}

// The fallback Env of RunTurn carries the member flag.
func TestMemberTurnFallbackEnv(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "rec", `{}`)), text("ok"))
	rec := newNamed("rec", tools.SetWeb)
	a, sess := newAgent(t, fp, rec)
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Member: true}); err != nil {
		t.Fatal(err)
	}
	if !rec.seen.Member {
		t.Fatal("the fallback Env lost the member flag")
	}
}

// A child of a member turn is a member turn.
func TestChildOfMemberTurnIsMember(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "rec", `{}`)), text("ok"))
	rec := newNamed("rec", tools.SetFile)
	a, parent := newAgent(t, fp, rec)
	r := a.RunChild(ctx, ChildSpec{Goal: "x", ParentSession: parent, ParentToolsets: all(),
		ParentEnv: &tools.Env{Member: true}})
	if r.Status != ChildOK || !rec.seen.Member {
		t.Fatalf("%+v member=%v", r, rec.seen.Member)
	}
	if !childEnv(&tools.Env{Member: true}, &sessions.Session{ID: "c1", Kind: KindSubagent}).Member {
		t.Fatal("childEnv lost the member flag")
	}
	if childEnv(&tools.Env{}, &sessions.Session{ID: "c2", Kind: KindSubagent}).Member {
		t.Fatal("an owner child must not be a member")
	}
}
