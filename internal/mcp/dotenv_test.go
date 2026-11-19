package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dnwandana/tether-agent/internal/security"
)

func TestManagerReadsEnvStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("SEARCH_KEY=first-value-0123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := security.NewDotEnvStore(path, map[string]string{"SEARCH_KEY": "first-value-0123"}, nil)
	m := &Manager{EnvStore: store, DotEnv: map[string]string{"SEARCH_KEY": "old"}}
	if got := m.dotEnv()["SEARCH_KEY"]; got != "first-value-0123" {
		t.Fatalf("got %q", got)
	}
	// A value saved later reaches the next dial, with no restart.
	if err := os.WriteFile(path, []byte("SEARCH_KEY=second-value-0123456\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := m.dotEnv()["SEARCH_KEY"]; got != "second-value-0123456" {
		t.Fatalf("got %q", got)
	}
	// With no store, the map of the field stays.
	if got := (&Manager{DotEnv: map[string]string{"SEARCH_KEY": "old"}}).dotEnv()["SEARCH_KEY"]; got != "old" {
		t.Fatalf("got %q", got)
	}
}
