package gateway

import (
	"context"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/skills"
)

const arxivHeader = `[IMPORTANT: The user has invoked the "arxiv" skill`

// busySkillGateway is busyGatewayProv with the skill arxiv in place before the first turn
// starts. loads counts the calls of Deps.Skills.
func busySkillGateway(t *testing.T) (g *Gateway, f *recFactory, done chan struct{}, fp *fakeProv, loads *atomic.Int32) {
	t.Helper()
	g, fp = gatewayWithSlowProvider(t, 2*time.Second)
	addTurnSkill(t, g.Deps, "arxiv", "", "Search arXiv.\n")
	load := g.Deps.Skills
	loads = &atomic.Int32{}
	g.Deps.Skills = func() ([]skills.Skill, []skills.Warning, error) {
		loads.Add(1)
		return load()
	}
	g.debounce = NewDebouncer(time.Millisecond, 10*time.Millisecond)
	f = &recFactory{}
	done = make(chan struct{})
	go func() { _ = g.Handle(context.Background(), inb("k", "first"), f.new); close(done) }()
	waitFor(t, func() bool { _, ok := g.Running("k"); return ok })
	waitFor(t, func() bool { return len(fp.lastUserTexts()) == 1 })
	t.Cleanup(func() {
		if rs, ok := g.Running("k"); ok {
			rs.Cancel()
		}
		<-done
		g.Wait(5 * time.Second)
	})
	return g, f, done, fp, loads
}

// queueSlash sends text on the busy key "k" and waits until it is in the queue. The
// returned channel is closed when the Handle call returns.
func queueSlash(t *testing.T, g *Gateway, f *recFactory, text string) chan struct{} {
	t.Helper()
	returned := make(chan struct{})
	go func() { _ = g.Handle(context.Background(), inb("k", text), f.new); close(returned) }()
	waitFor(t, func() bool { return g.queueLen("k") == 1 })
	return returned
}

func TestSkillCommandOnBusyKeyWaitsInQueue(t *testing.T) {
	g, f, done, fp, loads := busySkillGateway(t)
	returned := queueSlash(t, g, f, "/arxiv transformers 2026")
	rs := g.mustRun(t, "k")
	if rs.Steer.Pending() != "" {
		t.Fatal("a slash text must not be a steer")
	}
	waitFor(t, func() bool { return len(f.acks()) == 1 })
	if acks := f.acks(); !strings.HasPrefix(acks[0], QueueAck) {
		t.Fatal(acks)
	}
	// accept marks the text with no skill load. Only the turn start loads the skills.
	if n := loads.Load(); n != 0 {
		t.Fatal("accept must not load the skills:", n)
	}
	rs.Cancel()
	waitClosed(t, done, "the first turn must end")
	waitFor(t, func() bool { return len(fp.lastUserTexts()) == 2 })
	if got := fp.lastUserTexts(); !strings.HasPrefix(got[1], arxivHeader) {
		t.Fatalf("%q", got)
	}
	if n := loads.Load(); n != 1 {
		t.Fatal("the turn start loads the skills once:", n)
	}
	g.mustRun(t, "k").Cancel()
	waitClosed(t, returned, "the skill turn must end")
}

func TestUnknownSlashOnBusyKeyReachesModel(t *testing.T) {
	g, f, done, fp, _ := busySkillGateway(t)
	returned := queueSlash(t, g, f, "/nothing here")
	waitFor(t, func() bool { return len(f.acks()) == 1 })
	if acks := f.acks(); !strings.HasPrefix(acks[0], QueueAck) {
		t.Fatal(acks)
	}
	g.mustRun(t, "k").Cancel()
	waitClosed(t, done, "the first turn must end")
	waitFor(t, func() bool { return len(fp.lastUserTexts()) == 2 })
	if got := fp.lastUserTexts(); got[1] != "/nothing here" {
		t.Fatalf("%q", got)
	}
	g.mustRun(t, "k").Cancel()
	waitClosed(t, returned, "the queued turn must end")
}

func TestStopAndNewDropQueuedSkillCommand(t *testing.T) {
	for _, cmd := range []string{"/stop", "/new"} {
		t.Run(cmd, func(t *testing.T) {
			g, f, done, fp, loads := busySkillGateway(t)
			returned := queueSlash(t, g, f, "/arxiv x")
			_ = g.Handle(context.Background(), inb("k", cmd), func() Renderer { return &recRenderer{} })
			waitClosed(t, done, cmd+" must end the run")
			waitClosed(t, returned, cmd+" must drop the queued skill command")
			if !g.Wait(5 * time.Second) {
				t.Fatal("wait")
			}
			if got := fp.lastUserTexts(); len(got) != 1 {
				t.Fatalf("%q", got)
			}
			if n := loads.Load(); n != 0 {
				t.Fatal("a dropped skill command loads no skills:", n)
			}
		})
	}
}

