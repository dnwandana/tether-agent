package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/provider"
)

// DefaultContextWindow is the context window when no source gives one.
const DefaultContextWindow = 128000

// The source names of pricing.sources.
const (
	SourceOpenRouter = "openrouter"
	SourceLiteLLM    = "litellm"
)

// kvLiteLLMETag is the kv key of the ETag of the last LiteLLM download.
const kvLiteLLMETag = "pricing.litellm.etag"

// cacheFileName is the name of the price cache in the cache folder.
const cacheFileName = "prices.json"

// perMillion converts a price in USD per 1M tokens to USD per token.
const perMillion = 1e6

// Service looks up the prices and the context windows of the configured models.
// It is safe for concurrent use.
type Service struct {
	cfg      *config.Config
	cacheDir string
	hc       *http.Client
	db       *db.DB
	log      *slog.Logger
	// orURL and liteURL are the source URLs. Tests change them.
	orURL, liteURL string

	mu sync.RWMutex
	or map[string]Price
	// lite holds the LiteLLM entries of the models in liteFor.
	lite      map[string]Price
	liteFor   []string
	fetchedAt time.Time
	warned    map[string]bool
	// refreshMu makes the refreshes run one at a time.
	refreshMu sync.Mutex
}

// NewService returns a Service. It does not read or fetch data until Start. A nil hc uses
// http.DefaultClient. A nil db does not store the ETag. A nil log uses slog.Default.
func NewService(cfg *config.Config, cacheDir string, hc *http.Client, db *db.DB, log *slog.Logger) *Service {
	if hc == nil {
		hc = http.DefaultClient
	}
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		cfg: cfg, cacheDir: cacheDir, hc: hc, db: db, log: log,
		orURL: OpenRouterURL, liteURL: LiteLLMURL,
		warned: map[string]bool{},
	}
}

// Start loads the cache file, then refreshes the data in a goroutine: at once, and then every
// pricing.refresh_hours, until ctx ends. It never waits for the network. With
// pricing.sources set to [], it never fetches.
func (s *Service) Start(ctx context.Context) {
	if err := s.loadCache(); err != nil {
		s.log.Warn("pricing: cannot read the price cache; the prices are not known until the next refresh",
			"file", s.cachePath(), "err", err.Error())
	}
	if len(s.cfg.Pricing.Sources) == 0 {
		return
	}
	every := time.Duration(s.cfg.Pricing.RefreshHours) * time.Hour
	if every <= 0 {
		every = time.Duration(config.Default().Pricing.RefreshHours) * time.Hour
	}
	go func() {
		s.refresh(ctx)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.refresh(ctx)
			}
		}
	}()
}

// Lookup returns the price of the model of p. The order is pricing.overrides, then the
// OpenRouter data (only for an OpenRouter base URL), then the LiteLLM data.
func (s *Service) Lookup(providerName string, p config.Provider) (Price, bool) {
	if prices := s.sources(providerName, p); len(prices) > 0 {
		return prices[0], true
	}
	return Price{}, false
}

// Cost returns the cost in USD of one response. flat is true for a provider with
// flat_rate. known is false when the price is not known or the provider is flat. The
// cache-read tokens use the cache-read price when it is known, else the input price.
func (s *Service) Cost(providerName string, p config.Provider, u provider.Usage) (usd float64, known bool, flat bool) {
	if p.FlatRate {
		return 0, false, true
	}
	pr, ok := s.Lookup(providerName, p)
	if !ok {
		return 0, false, false
	}
	cache := pr.CacheReadPerToken
	if cache == 0 {
		cache = pr.InputPerToken
	}
	usd = float64(u.InputTokens)*pr.InputPerToken + float64(u.OutputTokens)*pr.OutputPerToken +
		float64(u.CacheReadTokens)*cache
	return usd, true, false
}

