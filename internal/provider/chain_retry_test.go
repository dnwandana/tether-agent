package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// discardLog is a log that drops each line.
var discardLog = slog.New(slog.DiscardHandler)

// sleepLog records each wait of a Health. With block set, a wait also blocks until release
// closes or the context ends.
type sleepLog struct {
	mu      sync.Mutex
	waits   []time.Duration
	block   bool
	release chan struct{}
}

func (s *sleepLog) sleep(c context.Context, d time.Duration) error {
	s.mu.Lock()
	s.waits = append(s.waits, d)
	block := s.block
	s.mu.Unlock()
	if block {
		select {
		case <-s.release:
		case <-c.Done():
		}
	}
	return c.Err()
}

func (s *sleepLog) list() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.waits)
}

// retryHealth returns a Health with the clock now (nil: time.Now), no jitter, and a sleep
// that records each wait and returns at once.
func retryHealth(now func() time.Time) (*Health, *sleepLog) {
	h := NewHealth(now)
	s := &sleepLog{release: make(chan struct{})}
	h.Sleep = s.sleep
	h.Jitter = func() float64 { return 0 }
	return h, s
}

// flaky is a fake provider that calls fail with the 1-based number of each call. A nil
// error answers like stub.
type flaky struct {
	name  string
	fail  func(n int) error
	calls atomic.Int32
}

func (f *flaky) Name() string  { return f.name }
func (f *flaky) Model() string { return f.name + "-model" }
func (f *flaky) Stream(c context.Context, req Request) (<-chan Event, error) {
	if err := f.fail(int(f.calls.Add(1))); err != nil {
		return nil, err
	}
	return (&stub{name: f.name}).Stream(c, req)
}

// statusErr returns the error of postJSON for an HTTP status with a Z.AI code and a
// Retry-After value.
func statusErr(name string, code int, zaiCode string, retryAfter time.Duration) error {
	return fmt.Errorf("provider %s: %w: %w", name, ErrBeforeFirstByte,
		&HTTPStatusError{Code: code, ProviderCode: zaiCode, RetryAfter: retryAfter})
}

// failEach fails each call with err. failFirst fails the first k calls with err.
func failEach(err error) func(int) error { return func(int) error { return err } }
func failFirst(k int, err error) func(int) error {
	return func(n int) error {
		if n <= k {
			return err
		}
		return nil
	}
}

// secs returns each value as seconds.
func secs(v ...int) []time.Duration {
	out := make([]time.Duration, len(v))
	for i, s := range v {
		out[i] = time.Duration(s) * time.Second
	}
	return out
}

func TestRetryZAIRateLimitThenOK(t *testing.T) {
	h, sl := retryHealth(nil)
	zai := &flaky{name: "zai", fail: failFirst(1, statusErr("zai", 429, "1302", 0))}
	next := &stub{name: "openrouter"}
	ch := NewChain([]ChainEntry{{Name: "zai", P: zai, ZAI: true}, {Name: "openrouter", P: next}}, h, discardLog)
	r := mustCollect(t, ch, Request{})
	eq(t, r.Provider, "zai")
	eq(t, zai.calls.Load(), int32(2))
	eq(t, next.calls.Load(), int32(0))
	if !slices.Equal(sl.list(), secs(2)) {
		t.Fatal(sl.list())
	}
	if ch.FallbackStatus().Active || len(h.Snapshot(time.Now())) != 0 {
		t.Fatal("a retry that works must not record a failure")
	}
}

