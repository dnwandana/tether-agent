package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
)

// cacheServer starts a fake provider whose list is chat-1. The status of call i is
// codes[i], and 200 after the end of codes. It counts the calls.
func cacheServer(t *testing.T, codes ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := int(calls.Add(1)) - 1
		if n < len(codes) && codes[n] != 200 {
			w.WriteHeader(codes[n])
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"chat-1"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// cacheEntry is a generic entry on the fake server.
func cacheEntry(url, key string) config.Provider {
	return config.Provider{Format: "openai", BaseURL: url, APIKey: key, Model: "chat-1"}
}

// cacheGet calls c.Get and checks the list.
func cacheGet(t *testing.T, c *ModelCache, name string, p config.Provider) {
	t.Helper()
	l, err := c.Get(ctx, name, p)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, strings.Join(l.All(), ","), "chat-1")
}

func TestModelCacheKeepsListForOneHour(t *testing.T) {
	srv, calls := cacheServer(t)
	c := NewModelCache(srv.Client())
	start := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	now := start
	c.now = func() time.Time { return now }
	p := cacheEntry(srv.URL, "k1")
	cacheGet(t, c, "primary", p)
	cacheGet(t, c, "primary", p)
	eq(t, calls.Load(), int32(1))
	now = start.Add(time.Hour - time.Second)
	cacheGet(t, c, "primary", p)
	eq(t, calls.Load(), int32(1))
	now = start.Add(time.Hour)
	cacheGet(t, c, "primary", p)
	eq(t, calls.Load(), int32(2))
}

func TestModelCacheKeyChanges(t *testing.T) {
	srv, calls := cacheServer(t)
	c := NewModelCache(srv.Client())
	p := cacheEntry(srv.URL, "k1")
	cacheGet(t, c, "primary", p)
	cacheGet(t, c, "primary", cacheEntry(srv.URL, "k2")) // a new key
	eq(t, calls.Load(), int32(2))
	cacheGet(t, c, "primary", p) // the k1 list stays
	eq(t, calls.Load(), int32(2))
	cacheGet(t, c, "backup", p) // a new entry name
	eq(t, calls.Load(), int32(3))
	q := p
	q.Format = "responses" // a new wire format
	cacheGet(t, c, "primary", q)
	eq(t, calls.Load(), int32(4))
	cacheGet(t, c, "primary", cacheEntry(srv.URL+"/", "k1")) // a new base URL
	eq(t, calls.Load(), int32(5))
}

func TestModelCacheKeyHasNoRawKey(t *testing.T) {
	p := cacheEntry("https://llm.example.com/v1", "secret-key")
	sum := sha256.Sum256([]byte("secret-key"))
	want := "primary\x00openai\x00https://llm.example.com/v1\x00" + hex.EncodeToString(sum[:])[:16]
	eq(t, modelCacheKey("primary", p), want)
	if strings.Contains(modelCacheKey("primary", p), "secret") {
		t.Fatal("the cache key holds the API key")
	}
}

func TestModelCacheSkipsFailure(t *testing.T) {
	srv, calls := cacheServer(t, 500)
	c := NewModelCache(srv.Client())
	p := cacheEntry(srv.URL, "k1")
	_, err := c.Get(ctx, "primary", p)
	var se *HTTPStatusError
	if !errors.As(err, &se) || se.Code != 500 {
		t.Fatalf("got %v, want HTTP 500", err)
	}
	cacheGet(t, c, "primary", p) // the failure is not stored
	eq(t, calls.Load(), int32(2))
	cacheGet(t, c, "primary", p)
	eq(t, calls.Load(), int32(2))
}

func TestModelCacheForget(t *testing.T) {
	srv, calls := cacheServer(t)
	c := NewModelCache(srv.Client())
	p := cacheEntry(srv.URL, "k1")
	cacheGet(t, c, "primary", p)
	c.Forget("primary", cacheEntry(srv.URL, "k2")) // another key: no change
	cacheGet(t, c, "primary", p)
	eq(t, calls.Load(), int32(1))
	c.Forget("primary", p)
	cacheGet(t, c, "primary", p)
	eq(t, calls.Load(), int32(2))
}

func TestNilModelCache(t *testing.T) {
	srv, calls := cacheServer(t)
	var c *ModelCache
	p := cacheEntry(srv.URL, "k1")
	cacheGet(t, c, "primary", p)
	cacheGet(t, c, "primary", p)
	eq(t, calls.Load(), int32(2))
	c.Forget("primary", p)
}
