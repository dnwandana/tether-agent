package cron

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// briefSend is one send of the daily status.
type briefSend struct {
	to      Target
	text    string
	noSound bool
}

// briefRec records the builds and the sends of the daily status.
type briefRec struct {
	mu      sync.Mutex
	builds  int
	sends   []briefSend
	sendErr error
	text    string
	empty   bool
	// block, when not nil, holds each build until it closes.
	block chan struct{}
}

func (b *briefRec) brief(context.Context) (string, bool, error) {
	b.mu.Lock()
	b.builds++
	block := b.block
	b.mu.Unlock()
	if block != nil {
		<-block
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text, b.empty, nil
}

func (b *briefRec) send(_ context.Context, to Target, text string, _ []string, noSound bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sendErr != nil {
		return b.sendErr
	}
	b.sends = append(b.sends, briefSend{to: to, text: text, noSound: noSound})
	return nil
}

func (b *briefRec) counts() (builds, sends int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.builds, len(b.sends)
}

// briefSched returns the scheduler fixture with a daily status at 09:00 and the owner chat 42.
// The clock is 2026-10-27 10:00 UTC.
func briefSched(t *testing.T) (*schedFixture, *clock, *briefRec) {
	t.Helper()
	s, c := scheduler(t)
	b := &briefRec{text: "1 thing needs you\n⏸ old has a bad schedule"}
	s.Brief, s.BriefAt = b.brief, 9*60
	s.Deliver.Send = b.send
	s.Deliver.OwnerHome = func(context.Context) (Target, bool) { return Target{ChatID: "42"}, true }
	return s, c, b
}

// briefDay returns the value of the kv key cron.brief_last.
func briefDay(t *testing.T, s *Scheduler) string {
	t.Helper()
	v, _, err := s.Store.DB.KVGet(ctx, BriefKVKey)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// The status goes to the owner chat, redacted, once a day. A restart does not send it again.
func TestBriefSendsOncePerDay(t *testing.T) {
	s, c, b := briefSched(t)
	s.Deliver.Redact = func(x string) string { return strings.ReplaceAll(x, "old", "[x]") }
	s.sendBrief(ctx)
	want := briefSend{to: Target{Source: "telegram", ChatID: "42"}, text: "1 thing needs you\n⏸ [x] has a bad schedule"}
	if len(b.sends) != 1 || b.sends[0] != want || briefDay(t, s.Scheduler) != "2026-10-27" {
		t.Fatalf("%+v %q", b.sends, briefDay(t, s.Scheduler))
	}
	s.sendBrief(ctx)
	restarted := &Scheduler{Store: s.Store, Deliver: s.Deliver, Brief: b.brief, BriefAt: 9 * 60, Now: c.Now}
	restarted.sendBrief(ctx)
	if builds, sends := b.counts(); builds != 1 || sends != 1 {
		t.Fatal("the same day must not build or send again:", builds, sends)
	}
	c.t = time.Date(2026, 10, 28, 9, 30, 0, 0, time.UTC)
	s.sendBrief(ctx)
	if _, sends := b.counts(); sends != 2 || briefDay(t, s.Scheduler) != "2026-10-28" {
		t.Fatal(sends, briefDay(t, s.Scheduler))
	}
}

// The window is [brief_at, brief_at + 2 h). A window can cross midnight.
func TestBriefWindow(t *testing.T) {
	for _, tc := range []struct {
		at      int
		now     time.Time
		wantDay string // "" means no send
	}{
		{9 * 60, time.Date(2026, 10, 27, 8, 59, 0, 0, time.UTC), ""},
		{9 * 60, time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC), "2026-10-27"},
		{9 * 60, time.Date(2026, 10, 27, 10, 59, 0, 0, time.UTC), "2026-10-27"},
		{9 * 60, time.Date(2026, 10, 27, 11, 0, 0, 0, time.UTC), ""},
		{23 * 60, time.Date(2026, 10, 28, 0, 30, 0, 0, time.UTC), "2026-10-27"},
		{23 * 60, time.Date(2026, 10, 28, 1, 30, 0, 0, time.UTC), ""},
	} {
		s, c, b := briefSched(t)
		s.BriefAt, c.t = tc.at, tc.now
		s.sendBrief(ctx)
		_, sends := b.counts()
		if got := briefDay(t, s.Scheduler); got != tc.wantDay || (sends == 1) != (tc.wantDay != "") {
			t.Errorf("at %d, now %s: day %q, %d sends; want day %q", tc.at, tc.now, got, sends, tc.wantDay)
		}
	}
}

// With nothing to send, the check writes the date and sends nothing. It never sends
// "Nothing needs you."
func TestBriefEmptyWritesDate(t *testing.T) {
	s, _, b := briefSched(t)
	b.text, b.empty = "Nothing needs you.", true
	s.sendBrief(ctx)
	s.sendBrief(ctx)
	if builds, sends := b.counts(); builds != 1 || sends != 0 || briefDay(t, s.Scheduler) != "2026-10-27" {
		t.Fatal(builds, sends, briefDay(t, s.Scheduler))
	}
}

// A failed send logs one warning and writes nothing, so the next tick in the window tries
// again.
func TestBriefRetryAfterFailedSend(t *testing.T) {
	s, c, b := briefSched(t)
	var logs bytes.Buffer
	s.Log = slog.New(slog.NewTextHandler(&logs, nil))
	b.sendErr = errors.New("flood wait")
	s.sendBrief(ctx)
	if briefDay(t, s.Scheduler) != "" || strings.Count(logs.String(), "daily status did not go out") != 1 {
		t.Fatal(briefDay(t, s.Scheduler), logs.String())
	}
	b.sendErr = nil
	c.t = c.t.Add(30 * time.Minute)
	s.sendBrief(ctx)
	if builds, sends := b.counts(); builds != 2 || sends != 1 || briefDay(t, s.Scheduler) != "2026-10-27" {
		t.Fatal(builds, sends)
	}
}

// In the quiet hours, the status goes out with no sound.
func TestBriefNoSoundInQuietHours(t *testing.T) {
	s, _, b := briefSched(t)
	s.Quiet = func(time.Time) bool { return true }
	s.sendBrief(ctx)
	if len(b.sends) != 1 || !b.sends[0].noSound {
		t.Fatalf("%+v", b.sends)
	}
}

// TickOnce and RunNow never start the daily status.
func TestBriefNotFromTickOnceOrRunNow(t *testing.T) {
	s, _, b := briefSched(t)
	s.TickOnce(ctx)
	id := mustCreate(t, s.Store, Job{Name: "a", Schedule: "30m", Prompt: "x", Enabled: true})
	if err := s.RunNow(ctx, id); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	if builds, _ := b.counts(); builds != 0 {
		t.Fatal(builds)
	}
}

// Ticks that overlap a slow build start no second build, and the owner gets one status.
func TestBriefOnceWithOverlappingTicks(t *testing.T) {
	s, _, b := briefSched(t)
	b.block = make(chan struct{})
	s.Tick = time.Millisecond
	ticks := make(chan struct{}, 64)
	s.afterTick = func() {
		select {
		case ticks <- struct{}{}:
		default:
		}
	}
	waitTicks := func(n int) {
		t.Helper()
		for range n {
			select {
			case <-ticks:
			case <-time.After(10 * time.Second):
				t.Fatal("no tick")
			}
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { s.Start(runCtx); close(done) }()
	waitTicks(5)
	close(b.block)
	for _, sends := b.counts(); sends == 0; _, sends = b.counts() {
		waitTicks(1)
	}
	waitTicks(5)
	cancel()
	<-done
	if builds, sends := b.counts(); builds != 1 || sends != 1 {
		t.Fatalf("%d builds, %d sends; want 1 and 1", builds, sends)
	}
}

// With no owner chat or no send function, Start logs one warning and the check does nothing.
func TestBriefWarnsAtStartWithoutTarget(t *testing.T) {
	for name, set := range map[string]func(d *Deliverer){
		"nil OwnerHome": func(d *Deliverer) { d.OwnerHome = nil },
		"no owner chat": func(d *Deliverer) {
			d.OwnerHome = func(context.Context) (Target, bool) { return Target{}, false }
		},
		"nil Send": func(d *Deliverer) { d.Send = nil },
	} {
		s, _, b := briefSched(t)
		set(s.Deliver)
		var logs bytes.Buffer
		s.Log = slog.New(slog.NewTextHandler(&logs, nil))
		startOnce(t, s.Scheduler)
		if builds, _ := b.counts(); builds != 0 || strings.Count(logs.String(), "the daily status is off") != 1 {
			t.Errorf("%s: %d builds, logs:\n%s", name, builds, logs.String())
		}
	}
}
