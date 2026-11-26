package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/learn"
	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// memHist is a memory.History that lists the newest rows first and stamps the time.
type memHist struct {
	*noticeHist
	at time.Time
}

func (h *memHist) Record(ctx context.Context, e memory.Edit) (int64, error) {
	e.At = h.at
	return h.noticeHist.Record(ctx, e)
}

func (h *memHist) Recent(_ context.Context, limit int) ([]memory.Edit, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []memory.Edit
	for i := len(h.edits) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, h.edits[i])
	}
	return out, nil
}

// memEnv is the setup of a /memory test.
type memEnv struct {
	c     *CommandCtx
	sent  *[]Reply
	out   *bytes.Buffer
	mem   *memory.Store
	dir   string
	held  *learn.Held
	notes *learn.Notices
}

// newMemEnv builds the setup. interactive true uses a Telegram context with buttons.
func newMemEnv(t *testing.T, interactive bool) *memEnv {
	t.Helper()
	e := &memEnv{}
	if interactive {
		e.c, e.sent = interactiveCtx(t)
	} else {
		e.c, e.out = cmdCtxDB(t, "cli:t")
	}
	e.dir = t.TempDir()
	mem, err := memory.Open(e.dir, 2200, 1375, 1000)
	if err != nil {
		t.Fatal(err)
	}
	mem.History = &memHist{noticeHist: &noticeHist{}, at: time.Date(2026, 11, 26, 9, 0, 0, 0, time.Local)}
	e.mem = mem
	d := e.c.Deps.Agent.DB
	e.held = learn.NewHeld(d)
	e.notes = learn.NewNotices(d)
	e.c.Deps.Learn = &LearnDeps{Memory: mem, Notices: e.notes, Held: e.held}
	setNow(t, time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC))
	return e
}

// writeFile writes the entries of the memory file name.
func (e *memEnv) writeFile(t *testing.T, name string, entries ...string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.dir, name), []byte(strings.Join(entries, memory.Separator)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e *memEnv) add(t *testing.T, target, content string) int64 {
	t.Helper()
	res, err := e.mem.ApplyContext(context.Background(), memory.Op{Action: "add", Target: target, Content: content, Source: "owner"})
	if err != nil || len(res.EditIDs) != 1 {
		t.Fatal(res, err)
	}
	return res.EditIDs[0]
}

// last returns the text of the last reply.
func (e *memEnv) last(t *testing.T) Reply {
	t.Helper()
	if e.sent != nil {
		return lastSent(t, e.sent)
	}
	lines := strings.Split(strings.TrimRight(e.out.String(), "\n"), "\n")
	return Reply{Text: strings.Join(lines, "\n")}
}

