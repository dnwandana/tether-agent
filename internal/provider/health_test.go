package provider

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// errQuota1308 is a Z.AI quota error, and errE503 a plain HTTP 503.
var (
	errQuota1308 = fmt.Errorf("provider zai: %w: %w", ErrBeforeFirstByte, &HTTPStatusError{Code: 429, ProviderCode: "1308"})
	errE503      = fmt.Errorf("provider zai: %w: %w", ErrBeforeFirstByte, &HTTPStatusError{Code: 503})
)

// skipLeft returns the time from now to the end of the skip of the only Snapshot row.
func skipLeft(t *testing.T, h *Health, now time.Time) time.Duration {
	t.Helper()
	rows := h.Snapshot(now)
	if len(rows) != 1 {
		t.Fatalf("%+v", rows)
	}
	return rows[0].SkipUntil.Sub(now)
}

func TestHealthFailKeepsLaterEnd(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	h := NewHealth(clk.now)
	h.fail("zai", "glm-5.3", errE503, time.Unix(1600, 0))
	h.fail("zai", "glm-5.3", errE503, time.Time{}) // now + 60 s ends before 1600
	if rows := h.Snapshot(clk.now()); len(rows) != 1 || !rows[0].SkipUntil.Equal(time.Unix(1600, 0)) {
		t.Fatalf("%+v", rows)
	}
	// A 5xx with a 60 s skip cannot shorten a quota skip.
	h.failQuota("zai", "glm-5.3", errQuota1308, time.Unix(5000, 0))
	h.fail("zai", "glm-5.3", errE503, time.Time{})
	clk.add(1000 * time.Second)
	if !h.waiting("zai", "glm-5.3") || !h.inQuota("zai") {
		t.Fatal("a later failure shortened the quota skip")
	}
}

func TestHealthQuotaBackoff(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	h := NewHealth(clk.now)
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() { h.failQuota("zai", "glm-5.3", errQuota1308, time.Time{}) })
	}
	wg.Wait()
	// Five quota errors at the same moment count as one: the first skip is 60 s.
	eq(t, skipLeft(t, h, clk.now()), 60*time.Second)
	clk.add(61 * time.Second)
	if h.inQuota("zai") {
		t.Fatal("the first quota skip must end after 60 s")
	}
	h.failQuota("zai", "glm-5.3", errQuota1308, time.Time{})
	eq(t, skipLeft(t, h, clk.now()), 120*time.Second)
	for range 10 {
		clk.add(5 * time.Hour)
		h.failQuota("zai", "glm-5.3", errQuota1308, time.Time{})
	}
	eq(t, skipLeft(t, h, clk.now()), 4*time.Hour)
	// A known reset time sets the end.
	h2 := NewHealth(clk.now)
	reset := clk.now().Add(2 * time.Hour)
	h2.failQuota("zai", "glm-5.3", errQuota1308, reset)
	eq(t, skipLeft(t, h2, clk.now()), 2*time.Hour)
	if h2.quotaErr("zai") != errQuota1308 || h2.quotaErr("openrouter") != nil {
		t.Fatal(h2.quotaErr("zai"))
	}
}

func TestHealthQuotaIsEntryWide(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	h := NewHealth(clk.now)
	h.failQuota("zai", "glm-5.3", errQuota1308, time.Unix(1000+7200, 0))
	if !h.waiting("zai", "glm-5.3") || !h.waiting("zai", "glm-4.7") || h.waiting("openrouter", "glm-5.3") {
		t.Fatal("a quota skip is for the entry, with all its models")
	}
	ch := NewChain([]ChainEntry{{Name: "zai", P: &stub{name: "zai"}, Model: "glm-4.7"}, {Name: "openrouter", P: &stub{name: "openrouter"}}}, h, nil)
	st := ch.FallbackStatus()
	if !st.Active || st.Cause != "HTTP 429" || !st.FailedAt.Equal(time.Unix(1000, 0)) || st.FallbackName != "openrouter" {
		t.Fatalf("%+v", st)
	}
}

func TestHealthClearKeepsNewerFailure(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	h := NewHealth(clk.now)
	started := clk.now()
	clk.add(time.Second)
	h.failQuota("zai", "glm-5.3", errQuota1308, time.Time{})
	h.fail("zai", "glm-5.3", errE503, time.Time{})
	// A call that started before the quota ran out cannot remove the new skip.
	h.clear("zai", "glm-4.7", started)
	h.clear("zai", "glm-5.3", started)
	if !h.inQuota("zai") || !h.waiting("zai", "glm-5.3") {
		t.Fatal("clear removed a failure that is newer than the call")
	}
	h.clear("zai", "glm-5.3", clk.now())
	if h.inQuota("zai") || h.waiting("zai", "glm-5.3") || h.quotaErr("zai") != nil {
		t.Fatal("a success must clear the failures")
	}
	// The success ended the episode: the next skip is 60 s again, with a new notice.
	clk.add(time.Second)
	h.failQuota("zai", "glm-5.3", errQuota1308, time.Time{})
	eq(t, skipLeft(t, h, clk.now()), 60*time.Second)
	if _, ok := h.takeNotice("zai"); !ok {
		t.Fatal("a new episode must set a pending notice")
	}
}

