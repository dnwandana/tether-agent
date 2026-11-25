package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// quotaHealth returns a Health with the clock now (nil: time.Now), no jitter, and a sleep
// that returns at once.
func quotaHealth(now func() time.Time) *Health {
	h := NewHealth(now)
	h.Sleep = func(c context.Context, _ time.Duration) error { return c.Err() }
	h.Jitter = func() float64 { return 0 }
	return h
}

// quotaFake is a fake provider that fails each call with err while err is not nil, and
// else answers like stub.
type quotaFake struct {
	name  string
	mu    sync.Mutex
	err   error
	calls atomic.Int32
}

func (q *quotaFake) set(err error) {
	q.mu.Lock()
	q.err = err
	q.mu.Unlock()
}

func (q *quotaFake) Name() string  { return q.name }
func (q *quotaFake) Model() string { return q.name + "-model" }
func (q *quotaFake) Stream(c context.Context, req Request) (<-chan Event, error) {
	q.calls.Add(1)
	q.mu.Lock()
	err := q.err
	q.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return (&stub{name: q.name}).Stream(c, req)
}

// quotaStatus returns the error of postJSON for an HTTP 429 with a Z.AI code, a reset time,
// and a Retry-After value.
func quotaStatus(name, code string, resetAt time.Time, retryAfter time.Duration) error {
	return fmt.Errorf("provider %s: %w: %w", name, ErrBeforeFirstByte,
		&HTTPStatusError{Code: 429, ProviderCode: code, ResetAt: resetAt, RetryAfter: retryAfter})
}

// zaiChain returns the chain [zai with the pin model, next] on h. A nil next gives a chain
// with one entry.
func zaiChain(h *Health, zai Provider, model string, next Provider) *Chain {
	es := []ChainEntry{{Name: "zai", P: zai, Model: model, ZAI: true, CodingPlan: true}}
	if next != nil {
		es = append(es, ChainEntry{Name: next.Name(), P: next})
	}
	return NewChain(es, h, slog.New(slog.DiscardHandler))
}

func TestQuotaSkipsEntryUntilReset(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	h := quotaHealth(clk.now)
	reset := time.Unix(1000+2*3600, 0)
	zai := &quotaFake{name: "zai", err: quotaStatus("zai", "1308", reset, 0)}
	next := &stub{name: "openrouter"}
	r := mustCollect(t, zaiChain(h, zai, "", next), Request{})
	eq(t, r.Provider, "openrouter")
	eq(t, zai.calls.Load(), int32(1))
	// The next calls skip the entry, for all its models, until the reset time.
	clk.add(2*time.Hour - time.Second)
	mustCollect(t, zaiChain(h, zai, "", next), Request{})
	mustCollect(t, zaiChain(h, zai, "glm-4.7", next), Request{})
	eq(t, zai.calls.Load(), int32(1))
	if st := zaiChain(h, zai, "", next).FallbackStatus(); !st.Active || st.FallbackName != "openrouter" {
		t.Fatalf("%+v", st)
	}
	if rows := h.Snapshot(clk.now()); len(rows) != 1 || !rows[0].SkipUntil.Equal(reset) {
		t.Fatalf("%+v", rows)
	}
	// After the reset time, the entry gets the call again, and the success ends the episode.
	clk.add(2 * time.Second)
	zai.set(nil)
	r = mustCollect(t, zaiChain(h, zai, "", next), Request{})
	eq(t, r.Provider, "zai")
	if h.inQuota("zai") || zaiChain(h, zai, "", next).FallbackStatus().Active {
		t.Fatal("a success must end the quota episode")
	}
}