func (e *memEnv) addHeld(t *testing.T, op memory.Op, matched string) learn.HeldWrite {
	t.Helper()
	h, err := e.held.Add(context.Background(), learn.HeldWrite{Op: op, MatchedEntry: matched, Source: "chat",
		SessionKey: e.c.Key, Sources: []string{"web_fetch"}, Hosts: []string{"docs.z.ai"}})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestMemoryCommandFields(t *testing.T) {
	cmd, ok := Commands()["memory"]
	if !ok || cmd.Name != "memory" || !cmd.WhileBusy || cmd.MemberOK {
		t.Fatalf("%+v", cmd)
	}
}

func TestMemoryList(t *testing.T) {
	e := newMemEnv(t, true)
	rule := "Do not send a file unless I ask.\nuntil 2026-12-31"
	e.writeFile(t, "rules.md", rule)
	e.writeFile(t, "memory.md", "Tether update procedure")
	e.writeFile(t, "user_profile.md", "Time zone: Asia/Jakarta.")
	run(t, e.c, "/memory")
	r := e.last(t)
	for _, want := range []string{"**Owner rules** (", "1. Do not send a file unless I ask. · until 2026-12-31",
		"**Memory** (", "**User profile** (", "2. Tether update procedure", "3. Time zone: Asia/Jakarta."} {
		if !strings.Contains(r.Text, want) {
			t.Fatal(want, "\n", r.Text)
		}
	}
	if strings.Index(r.Text, "**Owner rules**") > strings.Index(r.Text, "**Memory** (") {
		t.Fatal("the rules come first", r.Text)
	}
	want := Button{Text: "🗑 1", Data: "/memory delete " + memory.EntryID(rule)}
	if len(r.Buttons) == 0 || r.Buttons[0][0] != want || r.Edit {
		t.Fatalf("%+v", r)
	}
}

func TestMemoryListEmptyFile(t *testing.T) {
	e := newMemEnv(t, true)
	run(t, e.c, "/memory")
	if r := e.last(t); strings.Count(r.Text, "(empty)") != 3 {
		t.Fatal(r.Text)
	}
}

func TestMemoryListExpired(t *testing.T) {
	e := newMemEnv(t, true)
	e.writeFile(t, "memory.md", "Plan A\nuntil 2026-11-20")
	run(t, e.c, "/memory")
	if r := e.last(t); !strings.Contains(r.Text, "1. Plan A · until 2026-11-20 (expired)") {
		t.Fatal(r.Text)
	}
}

func TestMemoryListPages(t *testing.T) {
	e := newMemEnv(t, true)
	var entries []string
	for i := 1; i <= 10; i++ {
		entries = append(entries, fmt.Sprintf("Fact number %d", i))
	}
	e.writeFile(t, "memory.md", entries...)
	run(t, e.c, "/memory")
	r := e.last(t)
	n := 0
	for _, row := range r.Buttons[:len(r.Buttons)-1] {
		if len(row) != 4 {
			t.Fatalf("four buttons on a row: %+v", r.Buttons)
		}
		n += len(row)
	}
	if n != 8 {
		t.Fatalf("8 delete buttons: %+v", r.Buttons)
	}
	if lastRow := r.Buttons[len(r.Buttons)-1]; len(lastRow) != 1 || lastRow[0] != (Button{Text: "More ›", Data: "/memory page 2"}) {
		t.Fatalf("%+v", r.Buttons)
	}
	run(t, e.c, "/memory page 2")
	r = e.last(t)
	if !r.Edit || len(r.Buttons) != 1 || len(r.Buttons[0]) != 2 ||
		r.Buttons[0][0].Text != "🗑 9" || r.Buttons[0][1].Text != "🗑 10" {
		t.Fatalf("%+v", r)
	}
}

func TestMemoryListCLI(t *testing.T) {
	e := newMemEnv(t, false)
	entry := "Time zone: Asia/Jakarta."
	e.writeFile(t, "user_profile.md", entry)
	run(t, e.c, "/memory")
	id := memory.EntryID(entry)
	got := e.last(t).Text
	if !strings.Contains(got, "1. "+entry+" · "+id+"\n") || !strings.HasSuffix(got, "\n\nDelete: /memory delete <id>") {
		t.Fatal(got)
	}
}

func TestMemoryLog(t *testing.T) {
	e := newMemEnv(t, true)
	id := e.add(t, "user", "Time zone: Asia/Jakarta.")
	run(t, e.c, "/memory log")
	r := e.last(t)
	if !strings.Contains(r.Text, "Nov 26 09:00 · owner · add · user · Time zone: Asia/Jakarta.") {
		t.Fatal(r.Text)
	}
	ed, _ := e.mem.History.Get(context.Background(), id)
	want := Button{Text: "↩ Undo 1", Data: fmt.Sprintf("/memory undo %d.%s", id, ed.Stamp)}
	if len(r.Buttons) != 1 || r.Buttons[0][0] != want {
		t.Fatalf("%+v", r.Buttons)
	}
}

func TestMemoryLogCLIAndEmpty(t *testing.T) {
	e := newMemEnv(t, false)
	run(t, e.c, "/memory log")
	if got := e.last(t).Text; got != "No memory change is recorded yet." {
		t.Fatal(got)
	}
	e.out.Reset()
	id := e.add(t, "user", "Time zone: Asia/Jakarta.")
	ed, _ := e.mem.History.Get(context.Background(), id)
	run(t, e.c, "/memory log")
	if got := e.out.String(); !strings.Contains(got, fmt.Sprintf("Undo 1: /memory undo %d.%s", id, ed.Stamp)) {
		t.Fatal(got)
	}
	e.mem.History = nil
	e.out.Reset()
	run(t, e.c, "/memory log")
	if got := e.last(t).Text; got != "The memory history is not available." {
		t.Fatal(got)
	}
}

func TestMemoryDeleteAsks(t *testing.T) {
	e := newMemEnv(t, true)
	entry := "Time zone: Asia/Jakarta."
	e.add(t, "user", entry)
	id := memory.EntryID(entry)
	run(t, e.c, "/memory delete "+id)
	r := e.last(t)
	if !strings.HasPrefix(r.Text, "🗑 **Delete this entry?**\n") {
		t.Fatal(r.Text)
	}
	want := [][]Button{{{Text: "Cancel", Data: "cancel"}}, {{Text: "🗑 Delete", Data: "/memory delete " + id + " confirm"}}}
	if fmt.Sprint(r.Buttons) != fmt.Sprint(want) {
		t.Fatalf("%+v", r.Buttons)
	}
	if got := e.mem.Entries("user"); len(got) != 1 {
		t.Fatal("the entry stays", got)
	}
}

func TestMemoryDeleteConfirm(t *testing.T) {
	e := newMemEnv(t, true)
	entry := "Time zone: Asia/Jakarta."
	e.add(t, "user", entry)
	run(t, e.c, "/memory delete "+memory.EntryID(entry)+" confirm")
	if got := e.mem.Entries("user"); len(got) != 0 {
		t.Fatal(got)
	}
	rows, _ := e.mem.History.Recent(context.Background(), 10)
	if len(rows) != 2 || rows[0].Action != "remove" || rows[0].Source != "owner" {
		t.Fatalf("%+v", rows)
	}
	r := e.last(t)
	if len(r.Buttons) == 0 || r.Buttons[0][0].Text != "↩ Undo" || !strings.HasPrefix(r.Buttons[0][0].Data, "/notice undo ") {
		t.Fatalf("%+v", r)
	}
}

func TestMemoryDeleteCLIConfirm(t *testing.T) {
	e := newMemEnv(t, false)
	entry := "Time zone: Asia/Jakarta."
	e.add(t, "user", entry)
	id := memory.EntryID(entry)
	e.c.Confirm = func(string) bool { return false }
	run(t, e.c, "/memory delete "+id)
	if got := e.out.String(); !strings.Contains(got, "Not deleted.") || len(e.mem.Entries("user")) != 1 {
		t.Fatal(got)
	}
	e.out.Reset()
	e.c.Confirm = nil
	run(t, e.c, "/memory delete "+id)
	if got := e.out.String(); !strings.Contains(got, "To delete it, send: /memory delete "+id+" confirm") {
		t.Fatal(got)
	}
	e.out.Reset()
	e.c.Confirm = func(string) bool { return true }
	run(t, e.c, "/memory delete "+id)
	if len(e.mem.Entries("user")) != 0 {
		t.Fatal("a yes deletes the entry")
	}
}

func TestMemoryDeleteUnknown(t *testing.T) {
	e := newMemEnv(t, true)
	run(t, e.c, "/memory delete m-00000000 confirm")
	if got := e.last(t).Text; got != "No entry has the ID m-00000000. Send /memory to see the IDs." {
		t.Fatal(got)
	}
}

func TestMemoryUndoRedo(t *testing.T) {
	e := newMemEnv(t, true)
	id := e.add(t, "user", "Time zone: Asia/Jakarta.")
	ed, _ := e.mem.History.Get(context.Background(), id)
	ref := fmt.Sprintf("%d.%s", id, ed.Stamp)
	run(t, e.c, "/memory undo "+ref)
	if got := e.last(t).Text; got != "↩ Undone." || len(e.mem.Entries("user")) != 0 {
		t.Fatal(got)
	}
	run(t, e.c, "/memory undo "+ref)
	if got := e.last(t).Text; got != "This edit is undone already." {
		t.Fatal(got)
	}
	run(t, e.c, "/memory redo "+ref)
	if got := e.last(t).Text; got != "↪ Redone." || len(e.mem.Entries("user")) != 1 {
		t.Fatal(got)
	}
	run(t, e.c, fmt.Sprintf("/memory undo %d.ffff", id))
	if got := e.last(t).Text; got != "This button is too old." {
		t.Fatal(got)
	}
	run(t, e.c, "/memory undo nonsense")
	if got := e.last(t).Text; !strings.HasPrefix(got, "Use /memory, /memory log") {
		t.Fatal(got)
	}
}

func TestMemoryHeld(t *testing.T) {
	e := newMemEnv(t, true)
	h := e.addHeld(t, memory.Op{Action: "add", Target: "memory", Source: "chat",
		Content: "Z.ai coding plan base URL: https://api.z.ai/api/coding/paas/v4"}, "")
	run(t, e.c, "/memory held")
	r := e.last(t)
	wantText := "💾 Memory write held: this turn read outside content (web_fetch docs.z.ai).\n" +
		"add to memory: `Z.ai coding plan base URL: https://api.z.ai/api/coding/paas/v4`"
	if r.Text != wantText {
		t.Fatal(r.Text)
	}
	ref := fmt.Sprintf("%d.%s", h.ID, h.Stamp)
	want := [][]Button{{{Text: "❌ Drop", Data: "/memory drop " + ref}}, {{Text: "✅ Save", Data: "/memory save " + ref}}}
	if fmt.Sprint(r.Buttons) != fmt.Sprint(want) {
		t.Fatalf("%+v", r.Buttons)
	}
}

func TestMemoryHeldCLIAndNone(t *testing.T) {
	e := newMemEnv(t, false)
	run(t, e.c, "/memory held")
	if got := e.last(t).Text; got != "No memory write is held." {
		t.Fatal(got)
	}
	e.out.Reset()
	h := e.addHeld(t, memory.Op{Action: "add", Target: "memory", Content: "x", Source: "chat"}, "")
	run(t, e.c, "/memory held")
	ref := fmt.Sprintf("%d.%s", h.ID, h.Stamp)
	got := e.out.String()
	if !strings.Contains(got, "Save: /memory save "+ref) || !strings.Contains(got, "Drop: /memory drop "+ref) {
		t.Fatal(got)
	}
}

func TestMemorySaveHeld(t *testing.T) {
	e := newMemEnv(t, true)
	h := e.addHeld(t, memory.Op{Action: "add", Target: "memory", Content: "Base URL is v4", Source: "chat"}, "")
	run(t, e.c, fmt.Sprintf("/memory save %d.%s", h.ID, h.Stamp))
	if got := e.mem.Entries("memory"); !slices.Equal(got, []string{"Base URL is v4"}) {
		t.Fatal(got)
	}
	got, _ := e.held.Get(context.Background(), h.ID)
	if got.State != "saved" {
		t.Fatal(got.State)
	}
	var found bool
	for _, r := range *e.sent {
		if r.Edit && strings.Contains(r.Text, "✅ Saved.") {
			found = true
		}
	}
	if !found {
		t.Fatalf("%+v", *e.sent)
	}
	if r := e.last(t); !strings.Contains(r.Text, "Memory updated") {
		t.Fatalf("a card of the new edit follows: %+v", r)
	}
	// A second answer is refused.
	run(t, e.c, fmt.Sprintf("/memory drop %d.%s", h.ID, h.Stamp))
	if got := e.last(t).Text; got != "This held write is answered already." {
		t.Fatal(got)
	}
}

func TestMemorySaveWrongStamp(t *testing.T) {
	e := newMemEnv(t, true)
	h := e.addHeld(t, memory.Op{Action: "add", Target: "memory", Content: "x", Source: "chat"}, "")
	run(t, e.c, fmt.Sprintf("/memory save %d.ffff", h.ID))
	if got := e.last(t).Text; got != "This button is too old." {
		t.Fatal(got)
	}
	run(t, e.c, "/memory save 999999.ffff")
	if got := e.last(t).Text; got != "This button is too old." {
		t.Fatal(got)
	}
}

func TestMemorySaveChangedEntry(t *testing.T) {
	e := newMemEnv(t, true)
	h := e.addHeld(t, memory.Op{Action: "replace", Target: "memory", OldText: "Old fact", Content: "New fact", Source: "chat"}, "Old fact")
	run(t, e.c, fmt.Sprintf("/memory save %d.%s", h.ID, h.Stamp))
	if got := e.last(t).Text; !strings.Contains(got, "The entry changed after the hold. Nothing was saved.") {
		t.Fatal(got)
	}
	got, _ := e.held.Get(context.Background(), h.ID)
	if got.State != "held" {
		t.Fatal(got.State)
	}
}

func TestMemorySaveThreat(t *testing.T) {
	e := newMemEnv(t, true)
	e.mem.Threat = func(text string) string {
		if strings.Contains(text, "evil") {
			return "blocked by the scan"
		}
		return ""
	}
	h := e.addHeld(t, memory.Op{Action: "add", Target: "memory", Content: "evil text", Source: "chat"}, "")
	run(t, e.c, fmt.Sprintf("/memory save %d.%s", h.ID, h.Stamp))
	if got := e.last(t).Text; !strings.Contains(got, "blocked by the scan") {
		t.Fatal(got)
	}
	got, _ := e.held.Get(context.Background(), h.ID)
	if got.State != "held" || len(e.mem.Entries("memory")) != 0 {
		t.Fatal(got.State)
	}
}

func TestMemoryDropHeld(t *testing.T) {
	e := newMemEnv(t, true)
	h := e.addHeld(t, memory.Op{Action: "add", Target: "memory", Content: "Base URL is v4", Source: "chat"}, "")
	run(t, e.c, fmt.Sprintf("/memory drop %d.%s", h.ID, h.Stamp))
	r := e.last(t)
	if !r.Edit || !strings.Contains(r.Text, "❌ Dropped. Nothing was saved.") {
		t.Fatalf("%+v", r)
	}
	got, _ := e.held.Get(context.Background(), h.ID)
	if got.State != "dropped" || len(e.mem.Entries("memory")) != 0 {
		t.Fatal(got.State)
	}
}

func TestMemoryNoticeHeldAnswerIsSet(t *testing.T) {
	if noticeHeldAnswer == nil {
		t.Fatal("the memory commands set noticeHeldAnswer")
	}
}

func TestMemoryTidy(t *testing.T) {
	e := newMemEnv(t, true)
	run(t, e.c, "/memory tidy")
	if got := e.last(t).Text; got != "Memory tidy is not available here." {
		t.Fatal(got)
	}
	var card func(agent.Notice)
	var tidyErr error
	e.c.Deps.Learn.Tidy = func(_ context.Context, _ *sessions.Session, c func(agent.Notice)) error {
		card = c
		return tidyErr
	}
	run(t, e.c, "/memory tidy")
	if got := e.last(t).Text; got != "Start a session first." {
		t.Fatal(got)
	}
	run(t, e.c, "/new")
	e.c.Session, _ = e.c.Deps.Store.Active(context.Background(), e.c.Key)
	run(t, e.c, "/memory tidy")
	if got := e.last(t).Text; got != "Memory tidy started. A card follows." {
		t.Fatal(got)
	}
	card(agent.Notice{Text: "Memory tidy: nothing to change."})
	if got := e.last(t).Text; got != "Memory tidy: nothing to change." {
		t.Fatal(got)
	}
	tidyErr = agent.ErrTidyRunning
	run(t, e.c, "/memory tidy")
	if got := e.last(t).Text; got != "A memory tidy runs already." {
		t.Fatal(got)
	}
}

func TestMemoryGroupWithMember(t *testing.T) {
	e := newMemEnv(t, true)
	e.c.Key = "telegram:group:-100:5"
	e.c.Source = "telegram"
	e.writeFile(t, "user_profile.md", "Time zone: Asia/Jakarta.")
	var private []Reply
	e.c.Deps.Learn.GroupHasMember = func(context.Context, string) bool { return true }
	e.c.Deps.Learn.SendPrivate = func(_ context.Context, _ string, r Reply) error {
		private = append(private, r)
		return nil
	}
	run(t, e.c, "/memory")
	if len(private) != 1 || !strings.Contains(private[0].Text, "Time zone") {
		t.Fatalf("%+v", private)
	}
	if got := e.last(t).Text; got != "Sent to your private chat." {
		t.Fatal(got)
	}
	// A send error shows the list in the topic.
	e.c.Deps.Learn.SendPrivate = func(context.Context, string, Reply) error { return errors.New("boom") }
	run(t, e.c, "/memory")
	if got := e.last(t).Text; !strings.Contains(got, "Time zone") {
		t.Fatal(got)
	}
}

func TestMemoryActionsFit(t *testing.T) {
	for _, a := range []string{
		"/memory delete m-3fa29c1e confirm",
		"/memory redo 999999999.a3f1",
		"/memory page 99",
		"/memory save 999999999.a3f1",
	} {
		if len(a) > ActionMaxBytes {
			t.Fatalf("%q has %d bytes", a, len(a))
		}
	}
}

func TestMemoryNotAvailable(t *testing.T) {
	c, sent := interactiveCtx(t)
	run(t, c, "/memory")
	if got := lastSent(t, sent).Text; got != "Memory commands are not available here." {
		t.Fatal(got)
	}
}

func TestMemoryUsage(t *testing.T) {
	e := newMemEnv(t, true)
	run(t, e.c, "/memory nonsense")
	want := "Use /memory, /memory log, /memory held, /memory delete <id>, /memory undo <id>.<stamp>, " +
		"/memory redo <id>.<stamp>, /memory save <id>.<stamp>, /memory drop <id>.<stamp>, or /memory tidy."
	if got := e.last(t).Text; got != want {
		t.Fatal(got)
	}
}

func TestCLICardText(t *testing.T) {
	e := newMemEnv(t, false)
	c := &CLI{Deps: e.c.Deps, Key: "cli:t"}
	ctx := context.Background()
	if got := c.cardText(ctx, agent.Notice{Text: "plain"}); got != "plain" {
		t.Fatal(got)
	}
	id := e.add(t, "user", "Time zone: Asia/Jakarta.")
	n, err := e.notes.Create(ctx, "cli:t", "💾 User profile updated (add)", []learn.NoticeItem{{Kind: "memory", ID: id}})
	if err != nil {
		t.Fatal(err)
	}
	got := c.cardText(ctx, agent.Notice{Text: n.Text, ID: n.ID, Stamp: n.Stamp})
	if !strings.Contains(got, fmt.Sprintf("Undo: /notice undo %d.%s", n.ID, n.Stamp)) {
		t.Fatal(got)
	}
	if got := c.cardText(ctx, agent.Notice{Text: "gone", ID: 999}); got != "gone" {
		t.Fatal(got)
	}
	c.Deps = &Deps{}
	if got := c.cardText(ctx, agent.Notice{Text: "x", ID: n.ID}); got != "x" {
		t.Fatal(got)
	}
}

// The card of a tidy that started in a group with a member goes to the private chat, like the
// card of a turn.
func TestMemoryTidyCardInGroupGoesPrivate(t *testing.T) {
	e := newMemEnv(t, true)
	e.c.Key = "telegram:group:-100:5"
	e.c.Source = "telegram"
	run(t, e.c, "/new")
	e.c.Session, _ = e.c.Deps.Store.Active(context.Background(), e.c.Key)
	var card func(agent.Notice)
	e.c.Deps.Learn.Tidy = func(_ context.Context, _ *sessions.Session, c func(agent.Notice)) error {
		card = c
		return nil
	}
	var private []Reply
	e.c.Deps.Learn.GroupHasMember = func(context.Context, string) bool { return true }
	e.c.Deps.Learn.SendPrivate = func(_ context.Context, _ string, r Reply) error {
		private = append(private, r)
		return nil
	}
	run(t, e.c, "/memory tidy")
	card(agent.Notice{Text: "Memory tidy: Was: `old fact`"})
	if len(private) != 1 || !strings.Contains(private[0].Text, "old fact") {
		t.Fatalf("%+v", private)
	}
	if got := e.last(t).Text; got != "Sent to your private chat." {
		t.Fatal(got)
	}
}

// After an Undo and a Redo, the change is in the file again, so the log offers its Undo again.
func TestMemoryLogUndoAfterRedo(t *testing.T) {
	e := newMemEnv(t, true)
	id := e.add(t, "user", "Time zone: Asia/Jakarta.")
	ed, _ := e.mem.History.Get(context.Background(), id)
	if _, err := e.mem.Undo(context.Background(), id, ed.Stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := e.mem.Redo(context.Background(), id, ed.Stamp); err != nil {
		t.Fatal(err)
	}
	run(t, e.c, "/memory log")
	want := fmt.Sprintf("/memory undo %d.%s", id, ed.Stamp)
	for _, row := range e.last(t).Buttons {
		if row[0].Data == want {
			return
		}
	}
	t.Fatalf("no Undo for the redone change: %+v", e.last(t).Buttons)
}
