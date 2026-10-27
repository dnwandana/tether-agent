package agent

import (
	"strings"
	"testing"
	"time"
)

func containsAny(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

func TestSectionOrderAndEmptySectionsOmitted(t *testing.T) {
	s := BuildSystemPrompt(PromptInput{Base: "BASE", Memory: "m1", SkillsIndex: []SkillLine{{"deploy", "Deploy steps. Use when deploying."}},
		Now: time.Date(2026, 10, 27, 9, 14, 0, 0, time.FixedZone("+07", 7*3600)), WorkDir: "/w", Channel: "cli"})
	iBase, iMem, iSk, iCtx := strings.Index(s, "BASE"), strings.Index(s, "## MEMORY"), strings.Index(s, "## SKILLS"), strings.Index(s, "## CONTEXT")
	if !(iBase == 0 && iBase < iMem && iMem < iSk && iSk < iCtx) {
		t.Fatal(s)
	}
	if strings.Contains(s, "## USER PROFILE") || strings.Contains(s, "## TOOL NOTES") {
		t.Fatal("empty sections omitted")
	}
	if !strings.Contains(s, "- deploy: Deploy steps. Use when deploying.") || !strings.Contains(s, "2026-10-27 09:14 +07") {
		t.Fatal(s)
	}
	if !strings.Contains(s, "/w") || !strings.Contains(s, "cli") {
		t.Fatal(s)
	}
}

func TestAllSectionsInOrder(t *testing.T) {
	s := BuildSystemPrompt(PromptInput{Base: "BASE", Memory: "m1", Profile: "p1", SkillsIndex: []SkillLine{{"a", "b"}},
		ToolNotes: []string{"note one"}, Now: time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC), TZ: "Asia/Jakarta",
		WorkDir: "/w", Channel: "telegram", ChatInfo: "group \"Ops\", topic 7"})
	order := []string{"BASE", "## MEMORY\nm1", "## USER PROFILE\np1", "## SKILLS", "## TOOL NOTES\n- note one", "## CONTEXT"}
	last := -1
	for _, h := range order {
		i := strings.Index(s, h)
		if i <= last {
			t.Fatalf("%q out of order:\n%s", h, s)
		}
		last = i
	}
	for _, want := range []string{"Asia/Jakarta", "group \"Ops\", topic 7", "telegram"} {
		if !strings.Contains(s, want) {
			t.Fatal(want, s)
		}
	}
}

func TestPromptDeterministic(t *testing.T) {
	in := PromptInput{Base: "B", SkillsIndex: []SkillLine{{"z", "1"}, {"a", "2"}}, ToolNotes: []string{"n"}, Now: time.Unix(0, 0).UTC()}
	first := BuildSystemPrompt(in)
	for range 3 {
		if BuildSystemPrompt(in) != first {
			t.Fatal("the prompt must be the same for the same input")
		}
	}
}

func TestBlankMemoryOmitted(t *testing.T) {
	if s := BuildSystemPrompt(PromptInput{Base: "B", Memory: "  \n", Now: time.Unix(0, 0)}); strings.Contains(s, "## MEMORY") {
		t.Fatal(s)
	}
}

func TestDescriptionCap1024(t *testing.T) {
	s := BuildSystemPrompt(PromptInput{Base: "B", SkillsIndex: []SkillLine{{"long", strings.Repeat("é", 2000)}}, Now: time.Unix(0, 0)})
	line := ""
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "- long: ") {
			line = l
		}
	}
	if got := len([]rune(strings.TrimPrefix(line, "- long: "))); got != 1024 {
		t.Fatal(got)
	}
}

func TestDescriptionOnOneLine(t *testing.T) {
	s := BuildSystemPrompt(PromptInput{Base: "B", SkillsIndex: []SkillLine{{"x", "line1\nline2"}}, Now: time.Unix(0, 0)})
	if !strings.Contains(s, "- x: line1 line2") {
		t.Fatal(s)
	}
}

func TestToolNotesMentionCurlWhenTerminalVisible(t *testing.T) {
	n := DefaultToolNotes(map[string]bool{"run_command": true})
	if !containsAny(n, "curl") {
		t.Fatal(n)
	}
	if len(DefaultToolNotes(map[string]bool{})) != 0 {
		t.Fatal("no terminal → no curl note")
	}
	n = DefaultToolNotes(map[string]bool{"web_fetch": true, "browser_navigate": true})
	if len(n) != 2 || !containsAny(n, "pdftotext") || !containsAny(n, "browser tools") {
		t.Fatal(n)
	}
}

func TestBasePromptEmbedded(t *testing.T) {
	b := BasePrompt()
	if len(b) < 200 {
		t.Fatal()
	}
	for _, want := range []string{"Tether Agent", "MEDIA:", "skill_view", "memory"} {
		if !strings.Contains(b, want) {
			t.Fatal(want)
		}
	}
}
