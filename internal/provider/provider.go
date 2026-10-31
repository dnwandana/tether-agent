package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/dnwandana/tether-agent/internal/config"
)

// Provider streams one model call.
//
// Stream returns an error when the call fails before the response stream
// starts. After a successful return, the channel gives the events and
// closes at the end. The last event is EventDone or EventError.
type Provider interface {
	Stream(ctx context.Context, req Request) (<-chan Event, error)
	Name() string
	Model() string
}

// ErrBeforeFirstByte marks a failure before the first byte of the stream,
// for example a connection error, an HTTP 5xx, or an HTTP 429. The chain
// tries the next entry only for this error.
var ErrBeforeFirstByte = errors.New("provider failed before the first byte")

// endpoint holds the settings that the adapters share: the entry name, the
// wire format, the connection, the quirks of the preset, and the effort
// style.
type endpoint struct {
	name, format, baseURL, apiKey, model string
	hc                                   *http.Client
	quirks                               map[string]bool
	effortStyle                          string
}

// newEndpoint returns the endpoint of the config entry p. The quirks and
// the effort style come from the preset of p.
func newEndpoint(name string, p config.Provider, hc *http.Client) endpoint {
	e := endpoint{
		name: name, format: p.Format, baseURL: p.BaseURL, apiKey: p.APIKey, model: p.Model,
		hc: hc, effortStyle: config.EffortStyleOf(p),
	}
	if q := config.QuirksOf(p); len(q) > 0 {
		e.quirks = make(map[string]bool, len(q))
		for _, k := range q {
			e.quirks[k] = true
		}
	}
	return e
}

// issuer returns "<format> <baseURL> <model>". An empty model uses the
// model of the endpoint. Stored reasoning goes back only to its issuer.
func (e endpoint) issuer(model string) string {
	if model == "" {
		model = e.model
	}
	return e.format + " " + e.baseURL + " " + model
}

// has reports whether the endpoint has the quirk.
func (e endpoint) has(quirk string) bool { return e.quirks[quirk] }

// unusable is the adapter of an entry that cannot serve a call, for
// example an entry with no API key. Each call fails before the first byte,
// so the chain goes to the next entry.
type unusable struct {
	name, model, reason string
}

func (u *unusable) Name() string  { return u.name }
func (u *unusable) Model() string { return u.model }

// Stream returns the load error of the entry. It makes no network call.
func (u *unusable) Stream(context.Context, Request) (<-chan Event, error) {
	return nil, fmt.Errorf("provider %s: %w: %s", u.name, ErrBeforeFirstByte, u.reason)
}

// New returns the adapter for one config entry. An entry with Unusable set
// gets an adapter that fails each call. A nil hc uses http.DefaultClient.
func New(name string, p config.Provider, hc *http.Client) (Provider, error) {
	if p.Unusable != "" {
		return &unusable{name: name, model: p.Model, reason: p.Unusable}, nil
	}
	switch p.Format {
	case "openai":
		return newOpenAI(newEndpoint(name, p, hc)), nil
	case "anthropic":
		return newAnthropic(newEndpoint(name, p, hc)), nil
	case "responses":
		return newResponses(newEndpoint(name, p, hc)), nil
	default:
		return nil, fmt.Errorf("provider %s: format must be openai | anthropic | responses, not %q", name, p.Format)
	}
}

// Set holds the providers of a config. Raw holds the adapter of each entry
// by name. All is Raw with "primary" set to Default, for the readers that
// want the default chain. All chains share Health.
type Set struct {
	Default *Chain
	All     map[string]Provider
	Raw     map[string]Provider
	Health  *Health

	log      *slog.Logger
	fallback []string
	vision   map[string]bool
	effort   map[string]string
}

// FromConfig builds the adapter of every entry of cfg.Providers, one Health,
// and the default chain [primary, FallbackList()...]. The set keeps a copy
// of the config values that it needs, so a later edit of cfg does not
// change it.
func FromConfig(cfg *config.Config, hc *http.Client, log *slog.Logger) (*Set, error) {
	if _, ok := cfg.Providers["primary"]; !ok {
		return nil, errors.New("provider: providers.primary is missing in the config. Add it to config.yaml, or run \"tether setup\"")
	}
	s := &Set{
		Raw:      make(map[string]Provider, len(cfg.Providers)),
		Health:   NewHealth(nil),
		log:      log,
		fallback: cfg.FallbackList(),
		vision:   make(map[string]bool, len(cfg.Providers)),
		effort:   make(map[string]string, len(cfg.Providers)),
	}
	for name, pc := range cfg.Providers {
		p, err := New(name, pc, hc)
		if err != nil {
			return nil, err
		}
		s.Raw[name] = p
		s.vision[name], s.effort[name] = pc.Vision, pc.Effort
	}
	s.Default = s.ChainFor("primary", "")
	s.All = make(map[string]Provider, len(s.Raw))
	for name, p := range s.Raw {
		s.All[name] = p
	}
	s.All["primary"] = s.Default
	return s, nil
}

// ChainFor returns the chain [first, primary, FallbackList()...] with no
// name twice. model is a pin for the first entry only. An unknown first
// uses "primary". The chain reads Raw, so it never holds a chain.
func (s *Set) ChainFor(first, model string) *Chain {
	if _, ok := s.Raw[first]; !ok {
		first = "primary"
	}
	seen := map[string]bool{}
	var entries []ChainEntry
	for _, name := range append([]string{first, "primary"}, s.fallback...) {
		p, ok := s.Raw[name]
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		e := ChainEntry{Name: name, P: p, Vision: s.vision[name], Effort: s.effort[name]}
		if name == first {
			e.Model = model
		}
		entries = append(entries, e)
	}
	return NewChain(entries, s.Health, s.log)
}