func TestQuotaUnknownResetBacksOff(t *testing.T) {
	var logBuf bytes.Buffer
	clk := &clock{t: time.Unix(1000, 0)}
	h := quotaHealth(clk.now)
	body := `{"error":{"code":"1308","message":"Usage limit reached. Your limit will reset at soon"}}`
	zai := &quotaFake{name: "zai", err: fmt.Errorf("provider zai: %w: %w", ErrBeforeFirstByte, &HTTPStatusError{Code: 429, ProviderCode: "1308", Body: body})}
	next := &stub{name: "openrouter"}
	ch := NewChain([]ChainEntry{{Name: "zai", P: zai, ZAI: true}, {Name: "openrouter", P: next}}, h, slog.New(slog.NewTextHandler(&logBuf, nil)))
	for _, step := range []struct {
		add   time.Duration
		calls int32
	}{
		{0, 1},                 // the first quota skip: 60 s
		{59 * time.Second, 1},  //
		{2 * time.Second, 2},   // the second quota skip in a row: 120 s
		{119 * time.Second, 2}, //
		{2 * time.Second, 3},   //
	} {
		clk.add(step.add)
		mustCollect(t, ch, Request{})
		if got := zai.calls.Load(); got != step.calls {
			t.Fatalf("at %v: %d calls, want %d", clk.now(), got, step.calls)
		}
	}
	// The message with no readable reset time goes to the log one time in the episode.
	if n := strings.Count(logBuf.String(), `level=WARN msg="provider quota reset time not known"`); n != 1 {
		t.Fatal(logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "will reset at soon") {
		t.Fatal(logBuf.String())
	}
}

func TestQuotaAllEntriesWait(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	h := quotaHealth(clk.now)
	zai := &quotaFake{name: "zai", err: quotaStatus("zai", "1308", time.Time{}, 0)}
	next := &failing{name: "openrouter", code: 503}
	ch := zaiChain(h, zai, "", next)
	if _, err := ch.Stream(ctx, Request{}); err == nil {
		t.Fatal("want the joined error")
	}
	eq(t, zai.calls.Load(), int32(1))
	eq(t, next.calls.Load(), int32(3))
	// Both entries wait. The chain tries them, but not the entry in a quota skip.
	clk.add(10 * time.Second)
	if _, err := ch.Stream(ctx, Request{}); err == nil {
		t.Fatal("want the 503")
	}
	eq(t, zai.calls.Load(), int32(1))
	eq(t, next.calls.Load(), int32(6))
	// Only an entry in a quota skip is left: the chain returns its quota error and makes no call.
	_, err := zaiChain(h, zai, "", nil).Stream(ctx, Request{})
	if ErrorClass(err) != ClassQuota || !errors.Is(err, ErrBeforeFirstByte) {
		t.Fatal(err)
	}
	eq(t, zai.calls.Load(), int32(1))
}

// A cron run or a review finds the quota first. It has no notify function. The next chat
// call delivers the notice.
func TestQuotaNoticeReachesNextChatCall(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	h := quotaHealth(clk.now)
	reset := time.Unix(1000+3600, 0)
	zai := &quotaFake{name: "zai", err: quotaStatus("zai", "1308", reset, 0)}
	next := &stub{name: "openrouter"}
	var got []Notice
	nctx := WithNotify(ctx, func(n Notice) { got = append(got, n) })
	mustCollect(t, zaiChain(h, zai, "", next), Request{})
	eq(t, len(got), 0)
	s, err := zaiChain(h, zai, "", next).Stream(nctx, Request{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Collect(s, nil); err != nil {
		t.Fatal(err)
	}
	want := Notice{Kind: NoticeQuota, Entry: "zai", Model: "zai-model", NextEntry: "openrouter", NextModel: "stub-model", ResetAt: reset}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("%+v", got)
	}
	eq(t, zai.calls.Load(), int32(1))
	// The notice goes one time.
	s, _ = zaiChain(h, zai, "", next).Stream(nctx, Request{})
	_, _ = Collect(s, nil)
	eq(t, len(got), 1)
}

func TestQuotaNoticeOncePerEpisode(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	h := quotaHealth(clk.now)
	zai := &quotaFake{name: "zai", err: quotaStatus("zai", "1310", time.Time{}, 0)}
	next := &stub{name: "openrouter"}
	var got []Notice
	nctx := WithNotify(ctx, func(n Notice) { got = append(got, n) })
	call := func() {
		t.Helper()
		s, err := zaiChain(h, zai, "", next).Stream(nctx, Request{})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = Collect(s, nil)
	}
	call()
	if len(got) != 1 || got[0].Kind != NoticeQuota || !got[0].ResetAt.IsZero() {
		t.Fatalf("%+v", got)
	}
	// A new skip in the same episode sends no new notice.
	clk.add(61 * time.Second)
	call()
	eq(t, zai.calls.Load(), int32(2))
	eq(t, len(got), 1)
	// A success ends the episode. The next quota starts a new one, with a new notice.
	clk.add(121 * time.Second)
	zai.set(nil)
	call()
	zai.set(quotaStatus("zai", "1310", time.Time{}, 0))
	clk.add(time.Second)
	call()
	eq(t, zai.calls.Load(), int32(4))
	eq(t, len(got), 2)
	// With no next entry, a used-up quota gives no notice.
	got = nil
	if _, err := zaiChain(quotaHealth(clk.now), zai, "", nil).Stream(nctx, Request{}); err == nil {
		t.Fatal("want the quota error")
	}
	eq(t, len(got), 0)
}

func TestQuotaFromLongRetryAfter(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	h := quotaHealth(clk.now)
	a := &quotaFake{name: "primary", err: quotaStatus("primary", "", time.Time{}, 120*time.Second)}
	next := &stub{name: "openrouter"}
	var got []Notice
	nctx := WithNotify(ctx, func(n Notice) { got = append(got, n) })
	s, err := chainOf(h, a, next).Stream(nctx, Request{})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = Collect(s, nil)
	eq(t, a.calls.Load(), int32(1))
	if !h.inQuota("primary") || len(got) != 1 || !got[0].ResetAt.Equal(time.Unix(1120, 0)) {
		t.Fatalf("%+v", got)
	}
	clk.add(119 * time.Second)
	mustCollect(t, chainOf(h, a, next), Request{})
	eq(t, a.calls.Load(), int32(1))
	clk.add(2 * time.Second)
	mustCollect(t, chainOf(h, a, next), Request{})
	eq(t, a.calls.Load(), int32(2))
	// A Retry-After over 4 hours gives a skip of 4 hours.
	h2 := quotaHealth(clk.now)
	b := &quotaFake{name: "primary", err: quotaStatus("primary", "", time.Time{}, 10*time.Hour)}
	mustCollect(t, chainOf(h2, b, next), Request{})
	if rows := h2.Snapshot(clk.now()); len(rows) != 1 || rows[0].SkipUntil.Sub(clk.now()) != 4*time.Hour {
		t.Fatalf("%+v", rows)
	}
}
