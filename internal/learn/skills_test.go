package learn

import (
	"context"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/skills"
)

// skillLogDB opens a new state.db in a temporary folder. The test closes it at the end.
func skillLogDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// skillSession writes a sessions row with the id and the kind.
func skillSession(t *testing.T, d *db.DB, id, kind string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO sessions (id, kind) VALUES (?, ?)`, id, kind); err != nil {
		t.Fatal(err)
	}
}

// skillMessage writes a messages row. toolCalls is "" for NULL.
func skillMessage(t *testing.T, d *db.DB, sessionID, role, content, toolCalls string, at time.Time) {
	t.Helper()
	var tc any
	if toolCalls != "" {
		tc = toolCalls
	}
	if _, err := d.Exec(`INSERT INTO messages (session_id, role, content, tool_calls, timestamp) VALUES (?, ?, ?, ?, ?)`,
		sessionID, role, content, tc, at.UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

func TestSkillLogCopies(t *testing.T) {
	l := NewSkillLog(skillLogDB(t))
	t0 := time.Unix(1764100000, 0)
	var ids []int64
	for i := range 4 {
		c, err := l.AddCopy(skills.Copy{Name: "vps-services", RelDir: "devops/vps-services", At: t0.Add(time.Duration(i) * time.Minute),
			Action: "patch", Source: "review", SessionID: "r1", Path: "/data/skills/.history/x"})
		if err != nil || c.ID <= 0 || !regexp.MustCompile(`^[0-9a-f]{4}$`).MatchString(c.Stamp) {
			t.Fatalf("%+v %v", c, err)
		}
		ids = append(ids, c.ID)
	}
	other, _ := l.AddCopy(skills.Copy{Name: "daily-brief", RelDir: "daily-brief", Action: "create", Source: "chat"})
	if err := l.FinishCopy(ids[3], "h-after"); err != nil {
		t.Fatal(err)
	}
	if err := l.MarkCopyUndone(ids[2], other.ID); err != nil {
		t.Fatal(err)
	}
	c, err := l.GetCopy(ids[3])
	if err != nil || c.HashAfter != "h-after" || c.RelDir != "devops/vps-services" || c.Path != "/data/skills/.history/x" || !c.At.Equal(t0.Add(3*time.Minute)) {
		t.Fatalf("%+v %v", c, err)
	}
	if c, _ := l.GetCopy(ids[2]); c.UndoneBy != other.ID {
		t.Fatal(c.UndoneBy)
	}
	list, err := l.ListCopies("vps-services", 10)
	if err != nil || len(list) != 4 || list[0].ID != ids[3] {
		t.Fatal(list, err)
	}
	if all, _ := l.ListCopies("", 10); len(all) != 5 || all[0].ID != other.ID {
		t.Fatal(all)
	}
	gone, err := l.PruneCopies("vps-services", 2)
	if err != nil || len(gone) != 2 || gone[0].Path == "" {
		t.Fatal(gone, err)
	}
	if left, _ := l.ListCopies("vps-services", 10); len(left) != 2 || left[1].ID != ids[2] {
		t.Fatal(left)
	}
	if gone, _ := l.PruneCopies("vps-services", 0); len(gone) != 0 {
		t.Fatal("keep 0 must delete nothing", gone)
	}
}

func TestSkillLogFlagsAndRuns(t *testing.T) {
	ctx := context.Background()
	l := NewSkillLog(skillLogDB(t))
	seen := time.Unix(1764100000, 0)
	f, err := l.SaveFlag(ctx, skills.Flag{Name: "graphkb-lightrag", RelDir: "rag/graphkb-lightrag", FirstSeen: seen})
	if err != nil || f.ID <= 0 {
		t.Fatal(f, err)
	}
	f.Kept, f.ArchivedAt, f.ArchivedPath = true, seen.Add(time.Hour), "/data/skills/.archive/rag__graphkb-lightrag-1"
	if _, err := l.SaveFlag(ctx, f); err != nil {
		t.Fatal(err)
	}
	flags, err := l.Flags(ctx)
	got := flags["graphkb-lightrag"]
	if err != nil || len(flags) != 1 || got.ID != f.ID || !got.Kept || !got.FirstSeen.Equal(seen) ||
		!got.ArchivedAt.Equal(seen.Add(time.Hour)) || got.ArchivedPath != f.ArchivedPath {
		t.Fatalf("%+v %v", flags, err)
	}
	got.ArchivedAt, got.ArchivedPath = time.Time{}, ""
	l.SaveFlag(ctx, got)
	if flags, _ := l.Flags(ctx); !flags["graphkb-lightrag"].ArchivedAt.IsZero() {
		t.Fatal("a zero ArchivedAt must write NULL")
	}
	if _, ok, err := l.LastRun(ctx); ok || err != nil {
		t.Fatal(ok, err)
	}
	run := time.Unix(1764200000, 0)
	if err := l.SetLastRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if at, ok, err := l.LastRun(ctx); !ok || err != nil || !at.Equal(run) {
		t.Fatal(at, ok, err)
	}
}

func TestSkillLogLastActivity(t *testing.T) {
	ctx := context.Background()
	d := skillLogDB(t)
	l := NewSkillLog(d)
	skillSession(t, d, "c1", "chat")
	skillSession(t, d, "r1", "review")
	t1, t2, t3, t4 := time.Unix(1764100000, 0), time.Unix(1764200000, 0), time.Unix(1764300000, 0), time.Unix(1764400000, 0)
	l.now = func() time.Time { return t1 }
	l.RecordRead(ctx, "vps-services", "view", "c1")
	l.now = func() time.Time { return t3 }
	l.RecordRead(ctx, "vps-services", "view", "r1")  // a review read does not count
	l.RecordRead(ctx, "vps-services", "other", "c1") // an unknown source does not count
	l.AddCopy(skills.Copy{Name: "vps-services", At: t2, Action: "patch", Source: "owner"})
	l.AddCopy(skills.Copy{Name: "vps-services", At: t4, Action: "patch", Source: "review"}) // does not count
	at, ok, err := l.LastActivity(ctx, "vps-services")
	if err != nil || !ok || !at.Equal(t2) {
		t.Fatal(at, ok, err)
	}
	l.now = func() time.Time { return t4 }
	l.RecordRead(ctx, "vps-services", "cron", "")
	if at, _, _ := l.LastActivity(ctx, "vps-services"); !at.Equal(t4) {
		t.Fatal("a cron read with no session counts", at)
	}
	if _, ok, err := l.LastActivity(ctx, "never-read"); ok || err != nil {
		t.Fatal(ok, err)
	}
}

func TestSkillLogBackfill(t *testing.T) {
	ctx := context.Background()
	d := skillLogDB(t)
	l := NewSkillLog(d)
	skillSession(t, d, "c1", "chat")
	skillSession(t, d, "j1", "cron")
	skillSession(t, d, "r1", "review")
	t1, t2, t3 := time.Unix(1764100000, 0), time.Unix(1764200000, 0), time.Unix(1764300000, 0)
	skillMessage(t, d, "c1", "assistant", "", `[{"ID":"a","Name":"skill_view","Args":"{\"name\":\"graphkb-lightrag\"}"}]`, t1)
	skillMessage(t, d, "c1", "user", `[IMPORTANT: The user has invoked the "vps-services" skill, indicating they want you to follow its instructions. The full skill content is loaded below.]`+"\n\nbody", "", t2)
	skillMessage(t, d, "j1", "user", "Send the report.\n\n[Skill: daily-brief]\nbody\n", "", t3)
	skillMessage(t, d, "r1", "assistant", "", `[{"ID":"b","Name":"skill_view","Args":"{\"name\":\"review-only\"}"}]`, t3)
	if err := l.Backfill(ctx); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]time.Time{"graphkb-lightrag": t1, "vps-services": t2, "daily-brief": t3} {
		if at, ok, _ := l.LastActivity(ctx, name); !ok || !at.Equal(want) {
			t.Errorf("%s: %v %v, want %v", name, at, ok, want)
		}
	}
	if _, ok, _ := l.LastActivity(ctx, "review-only"); ok {
		t.Fatal("a review session must not count")
	}
	var n int
	d.QueryRow(`SELECT count(*) FROM skill_reads`).Scan(&n)
	if err := l.Backfill(ctx); err != nil {
		t.Fatal(err)
	}
	var again int
	d.QueryRow(`SELECT count(*) FROM skill_reads`).Scan(&again)
	if n != 3 || again != 3 {
		t.Fatal("the backfill must run once", n, again)
	}
	if v, ok, _ := d.KVGet(ctx, "skills.curator.backfilled"); !ok || v == "" {
		t.Fatal("the backfill must set its kv key")
	}
}
