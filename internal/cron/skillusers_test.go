package cron

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/dnwandana/tether-agent/internal/db"
)

func usersStore(t *testing.T) (*Store, *db.DB) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return &Store{DB: d}, d
}

func usersLine(j Job) string { return `"` + j.Name + `" (` + j.ID + `)` }

func TestSkillUsersSkillsColumn(t *testing.T) {
	st, _ := usersStore(t)
	job, err := st.Create(ctx, Job{Name: "brief", Schedule: "every 2h", Prompt: "p", Skills: []string{"pdf"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	users, err := SkillUsers(ctx, st, []string{"pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{usersLine(job)}; !slices.Equal(users["pdf"], want) {
		t.Fatalf("%v", users)
	}
}

func TestSkillUsersPromptWord(t *testing.T) {
	st, _ := usersStore(t)
	digest, err := st.Create(ctx, Job{Name: "digest", Schedule: "every 2h", Prompt: "Use graphkb-lightrag, then mail it.", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Create(ctx, Job{Name: "other", Schedule: "every 2h", Prompt: "Use graphkb-lightrag2 for this.", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	users, err := SkillUsers(ctx, st, []string{"graphkb-lightrag", "mail"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{usersLine(digest)}
	if !slices.Equal(users["graphkb-lightrag"], want) || !slices.Equal(users["mail"], want) {
		t.Fatalf("%v", users)
	}
}

func TestSkillUsersPausedJob(t *testing.T) {
	st, _ := usersStore(t)
	job, err := st.Create(ctx, Job{Name: "weekly", Schedule: "every 2h", Prompt: "p", Skills: []string{"pdf"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetEnabled(ctx, job.ID, false); err != nil {
		t.Fatal(err)
	}
	users, err := SkillUsers(ctx, st, []string{"pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{usersLine(job)}; !slices.Equal(users["pdf"], want) {
		t.Fatalf("%v", users)
	}
}

func TestSkillUsersCaseAndOnce(t *testing.T) {
	st, _ := usersStore(t)
	job, err := st.Create(ctx, Job{Name: "a", Schedule: "every 2h", Prompt: "Run PDF now.", Skills: []string{"pdf"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	users, err := SkillUsers(ctx, st, []string{"pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{usersLine(job)}; !slices.Equal(users["pdf"], want) {
		t.Fatalf("%v", users)
	}
}

func TestSkillUsersNilNames(t *testing.T) {
	st, _ := usersStore(t)
	job, err := st.Create(ctx, Job{Name: "a", Schedule: "every 2h", Prompt: "check vps-services", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	users, err := SkillUsers(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{usersLine(job)}
	if !slices.Equal(users["vps-services"], want) || !slices.Equal(users["check"], want) {
		t.Fatalf("%v", users)
	}
}

func TestSkillUsersOldSchema(t *testing.T) {
	st, d := usersStore(t)
	job, err := st.Create(ctx, Job{Name: "brief", Schedule: "every 2h", Prompt: "p", Skills: []string{"demo"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `ALTER TABLE cron_jobs DROP COLUMN draft_only`); err != nil {
		t.Fatal(err)
	}
	users, err := SkillUsers(ctx, st, []string{"demo"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{usersLine(job)}; !slices.Equal(users["demo"], want) {
		t.Fatalf("%v", users)
	}
}
