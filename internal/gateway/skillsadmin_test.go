package gateway

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/skills"
)

func TestCuratorCardText(t *testing.T) {
	got := CuratorCardText(skills.CuratorReport{
		Archived: []skills.Flag{{Name: "graphkb-lightrag"}, {Name: "test-probe-not-real"}},
		Stale:    []string{"a", "b", "c", "d", "e", "f"}, Kept: 2, SkippedBundled: 3, SkippedHub: 9,
		StaleAfterDays: 14, ArchiveAfterDays: 30})
	want := "🗂 Skill curator\nArchived (no read in 30 days):\n• " + EscapeMarkdown("graphkb-lightrag") +
		"\n• " + EscapeMarkdown("test-probe-not-real") +
		"\nStale (no read in 14 days): 6 skills. /skills stale\nKept: 2 · Skipped: 3 bundled, 9 from the hub"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestCuratorCardTextOneStaleNoArchive(t *testing.T) {
	got := CuratorCardText(skills.CuratorReport{Stale: []string{"a"}, StaleAfterDays: 14, ArchiveAfterDays: 30})
	if !strings.Contains(got, "Stale (no read in 14 days): 1 skill. /skills stale") || strings.Contains(got, "Archived") {
		t.Fatal(got)
	}
	none := CuratorCardText(skills.CuratorReport{})
	if strings.Contains(none, "Stale") || !strings.HasSuffix(none, "Kept: 0 · Skipped: 0 bundled, 0 from the hub") {
		t.Fatal(none)
	}
}

func TestSkillsCommandFields(t *testing.T) {
	cmd, ok := Commands()["skills"]
	if !ok || !cmd.WhileBusy || cmd.MemberOK {
		t.Fatalf("%+v", cmd)
	}
}

// saStore is a skills.CuratorStore over maps.
type saStore struct {
	flags    map[string]skills.Flag
	activity map[string]time.Time
	next     int64
}

func (s *saStore) Flags(context.Context) (map[string]skills.Flag, error) {
	return maps.Clone(s.flags), nil
}

func (s *saStore) SaveFlag(_ context.Context, f skills.Flag) (skills.Flag, error) {
	if f.ID == 0 {
		s.next++
		f.ID = s.next
	}
	s.flags[f.Name] = f
	return f, nil
}

func (s *saStore) LastActivity(_ context.Context, name string) (time.Time, bool, error) {
	t, ok := s.activity[name]
	return t, ok, nil
}
func (s *saStore) Backfill(context.Context) error                   { return nil }
func (s *saStore) LastRun(context.Context) (time.Time, bool, error) { return time.Time{}, false, nil }
func (s *saStore) SetLastRun(context.Context, time.Time) error      { return nil }

// saCopies is a skills.CopyRecorder over a slice.
type saCopies struct{ rows []skills.Copy }

func (r *saCopies) AddCopy(c skills.Copy) (skills.Copy, error) {
	c.ID = int64(len(r.rows) + 1)
	c.Stamp = "ab12"
	if c.Action != "create" {
		c.Path = fmt.Sprintf(".history/%d", c.ID)
	}
	r.rows = append(r.rows, c)
	return c, nil
}
func (r *saCopies) FinishCopy(id int64, h string) error { r.rows[id-1].HashAfter = h; return nil }
func (r *saCopies) GetCopy(id int64) (skills.Copy, error) {
	if id < 1 || int(id) > len(r.rows) {
		return skills.Copy{}, fmt.Errorf("no copy %d", id)
	}
	return r.rows[id-1], nil
}
func (r *saCopies) ListCopies(string, int) ([]skills.Copy, error)  { return r.rows, nil }
func (r *saCopies) MarkCopyUndone(id, undoID int64) error          { r.rows[id-1].UndoneBy = undoID; return nil }
func (r *saCopies) PruneCopies(string, int) ([]skills.Copy, error) { return nil, nil }

var saNow = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

type saEnv struct {
	c       *CommandCtx
	sent    *[]Reply
	store   *saStore
	copies  *saCopies
	dir     string
	refresh int
}

const saSkillMD = "---\nname: demo\ndescription: Demo. Use when testing.\n---\n# Steps\n1. build\n"

func saSkill(t *testing.T, dir, rel, name string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: test skill " + name + "\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(p, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newSAEnv(t *testing.T, interactive bool) *saEnv {
	t.Helper()
	e := &saEnv{store: &saStore{flags: map[string]skills.Flag{}, activity: map[string]time.Time{}}, copies: &saCopies{}}
	e.dir = filepath.Join(t.TempDir(), "skills")
	if interactive {
		e.c, e.sent = interactiveCtx(t)
	} else {
		c, out := cmdCtx(t, "cli:t")
		var sent []Reply
		c.Reply = func(s string) { sent = append(sent, Reply{Text: s}); _ = out }
		e.c, e.sent = c, &sent
	}
	e.c.Deps.Learn = &LearnDeps{
		Curator:     &skills.Curator{Dir: e.dir, Store: e.store, Now: func() time.Time { return saNow }},
		Skills:      &skills.Manager{Dir: e.dir, Copies: e.copies},
		Copies:      e.copies,
		RefreshMenu: func(context.Context) { e.refresh++ },
	}
	return e
}

func (e *saEnv) last(t *testing.T) Reply {
	t.Helper()
	return lastSent(t, e.sent)
}

func TestSkillsStale(t *testing.T) {
	e := newSAEnv(t, false)
	saSkill(t, e.dir, "demo", "demo")
	e.store.flags["demo"] = skills.Flag{ID: 1, Name: "demo", RelDir: "demo", FirstSeen: saNow.Add(-60 * 24 * time.Hour)}
	e.store.activity["demo"] = saNow.Add(-20 * 24 * time.Hour)
	run(t, e.c, "/skills")
	got := e.last(t).Text
	if !strings.HasPrefix(got, "🗂 **Skills that the curator checks**") || !strings.Contains(got, "demo · stale · last read Nov 6") {
		t.Fatal(got)
	}
	run(t, e.c, "/skills stale")
	if e.last(t).Text != got {
		t.Fatal(e.last(t).Text)
	}
}

func TestSkillsStaleNone(t *testing.T) {
	e := newSAEnv(t, false)
	run(t, e.c, "/skills stale")
	if e.last(t).Text != "No skill is a curator candidate." {
		t.Fatal(e.last(t).Text)
	}
}

func TestSkillsKeepUnkeep(t *testing.T) {
	e := newSAEnv(t, false)
	saSkill(t, e.dir, "demo", "demo")
	run(t, e.c, "/skills keep demo")
	if e.last(t).Text != "✓ Kept demo. The curator never archives it." || !e.store.flags["demo"].Kept {
		t.Fatal(e.last(t).Text, e.store.flags)
	}
	run(t, e.c, "/skills unkeep demo")
	if e.last(t).Text != "demo is not kept now. The curator can archive it." || e.store.flags["demo"].Kept {
		t.Fatal(e.last(t).Text, e.store.flags)
	}
}

func saArchive(t *testing.T, e *saEnv, name string) {
	t.Helper()
	saSkill(t, e.dir, name, name)
	e.store.flags[name] = skills.Flag{ID: 7, Name: name, RelDir: name, FirstSeen: saNow.Add(-60 * 24 * time.Hour)}
	e.store.activity[name] = saNow.Add(-40 * 24 * time.Hour)
	rep, _, err := e.c.Deps.Learn.Curator.Run(context.Background(), true, time.Time{})
	if err != nil || len(rep.Archived) != 1 {
		t.Fatalf("err=%v rep=%+v", err, rep)
	}
}

func TestSkillsArchivedAndRestore(t *testing.T) {
	e := newSAEnv(t, true)
	saArchive(t, e, "old-one")
	run(t, e.c, "/skills archived")
	r := e.last(t)
	if !strings.HasPrefix(r.Text, "🗂 **Archived skills**") || !strings.Contains(r.Text, "old-one · archived Nov 26 · old-one") {
		t.Fatal(r.Text)
	}
	if len(r.Buttons) != 1 || r.Buttons[0][0] != (Button{Text: "↩ Restore old-one", Data: "/skills restore old-one"}) {
		t.Fatalf("%+v", r.Buttons)
	}
	run(t, e.c, "/skills restore old-one")
	if e.last(t).Text != "↩ Restored old-one. It loads in new sessions." || e.refresh != 1 {
		t.Fatal(e.last(t).Text, e.refresh)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "old-one", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	run(t, e.c, "/skills archived")
	if e.last(t).Text != "No skill is archived." {
		t.Fatal(e.last(t).Text)
	}
}

func TestSkillsArchivedCLI(t *testing.T) {
	e := newSAEnv(t, false)
	saArchive(t, e, "old-one")
	run(t, e.c, "/skills archived")
	if !strings.Contains(e.last(t).Text, "Restore: /skills restore old-one") {
		t.Fatal(e.last(t).Text)
	}
}

func saLogEnv(t *testing.T, interactive bool) *saEnv {
	t.Helper()
	e := newSAEnv(t, interactive)
	if _, err := e.c.Deps.Learn.Skills.Create(skills.Origin{Source: "chat"}, "demo", saSkillMD); err != nil {
		t.Fatal(err)
	}
	if _, err := e.c.Deps.Learn.Skills.Patch(skills.Origin{Source: "chat"}, "demo", "1. build", "1. make"); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestSkillsLogAndUndo(t *testing.T) {
	e := saLogEnv(t, true)
	run(t, e.c, "/skills log")
	r := e.last(t)
	if !strings.Contains(r.Text, "· chat · patch · demo · copy 2") {
		t.Fatal(r.Text)
	}
	var found bool
	for _, row := range r.Buttons {
		if row[0] == (Button{Text: "↩ Undo 2", Data: "/skills undo 2.ab12"}) {
			found = true
		}
	}
	if !found {
		t.Fatalf("%+v", r.Buttons)
	}
	run(t, e.c, "/skills undo 2.ab12")
	if e.last(t).Text != "↩ Undone. The skill before the change is back." || e.refresh != 1 {
		t.Fatal(e.last(t).Text, e.refresh)
	}
	b, _ := os.ReadFile(filepath.Join(e.dir, "demo", "SKILL.md"))
	if string(b) != saSkillMD {
		t.Fatalf("%q", b)
	}
	run(t, e.c, "/skills undo 2.ab12")
	if e.last(t).Text != "This change is undone already." {
		t.Fatal(e.last(t).Text)
	}
}

func TestSkillsLogCLI(t *testing.T) {
	e := saLogEnv(t, false)
	run(t, e.c, "/skills log")
	if !strings.Contains(e.last(t).Text, "Undo 1: /skills undo 2.ab12") && !strings.Contains(e.last(t).Text, "/skills undo 2.ab12") {
		t.Fatal(e.last(t).Text)
	}
}

func TestSkillsUndoBadStamp(t *testing.T) {
	e := saLogEnv(t, false)
	path := filepath.Join(e.dir, "demo", "SKILL.md")
	before, _ := os.ReadFile(path)
	run(t, e.c, "/skills undo 2.0000")
	if e.last(t).Text != "This button is too old." {
		t.Fatal(e.last(t).Text)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("the skill changed")
	}
}

func TestSkillsUndoChanged(t *testing.T) {
	e := saLogEnv(t, false)
	if _, err := e.c.Deps.Learn.Skills.Patch(skills.Origin{Source: "chat"}, "demo", "1. make", "1. ship"); err != nil {
		t.Fatal(err)
	}
	run(t, e.c, "/skills undo 2.ab12")
	if e.last(t).Text != "The skill changed after this copy. Nothing was changed. Use /skills log." {
		t.Fatal(e.last(t).Text)
	}
}

func TestSkillsNotAvailable(t *testing.T) {
	c, out := cmdCtx(t, "cli:t")
	run(t, c, "/skills")
	if !strings.Contains(out.String(), "Skill commands are not available here.") {
		t.Fatal(out)
	}
	c.Deps.Learn = &LearnDeps{}
	out.Reset()
	run(t, c, "/skills stale")
	if !strings.Contains(out.String(), "Skill commands are not available here.") {
		t.Fatal(out)
	}
}

func TestSkillsUsage(t *testing.T) {
	e := newSAEnv(t, false)
	for _, in := range []string{"/skills bogus", "/skills keep", "/skills undo nope", "/skills stale extra"} {
		run(t, e.c, in)
		if !strings.HasPrefix(e.last(t).Text, "Use /skills stale, /skills archived") {
			t.Fatal(in, e.last(t).Text)
		}
	}
}

func TestSkillsActionsFit(t *testing.T) {
	if n := len("/skills undo 999999999.a3f1"); n > ActionMaxBytes {
		t.Fatalf("undo action is %d bytes", n)
	}
	e := newSAEnv(t, true)
	long := strings.Repeat("x", 40)
	saArchive(t, e, long)
	run(t, e.c, "/skills archived")
	r := e.last(t)
	if len(r.Buttons) != 0 || !strings.Contains(r.Text, EscapeMarkdown(long)) {
		t.Fatalf("%+v", r)
	}
}

// After an Undo and a Redo, the change is in the skill again, so the log offers its Undo again.
func TestSkillsLogUndoAfterRedo(t *testing.T) {
	e := saLogEnv(t, true)
	m := e.c.Deps.Learn.Skills
	if _, err := m.UndoCopy(skills.Origin{Source: "owner"}, 2, "ab12"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RedoCopy(skills.Origin{Source: "owner"}, 2, "ab12"); err != nil {
		t.Fatal(err)
	}
	run(t, e.c, "/skills log")
	for _, row := range e.last(t).Buttons {
		if row[0].Data == "/skills undo 2.ab12" {
			return
		}
	}
	t.Fatalf("no Undo for the redone change: %+v", e.last(t).Buttons)
}
