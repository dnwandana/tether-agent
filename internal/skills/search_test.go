package skills

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func skillMD(name string) string {
	return "---\nname: " + name + "\ndescription: d " + name + "\n---\nbody\n"
}

// writeTree writes each slash path in files under root with its content.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func foundNames(found []FoundSkill) []string {
	var names []string
	for _, f := range found {
		names = append(names, f.Name)
	}
	return names
}

func TestFindSkillsLayouts(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"skills/pdf/SKILL.md":           skillMD("pdf"),
		"skills/.curated/nb/SKILL.md":   skillMD("jupyter-notebook"),
		"skills/.hidden/x/SKILL.md":     skillMD("hidden"),
		"node_modules/m/SKILL.md":       skillMD("mod"),
		".git/s/SKILL.md":               skillMD("gitskill"),
		"skills/pdf/inner/SKILL.md":     skillMD("inner"),
		"a/b/c/d/e/f/SKILL.md":          skillMD("deep"),
		"a/b/c/d/e/SKILL.md":            skillMD("edge"),
		"broken/SKILL.md":               "no frontmatter",
		"broken/below/SKILL.md":         skillMD("below-broken"),
		"skills/pdf/scripts/run.sh":     "echo hi\n",
		"skills/.system/sys/SKILL.md":   skillMD("system-skill"),
		"skills/.experimental/e/README": "not a skill\n",
	})
	found, warns, err := FindSkills(root)
	if err != nil {
		t.Fatalf("FindSkills: %v", err)
	}
	got := strings.Join(foundNames(found), ",")
	if want := "edge,jupyter-notebook,pdf,system-skill"; got != want {
		t.Fatalf("names = %q, want %q", got, want)
	}
	for _, f := range found {
		switch f.Name {
		case "pdf":
			if f.RelDir != "skills/pdf" {
				t.Errorf("pdf RelDir = %q, want skills/pdf", f.RelDir)
			}
			if f.Description != "d pdf" {
				t.Errorf("pdf Description = %q", f.Description)
			}
		case "jupyter-notebook":
			if f.RelDir != "skills/.curated/nb" {
				t.Errorf("jupyter-notebook RelDir = %q", f.RelDir)
			}
		case "edge":
			if f.RelDir != "a/b/c/d/e" {
				t.Errorf("edge RelDir = %q", f.RelDir)
			}
		}
	}
	if len(warns) != 1 {
		t.Fatalf("warnings = %+v, want one", warns)
	}
	if want := filepath.Join(root, "broken", "SKILL.md"); warns[0].Path != want {
		t.Errorf("warning path = %q, want %q", warns[0].Path, want)
	}
}

func TestFindSkillsRootSkill(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"SKILL.md":       skillMD("solo"),
		"sub/SKILL.md":   skillMD("sub"),
		"docs/notes.txt": "x\n",
	})
	found, warns, err := FindSkills(root)
	if err != nil {
		t.Fatalf("FindSkills: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %+v", warns)
	}
	if len(found) != 1 || found[0].Name != "solo" || found[0].RelDir != "." {
		t.Fatalf("found = %+v, want one solo at .", found)
	}
}

func TestFindSkillsSortsByNameThenDir(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"z/SKILL.md": skillMD("pdf"),
		"a/SKILL.md": skillMD("pdf"),
		"m/SKILL.md": skillMD("docx"),
	})
	found, _, err := FindSkills(root)
	if err != nil {
		t.Fatalf("FindSkills: %v", err)
	}
	var got []string
	for _, f := range found {
		got = append(got, f.Name+"@"+f.RelDir)
	}
	if want := "docx@m,pdf@a,pdf@z"; strings.Join(got, ",") != want {
		t.Fatalf("order = %v, want %s", got, want)
	}
}

func TestFindSkillsDoesNotFollowSymlinks(t *testing.T) {
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{"evil/SKILL.md": skillMD("evil")})
	root := t.TempDir()
	writeTree(t, root, map[string]string{"real/SKILL.md": skillMD("real")})
	if err := os.Symlink(filepath.Join(outside, "evil"), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	found, _, err := FindSkills(root)
	if err != nil {
		t.Fatalf("FindSkills: %v", err)
	}
	if got := strings.Join(foundNames(found), ","); got != "real" {
		t.Fatalf("names = %q, want real", got)
	}
}

func TestFindSkillsMissingRoot(t *testing.T) {
	if _, _, err := FindSkills(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("FindSkills on a missing root: want an error")
	}
}

func TestSelectSkill(t *testing.T) {
	found := []FoundSkill{{Name: "docx", RelDir: "skills/docx"}, {Name: "pdf", RelDir: "skills/pdf"}}

	s, err := SelectSkill(found, "pdf")
	if err != nil || s.RelDir != "skills/pdf" {
		t.Fatalf("SelectSkill pdf = %+v, %v", s, err)
	}

	_, err = SelectSkill(found, "nope")
	if err == nil || !strings.Contains(err.Error(), `no skill named "nope"`) || !strings.Contains(err.Error(), "docx, pdf") {
		t.Fatalf("SelectSkill nope err = %v", err)
	}

	_, err = SelectSkill(found, "")
	var many *ManySkillsError
	if !errors.As(err, &many) {
		t.Fatalf("SelectSkill empty name err = %v, want *ManySkillsError", err)
	}
	if len(many.Found) != 2 {
		t.Errorf("ManySkillsError.Found = %+v", many.Found)
	}
	if want := "the repository has 2 skills. Select one with --skill"; err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}

	s, err = SelectSkill(found[:1], "")
	if err != nil || s.Name != "docx" {
		t.Fatalf("SelectSkill one = %+v, %v", s, err)
	}

	_, err = SelectSkill(nil, "")
	if err == nil || err.Error() != "no SKILL.md found" {
		t.Fatalf("SelectSkill none err = %v", err)
	}

	dup := append(append([]FoundSkill{}, found...), FoundSkill{Name: "pdf", RelDir: "other/pdf"})
	_, err = SelectSkill(dup, "pdf")
	if err == nil || !strings.Contains(err.Error(), `two folders have the skill "pdf"`) ||
		!strings.Contains(err.Error(), "skills/pdf") || !strings.Contains(err.Error(), "other/pdf") {
		t.Fatalf("SelectSkill dup err = %v", err)
	}
}

func TestNestedSkill(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"SKILL.md": skillMD("pdf"), "scripts/a.sh": "x\n"})
	rel, ok, err := NestedSkill(dir)
	if err != nil || ok || rel != "" {
		t.Fatalf("NestedSkill only root = %q, %v, %v", rel, ok, err)
	}

	writeTree(t, dir, map[string]string{"x/SKILL.md": skillMD("x"), "y/z/SKILL.md": skillMD("z")})
	rel, ok, err = NestedSkill(dir)
	if err != nil || !ok || rel != "x/SKILL.md" {
		t.Fatalf("NestedSkill = %q, %v, %v; want x/SKILL.md", rel, ok, err)
	}
}
