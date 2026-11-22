package gateway

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// queueCalls records the keys that the queue hook of g gets.
func queueCalls(g *Gateway) (calls func() []string) {
	var mu sync.Mutex
	var keys []string
	g.OnQueueChange(func(key string) {
		_ = g.Queue(key) // The hook runs outside Gateway.mu, so a read does not deadlock.
		mu.Lock()
		keys = append(keys, key)
		mu.Unlock()
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(keys)
	}
}

func TestQueueViewAndHook(t *testing.T) {
	ctx := context.Background()
	g, _ := gatewayWithSlowProvider(t, 500*time.Millisecond)
	calls := queueCalls(g)
	if q := g.Queue("k1"); q != nil {
		t.Fatalf("an unknown key has no queue: %+v", q)
	}
	f := &recFactory{}
	g.Submit(ctx, inb("k1", "hi"), f.new, nil)
	waitFor(t, func() bool { _, ok := g.Running("k1"); return ok })
	if q := g.Queue("k1"); len(q) != 0 {
		t.Fatalf("the running turn is not in the queue: %+v", q)
	}
	photo := inb("k1", "Look at this photo.\nIt is the new logo.")
	photo.IsText, photo.Images = false, []provider.Image{{Path: "/tmp/a.jpg", MIME: "image/jpeg"}}
	g.Submit(ctx, photo, f.new, nil)
	g.Submit(ctx, inb("k1", "/frobnicate "+strings.Repeat("x", 100)), f.new, nil)
	if err := g.Enqueue(ctx, "k1", "[batch b1 done]"); err != nil {
		t.Fatal(err)
	}
	want := []QueueItem{
		{Kind: "media", Preview: "Look at this photo."},
		{Kind: "skill", Preview: "/frobnicate " + strings.Repeat("x", 48) + "…"},
		{Kind: "system", Preview: "[batch b1 done]"},
	}
	if got := g.Queue("k1"); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	waitFor(t, func() bool { return len(f.finals()) == 4 })
	if q := g.Queue("k1"); len(q) != 0 {
		t.Fatalf("%+v", q)
	}
	// Three messages joined and three left.
	waitFor(t, func() bool { return len(calls()) >= 6 })
	for _, k := range calls() {
		if k != "k1" {
			t.Fatalf("the hook got the key %q", k)
		}
	}
}

func TestQueueHookOnStop(t *testing.T) {
	ctx := context.Background()
	g, _ := gatewayWithSlowProvider(t, 2*time.Second)
	calls := queueCalls(g)
	f := &recFactory{}
	g.Submit(ctx, inb("k1", "hi"), f.new, nil)
	waitFor(t, func() bool { _, ok := g.Running("k1"); return ok })
	photo := inb("k1", "")
	photo.IsText, photo.Files = false, []string{"/tmp/report.pdf"}
	g.Submit(ctx, photo, f.new, nil)
	if got := g.Queue("k1"); !reflect.DeepEqual(got, []QueueItem{{Kind: "media", Preview: ""}}) {
		t.Fatalf("%+v", got)
	}
	waitFor(t, func() bool { return len(calls()) >= 1 })
	n := len(calls())
	g.stopKey("k1")
	if q := g.Queue("k1"); len(q) != 0 {
		t.Fatalf("/stop drops the queue: %+v", q)
	}
	waitFor(t, func() bool { return len(calls()) > n })
}
