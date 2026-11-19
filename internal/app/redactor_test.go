package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewNamedRedactor(t *testing.T) {
	t.Setenv("GH_TOKEN", "env-gh-token-0123456789")
	t.Setenv("TETHER_TEST_LABEL_SECRET", "env-only-secret-0123456789")
	r := NewNamedRedactor(map[string]string{
		"PYPI_TOKEN": "pypi-dotenv-value-0123456789",
		"GH_TOKEN":   "dotenv-gh-token-0123456789",
	})
	in := "a pypi-dotenv-value-0123456789 b dotenv-gh-token-0123456789 c env-gh-token-0123456789 d env-only-secret-0123456789"
	want := "a «PYPI_TOKEN» b «GH_TOKEN» c «GH_TOKEN» d «TETHER_TEST_LABEL_SECRET»"
	if got := r.Redact(in); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// The wizard keeps the Mask form.
func TestNewRedactorKeepsMask(t *testing.T) {
	if got := NewRedactor(map[string]string{"SETUP_EXTRA_0": "typed-key-0123456789"}).Redact("typed-key-0123456789"); got == "«SETUP_EXTRA_0»" {
		t.Fatal("app.NewRedactor must keep the Mask form")
	}
}

func TestAppRedactorUsesLabels(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, fakeProviderURL(t))
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("PYPI_TOKEN=pypi-app-value-0123456789\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := New(ctx, Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if got := a.Redactor.Redact("x pypi-app-value-0123456789"); got != "x «PYPI_TOKEN»" {
		t.Fatal(got)
	}
}
