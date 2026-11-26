package agent

import (
	"strings"
	"testing"
)

func TestMemoryReviewPromptIsTheNewHermesText(t *testing.T) {
	p := MemoryReviewPrompt
	prefix := "Review the conversation above and consider saving to memory if appropriate.\n\nMemory has TWO distinct stores"
	if !strings.HasPrefix(p, prefix) {
		t.Fatalf("prefix wrong: %q", p[:min(len(p), 160)])
	}
	for _, want := range []string{
		"user_profile.md (memory tool, target='user')",
		"memory.md (memory tool, target='memory')",
		"Save only facts GROUNDED in this conversation",
		memoryTetherRules,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, bad := range []string{"USER.md", "MEMORY.md", "persona, desires"} {
		if strings.Contains(p, bad) {
			t.Errorf("contains old text %q", bad)
		}
	}
	if !strings.HasSuffix(p, "just say 'Nothing to save.' and stop.") {
		t.Errorf("suffix wrong")
	}
}

func TestCombinedPromptUsesRoutingAndLesson(t *testing.T) {
	p := CombinedReviewPrompt
	if !strings.Contains(p, "**Memory**: "+memoryRoutingBlock) {
		t.Fatal("missing memory routing block")
	}
	l := strings.Index(p, lessonLayerBlock)
	if l < 0 {
		t.Fatal("missing lesson block")
	}
	if a := strings.Index(p, "Target shape of the skill library"); a < 0 || l < a {
		t.Errorf("lesson block before target shape")
	}
	if b := strings.Index(p, "Signals that warrant a skill update"); b < 0 || l > b {
		t.Errorf("lesson block after signals")
	}
}

func TestSkillPromptHasLessonBlock(t *testing.T) {
	l := strings.Index(SkillReviewPrompt, lessonLayerBlock)
	s := strings.Index(SkillReviewPrompt, "Signals to look for")
	if l < 0 || s < 0 || l > s {
		t.Errorf("lesson block at %d, signals at %d", l, s)
	}
	if strings.Contains(MemoryReviewPrompt, lessonLayerBlock) {
		t.Error("memory prompt has the lesson block")
	}
}

func TestSkillProtectedNamesHubSkills(t *testing.T) {
	if !strings.Contains(skillProtected, "skills/.hub/lock.json") {
		t.Error("skillProtected does not name the hub lock")
	}
	for name, p := range map[string]string{"skill": SkillReviewPrompt, "combined": CombinedReviewPrompt} {
		if !strings.Contains(p, skillProtected) {
			t.Errorf("%s prompt lacks skillProtected", name)
		}
	}
}

func TestReviewPromptKeepsTheNotes(t *testing.T) {
	for _, p := range []string{reviewPrompt(true, false, nil), reviewPrompt(false, true, nil), reviewPrompt(true, true, nil)} {
		if !strings.Contains(p, reviewProvenanceNote) {
			t.Error("missing provenance note")
		}
		if !strings.HasSuffix(p, reviewToolNote) {
			t.Error("missing tool note at the end")
		}
	}
}
