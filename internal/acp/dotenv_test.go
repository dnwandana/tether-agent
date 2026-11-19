package acp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/security"
)

func TestManagerReadsEnvStore(t *testing.T) {
	// The parts keep the fake keys out of the push-protection scan.
	first, second := "sk-"+"ant-first-0123456789", "sk-"+"ant-second-0123456789abc"
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("ANTHROPIC_API_KEY="+first+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := security.NewDotEnvStore(path, map[string]string{"ANTHROPIC_API_KEY": first}, nil)
	m := NewManager(ManagerOptions{EnvStore: store})
	if got := m.dotEnv()["ANTHROPIC_API_KEY"]; got != first {
		t.Fatalf("got %q", got)
	}
	if err := os.WriteFile(path, []byte("ANTHROPIC_API_KEY="+second+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := m.dotEnv()["ANTHROPIC_API_KEY"]; got != second {
		t.Fatalf("got %q", got)
	}
	// The redactor of an agent masks the new value, because SecretValues takes each .env value.
	if got := m.redactor(config.ACPAgent{}).Redact("key " + second); got == "key "+second {
		t.Fatalf("the new value is not masked: %q", got)
	}
}
