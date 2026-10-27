package skills

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// writeSkill writes <root>/<rel>/SKILL.md with the given name and description.
func writeSkill(t *testing.T, root, rel, name, desc string) {
	t.Helper()
	writeSkillBody(t, root, rel, "---\nname: "+name+"\ndescription: "+desc+"\n---\n# "+name+"\n")
}

// writeSkillBody writes <root>/<rel>/SKILL.md with the exact content.
func writeSkillBody(t *testing.T, root, rel, content string) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRecursiveScanFlatAndNested(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "deploy", "deploy", "Deploy steps. Use when deploying.")
	writeSkill(t, root, "devops/backup", "backup", "Backups. Use when backing up.")
	writeSkill(t, root, ".hidden/x", "x", "hidden")
	sk, _, err := Load(root, nil, true)
	if err != nil || len(sk) != 2 {
		t.Fatal(sk, err)
	}
	if sk[0].Name != "backup" || sk[1].Name != "deploy" {
		t.Fatalf("not sorted by name: %v", sk)
	}
	if sk[0].Dir != filepath.Join(root, "devops", "backup") || sk[0].Path != filepath.Join(root, "devops", "backup", "SKILL.md") {
		t.Fatalf("bad paths: %+v", sk[0])
	}
}

func TestMissingDirIsEmptyNotError(t *testing.T) {
	sk, warns, err := Load(filepath.Join(t.TempDir(), "nope"), nil, true)
	if err != nil || len(sk) != 0 || len(warns) != 0 {
		t.Fatal(sk, warns, err)
	}
}

func TestLocalWinsOverExternalWithWarning(t *testing.T) {
	local, ext := t.TempDir(), t.TempDir()
	writeSkill(t, local, "deploy", "deploy", "local")
	writeSkill(t, ext, "deploy", "deploy", "external")
	sk, warns, _ := Load(local, []string{ext}, true)
	if len(sk) != 1 || sk[0].Description != "local" || len(warns) != 1 {
		t.Fatal(sk, warns)
	}
	if !strings.Contains(warns[0].Text, "deploy") {
		t.Fatal(warns[0])
	}
}

func TestExternalMarked(t *testing.T) {
	local, ext := t.TempDir(), t.TempDir()
	writeSkill(t, ext, "notes", "notes", "Notes. Use when taking notes.")
	sk, _, err := Load(local, []string{ext}, true)
	if err != nil || len(sk) != 1 {
		t.Fatal(sk, err)
	}
	if !sk[0].External || sk[0].Trust != TrustExternal {
		t.Fatalf("%+v", sk[0])
	}
}

func TestTrustLocalAndBuiltin(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "tether-self", "tether-self", "Manual.")
	writeSkill(t, root, "mine", "mine", "Mine.")
	sk, _, _ := Load(root, nil, true)
	got := map[string]Trust{}
	for _, s := range sk {
		got[s.Name] = s.Trust
		if s.External {
			t.Fatal("local skill marked external")
		}
	}
	want := map[string]Trust{"tether-self": TrustBuiltin, "mine": TrustAgentCreated}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}

func TestFrontmatterParsed(t *testing.T) {
	root := t.TempDir()
	writeSkillBody(t, root, "helius", "---\nname: helius\ndescription: Helius RPC. Use when reading Solana data.\nrequired_environment_variables: [HELIUS_API_KEY]\n---\nRun the script.\n")
	s, err := ParseSkillFile(filepath.Join(root, "helius", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "helius" || s.Description != "Helius RPC. Use when reading Solana data." {
		t.Fatalf("%+v", s)
	}
	if !reflect.DeepEqual(s.RequiredEnv, []string{"HELIUS_API_KEY"}) {
		t.Fatal(s.RequiredEnv)
	}
	if s.Body != "Run the script.\n" {
		t.Fatalf("body %q", s.Body)
	}
}

func TestFrontmatterCRLF(t *testing.T) {
	root := t.TempDir()
	writeSkillBody(t, root, "w", "---\r\nname: w\r\ndescription: Windows file.\r\n---\r\nBody\r\n")
	s, err := ParseSkillFile(filepath.Join(root, "w", "SKILL.md"))
	if err != nil || s.Name != "w" || s.Description != "Windows file." {
		t.Fatal(s, err)
	}
}

func TestBadFrontmatterIsWarningNotFatal(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "good", "good", "Good. Use when good.")
	writeSkillBody(t, root, "nodesc", "---\nname: nodesc\n---\nbody\n")
	writeSkillBody(t, root, "nofm", "just text, no frontmatter\n")
	writeSkillBody(t, root, "badyaml", "---\nname: [unclosed\n---\nbody\n")
	sk, warns, err := Load(root, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(sk) != 1 || sk[0].Name != "good" {
		t.Fatal(sk)
	}
	if len(warns) != 3 {
		t.Fatal(warns)
	}
	for _, w := range warns {
		if w.Path == "" || w.Text == "" {
			t.Fatalf("empty warning %+v", w)
		}
	}
}

func TestDescriptionCap1024InIndex(t *testing.T) {
	long := strings.Repeat("é", 1500)
	idx := Index([]Skill{{Name: "a", Description: long}, {Name: "b", Description: "short"}})
	if len(idx) != 2 {
		t.Fatal(idx)
	}
	if n := utf8.RuneCountInString(idx[0].Description); n != 1024 {
		t.Fatalf("got %d runes", n)
	}
	if idx[1] != (IndexLine{Name: "b", Description: "short"}) {
		t.Fatal(idx[1])
	}
}

func TestExternalHomeAndBadFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSkill(t, filepath.Join(home, "ext"), "notes", "notes", "Notes.")
	bad := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(bad, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sk, warns, err := Load(t.TempDir(), []string{"~/ext", bad}, true)
	if err != nil || len(sk) != 1 || sk[0].Name != "notes" {
		t.Fatal(sk, warns, err)
	}
}
