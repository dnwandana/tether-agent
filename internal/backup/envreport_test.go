package backup

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestRequiredVars(t *testing.T) {
	p := writeTmp(t, "providers:\n  openai:\n    api_key: ${OPENAI_API_KEY}\ntelegram:\n  bot_token: ${TELEGRAM_BOT_TOKEN}\n"+
		"mcp:\n  servers:\n    gh:\n      headers:\n        Authorization: Bearer ${GITHUB_TOKEN} ${GITHUB_TOKEN}\n")
	got, err := RequiredVars(p, func(n string) (string, bool) { return "x", n == "TELEGRAM_BOT_TOKEN" })
	want := []VarStatus{{"GITHUB_TOKEN", false}, {"OPENAI_API_KEY", false}, {"TELEGRAM_BOT_TOKEN", true}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatal(got, err)
	}
}

func TestRequiredVarsMissingConfig(t *testing.T) {
	got, err := RequiredVars(filepath.Join(t.TempDir(), "config.yaml"), nil)
	if err != nil || got != nil {
		t.Fatal(got, err)
	}
}

func TestRequiredVarsNoReferences(t *testing.T) {
	p := writeTmp(t, "model: gpt\n")
	got, err := RequiredVars(p, func(string) (string, bool) { return "", false })
	if err != nil || got != nil {
		t.Fatal(got, err)
	}
}
