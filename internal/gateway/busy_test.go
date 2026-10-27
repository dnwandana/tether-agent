package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/provider"
)

// fakeClock is a settable clock for the ack limiter.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// acks returns the acks of all renderers of f, in order.
func (f *recFactory) acks() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.list {
		r.mu.Lock()
		out = append(out, r.acks...)
		r.mu.Unlock()
	}
	return out
}

// busyGateway starts a slow turn on key "k" and returns when it runs. The debounce window
// is short, and the clock is fake. done is closed when the first turn ends.
func busyGateway(t *testing.T) (g *Gateway, f *recFactory, clk *fakeClock, done chan struct{}) {
	t.Helper()
	g, _ = gatewayWithSlowProvider(t, 2*time.Second)
	clk = &fakeClock{t: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
	g.now = clk.now
	g.debounce = NewDebouncer(time.Millisecond, 10*time.Millisecond)
	f = &recFactory{}
	done = make(chan struct{})
	go func() { _ = g.Handle(context.Background(), inb("k", "first"), f.new); close(done) }()
	waitFor(t, func() bool { _, ok := g.Running("k"); return ok })
	t.Cleanup(func() {
		if rs, ok := g.Running("k"); ok {
			rs.Cancel()
		}
		<-done
		g.Wait(5 * time.Second)
	})
	return g, f, clk, done
}

// mustRun returns the running state of key or fails the test.
func (g *Gateway) mustRun(t *testing.T, key string) *RunState {
	t.Helper()
	rs, ok := g.Running(key)
	if !ok {
		t.Fatal("no run for", key)
	}
	return rs
}

// queueLen returns the number of queued turns of key.
func (g *Gateway) queueLen(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.key(key).queue)
}

func media(key string) Inbound {
	in := inb(key, "")
	in.IsText = false
	in.Images = []provider.Image{{MIME: "image/png", Data: []byte("x")}}
	return in
}

func TestTextWhileBusySteersAndAcks(t *testing.T) {
	g, f, _, _ := busyGateway(t)
	if err := g.Handle(context.Background(), inb("k", "also grab prices"), f.new); err != nil {
		t.Fatal(err)
	}
	rs := g.mustRun(t, "k")
	waitFor(t, func() bool { return rs.Steer.Pending() != "" })
	if rs.Steer.Pending() != "also grab prices" {
		t.Fatal(rs.Steer.Pending())
	}
	acks := f.acks()
	if len(acks) != 1 || !strings.HasPrefix(acks[0], "⏩ Steered into current run (") ||
		!strings.Contains(acks[0], "Your message arrives after the next tool call.") {
		t.Fatal(acks)
	}
	// A steer is not also queued, and it does not stop the run.
	if g.queueLen("k") != 0 {
		t.Fatal("a steered message must not be queued")
	}
	if _, ok := g.Running("k"); !ok {
		t.Fatal("steer must not stop the run")
	}
}

func TestAckRateLimit30s(t *testing.T) {
	g, f, clk, _ := busyGateway(t)
	rs := g.mustRun(t, "k")
	_ = g.Handle(context.Background(), inb("k", "one"), f.new)
	waitFor(t, func() bool { return rs.Steer.Pending() == "one" })
	clk.add(5 * time.Second)
	_ = g.Handle(context.Background(), inb("k", "two"), f.new)
	waitFor(t, func() bool { return rs.Steer.Pending() == "one\ntwo" })
	if n := len(f.acks()); n != 1 {
		t.Fatal("one ack in 30 s:", f.acks())
	}
	clk.add(26 * time.Second)
	_ = g.Handle(context.Background(), inb("k", "three"), f.new)
	waitFor(t, func() bool { return len(f.acks()) == 2 })
	if !strings.Contains(rs.Steer.Pending(), "three") {
		t.Fatal(rs.Steer.Pending())
	}
}

func TestFirstAckHasOneTimeHint(t *testing.T) {
	g, f, clk, _ := busyGateway(t)
	_ = g.Handle(context.Background(), inb("k", "one"), f.new)
	waitFor(t, func() bool { return len(f.acks()) == 1 })
	clk.add(time.Minute)
	_ = g.Handle(context.Background(), inb("k", "two"), f.new)
	waitFor(t, func() bool { return len(f.acks()) == 2 })
	acks := f.acks()
	if !strings.HasSuffix(acks[0], "\n\n"+BusyHint) {
		t.Fatalf("%q", acks[0])
	}
	if strings.Contains(acks[1], BusyHint) {
		t.Fatalf("%q", acks[1])
	}
}

