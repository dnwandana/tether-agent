package app

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/tools"
)

func TestRegistersRequestSecret(t *testing.T) {
	a := newApp(t)
	tool, ok := a.Registry.Get("request_secret")
	if !ok {
		t.Fatal("request_secret must register also with an empty exec_secrets")
	}
	if rs, ok := tool.(tools.RequestSecretTool); !ok || len(rs.Bound) != 0 {
		t.Fatalf("%#v", tool)
	}
	t.Cleanup(func() { security.SetBoundSecretNames(nil) })
	dir := t.TempDir()
	writeConfig(t, dir, fakeProviderURL(t), "exec_secrets:\n  PYPI_TOKEN:\n    commands: [\"twine upload\"]\n")
	b, err := New(ctx, Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	tool, _ = b.Registry.Get("request_secret")
	if rs := tool.(tools.RequestSecretTool); !reflect.DeepEqual(rs.Bound, map[string][]string{"PYPI_TOKEN": {"twine upload"}}) {
		t.Fatalf("%#v", rs.Bound)
	}
}

func TestNewWiresBoundSecrets(t *testing.T) {
	t.Cleanup(func() { security.SetBoundSecretNames(nil) })
	dir := t.TempDir()
	writeConfig(t, dir, fakeProviderURL(t), "exec_secrets:\n  PYPI_TOKEN:\n    commands: [\"twine upload\", \"uv publish\"]\n"+
		"  GITHUB_PAT:\n    commands: [\"gh release\"]\n")
	const pypi = "pypi-AgEIcHlwaS5vcmcCJGQ5ZTk2"
	envFile := filepath.Join(dir, ".env")
	if err := os.WriteFile(envFile, []byte("PYPI_TOKEN="+pypi+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := New(ctx, Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	names := slices.Clone(security.BoundSecretNames())
	slices.Sort(names)
	if !slices.Equal(names, []string{"GITHUB_PAT", "PYPI_TOKEN"}) {
		t.Fatalf("bound names %q", names)
	}
	// GITHUB_PAT does not match the name heuristic, but a bound name is a secret name.
	if !security.IsSecretName("GITHUB_PAT", nil) {
		t.Fatal("a bound name must be a secret name")
	}
	want := map[string][]string{"GITHUB_PAT": {"gh release"}, "PYPI_TOKEN": {"twine upload", "uv publish"}}
	if a.terminal.Store != a.DotEnv || a.terminal.Pins == nil || a.terminal.Pins.DB != a.DB || !reflect.DeepEqual(a.terminal.Bound, want) {
		t.Fatalf("terminal fields: %+v", a.terminal)
	}
	if v, ok := a.DotEnv.Get("PYPI_TOKEN"); !ok || v != pypi {
		t.Fatalf("store value %q %v", v, ok)
	}
	// A value that another process saves is masked at once with its label.
	const pat = "github_" + "pat_11ABCDEFG0123456789abcdefghij"
	if err := os.WriteFile(envFile, []byte("PYPI_TOKEN="+pypi+"\nGITHUB_PAT="+pat+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !a.DotEnv.Refresh() {
		t.Fatal("the store must see the change of .env")
	}
	if got := a.Redactor.Redact("token " + pat); got != "token «GITHUB_PAT»" {
		t.Fatalf("got %q", got)
	}
	if got := a.Deps().EnvStatus([]string{"PYPI_TOKEN"}); !slices.Equal(got, []string{"bound: twine upload, uv publish"}) {
		t.Fatalf("got %q", got)
	}
}

func TestMCPUsesTheStore(t *testing.T) {
	a := newApp(t)
	if a.MCP.EnvStore != a.DotEnv || a.MCP.DotEnv != nil {
		t.Fatal("the MCP manager must read .env through the store")
	}
}
func TestAppSharesHandles(t *testing.T) {
	a := newApp(t)
	if a.Handles == nil || a.Agent.Handles != a.Handles || a.terminal.Handles != a.Handles {
		t.Fatal("the agent and the terminal tool must share one handle store")
	}
	a.Handles.Swap("s1", "ghp_"+strings.Repeat("c", 36), a.Redactor)
	a.ForgetSession("s1")
	if len(a.Handles.Values("s1")) != 0 {
		t.Fatal("ForgetSession must drop the handles of the session")
	}
}
