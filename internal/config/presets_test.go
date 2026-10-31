package config

import (
	"slices"
	"strings"
	"testing"
)

func TestPresetTable(t *testing.T) {
	want := []string{"xai", "openrouter", "openai", "anthropic", "zai", "zai-coding",
		"deepseek", "moonshot", "alibaba", "minimax", "ollama-cloud"}
	got := Presets()
	if len(got) != len(want) {
		t.Fatalf("len %d", len(got))
	}
	for i, id := range want {
		eq(t, got[i].ID, id)
	}
	cases := []struct {
		id, format, url, key, style string
		quirks                      []string
	}{
		{"xai", "responses", "https://api.x.ai/v1", "XAI_API_KEY", "xai", []string{"conv_id_header", "prompt_cache_key"}},
		{"openrouter", "openai", "https://openrouter.ai/api/v1", "OPENROUTER_API_KEY", "openrouter", []string{"openrouter_headers"}},
		{"openai", "responses", "https://api.openai.com/v1", "OPENAI_API_KEY", "responses", []string{"prompt_cache_key"}},
		{"anthropic", "anthropic", "https://api.anthropic.com", "ANTHROPIC_API_KEY", "anthropic", nil},
		{"zai", "openai", "https://api.z.ai/api/paas/v4", "ZAI_API_KEY", "glm-think", []string{"reasoning_content", "keep_thinking"}},
		{"zai-coding", "openai", "https://api.z.ai/api/coding/paas/v4", "ZAI_API_KEY", "glm-think", []string{"reasoning_content"}},
		{"deepseek", "openai", "https://api.deepseek.com/v1", "DEEPSEEK_API_KEY", "glm", []string{"reasoning_content"}},
		{"moonshot", "openai", "https://api.moonshot.ai/v1", "MOONSHOT_API_KEY", "glm-think", []string{"reasoning_content"}},
		{"alibaba", "openai", "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", "DASHSCOPE_API_KEY", "qwen", []string{"reasoning_content"}},
		{"minimax", "anthropic", "https://api.minimax.io/anthropic", "MINIMAX_API_KEY", "minimax", []string{"bearer_auth"}},
		{"ollama-cloud", "openai", "https://ollama.com/v1", "OLLAMA_API_KEY", "ollama", nil},
	}
	for _, c := range cases {
		p, ok := PresetByID(c.id)
		if !ok || p.Format != c.format || p.BaseURL != c.url || p.KeyEnv != c.key || p.EffortStyle != c.style ||
			!slices.Equal(p.Quirks, c.quirks) || p.Group == "" || p.KeyEnv == "" {
			t.Errorf("%s: %+v", c.id, p)
		}
	}
	for _, id := range []string{"alibaba-coding", "ollama"} {
		if _, ok := PresetByID(id); ok {
			t.Fatalf("%s must not exist", id)
		}
	}
	eq(t, strings.Join(PresetIDs(), ","), "alibaba,anthropic,deepseek,minimax,moonshot,ollama-cloud,openai,openrouter,xai,zai,zai-coding")
}

func TestPresetGroups(t *testing.T) {
	// The wizard shows one item for Z.AI with a second question. Ollama has one row, so it
	// asks no second question.
	z, _ := PresetByID("zai")
	zc, _ := PresetByID("zai-coding")
	eq(t, z.Group, "Z.AI")
	eq(t, z.Choice, "Pay as you go")
	eq(t, zc.Group, "Z.AI")
	eq(t, zc.Choice, "Coding Plan")
	oc, _ := PresetByID("ollama-cloud")
	eq(t, oc.Group, "Ollama")
	eq(t, oc.Choice, "")
}

func TestPresetsReturnsACopy(t *testing.T) {
	p := Presets()
	p[0].ID = "changed"
	p[0].Quirks[0] = "changed"
	q, _ := PresetByID("xai")
	eq(t, q.ID, "xai")
	eq(t, q.Quirks[0], "conv_id_header")
}

func TestNewConfigDefaults(t *testing.T) {
	d := Default()
	if d.Agent.Fallback != nil {
		t.Fatal("agent.fallback default must be nil")
	}
	eq(t, strings.Join(EffortLevels, ","), "none,minimal,low,medium,high,xhigh,max")
}
