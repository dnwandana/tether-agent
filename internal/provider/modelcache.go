package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
)

// modelCacheTTL is the time that a model list stays in the cache.
const modelCacheTTL = time.Hour

// ModelCache keeps the model lists of the provider entries in memory, for the
// gateway and the CLI chat. It stores a list on success only.
type ModelCache struct {
	// TTL is the time that a list stays in the cache. 0 means modelCacheTTL.
	TTL time.Duration
	// ListTimeout is the time limit of one list call. 0 means 10 seconds.
	ListTimeout time.Duration

	hc    *http.Client
	now   func() time.Time
	mu    sync.Mutex
	lists map[string]cachedList
}

// cachedList is a stored list and the time of the store.
type cachedList struct {
	list ModelList
	at   time.Time
}

// NewModelCache returns an empty cache that calls the providers through hc. It makes no call.
func NewModelCache(hc *http.Client) *ModelCache {
	return &ModelCache{hc: hc, now: time.Now, lists: map[string]cachedList{}}
}

// Get returns the model list of the entry p with the name name. It calls the
// provider when the cache has no list, or when the list is older than TTL.
func (c *ModelCache) Get(ctx context.Context, name string, p config.Provider) (ModelList, error) {
	if c == nil {
		return ListModels(ctx, name, p, nil)
	}
	ttl := c.TTL
	if ttl <= 0 {
		ttl = modelCacheTTL
	}
	key := modelCacheKey(name, p)
	c.mu.Lock()
	e, ok := c.lists[key]
	c.mu.Unlock()
	if ok && c.now().Before(e.at.Add(ttl)) {
		return e.list, nil
	}
	// The call can take as long as ListTimeout, so the mutex is free during the call. Two calls at
	// the same time can both go to the provider, and the last result stays.
	l, err := ListModelsWithin(ctx, name, p, c.hc, c.ListTimeout)
	if err != nil {
		return ModelList{}, err
	}
	c.mu.Lock()
	c.lists[key] = cachedList{list: l, at: c.now()}
	c.mu.Unlock()
	return l, nil
}

// Forget removes the list of the entry, so the next Get calls the provider.
func (c *ModelCache) Forget(name string, p config.Provider) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.lists, modelCacheKey(name, p))
	c.mu.Unlock()
}

// modelCacheKey returns the cache key of the entry. It holds a short hash of the
// API key, not the key.
func modelCacheKey(name string, p config.Provider) string {
	sum := sha256.Sum256([]byte(p.APIKey))
	return name + "\x00" + p.Format + "\x00" + p.BaseURL + "\x00" + hex.EncodeToString(sum[:])[:16]
}
