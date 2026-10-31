package provider

import (
	"reflect"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
)

func TestMapEffort(t *testing.T) {
	no, yes := false, true
	cases := []struct {
		style, level string
		want         effortFields
	}{
		{"responses", "none", effortFields{ResponsesEffort: "none"}},
		{"responses", "max", effortFields{ResponsesEffort: "max"}},
		{"xai", "none", effortFields{ResponsesEffort: "low"}},
		{"xai", "minimal", effortFields{ResponsesEffort: "low"}},
		{"xai", "max", effortFields{ResponsesEffort: "xhigh"}},
		{"xai", "medium", effortFields{ResponsesEffort: "medium"}},
		{"openrouter", "none", effortFields{ResponsesEffort: "none"}},
		{"anthropic", "none", effortFields{OutputEffort: "low"}},
		{"anthropic", "minimal", effortFields{OutputEffort: "low"}},
		{"anthropic", "xhigh", effortFields{OutputEffort: "xhigh"}},
		{"anthropic-std", "none", effortFields{OutputEffort: "none"}},
		{"glm", "none", effortFields{ThinkingDisabled: true}},
		{"glm", "minimal", effortFields{ReasoningEffort: "low"}},
		{"glm", "low", effortFields{ReasoningEffort: "low"}},
		{"glm", "medium", effortFields{ReasoningEffort: "high"}},
		{"glm", "high", effortFields{ReasoningEffort: "high"}},
		{"glm", "xhigh", effortFields{ReasoningEffort: "max"}},
		{"glm", "max", effortFields{ReasoningEffort: "max"}},
		{"glm-think", "none", effortFields{ReasoningEffort: "low"}},
		{"glm-think", "minimal", effortFields{ReasoningEffort: "low"}},
		{"glm-think", "low", effortFields{ReasoningEffort: "low"}},
		{"glm-think", "medium", effortFields{ReasoningEffort: "high"}},
		{"glm-think", "high", effortFields{ReasoningEffort: "high"}},
		{"glm-think", "xhigh", effortFields{ReasoningEffort: "max"}},
		{"glm-think", "max", effortFields{ReasoningEffort: "max"}},
		{"qwen", "none", effortFields{EnableThinking: &no}},
		{"qwen", "minimal", effortFields{EnableThinking: &yes, ReasoningEffort: "low"}},
		{"qwen", "low", effortFields{EnableThinking: &yes, ReasoningEffort: "low"}},
		{"qwen", "medium", effortFields{EnableThinking: &yes, ReasoningEffort: "medium"}},
		{"qwen", "high", effortFields{EnableThinking: &yes, ReasoningEffort: "xhigh"}},
		{"qwen", "xhigh", effortFields{EnableThinking: &yes, ReasoningEffort: "xhigh"}},
		{"qwen", "max", effortFields{EnableThinking: &yes, ReasoningEffort: "xhigh"}},
		{"minimax", "none", effortFields{OutputEffort: "low"}},
		{"minimax", "minimal", effortFields{OutputEffort: "low", ThinkingAdaptive: true}},
		{"minimax", "low", effortFields{OutputEffort: "low", ThinkingAdaptive: true}},
		{"minimax", "medium", effortFields{OutputEffort: "medium", ThinkingAdaptive: true}},
		{"minimax", "high", effortFields{OutputEffort: "high", ThinkingAdaptive: true}},
		{"minimax", "xhigh", effortFields{OutputEffort: "xhigh", ThinkingAdaptive: true}},
		{"minimax", "max", effortFields{OutputEffort: "max", ThinkingAdaptive: true}},
		{"ollama", "none", effortFields{ReasoningEffort: "none"}},
		{"chat", "xhigh", effortFields{ReasoningEffort: "xhigh"}},
	}
	for _, c := range cases {
		if got := mapEffort(c.style, c.level); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %s: %+v", c.style, c.level, got)
		}
	}
	for _, style := range []string{"responses", "xai", "glm", "glm-think", "qwen", "minimax", "chat", ""} {
		if got := mapEffort(style, ""); !reflect.DeepEqual(got, effortFields{}) {
			t.Errorf("%s empty: %+v", style, got)
		}
	}
}

// TestPresetsKeepThinkingOn checks the presets whose models always think. These
// models return HTTP 400 for thinking disabled, so no effort level can send it.
func TestPresetsKeepThinkingOn(t *testing.T) {
	for _, id := range []string{"zai", "zai-coding", "moonshot", "minimax"} {
		pre, ok := config.PresetByID(id)
		if !ok {
			t.Fatalf("no preset %s", id)
		}
		for _, level := range config.EffortLevels {
			if f := mapEffort(pre.EffortStyle, level); f.ThinkingDisabled {
				t.Errorf("%s %s: thinking disabled", id, level)
			}
		}
	}
}
