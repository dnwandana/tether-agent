package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/tools"
)

func TestSkillSecretNeedsEnvPassthrough(t *testing.T) {
	cfg := config.Default()
	cfg.Terminal.EnvPassthrough = []string{"TETHER_TEST_GH_TOKEN"}
	dotenv := map[string]string{"TETHER_TEST_GH_TOKEN": "gh-value", "TETHER_TEST_AWS_KEY": "aws-value"}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cloud"), 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: cloud\ndescription: Uses two keys.\nrequired_environment_variables:\n  - TETHER_TEST_GH_TOKEN\n  - TETHER_TEST_AWS_KEY\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "cloud", "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	// A skill_view of the skill adds no name.
	view := tools.SkillViewTool{Catalog: &tools.SkillCatalog{Dir: dir}}
	env := &tools.Env{Values: map[string]any{}}
	if _, err := view.Run(context.Background(), json.RawMessage(`{"name":"cloud"}`), env); err != nil {
		t.Fatal(err)
	}
	pass := skillPassthrough(&cfg)
	if !slices.Equal(pass, []string{"TETHER_TEST_GH_TOKEN"}) {
		t.Fatalf("got %v", pass)
	}
	child := security.ChildEnv(os.Environ(), dotenv, pass)
	if !slices.Contains(child, "TETHER_TEST_GH_TOKEN=gh-value") {
		t.Fatal("a name in terminal.env_passthrough must reach the child")
	}
	for _, kv := range child {
		if strings.HasPrefix(kv, "TETHER_TEST_AWS_KEY=") {
			t.Fatal("a skill must not pass a .env secret")
		}
	}
	// The result is a copy.
	pass[0] = "X"
	if cfg.Terminal.EnvPassthrough[0] != "TETHER_TEST_GH_TOKEN" {
		t.Fatal("skillPassthrough must not share the config slice")
	}
}

func TestEnvStatus(t *testing.T) {
	cfg := config.Default()
	cfg.Terminal.EnvPassthrough = []string{"TETHER_TEST_LISTED", "TETHER_TEST_LISTED_EMPTY"}
	dotenv := map[string]string{"TETHER_TEST_LISTED": "a", "TETHER_TEST_DOTENV": "b"}
	t.Setenv("TETHER_TEST_REGION", "eu")
	t.Setenv("TETHER_TEST_API_KEY", "c")
	const notPassed = "not passed: add it to terminal.env_passthrough in config.yaml"
	names := []string{"TETHER_TEST_LISTED", "TETHER_TEST_DOTENV", "TETHER_TEST_REGION",
		"TETHER_TEST_API_KEY", "TETHER_TEST_LISTED_EMPTY", "TETHER_TEST_MISSING"}
	want := []string{"passed", notPassed, "passed", notPassed, "not set", "not set"}
	if got := envStatus(&cfg, dotenv, names); !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := envStatus(&cfg, dotenv, nil); len(got) != 0 {
		t.Fatalf("got %q", got)
	}
}
