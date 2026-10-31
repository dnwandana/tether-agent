package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
)

// listReq is the part of a list request that the tests check.
type listReq struct {
	method, path, query string
	header              http.Header
}

// listServer starts a fake server that stores the last request in *got and
// answers with code and body.
func listServer(t *testing.T, got *listReq, code int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got = listReq{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, header: r.Header.Clone()}
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestListHeader(t *testing.T) {
	cases := []struct {
		name string
		e    endpoint
		want map[string]string // "" means that the header is absent
	}{
		{"openai key", endpoint{format: "openai", apiKey: "sk-1"}, map[string]string{"Authorization": "Bearer sk-1", "X-Api-Key": ""}},
		{"responses key", endpoint{format: "responses", apiKey: "sk-1"}, map[string]string{"Authorization": "Bearer sk-1"}},
		{"openai no key", endpoint{format: "openai"}, map[string]string{"Authorization": ""}},
		{"anthropic key", endpoint{format: "anthropic", apiKey: "ak-1"},
			map[string]string{"X-Api-Key": "ak-1", "Anthropic-Version": "2023-06-01", "Authorization": ""}},
		{"anthropic bearer_auth", endpoint{format: "anthropic", apiKey: "mk-1", quirks: map[string]bool{"bearer_auth": true}},
			map[string]string{"Authorization": "Bearer mk-1", "X-Api-Key": "", "Anthropic-Version": "2023-06-01"}},
		{"anthropic no key", endpoint{format: "anthropic"},
			map[string]string{"X-Api-Key": "", "Authorization": "", "Anthropic-Version": "2023-06-01"}},
		{"openrouter", endpoint{format: "openai", apiKey: "or-1", quirks: map[string]bool{"openrouter_headers": true}},
			map[string]string{"Authorization": "Bearer or-1", "Http-Referer": openRouterReferer, "X-Title": openRouterTitle}},
		{"no openrouter quirk", endpoint{format: "openai"}, map[string]string{"Http-Referer": "", "X-Title": ""}},
	}
	for _, c := range cases {
		h := listHeader(c.e)
		for k, v := range c.want {
			if got := h.Get(k); got != v {
				t.Errorf("%s: %s = %q, want %q", c.name, k, got, v)
			}
		}
	}
}

func TestListModelsURL(t *testing.T) {
	var got listReq
	srv := listServer(t, &got, 200, `{"data":[{"id":"m1"}]}`)
	cases := []struct{ format, base, path, query string }{
		{"openai", srv.URL + "/v1", "/v1/models", ""},
		{"openai", srv.URL + "/v1/", "/v1/models", ""},
		{"responses", srv.URL + "/v1", "/v1/models", ""},
		{"anthropic", srv.URL, "/v1/models", "limit=1000"},
		{"anthropic", srv.URL + "/v1", "/v1/models", "limit=1000"},
		{"anthropic", srv.URL + "/v1/", "/v1/models", "limit=1000"},
		{"anthropic", srv.URL + "/anthropic", "/anthropic/v1/models", "limit=1000"},
	}
	for _, c := range cases {
		_, err := ListModels(ctx, "p", config.Provider{Format: c.format, BaseURL: c.base, APIKey: "k"}, srv.Client())
		if err != nil || got.method != http.MethodGet || got.path != c.path || got.query != c.query ||
			got.header.Get("Accept") != "application/json" {
			t.Errorf("%s %s: %+v %v", c.format, c.base, got, err)
		}
	}
}

// TestListModelsNoKeySendsNoAuth checks an entry with no key, no model, and a
// load error: the call still runs, sends no auth header, and does not read the
// key env of the preset.
func TestListModelsNoKeySendsNoAuth(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "dk-env")
	var got listReq
	srv := listServer(t, &got, 401, `{"error":"no key"}`)
	p := config.Provider{Preset: "deepseek", Format: "openai", BaseURL: srv.URL, Unusable: "no API key"}
	_, err := ListModels(ctx, "primary", p, srv.Client())
	if got.path != "/models" || got.header.Get("Authorization") != "" || got.header.Get("X-Api-Key") != "" {
		t.Fatalf("%+v", got)
	}
	eq(t, ShortCause(err), "HTTP 401")
}

