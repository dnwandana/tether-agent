package agent

import (
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/pricing"
	"github.com/dnwandana/tether-agent/internal/provider"
)

func TestProviderForUsesTheSessionPin(t *testing.T) {
	fp := scripted()
	a, sess := newAgent(t, fp)
	if a.ProviderFor(sess) != provider.Provider(fp) {
		t.Fatal("no pin gives the default provider")
	}
	cheap := &fakeProv{name: "cheap", model: "cheap-default"}
	a.Providers["cheap"] = cheap
	sess.ModelOverride = "cheap"
	if a.ProviderFor(sess) != provider.Provider(cheap) {
		t.Fatal("a pin gives the pinned provider")
	}
	if a.ProviderFor(nil) != provider.Provider(fp) {
		t.Fatal("a nil session gives the default provider")
	}
}

func TestMetaStampsTheCost(t *testing.T) {
	fp := scripted()
	a, _ := newAgent(t, fp)
	a.Cfg.Pricing.Overrides = map[string]config.PriceOverride{"test-model": {Input: 2, Output: 10}}
	a.Prices = pricing.NewService(a.Cfg, t.TempDir(), nil, nil, nil)
	m := a.Meta(fp, provider.Result{FinishReason: "stop", Usage: provider.Usage{InputTokens: 1000, OutputTokens: 100}})
	if m.Provider != "primary" || m.Model != "test-model" || m.FinishReason != "stop" || m.Usage.InputTokens != 1000 {
		t.Fatalf("%+v", m)
	}
	if want := 1000*2e-6 + 100*10e-6; m.CostUSD < want-1e-12 || m.CostUSD > want+1e-12 {
		t.Fatal(m.CostUSD, want)
	}
	stamped := a.Meta(fp, provider.Result{Provider: "fallback", Model: "other-model"})
	if stamped.Provider != "fallback" || stamped.Model != "other-model" {
		t.Fatalf("the stamp of the stream names the provider that served it: %+v", stamped)
	}
}

func TestBatchesRunningCountsByKey(t *testing.T) {
	var none *Batches
	if none.Running("telegram:dm:1") != 0 {
		t.Fatal("a nil Batches has no running batch")
	}
	q := &enqueued{}
	b := &Batches{DB: openTestDB(t), Enqueue: q.fn}
	for range 2 {
		if _, err := b.Start(ctx, "telegram:dm:1", "s1", specs(1), slowRun(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Start(ctx, "telegram:dm:2", "s2", specs(1), slowRun(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if b.Running("telegram:dm:1") != 2 || b.Running("telegram:dm:2") != 1 || b.Running("cli:local") != 0 {
		t.Fatal(b.Running("telegram:dm:1"), b.Running("telegram:dm:2"), b.Running("cli:local"))
	}
	b.CancelKey("telegram:dm:1")
	b.CancelKey("telegram:dm:2")
	waitUntil(t, func() bool { return len(q.list()) == 3 })
	if b.Running("telegram:dm:1") != 0 || b.Running("telegram:dm:2") != 0 {
		t.Fatal("an ended batch is not running")
	}
}
