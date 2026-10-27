package pricing

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/provider"
)

type overrides map[string]config.PriceOverride

// fakeSource is a price server that counts its requests.
type fakeSource struct {
	srv  *httptest.Server
	hits atomic.Int32
	// inm holds the last If-None-Match header.
	inm atomic.Value
}

// sourceServer serves body. When etag is set, it answers 304 to a matching If-None-Match.
func sourceServer(t *testing.T, body []byte, etag string) *fakeSource {
	t.Helper()
	fs := &fakeSource{}
	fs.inm.Store("")
	fs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.hits.Add(1)
		fs.inm.Store(r.Header.Get("If-None-Match"))
		if r.Header.Get("Authorization") != "" {
			t.Error("a price fetch must not send credentials")
		}
		if etag != "" {
			w.Header().Set("ETag", etag)
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		w.Write(body)
	}))
	t.Cleanup(fs.srv.Close)
	return fs
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func openDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// testProviders is a provider list that wants OpenRouter and LiteLLM data.
func testProviders() map[string]config.Provider {
	return map[string]config.Provider{
		"primary":  {BaseURL: "https://api.z.ai/api/coding/paas/v4", Model: "glm-4.7"},
		"fallback": {BaseURL: "https://openrouter.ai/api/v1", Model: "z-ai/glm-4.7"},
	}
}

// svcFixture is a Service over fake sources.
type svcFixture struct {
	s        *Service
	or, lite *fakeSource
	cacheDir string
	db       *db.DB
}

func newFixture(t *testing.T, ov overrides, sources []string) *svcFixture {
	t.Helper()
	cfg := config.Default()
	cfg.Providers = testProviders()
	cfg.Pricing.Overrides = ov
	cfg.Pricing.Sources = sources
	f := &svcFixture{
		or:       sourceServer(t, readFile(t, "testdata/openrouter_models.json"), ""),
		lite:     sourceServer(t, readFile(t, "testdata/litellm_sample.json"), `"v1"`),
		cacheDir: t.TempDir(),
		db:       openDB(t),
	}
	f.s = NewService(&cfg, f.cacheDir, nil, f.db, nil)
	f.s.orURL, f.s.liteURL = f.or.srv.URL, f.lite.srv.URL
	return f
}

// serviceWith returns a Service with the overrides and the fixture data loaded.
func serviceWith(t *testing.T, ov overrides) *Service {
	t.Helper()
	f := newFixture(t, ov, []string{"openrouter", "litellm"})
	f.s.refresh(ctx)
	return f.s
}