func TestListModelsHTTPErrorIsRedacted(t *testing.T) {
	const key = "sk-secret-key"
	var got listReq
	srv := listServer(t, &got, 404, "no route for "+key+"\nline two")
	_, err := ListModels(ctx, "p", config.Provider{Format: "openai", BaseURL: srv.URL, APIKey: key}, srv.Client())
	var se *HTTPStatusError
	if !errors.As(err, &se) || se.Code != 404 || se.Body != "no route for [REDACTED] line two" || strings.Contains(err.Error(), key) {
		t.Fatal(err)
	}
	eq(t, ShortCause(err), "HTTP 404")
}

// TestListModelsBadBody checks that a 2xx body that is not a model list is an
// error, not a panic.
func TestListModelsBadBody(t *testing.T) {
	for _, body := range []string{"<html><body>Sign in</body></html>", `{"models":[{"id":"m1"}]}`, `"m1"`, ``} {
		var got listReq
		srv := listServer(t, &got, 200, body)
		_, err := ListModels(ctx, "p", config.Provider{Format: "openai", BaseURL: srv.URL}, srv.Client())
		if err == nil || ShortCause(err) != "error" {
			t.Errorf("%q: %v", body, err)
		}
	}
}

func TestListModelsNoConnection(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := ListModels(ctx, "p", config.Provider{Format: "openai", BaseURL: url, APIKey: "k"}, nil)
	eq(t, ShortCause(err), "no connection")
}

// listOf runs ListModels on a fake server that answers body, and returns the IDs.
func listOf(t *testing.T, body string) []string {
	t.Helper()
	var got listReq
	srv := listServer(t, &got, 200, body)
	l, err := ListModels(ctx, "p", config.Provider{Format: "openai", BaseURL: srv.URL}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Groups) != 1 || l.Groups[0].Title != "" || l.ToolsOnly || l.Search || l.At.IsZero() {
		t.Fatalf("%+v", l)
	}
	return l.Groups[0].Models
}

func TestListModelsBody(t *testing.T) {
	cases := []struct {
		body string
		want []string
	}{
		{`{"data":[{"id":"a/m1"},{"id":"b/m2"}]}`, []string{"a/m1", "b/m2"}},
		{`[{"id":"a/m1"},{"id":"b/m2"}]`, []string{"a/m1", "b/m2"}},
		{`{"data":[{"id":"a/m1"},{"name":"no id"},{"id":""},{"id":"b/m2"}]}`, []string{"a/m1", "b/m2"}},
		{`{"data":[{"id":"a/m1"},{"id":"b/m2"},{"id":"a/m1"}]}`, []string{"a/m1", "b/m2"}},
	}
	for _, c := range cases {
		if got := listOf(t, c.body); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v", c.body, got)
		}
	}
}

func TestListModelsOrder(t *testing.T) {
	cases := []struct {
		body string
		want []string
	}{
		{`{"data":[{"id":"old","created":100},{"id":"new","created":300},{"id":"mid","created":200}]}`, []string{"new", "mid", "old"}},
		{`{"data":[{"id":"old","created_at":"2025-01-02T00:00:00Z"},{"id":"new","created_at":"2026-10-01T00:00:00Z"}]}`, []string{"new", "old"}},
		{`{"data":[{"id":"z1"},{"id":"a1"},{"id":"m1"}]}`, []string{"z1", "a1", "m1"}},
		{`{"data":[{"id":"x1","created":100},{"id":"y1","created":100}]}`, []string{"x1", "y1"}},
	}
	for _, c := range cases {
		if got := listOf(t, c.body); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v", c.body, got)
		}
	}
}

