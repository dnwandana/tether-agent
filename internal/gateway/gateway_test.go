package gateway

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// recRenderer records the output of one turn.
type recRenderer struct {
	mu    sync.Mutex
	text  strings.Builder
	final string
	err   error
	done  bool
	acks  []string
	tools []string
}

func (r *recRenderer) Delta(s string) { r.mu.Lock(); r.text.WriteString(s); r.mu.Unlock() }
func (r *recRenderer) ToolStart(name, _ string) {
	r.mu.Lock()
	r.tools = append(r.tools, name)
	r.mu.Unlock()
}
func (r *recRenderer) ToolEnd(string, bool) {}
func (r *recRenderer) Boundary()            {}
func (r *recRenderer) Notice(string)        {}
func (r *recRenderer) Ack(s string)         { r.mu.Lock(); r.acks = append(r.acks, s); r.mu.Unlock() }
func (r *recRenderer) Done(final string, err error) {
	r.mu.Lock()
	r.final, r.err, r.done = final, err, true
	r.mu.Unlock()
}

// recFactory hands out recRenderers and keeps them in order.
type recFactory struct {
	mu   sync.Mutex
	list []*recRenderer
}

func (f *recFactory) new() Renderer {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := &recRenderer{}
	f.list = append(f.list, r)
	return r
}

func (f *recFactory) finals() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.list {
		r.mu.Lock()
		if r.done {
			out = append(out, r.final)
		}
		r.mu.Unlock()
	}
	return out
}

func inb(key, text string) Inbound {
	return Inbound{Key: key, Source: "telegram", ChatID: "1", Text: text, IsText: true}
}

// gatewayWithSlowProvider returns a gateway whose provider waits d before each answer.
func gatewayWithSlowProvider(t *testing.T, d time.Duration) (*Gateway, *fakeProv) {
	t.Helper()
	fp := &fakeProv{delay: d}
	return &Gateway{Deps: testDeps(t, fp)}, fp
}

// waitFor waits until cond is true, or fails the test after 5 s.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(end) {
			t.Fatal("timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestOneTurnPerKeyAtATime(t *testing.T) {
	ctx := context.Background()
	g, fp := gatewayWithSlowProvider(t, 100*time.Millisecond)
	f := &recFactory{}
	var wg sync.WaitGroup
	// A message that is not plain text waits in the queue. A plain text goes into the run as a steer.
	in := inb("k1", "hi")
	in.IsText = false
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = g.Handle(ctx, in, f.new) }()
	}
	wg.Wait()
	if fp.max() != 1 {
		t.Fatal("the turns of one key must not overlap:", fp.max())
	}
	if got := f.finals(); len(got) != 3 || got[0] != "ok" {
		t.Fatal(got)
	}
	sess, _ := g.Deps.Store.Active(ctx, "k1")
	if msgs, _ := g.Deps.Store.Messages(ctx, sess.ID); len(msgs) != 6 {
		t.Fatal(len(msgs))
	}
	if sess.Source != "telegram" || sess.ChatID != "1" {
		t.Fatalf("%+v", sess)
	}
}

func TestDifferentKeysRunInParallel(t *testing.T) {
	ctx := context.Background()
	g, fp := gatewayWithSlowProvider(t, 200*time.Millisecond)
	f := &recFactory{}
	var wg sync.WaitGroup
	for _, k := range []string{"k1", "k2"} {
		wg.Add(1)
		go func() { defer wg.Done(); _ = g.Handle(ctx, inb(k, "hi"), f.new) }()
	}
	wg.Wait()
	if fp.max() != 2 {
		t.Fatal("two keys must run at the same time:", fp.max())
	}
}

