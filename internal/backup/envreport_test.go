package backup

import (
	"path/filepath"
	"reflect"
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

func TestRequiredVarsAddsPresetKeyEnv(t *testing.T) {
	path := writeTmp(t, "providers:\n"+
		"  primary:\n    preset: zai-coding\n    model: m\n"+ // implicit ZAI_API_KEY
		"  own:\n    preset: deepseek\n    api_key: ${MY_KEY}\n    model: m\n"+ // explicit key: no DEEPSEEK_API_KEY
		"  proxy:\n    preset: openai\n    base_url: https://p/v1\n    api_key: x\n    model: m\n") // other host: no OPENAI_API_KEY
	got, err := RequiredVars(path, func(n string) (string, bool) { return "", n == "ZAI_API_KEY" })
	if err != nil {
		t.Fatal(err)
	}
	want := []VarStatus{{Name: "MY_KEY", Set: false}, {Name: "ZAI_API_KEY", Set: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}

func TestRequiredVarsPresetURLWithSlash(t *testing.T) {
	path := writeTmp(t, "providers:\n  a:\n    preset: zai-coding\n    base_url: https://api.z.ai/api/coding/paas/v4/\n    model: m\n"+
		"  b:\n    preset: unknown\n    model: m\n")
	got, err := RequiredVars(path, func(string) (string, bool) { return "", false })
	want := []VarStatus{{Name: "ZAI_API_KEY", Set: false}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, err)
	}
}

func TestRequiredVarsBadYAML(t *testing.T) {
	path := writeTmp(t, "providers: [\n  key: ${A_KEY}\n")
	got, err := RequiredVars(path, func(string) (string, bool) { return "", false })
	want := []VarStatus{{Name: "A_KEY", Set: false}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, err)
	}
}
