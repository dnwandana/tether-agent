package agent

import (
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// observe stores an observed group message in the session.
func observe(t *testing.T, a *Agent, sessID, content string) {
	t.Helper()
	m := provider.Message{Role: provider.RoleUser, Content: content}
	if _, err := a.Store.Append(ctx, sessID, m, sessions.MessageMeta{Observed: true}); err != nil {
		t.Fatal(err)
	}
}

// userContents returns the content of each user message of msgs.
func userContents(msgs []provider.Message) []string {
	var out []string
	for _, m := range msgs {
		if m.Role == provider.RoleUser {
			out = append(out, m.Content)
		}
	}
	return out
}

func TestObservedGoesIntoContextBlock(t *testing.T) {
	fp := scripted(text("previous reply"), text("ok"))
	a, sess := newAgent(t, fp)
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "[Bob|222]\nhello", Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	observe(t, a, sess.ID, "[Alice|111]\nCan we track the stock?")
	observe(t, a, sess.ID, "[Alice|111]\nIt has lots and expiry dates")
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "[Bob|222]\ncambio", Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	r := fp.requests[1]
	users := userContents(r.Messages)
	if len(r.Messages) != 3 || len(users) != 2 || users[0] != "[Bob|222]\nhello" {
		t.Fatalf("the observed messages must not replay as user turns: %+v", r.Messages)
	}
	want := ObservedHeader + "\n[Alice|111]\nCan we track the stock?\n[Alice|111]\nIt has lots and expiry dates\n\n" +
		CurrentHeader + "\n[Bob|222]\ncambio"
	if users[1] != want {
		t.Fatalf("got %q\nwant %q", users[1], want)
	}
	// The store keeps the addressed message without the block.
	msgs := messages(t, a, sess.ID)
	if got := msgs[len(msgs)-2].Msg.Content; got != "[Bob|222]\ncambio" {
		t.Fatalf("stored %q", got)
	}
}

func TestObservedBlockStaysInEachRoundOfTheTurn(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "echo", `{"s":"x"}`)), text("done"))
	a, sess := newAgent(t, fp, echoTool{})
	observe(t, a, sess.ID, "[Alice|111]\nside talk")
	// A message that arrives during the turn waits for the block of the next turn.
	fp.onRequest = func(i int) {
		if i == 0 {
			observe(t, a, sess.ID, "[Carol|333]\nlate talk")
		}
	}
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "[Bob|222]\ngo", Toolsets: all(), Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	r0, r1 := fp.requests[0], fp.requests[1]
	if r0.Messages[0].Content != r1.Messages[0].Content || !strings.HasPrefix(r1.Messages[0].Content, ObservedHeader) {
		t.Fatalf("the block must be the same in each round: %q / %q", r0.Messages[0].Content, r1.Messages[0].Content)
	}
	if len(r1.Messages) != 3 || r1.Messages[1].Role != provider.RoleAssistant || r1.Messages[2].Role != provider.RoleTool {
		t.Fatalf("an observed message must not split the tool round: %+v", r1.Messages)
	}
	if strings.Contains(r1.Messages[0].Content, "late talk") {
		t.Fatal("a message after the addressed message must not go into its block")
	}
	fp.script = []step{text("next")}
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "[Bob|222]\nnext", Toolsets: all(), Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	users := userContents(fp.requests[2].Messages)
	if len(users) != 2 || users[0] != "[Bob|222]\ngo" {
		t.Fatalf("an earlier addressed message replays without the block: %q", users)
	}
	if !strings.Contains(users[1], "side talk") || !strings.Contains(users[1], "late talk") ||
		!strings.HasSuffix(users[1], CurrentHeader+"\n[Bob|222]\nnext") {
		t.Fatalf("got %q", users[1])
	}
}

func TestViewAppliesObservedBlock(t *testing.T) {
	fp := scripted(text("ok"))
	a, sess := newAgent(t, fp)
	observe(t, a, sess.ID, "[Alice|111]\nside talk")
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "[Bob|222]\nhi", Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	observe(t, a, sess.ID, "[Alice|111]\nafter")
	v, err := a.View(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The review copies View, so View must match the last request of the parent.
	last := fp.requests[0].Messages
	if len(v) != len(last)+1 || v[0].Content != last[0].Content || v[1].Content != "ok" {
		t.Fatalf("view %+v\nrequest %+v", v, last)
	}
}

func TestObservedSkippedAfterCompaction(t *testing.T) {
	fp := scripted(text("SUM"), text("ok"))
	a, sess := compactingAgent(t, fp)
	observe(t, a, sess.ID, "[Alice|111]\nside talk")
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "[Bob|222]\nq", Toolsets: all(), Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	req := fp.requests[1]
	last := req.Messages[len(req.Messages)-1]
	if !strings.HasPrefix(last.Content, ObservedHeader+"\n[Alice|111]\nside talk\n\n") {
		t.Fatalf("got %q", last.Content)
	}
	for _, m := range req.Messages[:len(req.Messages)-1] {
		if strings.Contains(m.Content, "side talk") {
			t.Fatal("the observed message replays as a user turn")
		}
	}
}

func TestNoObservedNoBlock(t *testing.T) {
	view := []sessions.StoredMessage{
		{ID: 1, Msg: provider.Message{Role: provider.RoleUser, Content: "a"}},
		{ID: 2, Msg: provider.Message{Role: provider.RoleAssistant, Content: "b"}},
	}
	got := withObserved(view, 1)
	if len(got) != 2 || got[0].Msg.Content != "a" {
		t.Fatalf("%+v", got)
	}
}
