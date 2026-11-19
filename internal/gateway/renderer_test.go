package gateway

import (
	"context"
	"testing"
	"time"
)

// sessRenderer is a recRenderer that keeps the session id of its turn.
type sessRenderer struct {
	recRenderer
	got chan string
}

func (r *sessRenderer) SetSession(id string) { r.got <- id }

func TestRunTurnSetsSession(t *testing.T) {
	g, _ := gatewayWithSlowProvider(t, 10*time.Millisecond)
	r := &sessRenderer{got: make(chan string, 1)}
	if err := g.Handle(context.Background(), inb("k", "first"), func() Renderer { return r }); err != nil {
		t.Fatal(err)
	}
	g.Wait(5 * time.Second)
	sess, err := g.Deps.Store.Active(context.Background(), "k")
	if err != nil || sess == nil {
		t.Fatal(sess, err)
	}
	select {
	case id := <-r.got:
		if id != sess.ID {
			t.Fatalf("got %q, want %q", id, sess.ID)
		}
	default:
		t.Fatal("the renderer got no session id")
	}
}

func TestLazyRendererForwardsSetSession(t *testing.T) {
	inner := &sessRenderer{got: make(chan string, 1)}
	l := &lazyRenderer{new: func() Renderer { return inner }}
	var sr SessionRenderer = l
	sr.SetSession("s9")
	if id := <-inner.got; id != "s9" {
		t.Fatal(id)
	}
}
