package cron

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// heldFixture stores the job "digest", one run with delivery held, the hold row h1 of that run,
// and the original row o1. It returns the store and the run id.
func heldFixture(t *testing.T) (*Store, int64) {
	t.Helper()
	c := fixedNow(2026, 11, 20, 12, 0)
	s := store(t, c)
	id := mustCreate(t, s, Job{Name: "digest", Schedule: "30m", Prompt: "x", Enabled: true})
	addRun(t, s, id, "ok", DeliveryHeld, "out", c.t, nil, nil, false)
	runs, err := s.Runs(ctx, id, 1, 0)
	if err != nil || len(runs) != 1 {
		t.Fatal(runs, err)
	}
	h := heldRow("h1", id, c.t)
	h.RunID = runs[0].ID
	o := heldRow("o1", id, c.t)
	o.Kind, o.RunID, o.Text = "original", 0, "🛡️ the full note"
	hs := &HeldStore{DB: s.DB}
	for _, r := range []HeldSend{h, o} {
		if err := hs.Insert(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	return s, runs[0].ID
}

func runDelivery(t *testing.T, s *Store, runID int64) string {
	t.Helper()
	var d string
	if err := s.DB.QueryRowContext(ctx, `SELECT delivery FROM cron_runs WHERE id = ?`, runID).Scan(&d); err != nil {
		t.Fatal(err)
	}
	return d
}

func heldExists(t *testing.T, s *Store, id string) bool {
	t.Helper()
	_, ok, err := (&HeldStore{DB: s.DB}).Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestResolveHeldPost(t *testing.T) {
	s, run := heldFixture(t)
	var sent []string
	send := func(_ context.Context, h HeldSend) error { sent = append(sent, h.ID+"="+h.Text); return nil }
	var edits []string
	edit := func(h HeldSend, result string) { edits = append(edits, h.ID+"="+result) }
	if got, err := ResolveHeld(ctx, s, "h1", "post", send, edit); err != nil || got != "Sent." {
		t.Fatal(got, err)
	}
	if got, err := ResolveHeld(ctx, s, "o1", "post", send, edit); err != nil || got != "Posted." {
		t.Fatal(got, err)
	}
	if want := []string{"h1=the report", "o1=🛡️ the full note"}; len(sent) != 2 || sent[0] != want[0] || sent[1] != want[1] {
		t.Fatal(sent)
	}
	if len(edits) != 2 || edits[0] != "h1=Sent." || edits[1] != "o1=Posted." {
		t.Fatal(edits)
	}
	if heldExists(t, s, "h1") || heldExists(t, s, "o1") || runDelivery(t, s, run) != DeliverySent {
		t.Fatal("a post deletes the row and marks the run sent")
	}
	if _, err := ResolveHeld(ctx, s, "h1", "post", send, edit); !errors.Is(err, ErrHeldGone) {
		t.Fatalf("a second answer: %v", err)
	}
}

func TestResolveHeldDrop(t *testing.T) {
	s, run := heldFixture(t)
	send := func(context.Context, HeldSend) error { t.Error("a drop sends nothing"); return nil }
	if got, err := ResolveHeld(ctx, s, "h1", "drop", send, nil); err != nil || got != "Dropped." {
		t.Fatal(got, err)
	}
	if heldExists(t, s, "h1") || runDelivery(t, s, run) != DeliveryDropped {
		t.Fatal("a drop deletes the row and marks the run dropped")
	}
}

// A send error and a bad action put the row back and leave the run as it was.
func TestResolveHeldErrorsKeepRow(t *testing.T) {
	s, run := heldFixture(t)
	edited := false
	edit := func(HeldSend, string) { edited = true }
	fail := func(context.Context, HeldSend) error {
		return errors.New("telegram API error 403: Forbidden: bot was blocked by the user")
	}
	got, err := ResolveHeld(ctx, s, "h1", "post", fail, edit)
	if !errors.Is(err, ErrHeldNotSent) || got != "Not sent: telegram API error 403: Forbidden: bot was blocked by the user." {
		t.Fatal(got, err)
	}
	if got, err := ResolveHeld(ctx, s, "h1", "post", nil, edit); !errors.Is(err, ErrHeldNotSent) ||
		got != "Not sent: no send function is set." {
		t.Fatal(got, err)
	}
	if _, err := ResolveHeld(ctx, s, "h1", "send", fail, edit); err == nil ||
		err.Error() != `cron: held send action "send" is not valid. Use post or drop` {
		t.Fatal(err)
	}
	if edited || !heldExists(t, s, "h1") || runDelivery(t, s, run) != DeliveryHeld {
		t.Fatal("the row must come back, the run must not change, and the card must not change")
	}
	if _, err := ResolveHeld(ctx, s, "nope", "drop", nil, edit); !errors.Is(err, ErrHeldGone) {
		t.Fatal(err)
	}
}

// Two pages and a Telegram tap post at the same time: the text goes out once.
func TestResolveHeldOnce(t *testing.T) {
	s, _ := heldFixture(t)
	var sends, gone atomic.Int32
	send := func(context.Context, HeldSend) error { sends.Add(1); return nil }
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := ResolveHeld(ctx, s, "h1", "post", send, nil); errors.Is(err, ErrHeldGone) {
				gone.Add(1)
			}
		})
	}
	wg.Wait()
	if sends.Load() != 1 || gone.Load() != 3 {
		t.Fatalf("%d sends, %d gone", sends.Load(), gone.Load())
	}
}
