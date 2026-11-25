package provider

import (
	"context"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"
)

// waitTime is the default skip time of a failed entry. Hermes uses the same
// value after an HTTP 429.
const waitTime = 60 * time.Second

// maxSkip is the longest growing skip.
const maxSkip = 4 * time.Hour

// growingSkip returns 60 s × 2^(n-1), at most maxSkip. It stops the doubling at the
// maximum, so a large n cannot overflow.
func growingSkip(n int) time.Duration {
	d := time.Minute
	for i := 1; i < n && d < maxSkip; i++ {
		d *= 2
	}
	return min(d, maxSkip)
}

// healthKey names one entry and one model. A failure of a pinned model does
// not mark the other models of the entry.
type healthKey struct{ name, model string }

// failure is the last failure of one entry and model, or the quota record of one entry.
type failure struct {
	at    time.Time
	cause string
	until time.Time // the end of the skip
	// timeout is true for a Timeout-class failure.
	timeout bool
	// count is the timeouts in a row, or the quota skips in a row of a quota record.
	count int
	// The fields of a quota record only.
	model   string    // the model of the call that found the quota
	err     error     // the last quota error
	resetAt time.Time // the reset time of err, or the zero time when it is not known
	notice  bool      // a quota notice is pending
}

// Health holds the last failure of each entry and model, the quota record of each entry, and
// the retry hold of each entry. All chains share one Health, because it describes a provider,
// not one session. It lives in memory, so a restart clears it.
type Health struct {
	// Skip is the time that the chain skips an entry and model after the last try of a call
	// fails before the first byte. A used-up quota and a timeout get a longer skip. 0 uses
	// 60 s. Set it before the first call.
	Skip time.Duration
	// Sleep waits d, and stops when ctx ends. Nil uses a real timer. Tests set it. Set it
	// before the first call.
	Sleep func(ctx context.Context, d time.Duration) error
	// Jitter returns a random value in [0,1). Nil uses math/rand/v2. Tests set it.
	Jitter func() float64

	now func() time.Time

	mu       sync.Mutex
	fails    map[healthKey]failure // the last failure of each entry and model
	quota    map[string]failure    // the quota record of each entry
	hold     map[string]time.Time  // the end of the retry hold of each entry
	noEffort map[healthKey]bool    // the entries that reject the effort field
}

// NewHealth returns an empty Health. A nil now uses time.Now.
func NewHealth(now func() time.Time) *Health {
	if now == nil {
		now = time.Now
	}
	return &Health{
		now:      now,
		fails:    map[healthKey]failure{},
		quota:    map[string]failure{},
		hold:     map[string]time.Time{},
		noEffort: map[healthKey]bool{},
	}
}

