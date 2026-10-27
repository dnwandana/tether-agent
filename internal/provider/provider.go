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

// ErrBeforeFirstByte marks a failure before the first byte of the stream:
// a connection error, an HTTP 5xx, or an HTTP 429. The fallback wrapper
// tries the next provider only for this error.
var ErrBeforeFirstByte = errors.New("provider failed before the first byte")

// New returns the adapter for one config entry. A nil hc uses
// http.DefaultClient.
func New(name string, p config.Provider, hc *http.Client) (Provider, error) {
	switch p.Format {
	case "openai":
		return NewOpenAI(name, p.BaseURL, p.APIKey, p.Model, hc), nil
	case "anthropic":
		return NewAnthropic(name, p.BaseURL, p.APIKey, p.Model, hc), nil
	default:
		return nil, fmt.Errorf("provider %s: format must be openai | anthropic, not %q", name, p.Format)
	}
}

// FromConfig builds every entry of cfg.Providers. It returns the default
// provider and a map of all the entries by name, for pinning. The default
// provider is "primary", wrapped with "fallback" when that entry exists.
// The map entry "primary" is the same wrapped provider.
func FromConfig(cfg *config.Config, hc *http.Client, log *slog.Logger) (Provider, map[string]Provider, error) {
	if _, ok := cfg.Providers["primary"]; !ok {
		return nil, nil, errors.New("provider: providers.primary is missing in the config. Add it to config.yaml, or run \"tether setup\"")
	}
	all := make(map[string]Provider, len(cfg.Providers))
	for name, pc := range cfg.Providers {
		p, err := New(name, pc, hc)
		if err != nil {
			return nil, nil, err
		}
		all[name] = p
	}
	def := all["primary"]
	if fb, ok := all["fallback"]; ok {
		def = NewFallback(def, fb, log)
		all["primary"] = def
	}
	return def, all, nil
}
