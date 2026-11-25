package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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

// ChainEntry is one entry of a chain. Model is a ":model" pin; "" uses the
// model of P. Vision tells whether the entry takes images inline. Effort is
// the default effort of the entry.
type ChainEntry struct {
	Name   string
	P      Provider
	Model  string
	Vision bool
	Effort string
	// ZAI tells whether a Z.AI code in an error body counts. CodingPlan tells whether the
	// entry is a Z.AI Coding Plan entry, which gets the 1305 ladder on a chain with one entry.
	ZAI        bool
	CodingPlan bool
}

// model returns the model of the entry: the pin, else the model of P.
func (e ChainEntry) model() string {
	if e.Model != "" {
		return e.Model
	}
	return e.P.Model()
}

// Chain calls its entries in order. When a call fails before the first byte, the
// chain can try the same entry again, and then it goes to the next entry. Its
// state is in Health, so a chain is a cheap value that a caller can build on
// each call.
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
// its wait time, but tries all entries when all of them wait. Each entry gets
// its tries (tryEntry). When the last try fails before the first byte, Stream
// records the failure in Health and goes to the next entry. A Stop-class error
// or a cancel returns at once. After the first byte, no other entry gets the
// call. With req.NeedVision, only the entries with vision get the call.
// A used-up quota skips the entry, with all its models, until the reset time. When all
// entries wait, Stream leaves out each entry in a quota skip. When only such entries are
// left, Stream returns the quota error with no call. A call with a notify function in ctx
// sends the pending quota notice of a skipped entry. It sends the notice after the next
// entry returned a stream.
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
		// All entries wait. Try them all, but not an entry in a quota skip: its quota does not
		// clear before the reset time.
		last := ""
		for _, i := range order {
			if name := c.entries[i].Name; c.health.inQuota(name) {
				last = name
			} else {
				try = append(try, i)
			}
		}
		if len(try) == 0 {
			if err := c.health.quotaErr(last); err != nil {
				return nil, err
			}
			try = order
		}
	}
	var errs []error
	for k, i := range try {
		e, model := c.entries[i], c.healthModel(i, req)
		ch, cls, err := c.tryEntry(ctx, i, model, req, k+1 < len(try), len(c.entries) == 1)
		if err == nil {
			c.sendQuotaNotices(ctx, order, i, req)
			return ch, nil
		}
		switch cls {
		case ClassStop:
			return nil, err
		case ClassTimeout:
			c.health.failTimeout(e.Name, model, err)
		case ClassQuota:
			c.recordQuota(e.Name, model, err)
		default: // ClassRetry and ClassNextEntry
			c.health.fail(e.Name, model, err, retryAfterSkip(err, c.health.now()))
		}
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

// recordQuota records the used-up quota of entry name in Health. The reset time is the one
// that the error carries. Else it is now plus a Retry-After value over 60 s, at most 4
// hours. Else it is not known. The first quota of an episode with no known reset time logs
// the body.
func (c *Chain) recordQuota(name, model string, err error) {
	var reset time.Time
	body := ""
	if se, ok := errors.AsType[*HTTPStatusError](err); ok {
		body = se.Body
		switch {
		case !se.ResetAt.IsZero():
			reset = se.ResetAt
		case se.RetryAfter > 60*time.Second:
			reset = c.health.now().Add(min(se.RetryAfter, 4*time.Hour))
		}
	}
	if c.health.failQuota(name, model, err, reset) && reset.IsZero() {
		c.log.Warn("provider quota reset time not known", "entry", name, "model", model, "body", body)
	}
}

// sendQuotaNotices sends the pending quota notice of each entry before the serving entry
// in order. It does nothing when ctx has no notify function: the notice stays pending.
func (c *Chain) sendQuotaNotices(ctx context.Context, order []int, served int, req Request) {
	notify := notifyFrom(ctx)
	if notify == nil {
		return
	}
	for _, j := range order {
		if j == served {
			return
		}
		if reset, ok := c.health.takeNotice(c.entries[j].Name); ok {
			notify(Notice{Kind: NoticeQuota, Entry: c.entries[j].Name, Model: c.healthModel(j, req),
				NextEntry: c.entries[served].Name, NextModel: c.healthModel(served, req), ResetAt: reset})
		}
	}
}

// tryEntry makes all tries of entry i. On success it returns the stream, ClassStop, and a nil
// error. Else the error is a *ClassedError with the class of the last try. It sets no failure
// in Health: Stream does that after the last try.
func (c *Chain) tryEntry(ctx context.Context, i int, model string, req Request, hasNext, single bool) (<-chan Event, Class, error) {
	e, h := c.entries[i], c.health
	waits, noticed := 0, false
	// A call with a next entry does not wait for a hold over 10 s. Such a hold comes from a
	// call that had no next entry. The Retry-After rule sends a call with a next entry on.
	if hold := h.holdUntil(e.Name); hold.After(h.now()) && !(hasNext && hold.Sub(h.now()) > 10*time.Second) {
		d := hold.Sub(h.now())
		d += time.Duration(float64(d) * 0.5 * h.jitter())
		if fitsDeadline(ctx, d) {
			if err := h.sleep(ctx, d); err != nil {
				return nil, ClassStop, &ClassedError{Entry: e.Name, Class: ClassStop, Err: err}
			}
			waits = 1
		}
	}
	for {
		started := h.now()
		ch, err := c.call(ctx, i, model, req)
		if err == nil {
			h.clear(e.Name, model, started)
			return ch, ClassStop, nil
		}
		cls := Classify(err, e.ZAI)
		if ctx.Err() != nil {
			cls = ClassStop
		}
		wait, ok := c.retryWait(e, cls, err, waits, hasNext, single)
		if !ok || !fitsDeadline(ctx, wait) {
			return nil, cls, &ClassedError{Entry: e.Name, Class: cls, Err: err}
		}
		if cls == ClassRetry {
			h.setHold(e.Name, h.now().Add(wait))
		}
		c.log.Info("provider retry", "entry", e.Name, "model", model, "try", waits+2, "wait", wait, "cause", ShortCause(err))
		if notify := notifyFrom(ctx); notify != nil && wait > 10*time.Second && !noticed {
			notify(Notice{Kind: NoticeWait, Entry: e.Name, Model: model, Wait: wait, Cause: ShortCause(err)})
			noticed = true
		}
		if err := h.sleep(ctx, wait); err != nil {
			return nil, ClassStop, &ClassedError{Entry: e.Name, Class: ClassStop, Err: err}
		}
		waits++
	}
}

// retryWait returns the wait before retry n = waits + 1, and false when no retry comes.
func (c *Chain) retryWait(e ChainEntry, cls Class, err error, waits int, hasNext, single bool) (time.Duration, bool) {
	h := c.health
	n := waits + 1
	short := func() time.Duration {
		d := min(2*time.Second<<(n-1), 60*time.Second)
		return d + time.Duration(float64(d)*0.5*h.jitter())
	}
	switch cls {
	case ClassTimeout:
		if n == 1 && !hasNext {
			return short(), true
		}
	case ClassRetry:
		se, hasStatus := errors.AsType[*HTTPStatusError](err)
		if n <= 2 {
			if hasStatus && se.RetryAfter > 0 {
				switch {
				case se.RetryAfter <= 10*time.Second:
					return se.RetryAfter, true
				case se.RetryAfter <= 60*time.Second:
					if hasNext {
						return 0, false
					}
					return se.RetryAfter, true
				}
			}
			return short(), true
		}
		if n <= 6 && single && e.ZAI && e.CodingPlan && hasStatus && se.ProviderCode == "1305" {
			d := time.Duration(n-2) * 30 * time.Second
			return d + time.Duration(float64(d)*0.2*h.jitter()), true
		}
	}
	return 0, false
}

// fitsDeadline reports whether a wait of d and 5 s more end before the deadline of ctx.
func fitsDeadline(ctx context.Context, d time.Duration) bool {
	dl, ok := ctx.Deadline()
	return !ok || !time.Now().Add(d+5*time.Second).After(dl)
}

// retryAfterSkip returns now plus the Retry-After value of err when the value is over 10 s and
// at most 60 s. Else it returns the zero time.
func retryAfterSkip(err error, now time.Time) time.Time {
	if se, ok := errors.AsType[*HTTPStatusError](err); ok && se.RetryAfter > 10*time.Second && se.RetryAfter <= 60*time.Second {
		return now.Add(se.RetryAfter)
	}
	return time.Time{}
}

// call makes one try: it sends req to entry i, which has the health key model. The effort is
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
