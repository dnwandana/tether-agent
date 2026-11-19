package cron

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

func heldStore(t *testing.T) *HeldStore {
	t.Helper()
	return &HeldStore{DB: store(t, fixedNow(2026, 11, 18, 12, 0)).DB}
}

func heldRow(id, job string, at time.Time) HeldSend {
	return HeldSend{ID: id, Kind: "hold", JobID: job, RunID: 41, Target: "telegram:-100:7",
		TargetTitle: "Pricing team", Text: "the report", Reason: "the run read pastebin.com",
		Sources: []string{"terminal:curl", "web_fetch"}, CreatedAt: at}
}

func TestHeldStoreRoundTrip(t *testing.T) {
	hs := heldStore(t)
	at := time.UnixMilli(time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC).UnixMilli())
	h := heldRow("a1", "job1", at)
	if err := hs.Insert(ctx, h); err != nil {
		t.Fatal(err)
	}
	if err := hs.SetCard(ctx, "a1", "5", 77); err != nil {
		t.Fatal(err)
	}
	got, ok, err := hs.Claim(ctx, "a1")
	h.CardChat, h.CardMsgID = "5", 77
	if err != nil || !ok || !got.CreatedAt.Equal(at) {
		t.Fatal(got, ok, err)
	}
	got.CreatedAt = h.CreatedAt
	if !reflect.DeepEqual(got, h) {
		t.Fatalf("got %+v, want %+v", got, h)
	}
	if _, ok, err := hs.Claim(ctx, "a1"); ok || err != nil {
		t.Fatal("a claimed row is gone", ok, err)
	}
	if err := hs.Restore(ctx, got); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := hs.Claim(ctx, "a1"); !ok {
		t.Fatal("a restored row can be claimed again")
	}
	// RunID 0 is NULL and comes back as 0.
	h0 := heldRow("a2", "job1", at)
	h0.RunID, h0.Kind = 0, "original"
	if err := hs.Insert(ctx, h0); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := hs.Claim(ctx, "a2"); !ok || got.RunID != 0 || got.Kind != "original" {
		t.Fatal(got, ok)
	}
}

func TestHeldStoreClaimOnce(t *testing.T) {
	hs := heldStore(t)
	if err := hs.Insert(ctx, heldRow("b1", "job1", time.Now())); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for range 8 {
		wg.Go(func() {
			if _, ok, err := hs.Claim(ctx, "b1"); ok && err == nil {
				mu.Lock()
				won++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d callers got the row", won)
	}
}

func TestHeldStoreExpiredAndJob(t *testing.T) {
	hs := heldStore(t)
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for _, h := range []HeldSend{heldRow("old", "job1", now.Add(-25*time.Hour)), heldRow("new", "job1", now.Add(-time.Hour)),
		heldRow("other", "job2", now.Add(-time.Hour))} {
		if err := hs.Insert(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	got, err := hs.ClaimExpired(ctx, now.Add(-24*time.Hour))
	if err != nil || len(got) != 1 || got[0].ID != "old" {
		t.Fatal(got, err)
	}
	if err := hs.DeleteJob(ctx, "job1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := hs.Claim(ctx, "new"); ok {
		t.Fatal("DeleteJob must remove the rows of the job")
	}
	if _, ok, _ := hs.Claim(ctx, "other"); !ok {
		t.Fatal("DeleteJob must keep the rows of other jobs")
	}
}

func TestNewHeldID(t *testing.T) {
	id, err := newHeldID()
	if err != nil || len(id) != 16 {
		t.Fatal(id, err)
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			t.Fatalf("%q", id)
		}
	}
}