func TestRetryThree503ThenNextEntry(t *testing.T) {
	var logBuf bytes.Buffer
	h, sl := retryHealth(nil)
	a, b := &failing{name: "primary", code: 503}, &stub{name: "openrouter"}
	ch := NewChain([]ChainEntry{{Name: "primary", P: a}, {Name: "openrouter", P: b}}, h, slog.New(slog.NewTextHandler(&logBuf, nil)))
	r := mustCollect(t, ch, Request{})
	eq(t, r.Provider, "openrouter")
	eq(t, a.calls.Load(), int32(3))
	if !slices.Equal(sl.list(), secs(2, 4)) {
		t.Fatal(sl.list())
	}
	if !ch.FallbackStatus().Active {
		t.Fatal("the last try must record the failure")
	}
	for _, want := range []string{
		`level=INFO msg="provider retry" entry=primary model=primary-model try=2 wait=2s cause="HTTP 503"`,
		`level=INFO msg="provider retry" entry=primary model=primary-model try=3 wait=4s cause="HTTP 503"`,
	} {
		if !strings.Contains(logBuf.String(), want) {
			t.Fatal(logBuf.String())
		}
	}
}

func TestRetryZAIOverloadLadder(t *testing.T) {
	overload := statusErr("zai", 429, "1305", 0)
	h, sl := retryHealth(nil)
	z := &flaky{name: "zai", fail: failEach(overload)}
	if _, err := NewChain([]ChainEntry{{Name: "zai", P: z, ZAI: true, CodingPlan: true}}, h, discardLog).Stream(ctx, Request{}); err == nil {
		t.Fatal("want the overload error")
	}
	eq(t, z.calls.Load(), int32(7))
	if !slices.Equal(sl.list(), secs(2, 4, 30, 60, 90, 120)) {
		t.Fatal(sl.list())
	}
	// The jitter adds 0 to 50% to a short wait, and 0 to 20% to a ladder wait.
	hj, slj := retryHealth(nil)
	hj.Jitter = func() float64 { return 0.5 }
	zj := &flaky{name: "zai", fail: failEach(overload)}
	_, _ = NewChain([]ChainEntry{{Name: "zai", P: zj, ZAI: true, CodingPlan: true}}, hj, discardLog).Stream(ctx, Request{})
	want := []time.Duration{2500 * time.Millisecond, 5 * time.Second, 33 * time.Second, 66 * time.Second, 99 * time.Second, 132 * time.Second}
	if !slices.Equal(slj.list(), want) {
		t.Fatal(slj.list())
	}
	// With a fallback, the entry gets no ladder.
	hf, slf := retryHealth(nil)
	zf := &flaky{name: "zai", fail: failEach(overload)}
	r := mustCollect(t, NewChain([]ChainEntry{{Name: "zai", P: zf, ZAI: true, CodingPlan: true}, {Name: "openrouter", P: &stub{name: "openrouter"}}}, hf, discardLog), Request{})
	eq(t, r.Provider, "openrouter")
	eq(t, zf.calls.Load(), int32(3))
	if !slices.Equal(slf.list(), secs(2, 4)) {
		t.Fatal(slf.list())
	}
	// The same code on an entry that is not a Z.AI entry is a plain HTTP 429: no ladder.
	hn, _ := retryHealth(nil)
	zn := &flaky{name: "relay", fail: failEach(statusErr("relay", 429, "1305", 0))}
	_, _ = NewChain([]ChainEntry{{Name: "relay", P: zn, CodingPlan: true}}, hn, discardLog).Stream(ctx, Request{})
	eq(t, zn.calls.Load(), int32(3))
}

