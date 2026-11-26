package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/dnwandana/tether-agent/internal/learn"
	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/skills"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// memHistory is an in-memory memory.History.
type memHistory struct {
	mu    sync.Mutex
	edits []memory.Edit
}

func (h *memHistory) Record(_ context.Context, e memory.Edit) (int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e.ID = int64(len(h.edits) + 1)
	h.edits = append(h.edits, e)
	return e.ID, nil
}

func (h *memHistory) Get(_ context.Context, id int64) (memory.Edit, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if id < 1 || int(id) > len(h.edits) {
		return memory.Edit{}, errors.New("no such edit")
	}
	return h.edits[id-1], nil
}

func (h *memHistory) MarkUndone(context.Context, int64, int64) error     { return nil }
func (h *memHistory) Recent(context.Context, int) ([]memory.Edit, error) { return nil, nil }
func (h *memHistory) Blocked(context.Context, string) (bool, error)      { return false, nil }
func (h *memHistory) AddBlock(context.Context, memory.Block) error       { return nil }
func (h *memHistory) ClearBlock(context.Context, string) error           { return nil }
func (h *memHistory) DeleteBlockOf(context.Context, int64) error         { return nil }

// fakeNotices is a NoticeStore that records each call.
type fakeNotices struct {
	mu    sync.Mutex
	keys  []string
	texts []string
	items [][]learn.NoticeItem
}

func (f *fakeNotices) Create(_ context.Context, key, text string, items []learn.NoticeItem) (learn.Notice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, key)
	f.texts = append(f.texts, text)
	f.items = append(f.items, items)
	return learn.Notice{ID: int64(len(f.keys)), Stamp: "a3f1"}, nil
}

// refTool is a tool that adds one edit ref of kind when it runs.
type refTool struct {
	name, set, kind string
	next            *int64
}

