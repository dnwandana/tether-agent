package skills

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
)

type memCuratorStore struct {
	flags     map[string]Flag
	activity  map[string]time.Time
	lastRun   *time.Time
	backfills int
	nextID    int64
}

func newMemCuratorStore() *memCuratorStore {
	return &memCuratorStore{flags: map[string]Flag{}, activity: map[string]time.Time{}, nextID: 1}
}

func (m *memCuratorStore) Flags(context.Context) (map[string]Flag, error) {
	return maps.Clone(m.flags), nil
}

func (m *memCuratorStore) SaveFlag(_ context.Context, f Flag) (Flag, error) {
	if f.ID == 0 {
		f.ID = m.nextID
		m.nextID++
	}
	m.flags[f.Name] = f
	return f, nil
}

func (m *memCuratorStore) LastActivity(_ context.Context, name string) (time.Time, bool, error) {
	t, ok := m.activity[name]
	return t, ok, nil
}

func (m *memCuratorStore) Backfill(context.Context) error { m.backfills++; return nil }

func (m *memCuratorStore) LastRun(context.Context) (time.Time, bool, error) {
	if m.lastRun == nil {
		return time.Time{}, false, nil
	}
	return *m.lastRun, true, nil
}

func (m *memCuratorStore) SetLastRun(_ context.Context, t time.Time) error {
	m.lastRun = &t
	return nil
}