func TestRetryAfter(t *testing.T) {
	// 5 s replaces the computed wait.
	h, sl := retryHealth(nil)
	a := &flaky{name: "primary", fail: failFirst(1, statusErr("primary", 429, "", 5*time.Second))}
	r := mustCollect(t, chainOf(h, a, &stub{name: "openrouter"}), Request{})
	eq(t, r.Provider, "primary")
	if !slices.Equal(sl.list(), secs(5)) {
		t.Fatal(sl.list())
	}
	// 30 s with a next entry: no retry, and a skip of 30 s.
	clk := &clock{t: time.Unix(1000, 0)}
	h2, sl2 := retryHealth(clk.now)
	b := &flaky{name: "primary", fail: failEach(statusErr("primary", 429, "", 30*time.Second))}
	r = mustCollect(t, chainOf(h2, b, &stub{name: "openrouter"}), Request{})
	eq(t, r.Provider, "openrouter")
	eq(t, b.calls.Load(), int32(1))
	eq(t, len(sl2.list()), 0)
	if rows := h2.Snapshot(clk.now()); len(rows) != 1 || !rows[0].SkipUntil.Equal(time.Unix(1030, 0)) {
		t.Fatalf("%+v", rows)
	}
	// 30 s with no next entry replaces the wait.
	h3, sl3 := retryHealth(nil)
	c := &flaky{name: "primary", fail: failEach(statusErr("primary", 429, "", 30*time.Second))}
	if _, err := chainOf(h3, c).Stream(ctx, Request{}); err == nil {
		t.Fatal("want the 429")
	}
	eq(t, c.calls.Load(), int32(3))
	if !slices.Equal(sl3.list(), secs(30, 30)) {
		t.Fatal(sl3.list())
	}
}

func TestRetryDeadline(t *testing.T) {
	h, sl := retryHealth(nil)
	a, b := &failing{name: "primary", code: 503}, &stub{name: "openrouter"}
	ch := chainOf(h, a, b)
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	s, err := ch.Stream(dctx, Request{})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := Collect(s, nil); err != nil || r.Provider != "openrouter" {
		t.Fatal(r, err)
	}
	eq(t, a.calls.Load(), int32(1))
	eq(t, len(sl.list()), 0)
	if !ch.FallbackStatus().Active {
		t.Fatal("the deadline rule must record the failure")
	}
}

func TestRetryHold(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	h, _ := retryHealth(clk.now)
	a := &flaky{name: "primary", fail: failFirst(1, statusErr("primary", 503, "", 0))}
	var at []int32 // the request count at each wait
	var waits []time.Duration
	h.Sleep = func(c context.Context, d time.Duration) error {
		at, waits = append(at, a.calls.Load()), append(waits, d)
		return c.Err()
	}
	ch := chainOf(h, a, &stub{name: "openrouter"})
	mustCollect(t, ch, Request{}) // a 503, a wait of 2 s, then a 200; the hold ends at 1002
	mustCollect(t, ch, Request{}) // in the hold: the wait comes before the request
	if !slices.Equal(at, []int32{1, 2}) || !slices.Equal(waits, secs(2, 2)) {
		t.Fatal(at, waits)
	}
	eq(t, a.calls.Load(), int32(3))
	if ch.FallbackStatus().Active || len(h.Snapshot(clk.now())) != 0 {
		t.Fatal("a hold is not a failure")
	}
	clk.add(3 * time.Second)
	mustCollect(t, ch, Request{}) // after the hold: no wait
	eq(t, len(waits), 2)
	eq(t, a.calls.Load(), int32(4))
}

