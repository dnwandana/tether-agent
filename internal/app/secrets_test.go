package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/security"
)

func TestSaveSecret(t *testing.T) {
	keepCommandTable(t)
	dir := t.TempDir()
	writeConfig(t, dir, fakeProviderURL(t), "exec_secrets:\n  PYPI_TOKEN:\n    commands: [\"twine upload\"]\n")
	a, err := New(ctx, Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(); security.SetBoundSecretNames(nil) })
	if err := a.DB.KVSet(ctx, "secret_check:PYPI_TOKEN", "1763469060000"); err != nil {
		t.Fatal(err)
	}
	value := "pypi-AgEIcHlwaS5vcmcCJDU0ZjM4"
	if err := a.SaveSecret("PYPI_TOKEN", value); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil || !strings.Contains(string(b), "PYPI_TOKEN=") {
		t.Fatal(string(b), err)
	}
	if got := a.Redactor.Redact("token " + value); got != "token «PYPI_TOKEN»" {
		t.Fatal(got)
	}
	if v, ok := a.terminal.Store.Get("PYPI_TOKEN"); !ok || v != value {
		t.Fatal("the store has no value")
	}
	if v, ok, _ := a.DB.KVGet(ctx, "secret_check:PYPI_TOKEN"); ok && v != "" {
		t.Fatal("the mark is still set:", v)
	}
	// A refused value changes nothing.
	if err := a.SaveSecret("PYPI_TOKEN", "a\nb"); err == nil {
		t.Fatal("want the line break error")
	}
	if v, _ := a.terminal.Store.Get("PYPI_TOKEN"); v != value {
		t.Fatal("a refused value reached the store")
	}
}

func TestSecretsWiring(t *testing.T) {
	keepCommandTable(t)
	a := newApp(t)
	if _, ok := gateway.Commands()["secrets"]; !ok {
		t.Fatal("no /secrets command")
	}
	if a.CLIAsk == nil || a.CLIAsk.Save == nil {
		t.Fatal("the CLI has no save function")
	}
}

func TestSecretsHasValueSeesSetupSave(t *testing.T) {
	keepCommandTable(t)
	a := newApp(t)
	has := a.secretsDeps().HasValue
	if has("LATE_TOKEN") {
		t.Fatal("value before the save")
	}
	if err := security.SaveDotEnv(filepath.Join(a.DataDir, ".env"), "LATE_TOKEN", "late-value-123"); err != nil {
		t.Fatal(err)
	}
	if !has("LATE_TOKEN") {
		t.Fatal("HasValue does not see a value that another writer saved")
	}
}
