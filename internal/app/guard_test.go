package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
)

func TestGateGuardDefault(t *testing.T) {
	a := newApp(t)
	g, ok := a.Gate.Guard.(security.SendGuard)
	if !ok {
		t.Fatalf("guard %#v", a.Gate.Guard)
	}
	if g.Prov != a.Prov || g.Record == nil || g.Timeout != 0 {
		t.Fatalf("%+v", g)
	}
}

func TestGateGuardOff(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, fakeProviderURL(t), "outbound:\n  guard: false\n")
	a, err := New(ctx, Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	if a.Gate.Guard != nil {
		t.Fatalf("outbound.guard: false must leave no guard: %#v", a.Gate.Guard)
	}
}

func TestGateGuardProviderEntry(t *testing.T) {
	dir := t.TempDir()
	url := fakeProviderURL(t)
	cfg := "providers:\n" +
		"  primary:\n    format: openai\n    base_url: " + url + "\n    api_key: k\n    model: m\n" +
		"  fast:\n    format: openai\n    base_url: " + url + "\n    api_key: k\n    model: f\n" +
		"pricing:\n  sources: []\n" +
		"outbound:\n  guard_provider: fast\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := New(ctx, Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	g, ok := a.Gate.Guard.(security.SendGuard)
	if !ok {
		t.Fatalf("guard %#v", a.Gate.Guard)
	}
	ch, ok := g.Prov.(*provider.Chain)
	if !ok || ch.Name() != "fast" || g.Record == nil {
		t.Fatalf("guard provider %#v", g.Prov)
	}
}
