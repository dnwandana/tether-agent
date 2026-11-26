package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/learn"
	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/skills"
)

// noticeHist is an in-memory memory.History for the notice tests.
type noticeHist struct {
	mu     sync.Mutex
	edits  []memory.Edit
	blocks []memory.Block
}

func (f *noticeHist) Record(_ context.Context, e memory.Edit) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e.ID = int64(len(f.edits) + 1)
	f.edits = append(f.edits, e)
	return e.ID, nil
}

func (f *noticeHist) Get(_ context.Context, id int64) (memory.Edit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id < 1 || int(id) > len(f.edits) {
		return memory.Edit{}, fmt.Errorf("no edit %d", id)
	}
	return f.edits[id-1], nil
}

func (f *noticeHist) MarkUndone(_ context.Context, id, undoID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits[id-1].UndoneBy = undoID
	return nil
}

func (f *noticeHist) Recent(context.Context, int) ([]memory.Edit, error) { return nil, nil }
func (f *noticeHist) Blocked(context.Context, string) (bool, error)      { return false, nil }
func (f *noticeHist) AddBlock(context.Context, memory.Block) error       { return nil }
func (f *noticeHist) ClearBlock(context.Context, string) error           { return nil }
func (f *noticeHist) DeleteBlockOf(_ context.Context, editID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blocks = slices.DeleteFunc(f.blocks, func(b memory.Block) bool { return b.EditID == editID })
	return nil
}

// noticeEnv is the setup of a notice test.
type noticeEnv struct {
	c     *CommandCtx
	sent  *[]Reply
	n     *learn.Notices
	mem   *memory.Store
	hist  *noticeHist
	clock time.Time
}

func newNoticeEnv(t *testing.T) *noticeEnv {
	t.Helper()
	c, sent := interactiveCtx(t)
	mem, err := memory.Open(t.TempDir(), 2200, 1375, 1000)
	if err != nil {
		t.Fatal(err)
	}
	hist := &noticeHist{}
	mem.History = hist
	e := &noticeEnv{c: c, sent: sent, n: learn.NewNotices(c.Deps.Agent.DB), mem: mem, hist: hist, clock: time.Now()}
	c.Deps.Learn = &LearnDeps{Memory: mem, Notices: e.n}
	setNow(t, e.clock)
	return e
}

// add writes one entry and returns the id of its history row.
func (e *noticeEnv) add(t *testing.T, target, content string) int64 {
	t.Helper()
	res, err := e.mem.ApplyContext(context.Background(), memory.Op{Action: "add", Target: target, Content: content, Source: "chat"})
	if err != nil || len(res.EditIDs) != 1 {
		t.Fatal(res, err)
	}
	return res.EditIDs[0]
}

func (e *noticeEnv) notice(t *testing.T, text string, items ...learn.NoticeItem) learn.Notice {
	t.Helper()
	n, err := e.n.Create(context.Background(), e.c.Key, text, items)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *noticeEnv) state(t *testing.T, id int64) string {
	t.Helper()
	n, err := e.n.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return n.State
}

func ref(n learn.Notice) string { return fmt.Sprintf("%d.%s", n.ID, n.Stamp) }

func oneUserNotice(t *testing.T) (*noticeEnv, learn.Notice, int64) {
	e := newNoticeEnv(t)
	id := e.add(t, "user", "Time zone: Asia/Jakarta.")
	n := e.notice(t, "💾 User profile updated (add)\nNow: `Time zone: Asia/Jakarta.`", learn.NoticeItem{Kind: "memory", ID: id})
	return e, n, id
}

func TestNoticeCommandFields(t *testing.T) {
	cmd, ok := Commands()["notice"]
	if !ok || cmd.Name != "notice" || !cmd.WhileBusy || cmd.MemberOK || cmd.Help == "" {
		t.Fatalf("%+v", cmd)
	}
}

func TestNoticeOK(t *testing.T) {
	e, n, _ := oneUserNotice(t)
	run(t, e.c, "/notice ok "+ref(n))
	r := lastSent(t, e.sent)
	if r.Text != n.Text+"\n✓ Kept." || r.Buttons != nil || !r.Edit {
		t.Fatalf("%+v", r)
	}
	if s := e.state(t, n.ID); s != "kept" {
		t.Fatal(s)
	}
}

