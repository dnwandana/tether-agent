package pricing

import (
	"bytes"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
)

func TestCandidatesOrder(t *testing.T) {
	got := LiteLLMCandidates(config.Provider{BaseURL: "https://api.z.ai/api/coding/paas/v4", Model: "glm-4.7"})
	if !reflect.DeepEqual(got, []string{"zai/glm-4.7", "glm-4.7"}) {
		t.Fatal(got)
	}
	got = LiteLLMCandidates(config.Provider{BaseURL: "https://api.z.ai/x", Model: "glm-4.7", PriceKey: "zai/glm-4.7-flash"})
	if got[0] != "zai/glm-4.7-flash" || len(got) != 3 {
		t.Fatal(got)
	}
	got = LiteLLMCandidates(config.Provider{BaseURL: "http://localhost:8080/v1", Model: "qwen3"})
	if !reflect.DeepEqual(got, []string{"qwen3"}) {
		t.Fatal("an unknown host has no prefix:", got)
	}
	got = LiteLLMCandidates(config.Provider{BaseURL: "https://api.openai.com/v1", Model: "openai/gpt-4o", PriceKey: "openai/gpt-4o"})
	if !reflect.DeepEqual(got, []string{"openai/gpt-4o"}) {
		t.Fatal("no duplicates:", got)
	}
	if LiteLLMCandidates(config.Provider{}) != nil {
		t.Fatal("no model, no candidates")
	}
}

func TestHostOf(t *testing.T) {
	if hostOf("https://API.Z.AI:443/api") != "api.z.ai" || hostOf("not a url") != "" {
		t.Fatal()
	}
	if !isOpenRouter("https://openrouter.ai/api/v1") || isOpenRouter("https://api.z.ai/v4") {
		t.Fatal()
	}
}

func TestUniqueLastSegmentFallback(t *testing.T) {
	data := map[string]Price{"coralbricks/glm-5.3": {InputPerToken: 1}, "gpt-4o-mini": {InputPerToken: 2}}
	p := config.Provider{BaseURL: "https://example.com/v1", Model: "glm-5.3"}
	got, key, ambiguous := matchLiteLLM(data, p)
	if key != "coralbricks/glm-5.3" || got.InputPerToken != 1 || ambiguous {
		t.Fatal(got, key)
	}
	data["other/glm-5.3"] = Price{InputPerToken: 3}
	if _, key, ambiguous := matchLiteLLM(data, p); key != "" || !ambiguous {
		t.Fatal("two keys end in the model: not found")
	}
	// A direct candidate wins over the suffix rule.
	data["glm-5.3"] = Price{InputPerToken: 4}
	if got, key, _ := matchLiteLLM(data, p); key != "glm-5.3" || got.InputPerToken != 4 {
		t.Fatal(key)
	}
}

func TestAmbiguousWarnsOnce(t *testing.T) {
	var buf bytes.Buffer
	s := newSvc(t)
	s.log = slog.New(slog.NewTextHandler(&buf, nil))
	s.lite = map[string]Price{"a/glm-5.3": {InputPerToken: 1}, "b/glm-5.3": {InputPerToken: 2}}
	p := config.Provider{BaseURL: "https://example.com/v1", Model: "glm-5.3"}
	for range 3 {
		if _, ok := s.Lookup("primary", p); ok {
			t.Fatal("ambiguous must be not found")
		}
	}
	if n := strings.Count(buf.String(), "price_key"); n != 1 {
		t.Fatal("warn once:", buf.String())
	}
}

func TestLiteLLMWant(t *testing.T) {
	want := liteWant([]config.Provider{{BaseURL: "https://api.z.ai/v4", Model: "glm-4.7"}, {Model: "glm-5.3"}})
	for k, exp := range map[string]bool{"zai/glm-4.7": true, "glm-4.7": true, "coralbricks/glm-5.3": true, "gpt-4o": false, "xglm-5.3": false} {
		if want(k) != exp {
			t.Error(k)
		}
	}
}