// ContextWindow returns the context window of the model of p. The order is the
// context_window of the config, then the price data, then DefaultContextWindow.
func (s *Service) ContextWindow(providerName string, p config.Provider) int {
	if p.ContextWindow > 0 {
		return p.ContextWindow
	}
	for _, pr := range s.sources(providerName, p) {
		if pr.ContextWindow > 0 {
			return pr.ContextWindow
		}
	}
	return DefaultContextWindow
}

// sources returns the prices of p from each source that knows the model, in lookup order.
// It logs one warning for a model that no source knows, after the data is loaded.
func (s *Service) sources(providerName string, p config.Provider) []Price {
	var out []Price
	for _, k := range priceKeys(p) {
		if o, ok := s.cfg.Pricing.Overrides[k]; ok {
			out = append(out, Price{InputPerToken: o.Input / perMillion, OutputPerToken: o.Output / perMillion,
				CacheReadPerToken: o.CacheRead / perMillion})
			break
		}
	}
	s.mu.RLock()
	or, lite := s.or, s.lite
	s.mu.RUnlock()
	if isOpenRouter(p.BaseURL) {
		for _, k := range priceKeys(p) {
			if pr, ok := or[k]; ok {
				out = append(out, pr)
				break
			}
		}
	}
	pr, key, ambiguous := matchLiteLLM(lite, p)
	if key != "" {
		out = append(out, pr)
	}
	switch {
	case ambiguous:
		s.warnOnce(providerName, p, "pricing: more than one LiteLLM key ends in the model name; set price_key for the provider")
	case len(out) == 0 && lite != nil:
		s.warnOnce(providerName, p, "pricing: the price of the model is not known; set price_key for the provider to a LiteLLM key")
	}
	return out
}

// warnOnce logs msg one time for each provider and model.
func (s *Service) warnOnce(providerName string, p config.Provider, msg string) {
	id := providerName + "\x00" + p.Model
	s.mu.Lock()
	seen := s.warned[id]
	s.warned[id] = true
	s.mu.Unlock()
	if !seen {
		s.log.Warn(msg, "provider", providerName, "model", p.Model)
	}
}

// refresh fetches each source of pricing.sources once, keeps the data of the configured
// models, and writes the cache file. A failed source keeps its earlier data.
func (s *Service) refresh(ctx context.Context) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	provs := s.providers()
	if len(provs) == 0 {
		return
	}
	changed := false
	if slices.Contains(s.cfg.Pricing.Sources, SourceOpenRouter) && s.refreshOpenRouter(ctx, provs) {
		changed = true
	}
	if slices.Contains(s.cfg.Pricing.Sources, SourceLiteLLM) && s.refreshLiteLLM(ctx, provs) {
		changed = true
	}
	if !changed {
		return
	}
	s.mu.Lock()
	s.fetchedAt = time.Now().UTC()
	s.mu.Unlock()
	if err := s.saveCache(); err != nil {
		s.log.Warn("pricing: cannot write the price cache", "file", s.cachePath(), "err", err.Error())
	}
}

// refreshOpenRouter fetches the OpenRouter data when a provider uses OpenRouter. It
// returns true when the data changed.
func (s *Service) refreshOpenRouter(ctx context.Context, provs []config.Provider) bool {
	want := map[string]bool{}
	for _, p := range provs {
		if isOpenRouter(p.BaseURL) {
			for _, k := range priceKeys(p) {
				want[k] = true
			}
		}
	}
	if len(want) == 0 {
		return false
	}
	all, err := FetchOpenRouter(ctx, s.hc, s.orURL)
	if err != nil {
		s.log.Warn("pricing: the OpenRouter refresh failed; the earlier data stays", "err", err.Error())
		return false
	}
	kept := map[string]Price{}
	for k := range want {
		if pr, ok := all[k]; ok {
			kept[k] = pr
		}
	}
	s.mu.Lock()
	s.or = kept
	s.mu.Unlock()
	return true
}

