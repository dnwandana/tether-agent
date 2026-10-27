package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// ctx is the context for the provider calls in the tests.
var ctx = context.Background()

// serveFile writes the SSE fixture at path as the response body.
func serveFile(w http.ResponseWriter, path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write(b)
}

// fixtureServer starts a fake server that answers each request with the
// SSE fixture at path.
func fixtureServer(t *testing.T, path string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { serveFile(w, path) }))
	t.Cleanup(srv.Close)
	return srv
}