func TestCommandsBypassAndRouteToCommandTable(t *testing.T) {
	ctx := context.Background()
	g, _ := gatewayWithSlowProvider(t, 300*time.Millisecond)
	f := &recFactory{}
	go func() { _ = g.Handle(ctx, inb("k1", "hi"), f.new) }()
	waitFor(t, func() bool { _, ok := g.Running("k1"); return ok })
	r := &recRenderer{}
	start := time.Now()
	if err := g.Handle(ctx, inb("k1", "/sessions@tether_bot"), func() Renderer { return r }); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 200*time.Millisecond || len(r.acks) != 1 || !strings.Contains(r.acks[0], "Sessions") {
		t.Fatal(time.Since(start), r.acks)
	}
	if r.done {
		t.Fatal("a command runs no turn")
	}
	if _, ok := g.Running("k1"); !ok {
		t.Fatal("the turn must go on")
	}
	// An unknown command is text for the agent.
	r2 := &recRenderer{}
	_ = g.Handle(ctx, inb("k2", "/frobnicate now"), func() Renderer { return r2 })
	if !r2.done || r2.final != "ok" {
		t.Fatalf("%+v", r2)
	}
}

func TestStopHookCancelsTheRun(t *testing.T) {
	ctx := context.Background()
	g, _ := gatewayWithSlowProvider(t, 2*time.Second)
	Register(Command{Name: "teststop", WhileBusy: true, Run: func(_ context.Context, c *CommandCtx, _ string) error { c.Hooks.Stop(); return nil }})
	r := &recRenderer{}
	done := make(chan struct{})
	go func() { _ = g.Handle(ctx, inb("k1", "hi"), func() Renderer { return r }); close(done) }()
	waitFor(t, func() bool { _, ok := g.Running("k1"); return ok })
	_ = g.Handle(ctx, inb("k1", "/teststop"), func() Renderer { return &recRenderer{} })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the stop hook must cancel the turn")
	}
	if !errors.Is(r.err, context.Canceled) {
		t.Fatal(r.err)
	}
}

func TestEnqueueRunsAfterCurrentTurn(t *testing.T) {
	ctx := context.Background()
	g, fp := gatewayWithSlowProvider(t, 100*time.Millisecond)
	f := &recFactory{}
	done := make(chan struct{})
	go func() { _ = g.Handle(ctx, inb("k1", "first"), f.new); close(done) }()
	waitFor(t, func() bool { _, ok := g.Running("k1"); return ok })
	if err := g.Enqueue(ctx, "k1", "[background task done]"); err != nil {
		t.Fatal(err)
	}
	<-done
	waitFor(t, func() bool { return len(f.finals()) == 2 })
	if got := fp.lastUserTexts(); len(got) != 2 || got[0] != "first" || got[1] != "[background task done]" {
		t.Fatal(got)
	}
	if fp.max() != 1 {
		t.Fatal("overlap")
	}
	if !g.Wait(time.Second) {
		t.Fatal("wait")
	}
}

func TestEnqueueUnknownKeyUsesSessionChannel(t *testing.T) {
	ctx := context.Background()
	g, _ := gatewayWithSlowProvider(t, 0)
	if err := g.Enqueue(ctx, "nokey", "x"); err == nil {
		t.Fatal("a key with no session must fail")
	}
	sess, _ := g.Deps.Store.ActiveOrCreate(ctx, "telegram:dm:5", sessions.Session{Source: "telegram", ChatID: "5"})
	ch := &recChannel{sent: make(chan Outbound, 1)}
	g.Channels = map[string]Channel{"telegram": ch}
	if err := g.Enqueue(ctx, sess.Key, "hello"); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-ch.sent:
		if m.Text != "ok" || ch.to.ChatID != "5" {
			t.Fatal(m, ch.to)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no answer sent")
	}
}

func TestPendingSteerBecomesNextTurn(t *testing.T) {
	ctx := context.Background()
	g, fp := gatewayWithSlowProvider(t, 150*time.Millisecond)
	f := &recFactory{}
	done := make(chan struct{})
	go func() { _ = g.Handle(ctx, inb("k1", "first"), f.new); close(done) }()
	waitFor(t, func() bool { _, ok := g.Running("k1"); return ok })
	rs, _ := g.Running("k1")
	if !rs.Steer.Push("x") {
		t.Fatal("push")
	}
	<-done
	waitFor(t, func() bool { return len(f.finals()) == 2 })
	if got := fp.lastUserTexts(); len(got) != 2 || got[1] != "x" {
		t.Fatal(got)
	}
}