func TestStopKeepKeepsQueuedSkillCommand(t *testing.T) {
	g, f, done, fp, _ := busySkillGateway(t)
	returned := queueSlash(t, g, f, "/arxiv x")
	r := &recRenderer{}
	_ = g.Handle(context.Background(), inb("k", "/stop keep"), func() Renderer { return r })
	waitClosed(t, done, "/stop keep must end the run")
	if !reflect.DeepEqual(r.acks, []string{"⏹ **Stopped**\nThe waiting message runs now."}) {
		t.Fatalf("%q", r.acks)
	}
	waitFor(t, func() bool { return len(fp.lastUserTexts()) == 2 })
	if got := fp.lastUserTexts(); !strings.HasPrefix(got[1], arxivHeader) {
		t.Fatalf("%q", got)
	}
	g.mustRun(t, "k").Cancel()
	waitClosed(t, returned, "the skill turn must end")
}

func TestSlashTextIsNeverASteer(t *testing.T) {
	in := inb("k", "/arxiv x")
	in.Slash = true
	if steerable(in) || isCommandText(in) {
		t.Fatal("a marked slash text is a queued message, not a steer and not a command")
	}
	in.Slash = false
	if steerable(in) || !isCommandText(in) {
		t.Fatal("an unmarked slash text is a command")
	}
}

func TestSlashMarkResetOnCopies(t *testing.T) {
	ctx := context.Background()
	g, _ := gatewayWithSlowProvider(t, 0)
	first, _ := heldKey(g)
	f := &recFactory{}
	// The newest message of the key is a skill command, so each copy starts with Slash true.
	last := inb("k", "/arxiv x")
	last.Slash = true
	g.mu.Lock()
	ks := g.key("k")
	ks.last, ks.lastRenderer = last, f.new
	g.mu.Unlock()
	if err := g.Enqueue(ctx, "k", "/arxiv from a batch"); err != nil {
		t.Fatal(err)
	}
	if err := g.enqueue(ctx, "k", "[background task done]", false); err != nil {
		t.Fatal(err)
	}
	if err := g.enqueue(ctx, "k", "/arxiv y", false); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	q := append([]*pendingTurn(nil), ks.queue...)
	goal := g.goalTurnLocked(ks, first, goals.Continuation{SessionID: "s1", Text: "/arxiv goal", Epoch: 1})
	g.mu.Unlock()
	if len(q) != 3 || q[0].in.Slash || q[1].in.Slash || !q[2].in.Slash {
		t.Fatalf("system text, owner text, owner slash text: %+v", q)
	}
	if goal.in.Slash {
		t.Fatal("a goal turn must have Slash false")
	}
}

// The leftover steer of a skill turn copies the message of that turn. The copy must not
// keep the mark, so the steer text does not run as a skill command.
func TestLeftoverSteerOfSkillTurnIsNotASkillCommand(t *testing.T) {
	ctx := context.Background()
	g, fp := gatewayWithSlowProvider(t, 2*time.Second)
	addTurnSkill(t, g.Deps, "arxiv", "", "Search arXiv.\n")
	f := &recFactory{}
	done := make(chan struct{})
	go func() { _ = g.Handle(ctx, inb("k", "/arxiv q"), f.new); close(done) }()
	waitFor(t, func() bool { return len(fp.lastUserTexts()) == 1 })
	rs := g.mustRun(t, "k")
	_ = g.Handle(ctx, inb("k", "/steer /arxiv more"), f.new)
	waitFor(t, func() bool { return rs.Steer.Pending() == "/arxiv more" })
	// A take-over keeps the steer that the turn did not deliver.
	_ = g.Handle(ctx, inb("k", "/stop keep"), func() Renderer { return &recRenderer{} })
	waitClosed(t, done, "/stop keep must end the run")
	t.Cleanup(func() {
		if rs, ok := g.Running("k"); ok {
			rs.Cancel()
		}
		g.Wait(5 * time.Second)
	})
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	got := fp.lastUserTexts()
	if !strings.HasPrefix(got[0], arxivHeader) {
		t.Fatalf("the first turn must be the skill command: %q", got)
	}
	for _, u := range got[1:] {
		if strings.HasPrefix(u, arxivHeader) {
			t.Fatalf("the leftover steer must not run as a skill command: %q", got)
		}
	}
}

func TestResumeRunsQueuedSkillCommand(t *testing.T) {
	ctx := context.Background()
	g, fp, _, _, sess := resumeGateway(t, 0)
	addTurnSkill(t, g.Deps, "arxiv", "", "Search arXiv.\n")
	putMarker(t, g.Deps.Store, "k", sess.ID, []sessions.QueuedText{{Text: "/arxiv transformers 2026"}}, time.Now(), 0)
	if _, err := g.ResumeInterrupted(ctx, time.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(fp.lastUserTexts()) == 2 })
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	if got := fp.lastUserTexts(); got[0] != resumeNote || !strings.HasPrefix(got[1], arxivHeader) {
		t.Fatalf("%q", got)
	}
}