// refreshLiteLLM fetches the LiteLLM data with a conditional GET. It returns true when the
// data changed.
func (s *Service) refreshLiteLLM(ctx context.Context, provs []config.Provider) bool {
	forList := liteForList(provs)
	s.mu.RLock()
	// The stored ETag is valid only when the cached subset is for the same models.
	useETag := s.lite != nil && slices.Equal(s.liteFor, forList)
	s.mu.RUnlock()
	etag := ""
	if useETag && s.db != nil {
		if v, ok, err := s.db.KVGet(ctx, kvLiteLLMETag); err == nil && ok {
			etag = v
		}
	}
	prices, newETag, notModified, err := FetchLiteLLM(ctx, s.hc, s.liteURL, etag, liteWant(provs))
	if err != nil {
		s.log.Warn("pricing: the LiteLLM refresh failed; the earlier data stays", "err", err.Error())
		return false
	}
	if notModified {
		return false
	}
	s.mu.Lock()
	s.lite, s.liteFor = prices, forList
	s.mu.Unlock()
	if s.db != nil {
		if err := s.db.KVSet(ctx, kvLiteLLMETag, newETag); err != nil {
			s.log.Warn("pricing: cannot store the LiteLLM ETag", "err", err.Error())
		}
	}
	return true
}

// providers returns the configured provider entries in name order.
func (s *Service) providers() []config.Provider {
	names := make([]string, 0, len(s.cfg.Providers))
	for n := range s.cfg.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]config.Provider, 0, len(names))
	for _, n := range names {
		out = append(out, s.cfg.Providers[n])
	}
	return out
}

// liteForList returns a sorted list that identifies the models of a LiteLLM subset.
func liteForList(provs []config.Provider) []string {
	var out []string
	for _, p := range provs {
		out = append(out, strings.Join(LiteLLMCandidates(p), " "))
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// cacheFile is the content of prices.json.
type cacheFile struct {
	OpenRouter map[string]cachePrice `json:"openrouter"`
	LiteLLM    map[string]cachePrice `json:"litellm"`
	// LiteLLMFor identifies the models of the LiteLLM subset.
	LiteLLMFor []string  `json:"litellm_for"`
	FetchedAt  time.Time `json:"fetched_at"`
}

// cachePrice is one Price in prices.json.
type cachePrice struct {
	Input         float64 `json:"input_per_token"`
	Output        float64 `json:"output_per_token"`
	CacheRead     float64 `json:"cache_read_per_token"`
	ContextWindow int     `json:"context_window"`
}

func (s *Service) cachePath() string { return filepath.Join(s.cacheDir, cacheFileName) }

// loadCache reads prices.json. A missing file is not an error.
func (s *Service) loadCache() error {
	b, err := os.ReadFile(s.cachePath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var c cacheFile
	if err := json.Unmarshal(b, &c); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.OpenRouter != nil {
		s.or = fromCache(c.OpenRouter)
	}
	if c.LiteLLM != nil {
		s.lite, s.liteFor = fromCache(c.LiteLLM), c.LiteLLMFor
	}
	s.fetchedAt = c.FetchedAt
	return nil
}

// saveCache writes prices.json through a temporary file and a rename.
func (s *Service) saveCache() error {
	s.mu.RLock()
	c := cacheFile{OpenRouter: toCache(s.or), LiteLLM: toCache(s.lite), LiteLLMFor: s.liteFor, FetchedAt: s.fetchedAt}
	s.mu.RUnlock()
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.cacheDir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.cacheDir, cacheFileName+".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), s.cachePath()); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

func toCache(m map[string]Price) map[string]cachePrice {
	if m == nil {
		return nil
	}
	out := make(map[string]cachePrice, len(m))
	for k, p := range m {
		out[k] = cachePrice{Input: p.InputPerToken, Output: p.OutputPerToken, CacheRead: p.CacheReadPerToken, ContextWindow: p.ContextWindow}
	}
	return out
}

func fromCache(m map[string]cachePrice) map[string]Price {
	out := make(map[string]Price, len(m))
	for k, p := range m {
		out[k] = Price{InputPerToken: p.Input, OutputPerToken: p.Output, CacheReadPerToken: p.CacheRead, ContextWindow: p.ContextWindow}
	}
	return out
}
