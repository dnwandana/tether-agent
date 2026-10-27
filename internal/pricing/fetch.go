// Package pricing fetches and looks up model prices and context windows.
package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Price is the price of one model in USD per token, plus its context window.
type Price struct {
	InputPerToken     float64
	OutputPerToken    float64
	CacheReadPerToken float64
	ContextWindow     int
}

// OpenRouterURL is the OpenRouter model list. It needs no API key.
const OpenRouterURL = "https://openrouter.ai/api/v1/models"

// LiteLLMURL is the LiteLLM price file (about 2.8 MB).
const LiteLLMURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// sampleSpecKey is the LiteLLM entry that documents the schema. It is not a model.
const sampleSpecKey = "sample_spec"

// maxBody limits the bytes that one fetch reads, so a bad server cannot fill the memory.
const maxBody = 64 << 20

// get sends a GET request without credentials. It sends If-None-Match when etag is set.
func get(ctx context.Context, hc *http.Client, url, etag string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("price fetch: bad URL %q: %w", url, err)
	}
	req.Header.Set("Accept", "application/json")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("price fetch: GET %s: %w", url, err)
	}
	return resp, nil
}

// statusError returns an error that names the URL and the HTTP status.
func statusError(url string, resp *http.Response) error {
	return fmt.Errorf("price fetch: GET %s: unexpected status %s", url, resp.Status)
}

// expectDelim reads the next token and checks that it is the delimiter d.
func expectDelim(dec *json.Decoder, d json.Delim, where string) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("price fetch: %s: %w", where, err)
	}
	if got, ok := tok.(json.Delim); !ok || got != d {
		return fmt.Errorf("price fetch: %s: expected %q, got %v", where, d, tok)
	}
	return nil
}

// openRouterModel is the part of one OpenRouter model entry that we use.
// OpenRouter sends the prices as decimal strings.
type openRouterModel struct {
	ID            string `json:"id"`
	ContextLength int    `json:"context_length"`
	Pricing       struct {
		Prompt         string `json:"prompt"`
		Completion     string `json:"completion"`
		InputCacheRead string `json:"input_cache_read"`
	} `json:"pricing"`
}

// FetchOpenRouter downloads the OpenRouter model list and returns the prices
// by model id, for example "z-ai/glm-4.7". It skips a model with a negative
// price, because OpenRouter uses -1 for a router with a variable price.
func FetchOpenRouter(ctx context.Context, hc *http.Client, url string) (map[string]Price, error) {
	resp, err := get(ctx, hc, url, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(url, resp)
	}

	dec := json.NewDecoder(io.LimitReader(resp.Body, maxBody))
	if err := expectDelim(dec, '{', "OpenRouter body"); err != nil {
		return nil, err
	}
	out := map[string]Price{}
	for dec.More() {
		key, err := objectKey(dec)
		if err != nil {
			return nil, err
		}
		if key != "data" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, fmt.Errorf("price fetch: OpenRouter field %q: %w", key, err)
			}
			continue
		}
		if err := expectDelim(dec, '[', "OpenRouter data"); err != nil {
			return nil, err
		}
		for dec.More() {
			var m openRouterModel
			if err := dec.Decode(&m); err != nil {
				return nil, fmt.Errorf("price fetch: OpenRouter model entry: %w", err)
			}
			if p, ok := m.price(); ok {
				out[m.ID] = p
			}
		}
		if err := expectDelim(dec, ']', "OpenRouter data end"); err != nil {
			return nil, err
		}
	}
	if err := expectDelim(dec, '}', "OpenRouter body end"); err != nil {
		return nil, err
	}
	return out, nil
}

// price converts the string prices. It returns false for an entry with no id,
// or with a price that is not a number or is negative.
func (m openRouterModel) price() (Price, bool) {
	if m.ID == "" {
		return Price{}, false
	}
	in, ok1 := parsePrice(m.Pricing.Prompt)
	outp, ok2 := parsePrice(m.Pricing.Completion)
	cache, ok3 := parsePrice(m.Pricing.InputCacheRead)
	if !ok1 || !ok2 || !ok3 {
		return Price{}, false
	}
	return Price{InputPerToken: in, OutputPerToken: outp, CacheReadPerToken: cache, ContextWindow: m.ContextLength}, true
}

// parsePrice parses one OpenRouter price string. An empty string is 0.
func parsePrice(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, true
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0, false
	}
	return f, true
}

// objectKey reads the next object key.
func objectKey(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", fmt.Errorf("price fetch: read object key: %w", err)
	}
	key, ok := tok.(string)
	if !ok {
		return "", fmt.Errorf("price fetch: expected an object key, got %v", tok)
	}
	return key, nil
}

// flexNum is a JSON number that also accepts a numeric string. Any other value
// gives 0, so one odd field in the LiteLLM file does not stop the parse.
type flexNum float64

// UnmarshalJSON decodes a number or a numeric string, and ignores other values.
func (n *flexNum) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		*n = 0
		return nil
	}
	*n = flexNum(f)
	return nil
}

// liteLLMEntry is the part of one LiteLLM model entry that we use.
type liteLLMEntry struct {
	Input          flexNum `json:"input_cost_per_token"`
	Output         flexNum `json:"output_cost_per_token"`
	CacheRead      flexNum `json:"cache_read_input_token_cost"`
	MaxInputTokens flexNum `json:"max_input_tokens"`
}

// FetchLiteLLM downloads the LiteLLM price file and returns the prices for the
// keys where want(key) is true. It decodes the file as a stream and skips the
// other entries, so it never holds the full file in memory. When etag is set,
// it sends If-None-Match, and a 304 answer gives notModified=true.
func FetchLiteLLM(ctx context.Context, hc *http.Client, url, etag string, want func(key string) bool) (prices map[string]Price, newETag string, notModified bool, err error) {
	resp, err := get(ctx, hc, url, etag)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, etag, true, nil
	case http.StatusOK:
	default:
		return nil, "", false, statusError(url, resp)
	}
	newETag = resp.Header.Get("ETag")

	dec := json.NewDecoder(io.LimitReader(resp.Body, maxBody))
	if err := expectDelim(dec, '{', "LiteLLM body"); err != nil {
		return nil, "", false, err
	}
	prices = map[string]Price{}
	var skip json.RawMessage // reused for every skipped entry
	for dec.More() {
		key, err := objectKey(dec)
		if err != nil {
			return nil, "", false, err
		}
		if key == sampleSpecKey || !want(key) {
			if err := dec.Decode(&skip); err != nil {
				return nil, "", false, fmt.Errorf("price fetch: LiteLLM entry %q: %w", key, err)
			}
			continue
		}
		var e liteLLMEntry
		if err := dec.Decode(&e); err != nil {
			return nil, "", false, fmt.Errorf("price fetch: LiteLLM entry %q: %w", key, err)
		}
		prices[key] = Price{
			InputPerToken:     float64(e.Input),
			OutputPerToken:    float64(e.Output),
			CacheReadPerToken: float64(e.CacheRead),
			ContextWindow:     int(e.MaxInputTokens),
		}
	}
	if err := expectDelim(dec, '}', "LiteLLM body end"); err != nil {
		return nil, "", false, err
	}
	return prices, newETag, false, nil
}
