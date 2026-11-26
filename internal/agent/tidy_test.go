package agent

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// tidyAgent returns an Agent with the real memory tool and echo. The store holds the two VPS
// entries and the note inbox-user. The session ran one turn, so it has a request shape.
func tidyAgent(t *testing.T, fp *fakeProv) (*Agent, *sessions.Session, *memory.Store, *fakeNotices) {
	t.Helper()
	m, err := memory.Open(t.TempDir(), 2200, 1375, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"VPS = hetzner", "The VPS runs at hetzner"} {
		if _, err := m.Apply(memory.Op{Action: "add", Target: memory.TargetMemory, Content: c}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Apply(memory.Op{Action: "add", Target: memory.TargetUser, Topic: "inbox-user", Content: "Do not send a file unless I ask"}); err != nil {
		t.Fatal(err)
	}
	m.History = &memHistory{}
	a, sess := newAgent(t, fp, echoTool{}, tools.MemoryTool{Store: m, Quotes: acceptQuotes{}})
	a.Memory = m
	n := &fakeNotices{}
	a.Notices = n
	return a, sess, m, n
}

// tidyFillCount returns the rune count of the entries of the target, joined with the separator.
func tidyFillCount(m *memory.Store, target string) int {
	return utf8.RuneCountInString(strings.Join(m.Entries(target), memory.Separator))
}

// tidyRun starts a tidy and returns the card channel.
func tidyRun(t *testing.T, a *Agent, sess *sessions.Session) chan Notice {
	t.Helper()
	cards := make(chan Notice, 4)
	if err := a.RunTidy(ctx, sess, func(n Notice) { cards <- n }); err != nil {
		t.Fatal(err)
	}
	return cards
}

func tidyCard(t *testing.T, cards chan Notice) Notice {
	t.Helper()
	select {
	case n := <-cards:
		return n
	case <-time.After(5 * time.Second):
		t.Fatal("no tidy card")
	}
	return Notice{}
}

func TestTidyMergesAndCards(t *testing.T) {
	fp := scripted(text("ok"))
	a, sess, m, n := tidyAgent(t, fp)
	userTurn(t, a, sess, "hi")
	before := tidyFillCount(m, memory.TargetMemory)
	fp.mu.Lock()
	fp.script = []step{toolCalls(
		tc("t1", "memory", `{"action":"replace","target":"memory","old_text":"VPS = hetzner","content":"VPS = hetzner (Falkenstein)"}`),
		tc("t2", "memory", `{"action":"remove","target":"memory","old_text":"The VPS runs at hetzner"}`),
	), text("done")}
	fp.mu.Unlock()
	card := tidyCard(t, tidyRun(t, a, sess))
	after := tidyFillCount(m, memory.TargetMemory)
	want := "💾 Memory updated (replace)\nWas: `VPS = hetzner`\nNow: `VPS = hetzner (Falkenstein)`\n" +
		"💾 Memory updated (move)\nWas: `The VPS runs at hetzner`\nMoved to note archive-memory\n" +
		"Owner rules 0 → 0 of 1000\nMemory " + strconv.Itoa(before) + " → " + strconv.Itoa(after) + " of 2200\nUser profile 0 → 0 of 1375"
	if card.Text != want || card.ID != 1 || card.Stamp != "a3f1" {
		t.Fatalf("%+v\nwant %q", card, want)
	}
	if len(n.items) != 1 || len(n.items[0]) != 2 {
		t.Fatal(n.items)
	}
	body, err := m.Read("archive-memory", 0, memory.MaxReadChars)
	if err != nil || !strings.Contains(body, "The VPS runs at hetzner") {
		t.Fatal(body, err)
	}
}

func TestTidyPromptHasTheFiles(t *testing.T) {
	fp := scripted(text("ok"))
	a, sess, m, _ := tidyAgent(t, fp)
	userTurn(t, a, sess, "hi")
	used := tidyFillCount(m, memory.TargetMemory)
	fp.mu.Lock()
	fp.script = []step{text("Nothing to change.")}
	fp.mu.Unlock()
	tidyCard(t, tidyRun(t, a, sess))
	reqs := fp.requests
	if len(reqs) != 2 {
		t.Fatal(len(reqs))
	}
	req := reqs[1]
	if len(req.Messages) != 1 || req.Messages[0].Role != provider.RoleUser {
		t.Fatalf("%+v", req.Messages)
	}
	last := req.Messages[len(req.Messages)-1].Content
	for _, w := range []string{
		"- " + memory.EntryID("VPS = hetzner") + ": VPS = hetzner",
		"## MEMORY (target memory, " + strconv.Itoa(used) + "/2200)",
		"## OWNER RULES (target rule, 0/1000)\n(empty)",
		"## NOTE inbox-user",
		"Do not send a file unless I ask",
		"## TOPICS",
	} {
		if !strings.Contains(last, w) {
			t.Fatalf("missing %q in\n%s", w, last)
		}
	}
	if !strings.HasPrefix(last, TidyPrompt) {
		t.Fatal(last)
	}
	if req.System != "SYS" || len(req.Tools) != len(reqs[0].Tools) {
		t.Fatal(req.System, len(req.Tools), len(reqs[0].Tools))
	}
}

func TestTidyRunsAsKindTidy(t *testing.T) {
	fp := scripted(text("ok"))
	a, sess, _, _ := tidyAgent(t, fp)
	userTurn(t, a, sess, "hi")
	fp.mu.Lock()
	fp.script = []step{toolCalls(tc("e1", "echo", `{"s":"x"}`)), text("done")}
	fp.mu.Unlock()
	tidyCard(t, tidyRun(t, a, sess))
	reqs := fp.requests
	if got := lastToolContent(reqs[len(reqs)-1]); got != "tidy runs may only use memory" {
		t.Fatal(got)
	}
	var kind, parent string
	if err := a.DB.QueryRowContext(ctx, `SELECT kind, parent_id FROM sessions WHERE kind = 'tidy'`).Scan(&kind, &parent); err != nil {
		t.Fatal(err)
	}
	if kind != sessions.KindTidy || parent != sess.ID {
		t.Fatal(kind, parent)
	}
	if !noReviews(sessions.KindTidy) {
		t.Fatal("a tidy session gets a review")
	}
}

func TestTidyNothingToChange(t *testing.T) {
	fp := scripted(text("ok"))
	a, sess, _, n := tidyAgent(t, fp)
	userTurn(t, a, sess, "hi")
	fp.mu.Lock()
	fp.script = []step{text("Nothing to change.")}
	fp.mu.Unlock()
	card := tidyCard(t, tidyRun(t, a, sess))
	if card != (Notice{Text: "Memory tidy: nothing to change."}) || len(n.keys) != 0 {
		t.Fatalf("%+v %v", card, n.keys)
	}
}

func TestSecondTidyIsRefused(t *testing.T) {
	fp := scripted(text("ok"))
	a, sess, _, _ := tidyAgent(t, fp)
	userTurn(t, a, sess, "hi")
	gate := make(chan struct{})
	reached := make(chan struct{})
	fp.mu.Lock()
	fp.script = []step{text("Nothing to change.")}
	fp.onRequest = func(i int) {
		if i == 1 {
			close(reached)
			<-gate
		}
	}
	fp.mu.Unlock()
	cards := tidyRun(t, a, sess)
	<-reached
	err := a.RunTidy(ctx, sess, nil)
	if !errors.Is(err, ErrTidyRunning) || err.Error() != "A memory tidy runs already." {
		t.Fatal(err)
	}
	close(gate)
	tidyCard(t, cards)
	if !a.WaitReviews(5 * time.Second) {
		t.Fatal("the tidy did not end")
	}
	fp.mu.Lock()
	fp.script = []step{text("Nothing to change.")}
	fp.mu.Unlock()
	if err := a.RunTidy(ctx, sess, nil); err != nil {
		t.Fatal(err)
	}
	a.WaitReviews(5 * time.Second)
}

func TestTidyFlagClearsWhenNotStarted(t *testing.T) {
	fp := scripted(text("ok"))
	a, sess, _, _ := tidyAgent(t, fp)
	userTurn(t, a, sess, "hi")
	bg, cancel := context.WithCancel(context.Background())
	cancel()
	a.Background = bg
	if err := a.RunTidy(ctx, sess, nil); err == nil {
		t.Fatal("no error after the end of Background")
	}
	if a.tidyRunning.Load() {
		t.Fatal("the flag stays set")
	}
}

func TestTidyWorksInReadOnlyMode(t *testing.T) {
	fp := scripted(text("ok"))
	a, sess, _, _ := tidyAgent(t, fp)
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "hi", Toolsets: all(), System: "SYS", Sink: &recSink{},
		Mode: security.ModeReadOnly}); err != nil {
		t.Fatal(err)
	}
	fp.mu.Lock()
	fp.script = []step{text("Nothing to change.")}
	fp.mu.Unlock()
	card := tidyCard(t, tidyRun(t, a, sess))
	if card.Text != "Memory tidy: nothing to change." {
		t.Fatal(card)
	}
}

func TestTidyNeedsMemory(t *testing.T) {
	fp := scripted(text("ok"))
	a, sess := newAgent(t, fp, echoTool{})
	if err := a.RunTidy(ctx, sess, nil); err == nil {
		t.Fatal("no error without the memory tool")
	}
	var n int
	if err := a.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE kind = 'tidy'`).Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if a.tidyRunning.Load() {
		t.Fatal("the flag stays set")
	}
}