var curatorNow = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func curatorSkill(t *testing.T, dir, rel, name string) {
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

func curatorSetup(t *testing.T) (*Curator, *memCuratorStore, string) {
	t.Helper()
	dir := t.TempDir()
	st := newMemCuratorStore()
	last := curatorNow.Add(-8 * day)
	st.lastRun = &last
	c := &Curator{Dir: dir, Store: st, Cfg: config.CuratorConfig{Enabled: true}, Now: func() time.Time { return curatorNow }}
	return c, st, dir
}

func curatorFlag(st *memCuratorStore, name, rel string, id int64, seen time.Duration) {
	st.flags[name] = Flag{ID: id, Name: name, RelDir: rel, FirstSeen: curatorNow.Add(-seen)}
	if id >= st.nextID {
		st.nextID = id + 1
	}
}

func curatorExists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestCuratorFirstRunOnlySetsClock(t *testing.T) {
	c, st, dir := curatorSetup(t)
	st.lastRun = nil
	curatorSkill(t, dir, "old-one", "old-one")
	rep, ran, err := c.Run(context.Background(), false, time.Time{})
	if ran || err != nil || len(rep.Archived) != 0 {
		t.Fatalf("ran=%v err=%v rep=%+v", ran, err, rep)
	}
	if !curatorExists(filepath.Join(dir, "old-one", "SKILL.md")) {
		t.Fatal("folder moved")
	}
	if st.backfills != 1 || st.lastRun == nil || !st.lastRun.Equal(curatorNow) {
		t.Fatalf("backfills=%d lastRun=%v", st.backfills, st.lastRun)
	}
	if !st.flags["old-one"].FirstSeen.Equal(curatorNow) {
		t.Fatal("first seen not set")
	}
}

func curatorArchiveOne(t *testing.T) (*Curator, *memCuratorStore, string) {
	c, st, dir := curatorSetup(t)
	curatorSkill(t, dir, "research/graphkb-lightrag", "graphkb-lightrag")
	curatorFlag(st, "graphkb-lightrag", "research/graphkb-lightrag", 7, 40*day)
	st.activity["graphkb-lightrag"] = curatorNow.Add(-31 * day)
	rep, ran, err := c.Run(context.Background(), false, curatorNow.Add(-3*time.Hour))
	if !ran || err != nil || len(rep.Archived) != 1 || rep.Archived[0].Name != "graphkb-lightrag" {
		t.Fatalf("ran=%v err=%v rep=%+v", ran, err, rep)
	}
	return c, st, dir
}

func TestCuratorArchivesAfter30Days(t *testing.T) {
	_, st, dir := curatorArchiveOne(t)
	dest := filepath.Join(dir, ".archive", "research__graphkb-lightrag-7")
	if !curatorExists(filepath.Join(dest, "SKILL.md")) || curatorExists(filepath.Join(dir, "research", "graphkb-lightrag")) {
		t.Fatal("folder not moved")
	}
	f := st.flags["graphkb-lightrag"]
	if f.ArchivedPath != dest || f.RelDir != "research/graphkb-lightrag" || !f.ArchivedAt.Equal(curatorNow) {
		t.Fatalf("flag %+v", f)
	}
}

func TestCuratorStaleNotArchived(t *testing.T) {
	c, st, dir := curatorSetup(t)
	curatorSkill(t, dir, "a", "a")
	curatorFlag(st, "a", "a", 1, 40*day)
	st.activity["a"] = curatorNow.Add(-20 * day)
	rep, _, err := c.Run(context.Background(), false, time.Time{})
	if err != nil || len(rep.Stale) != 1 || rep.Stale[0] != "a" || len(rep.Archived) != 0 {
		t.Fatalf("err=%v rep=%+v", err, rep)
	}
	if !curatorExists(filepath.Join(dir, "a", "SKILL.md")) {
		t.Fatal("moved")
	}
}

func TestCuratorSkipsProtected(t *testing.T) {
	c, st, dir := curatorSetup(t)
	for _, n := range []string{"tether-memory", "hubbed", "keepme", "cronned"} {
		curatorSkill(t, dir, n, n)
		curatorFlag(st, n, n, int64(len(st.flags)+1), 60*day)
	}
	f := st.flags["keepme"]
	f.Kept = true
	st.flags["keepme"] = f
	if err := UpdateLock(dir, func(l *LockFile) error {
		l.Installed["hubbed"] = LockEntry{Source: "x"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c.CronUsers = func(_ context.Context, _ []string) (map[string][]string, error) {
		return map[string][]string{"cronned": {`"job" (j1)`}}, nil
	}
	rep, _, err := c.Run(context.Background(), true, time.Time{})
	if err != nil || len(rep.Archived) != 0 || rep.SkippedBundled != 1 || rep.SkippedHub != 1 || rep.Kept != 1 {
		t.Fatalf("err=%v rep=%+v", err, rep)
	}
}

func TestCuratorPromptWordCronUser(t *testing.T) {
	c, st, dir := curatorSetup(t)
	curatorSkill(t, dir, "daily-mail", "daily-mail")
	curatorFlag(st, "daily-mail", "daily-mail", 1, 60*day)
	c.CronUsers = func(_ context.Context, _ []string) (map[string][]string, error) {
		return map[string][]string{"daily-mail": {`"digest" (j1)`}}, nil
	}
	rep, _, err := c.Run(context.Background(), true, time.Time{})
	if err != nil || len(rep.Archived) != 0 {
		t.Fatalf("err=%v rep=%+v", err, rep)
	}
}

func TestCuratorNestedSkillNeverArchived(t *testing.T) {
	c, st, dir := curatorSetup(t)
	curatorSkill(t, dir, "parent", "parent")
	curatorSkill(t, dir, "parent/child", "child")
	curatorFlag(st, "parent", "parent", 1, 60*day)
	curatorFlag(st, "child", "parent/child", 2, 60*day)
	rep, _, err := c.Run(context.Background(), true, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range rep.Archived {
		if f.Name == "parent" {
			t.Fatal("parent archived")
		}
	}
	if !curatorExists(filepath.Join(dir, "parent", "SKILL.md")) {
		t.Fatal("parent SKILL.md gone")
	}
}

func TestCuratorGraceTime(t *testing.T) {
	c, st, dir := curatorSetup(t)
	curatorSkill(t, dir, "a", "a")
	curatorFlag(st, "a", "a", 1, 10*day)
	rep, _, err := c.Run(context.Background(), true, time.Time{})
	if err != nil || len(rep.Archived) != 0 || len(rep.Stale) != 0 {
		t.Fatalf("err=%v rep=%+v", err, rep)
	}
}

func TestCuratorNotDue(t *testing.T) {
	c, st, _ := curatorSetup(t)
	ctx := context.Background()
	l := curatorNow.Add(-1 * day)
	st.lastRun = &l
	if _, ran, _ := c.Run(ctx, false, time.Time{}); ran {
		t.Fatal("ran inside the interval")
	}
	l = curatorNow.Add(-8 * day)
	if _, ran, _ := c.Run(ctx, false, curatorNow.Add(-30*time.Minute)); ran {
		t.Fatal("ran while not idle")
	}
	c.Cfg.Enabled = false
	if _, ran, _ := c.Run(ctx, false, time.Time{}); ran {
		t.Fatal("ran while disabled")
	}
	if _, ran, err := c.Run(ctx, true, time.Time{}); !ran || err != nil {
		t.Fatalf("force: ran=%v err=%v", ran, err)
	}
}

func TestCuratorArchiveTargetExists(t *testing.T) {
	c, st, dir := curatorSetup(t)
	curatorSkill(t, dir, "old-one", "old-one")
	curatorFlag(st, "old-one", "old-one", 1, 60*day)
	if err := os.MkdirAll(filepath.Join(dir, ".archive", "old-one-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	rep, _, err := c.Run(context.Background(), true, time.Time{})
	if err != nil || len(rep.Archived) != 0 {
		t.Fatalf("err=%v rep=%+v", err, rep)
	}
	if !curatorExists(filepath.Join(dir, "old-one", "SKILL.md")) {
		t.Fatal("moved")
	}
}

func TestCuratorCronError(t *testing.T) {
	c, st, dir := curatorSetup(t)
	curatorSkill(t, dir, "old-one", "old-one")
	curatorFlag(st, "old-one", "old-one", 1, 60*day)
	c.CronUsers = func(context.Context, []string) (map[string][]string, error) {
		return nil, errors.New("db closed")
	}
	if _, _, err := c.Run(context.Background(), true, time.Time{}); err == nil {
		t.Fatal("want error")
	}
	if !curatorExists(filepath.Join(dir, "old-one", "SKILL.md")) {
		t.Fatal("moved")
	}
}

func TestCuratorRestore(t *testing.T) {
	c, st, dir := curatorArchiveOne(t)
	ctx := context.Background()
	if err := c.Restore(ctx, "graphkb-lightrag"); err != nil {
		t.Fatal(err)
	}
	if !curatorExists(filepath.Join(dir, "research", "graphkb-lightrag", "SKILL.md")) {
		t.Fatal("not back")
	}
	f := st.flags["graphkb-lightrag"]
	if !f.ArchivedAt.IsZero() || f.ArchivedPath != "" || !f.FirstSeen.Equal(curatorNow) {
		t.Fatalf("flag %+v", f)
	}
	if c.IsArchived(ctx, "graphkb-lightrag") {
		t.Fatal("still archived")
	}
	err := c.Restore(ctx, "graphkb-lightrag")
	if err == nil || err.Error() != "the skill graphkb-lightrag is not archived" {
		t.Fatalf("err=%v", err)
	}
}

func TestCuratorRestoreRefusedWhenNameExists(t *testing.T) {
	c, st, dir := curatorSetup(t)
	curatorSkill(t, dir, "x", "x")
	curatorFlag(st, "x", "x", 1, 60*day)
	if _, _, err := c.Run(context.Background(), true, time.Time{}); err != nil {
		t.Fatal(err)
	}
	curatorSkill(t, dir, "x", "x")
	err := c.Restore(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "a skill named x exists") {
		t.Fatalf("err=%v", err)
	}
	if !curatorExists(filepath.Join(dir, ".archive", "x-1", "SKILL.md")) {
		t.Fatal("archive folder gone")
	}
}

func TestCuratorStaleRows(t *testing.T) {
	c, st, dir := curatorSetup(t)
	for _, n := range []string{"a", "b", "tether-self"} {
		curatorSkill(t, dir, n, n)
		curatorFlag(st, n, n, int64(len(st.flags)+1), 60*day)
	}
	st.activity["a"] = curatorNow.Add(-20 * day)
	st.activity["b"] = curatorNow.Add(-2 * day)
	rows, err := c.Stale(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []StaleRow{{"a", curatorNow.Add(-20 * day), "stale"}, {"b", curatorNow.Add(-2 * day), "active"}}
	if len(rows) != 2 || rows[0] != want[0] || rows[1] != want[1] {
		t.Fatalf("rows %+v", rows)
	}
}

func TestCuratorSetKept(t *testing.T) {
	c, st, dir := curatorSetup(t)
	curatorSkill(t, dir, "a", "a")
	ctx := context.Background()
	if err := c.SetKept(ctx, "a", true); err != nil || !st.flags["a"].Kept {
		t.Fatalf("err=%v", err)
	}
	if err := c.SetKept(ctx, "a", false); err != nil || st.flags["a"].Kept {
		t.Fatalf("err=%v", err)
	}
	err := c.SetKept(ctx, "nope", true)
	if err == nil || err.Error() != "no skill named nope" {
		t.Fatalf("err=%v", err)
	}
}

func TestCuratorLimitsDefaults(t *testing.T) {
	c := &Curator{Cfg: config.CuratorConfig{StaleAfterDays: 20, ArchiveAfterDays: 10}}
	_, _, s, a := c.limits()
	if s != 20*day || a != 20*day {
		t.Fatalf("stale=%v archive=%v", s, a)
	}
	c = &Curator{}
	i, idle, s, a := c.limits()
	if i != 168*time.Hour || idle != 2*time.Hour || s != 14*day || a != 30*day {
		t.Fatalf("%v %v %v %v", i, idle, s, a)
	}
}

// A changed flag row cannot make Restore move a folder from outside the archive, or into a
// place outside the skills folder.
func TestCuratorRestoreRefusesPathOutsideArchive(t *testing.T) {
	c, st, dir := curatorArchiveOne(t)
	outside := filepath.Join(t.TempDir(), "other")
	curatorSkill(t, outside, "evil", "evil")
	f := st.flags["graphkb-lightrag"]
	good := f.ArchivedPath
	f.ArchivedPath = filepath.Join(outside, "evil")
	st.flags["graphkb-lightrag"] = f
	err := c.Restore(context.Background(), "graphkb-lightrag")
	if err == nil || !strings.Contains(err.Error(), "is not in the skill archive") {
		t.Fatalf("err=%v", err)
	}
	if !curatorExists(filepath.Join(outside, "evil", "SKILL.md")) {
		t.Fatal("the outside folder moved")
	}
	f.ArchivedPath, f.RelDir = good, "../../escape"
	st.flags["graphkb-lightrag"] = f
	err = c.Restore(context.Background(), "graphkb-lightrag")
	if err == nil || !strings.Contains(err.Error(), "is not in the skill archive") {
		t.Fatalf("err=%v", err)
	}
	if !curatorExists(filepath.Join(good, "SKILL.md")) || curatorExists(filepath.Join(dir, "research", "graphkb-lightrag")) {
		t.Fatal("the archive folder moved")
	}
}
