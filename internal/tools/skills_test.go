package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	out := runTool(t, SkillViewTool{Catalog: cat}, `{"name":"deploy"}`)
	if !strings.Contains(out, "# Steps") || !strings.Contains(out, "references/env.md") {
		t.Fatal(out)
	}
	if out := runTool(t, SkillViewTool{Catalog: cat}, `{"name":"deploy","file":"references/env.md"}`); out != "ENV notes" {
		t.Fatal(out)
	}
	if out := runToolErr(t, SkillViewTool{Catalog: cat}, `{"name":"deploy","file":"references/none.md"}`); !strings.Contains(out, "references/env.md") {
		t.Fatal(out)
	}
}

func TestSkillViewRejectsPathEscape(t *testing.T) {
	cat := catalogWith(t, "deploy", "Deploy steps.", "body", nil)
	if err := os.WriteFile(filepath.Join(cat.Dir, ".env"), []byte("SECRET=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"../../.env", "../.env", "a/../../.env"} {
		out := runToolErr(t, SkillViewTool{Catalog: cat}, fmt.Sprintf(`{"name":"deploy","file":%q}`, f))
		if !strings.Contains(out, "file must be inside the skill folder") {
			t.Fatal(f, out)
		}
	}
	// A symlink in the skill folder that points out of it is refused too.
	if err := os.Symlink(filepath.Join(cat.Dir, ".env"), filepath.Join(cat.Dir, "deploy", "link")); err != nil {
		t.Fatal(err)
	}
	if out := runToolErr(t, SkillViewTool{Catalog: cat}, `{"name":"deploy","file":"link"}`); !strings.Contains(out, "file must be inside the skill folder") {
		t.Fatal(out)
	}
}

func TestSkillViewUnknownListsNames(t *testing.T) {
	cat := catalogWith(t, "deploy", "Deploy steps.", "body", nil)
	writeSkill(t, cat.Dir, "backup", "Backup steps.", "", "body", nil)
	out := runToolErr(t, SkillViewTool{Catalog: cat}, `{"name":"nope"}`)
	if !strings.Contains(out, "backup") || !strings.Contains(out, "deploy") {
		t.Fatal(out)
	}
}

func TestSkillViewEnvStatusLine(t *testing.T) {
	cat := catalogWithEnv(t, "helius", []string{"HELIUS_API_KEY", "HELIUS_REGION"})
	var asked []string
	tool := SkillViewTool{Catalog: cat, EnvStatus: func(names []string) []string {
		asked = names
		return []string{"not passed: add it to terminal.env_passthrough in config.yaml", "passed"}
	}}
	env := &Env{Values: map[string]any{}}
	out := runToolEnv(t, tool, `{"name":"helius"}`, env)
	if !slices.Equal(asked, []string{"HELIUS_API_KEY", "HELIUS_REGION"}) {
		t.Fatalf("EnvStatus got %q", asked)
	}
	want := "\nRequired environment variables: HELIUS_API_KEY (not passed: add it to terminal.env_passthrough in config.yaml), HELIUS_REGION (passed).\n"
	if !strings.Contains(out, want) || strings.Contains(out, "gets them in this session") {
		t.Fatal(out)
	}
	// skill_view does not register the names in the session.
	if _, ok := env.Values["env_passthrough"]; ok {
		t.Fatal("skill_view must not register env names")
	}
	// A nil EnvStatus gives "not set" for each name.
	out = runToolEnv(t, SkillViewTool{Catalog: cat}, `{"name":"helius"}`, env)
	if !strings.Contains(out, "Required environment variables: HELIUS_API_KEY (not set), HELIUS_REGION (not set).") {
		t.Fatal(out)
	}
}

func TestSkillSchemasNameExecSecrets(t *testing.T) {
	view := SkillViewTool{}.Schema().Description
	if !strings.Contains(view, "binds it to a command in exec_secrets in config.yaml. You never see the value.") ||
		strings.Contains(view, "gets them in this session") {
		t.Fatal(view)
	}
	manage := SkillManageTool{}.Schema().Description
	if !strings.Contains(manage, "Ask the owner to bind the key name in exec_secrets to the path of the script.") {
		t.Fatal(manage)
	}
}

func TestSkillViewSeesNewSkill(t *testing.T) {
	cat := catalogWith(t, "deploy", "Deploy steps.", "body", nil)
	runToolErr(t, SkillViewTool{Catalog: cat}, `{"name":"later"}`)
	writeSkill(t, cat.Dir, "later", "Made later.", "", "new body", nil)
	if out := runTool(t, SkillViewTool{Catalog: cat}, `{"name":"later"}`); !strings.Contains(out, "new body") {
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
	for _, tool := range []Tool{SkillsListTool{cat}, SkillViewTool{Catalog: cat}} {
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
	if got := runToolEnv(t, SkillViewTool{Catalog: tool.Catalog}, `{"name":"deploy","file":"references/env.md"}`, env); got != "ENV notes" {
		t.Fatal(got)
	}
	runToolEnv(t, tool, `{"action":"patch","name":"deploy","file":"references/env.md","old_string":"ENV","new_string":"Env"}`, env)
	if got := runToolEnv(t, SkillViewTool{Catalog: tool.Catalog}, `{"name":"deploy","file":"references/env.md"}`, env); got != "Env notes" {
		t.Fatal(got)
	}
	if got := runToolEnv(t, SkillViewTool{Catalog: tool.Catalog}, `{"name":"deploy"}`, env); !strings.Contains(got, "2. ship") {
		t.Fatal(got)
	}
	runToolEnv(t, tool, `{"action":"delete","name":"deploy"}`, env)
	runToolEnvErr(t, SkillViewTool{Catalog: tool.Catalog}, `{"name":"deploy"}`, env)
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

func TestSkillInvocationText(t *testing.T) {
	const header = `[IMPORTANT: The user has invoked the "arxiv" skill, indicating they want you to follow its instructions. The full skill content is loaded below.]`
	const resolve = "Resolve any relative paths in this skill (e.g. `scripts/foo.js`, `templates/config.yaml`) against that directory, then run them with the terminal tool using the absolute path."

	t.Run("no env, no files, no args", func(t *testing.T) {
		dir := t.TempDir()
		s := skills.Skill{Name: "arxiv", Description: "Search arXiv.", Body: "raw body", Dir: dir}
		got := SkillInvocationText(s, "\n  # arXiv\nSearch papers.\n\n", "", nil)
		want := strings.Join([]string{
			header,
			"",
			"# arXiv\nSearch papers.",
			"",
			"[Skill directory: " + dir + "]",
			resolve,
		}, "\n")
		if got != want {
			t.Fatalf("got:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("env, files, and args", func(t *testing.T) {
		root := t.TempDir()
		writeSkill(t, root, "arxiv", "Search arXiv.", "", "raw body", map[string]string{
			"scripts/search.py": "print(1)",
			"references/api.md": "API",
			".env":              "SECRET=1",
		})
		dir := filepath.Join(root, "arxiv")
		s := skills.Skill{Name: "arxiv", Body: "raw body", Dir: dir, RequiredEnv: []string{"ARXIV_KEY", "ARXIV_USER"}}
		got := SkillInvocationText(s, "Search papers.", "transformers 2026\nonly new papers", func([]string) []string { return []string{"passed", "not set"} })
		want := strings.Join([]string{
			header,
			"",
			"Search papers.",
			"",
			"[Skill directory: " + dir + "]",
			resolve,
			"",
			"[Required environment variables: ARXIV_KEY (passed), ARXIV_USER (not set).]",
			"",
			"[This skill has supporting files (paths relative to the skill directory above):]",
			"- references/api.md",
			"- scripts/search.py",
			"",
			`Load any of these with skill_view(name="arxiv", file="<path>"), or run scripts directly by absolute path (e.g. ` + "`node " + dir + "/scripts/foo.js`).",
			"",
			"The user has provided the following instruction alongside the skill invocation: transformers 2026\nonly new papers",
		}, "\n")
		if got != want {
			t.Fatalf("got:\n%s\nwant:\n%s", got, want)
		}
	})
}

func TestSkillViewReplacesTemplateTokens(t *testing.T) {
	cat := catalogWith(t, "deploy", "Deploy steps.",
		"Run ${HERMES_SKILL_DIR}/scripts/run.sh for ${HERMES_SESSION_ID}.",
		map[string]string{"scripts/notes.md": "See ${HERMES_SKILL_DIR} and ${HERMES_SESSION_ID}."})
	all, _, err := cat.Load()
	if err != nil || len(all) != 1 {
		t.Fatal(all, err)
	}
	dir := all[0].Dir
	env := &Env{SessionID: "sess-42", Values: map[string]any{}}
	out := runToolEnv(t, SkillViewTool{Catalog: cat}, `{"name":"deploy"}`, env)
	if !strings.Contains(out, "Run "+dir+"/scripts/run.sh for sess-42.") {
		t.Fatal(out)
	}
	// A support file read with file does not change.
	if out := runToolEnv(t, SkillViewTool{Catalog: cat}, `{"name":"deploy","file":"scripts/notes.md"}`, env); out != "See ${HERMES_SKILL_DIR} and ${HERMES_SESSION_ID}." {
		t.Fatal(out)
	}
	// With a nil env, the session ID is empty, so its token stays.
	out, err = SkillViewTool{Catalog: cat}.Run(context.Background(), json.RawMessage(`{"name":"deploy"}`), nil)
	if err != nil || !strings.Contains(out, "Run "+dir+"/scripts/run.sh for ${HERMES_SESSION_ID}.") {
		t.Fatal(out, err)
	}
}

func TestSkillViewMaxLinkedFiles(t *testing.T) {
	cat := catalogWith(t, "deploy", "Deploy steps.", "# Steps\n1. build", map[string]string{
		"references/a.md": "a", "references/b.md": "b", "references/c.md": "c"})
	tool := SkillViewTool{Catalog: cat, MaxLinkedFiles: 2}
	out := runTool(t, tool, `{"name":"deploy"}`)
	if !strings.Contains(out, "- references/a.md\n- references/b.md\n") || strings.Contains(out, "references/c.md") {
		t.Fatal(out)
	}
	out = runToolErr(t, tool, `{"name":"deploy","file":"references/none.md"}`)
	if !strings.Contains(out, "the files are: references/a.md, references/b.md") || strings.Contains(out, "c.md") {
		t.Fatal(out)
	}
	if out := runTool(t, SkillViewTool{Catalog: cat}, `{"name":"deploy"}`); !strings.Contains(out, "references/c.md") {
		t.Fatal("0 keeps the limit of 50:", out)
	}
}

// skillCopies is a skills.CopyRecorder in memory.
type skillCopies struct{ rows []skills.Copy }

func (r *skillCopies) AddCopy(c skills.Copy) (skills.Copy, error) {
	c.ID, c.Stamp = int64(len(r.rows)+1), "ab12"
	r.rows = append(r.rows, c)
	return c, nil
}
func (r *skillCopies) FinishCopy(id int64, h string) error { r.rows[id-1].HashAfter = h; return nil }
func (r *skillCopies) GetCopy(id int64) (skills.Copy, error) {
	return r.rows[id-1], nil
}
func (r *skillCopies) ListCopies(string, int) ([]skills.Copy, error) { return r.rows, nil }
func (r *skillCopies) MarkCopyUndone(int64, int64) error             { return nil }
func (r *skillCopies) PruneCopies(string, int) ([]skills.Copy, error) {
	return nil, nil
}

func TestSkillManageSourceFromKind(t *testing.T) {
	md := `---\nname: deploy\ndescription: Deploy. Use when deploying.\n---\n# Steps\n1. build\n`
	cases := []struct {
		kind, want string
		nilEnv     bool
	}{
		{"chat", "chat", false}, {"review", "review", false}, {"cron", "cron", false},
		{"subagent", "child", false}, {"", "chat", false}, {"", "chat", true},
	}
	for _, tc := range cases {
		tool := manageTool(t)
		rec := &skillCopies{}
		tool.Mgr.Copies = rec
		env := newEnv(t)
		env.Kind, env.SessionID = tc.kind, "s1"
		if tc.nilEnv {
			runToolEnv(t, tool, `{"action":"create","name":"deploy","content":"`+md+`"}`, nil)
		} else {
			runToolEnv(t, tool, `{"action":"create","name":"deploy","content":"`+md+`"}`, env)
		}
		if len(rec.rows) != 1 || rec.rows[0].Source != tc.want {
			t.Fatalf("kind %q nil=%v: rows %+v, want source %s", tc.kind, tc.nilEnv, rec.rows, tc.want)
		}
		if tc.nilEnv {
			continue
		}
		if rec.rows[0].SessionID != "s1" {
			t.Fatalf("session %q", rec.rows[0].SessionID)
		}
		if got := Edits(env); len(got) != 1 || got[0] != (EditRef{Kind: "skill", ID: 1}) {
			t.Fatalf("edits %+v", got)
		}
	}
}

func TestSkillManageDeleteAddsRef(t *testing.T) {
	tool := manageTool(t)
	tool.Mgr.Copies = &skillCopies{}
	env := newEnv(t)
	md := `---\nname: deploy\ndescription: Deploy. Use when deploying.\n---\n# Steps\n1. build\n`
	runToolEnv(t, tool, `{"action":"create","name":"deploy","content":"`+md+`"}`, env)
	ResetEdits(env)
	runToolEnv(t, tool, `{"action":"delete","name":"deploy"}`, env)
	if got := Edits(env); len(got) != 1 || got[0] != (EditRef{Kind: "skill", ID: 2}) {
		t.Fatalf("edits %+v", got)
	}
}

type readRec struct{ got []string } // each item "<name>|<source>|<session>"

func (r *readRec) RecordRead(_ context.Context, name, source, sessionID string) error {
	r.got = append(r.got, name+"|"+source+"|"+sessionID)
	return nil
}

func TestSkillViewRecordsRead(t *testing.T) {
	cat := catalogWith(t, "demo", "Demo skill.", "body", map[string]string{"references/api.md": "API"})
	rec := &readRec{}
	tool := SkillViewTool{Catalog: cat, Reads: rec}
	env := &Env{SessionID: "s1", Kind: "chat"}
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"name":"demo"}`), env); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"name":"demo","file":"references/api.md"}`), env); err != nil {
		t.Fatal(err)
	}
	if want := []string{"demo|view|s1", "demo|view|s1"}; !slices.Equal(rec.got, want) {
		t.Fatalf("got %q", rec.got)
	}
}

func TestSkillViewNoReadInReview(t *testing.T) {
	rec := &readRec{}
	tool := SkillViewTool{Catalog: catalogWith(t, "demo", "Demo skill.", "body", nil), Reads: rec}
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"name":"demo"}`), &Env{SessionID: "s1", Kind: "review"}); err != nil {
		t.Fatal(err)
	}
	if len(rec.got) != 0 {
		t.Fatalf("got %q", rec.got)
	}
}

func TestSkillViewNoReadOnError(t *testing.T) {
	rec := &readRec{}
	tool := SkillViewTool{Catalog: catalogWith(t, "demo", "Demo skill.", "body", nil), Reads: rec}
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"name":"missing"}`), &Env{SessionID: "s1", Kind: "chat"}); err == nil {
		t.Fatal("want an error")
	}
	if len(rec.got) != 0 {
		t.Fatalf("got %q", rec.got)
	}
}

