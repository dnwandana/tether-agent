package provider

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// statusServer starts a fake server that answers each request with code
// and the body "secret-account-data".
func statusServer(t *testing.T, code int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		fmt.Fprint(w, "secret-account-data")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestShortCause(t *testing.T) {
	srv := statusServer(t, 503)
	_, err := NewOpenAI("p", srv.URL, "k", "m", srv.Client()).Stream(ctx, Request{})
	if got := ShortCause(err); got != "HTTP 503" {
		t.Fatal(got, err)
	}
	if !errors.Is(err, ErrBeforeFirstByte) {
		t.Fatal(err)
	}
	// The full error text does not change.
	if !strings.Contains(err.Error(), "HTTP 503: secret-account-data") {
		t.Fatal(err)
	}
	if strings.Contains(ShortCause(err), "secret") {
		t.Fatal(ShortCause(err))
	}

	srv = statusServer(t, 429)
	_, err = NewOpenAI("p", srv.URL, "k", "m", srv.Client()).Stream(ctx, Request{})
	if got := ShortCause(err); got != "HTTP 429" {
		t.Fatal(got, err)
	}

	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := closed.URL
	closed.Close()
	_, err = NewOpenAI("p", url, "k", "m", http.DefaultClient).Stream(ctx, Request{})
	if got := ShortCause(err); got != "no connection" {
		t.Fatal(got, err)
	}
	if strings.Contains(ShortCause(err), "127.0.0.1") {
		t.Fatal(ShortCause(err))
	}

	srv = statusServer(t, 400)
	_, err = NewOpenAI("p", srv.URL, "k", "m", srv.Client()).Stream(ctx, Request{})
	if got := ShortCause(err); got != "HTTP 400" {
		t.Fatal(got, err)
	}
	if errors.Is(err, ErrBeforeFirstByte) {
		t.Fatal(err)
	}
	if !strings.Contains(err.Error(), "provider p: HTTP 400: secret-account-data") {
		t.Fatal(err)
	}

	if got := ShortCause(errors.New("x")); got != "error" {
		t.Fatal(got)
	}
	if got := ShortCause(nil); got != "error" {
		t.Fatal(got)
	}
}