func TestNoticeUndoOne(t *testing.T) {
	e, n, _ := oneUserNotice(t)
	run(t, e.c, "/notice undo "+ref(n))
	r := lastSent(t, e.sent)
	if !strings.HasSuffix(r.Text, "\n↩ Undone.") || !r.Edit {
		t.Fatalf("%+v", r)
	}
	want := [][]Button{{{"↪ Redo", "/notice redo " + ref(n)}}, {{"✓ OK", "/notice ok " + ref(n)}}}
	if !reflect.DeepEqual(r.Buttons, want) {
		t.Fatalf("%+v", r.Buttons)
	}
	if s := e.state(t, n.ID); s != "undone" {
		t.Fatal(s)
	}
	if got := e.mem.Entries(memory.TargetUser); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestNoticeRedo(t *testing.T) {
	e, n, _ := oneUserNotice(t)
	run(t, e.c, "/notice undo "+ref(n))
	run(t, e.c, "/notice redo "+ref(n))
	r := lastSent(t, e.sent)
	if !strings.HasSuffix(r.Text, "\n↪ Redone.") || !r.Edit {
		t.Fatalf("%+v", r)
	}
	if got := e.mem.Entries(memory.TargetUser); len(got) != 1 {
		t.Fatal(got)
	}
	if s := e.state(t, n.ID); s != "open" {
		t.Fatal(s)
	}
	// An undo after the redo works again.
	run(t, e.c, "/notice undo "+ref(n))
	if got := e.mem.Entries(memory.TargetUser); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestNoticeUndoTwiceNoChange(t *testing.T) {
	e, n, _ := oneUserNotice(t)
	run(t, e.c, "/notice undo "+ref(n))
	first := lastSent(t, e.sent)
	run(t, e.c, "/notice undo "+ref(n))
	if second := lastSent(t, e.sent); !reflect.DeepEqual(first, second) {
		t.Fatalf("%+v\n%+v", first, second)
	}
	if got := e.mem.Entries(memory.TargetUser); len(got) != 0 {
		t.Fatal(got)
	}
	if len(e.hist.edits) != 2 {
		t.Fatal("a second undo wrote a row", len(e.hist.edits))
	}
}

func TestNoticeStampMismatchNeverUndoes(t *testing.T) {
	e, n, id := oneUserNotice(t)
	run(t, e.c, fmt.Sprintf("/notice undo %d.0000", n.ID))
	if r := lastSent(t, e.sent); r.Text != "This button is too old." || r.Buttons != nil || !r.Edit {
		t.Fatalf("%+v", r)
	}
	if got := e.mem.Entries(memory.TargetUser); len(got) != 1 {
		t.Fatal(got)
	}
	if e.hist.edits[id-1].UndoneBy != 0 {
		t.Fatal("row undone")
	}
	if s := e.state(t, n.ID); s != "open" {
		t.Fatal(s)
	}
	run(t, e.c, "/notice undo 999.a3f1")
	if r := lastSent(t, e.sent); r.Text != "This button is too old." {
		t.Fatalf("%+v", r)
	}
}

func TestNoticeOlderThan30Days(t *testing.T) {
	e, n, _ := oneUserNotice(t)
	setNow(t, n.At.Add(31*24*time.Hour))
	run(t, e.c, "/notice undo "+ref(n))
	if r := lastSent(t, e.sent); r.Text != "This notice is older than 30 days. Use /memory log or /skills log." {
		t.Fatalf("%+v", r)
	}
	if got := e.mem.Entries(memory.TargetUser); len(got) != 1 {
		t.Fatal(got)
	}
}

func TestNoticeUnavailable(t *testing.T) {
	c, sent := interactiveCtx(t)
	run(t, c, "/notice ok 1.a3f1")
	if r := lastSent(t, sent); r.Text != "Change notices are not available here." || !r.Edit {
		t.Fatalf("%+v", r)
	}
}

func twoItemNotice(t *testing.T) (*noticeEnv, learn.Notice) {
	e := newNoticeEnv(t)
	a := e.add(t, "user", "Time zone: Asia/Jakarta.")
	b := e.add(t, "memory", "The project uses Go 1.26.")
	n := e.notice(t, "💾 Two changes", learn.NoticeItem{Kind: "memory", ID: a}, learn.NoticeItem{Kind: "memory", ID: b})
	return e, n
}

func TestNoticePartialUndo(t *testing.T) {
	e, n := twoItemNotice(t)
	if _, err := e.mem.ApplyContext(context.Background(), memory.Op{Action: "replace", Target: "user",
		OldText: "Time zone", Content: "Time zone: Asia/Makassar.", Source: "chat"}); err != nil {
		t.Fatal(err)
	}
	run(t, e.c, "/notice undo "+ref(n))
	r := lastSent(t, e.sent)
	if !strings.HasSuffix(r.Text, "\n↩ Undone 1 of 2. Changed since this notice: User profile. Use /memory log.") {
		t.Fatalf("%q", r.Text)
	}
	if got := e.mem.Entries(memory.TargetMemory); len(got) != 0 {
		t.Fatal(got)
	}
	if got := e.mem.Entries(memory.TargetUser); len(got) != 1 || !strings.Contains(got[0], "Makassar") {
		t.Fatal(got)
	}
	if s := e.state(t, n.ID); s != "undone" {
		t.Fatal(s)
	}
}

func TestNoticePickAndUndoItem(t *testing.T) {
	e, n := twoItemNotice(t)
	run(t, e.c, "/notice pick "+ref(n))
	r := lastSent(t, e.sent)
	want := [][]Button{
		{{"↩ 1. User profile", "/notice undo " + ref(n) + " 1"}},
		{{"↩ 2. Memory", "/notice undo " + ref(n) + " 2"}},
		{{"« Back", "/notice show " + ref(n)}},
	}
	if r.Text != n.Text || !r.Edit || !reflect.DeepEqual(r.Buttons, want) {
		t.Fatalf("%+v", r)
	}
	run(t, e.c, "/notice undo "+ref(n)+" 2")
	r = lastSent(t, e.sent)
	if !strings.HasSuffix(r.Text, "\n↩ Undone: Memory.") {
		t.Fatalf("%q", r.Text)
	}
	if len(e.mem.Entries(memory.TargetMemory)) != 0 || len(e.mem.Entries(memory.TargetUser)) != 1 {
		t.Fatal("wrong item undone")
	}
	if s := e.state(t, n.ID); s != "open" {
		t.Fatal(s)
	}
	if !reflect.DeepEqual(r.Buttons, CardReply(n, true).Buttons) {
		t.Fatalf("%+v", r.Buttons)
	}
	run(t, e.c, "/notice undo "+ref(n)+" 3")
	if r := lastSent(t, e.sent); r.Text != "There is no item 3." {
		t.Fatalf("%+v", r)
	}
	run(t, e.c, "/notice show "+ref(n))
	if r := lastSent(t, e.sent); !reflect.DeepEqual(r, withEdit(CardReply(n, true))) {
		t.Fatalf("%+v", r)
	}
}

func withEdit(r Reply) Reply { r.Edit = true; return r }

// archiveStore is a skills.CuratorStore over a map.
type archiveStore struct{ flags map[string]skills.Flag }

func (s *archiveStore) Flags(context.Context) (map[string]skills.Flag, error) {
	return maps.Clone(s.flags), nil
}
func (s *archiveStore) SaveFlag(_ context.Context, f skills.Flag) (skills.Flag, error) {
	s.flags[f.Name] = f
	return f, nil
}
func (s *archiveStore) LastActivity(context.Context, string) (time.Time, bool, error) {
	return time.Time{}, false, nil
}
func (s *archiveStore) Backfill(context.Context) error { return nil }
func (s *archiveStore) LastRun(context.Context) (time.Time, bool, error) {
	return time.Time{}, false, nil
}
func (s *archiveStore) SetLastRun(context.Context, time.Time) error { return nil }

func TestNoticeArchiveRestore(t *testing.T) {
	e := newNoticeEnv(t)
	dir := t.TempDir()
	arch := filepath.Join(dir, ".archive", "research__graphkb-7")
	if err := os.MkdirAll(arch, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: graphkb\ndescription: test skill\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(arch, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	st := &archiveStore{flags: map[string]skills.Flag{"graphkb": {ID: 7, Name: "graphkb", RelDir: "research/graphkb",
		ArchivedAt: time.Now(), ArchivedPath: arch}}}
	refresh := 0
	e.c.Deps.Learn.Curator = &skills.Curator{Dir: dir, Store: st, Now: time.Now}
	e.c.Deps.Learn.RefreshMenu = func(context.Context) { refresh++ }
	n := e.notice(t, "🗄 Archived graphkb", learn.NoticeItem{Kind: "archive", ID: 7})
	r := CardReply(n, true)
	if r.Buttons[0][0].Text != "↩ Restore" {
		t.Fatalf("%+v", r.Buttons)
	}
	run(t, e.c, "/notice undo "+ref(n))
	if _, err := os.Stat(filepath.Join(dir, "research", "graphkb", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if refresh != 1 {
		t.Fatal(refresh)
	}
	if s := e.state(t, n.ID); s != "undone" {
		t.Fatal(s)
	}
	got := lastSent(t, e.sent)
	if !strings.HasSuffix(got.Text, "\n↩ Undone.") || len(got.Buttons) != 1 || got.Buttons[0][0].Text != "✓ OK" {
		t.Fatalf("%+v", got)
	}
}

func TestNoticeBadForm(t *testing.T) {
	e, n, _ := oneUserNotice(t)
	for _, args := range []string{"", "undo 42", "undo 42.XYZ1", "undo 0.a3f1", "undo -1.a3f1", "frob " + ref(n), "ok " + ref(n) + " 1"} {
		run(t, e.c, strings.TrimSpace("/notice "+args))
		if r := lastSent(t, e.sent); r.Text != "Use /notice undo|redo|ok|pick <id>.<stamp>." {
			t.Fatalf("%q: %+v", args, r)
		}
	}
}

func TestNoticeSaveWithoutHandler(t *testing.T) {
	e := newNoticeEnv(t)
	n := e.notice(t, "held", learn.NoticeItem{Kind: "held", ID: 5})
	old := noticeHeldAnswer
	t.Cleanup(func() { noticeHeldAnswer = old })
	noticeHeldAnswer = nil
	run(t, e.c, "/notice save "+ref(n))
	if r := lastSent(t, e.sent); r.Text != "Memory commands are not available here." {
		t.Fatalf("%+v", r)
	}
	var gotID int64
	var gotSave bool
	noticeHeldAnswer = func(_ context.Context, _ *CommandCtx, id int64, save bool) { gotID, gotSave = id, save }
	run(t, e.c, "/notice drop "+ref(n))
	if gotID != 5 || gotSave {
		t.Fatal(gotID, gotSave)
	}
}

func TestCardReplyButtons(t *testing.T) {
	one := learn.Notice{ID: 42, Stamp: "a3f1", Text: "T", State: "open", Items: []learn.NoticeItem{{Kind: "memory", ID: 1}}}
	two := one
	two.Items = []learn.NoticeItem{{Kind: "memory", ID: 1}, {Kind: "skill", ID: 2}}
	undone := one
	undone.State = "undone"
	kept := one
	kept.State = "kept"
	cur := one
	cur.Items = []learn.NoticeItem{{Kind: "archive", ID: 1}}
	curTwo := cur
	curTwo.Items = []learn.NoticeItem{{Kind: "archive", ID: 1}, {Kind: "archive", ID: 2}}
	curUndone := cur
	curUndone.State = "undone"
	held := one
	held.Items = []learn.NoticeItem{{Kind: "held", ID: 3}}
	b := func(text, act string) []Button { return []Button{{Text: text, Data: act}} }
	for name, tc := range map[string]struct {
		n    learn.Notice
		want [][]Button
	}{
		"one":       {one, [][]Button{b("↩ Undo", "/notice undo 42.a3f1"), b("✓ OK", "/notice ok 42.a3f1")}},
		"two":       {two, [][]Button{b("↩ Undo all", "/notice undo 42.a3f1"), b("Undo one ›", "/notice pick 42.a3f1"), b("✓ OK", "/notice ok 42.a3f1")}},
		"undone":    {undone, [][]Button{b("↪ Redo", "/notice redo 42.a3f1"), b("✓ OK", "/notice ok 42.a3f1")}},
		"kept":      {kept, nil},
		"curator":   {cur, [][]Button{b("↩ Restore", "/notice undo 42.a3f1"), b("✓ OK", "/notice ok 42.a3f1")}},
		"curator2":  {curTwo, [][]Button{b("↩ Restore all", "/notice undo 42.a3f1"), b("Restore one ›", "/notice pick 42.a3f1"), b("✓ OK", "/notice ok 42.a3f1")}},
		"curUndone": {curUndone, [][]Button{b("✓ OK", "/notice ok 42.a3f1")}},
		"held":      {held, [][]Button{b("❌ Drop", "/notice drop 42.a3f1"), b("✅ Save", "/notice save 42.a3f1")}},
	} {
		r := CardReply(tc.n, true)
		if r.Text != "T" || !reflect.DeepEqual(r.Buttons, tc.want) {
			t.Errorf("%s: %q %+v", name, r.Text, r.Buttons)
		}
	}
}

func TestCardReplyLines(t *testing.T) {
	n := learn.Notice{ID: 42, Stamp: "a3f1", Text: "T", State: "open", Items: []learn.NoticeItem{{Kind: "memory", ID: 1}}}
	r := CardReply(n, false)
	if r.Text != "T\n\nUndo: /notice undo 42.a3f1\nOK: /notice ok 42.a3f1" || r.Buttons != nil {
		t.Fatalf("%q %+v", r.Text, r.Buttons)
	}
	n.Items = append(n.Items, learn.NoticeItem{Kind: "memory", ID: 2})
	if r := CardReply(n, false); r.Text != "T\n\nUndo: /notice undo 42.a3f1\nUndo one: /notice undo 42.a3f1 <n>\nOK: /notice ok 42.a3f1" {
		t.Fatalf("%q", r.Text)
	}
	n.State = "undone"
	if r := CardReply(n, false); r.Text != "T\n\nRedo: /notice redo 42.a3f1\nOK: /notice ok 42.a3f1" {
		t.Fatalf("%q", r.Text)
	}
	n.State = "kept"
	if r := CardReply(n, false); r.Text != "T" {
		t.Fatalf("%q", r.Text)
	}
	cur := learn.Notice{ID: 42, Stamp: "a3f1", Text: "T", State: "open", Items: []learn.NoticeItem{{Kind: "archive", ID: 1}, {Kind: "archive", ID: 2}}}
	if r := CardReply(cur, false); r.Text != "T\n\nRestore all: /notice undo 42.a3f1\nRestore one: /notice undo 42.a3f1 <n>\nOK: /notice ok 42.a3f1" {
		t.Fatalf("%q", r.Text)
	}
}

func TestNoticeActionsFit(t *testing.T) {
	n := learn.Notice{ID: 999999999, Stamp: "a3f1", Text: "T", State: "open",
		Items: []learn.NoticeItem{{Kind: "memory", ID: 1}, {Kind: "memory", ID: 2}}}
	check := func(r Reply) {
		for _, row := range r.Buttons {
			if len(row) != 1 {
				t.Fatalf("row %+v", row)
			}
			if len(row[0].Data) > ActionMaxBytes {
				t.Fatalf("%q is %d bytes", row[0].Data, len(row[0].Data))
			}
		}
	}
	for _, st := range []string{"open", "undone", "kept"} {
		n.State = st
		check(CardReply(n, true))
	}
	n.Items = make([]learn.NoticeItem, 10)
	n.State = "open"
	check(pickReply(n, make([]string, 10)))
	if a := "/notice undo " + ref(n) + " 10"; len(a) > ActionMaxBytes {
		t.Fatal(a)
	}
}

func cardGateway(e *noticeEnv) *Gateway {
	return &Gateway{Deps: e.c.Deps, Log: slog.New(slog.DiscardHandler)}
}

func TestTurnCardNoUserGivesLines(t *testing.T) {
	e, n, _ := oneUserNotice(t)
	g := cardGateway(e)
	r := &recRenderer{}
	in := Inbound{Key: e.c.Key, Source: "telegram"}
	g.sendCard(context.Background(), in, r, "", agent.Notice{Text: n.Text, ID: n.ID, Stamp: n.Stamp})
	if len(r.acks) != 1 || !strings.Contains(r.acks[0], "Undo: /notice undo "+ref(n)) {
		t.Fatalf("%q", r.acks)
	}
	// A card with no row sends the plain text.
	r = &recRenderer{}
	g.sendCard(context.Background(), in, r, "u1", agent.Notice{Text: "plain"})
	if len(r.acks) != 1 || r.acks[0] != "plain" {
		t.Fatalf("%q", r.acks)
	}
}

type replyRec struct {
	recRenderer
	replies []Reply
}

func (r *replyRec) AckReply(rep Reply) { r.replies = append(r.replies, rep) }

func TestTurnCardWithUserGivesButtons(t *testing.T) {
	e, n, _ := oneUserNotice(t)
	g := cardGateway(e)
	r := &replyRec{}
	g.sendCard(context.Background(), Inbound{Key: e.c.Key, Source: "telegram"}, r, "u1", agent.Notice{Text: n.Text, ID: n.ID, Stamp: n.Stamp})
	if len(r.replies) != 1 || len(r.replies[0].Buttons) != 2 || len(r.acks) != 0 {
		t.Fatalf("%+v %q", r.replies, r.acks)
	}
}

func TestTurnCardGroupWithMember(t *testing.T) {
	e, n, _ := oneUserNotice(t)
	var private []Reply
	e.c.Deps.Learn.GroupHasMember = func(context.Context, string) bool { return true }
	e.c.Deps.Learn.SendPrivate = func(_ context.Context, _ string, r Reply) error { private = append(private, r); return nil }
	g := cardGateway(e)
	r := &replyRec{}
	g.sendCard(context.Background(), Inbound{Key: "telegram:group:9", Source: "telegram"}, r, "u1", agent.Notice{Text: n.Text, ID: n.ID, Stamp: n.Stamp})
	if len(private) != 1 || len(private[0].Buttons) != 2 {
		t.Fatalf("%+v", private)
	}
	if len(r.acks) != 1 || r.acks[0] != "Sent to your private chat." || len(r.replies) != 0 {
		t.Fatalf("%q %+v", r.acks, r.replies)
	}
	// A failed private send falls back to the topic.
	e.c.Deps.Learn.SendPrivate = func(context.Context, string, Reply) error { return fmt.Errorf("blocked") }
	r = &replyRec{}
	g.sendCard(context.Background(), Inbound{Key: "telegram:group:9", Source: "telegram"}, r, "u1", agent.Notice{Text: n.Text, ID: n.ID, Stamp: n.Stamp})
	if len(r.replies) != 1 {
		t.Fatalf("%q %+v", r.acks, r.replies)
	}
}

// A redo of a two-row spill card redoes the whole group, so the second item is done too.
func TestNoticeRedoSpillGroup(t *testing.T) {
	e := newNoticeEnv(t)
	mem, err := memory.Open(t.TempDir(), 10, 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	mem.History = e.hist
	e.mem = mem
	e.c.Deps.Learn.Memory = mem
	e.add(t, memory.TargetMemory, "abc")
	res, err := mem.ApplyContext(context.Background(), memory.Op{Action: "replace", Target: memory.TargetMemory,
		OldText: "abc", Content: strings.Repeat("z", 11), Source: "chat"})
	if err != nil || len(res.EditIDs) != 2 {
		t.Fatal(res, err)
	}
	n := e.notice(t, "💾 Memory updated (replace)",
		learn.NoticeItem{Kind: "memory", ID: res.EditIDs[0]}, learn.NoticeItem{Kind: "memory", ID: res.EditIDs[1]})
	run(t, e.c, "/notice undo "+ref(n))
	if got := mem.Entries(memory.TargetMemory); !slices.Equal(got, []string{"abc"}) {
		t.Fatal(got)
	}
	run(t, e.c, "/notice redo "+ref(n))
	if r := lastSent(t, e.sent); !strings.HasSuffix(r.Text, "\n↪ Redone.") {
		t.Fatalf("%+v", r)
	}
	if got := mem.Entries(memory.TargetMemory); len(got) != 0 {
		t.Fatal(got)
	}
}

// A held card is answered with Save or Drop. After the answer the card is kept, so a later
// /notice show gives no Drop and Save buttons.
func TestNoticeHeldAnswerKeepsCard(t *testing.T) {
	e := newMemEnv(t, true)
	h := e.addHeld(t, memory.Op{Action: "add", Target: memory.TargetMemory, Content: "VPS = ovh", Source: "chat"}, "")
	n, err := e.notes.Create(context.Background(), e.c.Key, "Held for you", []learn.NoticeItem{{Kind: "held", ID: h.ID}})
	if err != nil {
		t.Fatal(err)
	}
	run(t, e.c, "/notice drop "+ref(n))
	got, err := e.notes.Get(context.Background(), n.ID)
	if err != nil || got.State != "kept" {
		t.Fatalf("state %q, err %v", got.State, err)
	}
	run(t, e.c, "/notice show "+ref(n))
	if r := lastSent(t, e.sent); r.Buttons != nil {
		t.Fatalf("%+v", r)
	}
}
