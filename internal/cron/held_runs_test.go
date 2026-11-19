package cron

import (
	"testing"
	"time"
)

func TestHeldRowGetsRunID(t *testing.T) {
	c := fixedNow(2026, 11, 18, 12, 0)
	s := histStore(t, c, 5)
	hs := &HeldStore{DB: s.DB}
	id := mustCreate(t, s, Job{Name: "p", Schedule: "30m", Prompt: "x", Enabled: true})
	if err := hs.Insert(ctx, HeldSend{ID: "h1", Kind: "hold", JobID: id, Target: "telegram:-100", CreatedAt: c.t}); err != nil {
		t.Fatal(err)
	}
	if err := hs.Insert(ctx, HeldSend{ID: "o1", Kind: "original", JobID: id, Target: "telegram:-100", CreatedAt: c.t}); err != nil {
		t.Fatal(err)
	}
	rec := runRec("ok", "report", c.t)
	rec.Delivery = DeliveryHeld
	if err := s.MarkRun(ctx, id, rec); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Runs(ctx, id, 1, 0)
	if err != nil || len(rs) != 1 || rs[0].Delivery != DeliveryHeld {
		t.Fatal(rs, err)
	}
	h, ok, _ := hs.Claim(ctx, "h1")
	if !ok || h.RunID != rs[0].ID {
		t.Fatalf("the hold row gets the run id: %+v", h)
	}
	if o, _, _ := hs.Claim(ctx, "o1"); o.RunID != 0 {
		t.Fatalf("an original row keeps no run id: %+v", o)
	}
	if err := s.UpdateRunDelivery(ctx, rs[0].ID, DeliveryDropped); err != nil {
		t.Fatal(err)
	}
	if rs, _ := s.Runs(ctx, id, 1, 0); rs[0].Delivery != DeliveryDropped {
		t.Fatal(rs[0].Delivery)
	}
	if err := s.UpdateRunDelivery(ctx, 99999, DeliverySent); err != nil {
		t.Fatal("a missing run row is no error:", err)
	}
}

func TestRemoveDeletesHeldRows(t *testing.T) {
	s := store(t, fixedNow(2026, 11, 18, 12, 0))
	hs := &HeldStore{DB: s.DB}
	id := mustCreate(t, s, Job{Name: "p", Schedule: "30m", Prompt: "x", Enabled: true})
	if err := hs.Insert(ctx, HeldSend{ID: "h1", Kind: "hold", JobID: id, Target: "telegram:-100", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := hs.Claim(ctx, "h1"); ok {
		t.Fatal("Remove must delete the held rows of the job")
	}
}

// TestHeldRunWithClaimedRow covers a tap that claims the hold row before MarkRun writes the
// run row. No row can get the run id, so the run must not stay held.
func TestHeldRunWithClaimedRow(t *testing.T) {
	c := fixedNow(2026, 11, 18, 12, 0)
	s := histStore(t, c, 5)
	hs := &HeldStore{DB: s.DB}
	id := mustCreate(t, s, Job{Name: "p", Schedule: "30m", Prompt: "x", Enabled: true})
	if err := hs.Insert(ctx, HeldSend{ID: "h1", Kind: "hold", JobID: id, Target: "telegram:-100", CreatedAt: c.t}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := hs.Claim(ctx, "h1"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	rec := runRec("ok", "report", c.t)
	rec.Delivery = DeliveryHeld
	if err := s.MarkRun(ctx, id, rec); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Runs(ctx, id, 1, 0)
	if err != nil || len(rs) != 1 || rs[0].Delivery != DeliveryNone {
		t.Fatalf("the run of a claimed hold row: %+v %v", rs, err)
	}
}

// TestRestoreRelinksRun covers the order: insert, claim, record, restore. The run row gets
// DeliveryNone in record. Restore must link the row and set the run to held again.
func TestRestoreRelinksRun(t *testing.T) {
	c := fixedNow(2026, 11, 18, 12, 0)
	s := histStore(t, c, 5)
	hs := &HeldStore{DB: s.DB}
	id := mustCreate(t, s, Job{Name: "p", Schedule: "30m", Prompt: "x", Enabled: true})
	if err := hs.Insert(ctx, HeldSend{ID: "h1", Kind: "hold", JobID: id, Target: "telegram:-100", CreatedAt: c.t}); err != nil {
		t.Fatal(err)
	}
	h, ok, err := hs.Claim(ctx, "h1")
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	rec := runRec("ok", "report", c.t)
	rec.Delivery = DeliveryHeld
	if err := s.MarkRun(ctx, id, rec); err != nil {
		t.Fatal(err)
	}
	if err := hs.Restore(ctx, h); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Runs(ctx, id, 1, 0)
	if err != nil || len(rs) != 1 || rs[0].Delivery != DeliveryHeld {
		t.Fatalf("%+v %v", rs, err)
	}
	got, ok, _ := hs.Claim(ctx, "h1")
	if !ok || got.RunID != rs[0].ID {
		t.Fatalf("the restored row gets the run id: %+v", got)
	}
}
