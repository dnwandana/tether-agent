package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

// A transport failure wraps ErrConnection and ErrBeforeFirstByte. A bad status and an
// unusable entry do not wrap ErrConnection.
func TestConnectionErrorIsErrConnection(t *testing.T) {
	srv := bodyServer(t, 503, "down")
	_, err := NewOpenAI("p", srv.URL, "k", "m", srv.Client()).Stream(ctx, Request{})
	if !errors.Is(err, ErrBeforeFirstByte) || errors.Is(err, ErrConnection) {
		t.Fatal(err)
	}
	url := srv.URL
	srv.Close()
	_, err = NewOpenAI("p", url, "k", "m", http.DefaultClient).Stream(ctx, Request{})
	if !errors.Is(err, ErrBeforeFirstByte) || !errors.Is(err, ErrConnection) {
		t.Fatal(err)
	}
	u := &unusable{name: "x", reason: "no key"}
	if _, err := u.Stream(ctx, Request{}); errors.Is(err, ErrConnection) {
		t.Fatal(err)
	}
}

// retryNow is the fixed clock of the parser tests.
var retryNow = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

// hdr returns a header with the pairs k1, v1, k2, v2, and so on.
func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		name string
		h    http.Header
		want time.Duration
	}{
		{"ms", hdr("retry-after-ms", "1500"), 1500 * time.Millisecond},
		{"ms before seconds", hdr("retry-after-ms", "1500", "Retry-After", "9"), 1500 * time.Millisecond},
		{"bad ms, then seconds", hdr("retry-after-ms", "abc", "Retry-After", "7"), 7 * time.Second},
		{"seconds", hdr("Retry-After", "5"), 5 * time.Second},
		{"long seconds", hdr("Retry-After", "120"), 120 * time.Second},
		{"date", hdr("Retry-After", retryNow.Add(30*time.Second).Format(http.TimeFormat)), 30 * time.Second},
		{"past date", hdr("Retry-After", retryNow.Add(-time.Minute).Format(http.TimeFormat)), 0},
		{"bad", hdr("Retry-After", "soon"), 0},
		{"nan", hdr("Retry-After", "NaN"), 0},
		{"zero", hdr("Retry-After", "0"), 0},
		{"negative", hdr("Retry-After", "-5"), 0},
		{"absent", hdr(), 0},
	}
	for _, c := range cases {
		if got := parseRetryAfter(c.h, retryNow); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// A huge value must not overflow into a negative wait.
func TestParseRetryAfterHugeValue(t *testing.T) {
	for _, h := range []http.Header{
		hdr("retry-after-ms", "9223372036854775807"),
		hdr("retry-after-ms", "99999999999999999999999"),
		hdr("Retry-After", "9223372036854775807"),
		hdr("Retry-After", "99999999999999999999999"),
		hdr("Retry-After", "1e300"),
		hdr("Retry-After", time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC).Format(http.TimeFormat)),
	} {
		if got := parseRetryAfter(h, retryNow); got != maxRetryAfter || got <= 0 {
			t.Errorf("%v: got %v, want %v", h, got, maxRetryAfter)
		}
	}
}

func TestParseProviderCode(t *testing.T) {
	cases := []struct{ body, want string }{
		{`{"error":{"code":"1302","message":"Rate limit reached"}}`, "1302"},
		{`{"error":{"code":1308,"message":"Usage limit reached"}}`, "1308"},
		{`{ "error" : { "code" : "1305" , "message" : "busy" } }`, "1305"},
		{`upstream error: {"code" :  1305 , "msg": "busy"`, "1305"},
		{`{"error":{"code":"1310","message":"Weekly/Monthly Limit Exhausted. Your limit will reset at 2026-12-01 00:`, "1310"},
		{`{"code":"1113","msg":"Insufficient balance"}`, "1113"},
		{`{"error":{"code":"rate_limit_exceeded"}}`, ""},
		{`{"error":{"code":null}}`, ""},
		{`{"code": 12345}`, ""},
		{`{"error":{"message":"bad"}}`, ""},
		{`plain text`, ""},
	}
	for _, c := range cases {
		if got := parseProviderCode([]byte(c.body)); got != c.want {
			t.Errorf("%q: got %q, want %q", c.body, got, c.want)
		}
	}
}

