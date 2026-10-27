package pricing

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"
)

var ctx = context.Background()

// fileServer serves the content of path for every request.
func fileServer(t *testing.T, path string) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// etagServer serves body with the given ETag, and answers 304 when the
// request sends the same ETag in If-None-Match. It also fails the test when
// the request carries an Authorization header.
func etagServer(t *testing.T, path, etag string) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return etagServerBody(t, body, etag)
}

func etagServerBody(t *testing.T, body []byte, etag string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("the LiteLLM fetch must not send credentials")
		}
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOpenRouterParse(t *testing.T) {
	srv := fileServer(t, "testdata/openrouter_models.json")
	m, err := FetchOpenRouter(ctx, srv.Client(), srv.URL)
	p := m["z-ai/glm-4.7"]
	if err != nil || p.InputPerToken != 0.0000006 || p.OutputPerToken != 0.0000022 || p.CacheReadPerToken != 0.00000011 || p.ContextWindow != 202752 {
		t.Fatal(p, err)
	}
	if free, ok := m["meta-llama/llama-3.3-70b-instruct:free"]; !ok || free.InputPerToken != 0 || free.ContextWindow != 65536 {
		t.Fatal("free model", free, ok)
	}
	if _, ok := m["openrouter/auto"]; ok {
		t.Fatal("a model with a negative (variable) price must be skipped")
	}
}

func TestOpenRouterBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if _, err := FetchOpenRouter(ctx, srv.Client(), srv.URL); err == nil {
		t.Fatal("a 503 must give an error")
	}
}

func TestLiteLLMKeepsOnlyWanted(t *testing.T) {
	srv := etagServer(t, "testdata/litellm_sample.json", `"abc"`)
	m, tag, nm, err := FetchLiteLLM(ctx, srv.Client(), srv.URL, "", func(k string) bool { return k == "zai/glm-4.7" })
	if err != nil || nm || tag != `"abc"` || len(m) != 1 || m["zai/glm-4.7"].ContextWindow == 0 {
		t.Fatal(m, tag, err)
	}
	p := m["zai/glm-4.7"]
	if p.InputPerToken != 6e-07 || p.OutputPerToken != 2.2e-06 || p.CacheReadPerToken != 1.1e-07 || p.ContextWindow != 200000 {
		t.Fatal(p)
	}
}

func TestLiteLLM304(t *testing.T) {
	srv := etagServer(t, "testdata/litellm_sample.json", `"abc"`)
	_, _, nm, err := FetchLiteLLM(ctx, srv.Client(), srv.URL, `"abc"`, func(string) bool { return true })
	if err != nil || !nm {
		t.Fatal("If-None-Match must give 304")
	}
}

func TestLiteLLMAllWanted(t *testing.T) {
	srv := etagServer(t, "testdata/litellm_sample.json", `"abc"`)
	m, _, _, err := FetchLiteLLM(ctx, srv.Client(), srv.URL, "", func(string) bool { return true })
	if err != nil || len(m) != 4 {
		t.Fatal(len(m), err)
	}
}

func TestLiteLLMSkipsSampleSpec(t *testing.T) {
	srv := etagServer(t, "testdata/litellm_sample.json", `"abc"`)
	m, _, _, err := FetchLiteLLM(ctx, srv.Client(), srv.URL, "", func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m["sample_spec"]; ok {
		t.Fatal("sample_spec must not be returned")
	}
}

func TestLiteLLMBadJSON(t *testing.T) {
	srv := etagServerBody(t, []byte(`{"a": {"input_cost_per_token": 1e-6}, "b": `), `"x"`)
	if _, _, _, err := FetchLiteLLM(ctx, srv.Client(), srv.URL, "", func(string) bool { return true }); err == nil {
		t.Fatal("a cut JSON body must give an error")
	}
}

// syntheticLiteLLM returns a LiteLLM-shaped body of at least size bytes.
func syntheticLiteLLM(size int) []byte {
	var b bytes.Buffer
	b.WriteString("{")
	for i := 0; b.Len() < size; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"vendor/model-%d":{"input_cost_per_token":1e-06,"output_cost_per_token":2e-06,`+
			`"cache_read_input_token_cost":1e-07,"litellm_provider":"vendor","max_input_tokens":128000,`+
			`"max_output_tokens":8192,"max_tokens":8192,"mode":"chat","supports_function_calling":true,`+
			`"supports_vision":false,"source":"https://example.com/pricing/model-%d"}`, i, i)
	}
	b.WriteString("}")
	return b.Bytes()
}

func TestLiteLLMStreamsWithoutFullDecode(t *testing.T) {
	body := syntheticLiteLLM(3 << 20)
	srv := etagServerBody(t, body, `"big"`)
	hc := srv.Client()

	// Warm up the client connection, so its buffers are in the baseline.
	if _, _, _, err := FetchLiteLLM(ctx, hc, srv.URL, `"big"`, func(string) bool { return false }); err != nil {
		t.Fatal(err)
	}

	var ms runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms)
	base := ms.HeapAlloc
	var peak uint64
	n := 0
	want := func(k string) bool {
		n++
		if n%1000 == 0 {
			runtime.GC()
			runtime.ReadMemStats(&ms)
			peak = max(peak, ms.HeapAlloc)
		}
		return k == "vendor/model-7"
	}
	m, _, _, err := FetchLiteLLM(ctx, hc, srv.URL, "", want)
	if err != nil || len(m) != 1 {
		t.Fatal(len(m), err)
	}
	if n < 5000 {
		t.Fatalf("only %d keys seen", n)
	}
	runtime.GC()
	runtime.ReadMemStats(&ms)
	after := ms.HeapAlloc
	const limit = 1 << 20
	if peak > base && peak-base > limit {
		t.Fatalf("live heap grew by %d bytes during the parse; limit is %d", peak-base, limit)
	}
	if after > base && after-base > limit {
		t.Fatalf("live heap grew by %d bytes after the parse; limit is %d", after-base, limit)
	}
	t.Logf("body %d bytes, keys %d, peak growth %d bytes", len(body), n, int64(peak)-int64(base))
}
