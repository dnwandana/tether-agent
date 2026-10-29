package provider

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// FallbackStatus is the state of a fallback wrapper.
type FallbackStatus struct {
	Active        bool // the last call went to the fallback
	PrimaryModel  string
	FallbackName  string // the config entry name, "fallback"
	FallbackModel string
	FailedAt      time.Time // the time of the last primary failure
	Cause         string    // "HTTP 503", "no connection", or "error"
}

// FallbackReporter is a provider that reports a fallback state.
type FallbackReporter interface{ FallbackStatus() FallbackStatus }

// StatusOf returns the state of p when p implements FallbackReporter.
func StatusOf(p Provider) (FallbackStatus, bool) {
	r, ok := p.(FallbackReporter)
	if !ok {
		return FallbackStatus{}, false
	}
	return r.FallbackStatus(), true
}

// fallback tries a secondary provider when the primary fails before the
// first byte of the stream.
type fallback struct {
	primary, secondary Provider
	log                *slog.Logger

	// mu guards the state of the primary. All sessions share one state,
	// because it describes the health of the primary, not one call.
	mu       sync.Mutex
	active   bool
	failedAt time.Time
	cause    string
}

// NewFallback returns a provider that calls primary, and calls secondary
// one time when the primary error matches ErrBeforeFirstByte. After the
// first byte, an error ends the stream and the partial text stays. Name
// and Model still report the primary. FallbackStatus reports which side
// served the last call. A nil log uses slog.Default.
func NewFallback(primary, secondary Provider, log *slog.Logger) Provider {
	if log == nil {
		log = slog.Default()
	}
	return &fallback{primary: primary, secondary: secondary, log: log}
}

func (f *fallback) Name() string  { return f.primary.Name() }
func (f *fallback) Model() string { return f.primary.Model() }

// FallbackStatus returns the state of the primary and the names of the
// two sides.
func (f *fallback) FallbackStatus() FallbackStatus {
	f.mu.Lock()
	active, failedAt, cause := f.active, f.failedAt, f.cause
	f.mu.Unlock()
	return FallbackStatus{
		Active:        active,
		PrimaryModel:  f.primary.Model(),
		FallbackName:  f.secondary.Name(),
		FallbackModel: f.secondary.Model(),
		FailedAt:      failedAt,
		Cause:         cause,
	}
}

// Stream calls the primary, then the secondary on an early failure.
func (f *fallback) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	ch, err := f.primary.Stream(ctx, req)
	if err == nil {
		// Keep the time and the cause of the last failure for the reply.
		f.mu.Lock()
		f.active = false
		f.mu.Unlock()
		return ch, nil
	}
	if ctx.Err() != nil {
		return ch, err
	}
	if !errors.Is(err, ErrBeforeFirstByte) {
		// This call did not go to the fallback, so the reply must not say that it did.
		f.mu.Lock()
		f.active = false
		f.mu.Unlock()
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
	f.mu.Lock()
	f.active, f.failedAt, f.cause = true, time.Now(), ShortCause(err)
	f.mu.Unlock()
	return ch, nil
}
