package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// FallbackStatus is the state of the first entry of a chain.
type FallbackStatus struct {
	Active bool // the first entry is in its wait time
	// FirstName is the name of the first entry, and PrimaryModel its model.
	FirstName    string
	PrimaryModel string
	// FallbackName and FallbackModel name the entry that serves the calls:
	// the first entry that is not in its wait time.
	FallbackName  string
	FallbackModel string
	FailedAt      time.Time // the time of the last failure of the first entry
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

// waitTime is the time that the chain skips an entry after it fails before
// the first byte. Hermes uses the same value after an HTTP 429.
const waitTime = 60 * time.Second

// healthKey names one entry and one model. A failure of a pinned model does
// not mark the other models of the entry.
type healthKey struct{ name, model string }

// failure is the last failure of one entry and model.
type failure struct {
	at    time.Time
	cause string
}

// Health holds the last failure of each entry and model. All chains share
// one Health, because it describes a provider, not one session. It lives in
// memory, so a restart clears it.
type Health struct {
	now func() time.Time

	mu    sync.Mutex
	fails map[healthKey]failure
	// noEffort holds the entries that reject the effort field.
	noEffort map[healthKey]bool
}

// NewHealth returns an empty Health. A nil now uses time.Now.
func NewHealth(now func() time.Time) *Health {
	if now == nil {
		now = time.Now
	}
	return &Health{now: now, fails: map[healthKey]failure{}, noEffort: map[healthKey]bool{}}
}

// RejectsEffort reports whether the entry rejected an effort field. A call
// to such an entry sends no effort, so the provider default applies.
func (h *Health) RejectsEffort(name, model string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.noEffort[healthKey{name, model}]
}

// rejectEffort records that the entry rejects the effort field.
func (h *Health) rejectEffort(name, model string) {
	h.mu.Lock()
	h.noEffort[healthKey{name, model}] = true
	h.mu.Unlock()
}

// waiting reports whether the entry failed less than waitTime ago.
func (h *Health) waiting(name, model string) bool {
	f, ok := h.last(name, model)
	return ok && h.now().Sub(f.at) < waitTime
}

// last returns the last failure of the entry.
func (h *Health) last(name, model string) (failure, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f, ok := h.fails[healthKey{name, model}]
	return f, ok
}

// fail records a failure of the entry with the short cause of err.
func (h *Health) fail(name, model string, err error) {
	t := h.now()
	h.mu.Lock()
	h.fails[healthKey{name, model}] = failure{at: t, cause: ShortCause(err)}
	h.mu.Unlock()
}

// clear removes the failure of the entry after a success.
func (h *Health) clear(name, model string) {
	h.mu.Lock()
	delete(h.fails, healthKey{name, model})
	h.mu.Unlock()
}

// ChainEntry is one entry of a chain. Model is a ":model" pin; "" uses the
// model of P. Vision tells whether the entry takes images inline. Effort is
// the default effort of the entry.
type ChainEntry struct {
	Name   string
	P      Provider
	Model  string
	Vision bool
	Effort string
}

// model returns the model of the entry: the pin, else the model of P.
func (e ChainEntry) model() string {
	if e.Model != "" {
		return e.Model
	}
	return e.P.Model()
}

// Chain calls its entries in order and goes to the next entry when a call
// fails before the first byte. Its state is in Health, so a chain is a cheap
// value that a caller can build on each call.
type Chain struct {
	entries []ChainEntry
	health  *Health
	log     *slog.Logger
}

// NewChain returns a chain of entries that shares h. A nil log uses
// slog.Default.
func NewChain(entries []ChainEntry, h *Health, log *slog.Logger) *Chain {
	if log == nil {
		log = slog.Default()
	}
	if h == nil {
		h = NewHealth(nil)
	}
	return &Chain{entries: entries, health: h, log: log}
}

// Name returns the name of the first entry.
func (c *Chain) Name() string { return c.entries[0].Name }

// Model returns the model of the first entry.
func (c *Chain) Model() string { return c.entries[0].model() }

// First returns the first entry of the chain.
func (c *Chain) First() ChainEntry { return c.entries[0] }

// order returns the indexes of the entries from the entry named startAt:
// that entry, the entries after it, then the entries before it. An empty or
// unknown startAt starts at the first entry.
func (c *Chain) order(startAt string) []int {
	start := 0
	for i, e := range c.entries {
		if e.Name == startAt {
			start = i
			break
		}
	}
	out := make([]int, 0, len(c.entries))
	for k := range c.entries {
		out = append(out, (start+k)%len(c.entries))
	}
	return out
}

// requestModel returns the model of the request to entry i. The first
// entry gets its pin, else the model of req. Another entry uses its own
// model, because the model of req names a model of the first entry.
func (c *Chain) requestModel(i int, req Request) string {
	if i != 0 {
		return ""
	}
	if c.entries[0].Model != "" {
		return c.entries[0].Model
	}
	return req.Model
}

// healthModel returns the model that keys the health of entry i.
func (c *Chain) healthModel(i int, req Request) string {
	if m := c.requestModel(i, req); m != "" {
		return m
	}
	return c.entries[i].P.Model()
}

// Stream calls the entries in order from req.StartAt. It skips an entry in
// its wait time, but tries all entries when all of them wait. A call that
// fails before the first byte marks the entry and goes to the next entry.
// Another error or a cancel returns at once. After the first byte, no other
// entry gets the call. With req.NeedVision, only the entries with vision get
// the call.
func (c *Chain) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	order := c.order(req.StartAt)
	if req.NeedVision {
		order = slices.DeleteFunc(order, func(i int) bool { return !c.entries[i].Vision })
		if len(order) == 0 {
			return nil, fmt.Errorf("provider %s: %w: no entry of the chain has vision", c.entries[0].Name, ErrBeforeFirstByte)
		}
	}
	try := make([]int, 0, len(order))
	for _, i := range order {
		if !c.health.waiting(c.entries[i].Name, c.healthModel(i, req)) {
			try = append(try, i)
		}
	}
	if len(try) == 0 {
		try = order
	}
	var errs []error
	for k, i := range try {
		e, model := c.entries[i], c.healthModel(i, req)
		ch, err := c.call(ctx, i, model, req)
		if err == nil {
			c.health.clear(e.Name, model)
			return ch, nil
		}
		if ctx.Err() != nil || !errors.Is(err, ErrBeforeFirstByte) {
			return nil, err
		}
		c.health.fail(e.Name, model, err)
		errs = append(errs, err)
		if k+1 < len(try) {
			j := try[k+1]
			c.log.Warn("provider fallback",
				"from", e.Name, "to", c.entries[j].Name,
				"from_model", model, "to_model", c.healthModel(j, req),
				"err", err.Error())
		}
	}
	return nil, errors.Join(errs...)
}

