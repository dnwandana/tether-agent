package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// failing returns a provider that fails before the first byte with HTTP code.
type failing struct {
	name  string
	code  int
	calls atomic.Int32
}

func (f *failing) Name() string  { return f.name }
func (f *failing) Model() string { return f.name + "-model" }
func (f *failing) Stream(context.Context, Request) (<-chan Event, error) {
	f.calls.Add(1)
	return nil, fmt.Errorf("provider %s: %w: %w", f.name, ErrBeforeFirstByte, &HTTPStatusError{Code: f.code})
}

// plain400 is a provider that fails with a plain HTTP 400, which is not a
// fallback trigger.
type plain400 struct{}

func (plain400) Name() string  { return "primary" }
func (plain400) Model() string { return "primary-model" }
func (plain400) Stream(context.Context, Request) (<-chan Event, error) {
	return nil, fmt.Errorf("provider primary: %w", &HTTPStatusError{Code: 400})
}

// clock is a fake time source for Health.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// chainOf returns a chain of ps with no pins and a quiet log.
func chainOf(h *Health, ps ...Provider) *Chain {
	var es []ChainEntry
	for _, p := range ps {
		es = append(es, ChainEntry{Name: p.Name(), P: p})
	}
	return NewChain(es, h, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// mustCollect streams req through p and fails the test on an error.
func mustCollect(t *testing.T, p Provider, req Request) Result {
	t.Helper()
	ch, err := p.Stream(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Collect(ch, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestChainMovesOnAndWaits(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	h := NewHealth(clk.now)
	a, b := &failing{name: "primary", code: 503}, &failing{name: "deepseek", code: 429}
	c := &stub{name: "openrouter"}
	ch := chainOf(h, a, b, c)
	r := mustCollect(t, ch, Request{})
	eq(t, r.Provider, "openrouter")
	st := ch.FallbackStatus()
	if !st.Active || st.FirstName != "primary" || st.FallbackName != "openrouter" || st.Cause != "HTTP 503" ||
		st.PrimaryModel != "primary-model" || st.FallbackModel != "stub-model" || !st.FailedAt.Equal(time.Unix(1000, 0)) {
		t.Fatalf("%+v", st)
	}
	clk.add(30 * time.Second)
	mustCollect(t, ch, Request{})
	if a.calls.Load() != 1 || b.calls.Load() != 1 {
		t.Fatal("an entry in its wait time got a call")
	}
	clk.add(31 * time.Second)
	mustCollect(t, ch, Request{})
	if a.calls.Load() != 2 {
		t.Fatal("after 60 s the first entry must get the call")
	}
}

func TestChainSuccessClearsFailure(t *testing.T) {
	clk := &clock{t: time.Unix(0, 0)}
	h := NewHealth(clk.now)
	chainOf(h, &failing{name: "primary", code: 503}, &stub{name: "b"}).Stream(ctx, Request{})
	ok := chainOf(h, &stub{name: "primary"}, &stub{name: "b"})
	ok.entries[0].Model = "primary-model"
	if !ok.FallbackStatus().Active {
		t.Fatal("the failure must show before the wait time ends")
	}
	clk.add(61 * time.Second)
	mustCollect(t, ok, Request{})
	if st := ok.FallbackStatus(); st.Active || st.FallbackName != "primary" {
		t.Fatalf("%+v", st)
	}
}

func TestChainStopsOnPlainErrorAndCancel(t *testing.T) {
	h := NewHealth(nil)
	bad := &failing{name: "primary", code: 400}
	next := &stub{name: "fallback"}
	_, err := chainOf(h, plain400{}, next).Stream(ctx, Request{})
	if err == nil || next.calls.Load() != 0 {
		t.Fatal("a plain 400 must not move on")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := chainOf(h, bad, next).Stream(cctx, Request{}); err == nil || next.calls.Load() != 0 {
		t.Fatal("a cancel must not move on")
	}
	if chainOf(h, bad, next).FallbackStatus().Active {
		t.Fatal("a cancel must not mark a failure")
	}
}

func TestChainAllFailJoinsAndAllWaitingStillTries(t *testing.T) {
	clk := &clock{t: time.Unix(0, 0)}
	h := NewHealth(clk.now)
	a, b := &failing{name: "a", code: 503}, &failing{name: "b", code: 502}
	_, err := chainOf(h, a, b).Stream(ctx, Request{})
	if err == nil || !strings.Contains(err.Error(), "provider a") || !strings.Contains(err.Error(), "provider b") {
		t.Fatal(err)
	}
	_, _ = chainOf(h, a, b).Stream(ctx, Request{})
	if a.calls.Load() != 2 || b.calls.Load() != 2 {
		t.Fatal("all entries wait: the chain must still try them")
	}
}

func TestChainDownWhenAllEntriesWait(t *testing.T) {
	clk := &clock{t: time.Unix(0, 0)}
	h := NewHealth(clk.now)
	a, b := &failing{name: "a", code: 503}, &failing{name: "b", code: 502}
	if chainOf(h, a, b).Down() {
		t.Fatal("a chain with no failure is not down")
	}
	_, _ = chainOf(h, a, &stub{name: "b"}).Stream(ctx, Request{})
	if chainOf(h, a, b).Down() {
		t.Fatal("one entry works, so the chain is not down")
	}
	_, _ = chainOf(h, a, b).Stream(ctx, Request{})
	if !chainOf(h, a, b).Down() {
		t.Fatal("all entries wait, so the chain is down")
	}
	clk.add(61 * time.Second)
	if chainOf(h, a, b).Down() {
		t.Fatal("after the wait time the chain is not down")
	}
}

func TestChainStartAtAndPin(t *testing.T) {
	h := NewHealth(nil)
	var gotModel []string
	rec := func(name string) Provider { return &recorder{name: name, models: &gotModel} }
	es := []ChainEntry{{Name: "deepseek", P: rec("deepseek"), Model: "deepseek-reasoner"}, {Name: "primary", P: rec("primary")}, {Name: "openrouter", P: rec("openrouter")}}
	ch := NewChain(es, h, nil)
	r := mustCollect(t, ch, Request{StartAt: "openrouter"})
	eq(t, r.Provider, "openrouter")
	mustCollect(t, ch, Request{Model: "ignored"})
	eq(t, strings.Join(gotModel, ","), ",deepseek-reasoner") // a fallback entry sends no model; the pin goes only to the first
	eq(t, ch.Name(), "deepseek")
	eq(t, ch.Model(), "deepseek-reasoner")
}

func TestChainStartAtWrapsAround(t *testing.T) {
	h := NewHealth(nil)
	a, b := &stub{name: "a"}, &failing{name: "b", code: 503}
	c := &failing{name: "c", code: 503}
	r := mustCollect(t, chainOf(h, a, b, c), Request{StartAt: "b"})
	eq(t, r.Provider, "a")
	if b.calls.Load() != 1 || c.calls.Load() != 1 {
		t.Fatal("the chain must try the start entry, then the entries after it")
	}
}

func TestHealthIsPerEntryAndModel(t *testing.T) {
	h := NewHealth(nil)
	pinned := NewChain([]ChainEntry{{Name: "primary", P: &failing{name: "primary", code: 403}, Model: "glm-5"}, {Name: "openrouter", P: &stub{name: "openrouter"}}}, h, slog.New(slog.DiscardHandler))
	mustCollect(t, pinned, Request{})
	if !pinned.FallbackStatus().Active {
		t.Fatal("the pinned chain must report its failure")
	}
	def := NewChain([]ChainEntry{{Name: "primary", P: &stub{name: "primary"}}}, h, nil)
	if st := def.FallbackStatus(); st.Active {
		t.Fatal("a 403 for primary:glm-5 marked primary with its own model")
	}
}

func TestHealthParallel(t *testing.T) {
	h := NewHealth(nil)
	ch := chainOf(h, &failing{name: "a", code: 503}, &stub{name: "b"})
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() { defer wg.Done(); mustCollect(t, ch, Request{}); _ = ch.FallbackStatus() }()
	}
	wg.Wait()
}

// effortProv records req.Effort and the image count, and answers HTTP 400
// "unknown parameter: reasoning_effort" while reject is true and the effort
// is not empty.
type effortProv struct {
	name    string
	reject  bool
	efforts []string
	images  []int
}

func (p *effortProv) Name() string  { return p.name }
func (p *effortProv) Model() string { return p.name + "-model" }
func (p *effortProv) Stream(_ context.Context, req Request) (<-chan Event, error) {
	p.efforts = append(p.efforts, req.Effort)
	n := 0
	for _, m := range req.Messages {
		n += len(m.Images)
	}
	p.images = append(p.images, n)
	if p.reject && req.Effort != "" {
		return nil, fmt.Errorf("provider %s: %w: unknown parameter", p.name, &HTTPStatusError{Code: 400, Body: "unknown parameter: reasoning_effort"})
	}
	return (&stub{name: p.name}).Stream(ctx, req)
}

// always400 answers HTTP 400 with the effort body for every effort.
type always400 struct{ *effortProv }

func (a *always400) Stream(_ context.Context, req Request) (<-chan Event, error) {
	a.efforts = append(a.efforts, req.Effort)
	return nil, fmt.Errorf("provider %s: %w: unknown parameter", a.name, &HTTPStatusError{Code: 400, Body: "unknown parameter: reasoning_effort"})
}

func TestChainEffortFill(t *testing.T) {
	b := &effortProv{name: "openrouter"}
	ch := NewChain([]ChainEntry{{Name: "primary", P: &failing{name: "primary", code: 503}, Effort: "high"}, {Name: "openrouter", P: b, Effort: "low"}}, NewHealth(nil), slog.New(slog.DiscardHandler))
	mustCollect(t, ch, Request{})
	mustCollect(t, ch, Request{Effort: "max"})
	eq(t, strings.Join(b.efforts, ","), "low,max") // the config value of the serving entry, then the session value
	eq(t, ch.First().Effort, "high")
}

func TestChainEffortSafetyNet(t *testing.T) {
	var logBuf bytes.Buffer
	h := NewHealth(nil)
	p := &effortProv{name: "primary", reject: true}
	ch := NewChain([]ChainEntry{{Name: "primary", P: p}}, h, slog.New(slog.NewTextHandler(&logBuf, nil)))
	r := mustCollect(t, ch, Request{Effort: "none"})
	eq(t, r.Provider, "primary")
	eq(t, strings.Join(p.efforts, ","), "none,")
	if !strings.Contains(logBuf.String(), `model primary-model rejects the effort \"none\". The provider default applies`) ||
		!h.RejectsEffort("primary", "primary-model") || ch.FallbackStatus().Active {
		t.Fatal(logBuf.String())
	}
	mustCollect(t, ch, Request{Effort: "none"})
	eq(t, strings.Join(p.efforts, ","), "none,,") // the next call sends no effort
}

func TestChainSecond400IsNormalError(t *testing.T) {
	p := &effortProv{name: "primary", reject: true}
	// The provider rejects the call also with no effort. The chain returns
	// that error and does not try again.
	always := &always400{effortProv: p}
	if _, err := NewChain([]ChainEntry{{Name: "primary", P: always}}, NewHealth(nil), slog.New(slog.DiscardHandler)).Stream(ctx, Request{Effort: "low"}); err == nil {
		t.Fatal("want the second 400")
	}
	eq(t, len(p.efforts), 2)
}

func TestChainNoSafetyNetWithoutEffort(t *testing.T) {
	p := &effortProv{name: "primary"}
	always := &always400{effortProv: p}
	if _, err := NewChain([]ChainEntry{{Name: "primary", P: always}}, NewHealth(nil), nil).Stream(ctx, Request{}); err == nil {
		t.Fatal("want the 400")
	}
	eq(t, len(p.efforts), 1)
}

func TestChainStripsImagesForNoVisionEntry(t *testing.T) {
	b := &effortProv{name: "openrouter"}
	a := &effortProv{name: "primary"}
	ch := NewChain([]ChainEntry{{Name: "primary", P: &failing{name: "primary", code: 503}, Vision: true}, {Name: "openrouter", P: b}}, NewHealth(nil), slog.New(slog.DiscardHandler))
	img := Message{Role: RoleUser, Content: "look [image: /tmp/a.png]", Images: []Image{{Path: "/tmp/a.png", MIME: "image/png", Data: []byte{1}}}}
	msgs := []Message{img}
	mustCollect(t, ch, Request{Messages: msgs})
	eq(t, b.images[0], 0)
	eq(t, len(msgs[0].Images), 1) // the slice of the caller does not change
	mustCollect(t, NewChain([]ChainEntry{{Name: "primary", P: a, Vision: true}}, NewHealth(nil), nil), Request{Messages: msgs})
	eq(t, a.images[0], 1)
}

func TestChainVisionCallSkipsNoVisionEntry(t *testing.T) {
	text := &effortProv{name: "openrouter"}
	see := &effortProv{name: "vision"}
	img := Message{Role: RoleUser, Content: "what is in it", Images: []Image{{MIME: "image/png", Data: []byte{1}}}}
	ch := NewChain([]ChainEntry{
		{Name: "primary", P: &failing{name: "primary", code: 503}, Vision: true},
		{Name: "openrouter", P: text},
		{Name: "vision", P: see, Vision: true},
	}, NewHealth(nil), nil)
	r := mustCollect(t, ch, Request{Messages: []Message{img}, NeedVision: true})
	eq(t, r.Provider, "vision")
	eq(t, len(text.efforts), 0) // the entry with no vision got no call
	eq(t, see.images[0], 1)

	none := NewChain([]ChainEntry{{Name: "primary", P: text}}, NewHealth(nil), nil)
	if _, err := none.Stream(ctx, Request{Messages: []Message{img}, NeedVision: true}); !errors.Is(err, ErrBeforeFirstByte) {
		t.Fatal(err)
	}
}

func TestChainEffortNotRecordedWhenRetryFails(t *testing.T) {
	h := NewHealth(nil)
	always := &always400{effortProv: &effortProv{name: "primary"}}
	_, _ = NewChain([]ChainEntry{{Name: "primary", P: always}}, h, nil).Stream(ctx, Request{Effort: "low"})
	if h.RejectsEffort("primary", "primary-model") {
		t.Fatal("a 400 with another cause must not turn off the effort")
	}
}