func (r refTool) Name() string       { return r.name }
func (r refTool) Toolset() string    { return r.set }
func (r refTool) ParallelSafe() bool { return false }
func (r refTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{Name: r.name, Description: r.name, Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (r refTool) Run(_ context.Context, _ json.RawMessage, env *tools.Env) (string, error) {
	*r.next++
	tools.AddEdit(env, r.kind, *r.next)
	return "ok", nil
}

// heldMemory is a memory tool that holds each write.
type heldMemory struct{ next *int64 }

func (heldMemory) Name() string       { return "memory" }
func (heldMemory) Toolset() string    { return tools.SetMemory }
func (heldMemory) ParallelSafe() bool { return false }
func (heldMemory) Schema() provider.ToolSchema {
	return provider.ToolSchema{Name: "memory", Description: "memory", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (h heldMemory) Run(_ context.Context, _ json.RawMessage, env *tools.Env) (string, error) {
	tools.MarkOutside(env, "web_fetch", "docs.z.ai")
	*h.next++
	tools.AddEdit(env, "held", *h.next)
	return tools.HeldResultPrefix + " this turn read outside content (web_fetch docs.z.ai).", nil
}

func TestCardText(t *testing.T) {
	const rule = "Do not send a file unless I ask"
	long := strings.Repeat("a", 301)
	many := make([]memory.Edit, 12)
	for i := range many {
		many[i] = memory.Edit{Target: "memory", Action: "add", NewText: "x"}
	}
	cases := []struct {
		name   string
		edits  []memory.Edit
		copies []skills.Copy
		extra  []string
		want   string
	}{
		{"user add with quote", []memory.Edit{{Target: "user", Action: "add", NewText: "The user prefers Go", Quote: "I prefer Go always"}}, nil, nil,
			"💾 User profile updated (add)\nNow: `The user prefers Go`\nYou said: \"I prefer Go always\""},
		{"memory replace", []memory.Edit{{Target: "memory", Action: "replace", OldText: "VPS = hetzner", NewText: "VPS = ovh"}}, nil, nil,
			"💾 Memory updated (replace)\nWas: `VPS = hetzner`\nNow: `VPS = ovh`"},
		{"user remove", []memory.Edit{{Target: "user", Action: "remove", OldText: "The user prefers Go"}}, nil, nil,
			"💾 User profile updated (remove)\nWas: `The user prefers Go`"},
		{"rule add", []memory.Edit{{Target: "rule", Action: "add", NewText: rule}}, nil, nil, "📌 Rule saved\nNow: `" + rule + "`"},
		{"rule replace", []memory.Edit{{Target: "rule", Action: "replace", OldText: rule, NewText: rule}}, nil, nil,
			"📌 Rule changed\nWas: `" + rule + "`\nNow: `" + rule + "`"},
		{"rule remove", []memory.Edit{{Target: "rule", Action: "remove", OldText: rule}}, nil, nil, "📌 Rule removed\nWas: `" + rule + "`"},
		{"move to note", []memory.Edit{{Target: "memory", Dest: "note:archive-memory", Action: "move", OldText: "Tether update procedure"}}, nil, nil,
			"💾 Memory updated (move)\nWas: `Tether update procedure`\nMoved to note archive-memory"},
		{"move to rule", []memory.Edit{{Target: "note:inbox-user", Dest: "rule", Action: "move", NewText: rule, Quote: "do not send a file unless I ask"}}, nil, nil,
			"📌 Rule saved\nNow: `" + rule + "`\nYou said: \"do not send a file unless I ask\""},
		{"note add", []memory.Edit{{Target: "note:inbox-user", Action: "add", NewText: "x"}}, nil, nil,
			"💾 Memory note updated (add): inbox-user\nNow: `x`"},
		{"cut of the old text only", []memory.Edit{{Target: "memory", Action: "replace", OldText: long, NewText: strings.Repeat("é", 500)}}, nil, nil,
			"💾 Memory updated (replace)\nWas: `" + strings.Repeat("a", 300) + "…`\nNow: `" + strings.Repeat("é", 500) + "`"},
		{"backtick and newline", []memory.Edit{{Target: "memory", Action: "add", NewText: "a `b`\n c"}}, nil, nil,
			"💾 Memory updated (add)\nNow: `a 'b' c`"},
		{"hidden rune", []memory.Edit{{Target: "memory", Action: "add", NewText: "VPS\u200b = ovh"}}, nil, nil,
			"💾 Memory updated (add)\nNow: `VPS<U+200B> = ovh`"},
		{"quote escape", []memory.Edit{{Target: "memory", Action: "add", NewText: "x", Quote: "use *bold*"}}, nil, nil,
			"💾 Memory updated (add)\nNow: `x`\nYou said: \"use \\*bold\\*\""},
		{"skill groups", nil, []skills.Copy{{Name: "a", Action: "patch"}, {Name: "b", Action: "write_file"}, {Name: "c", Action: "create"},
			{Name: "d", Action: "delete"}, {Name: "e", Action: "delete"}}, nil,
			"🧩 Skills updated: a, b\n🧩 Skill created: c\n🧩 Skills deleted: d, e"},
		{"skill escape", nil, []skills.Copy{{Name: "my_skill", Action: "patch"}}, nil, "🧩 Skill updated: my\\_skill"},
		{"expire", []memory.Edit{{Target: "memory", Action: "expire", OldText: "x"}}, nil, nil, ""},
		{"undo and redo", []memory.Edit{{Target: "memory", Action: "undo"}, {Target: "memory", Action: "redo"}}, nil, nil, ""},
		{"nothing", nil, nil, nil, ""},
		{"twelve adds", many, nil, nil, strings.Repeat("💾 Memory updated (add)\nNow: `x`\n", 10) + "… and 2 more. /memory log"},
		{"extra last", []memory.Edit{{Target: "memory", Action: "add", NewText: "x"}}, nil, []string{"User profile 1363 → 712 of 1375"},
			"💾 Memory updated (add)\nNow: `x`\nUser profile 1363 → 712 of 1375"},
	}
	for _, c := range cases {
		if got := CardText(c.edits, c.copies, c.extra); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestHoldCards(t *testing.T) {
	var sent []Notice
	card, release := HoldCards(func(n Notice) { sent = append(sent, n) })
	card(Notice{Text: "a", ID: 1})
	if len(sent) != 0 {
		t.Fatal("held before release")
	}
	release()
	if len(sent) != 1 || sent[0].Text != "a" {
		t.Fatal(sent)
	}
	card(Notice{Text: "b"})
	if len(sent) != 2 {
		t.Fatal("after release: send at once")
	}
	release()
	if len(sent) != 2 {
		t.Fatal("a second release sends nothing again")
	}
}

func TestHoldCardsKeepOrderUnderConcurrency(t *testing.T) {
	var (
		mu   sync.Mutex
		sent []Notice
	)
	card, release := HoldCards(func(n Notice) { mu.Lock(); sent = append(sent, n); mu.Unlock() })
	card(Notice{Text: "a"})
	card(Notice{Text: "b"})
	var wg sync.WaitGroup
	wg.Go(release)
	card(Notice{Text: "c"})
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 3 || sent[0].Text != "a" || sent[1].Text != "b" {
		t.Fatal(sent)
	}
}

// cardAgent returns an Agent with the real memory tool over a store with a history, and
// the notice store.
func cardAgent(t *testing.T, fp *fakeProv, extra ...tools.Tool) (*Agent, *sessions.Session, *fakeNotices) {
	t.Helper()
	m, err := memory.Open(t.TempDir(), 2200, 1375, 1000)
	if err != nil {
		t.Fatal(err)
	}
	m.History = &memHistory{}
	ts := append([]tools.Tool{tools.MemoryTool{Store: m, Quotes: acceptQuotes{}}}, extra...)
	a, sess := newAgent(t, fp, ts...)
	a.Memory = m
	n := &fakeNotices{}
	a.Notices = n
	return a, sess, n
}

func runCard(t *testing.T, a *Agent, sess *sessions.Session, turn Turn) {
	t.Helper()
	turn.Session, turn.UserText, turn.Toolsets, turn.System, turn.Sink = sess, "hi", all(), "SYS", &recSink{}
	if _, err := a.RunTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
}

const addVPS = `{"action":"add","target":"memory","content":"VPS = ovh"}`

func TestChatTurnCard(t *testing.T) {
	a, sess, n := cardAgent(t, scripted(toolCalls(tc("c1", "memory", addVPS)), text("Saved.")))
	var cards []Notice
	runCard(t, a, sess, Turn{Card: func(c Notice) { cards = append(cards, c) }})
	want := Notice{Text: "💾 Memory updated (add)\nNow: `VPS = ovh`", ID: 1, Stamp: "a3f1"}
	if len(cards) != 1 || cards[0] != want {
		t.Fatalf("%+v", cards)
	}
	if len(n.keys) != 1 || n.keys[0] != sess.Key || !reflect.DeepEqual(n.items[0], []learn.NoticeItem{{Kind: "memory", ID: 1}}) {
		t.Fatal(n.keys, n.items)
	}
}

func TestChatTurnTwoWritesOneCard(t *testing.T) {
	a, sess, n := cardAgent(t, scripted(
		toolCalls(tc("c1", "memory", addVPS)),
		toolCalls(tc("c2", "memory", `{"action":"add","target":"memory","content":"DNS = cf"}`)),
		text("Saved.")))
	var cards []Notice
	runCard(t, a, sess, Turn{Card: func(c Notice) { cards = append(cards, c) }})
	if len(cards) != 1 || len(n.items) != 1 || len(n.items[0]) != 2 {
		t.Fatal(cards, n.items)
	}
}

func TestChatTurnCardFallsBackToNotify(t *testing.T) {
	a, sess, _ := cardAgent(t, scripted(toolCalls(tc("c1", "memory", addVPS)), text("Saved.")))
	var notified []string
	runCard(t, a, sess, Turn{Notify: func(s string) { notified = append(notified, s) }})
	if !reflect.DeepEqual(notified, []string{"💾 Memory updated (add)\nNow: `VPS = ovh`"}) {
		t.Fatal(notified)
	}
}

func TestChatTurnCardWithoutStore(t *testing.T) {
	a, sess, _ := cardAgent(t, scripted(toolCalls(tc("c1", "memory", addVPS)), text("Saved.")))
	a.Notices = nil
	var cards []Notice
	runCard(t, a, sess, Turn{Card: func(c Notice) { cards = append(cards, c) }})
	if len(cards) != 1 || cards[0].ID != 0 || cards[0].Stamp != "" || cards[0].Text == "" {
		t.Fatalf("%+v", cards)
	}
}

func TestChatTurnSkillChangeNoCard(t *testing.T) {
	a, sess, n := cardAgent(t, scripted(toolCalls(tc("c1", "skill_manage", `{"action":"patch","name":"a"}`)), text("Done.")),
		refTool{name: "skill_manage", set: tools.SetSkills, kind: "skill", next: new(int64)})
	var cards []Notice
	runCard(t, a, sess, Turn{Card: func(c Notice) { cards = append(cards, c) }})
	if len(cards) != 0 || len(n.keys) != 0 {
		t.Fatal(cards)
	}
}

func TestCronTurnNoCard(t *testing.T) {
	a, _, n := cardAgent(t, scripted(toolCalls(tc("c1", "memory", addVPS)), text("Saved.")))
	sess, err := a.Store.Create(ctx, sessions.Session{Key: "cron:j", Source: "cron", Kind: "cron"})
	if err != nil {
		t.Fatal(err)
	}
	var cards []Notice
	runCard(t, a, sess, Turn{Card: func(c Notice) { cards = append(cards, c) }})
	if len(cards) != 0 || len(n.keys) != 0 {
		t.Fatal(cards)
	}
}

func TestChatTurnHeldCard(t *testing.T) {
	a, sess := newAgent(t, scripted(toolCalls(tc("c1", "memory", `{"action":"add","target":"memory","content":"x"}`)), text("Ok.")),
		heldMemory{next: new(int64)})
	n := &fakeNotices{}
	a.Notices = n
	var cards []Notice
	runCard(t, a, sess, Turn{Card: func(c Notice) { cards = append(cards, c) }})
	if len(cards) != 1 || len(n.items) != 1 || !reflect.DeepEqual(n.items[0], []learn.NoticeItem{{Kind: "held", ID: 1}}) {
		t.Fatal(cards, n.items)
	}
	if !strings.HasPrefix(cards[0].Text, "💾 Memory write held: this turn read outside content (web_fetch docs.z.ai).\nadd to memory: `x`") {
		t.Fatal(cards[0].Text)
	}
}

func reviewCardAgent(t *testing.T, mem tools.Tool, extra ...tools.Tool) (*Agent, *sessions.Session, *fakeNotices, *fakeProv) {
	t.Helper()
	fp := scripted(text("ok"))
	a, sess := newAgent(t, fp, append([]tools.Tool{echoTool{}, mem}, extra...)...)
	n := &fakeNotices{}
	a.Notices = n
	userTurn(t, a, sess, "hi")
	return a, sess, n, fp
}

func TestReviewCard(t *testing.T) {
	m, err := memory.Open(t.TempDir(), 2200, 1375, 1000)
	if err != nil {
		t.Fatal(err)
	}
	m.History = &memHistory{}
	next := new(int64)
	*next = 10
	a, sess, n, fp := reviewCardAgent(t, tools.MemoryTool{Store: m, Quotes: acceptQuotes{}},
		refTool{name: "skill_manage", set: tools.SetSkills, kind: "skill", next: next})
	a.Memory = m
	fp.script = []step{toolCalls(
		tc("r1", "memory", `{"action":"add","target":"memory","content":"x"}`),
		tc("r2", "skill_manage", `{"action":"patch","name":"a"}`),
		tc("r3", "skill_manage", `{"action":"patch","name":"b"}`),
	), text("done")}
	var cards []Notice
	if _, err := a.RunReview(ctx, ReviewRequest{Session: sess, System: "SYS", Memory: true, Skills: true,
		Card: func(c Notice) { cards = append(cards, c) }}); err != nil {
		t.Fatal(err)
	}
	want := "💾 Memory updated (add)\nNow: `x`\n🧩 Skills updated: a, b"
	if len(cards) != 1 || cards[0].Text != want || cards[0].ID != 1 {
		t.Fatalf("%+v", cards)
	}
	wantItems := []learn.NoticeItem{{Kind: "memory", ID: 1}, {Kind: "skill", ID: 11}, {Kind: "skill", ID: 12}}
	if len(n.keys) != 1 || n.keys[0] != sess.Key || !reflect.DeepEqual(n.items[0], wantItems) {
		t.Fatal(n.keys, n.items)
	}
}

func TestReviewCardSkipsHeld(t *testing.T) {
	a, sess, n, fp := reviewCardAgent(t, heldMemory{next: func() *int64 { v := int64(4); return &v }()})
	fp.script = []step{toolCalls(tc("r1", "memory", `{"action":"add","target":"memory","content":"x"}`)), text("done")}
	var cards []Notice
	saved, err := a.RunReview(ctx, ReviewRequest{Session: sess, System: "SYS", Memory: true,
		Card: func(c Notice) { cards = append(cards, c) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 0 {
		t.Fatal(saved)
	}
	if len(cards) != 1 || len(n.items) != 1 || !reflect.DeepEqual(n.items[0], []learn.NoticeItem{{Kind: "held", ID: 5}}) {
		t.Fatal(cards, n.items)
	}
	first, _, _ := strings.Cut(cards[0].Text, "\n")
	if first != "💾 Memory write held: this turn read outside content (web_fetch docs.z.ai)." {
		t.Fatal(cards[0].Text)
	}
}
