package config

import (
	"slices"
	"sort"
)

// Preset is one known provider endpoint. An entry with `preset: <ID>` takes its
// format, base URL, key env, and quirks from this row.
type Preset struct {
	// ID is the value of providers.<name>.preset.
	ID string
	// Group is the wizard item. Two rows can share one group.
	Group string
	// Choice is the answer to the second wizard question of a group with two rows.
	// It is empty for a group with one row.
	Choice string
	// Format is the wire format: openai, anthropic, or responses.
	Format string
	// BaseURL is the base URL of the API.
	BaseURL string
	// KeyEnv is the env var that holds the API key. Each preset has a key env.
	KeyEnv string
	// EffortStyle tells the provider package how to send the reasoning effort.
	EffortStyle string
	// Quirks are the provider flags. They apply only on the preset URL.
	Quirks []string
}

// removedPresets are the preset ids that are not in the table any more. The value
// tells the user what to set in place of the preset.
var removedPresets = map[string]string{
	"ollama": "For a local server, remove the preset line and set format: openai and base_url: http://localhost:11434/v1. " +
		"For Ollama Cloud, use preset: ollama-cloud",
}

// presets is the table in wizard order. The URLs and the formats match the Hermes
// profiles and the docs of each provider.
var presets = []Preset{
	{ID: "xai", Group: "xAI", Format: "responses", BaseURL: "https://api.x.ai/v1", KeyEnv: "XAI_API_KEY",
		EffortStyle: "xai", Quirks: []string{"conv_id_header", "prompt_cache_key"}},
	{ID: "openrouter", Group: "OpenRouter", Format: "openai", BaseURL: "https://openrouter.ai/api/v1", KeyEnv: "OPENROUTER_API_KEY",
		EffortStyle: "openrouter", Quirks: []string{"openrouter_headers"}},
	{ID: "openai", Group: "OpenAI", Format: "responses", BaseURL: "https://api.openai.com/v1", KeyEnv: "OPENAI_API_KEY",
		EffortStyle: "responses", Quirks: []string{"prompt_cache_key"}},
	{ID: "anthropic", Group: "Anthropic", Format: "anthropic", BaseURL: "https://api.anthropic.com", KeyEnv: "ANTHROPIC_API_KEY",
		EffortStyle: "anthropic"},
	{ID: "zai", Group: "Z.AI", Choice: "Pay as you go", Format: "openai", BaseURL: "https://api.z.ai/api/paas/v4", KeyEnv: "ZAI_API_KEY",
		EffortStyle: "glm-think", Quirks: []string{"reasoning_content", "keep_thinking"}},
	{ID: "zai-coding", Group: "Z.AI", Choice: "Coding Plan", Format: "openai", BaseURL: "https://api.z.ai/api/coding/paas/v4", KeyEnv: "ZAI_API_KEY",
		EffortStyle: "glm-think", Quirks: []string{"reasoning_content"}},
	{ID: "deepseek", Group: "DeepSeek", Format: "openai", BaseURL: "https://api.deepseek.com/v1", KeyEnv: "DEEPSEEK_API_KEY",
		EffortStyle: "glm", Quirks: []string{"reasoning_content"}},
	{ID: "moonshot", Group: "Moonshot", Format: "openai", BaseURL: "https://api.moonshot.ai/v1", KeyEnv: "MOONSHOT_API_KEY",
		EffortStyle: "glm-think", Quirks: []string{"reasoning_content"}},
	{ID: "alibaba", Group: "Alibaba", Format: "openai", BaseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", KeyEnv: "DASHSCOPE_API_KEY",
		EffortStyle: "qwen", Quirks: []string{"reasoning_content"}},
	{ID: "minimax", Group: "MiniMax", Format: "anthropic", BaseURL: "https://api.minimax.io/anthropic", KeyEnv: "MINIMAX_API_KEY",
		EffortStyle: "minimax", Quirks: []string{"bearer_auth"}},
	{ID: "ollama-cloud", Group: "Ollama", Format: "openai", BaseURL: "https://ollama.com/v1", KeyEnv: "OLLAMA_API_KEY",
		EffortStyle: "ollama"},
}

// EffortLevels are the permitted values of providers.<name>.effort and /effort, from
// the lowest to the highest. The empty value is also permitted and sends no field.
var EffortLevels = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// Presets returns a copy of the preset table in wizard order.
func Presets() []Preset {
	out := make([]Preset, len(presets))
	for i, p := range presets {
		p.Quirks = slices.Clone(p.Quirks)
		out[i] = p
	}
	return out
}

// PresetByID returns the preset with the given id.
func PresetByID(id string) (Preset, bool) {
	for _, p := range presets {
		if p.ID == id {
			p.Quirks = slices.Clone(p.Quirks)
			return p, true
		}
	}
	return Preset{}, false
}

// PresetIDs returns the preset ids in sorted order.
func PresetIDs() []string {
	ids := make([]string, len(presets))
	for i, p := range presets {
		ids[i] = p.ID
	}
	sort.Strings(ids)
	return ids
}
