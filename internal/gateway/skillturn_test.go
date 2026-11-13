package gateway

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/skills"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// addTurnSkill writes the skill name with the frontmatter lines extra and the body into a
// new skills folder. It registers skill_view in the registry of d, so the toolset gate is
// open, and it points d.Skills at the folder.
func addTurnSkill(t *testing.T, d *Deps, name, extra, body string) *tools.SkillCatalog {
	t.Helper()
	dir := t.TempDir()
	sd := filepath.Join(dir, name)
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: Search arXiv papers.\n" + extra + "---\n" + body
	if err := os.WriteFile(filepath.Join(sd, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	cat := &tools.SkillCatalog{Dir: dir}
	if d.Agent != nil && d.Agent.Tools != nil {
		d.Agent.Tools.Register(tools.SkillViewTool{Catalog: cat})
	}
	d.Skills = cat.Load
	return cat
}

// skillTurn runs one turn of text on a new CLI session of d and returns the session.
func skillTurn(t *testing.T, d *Deps, text string, slash bool) *sessions.Session {
	t.Helper()
	ctx := context.Background()
	sess, err := d.Store.Create(ctx, sessions.Session{Key: "cli:test", Source: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	in := TurnInput{Text: text, Channel: "cli", Sink: &CLISink{W: io.Discard}, Slash: slash, Owner: true}
	if _, err := d.Turn(ctx, sess, in); err != nil {
		t.Fatal(err)
	}
	return sess
}

// firstUserText returns the content of the first saved user message of the session id.
func firstUserText(t *testing.T, d *Deps, id string) string {
	t.Helper()
	msgs, err := d.Store.Messages(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.Msg.Role == provider.RoleUser {
			return m.Msg.Content
		}
	}
	t.Fatal("no user message")
	return ""
}

const skillHeader = `[IMPORTANT: The user has invoked the "arxiv" skill`

func TestTurnExpandsSkillCommand(t *testing.T) {
	fp := &fakeProv{}
	d := testDeps(t, fp)
	cat := addTurnSkill(t, d, "arxiv", "required_environment_variables:\n  - ARXIV_KEY\n",
		"Run ${HERMES_SKILL_DIR}/scripts/search.py for session ${HERMES_SESSION_ID}.\n")
	vals := map[string]any{}
	d.NewEnv = func(s *sessions.Session, _ string) *tools.Env {
		return &tools.Env{SessionID: s.ID, Values: vals}
	}
	d.EnvStatus = func(names []string) []string { return []string{"passed"} }
	sess := skillTurn(t, d, "/arxiv transformers 2026", true)
	list, _, err := cat.Load()
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	sk := list[0]
	want := tools.SkillInvocationText(sk, skills.SubstituteTemplateVars(sk.Body, sk.Dir, sess.ID), "transformers 2026", d.EnvStatus)
	if got := fp.lastUserTexts(); len(got) != 1 || got[0] != want {
		t.Fatalf("%q", got)
	}
	if !strings.HasPrefix(want, skillHeader) ||
		!strings.Contains(want, "[Required environment variables: ARXIV_KEY (passed).]") ||
		!strings.Contains(want, "Run "+sk.Dir+"/scripts/search.py for session "+sess.ID+".") ||
		!strings.Contains(want, "The user has provided the following instruction alongside the skill invocation: transformers 2026") {
		t.Fatalf("%q", want)
	}
	// The saved user message is the long text. The title is the typed text.
	if got := firstUserText(t, d, sess.ID); got != want {
		t.Fatalf("the saved user message: %q", got)
	}
	s, err := d.Store.Get(context.Background(), sess.ID)
	if err != nil || s.Title != "/arxiv transformers 2026" {
		t.Fatalf("the title: %+v %v", s, err)
	}
	if _, ok := vals["env_passthrough"]; ok {
		t.Fatal("a skill command must not register env names")
	}
}

func TestTurnCutsLongSkillBody(t *testing.T) {
	fp := &fakeProv{}
	d := testDeps(t, fp)
	d.Cfg.Context.ToolOutputMaxChars = 200
	long := strings.Repeat("a", 150) + strings.Repeat("b", 400)
	addTurnSkill(t, d, "arxiv", "", long+"\n")
	skillTurn(t, d, "/arxiv x", true)
	got := fp.lastUserTexts()
	if len(got) != 1 || !strings.HasPrefix(got[0], skillHeader) ||
		!strings.Contains(got[0], "chars omitted. Full output: "+d.Agent.SpillDir) ||
		strings.Contains(got[0], strings.Repeat("b", 100)) {
		t.Fatalf("%q", got)
	}
	files, err := os.ReadDir(d.Agent.SpillDir)
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	b, err := os.ReadFile(filepath.Join(d.Agent.SpillDir, files[0].Name()))
	if err != nil || !strings.Contains(string(b), long) {
		t.Fatalf("the spill file must hold the full body: %v", err)
	}
}

func TestTurnCutsSkillBodyWithoutSpill(t *testing.T) {
	fp := &fakeProv{}
	d := testDeps(t, fp)
	d.Cfg.Context.ToolOutputMaxChars = 200
	// A regular file in the path makes the spill write fail.
	block := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(block, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.Agent.SpillDir = filepath.Join(block, "spill")
	addTurnSkill(t, d, "arxiv", "", strings.Repeat("a", 150)+strings.Repeat("b", 400)+"\n")
	skillTurn(t, d, "/arxiv x", true)
	got := fp.lastUserTexts()
	if len(got) != 1 || !strings.HasPrefix(got[0], skillHeader) ||
		!strings.Contains(got[0], "chars omitted. The full output could not be saved.]") ||
		strings.Contains(got[0], "Full output: ") {
		t.Fatalf("%q", got)
	}
}

func TestTurnWithoutSlashKeepsText(t *testing.T) {
	fp := &fakeProv{}
	d := testDeps(t, fp)
	addTurnSkill(t, d, "arxiv", "required_environment_variables:\n  - ARXIV_KEY\n", "Search arXiv.\n")
	vals := map[string]any{}
	d.NewEnv = func(s *sessions.Session, _ string) *tools.Env {
		return &tools.Env{SessionID: s.ID, Values: vals}
	}
	// A goal kick, a steer, or a system text has Slash false, also when it starts with /arxiv.
	skillTurn(t, d, "/arxiv x", false)
	if got := fp.lastUserTexts(); len(got) != 1 || got[0] != "/arxiv x" {
		t.Fatalf("%q", got)
	}
	if _, ok := vals["env_passthrough"]; ok {
		t.Fatal("a skill command must not register env names")
	}
}

func TestTurnUnknownSlashKeepsText(t *testing.T) {
	fp := &fakeProv{}
	d := testDeps(t, fp)
	addTurnSkill(t, d, "arxiv", "", "Search arXiv.\n")
	skillTurn(t, d, "/nothing x", true)
	if got := fp.lastUserTexts(); len(got) != 1 || got[0] != "/nothing x" {
		t.Fatalf("%q", got)
	}
}

// A member skill command does not load the skill body when the member toolsets have no
// skill_view. The owner turn on the same channel loads it.
func TestTurnMemberSkillCommandNeedsSkillView(t *testing.T) {
	for _, tc := range []struct {
		owner bool
		want  bool
	}{{true, true}, {false, false}} {
		d := testDeps(t, &fakeProv{})
		cfg := config.Default()
		cfg.Toolsets["telegram"] = []string{tools.SetFile, tools.SetSkills}
		cfg.Toolsets["member"] = []string{tools.SetFile}
		d.Cfg = &cfg
		addTurnSkill(t, d, "arxiv", "", "Secret skill body.\n")
		ctx := context.Background()
		sess, err := d.Store.Create(ctx, sessions.Session{Key: "tg:test", Source: "telegram"})
		if err != nil {
			t.Fatal(err)
		}
		in := TurnInput{Text: "/arxiv x", Channel: "telegram", Sink: &CLISink{W: io.Discard}, Slash: true, Owner: tc.owner, Shared: true}
		if _, err := d.Turn(ctx, sess, in); err != nil {
			t.Fatal(err)
		}
		got := firstUserText(t, d, sess.ID)
		if loaded := strings.Contains(got, "Secret skill body."); loaded != tc.want {
			t.Fatalf("owner %v: loaded = %v, text %q", tc.owner, loaded, got)
		}
	}
}