// sleep waits d, and returns the error of ctx when ctx ends first.
func (h *Health) sleep(ctx context.Context, d time.Duration) error {
	if h.Sleep != nil {
		return h.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// jitter returns a random value in [0,1).
func (h *Health) jitter() float64 {
	if h.Jitter != nil {
		return h.Jitter()
	}
	return rand.Float64()
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

// skip returns the skip time of a failed entry.
func (h *Health) skip() time.Duration {
	if h.Skip > 0 {
		return h.Skip
	}
	return waitTime
}

// waiting reports whether the model skip of the entry is active, or the quota skip of the entry.
func (h *Health) waiting(name, model string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	if f, ok := h.fails[healthKey{name, model}]; ok && f.until.After(now) {
		return true
	}
	return h.inQuotaLocked(name, now)
}

// inQuota reports whether the quota skip of the entry is active.
func (h *Health) inQuota(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.inQuotaLocked(name, h.now())
}

func (h *Health) inQuotaLocked(name string, now time.Time) bool {
	q, ok := h.quota[name]
	return ok && q.until.After(now)
}

// last returns the last failure of the entry and model. When the entry has a newer quota
// record, last returns the quota record, so a quota that another model found shows too.
// During a quota skip, last always returns the quota record, because the skip is its cause.
func (h *Health) last(name, model string) (failure, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f, ok := h.fails[healthKey{name, model}]
	if q, qok := h.quota[name]; qok && (!ok || q.at.After(f.at) || q.until.After(h.now())) {
		return q, true
	}
	return f, ok
}

// fail records a failure of the entry with the short cause of err. A zero until means now plus
// the skip time. The record keeps a later end time that it holds already.
func (h *Health) fail(name, model string, err error, until time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	if until.IsZero() {
		until = now.Add(h.skip())
	}
	k := healthKey{name, model}
	if old, ok := h.fails[k]; ok && old.until.After(until) {
		until = old.until
	}
	h.fails[k] = failure{at: now, cause: ShortCause(err), until: until}
}

// failTimeout records a Timeout-class failure. The skip grows with each timeout in a row.
func (h *Health) failTimeout(name, model string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	k := healthKey{name, model}
	old, ok := h.fails[k]
	n := 1
	if ok && old.timeout {
		n = old.count + 1
	}
	until := now.Add(growingSkip(n))
	if ok && old.until.After(until) {
		until = old.until
	}
	h.fails[k] = failure{at: now, cause: ShortCause(err), until: until, timeout: true, count: n}
}

// failQuota records a used-up quota of the entry. The count goes up only outside a quota skip.
// Thus calls that get the error at the same moment count as one. In a skip, a
// zero resetAt keeps the reset time that the record has. The result is true when the call
// started a quota episode.
func (h *Health) failQuota(name, model string, err error, resetAt time.Time) (episode bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	q := h.quota[name]
	inSkip := q.until.After(now)
	if !inSkip {
		q.count++
		if q.count == 1 {
			q.notice, episode = true, true
		}
		q.until = time.Time{}
	}
	end := resetAt
	if end.IsZero() {
		end = now.Add(growingSkip(q.count))
	}
	if end.After(q.until) {
		q.until = end
	}
	if !resetAt.IsZero() || !inSkip {
		q.resetAt = resetAt
	}
	q.at, q.cause, q.model, q.err = now, ShortCause(err), model, err
	h.quota[name] = q
	return episode
}

// quotaErr returns the stored quota error of the entry, or nil.
func (h *Health) quotaErr(name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.quota[name].err
}

// takeNotice returns the reset time when a quota notice of the entry is pending, and clears it.
// A notice whose quota skip ended goes away with no result, because its reset time is old.
func (h *Health) takeNotice(name string) (resetAt time.Time, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	q, found := h.quota[name]
	if !found || !q.notice {
		return time.Time{}, false
	}
	q.notice = false
	h.quota[name] = q
	if !q.until.After(h.now()) {
		return time.Time{}, false
	}
	return q.resetAt, true
}

// clear removes the failures of the entry after a success. It keeps a failure that is newer
// than started, because the call began before that failure. A removed quota record ends the
// quota episode, its count, and its pending notice.
func (h *Health) clear(name, model string, started time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	k := healthKey{name, model}
	if f, ok := h.fails[k]; ok && !f.at.After(started) {
		delete(h.fails, k)
	}
	if q, ok := h.quota[name]; ok && !q.at.After(started) {
		delete(h.quota, name)
	}
}

// holdUntil returns the end of the retry hold of the entry, or the zero time.
func (h *Health) holdUntil(name string) time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hold[name]
}

// setHold sets the end of the retry hold of the entry. It keeps the later time.
func (h *Health) setHold(name string, until time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if until.After(h.hold[name]) {
		h.hold[name] = until
	}
}

// HealthRow is the last failure of one entry and model. SkipUntil is the end of the skip, or
// the zero time when the skip ended.
type HealthRow struct {
	Entry, Model, Cause string
	FailedAt, SkipUntil time.Time
}

// Snapshot returns the last failure of each entry and model, sorted by entry, then by model.
// A quota record shows as one row of its entry and model. The dashboard Models page reads it.
func (h *Health) Snapshot(now time.Time) []HealthRow {
	h.mu.Lock()
	rows := make(map[healthKey]HealthRow, len(h.fails)+len(h.quota))
	row := func(k healthKey, f failure) HealthRow {
		r := HealthRow{Entry: k.name, Model: k.model, Cause: f.cause, FailedAt: f.at}
		if f.until.After(now) {
			r.SkipUntil = f.until
		}
		return r
	}
	for k, f := range h.fails {
		rows[k] = row(k, f)
	}
	for name, q := range h.quota {
		k := healthKey{name, q.model}
		r := row(k, q)
		if old, ok := rows[k]; ok {
			if old.FailedAt.After(r.FailedAt) {
				r.Cause, r.FailedAt = old.Cause, old.FailedAt
			}
			if old.SkipUntil.After(r.SkipUntil) {
				r.SkipUntil = old.SkipUntil
			}
		}
		rows[k] = r
	}
	h.mu.Unlock()
	out := make([]HealthRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b HealthRow) int {
		if c := strings.Compare(a.Entry, b.Entry); c != 0 {
			return c
		}
		return strings.Compare(a.Model, b.Model)
	})
	return out
}