func TestBusyAckDetail(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	rs := &RunState{
		Started: now.Add(-3*time.Minute - 10*time.Second),
		Round:   func() (int, int) { return 4, 90 },
		Tool:    func() string { return "web_fetch" },
	}
	if got := BusyAckDetail(rs, now); got != "3 min elapsed, iteration 4/90, running: web_fetch" {
		t.Fatal(got)
	}
	// Hermes leaves out a zero elapsed time and an idle tool.
	rs.Started, rs.Tool = now.Add(-10*time.Second), func() string { return "" }
	if got := BusyAckDetail(rs, now); got != "iteration 4/90" {
		t.Fatal(got)
	}
	if BusyAckDetail(nil, now) != "" || BusyAckDetail(&RunState{}, now) != "" {
		t.Fatal("no detail without a run")
	}
	if got := busyAckText(QueueAck, ""); got != "⏳ Queued for the next turn. I'll respond once the current task finishes." {
		t.Fatal(got)
	}
}

func TestDebounceMerges(t *testing.T) {
	var mu sync.Mutex
	var got []string
	add := func(m string) { mu.Lock(); got = append(got, m); mu.Unlock() }
	d := NewDebouncer(350*time.Millisecond, time.Second)
	d.Add("k", "a", add)
	d.Add("k", "b", add)
	d.Add("other", "c", add)
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 2 })
	mu.Lock()
	defer mu.Unlock()
	if !(got[0] == "a\nb" && got[1] == "c" || got[0] == "c" && got[1] == "a\nb") {
		t.Fatal(got)
	}
}

func TestDebounceHardCap(t *testing.T) {
	flushed := make(chan string, 4)
	d := NewDebouncer(100*time.Millisecond, 300*time.Millisecond)
	start := time.Now()
	stop := time.After(600 * time.Millisecond)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	d.Add("k", "x", func(m string) { flushed <- m })
	for loop := true; loop; {
		select {
		case <-tick.C:
			d.Add("k", "x", func(m string) { flushed <- m })
		case <-stop:
			loop = false
		}
	}
	select {
	case <-flushed:
		if el := time.Since(start); el < 250*time.Millisecond {
			t.Fatal("flushed too early:", el)
		}
	default:
		t.Fatal("a steady stream must flush at the hard cap")
	}
}

func TestDebounceDrop(t *testing.T) {
	d := NewDebouncer(20*time.Millisecond, time.Second)
	called := make(chan string, 1)
	d.Add("k", "a", func(m string) { called <- m })
	d.Drop("k")
	select {
	case m := <-called:
		t.Fatal("a dropped text must not flush:", m)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestMediaWhileBusyIsQueuedNotSteered(t *testing.T) {
	g, f, _, _ := busyGateway(t)
	returned := make(chan struct{})
	go func() { _ = g.Handle(context.Background(), media("k"), f.new); close(returned) }()
	waitFor(t, func() bool { return g.queueLen("k") == 1 })
	rs := g.mustRun(t, "k")
	if rs.Steer.Pending() != "" {
		t.Fatal("media must not be steered")
	}
	waitFor(t, func() bool { return len(f.acks()) == 1 })
	if acks := f.acks(); !strings.HasPrefix(acks[0], "⏳ Queued for the next turn (") {
		t.Fatal(acks)
	}
	// The media message runs as its own turn after the current one.
	rs.Cancel()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("the queued media turn did not run")
	}
	if got := f.finals(); len(got) != 2 {
		t.Fatal(got)
	}
}

func TestSteerBeforeAgentStartsQueues(t *testing.T) {
	g := &Gateway{Deps: testDeps(t, &fakeProv{})}
	r := &recRenderer{}
	if g.onBusy(context.Background(), &RunState{Started: time.Now()}, inb("k", "x"), r) {
		t.Fatal("a run with no steer queue cannot take a steer")
	}
	if g.onBusy(context.Background(), nil, inb("k", "x"), r) {
		t.Fatal("no run cannot take a steer")
	}
	rs := &RunState{Steer: agent.NewSteerQueue(), Started: time.Now()}
	if g.onBusy(context.Background(), rs, inb("k", "   "), r) {
		t.Fatal("an empty text cannot be steered")
	}
	if !g.onBusy(context.Background(), rs, inb("k", "go"), r) || rs.Steer.Pending() != "go" || len(r.acks) != 1 {
		t.Fatal(rs.Steer.Pending(), r.acks)
	}

	// Through Handle: the key is busy, but the agent did not start yet.
	g.debounce = NewDebouncer(time.Millisecond, 10*time.Millisecond)
	g.mu.Lock()
	ks := g.key("s")
	ks.busy = true
	ks.current = &pendingTurn{in: inb("s", "first")}
	g.mu.Unlock()
	f := &recFactory{}
	_ = g.Handle(context.Background(), inb("s", "later"), f.new)
	waitFor(t, func() bool { return g.queueLen("s") == 1 })
	waitFor(t, func() bool { return len(f.acks()) == 1 })
	if acks := f.acks(); !strings.HasPrefix(acks[0], "⏳ Queued for the next turn") {
		t.Fatal(acks)
	}
}

func TestStopCancelsAndReplies(t *testing.T) {
	g, _, _, done := busyGateway(t)
	sid := g.mustRun(t, "k").SessionID
	r := &recRenderer{}
	_ = g.Handle(context.Background(), inb("k", "/stop"), func() Renderer { return r })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("/stop must end the run")
	}
	if len(r.acks) != 1 || r.acks[0] != StopReply {
		t.Fatal(r.acks)
	}
	waitFor(t, func() bool { _, ok := g.Running("k"); return !ok })
	r2 := &recRenderer{}
	_ = g.Handle(context.Background(), inb("k", "/stop"), func() Renderer { return r2 })
	if len(r2.acks) != 1 || r2.acks[0] != NoTaskReply {
		t.Fatal(r2.acks)
	}
	// The session stays, so the next message continues it.
	if sess, _ := g.Deps.Store.Active(context.Background(), "k"); sess == nil || sess.ID != sid || sid == "" {
		t.Fatal("the session must stay", sid)
	}
}

