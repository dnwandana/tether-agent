package gateway

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

const (
	cliSkillHeader  = `[IMPORTANT: The user has invoked the "arxiv" skill`
	cliLoadingLine  = ansiDim + "⚡ Loading skill: arxiv" + ansiReset + "\n"
	cliArgsLine     = "The user has provided the following instruction alongside the skill invocation: transformers 2026"
	cliUnknownArxiv = "Unknown command /arxiv. Send /help to see the commands.\n"
)

func TestCLIRunsSkillCommand(t *testing.T) {
	fp := &fakeProv{}
	d := testDeps(t, fp)
	addTurnSkill(t, d, "arxiv", "", "Search arXiv.\n")
	out := runCLI(t, d, "/arxiv transformers 2026\n")
	if !strings.Contains(out, cliLoadingLine) || strings.Contains(out, "Unknown command") {
		t.Fatalf("%q", out)
	}
	got := fp.lastUserTexts()
	if len(got) != 1 || !strings.HasPrefix(got[0], cliSkillHeader) || !strings.Contains(got[0], cliArgsLine) {
		t.Fatalf("%q", got)
	}
	sess, err := d.Store.Active(context.Background(), "cli:test")
	if err != nil || sess == nil || sess.Title != "/arxiv transformers 2026" {
		t.Fatalf("the title must be the typed text: %+v %v", sess, err)
	}
}

func TestCLIUnknownSlashTextWithSkills(t *testing.T) {
	fp := &fakeProv{}
	d := testDeps(t, fp)
	addTurnSkill(t, d, "arxiv", "", "Search arXiv.\n")
	out := runCLI(t, d, "/nothing here\n")
	if !strings.Contains(out, "Unknown command /nothing. Send /help to see the commands.\n") ||
		strings.Contains(out, "Loading skill") {
		t.Fatalf("%q", out)
	}
	if got := fp.lastUserTexts(); len(got) != 0 {
		t.Fatalf("no turn must run: %q", got)
	}
}

func TestCLISkillCommandNeedsSkillView(t *testing.T) {
	fp := &fakeProv{}
	d := testDeps(t, fp)
	addTurnSkill(t, d, "arxiv", "", "Search arXiv.\n")
	// The CLI toolsets hide skill_view, so the CLI has no skill commands.
	d.Cfg.Toolsets["cli"] = []string{tools.SetFile}
	out := runCLI(t, d, "/arxiv x\n")
	if !strings.Contains(out, cliUnknownArxiv) || strings.Contains(out, "Loading skill") {
		t.Fatalf("%q", out)
	}
	if got := fp.lastUserTexts(); len(got) != 0 {
		t.Fatalf("no turn must run: %q", got)
	}
}

func TestCLIGoalTurnAfterSkillCommandIsNotExpanded(t *testing.T) {
	judge := &fakeProv{script: []step{
		{text: `{"verdict": "continue", "reason": "not yet"}`},
		{text: `{"verdict": "done", "reason": "all pass"}`},
	}}
	d := cliGoalDeps(t, judge)
	// The agent of testDeps saves its messages in the store of the goal deps.
	fp := &fakeProv{}
	ad := testDeps(t, fp)
	ad.Agent.Store = d.Store
	d.Agent = ad.Agent
	addTurnSkill(t, d, "arxiv", "", "Search arXiv.\n")
	// Run pauses an active goal at the start, so the goal is set at the first read.
	in := &hookReader{lines: []string{"/arxiv transformers 2026"}, first: func() {
		setCLIGoal(t, d, "cli:test", "find papers on transformers", nil)
	}}
	var out bytes.Buffer
	c := &CLI{In: in, Out: &out, Key: "cli:test", Deps: d, Interrupts: make(chan os.Signal)}
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := fp.lastUserTexts()
	if len(got) != 2 || !strings.HasPrefix(got[0], cliSkillHeader) || !strings.HasPrefix(got[1], goals.ContinuationPrefix) {
		t.Fatalf("%q", got)
	}
	if c.slashText != "" {
		t.Fatalf("slashText must be empty after the skill turn: %q", c.slashText)
	}
}

// The goal continuation text does not start with "/", so the test above does not show
// that the mark clears. This test sends the same slash text twice: only the first turn
// carries the mark.
func TestCLIAgentTurnMarksOnlyTheFirstTurn(t *testing.T) {
	fp := &fakeProv{}
	d := testDeps(t, fp)
	addTurnSkill(t, d, "arxiv", "", "Search arXiv.\n")
	ctx := context.Background()
	sess, err := d.Store.Create(ctx, sessions.Session{Key: "cli:test", Source: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := &CLI{Out: &out, Key: "cli:test", Deps: d, slashText: "/arxiv x"}
	for range 2 {
		if _, err := c.AgentTurn(ctx, sess, "/arxiv x", &out); err != nil {
			t.Fatal(err)
		}
	}
	got := fp.lastUserTexts()
	if len(got) != 2 || !strings.HasPrefix(got[0], cliSkillHeader) || got[1] != "/arxiv x" {
		t.Fatalf("%q", got)
	}
	if c.slashText != "" {
		t.Fatalf("slashText must be empty after the turn: %q", c.slashText)
	}
}

// hookReader is an input of the CLI that gives lines and then io.EOF. It calls first at the
// start of the first ReadLine, after the start of Run.
type hookReader struct {
	lines []string
	first func()
	read  bool
}

func (r *hookReader) Read([]byte) (int, error) { return 0, io.EOF }

func (r *hookReader) ReadLine() (string, error) {
	if !r.read {
		r.read = true
		r.first()
	}
	if len(r.lines) == 0 {
		return "", io.EOF
	}
	line := r.lines[0]
	r.lines = r.lines[1:]
	return line, nil
}

func (r *hookReader) Close() error { return nil }
