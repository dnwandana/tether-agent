package provider

import (
	"context"
	"errors"
	"log/slog"
)

// fallback tries a secondary provider when the primary fails before the
// first byte of the stream.
type fallback struct {
	primary, secondary Provider
	log                *slog.Logger
}

// NewFallback returns a provider that calls primary, and calls secondary
// one time when the primary error matches ErrBeforeFirstByte. After the
// first byte, an error ends the stream and the partial text stays. Name
// and Model report the primary. A nil log uses slog.Default.
func NewFallback(primary, secondary Provider, log *slog.Logger) Provider {
	if log == nil {
		log = slog.Default()
	}
	return &fallback{primary: primary, secondary: secondary, log: log}
}

func (f *fallback) Name() string  { return f.primary.Name() }
func (f *fallback) Model() string { return f.primary.Model() }

// Stream calls the primary, then the secondary on an early failure.
func (f *fallback) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	ch, err := f.primary.Stream(ctx, req)
	if err == nil || !errors.Is(err, ErrBeforeFirstByte) || ctx.Err() != nil {
		return ch, err
	}
	f.log.Warn("provider fallback",
		"from", f.primary.Name(), "to", f.secondary.Name(),
		"from_model", f.primary.Model(), "to_model", f.secondary.Model(),
		"err", err.Error())
	// A model override names a model of the primary. The secondary uses
	// its own model.
	req.Model = ""
	ch, err2 := f.secondary.Stream(ctx, req)
	if err2 != nil {
		f.log.Error("provider fallback failed", "from", f.primary.Name(), "to", f.secondary.Name(), "err", err2.Error())
		return nil, errors.Join(err, err2)
	}
	return ch, nil
}