func TestHealthTimeoutGrowingSkip(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	h := NewHealth(clk.now)
	timeout := fmt.Errorf("provider zai: %w: %w: %w", ErrBeforeFirstByte, ErrConnection, ErrHeaderTimeout)
	for _, want := range []time.Duration{60 * time.Second, 120 * time.Second, 240 * time.Second} {
		h.failTimeout("zai", "glm-5.3", timeout)
		eq(t, skipLeft(t, h, clk.now()), want)
		clk.add(want + time.Second)
	}
	for range 10 {
		h.failTimeout("zai", "glm-5.3", timeout)
		clk.add(5 * time.Hour)
	}
	h.failTimeout("zai", "glm-5.3", timeout)
	eq(t, skipLeft(t, h, clk.now()), 4*time.Hour)
	if h.waiting("zai", "glm-4.7") {
		t.Fatal("a timeout skip is for one model")
	}
	// A success clears the count.
	h.clear("zai", "glm-5.3", clk.now())
	clk.add(time.Second)
	h.failTimeout("zai", "glm-5.3", timeout)
	eq(t, skipLeft(t, h, clk.now()), 60*time.Second)
}

func TestHealthTakeNoticeOncePerEpisode(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	h := NewHealth(clk.now)
	if _, ok := h.takeNotice("zai"); ok {
		t.Fatal("no episode, no notice")
	}
	reset := time.Unix(1000+3600, 0)
	h.failQuota("zai", "glm-5.3", errQuota1308, reset)
	if got, ok := h.takeNotice("zai"); !ok || !got.Equal(reset) {
		t.Fatal(got, ok)
	}
	if _, ok := h.takeNotice("zai"); ok {
		t.Fatal("a notice goes one time")
	}
	// A new skip in the same episode sets no new notice.
	clk.add(3601 * time.Second)
	h.failQuota("zai", "glm-5.3", errQuota1308, time.Time{})
	if _, ok := h.takeNotice("zai"); ok {
		t.Fatal("a new skip in the same episode set a notice")
	}
}

func TestHealthHoldKeepsLater(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	h := NewHealth(clk.now)
	h.setHold("zai", time.Unix(1010, 0))
	h.setHold("zai", time.Unix(1005, 0))
	eq(t, h.holdUntil("zai"), time.Unix(1010, 0))
	h.setHold("zai", time.Unix(1020, 0))
	eq(t, h.holdUntil("zai"), time.Unix(1020, 0))
	if !h.holdUntil("openrouter").IsZero() {
		t.Fatal("a hold is for one entry")
	}
	if h.waiting("zai", "glm-5.3") || len(h.Snapshot(clk.now())) != 0 {
		t.Fatal("a hold is not a failure")
	}
}

func TestHealthSleepAndJitter(t *testing.T) {
	h := NewHealth(nil)
	if err := h.sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := h.sleep(cctx, time.Hour); !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatal("the end of the context must stop the wait at once:", err)
	}
	for range 100 {
		if j := h.jitter(); j < 0 || j >= 1 {
			t.Fatal(j)
		}
	}
	var got time.Duration
	h.Sleep = func(_ context.Context, d time.Duration) error { got = d; return nil }
	h.Jitter = func() float64 { return 0.25 }
	_ = h.sleep(context.Background(), 3*time.Second)
	eq(t, got, 3*time.Second)
	eq(t, h.jitter(), 0.25)
}

// In a quota skip, an error with no reset time keeps the known reset time. Only the call that
// starts the episode reports it.
func TestHealthQuotaKeepsKnownReset(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	h := NewHealth(clk.now)
	reset := time.Unix(1000+3600, 0)
	if !h.failQuota("zai", "glm-5.3", errQuota1308, reset) {
		t.Fatal("the first quota error starts an episode")
	}
	clk.add(time.Second)
	if h.failQuota("zai", "glm-5.3", errQuota1308, time.Time{}) {
		t.Fatal("a quota error in the skip starts no episode")
	}
	if got, ok := h.takeNotice("zai"); !ok || !got.Equal(reset) {
		t.Fatal(got, ok)
	}
}

// A pending notice whose quota skip ended goes away, so no notice names an old reset time.
func TestHealthOldNoticeGoesAway(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	h := NewHealth(clk.now)
	h.failQuota("zai", "glm-5.3", errQuota1308, time.Unix(1060, 0))
	clk.add(61 * time.Second)
	h.fail("zai", "glm-5.3", errE503, time.Time{})
	if got, ok := h.takeNotice("zai"); ok {
		t.Fatal("a notice of an ended skip:", got)
	}
}

// During a quota skip, the status shows the quota cause, also after a newer failure of
// another kind.
func TestHealthLastPrefersQuotaInSkip(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	h := NewHealth(clk.now)
	h.failQuota("zai", "glm-5.3", errQuota1308, time.Unix(1000+3600, 0))
	clk.add(time.Second)
	h.fail("zai", "glm-5.3", errE503, time.Time{})
	f, ok := h.last("zai", "glm-5.3")
	if !ok || f.cause != ShortCause(errQuota1308) {
		t.Fatal(f.cause, ok)
	}
	clk.add(3600 * time.Second)
	if f, _ := h.last("zai", "glm-5.3"); f.cause != ShortCause(errE503) {
		t.Fatal("after the skip, the newer failure shows:", f.cause)
	}
}