func TestSkillViewArchivedAnswer(t *testing.T) {
	tool := SkillViewTool{
		Catalog:  catalogWith(t, "demo", "Demo skill.", "body", nil),
		Archived: func(_ context.Context, n string) bool { return n == "graphkb-lightrag" },
	}
	env := &Env{SessionID: "s1", Kind: "chat"}
	_, err := tool.Run(context.Background(), json.RawMessage(`{"name":"graphkb-lightrag"}`), env)
	if err == nil || !strings.Contains(err.Error(), "The skill graphkb-lightrag is archived. The owner can run /skills restore graphkb-lightrag.") {
		t.Fatalf("%v", err)
	}
	_, err = tool.Run(context.Background(), json.RawMessage(`{"name":"other"}`), env)
	if err == nil || !strings.Contains(err.Error(), `no skill named "other"`) {
		t.Fatalf("%v", err)
	}
}

func TestSkillManageCreateListsSimilar(t *testing.T) {
	tool := manageTool(t)
	mk := func(name, desc string) string {
		return "---\nname: " + name + "\ndescription: " + desc + "\n---\nbody\n"
	}
	desc := "Build and query a lightrag graph knowledge base from documents."
	env := &Env{Kind: "chat"}
	first := mk("lightrag-graph", desc)
	if _, err := tool.Run(context.Background(), json.RawMessage(jsonArgs(t, map[string]any{"action": "create", "name": "lightrag-graph", "content": first})), env); err != nil {
		t.Fatal(err)
	}
	second := mk("lightrag-kb", desc)
	out, err := tool.Run(context.Background(), json.RawMessage(jsonArgs(t, map[string]any{"action": "create", "name": "lightrag-kb", "content": second})), env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Similar skills exist: lightrag-graph.") {
		t.Fatalf("%q", out)
	}
}

func jsonArgs(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
