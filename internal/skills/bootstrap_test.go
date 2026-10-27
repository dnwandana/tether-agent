package skills

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestCopyMissingOnce(t *testing.T) {
	d := t.TempDir()
	c, err := CopyMissing(d)
	if err != nil || !slices.Contains(c, "tether-self") {
		t.Fatal(c, err)
	}
	if err := os.WriteFile(filepath.Join(d, "tether-self/SKILL.md"), []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, _ = CopyMissing(d)
	if len(c) != 0 {
		t.Fatal("no overwrite")
	}
	if b, _ := os.ReadFile(filepath.Join(d, "tether-self/SKILL.md")); string(b) != "edited" {
		t.Fatal()
	}
}

func TestCopyMissingCreatesDir(t *testing.T) {
	d := filepath.Join(t.TempDir(), "data", "skills")
	c, err := CopyMissing(d)
	if err != nil || len(c) != len(BootstrapNames()) {
		t.Fatal(c, err)
	}
	sk, warns, err := Load(d, nil, true)
	if err != nil || len(warns) != 0 || len(sk) != len(BootstrapNames()) {
		t.Fatal(sk, warns, err)
	}
	for _, s := range sk {
		if s.Trust != TrustBuiltin {
			t.Fatalf("%s: %s", s.Name, s.Trust)
		}
	}
}

func TestResetRestores(t *testing.T) {
	d := t.TempDir()
	if _, err := CopyMissing(d); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "tether-self", "SKILL.md")
	if err := os.WriteFile(p, []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Reset(d, "tether-self"); err != nil {
		t.Fatal(err)
	}
	want, _ := bootstrapFS.ReadFile("bootstrap/tether-self/SKILL.md")
	if got, _ := os.ReadFile(p); string(got) != string(want) {
		t.Fatal("not restored")
	}
	if err := Reset(d, "nope"); err == nil {
		t.Fatal("unknown name must fail")
	}
	if err := Reset(d, "../x"); err == nil {
		t.Fatal("bad name must fail")
	}
}

func TestResetWhenMissing(t *testing.T) {
	d := t.TempDir()
	if err := Reset(d, "tether-self"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d, "tether-self", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}

func TestTetherSelfCoversKeyPoints(t *testing.T) {
	b, _ := bootstrapFS.ReadFile("bootstrap/tether-self/SKILL.md")
	s := string(b)
	for _, must := range []string{"name: tether-self", "curl -sS -w '\\n%{http_code}'", "--data @", "pdftotext", "use the browser tools",
		"toolsets", "compaction", "cronjob", "no_agent", "delegate_task", "approval",
		// The facts must match the config defaults.
		"50%", "first 3", "last 20", "600", "5"} {
		if !strings.Contains(s, must) {
			t.Fatal("missing", must)
		}
	}
	if !strings.Contains(s, "description: Tether Agent's own manual. Use when") {
		t.Fatal("description style")
	}
}

func TestAllThreeBootstrapSkills(t *testing.T) {
	if !reflect.DeepEqual(BootstrapNames(), []string{"tether-memory", "tether-self", "tether-skills"}) {
		t.Fatal(BootstrapNames())
	}
}

func TestTetherSkillsTeachesScriptPattern(t *testing.T) {
	b, _ := bootstrapFS.ReadFile("bootstrap/tether-skills/SKILL.md")
	s := string(b)
	for _, must := range []string{"name: tether-skills", "description:", "Use when", "references/", "~/.tether/scripts/", "required_environment_variables", "one lesson per rule"} {
		if !strings.Contains(s, must) {
			t.Fatal(must)
		}
	}
}

func TestTetherMemoryLimits(t *testing.T) {
	b, _ := bootstrapFS.ReadFile("bootstrap/tether-memory/SKILL.md")
	s := string(b)
	for _, must := range []string{"2,200", "1,375", "memory.md", "user_profile.md", "skill"} {
		if !strings.Contains(s, must) {
			t.Fatal(must)
		}
	}
}

// TestBootstrapSkillsPassOwnGuardAnyway scans every embedded file. The
// bootstrap skills are never scanned at run time, but clean examples teach
// the model to write skills that pass the guard.
func TestBootstrapSkillsPassOwnGuardAnyway(t *testing.T) {
	for _, name := range BootstrapNames() {
		files := map[string]string{}
		err := fs.WalkDir(bootstrapFS, "bootstrap/"+name, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := bootstrapFS.ReadFile(p)
			files[p] = string(b)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		r := Scan(files)
		if r.Verdict == VerdictDangerous {
			t.Errorf("%s: %s", name, FormatFindings(r))
		}
		for _, f := range r.Findings {
			if f.Severity == SevCritical || f.Severity == SevHigh {
				t.Errorf("%s: keep the examples clean: %s", name, FormatFindings(r))
				break
			}
		}
	}
}
