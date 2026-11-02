package gateway

import (
	"bytes"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/skills"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// testSkill returns a data-dir skill with the name.
func testSkill(name string) skills.Skill {
	return skills.Skill{Name: name, Description: "The " + name + " skill.", Dir: "/skills/" + name}
}

// cmdSlugs returns the slugs of cmds in order.
func cmdSlugs(cmds []SkillCommand) []string {
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, c.Slug)
	}
	return out
}

// logTo returns a logger that writes text lines to buf.
func logTo(buf *bytes.Buffer) *slog.Logger { return slog.New(slog.NewTextHandler(buf, nil)) }

func TestSkillCommandsSkipsReservedNames(t *testing.T) {
	// The init of commands.go registers /reload-mcp, so the table has it.
	var logs bytes.Buffer
	// The chat role has no /restart in the command table. ReservedCommandNames holds it.
	list := []skills.Skill{testSkill("arxiv"), testSkill("new"), testSkill("reload_mcp"), testSkill("restart"), testSkill("start")}
	got := SkillCommands(list, logTo(&logs))
	if s := cmdSlugs(got); !slices.Equal(s, []string{"arxiv"}) {
		t.Fatal(s)
	}
	for _, line := range []string{
		"skill command /new is not available: a built-in command has the name; skill new",
		"skill command /reload-mcp is not available: a built-in command has the name; skill reload_mcp",
		"skill command /restart is not available: a built-in command has the name; skill restart",
		"skill command /start is not available: a built-in command has the name; skill start",
	} {
		if !strings.Contains(logs.String(), line) {
			t.Errorf("no log line %q in:\n%s", line, logs.String())
		}
	}
	if strings.Count(logs.String(), "level=WARN") != 4 {
		t.Fatalf("want 4 warn lines:\n%s", logs.String())
	}
}

func TestSkillCommandsSlugClashFirstWins(t *testing.T) {
	var logs bytes.Buffer
	// skills.Load sorts by name: "git-helper" comes before "git_helper".
	got := SkillCommands([]skills.Skill{testSkill("git-helper"), testSkill("git_helper")}, logTo(&logs))
	if len(got) != 1 || got[0].Skill.Name != "git-helper" || got[0].Slug != "git-helper" || got[0].MenuName != "git_helper" {
		t.Fatalf("%+v", got)
	}
	want := "skill command /git-helper is not available: skill git-helper has the same command; skill git_helper"
	if !strings.Contains(logs.String(), want) || !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("no log line %q in:\n%s", want, logs.String())
	}
}

func TestSkillCommandsDataDirWinsOverExternal(t *testing.T) {
	var logs bytes.Buffer
	ext := skills.Skill{Name: "Arxiv", Description: "An external skill.", Dir: "/ext/Arxiv", External: true}
	// skills.Load sorts by name: "Arxiv" comes before "arxiv".
	got := SkillCommands([]skills.Skill{ext, testSkill("arxiv")}, logTo(&logs))
	if len(got) != 1 || got[0].Skill.Name != "arxiv" || got[0].Skill.External {
		t.Fatalf("%+v", got)
	}
	want := "skill command /arxiv is not available: skill arxiv has the same command; skill Arxiv"
	if !strings.Contains(logs.String(), want) {
		t.Fatalf("no log line %q in:\n%s", want, logs.String())
	}
}

func TestSkillCommandsSkipsBadNamesAndSortsBySlug(t *testing.T) {
	zeta := testSkill("zeta")
	alpha := skills.Skill{Name: "alpha", Description: "An external skill.", Dir: "/ext/alpha", External: true}
	list := []skills.Skill{
		testSkill("Git Helper"), testSkill("pdf.tools"), testSkill(`say "hi"`), testSkill("tab\tname"),
		zeta, testSkill("小说拆条"), alpha,
	}
	got := SkillCommands(list, slog.New(slog.DiscardHandler))
	if s := cmdSlugs(got); !slices.Equal(s, []string{"alpha", "git-helper", "pdftools", "zeta"}) {
		t.Fatal(s)
	}
	var menu []string
	for _, c := range got {
		menu = append(menu, c.MenuName)
	}
	if !slices.Equal(menu, []string{"alpha", "git_helper", "pdftools", "zeta"}) {
		t.Fatal(menu)
	}
}

