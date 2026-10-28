package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecide(t *testing.T) {
	cases := []struct {
		tr    Trust
		v     Verdict
		force bool
		want  Decision
	}{
		{TrustCommunity, VerdictSafe, false, DecideAllow},
		{TrustCommunity, VerdictSafe, true, DecideAllow},
		{TrustCommunity, VerdictCaution, false, DecideNeedForce},
		{TrustCommunity, VerdictCaution, true, DecideAllow},
		{TrustCommunity, VerdictDangerous, false, DecideNeedOverride},
		{TrustCommunity, VerdictDangerous, true, DecideNeedOverride},
		{TrustTrusted, VerdictSafe, false, DecideAllow},
		{TrustTrusted, VerdictCaution, false, DecideAllow},
		{TrustTrusted, VerdictDangerous, false, DecideNeedOverride},
		{TrustTrusted, VerdictDangerous, true, DecideNeedOverride},
	}
	for _, c := range cases {
		if got := Decide(c.tr, c.v, c.force); got != c.want {
			t.Errorf("Decide(%s, %s, %v) = %d, want %d", c.tr, c.v, c.force, got, c.want)
		}
	}
}

func TestCheckNameRefusesDisplayNames(t *testing.T) {
	err := CheckName("Convex Best Practices")
	if err == nil || !strings.Contains(err.Error(), "invalid skill name") {
		t.Fatalf("CheckName(display name) = %v, want an invalid skill name error", err)
	}
	if err := CheckName(strings.Repeat("a", 65)); err == nil {
		t.Fatal("CheckName(65 characters) = nil, want an error")
	}
	if err := CheckName("pdf"); err != nil {
		t.Fatalf("CheckName(pdf) = %v, want nil", err)
	}
}

func TestFindSymlink(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"SKILL.md":          skillMD("pdf"),
		"scripts/run.sh":    "echo hi\n",
		"references/doc.md": "text\n",
	})
	rel, ok, err := FindSymlink(dir)
	if err != nil || ok || rel != "" {
		t.Fatalf("FindSymlink(no link) = %q, %v, %v; want \"\", false, nil", rel, ok, err)
	}

	if err := os.Symlink("../../etc/passwd", filepath.Join(dir, "scripts", "link")); err != nil {
		t.Fatal(err)
	}
	rel, ok, err = FindSymlink(dir)
	if err != nil || !ok || rel != "scripts/link" {
		t.Fatalf("FindSymlink(link) = %q, %v, %v; want scripts/link, true, nil", rel, ok, err)
	}
}

func TestFindSymlinkMissingDir(t *testing.T) {
	if _, _, err := FindSymlink(filepath.Join(t.TempDir(), "gone")); err == nil {
		t.Fatal("FindSymlink(missing folder) = nil error, want an error")
	}
}

func TestCheckCollision(t *testing.T) {
	skills := t.TempDir()
	lock := &LockFile{Version: 1, Installed: map[string]LockEntry{
		"pdf": {Identifier: "openai/skills", InstallPath: "pdf"},
	}}

	wantErr := func(t *testing.T, name string, loaded []Skill, force bool, want string) {
		t.Helper()
		got, err := CheckCollision(name, skills, lock, loaded, force)
		if err == nil || err.Error() != want {
			t.Fatalf("CheckCollision(%q, force %v) = %+v, %v; want error %q", name, force, got, err, want)
		}
	}
	wantOK := func(t *testing.T, name string, loaded []Skill, force bool, want Collision) {
		t.Helper()
		got, err := CheckCollision(name, skills, lock, loaded, force)
		if err != nil || got != want {
			t.Fatalf("CheckCollision(%q, force %v) = %+v, %v; want %+v, nil", name, force, got, err, want)
		}
	}

	for _, force := range []bool{false, true} {
		_, err := CheckCollision("tether-memory", skills, lock, nil, force)
		if err == nil || !strings.Contains(err.Error(), "bootstrap") {
			t.Fatalf("CheckCollision(bootstrap name, force %v) = %v, want a bootstrap error", force, err)
		}
	}

	wantErr(t, "pdf", nil, false,
		`"pdf" is already installed from openai/skills. Use tether skills update pdf, or run the install again with --force.`)
	wantOK(t, "pdf", nil, true, Collision{Replace: true})

	if err := os.Mkdir(filepath.Join(skills, "mine"), 0o755); err != nil {
		t.Fatal(err)
	}
	mineErr := "skills/mine already exists and the lockfile does not list it. Rename or delete that folder first."
	wantErr(t, "mine", nil, false, mineErr)
	wantErr(t, "mine", nil, true, mineErr)

	if err := os.Symlink("/nowhere", filepath.Join(skills, "dangling")); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "dangling", nil, true,
		"skills/dangling already exists and the lockfile does not list it. Rename or delete that folder first.")

	loaded := []Skill{
		{Name: "other", Path: skills + "/x/SKILL.md"},
		{Name: "ext", External: true, Path: "/ext/ext/SKILL.md"},
		{Name: "pdf", External: true, Path: "/ext/pdf/SKILL.md"},
	}
	otherErr := "skills/other already exists and the lockfile does not list it. Rename or delete that folder first."
	wantErr(t, "other", loaded, false, otherErr)
	wantErr(t, "other", loaded, true, otherErr)

	wantOK(t, "ext", loaded, false, Collision{
		Warning: `The installed copy of "ext" now wins over the external copy at /ext/ext/SKILL.md.`,
	})
	wantOK(t, "pdf", loaded, true, Collision{
		Replace: true,
		Warning: `The installed copy of "pdf" now wins over the external copy at /ext/pdf/SKILL.md.`,
	})

	wantOK(t, "fresh", loaded, false, Collision{})
	wantOK(t, "fresh", loaded, true, Collision{})
}

func TestCheckCollisionNilLock(t *testing.T) {
	got, err := CheckCollision("fresh", t.TempDir(), nil, nil, false)
	if err != nil || got != (Collision{}) {
		t.Fatalf("CheckCollision(nil lock) = %+v, %v; want {}, nil", got, err)
	}
}
