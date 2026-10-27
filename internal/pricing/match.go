package pricing

import (
	"net/url"
	"strings"

	"github.com/dnwandana/tether-agent/internal/config"
)

// HostPrefix maps the host of a provider base URL to its LiteLLM key prefix.
var HostPrefix = map[string]string{
	"api.z.ai":          "zai",
	"api.openai.com":    "openai",
	"api.anthropic.com": "anthropic",
	"api.deepseek.com":  "deepseek",
	"api.groq.com":      "groq",
	"api.mistral.ai":    "mistral",
}

// openRouterHost is the host whose providers use the OpenRouter price data.
const openRouterHost = "openrouter.ai"

// LiteLLMCandidates returns the LiteLLM keys to try for p, in this order: price_key,
// <prefix>/<model>, and the bare model. A key appears one time only. The last rule of the
// match, a unique key whose last path segment is the model, is in matchLiteLLM.
func LiteLLMCandidates(p config.Provider) []string {
	var out []string
	add := func(k string) {
		if k == "" {
			return
		}
		for _, have := range out {
			if have == k {
				return
			}
		}
		out = append(out, k)
	}
	add(p.PriceKey)
	if p.Model != "" {
		if prefix, ok := HostPrefix[hostOf(p.BaseURL)]; ok && !strings.HasPrefix(p.Model, prefix+"/") {
			add(prefix + "/" + p.Model)
		}
		add(p.Model)
	}
	return out
}

// matchLiteLLM finds the price of p in data. It tries the candidates first. Then it tries
// the keys whose last path segment is the model, and uses one only when it is unique.
// ambiguous is true when more than one key ends in the model.
func matchLiteLLM(data map[string]Price, p config.Provider) (price Price, key string, ambiguous bool) {
	for _, k := range LiteLLMCandidates(p) {
		if pr, ok := data[k]; ok {
			return pr, k, false
		}
	}
	if p.Model == "" {
		return Price{}, "", false
	}
	for k, pr := range data {
		if !endsInSegment(k, p.Model) {
			continue
		}
		if key != "" {
			return Price{}, "", true
		}
		price, key = pr, k
	}
	return price, key, false
}

// endsInSegment reports whether key is "<something>/<model>".
func endsInSegment(key, model string) bool {
	return len(key) > len(model)+1 && strings.HasSuffix(key, "/"+model)
}

// liteWant returns the filter for the LiteLLM fetch: the candidates of each provider, and
// each key whose last path segment is the model of a provider.
func liteWant(provs []config.Provider) func(key string) bool {
	keys := map[string]bool{}
	var models []string
	for _, p := range provs {
		for _, k := range LiteLLMCandidates(p) {
			keys[k] = true
		}
		if p.Model != "" {
			models = append(models, p.Model)
		}
	}
	return func(key string) bool {
		if keys[key] {
			return true
		}
		for _, m := range models {
			if endsInSegment(key, m) {
				return true
			}
		}
		return false
	}
}

// priceKeys returns the override keys and the OpenRouter ids to try for p: price_key,
// then the model.
func priceKeys(p config.Provider) []string {
	var out []string
	if p.PriceKey != "" {
		out = append(out, p.PriceKey)
	}
	if p.Model != "" && p.Model != p.PriceKey {
		out = append(out, p.Model)
	}
	return out
}

// hostOf returns the lower-case host name of a base URL, or "" when the URL has no host.
func hostOf(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// isOpenRouter reports whether the base URL is on the OpenRouter host.
func isOpenRouter(base string) bool {
	h := hostOf(base)
	return h == openRouterHost || strings.HasSuffix(h, "."+openRouterHost)
}
