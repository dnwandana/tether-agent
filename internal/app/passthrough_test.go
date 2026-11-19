package app

import (
	"context"
	"encoding/json"
	"fmt"
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

// testStore writes dotenv to a new .env file and returns a store over it.
func testStore(t *testing.T, dotenv map[string]string) *security.DotEnvStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	var b strings.Builder
	for k, v := range dotenv {
		fmt.Fprintf(&b, "%s=%s\n", k, v)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return security.NewDotEnvStore(path, dotenv, nil)
}

func TestEnvStatus(t *testing.T) {
	cfg := config.Default()
	cfg.Terminal.EnvPassthrough = []string{"TETHER_TEST_LISTED", "TETHER_TEST_LISTED_EMPTY"}
	cfg.ExecSecrets = map[string]config.ExecSecret{
		"TETHER_TEST_BOUND":      {Commands: []string{"twine upload", "uv publish"}},
		"TETHER_TEST_BOUND_PROC": {Commands: []string{"gh release"}},
	}
	store := testStore(t, map[string]string{"TETHER_TEST_LISTED": "a", "TETHER_TEST_DOTENV": "b", "TETHER_TEST_BOUND": "c"})
	t.Setenv("TETHER_TEST_REGION", "eu")
	t.Setenv("TETHER_TEST_API_KEY", "c")
	// A process value of a bound name does not count.
	t.Setenv("TETHER_TEST_BOUND_PROC", "d")
	const notPassed = "not passed: add it to terminal.env_passthrough in config.yaml"
	names := []string{"TETHER_TEST_LISTED", "TETHER_TEST_DOTENV", "TETHER_TEST_REGION",
		"TETHER_TEST_API_KEY", "TETHER_TEST_LISTED_EMPTY", "TETHER_TEST_MISSING",
		"TETHER_TEST_BOUND", "TETHER_TEST_BOUND_PROC"}
	want := []string{"passed", notPassed, "passed", notPassed, "not set", "not set",
		"bound: twine upload, uv publish", "not set"}
	if got := envStatus(&cfg, store, names); !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := envStatus(&cfg, store, nil); len(got) != 0 {
		t.Fatalf("got %q", got)
	}
}