func TestStopDropsQueueAndPendingSteer(t *testing.T) {
	g, f, _, done := busyGateway(t)
	rs := g.mustRun(t, "k")
	returned := make(chan struct{})
	go func() { _ = g.Handle(context.Background(), media("k"), f.new); close(returned) }()
	waitFor(t, func() bool { return g.queueLen("k") == 1 })
	rs.Steer.Push("steer text")
	_ = g.Handle(context.Background(), inb("k", "/stop"), func() Renderer { return &recRenderer{} })
	<-done
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("the queued message must be dropped")
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	if got := f.finals(); len(got) != 1 {
		t.Fatal("no turn may run after /stop:", got)
	}
	if _, ok := g.Running("k"); ok {
		t.Fatal("run")
	}
}

func TestStopCancelsBackgroundWork(t *testing.T) {
	g, _, _, _ := busyGateway(t)
	var keys []string
	g.Deps.CancelKey = func(key string) { keys = append(keys, key) }
	_ = g.Handle(context.Background(), inb("k", "/stop"), func() Renderer { return &recRenderer{} })
	if len(keys) != 1 || keys[0] != "k" {
		t.Fatal(keys)
	}
}

func TestNewCancelsRunAndClearsQueue(t *testing.T) {
	g, f, _, done := busyGateway(t)
	old := g.mustRun(t, "k").SessionID
	returned := make(chan struct{})
	go func() { _ = g.Handle(context.Background(), media("k"), f.new); close(returned) }()
	waitFor(t, func() bool { return g.queueLen("k") == 1 })
	r := &recRenderer{}
	_ = g.Handle(context.Background(), inb("k", "/new"), func() Renderer { return r })
	<-done
	<-returned
	if len(r.acks) != 1 || !strings.HasPrefix(r.acks[0], "New session") {
		t.Fatal(r.acks)
	}
	sess, _ := g.Deps.Store.Active(context.Background(), "k")
	if sess.ID == old {
		t.Fatal("the key must have a new session")
	}
	if got := f.finals(); len(got) != 1 {
		t.Fatal(got)
	}
}

func TestSteerCommandModes(t *testing.T) {
	// Running: the text goes into the run.
	g, _, _, _ := busyGateway(t)
	r := &recRenderer{}
	_ = g.Handle(context.Background(), inb("k", "/steer use the cache"), func() Renderer { return r })
	if g.mustRun(t, "k").Steer.Pending() != "use the cache" || len(r.acks) != 1 || !strings.HasPrefix(r.acks[0], "⏩ Steer queued") {
		t.Fatal(r.acks)
	}
	// No text: usage.
	r = &recRenderer{}
	_ = g.Handle(context.Background(), inb("k", "/steer"), func() Renderer { return r })
	if len(r.acks) != 1 || !strings.Contains(r.acks[0], "Usage: /steer") {
		t.Fatal(r.acks)
	}

	// Starting: queued for the next turn.
	g2 := &Gateway{Deps: testDeps(t, &fakeProv{})}
	g2.mu.Lock()
	ks := g2.key("s")
	ks.busy = true
	ks.current = &pendingTurn{in: inb("s", "first")}
	g2.mu.Unlock()
	r = &recRenderer{}
	_ = g2.Handle(context.Background(), inb("s", "/steer x"), func() Renderer { return r })
	if len(r.acks) != 1 || r.acks[0] != SteerStartingReply || g2.queueLen("s") != 1 {
		t.Fatal(r.acks, g2.queueLen("s"))
	}

	// Idle: a normal turn with the text.
	fp := &fakeProv{}
	g3 := &Gateway{Deps: testDeps(t, fp)}
	r = &recRenderer{}
	_ = g3.Handle(context.Background(), inb("i", "/steer x"), func() Renderer { return r })
	if r.final != "ok" || len(fp.lastUserTexts()) != 1 || fp.lastUserTexts()[0] != "x" {
		t.Fatal(r.final, fp.lastUserTexts())
	}
}

