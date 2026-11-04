package app

import (
	"reflect"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/tools"
)

func TestSkillPassthroughDropsAgentSecrets(t *testing.T) {
	cfg := config.Default()
	cfg.Terminal.EnvPassthrough = []string{"OPENAI_API_KEY"}
	cfg.Telegram.BotToken = "123456:bot-token-value"
	cfg.Providers = map[string]config.Provider{"main": {APIKey: "sk-custom-value"}}
	dotenv := map[string]string{
		"WEATHER_API_KEY": "weather-value",
		"MY_LLM_KEY":      "sk-custom-value",
		"BOT":             "123456:bot-token-value",
	}
	env := &tools.Env{}
	tools.AddPassthrough(env, []string{"WEATHER_API_KEY", "ANTHROPIC_API_KEY", "TELEGRAM_BOT_TOKEN", "MY_LLM_KEY", "BOT"})
	got := skillPassthrough(&cfg, dotenv, env)
	// The owner list keeps its names; a skill keeps only names that are not agent secrets.
	if want := []string{"OPENAI_API_KEY", "WEATHER_API_KEY"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