// call sends req to entry i, which has the health key model. The effort is
// the effort of req, else the effort of the entry, and none when the entry
// rejects effort. An entry with no vision gets the messages with no images;
// the "[image: <path>]" text stays. When the entry rejects a non-empty
// effort with an HTTP 400, call sends the call one more time with no
// effort. Only a retry that works records the rejection, because the 400
// can have another cause. That retry marks no failure.
func (c *Chain) call(ctx context.Context, i int, model string, req Request) (<-chan Event, error) {
	e := c.entries[i]
	req.Model = c.requestModel(i, req)
	if req.Effort == "" {
		req.Effort = e.Effort
	}
	if c.health.RejectsEffort(e.Name, model) {
		req.Effort = ""
	}
	if !e.Vision {
		req.Messages = withoutImages(req.Messages)
	}
	ch, err := e.P.Stream(ctx, req)
	if err == nil || req.Effort == "" || ctx.Err() != nil || !RejectsEffort(err) {
		return ch, err
	}
	level := req.Effort
	req.Effort = ""
	ch, err2 := e.P.Stream(ctx, req)
	if err2 != nil {
		return ch, err2
	}
	c.log.Warn(fmt.Sprintf("provider %s: model %s rejects the effort %q. The provider default applies", e.Name, model, level),
		"err", err.Error())
	c.health.rejectEffort(e.Name, model)
	return ch, nil
}

// withoutImages returns msgs with no images. It copies the slice only when
// a message has images, so the slice of the caller does not change.
func withoutImages(msgs []Message) []Message {
	var out []Message
	for i, m := range msgs {
		if len(m.Images) == 0 {
			continue
		}
		if out == nil {
			out = append([]Message(nil), msgs...)
		}
		out[i].Images = nil
	}
	if out == nil {
		return msgs
	}
	return out
}

// Down reports whether every entry of the chain is in its wait time after a failure.
// Stream still tries all entries then, but a background call, for example a review, can
// skip its work and save the cost of a call that fails.
func (c *Chain) Down() bool {
	for _, e := range c.entries {
		if !c.health.waiting(e.Name, e.model()) {
			return false
		}
	}
	return len(c.entries) > 0
}

// FallbackStatus returns the state of the first entry, from Health only.
// FallbackName and FallbackModel name the first entry that is not in its
// wait time.
func (c *Chain) FallbackStatus() FallbackStatus {
	first := c.entries[0]
	st := FallbackStatus{FirstName: first.Name, PrimaryModel: first.model()}
	if f, ok := c.health.last(first.Name, first.model()); ok {
		st.Active = c.health.waiting(first.Name, first.model())
		st.FailedAt, st.Cause = f.at, f.cause
	}
	served := first
	for _, e := range c.entries {
		if !c.health.waiting(e.Name, e.model()) {
			served = e
			break
		}
	}
	st.FallbackName, st.FallbackModel = served.Name, served.model()
	return st
}