// newSvc returns a Service with no data and no sources.
func newSvc(t *testing.T) *Service {
	t.Helper()
	cfg := config.Default()
	cfg.Providers = testProviders()
	return NewService(&cfg, t.TempDir(), nil, nil, nil)
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

func TestOrderOverridesThenOpenRouterThenLiteLLM(t *testing.T) {
	s := serviceWith(t, overrides{"z-ai/glm-4.7": {Input: 1, Output: 2}})
	p, ok := s.Lookup("fallback", config.Provider{BaseURL: "https://openrouter.ai/api/v1", Model: "z-ai/glm-4.7"})
	if !ok || p.InputPerToken != 1e-6 || p.OutputPerToken != 2e-6 {
		t.Fatal("override per 1M → per token", p)
	}
	s = serviceWith(t, nil)
	p, ok = s.Lookup("fallback", config.Provider{BaseURL: "https://openrouter.ai/api/v1", Model: "z-ai/glm-4.7"})
	if !ok || p.InputPerToken != 0.0000006 || p.ContextWindow != 202752 {
		t.Fatal("OpenRouter next", p)
	}
	p, ok = s.Lookup("primary", config.Provider{BaseURL: "https://api.z.ai/api/coding/paas/v4", Model: "glm-4.7"})
	if !ok || p.ContextWindow != 200000 || !near(p.OutputPerToken, 2.2e-6) {
		t.Fatal("LiteLLM last", p)
	}
}

func TestOverrideByPriceKey(t *testing.T) {
	s := newSvc(t)
	s.cfg.Pricing.Overrides = map[string]config.PriceOverride{"my-key": {Input: 3}}
	p, ok := s.Lookup("primary", config.Provider{Model: "x", PriceKey: "my-key"})
	if !ok || p.InputPerToken != 3e-6 {
		t.Fatal(p)
	}
}

func TestOpenRouterOnlyForOpenRouterHost(t *testing.T) {
	s := serviceWith(t, nil)
	// z-ai/glm-4.7 is in the OpenRouter data, but a z.ai provider never reads it.
	if p, ok := s.Lookup("primary", config.Provider{BaseURL: "https://api.z.ai/v4", Model: "z-ai/glm-4.7"}); ok {
		t.Fatal(p)
	}
}

func TestFlatRateCostZero(t *testing.T) {
	usd, known, flat := newSvc(t).Cost("primary", config.Provider{FlatRate: true}, provider.Usage{InputTokens: 1000})
	if !flat || known || usd != 0 {
		t.Fatal()
	}
}

func TestUnknownCost(t *testing.T) {
	usd, known, flat := newSvc(t).Cost("primary", config.Provider{Model: "nothing"}, provider.Usage{InputTokens: 1000})
	if flat || known || usd != 0 {
		t.Fatal()
	}
}

func TestCacheReadPriceUsed(t *testing.T) {
	s := serviceWith(t, nil)
	p := config.Provider{BaseURL: "https://api.z.ai/v4", Model: "glm-4.7"}
	usd, known, flat := s.Cost("primary", p, provider.Usage{InputTokens: 1000, OutputTokens: 100, CacheReadTokens: 10000})
	want := 1000*6e-7 + 100*2.2e-6 + 10000*1.1e-7
	if !known || flat || !near(usd, want) {
		t.Fatal(usd, want)
	}
	// With no cache-read price, the cache reads use the input price.
	s.cfg.Pricing.Overrides = map[string]config.PriceOverride{"glm-4.7": {Input: 1, Output: 2}}
	usd, _, _ = s.Cost("primary", p, provider.Usage{CacheReadTokens: 1000000})
	if !near(usd, 1) {
		t.Fatal(usd)
	}
}

func TestContextWindowOrder(t *testing.T) {
	s := serviceWith(t, overrides{"glm-4.7": {Input: 1}})
	p := config.Provider{BaseURL: "https://api.z.ai/v4", Model: "glm-4.7"}
	if w := s.ContextWindow("primary", p); w != 200000 {
		t.Fatal("an override has no window; the data gives it:", w)
	}
	p.ContextWindow = 64000
	if w := s.ContextWindow("primary", p); w != 64000 {
		t.Fatal("the config wins", w)
	}
	if w := s.ContextWindow("primary", config.Provider{Model: "unknown"}); w != 128000 {
		t.Fatal("default", w)
	}
}

func TestOfflineUsesCachedFile(t *testing.T) {
	f := newFixture(t, nil, []string{"openrouter", "litellm"})
	f.s.refresh(ctx)
	if _, err := os.Stat(filepath.Join(f.cacheDir, "prices.json")); err != nil {
		t.Fatal(err)
	}
	// A new service with the servers down reads the cache file.
	f.or.srv.Close()
	f.lite.srv.Close()
	cfg := *f.s.cfg
	s2 := NewService(&cfg, f.cacheDir, nil, f.db, nil)
	s2.orURL, s2.liteURL = f.or.srv.URL, f.lite.srv.URL
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s2.Start(cctx)
	for _, c := range []struct {
		name string
		p    config.Provider
	}{
		{"primary", config.Provider{BaseURL: "https://api.z.ai/v4", Model: "glm-4.7"}},
		{"fallback", config.Provider{BaseURL: "https://openrouter.ai/api/v1", Model: "z-ai/glm-4.7"}},
	} {
		if _, ok := s2.Lookup(c.name, c.p); !ok {
			t.Fatal("cached price missing for", c.name)
		}
	}
	// The failed refresh keeps the cached data.
	time.Sleep(100 * time.Millisecond)
	if _, ok := s2.Lookup("primary", config.Provider{BaseURL: "https://api.z.ai/v4", Model: "glm-4.7"}); !ok {
		t.Fatal("a failed refresh must keep the data")
	}
}

func TestCacheHoldsWantedModelsOnly(t *testing.T) {
	f := newFixture(t, nil, []string{"openrouter", "litellm"})
	f.s.refresh(ctx)
	var c struct {
		OpenRouter map[string]json.RawMessage `json:"openrouter"`
		LiteLLM    map[string]json.RawMessage `json:"litellm"`
		FetchedAt  time.Time                  `json:"fetched_at"`
	}
	if err := json.Unmarshal(readFile(t, filepath.Join(f.cacheDir, "prices.json")), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.OpenRouter) != 1 || c.OpenRouter["z-ai/glm-4.7"] == nil || len(c.LiteLLM) != 1 || c.LiteLLM["zai/glm-4.7"] == nil || c.FetchedAt.IsZero() {
		t.Fatalf("%+v", c)
	}
}

func TestSourcesEmptyNeverFetches(t *testing.T) {
	f := newFixture(t, nil, []string{})
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	f.s.Start(cctx)
	f.s.refresh(ctx)
	time.Sleep(50 * time.Millisecond)
	if f.or.hits.Load() != 0 || f.lite.hits.Load() != 0 {
		t.Fatal("fetch with sources: []")
	}
}

func TestOnlyListedSourceFetched(t *testing.T) {
	f := newFixture(t, nil, []string{"litellm"})
	f.s.refresh(ctx)
	if f.or.hits.Load() != 0 || f.lite.hits.Load() != 1 {
		t.Fatal(f.or.hits.Load(), f.lite.hits.Load())
	}
}

func TestStartDoesNotBlock(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = testProviders()
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(block); srv.Close() })
	s := NewService(&cfg, t.TempDir(), nil, nil, nil)
	s.orURL, s.liteURL = srv.URL, srv.URL
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	start := time.Now()
	s.Start(cctx)
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("Start blocks")
	}
}

