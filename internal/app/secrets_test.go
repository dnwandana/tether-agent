package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/dashboard"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/sessions"
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

// /secrets set from the dashboard opens a secret card. The answer saves the value.
func TestStartSecretDashboardCard(t *testing.T) {
	a := gatewayApp(t, "", dashOn+"exec_secrets:\n  PYPI_TOKEN:\n    commands: [\"twine upload\"]\n")
	t.Cleanup(func() { security.SetBoundSecretNames(nil) })
	ch, err := a.AddDashboard(ctx, DashboardFuncs{})
	if err != nil || ch == nil {
		t.Fatal(ch, err)
	}
	ch.SetTurnContext(ctx)
	sess := &sessions.Session{ID: "s1", Key: dashboard.SessionKey, Source: "dashboard", ChatID: dashboard.OwnerChatID}
	c := &gateway.CommandCtx{Key: dashboard.SessionKey, Owner: true, Session: sess}
	if err := a.startSecret(ctx, c, "PYPI_TOKEN"); err != nil {
		t.Fatal(err)
	}
	var card dashboard.Card
	deadline := time.Now().Add(2 * time.Second)
	for {
		if open := ch.Cards().Open(); len(open) == 1 {
			card = open[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no secret card")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if card.Kind != "secret" || card.SecretName != "PYPI_TOKEN" {
		t.Fatalf("%+v", card)
	}
	value := "pypi-AgEIcHlwaS5vcmcCJDU0ZjM4"
	if err := ch.Cards().Answer(ctx, card.ID, dashboard.CardAnswer{Value: value}); err != nil {
		t.Fatal(err)
	}
	for {
		b, _ := os.ReadFile(filepath.Join(a.DataDir, ".env"))
		if strings.Contains(string(b), "PYPI_TOKEN=") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the value is not in .env")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := a.Redactor.Redact("token " + value); got != "token «PYPI_TOKEN»" {
		t.Fatal(got)
	}
}

// With no dashboard channel, a dashboard key gets the error of every channel with no card.
func TestStartSecretDashboardOff(t *testing.T) {
	a := gatewayApp(t, "", "")
	c := &gateway.CommandCtx{Key: "dashboard:owner", Owner: true,
		Session: &sessions.Session{ID: "s1", Key: "dashboard:owner", Source: "dashboard", ChatID: "owner"}}
	if err := a.startSecret(ctx, c, "PYPI_TOKEN"); err == nil || err.Error() != "no channel can ask for the value" {
		t.Fatal(err)
	}
}
