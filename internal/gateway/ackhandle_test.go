package gateway

import (
	"reflect"
	"sync"
	"testing"
)

var _ HandleRenderer = (*lazyRenderer)(nil)

// stubHandle is an AckHandle that records its Close calls.
type stubHandle struct {
	id     int
	mu     sync.Mutex
	closes []string
}

func (h *stubHandle) MsgID() int { return h.id }

func (h *stubHandle) Close(text string) <-chan struct{} {
	h.mu.Lock()
	h.closes = append(h.closes, text)
	h.mu.Unlock()
	done := make(chan struct{})
	close(done)
	return done
}

// stubHandleRenderer is a replyRenderer that returns a stubHandle for each sent ack.
type stubHandleRenderer struct {
	replyRenderer
	handles []*stubHandle
}

func (r *stubHandleRenderer) AckReplyHandle(rep Reply) AckHandle {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replies = append(r.replies, rep)
	h := &stubHandle{id: 100 + len(r.handles)}
	r.handles = append(r.handles, h)
	return h
}

func TestAckReplyReturnsHandle(t *testing.T) {
	rep := Reply{Text: SteerAck, Buttons: [][]Button{nowButton(3)}}
	hr := &stubHandleRenderer{}
	h := ackReply(hr, rep)
	if h == nil || h.MsgID() != 100 || len(hr.replies) != 1 || !reflect.DeepEqual(hr.replies[0], rep) {
		t.Fatalf("%v %+v", h, hr.replies)
	}
	// A ReplyRenderer gets the reply with its buttons, and no handle comes back.
	rr := &replyRenderer{}
	if h := ackReply(rr, rep); h != nil || len(rr.replies) != 1 || len(rr.acks) != 0 {
		t.Fatalf("%v %+v %q", h, rr.replies, rr.acks)
	}
	// A plain Renderer gets the text only.
	pr := &recRenderer{}
	if h := ackReply(pr, rep); h != nil || !reflect.DeepEqual(pr.acks, []string{SteerAck}) {
		t.Fatalf("%v %q", h, pr.acks)
	}
}

func TestLazyRendererForwardsHandle(t *testing.T) {
	rep := Reply{Text: QueueAck, Buttons: [][]Button{nowButton(1)}}
	hr := &stubHandleRenderer{}
	if h := ackReply(&lazyRenderer{new: func() Renderer { return hr }}, rep); h == nil || h.MsgID() != 100 || len(hr.replies) != 1 {
		t.Fatalf("%v %+v", h, hr.replies)
	}
	rr := &replyRenderer{}
	if h := ackReply(&lazyRenderer{new: func() Renderer { return rr }}, rep); h != nil || len(rr.replies) != 1 {
		t.Fatalf("%v %+v", h, rr.replies)
	}
	pr := &recRenderer{}
	if h := ackReply(&lazyRenderer{new: func() Renderer { return pr }}, rep); h != nil || !reflect.DeepEqual(pr.acks, []string{QueueAck}) {
		t.Fatalf("%v %q", h, pr.acks)
	}
}
