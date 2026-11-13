package provider

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
)

func TestNewUnusableEntryFailsBeforeFirstByte(t *testing.T) {
	msg := "config: providers.deepseek needs an API key. Set DEEPSEEK_API_KEY in ~/.tether/.env, or set providers.deepseek.api_key"
	p, err := New("deepseek", config.Provider{Format: "openai", Model: "m", Unusable: msg}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "deepseek" || p.Model() != "m" {
		t.Fatal(p.Name(), p.Model())
	}
	_, err = p.Stream(ctx, Request{})
	if !errors.Is(err, ErrBeforeFirstByte) || !strings.Contains(err.Error(), msg) {
		t.Fatal(err)
	}
}

func TestEndpointIssuerAndQuirks(t *testing.T) {
	e := newEndpoint("primary", config.Provider{Preset: "xai", Format: "responses", BaseURL: "https://api.x.ai/v1", Model: "grok-4.7"}, nil)
	if e.issuer("") != "responses https://api.x.ai/v1 grok-4.7" || e.issuer("grok-4.6") != "responses https://api.x.ai/v1 grok-4.6" {
		t.Fatal(e.issuer(""))
	}
	if !e.has("conv_id_header") || !e.has("prompt_cache_key") || e.has("bearer_auth") || e.effortStyle != "xai" {
		t.Fatalf("%+v", e)
	}
}

// setConfig returns a config with three entries and a fallback list. With
// extra "old", the config has the old "fallback" entry and no list.
func setConfig(extra string) *config.Config {
	c := config.Default()
	c.Providers = map[string]config.Provider{
		"primary":    {Format: "openai", BaseURL: "http://p", Model: "m1", Vision: true, Effort: "high"},
		"deepseek":   {Format: "openai", BaseURL: "http://d", Model: "m2"},
		"openrouter": {Format: "openai", BaseURL: "http://o", Model: "m3"},
	}
	c.Agent.Fallback = []string{"deepseek", "openrouter"}
	if extra == "old" {
		c.Agent.Fallback = nil
		c.Providers["fallback"] = config.Provider{Format: "anthropic", BaseURL: "http://f", Model: "m4"}
	}
	return &c
}

// names returns "name/pin" for each entry of c.
func names(c *Chain) string {
	var out []string
	for _, e := range c.entries {
		out = append(out, e.Name+"/"+e.Model)
	}
	return strings.Join(out, ",")
}

func TestFromConfigBuildsSet(t *testing.T) {
	s, err := FromConfig(setConfig(""), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, names(s.Default), "primary/,deepseek/,openrouter/")
	if s.All["primary"] != Provider(s.Default) || s.Raw["primary"] == Provider(s.Default) || s.Health == nil {
		t.Fatal("All must hold the chain as primary; Raw the adapter")
	}
	if len(s.All) != 3 || len(s.Raw) != 3 || s.All["deepseek"] != s.Raw["deepseek"] {
		t.Fatal(s.All, s.Raw)
	}
	if !s.Default.entries[0].Vision || s.Default.entries[0].Effort != "high" {
		t.Fatalf("%+v", s.Default.entries[0])
	}
	old, _ := FromConfig(setConfig("old"), nil, nil)
	eq(t, names(old.Default), "primary/,fallback/")
	none := setConfig("old")
	none.Agent.Fallback = []string{}
	off, _ := FromConfig(none, nil, nil)
	eq(t, names(off.Default), "primary/")
}

func TestChainForPinned(t *testing.T) {
	s, _ := FromConfig(setConfig(""), nil, nil)
	eq(t, names(s.ChainFor("deepseek", "deepseek-reasoner")), "deepseek/deepseek-reasoner,primary/,openrouter/")
	eq(t, names(s.ChainFor("primary", "")), "primary/,deepseek/,openrouter/")
	eq(t, names(s.ChainFor("primary", "m9")), "primary/m9,deepseek/,openrouter/")
	eq(t, names(s.ChainFor("nope", "")), "primary/,deepseek/,openrouter/")
	if s.ChainFor("deepseek", "").health != s.Health {
		t.Fatal("every chain shares one Health")
	}
	if _, ok := s.ChainFor("deepseek", "").entries[1].P.(*Chain); ok {
		t.Fatal("a pinned chain must not hold a chain")
	}
}

func TestChainForSkipsUnusable(t *testing.T) {
	c := setConfig("")
	c.Providers["zai"] = config.Provider{Format: "openai", BaseURL: "http://z", Model: "glm-4.7",
		Unusable: "config: providers.zai needs an API key. Set ZAI_API_KEY in ~/.tether/.env, or set providers.zai.api_key"}
	c.Agent.Fallback = []string{"zai", "deepseek", "openrouter"}
	s, err := FromConfig(c, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A pinned first entry that is not usable gives the default chain with no pin.
	eq(t, names(s.ChainFor("zai", "m")), "primary/,deepseek/,openrouter/")
	eq(t, s.ChainFor("zai", "m").FallbackStatus().FirstName, "primary")
	// A fallback entry that is not usable is never in a chain.
	eq(t, names(s.ChainFor("primary", "")), "primary/,deepseek/,openrouter/")
	eq(t, names(s.Default), "primary/,deepseek/,openrouter/")
	eq(t, names(s.ChainFor("deepseek", "m9")), "deepseek/m9,primary/,openrouter/")

	// A primary that is not usable stays, so a chain is never empty.
	bad := setConfig("")
	p := bad.Providers["primary"]
	p.Unusable = "x"
	bad.Providers["primary"] = p
	bad.Agent.Fallback = []string{}
	s2, err := FromConfig(bad, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, names(s2.Default), "primary/")
	eq(t, names(s2.ChainFor("primary", "m9")), "primary/m9")
	eq(t, names(s2.ChainFor("deepseek", "m9")), "deepseek/m9,primary/")
}

func TestFromConfigNeedsPrimary(t *testing.T) {
	c := config.Default()
	c.Providers = map[string]config.Provider{}
	if _, err := FromConfig(&c, nil, nil); err == nil || !strings.Contains(err.Error(), "providers.primary is missing") {
		t.Fatal(err)
	}
	bad := setConfig("")
	bad.Providers["deepseek"] = config.Provider{Format: "grpc"}
	if _, err := FromConfig(bad, nil, nil); err == nil {
		t.Fatal("want the format error")
	}
}

func TestFromConfigSetsProviderKeys(t *testing.T) {
	c := setConfig("old")
	c.Provider.FailureSkipS = 5
	c.Provider.AnthropicMaxTokens = 2048
	s, err := FromConfig(c, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Health.Skip != 5*time.Second {
		t.Fatal(s.Health.Skip)
	}
	if got := s.Raw["fallback"].(*anthropic).maxTokens; got != 2048 {
		t.Fatal(got)
	}
	c.Provider = config.ProviderConfig{}
	s, err = FromConfig(c, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Health.Skip != 0 || s.Raw["fallback"].(*anthropic).maxTokens != 0 {
		t.Fatal("zero keys keep the consts:", s.Health.Skip)
	}
}