func TestStopAndSteerInCommandTable(t *testing.T) {
	all := Commands()
	if _, ok := all["stop"]; !ok {
		t.Fatal("stop")
	}
	if _, ok := all["steer"]; !ok {
		t.Fatal("steer")
	}
	// With no hooks, as in the CLI at the prompt, /stop has no task.
	c, out := cmdCtx(t, "cli:x")
	run(t, c, "/stop")
	if !strings.Contains(out.String(), NoTaskReply) {
		t.Fatal(out.String())
	}
}

func TestQueuedSlashCommandDiscarded(t *testing.T) {
	g, f, _, done := busyGateway(t)
	if err := g.Enqueue(context.Background(), "k", "/frobnicate"); err != nil {
		t.Fatal(err)
	}
	if err := g.Enqueue(context.Background(), "k", "after"); err != nil {
		t.Fatal(err)
	}
	fp := g.Deps.Agent.Prov.(*fakeProv)
	g.mustRun(t, "k").Cancel()
	<-done
	waitFor(t, func() bool { return len(f.finals()) == 2 })
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	for _, u := range fp.lastUserTexts() {
		if strings.HasPrefix(u, "/frobnicate") {
			t.Fatal("a queued slash command must be dropped:", fp.lastUserTexts())
		}
	}
}

func TestBusyTextAfterRunEndsStartsTurn(t *testing.T) {
	fp := &fakeProv{}
	g := &Gateway{Deps: testDeps(t, fp)}
	// The text arrives while the key is busy, and the run ends before the flush.
	g.mu.Lock()
	ks := g.key("k")
	ks.busy = true
	g.mu.Unlock()
	f := &recFactory{}
	g.debounce = NewDebouncer(50*time.Millisecond, time.Second)
	_ = g.Handle(context.Background(), inb("k", "late"), f.new)
	g.mu.Lock()
	ks.busy = false
	g.mu.Unlock()
	waitFor(t, func() bool { return len(f.finals()) == 1 })
	if got := fp.lastUserTexts(); len(got) != 1 || got[0] != "late" {
		t.Fatal(got)
	}
}

func TestSteerReachesRunAfterToolCall(t *testing.T) {
	p := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(p, []byte("data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"path": p})
	fp := &fakeProv{delay: 300 * time.Millisecond, script: []step{
		{calls: []provider.ToolCall{{ID: "c1", Name: "read_file", Args: string(args)}}},
		{text: "done"},
	}}
	d := testDeps(t, fp)
	g := &Gateway{Deps: d}
	g.debounce = NewDebouncer(time.Millisecond, 10*time.Millisecond)
	f := &recFactory{}
	done := make(chan struct{})
	go func() { _ = g.Handle(context.Background(), inb("k", "read it"), f.new); close(done) }()
	waitFor(t, func() bool { _, ok := g.Running("k"); return ok })
	_ = g.Handle(context.Background(), inb("k", "also count lines"), f.new)
	<-done
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	// One turn only: the steer arrived in the run and was not replayed as a new turn.
	if got := f.finals(); len(got) != 1 || got[0] != "done" {
		t.Fatal(got)
	}
	fp.mu.Lock()
	last := fp.reqs[len(fp.reqs)-1].Messages
	fp.mu.Unlock()
	tool := last[len(last)-1]
	if tool.Role != provider.RoleTool || !strings.HasSuffix(tool.Content, agent.SteerMarker+"also count lines") {
		t.Fatalf("%+v", tool)
	}
}

func TestStoppedTurnErrorIsCanceled(t *testing.T) {
	g, f, _, done := busyGateway(t)
	_ = g.Handle(context.Background(), inb("k", "/stop"), func() Renderer { return &recRenderer{} })
	<-done
	f.mu.Lock()
	r := f.list[0]
	f.mu.Unlock()
	if !errors.Is(r.err, context.Canceled) {
		t.Fatal(r.err)
	}
}