func TestPanicInTurnKeepsQueue(t *testing.T) {
	ctx := context.Background()
	g, _ := gatewayWithSlowProvider(t, 50*time.Millisecond)
	var once sync.Once
	g.Deps.NewEnv = func(*sessions.Session, string) *tools.Env {
		once.Do(func() { panic("boom") })
		return nil
	}
	r1, r2 := &recRenderer{}, &recRenderer{}
	_ = g.Handle(ctx, inb("k1", "a"), func() Renderer { return r1 })
	_ = g.Handle(ctx, inb("k1", "b"), func() Renderer { return r2 })
	if r1.err == nil || !r2.done || r2.final != "ok" {
		t.Fatalf("%+v %+v", r1, r2)
	}
}

func TestRunStateRoundAndTool(t *testing.T) {
	tr := &trackSink{Sink: &recRenderer{}}
	if tr.round() != 1 || tr.tool() != "" {
		t.Fatal()
	}
	tr.Boundary()
	tr.ToolStart("read_file", "a")
	if tr.round() != 2 || tr.tool() != "read_file" {
		t.Fatal(tr.round(), tr.tool())
	}
	tr.ToolEnd("read_file", false)
	if tr.tool() != "" {
		t.Fatal(tr.tool())
	}
	var _ agent.Sink = tr
}

// recChannel records the messages that the gateway sends.
type recChannel struct {
	to   Target
	sent chan Outbound
}

func (c *recChannel) Name() string                        { return "telegram" }
func (c *recChannel) Run(context.Context, *Gateway) error { return nil }
func (c *recChannel) Send(_ context.Context, to Target, m Outbound) error {
	c.to = to
	c.sent <- m
	return nil
}

func TestSubmitReturnsBeforeTheTurnEnds(t *testing.T) {
	ctx := context.Background()
	g, _ := gatewayWithSlowProvider(t, 300*time.Millisecond)
	f := &recFactory{}
	res := make(chan error, 1)
	start := time.Now()
	g.Submit(ctx, inb("k1", "hi"), f.new, func(err error) { res <- err })
	if time.Since(start) > 150*time.Millisecond {
		t.Fatal("Submit must not wait for the turn:", time.Since(start))
	}
	// The key belongs to the message when Submit returns.
	g.mu.Lock()
	busy := g.key("k1").busy
	g.mu.Unlock()
	if !busy {
		t.Fatal("the key must be busy when Submit returns")
	}
	select {
	case err := <-res:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("done was not called")
	}
	if got := f.finals(); len(got) != 1 || got[0] != "ok" {
		t.Fatal(got)
	}
}

func TestSubmitKeepsTheOrderOfOneKey(t *testing.T) {
	ctx := context.Background()
	g, fp := gatewayWithSlowProvider(t, 30*time.Millisecond)
	f := &recFactory{}
	var wg sync.WaitGroup
	for _, text := range []string{"one", "two", "three", "four"} {
		in := inb("k1", text)
		in.IsText = false // not a steer: each message is a turn
		wg.Add(1)
		g.Submit(ctx, in, f.new, func(error) { wg.Done() })
	}
	wg.Wait()
	if got := strings.Join(fp.lastUserTexts(), " "); got != "one two three four" {
		t.Fatal(got)
	}
	if !g.Wait(time.Second) {
		t.Fatal("the turns did not end")
	}
}

