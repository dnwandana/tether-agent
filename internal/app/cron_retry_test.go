package app

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// cronTurn returns the turn error with its wraps, so the cron retry can find the HTTP status
// and the connection error.
func TestCronTurnKeepsProviderErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	dir := t.TempDir()
	writeConfig(t, dir, srv.URL)
	a, err := New(ctx, Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	sess, err := a.Store.Create(ctx, sessions.Session{Source: "cron", Kind: "cron", Key: "cron:j1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.cronTurn(ctx, cron.CronTurn{Session: sess, Text: "report"})
	var se *provider.HTTPStatusError
	if !errors.As(err, &se) || se.Code != 503 {
		t.Fatal(err)
	}
	srv.Close()
	if _, err = a.cronTurn(ctx, cron.CronTurn{Session: sess, Text: "report"}); !errors.Is(err, provider.ErrConnection) {
		t.Fatal(err)
	}
}
