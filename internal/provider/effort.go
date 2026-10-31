// The map follows the provider docs of Oct 31, 2026.

package provider

// effortFields are the wire fields for one effort level on one provider.
// The zero value sends no effort field.
type effortFields struct {
	// ReasoningEffort is the top-level "reasoning_effort" of Chat
	// Completions.
	ReasoningEffort string
	// ResponsesEffort is "reasoning.effort" of the Responses format, and of
	// OpenRouter on Chat Completions.
	ResponsesEffort string
	// OutputEffort is the Anthropic "output_config.effort".
	OutputEffort string
	// ThinkingDisabled sends thinking: {"type":"disabled"}.
	ThinkingDisabled bool
	// ThinkingAdaptive sends the Anthropic thinking: {"type":"adaptive"}.
	ThinkingAdaptive bool
	// EnableThinking is the Alibaba "enable_thinking".
	EnableThinking *bool
}

// mapEffort returns the wire fields for level on a provider with the
// effort style style. Each level goes to the nearest level that the
// provider accepts. An empty level gives the zero value, so the provider
// default applies. An unknown style maps like "chat".
func mapEffort(style, level string) effortFields {
	if level == "" {
		return effortFields{}
	}
	switch style {
	case "responses", "openrouter":
		return effortFields{ResponsesEffort: level}
	case "xai":
		// xAI cannot turn off reasoning and has no "minimal" or "max".
		switch level {
		case "none", "minimal":
			level = "low"
		case "max":
			level = "xhigh"
		}
		return effortFields{ResponsesEffort: level}
	case "anthropic":
		// Claude cannot turn off reasoning with the effort field.
		if level == "none" || level == "minimal" {
			level = "low"
		}
		return effortFields{OutputEffort: level}
	case "anthropic-std":
		return effortFields{OutputEffort: level}
	case "glm":
		// DeepSeek accepts thinking disabled, so "none" turns thinking off.
		switch level {
		case "none":
			return effortFields{ThinkingDisabled: true}
		case "minimal", "low":
			return effortFields{ReasoningEffort: "low"}
		case "medium", "high":
			return effortFields{ReasoningEffort: "high"}
		default:
			return effortFields{ReasoningEffort: "max"}
		}
	case "glm-think":
		// GLM-5.3 and kimi-k3.1-pro always think. They accept only low, high,
		// and max, and they return HTTP 400 for thinking disabled. Thus
		// "none" and "minimal" go to "low".
		switch level {
		case "none", "minimal", "low":
			return effortFields{ReasoningEffort: "low"}
		case "medium", "high":
			return effortFields{ReasoningEffort: "high"}
		default:
			return effortFields{ReasoningEffort: "max"}
		}
	case "qwen":
		// Qwen3.8 accepts the levels low, medium, and xhigh. "none" turns
		// thinking off, because Qwen models can think or not think.
		on := level != "none"
		if !on {
			return effortFields{EnableThinking: &on}
		}
		switch level {
		case "minimal", "low":
			level = "low"
		case "medium":
		default:
			level = "xhigh"
		}
		return effortFields{EnableThinking: &on, ReasoningEffort: level}
	case "minimax":
		// MiniMax-M3 thinks only with thinking adaptive. MiniMax-M3.1-Flash
		// always thinks and returns HTTP 400 for thinking disabled. Thus
		// "none" sends only the effort "low": M3 then does not think, and
		// M3.1-Flash thinks at the lowest level.
		if level == "none" {
			return effortFields{OutputEffort: "low"}
		}
		if level == "minimal" {
			level = "low"
		}
		return effortFields{OutputEffort: level, ThinkingAdaptive: true}
	default:
		// "chat", "ollama", and an unknown style send the level as is.
		return effortFields{ReasoningEffort: level}
	}
}