func TestSubmitSteerCommandOnIdleKeyDoesNotWait(t *testing.T) {
	ctx := context.Background()
	g, fp := gatewayWithSlowProvider(t, 300*time.Millisecond)
	res := make(chan error, 1)
	start := time.Now()
	g.Submit(ctx, inb("k1", "/steer x"), func() Renderer { return &recRenderer{} }, func(err error) { res <- err })
	if time.Since(start) > 150*time.Millisecond {
		t.Fatal("Submit must not wait for the turn of /steer:", time.Since(start))
	}
	<-res
	if got := fp.lastUserTexts(); len(got) != 1 || got[0] != "x" {
		t.Fatal(got)
	}
}

func TestStateCommandsRefusedWhileBusy(t *testing.T) {
	// Hermes gateway/run.py: a command that is not safe mid-run gets a busy reply.
	g, f, _, _ := busyGateway(t)
	sess := g.mustRun(t, "k").SessionID
	for _, cmd := range []string{"/delete " + sess, "/compact", "/model primary", "/prompt set x",
		"/fork", "/resume " + sess, "/rename x", "/archive", "/reload-mcp"} {
		r := &recRenderer{}
		if err := g.Handle(context.Background(), inb("k", cmd), func() Renderer { return r }); err != nil {
			t.Fatal(cmd, err)
		}
		name, _, _ := IsCommand(cmd)
		want := "⏳ Agent is running — `/" + name + "` can't run mid-turn. Wait for the current response or `/stop` first."
		if len(r.acks) != 1 || r.acks[0] != want {
			t.Fatalf("%s: %q", cmd, r.acks)
		}
	}
	if _, ok := g.Running("k"); !ok {
		t.Fatal("the run must go on")
	}
	s, err := g.Deps.Store.Active(context.Background(), "k")
	if err != nil || s == nil || s.ID != sess || s.Title != "" || s.Archived || s.ModelOverride != "" || s.PromptOverride != "" {
		t.Fatalf("the session changed: %+v %v", s, err)
	}
	// The read-only commands and the run controls still work.
	r := &recRenderer{}
	_ = g.Handle(context.Background(), inb("k", "/sessions"), func() Renderer { return r })
	if len(r.acks) != 1 || !strings.Contains(r.acks[0], "Sessions") {
		t.Fatal(r.acks)
	}
	_ = f
}

func TestQueuedTurnCountsAsActiveFromItsHandoff(t *testing.T) {
	// Wait must not see zero turns between the end of a turn and the start of the next one.
	ctx := context.Background()
	g := &Gateway{Deps: testDeps(t, &fakeProv{})}
	f := &recFactory{}
	in := inb("k", "x")
	in.IsText = false
	g.mu.Lock()
	ks := g.key("k")
	ks.busy, ks.current = true, &pendingTurn{in: inb("k", "first")}
	g.mu.Unlock()
	rest := g.acceptMessage(ctx, in, f.new)
	g.mu.Lock()
	p := ks.queue[0]
	g.next(ctx, ks) // the end of the first turn: next unlocks g.mu
	<-p.ready
	if n := g.active.Load(); n != 1 {
		t.Fatal("active after the handoff:", n)
	}
	if err := rest(); err != nil {
		t.Fatal(err)
	}
	if n := g.active.Load(); n != 0 || len(f.finals()) != 1 {
		t.Fatal(n, f.finals())
	}
}

func TestStopAfterHandoffFreesTheKey(t *testing.T) {
	// /stop between the handoff and the start of a queued turn must not leave the key busy.
	ctx := context.Background()
	g := &Gateway{Deps: testDeps(t, &fakeProv{})}
	f := &recFactory{}
	in := inb("k", "x")
	in.IsText = false
	g.mu.Lock()
	ks := g.key("k")
	ks.busy, ks.current = true, &pendingTurn{in: inb("k", "first")}
	g.mu.Unlock()
	rest := g.acceptMessage(ctx, in, f.new)
	g.mu.Lock()
	g.next(ctx, ks)
	g.stopKey("k")
	_ = rest()
	g.mu.Lock()
	busy := ks.busy
	g.mu.Unlock()
	if busy {
		t.Fatal("the key is still busy")
	}
}
