package gateway

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/skills"
)

// withSkills makes list the skills of c. A nil tool registry opens the toolset gate, so
// SkillCommandsFor gives the commands of list on each channel.
func withSkills(c *CommandCtx, list ...skills.Skill) {
	c.Deps.Agent.Tools = nil
	c.Deps.Skills = func() ([]skills.Skill, []skills.Warning, error) { return list, nil, nil }
}

// threeSkills returns a plain skill, a skill with Markdown characters in its description,
// and a skill with a long description on two lines.
func threeSkills() []skills.Skill {
	return []skills.Skill{
		{Name: "arxiv", Description: "Search and summarize arXiv papers"},
		{Name: "git-helper", Description: "Run *git* and [links]"},
		{Name: "notes", Description: "Read the\nnotes " + strings.Repeat("y", 60)},
	}
}

// manySkills returns the skills s01 to s<n>, with the descriptions "Skill 01" and so on.
func manySkills(n int) []skills.Skill {
	var list []skills.Skill
	for i := 1; i <= n; i++ {
		list = append(list, skills.Skill{Name: fmt.Sprintf("s%02d", i), Description: fmt.Sprintf("Skill %02d", i)})
	}
	return list
}

// notesLine is the line of the "notes" skill: one line, cut to 60 runes with "…".
var notesLine = "/notes · Read the notes " + strings.Repeat("y", 44) + "…"

// manyLines returns the CLI lines of the skills s<from> to s<to>.
func manyLines(from, to int) string {
	var b strings.Builder
	for i := from; i <= to; i++ {
		fmt.Fprintf(&b, "/s%02d · Skill %02d\n", i, i)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func TestHelpNoSkills(t *testing.T) {
	c, out := cmdCtx(t, "cli:t")
	withSkills(c)
	run(t, c, "/help")
	if out.String() != helpCLI+"\n" {
		t.Fatalf("got:\n%s", out)
	}
}

func TestHelpThreeSkillsTelegram(t *testing.T) {
	c, sent := interactiveCtx(t)
	withSkills(c, threeSkills()...)
	run(t, c, "/help")
	want := helpTelegram + "\n\n⚡ **Skills** (3)\n" +
		"/arxiv · Search and summarize arXiv papers\n" +
		"/git\\_helper · Run \\*git\\* and \\[links\\]\n" + notesLine
	if r := lastSent(t, sent); r.Text != want || r.Buttons != nil {
		t.Fatalf("got:\n%s", r.Text)
	}
}

func TestHelpThreeSkillsMember(t *testing.T) {
	c, sent := interactiveCtx(t)
	c.Owner = false
	withSkills(c, threeSkills()...)
	run(t, c, "/help")
	want := helpMember + "\n\n⚡ **Skills** (3)\n" +
		"/arxiv · Search and summarize arXiv papers\n" +
		"/git\\_helper · Run \\*git\\* and \\[links\\]\n" + notesLine
	if r := lastSent(t, sent); r.Text != want || r.Buttons != nil {
		t.Fatalf("got:\n%s", r.Text)
	}
}

func TestHelpThreeSkillsCLI(t *testing.T) {
	c, out := cmdCtx(t, "cli:t")
	withSkills(c, threeSkills()...)
	run(t, c, "/help")
	want := helpCLI + "\n\n⚡ **Skills** (3)\n" +
		"/arxiv · Search and summarize arXiv papers\n" +
		"/git-helper · Run \\*git\\* and \\[links\\]\n" + notesLine + "\n"
	if out.String() != want {
		t.Fatalf("got:\n%s", out)
	}
}

func TestHelpFourteenSkills(t *testing.T) {
	c, out := cmdCtx(t, "cli:t")
	withSkills(c, manySkills(14)...)
	run(t, c, "/help")
	want := helpCLI + "\n\n⚡ **Skills** (14)\n" + manyLines(1, 10) +
		"\n… and 4 more. Send /help skills to see all.\n"
	if out.String() != want {
		t.Fatalf("got:\n%s", out)
	}
}

func TestHelpSkillsArgument(t *testing.T) {
	c, sent := interactiveCtx(t)
	withSkills(c, threeSkills()...)
	run(t, c, "/help skills")
	want := "⚡ **Skills** (3)\n" +
		"/arxiv · Search and summarize arXiv papers\n" +
		"/git\\_helper · Run \\*git\\* and \\[links\\]\n" + notesLine +
		"\n\nSend /<skill> <request> to run a skill."
	if r := lastSent(t, sent); r.Text != want {
		t.Fatalf("got:\n%s", r.Text)
	}

	cli, out := cmdCtx(t, "cli:t")
	withSkills(cli, manySkills(14)...)
	run(t, cli, "/help skills")
	want = "⚡ **Skills** (14)\n" + manyLines(1, 14) + "\n\nSend /<skill> <request> to run a skill.\n"
	if out.String() != want {
		t.Fatalf("got:\n%s", out)
	}
}

func TestHelpSkillsArgumentNone(t *testing.T) {
	c, out := cmdCtx(t, "cli:t")
	withSkills(c)
	run(t, c, "/help skills")
	want := "⚡ **No skill commands**\nAdd a skill with tether skills install, or ask the agent to make one.\n"
	if out.String() != want {
		t.Fatalf("got:\n%s", out)
	}
}

func TestHelpOtherArgumentGivesNormalHelp(t *testing.T) {
	c, out := cmdCtx(t, "cli:t")
	withSkills(c, threeSkills()...)
	run(t, c, "/help")
	plain := out.String()
	out.Reset()
	run(t, c, "/help model")
	if out.String() != plain {
		t.Fatalf("got:\n%s\nwant:\n%s", out, plain)
	}
}

func TestHelpSkillsMaxFromConfig(t *testing.T) {
	c, out := cmdCtx(t, "cli:t")
	withSkills(c, manySkills(14)...)
	c.Deps.Cfg.Gateway.HelpMaxSkills = 3
	run(t, c, "/help")
	want := helpCLI + "\n\n⚡ **Skills** (14)\n" + manyLines(1, 3) +
		"\n… and 11 more. Send /help skills to see all.\n"
	if out.String() != want {
		t.Fatalf("got:\n%s", out)
	}
	var nilDeps *Deps
	for _, d := range []*Deps{nilDeps, {}, {Cfg: &config.Config{}}} {
		if got := d.helpSkillsLimit(); got != helpSkillsMax {
			t.Fatalf("%+v: %d", d, got)
		}
	}
}