func TestDecodeModelItems(t *testing.T) {
	items, err := decodeModelItems(json.RawMessage(`{"data":[{"id":"a/m1","created":5},{"id":"a/m1"},{"id":"b/m2"}]}`))
	if err != nil || len(items) != 3 || items[0].Created != 5 {
		t.Fatalf("%+v %v", items, err)
	}
	if got := uniqueIDs(items); !slices.Equal(got, []string{"a/m1", "b/m2"}) {
		t.Fatal(got)
	}
	if _, err := decodeModelItems(json.RawMessage(`{"models":[]}`)); err == nil {
		t.Fatal("an object with no data list must fail")
	}
}

func TestModelListAllAndStamp(t *testing.T) {
	l := ModelList{Groups: []ModelGroup{
		{Title: "Most used this week", Models: []string{"a/m1", "b/m2"}},
		{Title: "Best for agents", Models: []string{"c/m3"}},
	}}
	if got := l.All(); !slices.Equal(got, []string{"a/m1", "b/m2", "c/m3"}) {
		t.Fatal(got)
	}
	eq(t, l.Stamp(), "b806")
	eq(t, ModelList{Groups: []ModelGroup{{Models: []string{"a/m1", "c/m3", "b/m2"}}}}.Stamp(), "78bb")
	eq(t, ModelList{}.Stamp(), "811c")
	eq(t, len(ModelList{}.All()), 0)
}

func TestIsChatModel(t *testing.T) {
	for _, id := range []string{"text-embedding-3-large", "qwen3-embedding", "gpt-4o-mini-tts", "speech-2.6",
		"whisper-1", "gpt-4o-transcribe", "dall-e-3", "gpt-image-1", "qwen-image", "omni-moderation-latest",
		"gpt-realtime", "gpt-4o-audio-preview", "qwen3-rerank", "Text-Embedding-Ada-002", "openai/GPT-Image-1"} {
		if isChatModel(id) {
			t.Errorf("%s must be removed", id)
		}
	}
	for _, id := range []string{"gpt-6.1-sol", "deepseek-v4-pro", "z-ai/glm-5.3", "claude-sonnet-5-5",
		"MiniMax-M3", "qwen3.8-max", "kimi-k3.1-pro"} {
		if !isChatModel(id) {
			t.Errorf("%s must stay", id)
		}
	}
}

func TestListModelsRemovesNonChatModels(t *testing.T) {
	got := listOf(t, `{"data":[{"id":"gpt-6.1-sol"},{"id":"text-embedding-3-large"},{"id":"whisper-1"},`+
		`{"id":"gpt-image-1"},{"id":"gpt-6.1-mini"}]}`)
	if !slices.Equal(got, []string{"gpt-6.1-sol", "gpt-6.1-mini"}) {
		t.Fatal(got)
	}
}

func TestListModelsNoChatModels(t *testing.T) {
	for _, body := range []string{`{"data":[{"id":"text-embedding-3-large"},{"id":"gpt-4o-mini-tts"}]}`, `{"data":[]}`} {
		var got listReq
		srv := listServer(t, &got, 200, body)
		_, err := ListModels(ctx, "p", config.Provider{Format: "openai", BaseURL: srv.URL}, srv.Client())
		if !errors.Is(err, ErrNoChatModels) {
			t.Errorf("%s: %v", body, err)
		}
		eq(t, ModelListCause(err), "no chat models")
	}
}

func TestModelListCause(t *testing.T) {
	eq(t, ErrNoChatModels.Error(), "no chat models")
	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("provider p: %w", ErrNoChatModels), "no chat models"},
		{fmt.Errorf("provider p: %w: not found", &HTTPStatusError{Code: 404}), "HTTP 404"},
		{fmt.Errorf("provider p: %w: connection refused", ErrBeforeFirstByte), "no connection"},
		{errors.New("provider p: cannot decode the response"), "error"},
	}
	for _, c := range cases {
		eq(t, ModelListCause(c.err), c.want)
	}
}