func TestParseResetAt(t *testing.T) {
	cases := []struct {
		msg  string
		want time.Time
	}{
		{"Usage limit reached for 5 hour. Your limit will reset at 2026-11-25 18:40:00",
			time.Date(2026, 11, 25, 10, 40, 0, 0, time.UTC)},
		{`{"error":{"code":"1308","message":"Your limit will reset at 2026-11-25 18:40:00"}}`,
			time.Date(2026, 11, 25, 10, 40, 0, 0, time.UTC)},
		{"Resets at 2026-11-25T12:00:00Z.", time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)},
		{"Resets at 2026-11-25T20:00:00+08:00", time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)},
		{"Resets at 2026-11-25 10:40:00 UTC", time.Date(2026, 11, 25, 10, 40, 0, 0, time.UTC)},
		{"Resets at 2026-11-25 10:40:00 GMT.", time.Date(2026, 11, 25, 10, 40, 0, 0, time.UTC)},
		{"Resets at 2026-11-25 18:40:00 +08:00", time.Date(2026, 11, 25, 10, 40, 0, 0, time.UTC)},
		{"Resets at 2026-11-25 10:40:00+0000", time.Date(2026, 11, 25, 10, 40, 0, 0, time.UTC)},
		{"Resets at 2026-11-25 18:40:00. Try again later", time.Date(2026, 11, 25, 10, 40, 0, 0, time.UTC)},
		{"Resets at 2026-11-25 18:40:00 (Beijing time)", time.Date(2026, 11, 25, 10, 40, 0, 0, time.UTC)},
		{"Resets at 2026-11-25 18:40:00 CST", time.Time{}}, // a zone name that is not known
		{fmt.Sprintf("Your limit will reset at %d", retryNow.Add(time.Hour).Unix()), retryNow.Add(time.Hour)},
		{fmt.Sprintf("Resets at %d.", retryNow.Add(2*time.Hour).UnixMilli()), retryNow.Add(2 * time.Hour)},
		{"Your limit will reset soon", time.Time{}},
		{"Resets at tomorrow", time.Time{}},
		{"Resets at 2026-11-25 08:00:00", time.Time{}}, // in the past
		{"Resets at 2026-12-10 00:00:00", time.Time{}}, // more than 8 days ahead
		{"Rate limit reached", time.Time{}},
	}
	for _, c := range cases {
		if got := parseResetAt(c.msg, retryNow); !got.Equal(c.want) {
			t.Errorf("%q: got %v, want %v", c.msg, got, c.want)
		}
	}
}

// postJSON fills the retry fields of the status error from the header and the raw body.
func TestStatusErrorRetryFields(t *testing.T) {
	reset := time.Now().Add(2 * time.Hour).Unix()
	body := fmt.Sprintf(`{"error":{"code":"1308","message":"Usage limit reached. Your limit will reset at %d"}}`, reset)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(429)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	_, err := NewOpenAI("p", srv.URL, "k", "m", srv.Client()).Stream(ctx, Request{})
	se, ok := errors.AsType[*HTTPStatusError](err)
	if !ok || !errors.Is(err, ErrBeforeFirstByte) {
		t.Fatal(err)
	}
	if se.Code != 429 || se.RetryAfter != 30*time.Second || se.ProviderCode != "1308" || se.ResetAt.Unix() != reset {
		t.Fatalf("%+v", se)
	}
	// A body with no code and no reset time leaves the fields empty.
	srv2 := bodyServer(t, 503, "down")
	_, err = NewOpenAI("p", srv2.URL, "k", "m", srv2.Client()).Stream(ctx, Request{})
	se, ok = errors.AsType[*HTTPStatusError](err)
	if !ok || se.RetryAfter != 0 || se.ProviderCode != "" || !se.ResetAt.IsZero() {
		t.Fatalf("%+v %v", se, err)
	}
}

// A server that never sends headers gives ErrHeaderTimeout. A refused connection and the end
// of the context do not.
func TestHeaderTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	const key = "sk-secret-key"
	hc := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 50 * time.Millisecond}}
	_, err := NewOpenAI("p", srv.URL, key, "m", hc).Stream(ctx, Request{})
	if !errors.Is(err, ErrHeaderTimeout) || !errors.Is(err, ErrBeforeFirstByte) || !errors.Is(err, ErrConnection) {
		t.Fatal(err)
	}
	if !strings.Contains(err.Error(), "timeout awaiting response headers") || strings.Contains(err.Error(), key) {
		t.Fatal(err)
	}
	// The raw transport error is not wrapped, because its URL can hold a key.
	if _, ok := errors.AsType[net.Error](err); ok {
		t.Fatal("the transport error is wrapped:", err)
	}
	// A refused connection.
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	if _, err := NewOpenAI("p", url, "k", "m", hc).Stream(ctx, Request{}); errors.Is(err, ErrHeaderTimeout) || !errors.Is(err, ErrConnection) {
		t.Fatal(err)
	}
	// The end of the context.
	slow := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 10 * time.Second}}
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := NewOpenAI("p", srv.URL, "k", "m", slow).Stream(cctx, Request{}); errors.Is(err, ErrHeaderTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