func TestResolveSkill(t *testing.T) {
	cmds := SkillCommands([]skills.Skill{testSkill("arxiv"), testSkill("tether-memory")}, slog.New(slog.DiscardHandler))
	for _, name := range []string{"tether-memory", "tether_memory"} {
		c, ok := ResolveSkill(cmds, name)
		if !ok || c.Skill.Name != "tether-memory" {
			t.Fatalf("%s: %+v %v", name, c, ok)
		}
	}
	if c, ok := ResolveSkill(cmds, "arxiv"); !ok || c.Skill.Name != "arxiv" {
		t.Fatalf("%+v %v", c, ok)
	}
	if _, ok := ResolveSkill(cmds, "nothing"); ok {
		t.Fatal("an unknown name must not resolve")
	}
	if _, ok := ResolveSkill(nil, "arxiv"); ok {
		t.Fatal("an empty table must not resolve")
	}
}

func TestSlashCandidate(t *testing.T) {
	for text, want := range map[string]bool{
		"/arxiv x":          true,
		"/arxiv@tether_bot": true,
		"/tether_memory":    true,
		"/nothing":          true,
		"/new":              false,
		"/NEW":              false,
		"/start":            false,
		"/restart":          false,
		"/exit":             false,
		"/reload_mcp":       false,
		"/reload-mcp":       false,
		"hello":             false,
		"/etc/hosts":        false,
		"":                  false,
	} {
		if got := SlashCandidate(text); got != want {
			t.Errorf("SlashCandidate(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestSkillCommandsForGate(t *testing.T) {
	calls := 0
	load := func() ([]skills.Skill, []skills.Warning, error) {
		calls++
		return []skills.Skill{testSkill("arxiv")}, nil, nil
	}
	cfg := config.Default()
	cfg.Toolsets["telegram"] = []string{tools.SetFile}
	reg := tools.NewRegistry()
	reg.Register(tools.SkillViewTool{})
	reg.Register(tools.ReadFileTool{})
	d := &Deps{Cfg: &cfg, Agent: &agent.Agent{Tools: reg}, Skills: load, Log: slog.New(slog.DiscardHandler)}

	if s := cmdSlugs(d.SkillCommandsFor("cli")); !slices.Equal(s, []string{"arxiv"}) {
		t.Fatal(s)
	}
	// The toolsets of telegram hide skill_view, so telegram has no skill commands and
	// reads no skill folder.
	calls = 0
	if got := d.SkillCommandsFor("telegram"); got != nil || calls != 0 {
		t.Fatalf("%+v, %d loads", got, calls)
	}
	// With no agent, or an agent with no tool registry, the gate is open.
	if s := cmdSlugs((&Deps{Skills: load}).SkillCommandsFor("cli")); !slices.Equal(s, []string{"arxiv"}) {
		t.Fatal(s)
	}
	if s := cmdSlugs((&Deps{Agent: &agent.Agent{}, Skills: load}).SkillCommandsFor("cli")); !slices.Equal(s, []string{"arxiv"}) {
		t.Fatal(s)
	}
}

func TestSkillCommandsForNoSkills(t *testing.T) {
	if got := (&Deps{Log: slog.New(slog.DiscardHandler)}).SkillCommandsFor("cli"); got != nil {
		t.Fatalf("a nil Skills must give no commands: %+v", got)
	}
	var logs bytes.Buffer
	d := &Deps{Log: logTo(&logs), Skills: func() ([]skills.Skill, []skills.Warning, error) {
		return []skills.Skill{testSkill("arxiv")}, nil, errors.New("disk failed")
	}}
	if got := d.SkillCommandsFor("cli"); got != nil {
		t.Fatalf("a load error must give no commands: %+v", got)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "disk failed") {
		t.Fatalf("no warn line for the load error:\n%s", logs.String())
	}
}
