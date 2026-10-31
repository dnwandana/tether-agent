package provider

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// bodyServer starts a fake server that answers each request with code and
// body.
func bodyServer(t *testing.T, code int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTriggerCodes(t *testing.T) {
	cases := []struct {
		code    int
		body    string
		trigger bool
	}{
		{503, "", true}, {429, "", true}, {401, "bad key", true}, {402, "no credit", true},
		{403, "plan denied", true}, {408, "timeout", true},
		{400, `{"error":"Your credit balance is too low"}`, true},
		{400, `{"code":"Arrearage"}`, true},
		{400, "you hit your SPEND LIMIT", true},
		{400, "insufficient balance", true}, {400, "insufficient_quota", true},
		{400, "bad request", false}, {404, "", false}, {413, "", false}, {422, "", false},
	}
	for _, c := range cases {
		srv := bodyServer(t, c.code, c.body)
		_, err := NewOpenAI("p", srv.URL, "k", "m", srv.Client()).Stream(ctx, Request{})
		if got := errors.Is(err, ErrBeforeFirstByte); got != c.trigger {
			t.Errorf("%d %q: trigger %v, err %v", c.code, c.body, got, err)
		}
	}
}

func TestHTTPStatusErrorBodyIsRedacted(t *testing.T) {
	const key = "sk-secret-key"
	srv := bodyServer(t, 400, "bad key "+key+"\nline two")
	_, err := NewOpenAI("p", srv.URL, key, "m", srv.Client()).Stream(ctx, Request{})
	var se *HTTPStatusError
	if !errors.As(err, &se) {
		t.Fatal(err)
	}
	if strings.Contains(se.Body, key) || se.Body != "bad key [REDACTED] line two" {
		t.Fatalf("%q", se.Body)
	}
	if se.Error() != "HTTP 400" {
		t.Fatal(se.Error())
	}
}