// Ten calls hit the same 429 in one hold window. The nine calls that start during the hold
// send no request until the hold ends, so the primary gets 21 requests, not 30.
func TestRetryHoldTenCalls(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	h, sl := retryHealth(clk.now)
	a, b := &failing{name: "primary", code: 429}, &stub{name: "openrouter"}
	ch := chainOf(h, a, b)
	mustCollect(t, ch, Request{}) // 3 tries; the hold ends at 1004
	eq(t, a.calls.Load(), int32(3))
	// Lift the failure skip, so that the nine calls reach the entry and meet the hold only.
	h.clear("primary", "primary-model", clk.now())
	sl.mu.Lock()
	sl.block = true
	sl.mu.Unlock()
	var wg sync.WaitGroup
	for range 9 {
		wg.Go(func() {
			if _, err := ch.Stream(ctx, Request{}); err != nil {
				t.Error(err)
			}
		})
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(sl.list()) < 2+9 {
		if time.Now().After(deadline) {
			t.Fatal("the calls do not wait for the hold:", sl.list())
		}
		time.Sleep(time.Millisecond)
	}
	eq(t, a.calls.Load(), int32(3)) // no request during the hold
	if got := sl.list()[2:]; !slices.Equal(got, slices.Repeat(secs(4), 9)) {
		t.Fatal(got)
	}
	close(sl.release)
	wg.Wait()
	eq(t, a.calls.Load(), int32(3+9*2))
	eq(t, b.calls.Load(), int32(10))
}

func TestRetryCancelDuringWait(t *testing.T) {
	h, _ := retryHealth(nil)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	h.Sleep = func(c context.Context, _ time.Duration) error { cancel(); <-c.Done(); return c.Err() }
	a, b := &failing{name: "primary", code: 503}, &stub{name: "openrouter"}
	ch := chainOf(h, a, b)
	if _, err := ch.Stream(cctx, Request{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	eq(t, a.calls.Load(), int32(1))
	eq(t, b.calls.Load(), int32(0))
	if ch.FallbackStatus().Active {
		t.Fatal("a cancel must not record a failure")
	}
}

func TestRetryNextEntryErrors(t *testing.T) {
	billing := fmt.Errorf("provider primary: %w: %w", ErrBeforeFirstByte, &HTTPStatusError{Code: 429, Body: "insufficient balance"})
	for _, err := range []error{statusErr("primary", 401, "", 0), billing} {
		h, sl := retryHealth(nil)
		a := &flaky{name: "primary", fail: failEach(err)}
		r := mustCollect(t, chainOf(h, a, &stub{name: "openrouter"}), Request{})
		if r.Provider != "openrouter" || a.calls.Load() != 1 || len(sl.list()) != 0 {
			t.Fatal(err, r.Provider, a.calls.Load(), sl.list())
		}
	}
	h, sl := retryHealth(nil)
	bad := &unusable{name: "primary", model: "m", reason: "no API key"}
	r := mustCollect(t, NewChain([]ChainEntry{{Name: "primary", P: bad}, {Name: "openrouter", P: &stub{name: "openrouter"}}}, h, discardLog), Request{})
	if r.Provider != "openrouter" || len(sl.list()) != 0 {
		t.Fatal(r.Provider, sl.list())
	}
}

func TestRetryTimeout(t *testing.T) {
	timeout := fmt.Errorf("provider primary: %w: %w: %w", ErrBeforeFirstByte, ErrConnection, ErrHeaderTimeout)
	clk := &clock{t: time.Unix(1000, 0)}
	h, sl := retryHealth(clk.now)
	a := &flaky{name: "primary", fail: failEach(timeout)}
	r := mustCollect(t, chainOf(h, a, &stub{name: "openrouter"}), Request{})
	if r.Provider != "openrouter" || a.calls.Load() != 1 || len(sl.list()) != 0 {
		t.Fatal(r.Provider, a.calls.Load(), sl.list())
	}
	if rows := h.Snapshot(clk.now()); len(rows) != 1 || !rows[0].SkipUntil.Equal(time.Unix(1060, 0)) {
		t.Fatalf("%+v", rows)
	}
	// With no next entry: 2 tries.
	h2, sl2 := retryHealth(nil)
	b := &flaky{name: "primary", fail: failEach(timeout)}
	_, _ = chainOf(h2, b).Stream(ctx, Request{})
	eq(t, b.calls.Load(), int32(2))
	if !slices.Equal(sl2.list(), secs(2)) {
		t.Fatal(sl2.list())
	}
}

func TestRetryWaitNotice(t *testing.T) {
	var got []Notice
	nctx := WithNotify(ctx, func(n Notice) { got = append(got, n) })
	h, _ := retryHealth(nil)
	a := &flaky{name: "primary", fail: failEach(statusErr("primary", 429, "", 30*time.Second))}
	if _, err := chainOf(h, a).Stream(nctx, Request{}); err == nil {
		t.Fatal("want the 429")
	}
	want := Notice{Kind: NoticeWait, Entry: "primary", Model: "primary-model", Wait: 30 * time.Second, Cause: "HTTP 429"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("%+v", got)
	}
	// The ladder also sends one notice, before its first wait over 10 s.
	got = nil
	hz, _ := retryHealth(nil)
	z := &flaky{name: "zai", fail: failEach(statusErr("zai", 429, "1305", 0))}
	_, _ = NewChain([]ChainEntry{{Name: "zai", P: z, ZAI: true, CodingPlan: true}}, hz, discardLog).Stream(nctx, Request{})
	want = Notice{Kind: NoticeWait, Entry: "zai", Model: "zai-model", Wait: 30 * time.Second, Cause: "HTTP 429"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("%+v", got)
	}
	// A short retry gives no notice.
	got = nil
	hs, _ := retryHealth(nil)
	_, _ = chainOf(hs, &failing{name: "b", code: 503}).Stream(nctx, Request{})
	if len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// Calls that start while another call of the entry waits for its retry send no request until
// the hold ends. The primary gets 21 requests for ten calls, not 30.
func TestRetryHoldInFlight(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	h, sl := retryHealth(clk.now)
	sl.block = true
	a, b := &failing{name: "primary", code: 429}, &stub{name: "openrouter"}
	ch := chainOf(h, a, b)
	var wg sync.WaitGroup
	call := func() {
		if _, err := ch.Stream(ctx, Request{}); err != nil {
			t.Error(err)
		}
	}
	waitFor := func(n int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for len(sl.list()) < n {
			if time.Now().After(deadline) {
				t.Fatal("the calls do not wait:", sl.list())
			}
			time.Sleep(time.Millisecond)
		}
	}
	wg.Go(call)
	waitFor(1) // the first call waits 2 s for its retry; the hold ends at 1002
	for range 9 {
		wg.Go(call)
	}
	waitFor(10)
	eq(t, a.calls.Load(), int32(1)) // no request during the hold
	if got := sl.list(); !slices.Equal(got, slices.Repeat(secs(2), 10)) {
		t.Fatal(got)
	}
	close(sl.release)
	wg.Wait()
	eq(t, a.calls.Load(), int32(3+9*2))
	eq(t, b.calls.Load(), int32(10))
}

// A call with a next entry does not wait for a hold over 10 s. A call with no next entry,
// and a call that meets a short hold, waits.
func TestRetryLongHoldWithNextEntry(t *testing.T) {
	for _, c := range []struct {
		name string
		hold time.Duration
		next bool
		want []time.Duration
	}{
		{"long hold, next entry", 30 * time.Second, true, nil},
		{"long hold, no next entry", 30 * time.Second, false, secs(30)},
		{"short hold, next entry", 4 * time.Second, true, secs(4)},
	} {
		clk := &clock{t: time.Unix(1000, 0)}
		h, sl := retryHealth(clk.now)
		h.setHold("primary", clk.now().Add(c.hold))
		a := &stub{name: "primary"}
		ps := []Provider{a}
		if c.next {
			ps = append(ps, &stub{name: "openrouter"})
		}
		r := mustCollect(t, chainOf(h, ps...), Request{})
		if r.Provider != "primary" || !slices.Equal(sl.list(), c.want) {
			t.Fatal(c.name, r.Provider, sl.list())
		}
	}
}

// A Timeout-class retry sets no hold, so other calls of the entry do not wait.
func TestRetryTimeoutSetsNoHold(t *testing.T) {
	timeout := fmt.Errorf("provider primary: %w: %w: %w", ErrBeforeFirstByte, ErrConnection, ErrHeaderTimeout)
	clk := &clock{t: time.Unix(1000, 0)}
	h, sl := retryHealth(clk.now)
	a := &flaky{name: "primary", fail: failFirst(1, timeout)}
	mustCollect(t, chainOf(h, a), Request{})
	if !slices.Equal(sl.list(), secs(2)) || !h.holdUntil("primary").IsZero() {
		t.Fatal(sl.list(), h.holdUntil("primary"))
	}
}