func TestETagStoredInKV(t *testing.T) {
	f := newFixture(t, nil, []string{"litellm"})
	f.s.refresh(ctx)
	if v, ok, _ := f.db.KVGet(ctx, "pricing.litellm.etag"); !ok || v != `"v1"` {
		t.Fatal(v, ok)
	}
	if f.lite.inm.Load().(string) != "" {
		t.Fatal("the first fetch has no ETag")
	}
	f.s.refresh(ctx)
	if f.lite.inm.Load().(string) != `"v1"` {
		t.Fatal("the next refresh must send If-None-Match")
	}
	// A 304 keeps the data.
	if _, ok := f.s.Lookup("primary", config.Provider{BaseURL: "https://api.z.ai/v4", Model: "glm-4.7"}); !ok {
		t.Fatal("304 must keep the data")
	}
}

func TestNewModelSkipsETag(t *testing.T) {
	// After a config change, the cached subset lacks the new model, so the refresh must
	// download the full file again.
	f := newFixture(t, nil, []string{"litellm"})
	f.s.refresh(ctx)
	f.s.cfg.Providers["cheap"] = config.Provider{BaseURL: "https://api.openai.com/v1", Model: "gpt-4o-mini"}
	f.s.refresh(ctx)
	if f.lite.inm.Load().(string) != "" {
		t.Fatal("a changed model list must not send If-None-Match")
	}
	if _, ok := f.s.Lookup("cheap", f.s.cfg.Providers["cheap"]); !ok {
		t.Fatal("the new model must be found")
	}
}
