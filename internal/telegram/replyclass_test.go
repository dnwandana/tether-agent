package telegram

import (
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/gateway"
)

// replyGroup is the group chat of the class tests. A negative chat id is a group.
const replyGroup int64 = -100

// fillGroupWindow makes 18 counted calls in chat, 1 s apart, through the pace of c.
// Then the window of the chat is full. The slot is free again 1 s after the last call,
// and the oldest call is 60 s old 43 s after the last call.
func fillGroupWindow(t *testing.T, c *Client, clk *fakeClock, chat int64) {
	t.Helper()
	for i := range 18 {
		if i > 0 {
			clk.Advance(time.Second)
		}
		if w := c.Pace.TryTake(chat, paceWait); w != 0 {
			t.Fatalf("call %d: the pace holds for %s", i+1, w)
		}
	}
}

// fakeElapsed runs f and returns the time that the fake clock moved during f.
func fakeElapsed(clk *fakeClock, f func()) time.Duration {
	start := clk.Now()
	f()
	return clk.Now().Sub(start)
}

func TestRendererAcksIgnoreTheGroupWindow(t *testing.T) {
	api := newFakeBot(t)
	clk := newFakeClock()
	ch := &Channel{C: api.pacedClient(clk), Cfg: config.Default().Telegram}
	fillGroupWindow(t, ch.C, clk, replyGroup)
	r := ch.newRenderer(ctx, replyGroup, "", false)
	r.userID = "7"
	acks := []func(){
		func() { r.Ack("⏳ Busy. Your message waits in the queue.") },
		func() { r.AckReply(gateway.Reply{Text: "Model set."}) },
		func() { _ = r.AckReplyHandle(gateway.Reply{Text: "⏳ Queued.", Buttons: gateway.StopButtons()}) },
	}
	for i, ack := range acks {
		if got := fakeElapsed(clk, ack); got != time.Second {
			t.Fatalf("ack %d waited %s: an ack waits only for the 1 s slot", i+1, got)
		}
	}
	if api.count("sendMessage") != 3 {
		t.Fatal(api.methods())
	}
}

func TestCommandReplyIgnoresTheGroupWindow(t *testing.T) {
	api := newFakeBot(t)
	clk := newFakeClock()
	ch := &Channel{C: api.pacedClient(clk), Cfg: config.Default().Telegram}
	fillGroupWindow(t, ch.C, clk, replyGroup)
	m := Message{MessageID: 1, Chat: Chat{ID: replyGroup}}
	n := Normalized{ChatID: "-100", UserID: "7"}
	ok := false
	got := fakeElapsed(clk, func() { ok = ch.localCommand(ctx, m, n, "/start") })
	if !ok || got != time.Second || api.count("sendMessage") != 1 {
		t.Fatalf("handled %v, waited %s, calls %v: a command reply waits only for the 1 s slot",
			ok, got, api.methods())
	}
}

func TestPromptSendIgnoresTheGroupWindow(t *testing.T) {
	api := newFakeBot(t)
	clk := newFakeClock()
	it := &Interactions{C: api.pacedClient(clk)}
	fillGroupWindow(t, it.C, clk, replyGroup)
	p := it.add(approvalPrefix, replyGroup, "")
	var err error
	got := fakeElapsed(clk, func() { err = it.send(ctx, p, "Run `make test`?", nil) })
	if err != nil || got != time.Second || api.count("sendMessage") != 1 {
		t.Fatalf("err %v, waited %s: a prompt waits only for the 1 s slot", err, got)
	}
}

func TestCronSendWaitsForTheGroupWindow(t *testing.T) {
	api := newFakeBot(t)
	clk := newFakeClock()
	ch := &Channel{C: api.pacedClient(clk)}
	fillGroupWindow(t, ch.C, clk, replyGroup)
	var err error
	got := fakeElapsed(clk, func() {
		err = ch.Send(ctx, gateway.Target{Source: "telegram", ChatID: "-100"}, gateway.Outbound{Text: "Daily report"})
	})
	if err != nil || got != 43*time.Second || api.count("sendMessage") != 1 {
		t.Fatalf("err %v, waited %s: a cron send waits until the oldest call is 60 s old", err, got)
	}
}
