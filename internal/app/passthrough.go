package app

import (
	"os"
	"slices"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// skillPassthrough returns the env names that a child process of the session
// keeps: terminal.env_passthrough, and the required_environment_variables of
// the skills that the session loaded. The owner writes terminal.env_passthrough,
// so each of its names passes. A skill cannot pass a secret of Tether Agent
// itself (see agentSecret), because the author of a skill is not the owner.
func skillPassthrough(cfg *config.Config, dotenv map[string]string, env *tools.Env) []string {
	out := slices.Clone(cfg.Terminal.EnvPassthrough)
	for _, name := range tools.PassthroughFor(env) {
		if agentSecret(cfg, dotenv, name) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// agentSecret reports whether name holds a secret that Tether Agent uses: the
// Telegram bot token, the key env of a provider preset, or a variable whose
// value is the bot token or the API key of a configured provider.
func agentSecret(cfg *config.Config, dotenv map[string]string, name string) bool {
	if name == "TELEGRAM_BOT_TOKEN" {
		return true
	}
	for _, id := range config.PresetIDs() {
		if p, ok := config.PresetByID(id); ok && p.KeyEnv == name {
			return true
		}
	}
	var secrets []string
	if cfg.Telegram.BotToken != "" {
		secrets = append(secrets, cfg.Telegram.BotToken)
	}
	for _, p := range cfg.Providers {
		if p.APIKey != "" {
			secrets = append(secrets, p.APIKey)
		}
	}
	for _, v := range []string{dotenv[name], os.Getenv(name)} {
		if v != "" && slices.Contains(secrets, v) {
			return true
		}
	}
	return false
}
