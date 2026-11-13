package gateway

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// A queued member message keeps its owner flag in the marker.
func TestMarkerKeepsTheOwnerFlagOfAQueuedText(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g, fp := gatewayWithSlowProvider(t, 5*time.Second)
	f := &recFactory{}
	g.Submit(ctx, sharedInb("k", "7", "first"), f.new, nil)
	waitFor(t, func() bool { return requests(fp) == 1 })
	look := photoIn("k", "look") // a queued message, not a steer
	look.Owner, look.Shared, look.SenderID = false, true, "8"
	g.Submit(ctx, look, f.new, nil)
	g.Submit(ctx, photoIn("k", "mine"), f.new, nil)
	g.BeginStopping(stopCtx(t))
	wantMarker(t, g, "k", []sessions.QueuedText{
		{Text: "look", Lost: 1, Owner: new(false)},
		{Text: "mine", Lost: 1, Owner: new(true)},
	})
	cancel()
	g.Wait(5 * time.Second)
}

// sharedMember is a text of the member uid in a shared session.
func sharedMember(key, uid, text string) Inbound {
	in := sharedInb(key, uid, text)
	in.Owner = false
	return in
}

// recordTurnOwners sets NewTurnEnv of g. It returns a function that gives the owner flag
// of each turn that started, in order (!Env.Member). Read it after the turns end.
func recordTurnOwners(g *Gateway) func() []bool {
	var mu sync.Mutex
	var envs []*tools.Env
	g.Deps.NewTurnEnv = func(sess *sessions.Session, _ string, _ string) *tools.Env {
		env := &tools.Env{SessionID: sess.ID, SessionKey: sess.Key, Values: map[string]any{}}
		mu.Lock()
		envs = append(envs, env)
		mu.Unlock()
		return env
	}
	return func() []bool {
		mu.Lock()
		defer mu.Unlock()
		out := make([]bool, len(envs))
		for i, e := range envs {
			out[i] = !e.Member
		}
		return out
	}
}

// webProbe is a tool of the set "web", which the default member list allows.
type webProbe struct{}

func (webProbe) Name() string       { return "web_probe" }
func (webProbe) Toolset() string    { return tools.SetWeb }
func (webProbe) ParallelSafe() bool { return true }
func (webProbe) Schema() provider.ToolSchema {
	return provider.ToolSchema{Name: "web_probe", Description: "probe", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (webProbe) Run(context.Context, json.RawMessage, *tools.Env) (string, error) { return "ok", nil }

// memberTurn runs one Deps.Turn on the channel "telegram" and returns the Env and the
// schema names of the first request.
func memberTurn(t *testing.T, in TurnInput) (*tools.Env, []string) {
	t.Helper()
	ctx := context.Background()
	fp := &fakeProv{}
	d := testDeps(t, fp)
	d.Agent.Tools.Register(webProbe{})
	var env *tools.Env
	d.NewEnv = func(s *sessions.Session, _ string) *tools.Env {
		env = &tools.Env{SessionID: s.ID, SessionKey: s.Key, Values: map[string]any{}}
		return env
	}
	sess, err := d.Store.Create(ctx, sessions.Session{Key: "telegram:group:-100", Source: "telegram"})
	if err != nil {
		t.Fatal(err)
	}
	in.Text, in.Channel, in.Sink = "hi", "telegram", &CLISink{W: io.Discard}
	if _, err := d.Turn(ctx, sess, in); err != nil {
		t.Fatal(err)
	}
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if len(fp.reqs) == 0 {
		t.Fatal("no model request")
	}
	var names []string
	for _, s := range fp.reqs[0].Tools {
		names = append(names, s.Name)
	}
	slices.Sort(names)
	return env, names
}

func TestTurnMemberSessionGetsMemberSchemas(t *testing.T) {
	env, names := memberTurn(t, TurnInput{Owner: false, Shared: false})
	if !slices.Equal(names, []string{"web_probe"}) || !env.Member {
		t.Fatal(names, env.Member)
	}
}

func TestTurnSharedMemberKeepsChannelSchemas(t *testing.T) {
	env, names := memberTurn(t, TurnInput{Owner: false, Shared: true})
	if !slices.Equal(names, []string{"read_file", "web_probe"}) || !env.Member {
		t.Fatal(names, env.Member)
	}
	sets, visible := tools.TurnTools(env)
	if len(sets) != 3 || !sets["web"] || !sets["ask"] || !sets["todo"] {
		t.Fatal(sets)
	}
	if len(visible) != 1 || !visible["web_probe"] {
		t.Fatal(visible)
	}
}

func TestTurnOwnerGetsChannelTools(t *testing.T) {
	env, names := memberTurn(t, TurnInput{Owner: true, Shared: true})
	if !slices.Equal(names, []string{"read_file", "web_probe"}) || env.Member {
		t.Fatal(names, env.Member)
	}
}

func TestGatewayTurnCopiesOwnerFlag(t *testing.T) {
	ctx := context.Background()
	g := &Gateway{Deps: testDeps(t, &fakeProv{})}
	owners := recordTurnOwners(g)
	f := &recFactory{}
	if err := g.Handle(ctx, sharedMember("k", "8", "a"), f.new); err != nil {
		t.Fatal(err)
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("the turns did not end")
	}
	if err := g.Handle(ctx, sharedInb("k", "7", "b"), f.new); err != nil {
		t.Fatal(err)
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("the turns did not end")
	}
	if got := owners(); !slices.Equal(got, []bool{false, true}) {
		t.Fatal(got)
	}
}

func TestCLITurnIsOwner(t *testing.T) {
	ctx := context.Background()
	d := testDeps(t, &fakeProv{})
	var env *tools.Env
	d.NewEnv = func(s *sessions.Session, _ string) *tools.Env {
		env = &tools.Env{SessionID: s.ID, SessionKey: s.Key, Values: map[string]any{}}
		return env
	}
	c := &CLI{Deps: d, Key: "cli:test"}
	sess, err := d.Store.Create(ctx, sessions.Session{Key: "cli:test", Source: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.AgentTurn(ctx, sess, "hi", io.Discard); err != nil {
		t.Fatal(err)
	}
	if env == nil || env.Member {
		t.Fatal("the CLI turn is not an owner turn")
	}
}

func TestOwnerOfDefaults(t *testing.T) {
	sess := &sessions.Session{ID: "s1"}
	check := func(name string, d *Deps, s *sessions.Session, wantOwner, wantShared bool) {
		t.Helper()
		if o, sh := d.ownerOf(s); o != wantOwner || sh != wantShared {
			t.Fatalf("%s: got %v %v", name, o, sh)
		}
	}
	check("no hook", &Deps{}, sess, true, false)
	hook := &Deps{OwnerOf: func(*sessions.Session) (bool, bool) { return false, true }}
	check("nil session", hook, nil, true, false)
	check("hook", hook, sess, false, true)
	var nilDeps *Deps
	check("nil deps", nilDeps, sess, true, false)
}

// firstSchemaNames returns the sorted tool names of the first request of fp.
func firstSchemaNames(t *testing.T, fp *fakeProv) []string {
	t.Helper()
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if len(fp.reqs) == 0 {
		t.Fatal("no model request")
	}
	var names []string
	for _, s := range fp.reqs[0].Tools {
		names = append(names, s.Name)
	}
	slices.Sort(names)
	return names
}

// A v0.18 marker row in a shared session restores as member turns.
func TestResumeV018MarkerInSharedSessionRunsMemberTurns(t *testing.T) {
	ctx := context.Background()
	g, fp, _, _, sess := resumeGateway(t, 0)
	g.Deps.OwnerOf = func(*sessions.Session) (bool, bool) { return false, true }
	owners := recordTurnOwners(g)
	putMarker(t, g.Deps.Store, "k", sess.ID, []sessions.QueuedText{{Text: "hi"}}, time.Now(), 0)
	if _, err := g.ResumeInterrupted(ctx, time.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("the turns did not end")
	}
	if got := fp.lastUserTexts(); !slices.Equal(got, []string{resumeNote, "hi"}) {
		t.Fatalf("%q", got)
	}
	if got := owners(); !slices.Equal(got, []bool{false, false}) {
		t.Fatal(got)
	}
	// The shared session keeps the channel schemas.
	if names := firstSchemaNames(t, fp); !slices.Contains(names, "read_file") {
		t.Fatal(names)
	}
}

// A stored owner flag counts only when the hook still gives owner. A text with a member
// flag stays a member turn.
func TestResumeQueuedTextKeepsItsOwnerFlag(t *testing.T) {
	ctx := context.Background()
	g, _, _, _, sess := resumeGateway(t, 0)
	g.Deps.OwnerOf = func(*sessions.Session) (bool, bool) { return true, true }
	owners := recordTurnOwners(g)
	putMarker(t, g.Deps.Store, "k", sess.ID, []sessions.QueuedText{
		{Text: "a", Owner: new(true)},
		{Text: "b", Owner: new(false)},
	}, time.Now(), 0)
	if _, err := g.ResumeInterrupted(ctx, time.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("the turns did not end")
	}
	if got := owners(); !slices.Equal(got, []bool{true, true, false}) {
		t.Fatal(got)
	}
}

// A user that left the allow list after the marker was saved gets a member turn.
func TestResumeStoredOwnerFlagNeedsCurrentOwner(t *testing.T) {
	ctx := context.Background()
	g, _, _, _, sess := resumeGateway(t, 0)
	g.Deps.OwnerOf = func(*sessions.Session) (bool, bool) { return false, true }
	owners := recordTurnOwners(g)
	putMarker(t, g.Deps.Store, "k", sess.ID, []sessions.QueuedText{{Text: "a", Owner: new(true)}}, time.Now(), 0)
	if _, err := g.ResumeInterrupted(ctx, time.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("the turns did not end")
	}
	if got := owners(); !slices.Equal(got, []bool{false, false}) {
		t.Fatal(got)
	}
}

// When the session read fails, a text with no owner flag, and a text with an owner flag,
// both restore as member turns.
func TestResumeUnknownSessionRestoresMemberTurns(t *testing.T) {
	ctx := context.Background()
	g, _, _, _, sess := resumeGateway(t, 0)
	owners := recordTurnOwners(g)
	m := sessions.TurnMarker{SessionKey: "k", SessionID: sess.ID, Queued: []sessions.QueuedText{
		{Text: "a"}, {Text: "b", Owner: new(true)},
	}}
	g.enqueueQueued(ctx, m, nil, true)
	if !g.Wait(5 * time.Second) {
		t.Fatal("the turns did not end")
	}
	if got := owners(); !slices.Equal(got, []bool{false, false}) {
		t.Fatal(got)
	}
}

// With no hook, a v0.18 row restores as owner turns.
func TestResumeWithNoHookRunsOwnerTurns(t *testing.T) {
	ctx := context.Background()
	g, _, _, _, sess := resumeGateway(t, 0)
	owners := recordTurnOwners(g)
	putMarker(t, g.Deps.Store, "k", sess.ID, []sessions.QueuedText{{Text: "hi"}}, time.Now(), 0)
	if _, err := g.ResumeInterrupted(ctx, time.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("the turns did not end")
	}
	if got := owners(); !slices.Equal(got, []bool{true, true}) {
		t.Fatal(got)
	}
}

// Enqueue is an owner turn and EnqueueAs takes the flag, also after a member message.
func TestEnqueueOwnerFlag(t *testing.T) {
	ctx := context.Background()
	g := &Gateway{Deps: testDeps(t, &fakeProv{})}
	owners := recordTurnOwners(g)
	f := &recFactory{}
	if err := g.Handle(ctx, sharedMember("k", "8", "hi"), f.new); err != nil {
		t.Fatal(err)
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("the turns did not end")
	}
	if err := g.Enqueue(ctx, "k", "batch result"); err != nil {
		t.Fatal(err)
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("the turns did not end")
	}
	if err := g.EnqueueAs(ctx, "k", "member result", false); err != nil {
		t.Fatal(err)
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("the turns did not end")
	}
	if got := owners(); !slices.Equal(got, []bool{false, true, false}) {
		t.Fatal(got)
	}
}

// A goal turn after a member message is an owner turn.
func TestGoalTurnIsOwnerTurn(t *testing.T) {
	ctx := t.Context()
	g, fp := gatewayWithSlowProvider(t, 2*time.Second)
	f := &recFactory{}
	g.Submit(ctx, sharedMember("k", "8", "first"), f.new, nil)
	waitFor(t, func() bool { return requests(fp) == 1 })
	g.mu.Lock()
	ks := g.key("k")
	p := g.goalTurnLocked(ks, &pendingTurn{in: sharedMember("k", "8", "x")}, goals.Continuation{Text: "go", SessionID: "s", Epoch: 2})
	g.mu.Unlock()
	if !p.in.Owner {
		t.Fatal("the goal turn is not an owner turn")
	}
	if err := g.enqueueGoal(ctx, "k", goals.Continuation{Text: "go on", SessionID: "s", Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	q := g.key("k").queue
	ok := len(q) > 0 && q[len(q)-1].goalTurn && q[len(q)-1].in.Owner
	g.mu.Unlock()
	if !ok {
		t.Fatal("the queued goal turn is not an owner turn")
	}
	g.mustRun(t, "k").Cancel()
	g.Wait(5 * time.Second)
}

// The debouncer gives the AND of the owner flags of the parts.
func TestDebouncerOwnerIsTheANDOfTheParts(t *testing.T) {
	type flushed struct {
		text, sender string
		owner        bool
	}
	out := make(chan flushed, 4)
	fl := func(m, s string, o bool) { out <- flushed{m, s, o} }
	d := NewDebouncer(time.Hour, time.Hour)
	d.AddFrom("k", "7", "a", true, fl)
	d.AddFrom("k", "8", "b", false, fl)
	if text, sender, owner := d.TakeFrom("k"); text != "a\nb" || sender != "" || owner {
		t.Fatal(text, sender, owner)
	}
	d.AddFrom("k", "7", "a", true, fl)
	d.AddFrom("k", "7", "b", true, fl)
	if _, _, owner := d.TakeFrom("k"); !owner {
		t.Fatal("two owner parts must give an owner text")
	}
	if _, _, owner := d.TakeFrom("k"); owner {
		t.Fatal("a text with no part is not an owner text")
	}
	d = NewDebouncer(20*time.Millisecond, time.Second)
	d.AddFrom("k", "7", "a", true, fl)
	d.AddFrom("k", "8", "b", false, fl)
	if got := <-out; got != (flushed{"a\nb", "", false}) {
		t.Fatal(got)
	}
	d.AddFrom("k", "7", "a", true, fl)
	d.AddFrom("k", "7", "b", true, fl)
	if got := <-out; got != (flushed{"a\nb", "7", true}) {
		t.Fatal(got)
	}
}

// mergedTurnOwners runs the turn of first on the shared key "k", sends texts in one
// debounce window, and returns the owner flag of each turn. The merged text is the
// second turn: a left-over steer.
func mergedTurnOwners(t *testing.T, first Inbound, texts ...Inbound) []bool {
	t.Helper()
	ctx := context.Background()
	g, fp := gatewayWithSlowProvider(t, 300*time.Millisecond)
	g.debounce = NewDebouncer(50*time.Millisecond, time.Second)
	owners := recordTurnOwners(g)
	f := &recFactory{}
	done := make(chan struct{})
	go func() { _ = g.Handle(ctx, first, f.new); close(done) }()
	waitFor(t, func() bool { return len(fp.lastUserTexts()) == 1 })
	for _, in := range texts {
		_ = g.Handle(ctx, in, f.new)
	}
	waitFor(t, func() bool { return !g.debouncer().Pending() })
	<-done
	if !g.Wait(5 * time.Second) {
		t.Fatal("the turns did not end")
	}
	if got := fp.lastUserTexts(); !slices.Equal(got, []string{first.Text, "a\nb"}) {
		t.Fatalf("%q", got)
	}
	return owners()
}

// A debounced merge of an owner text and a member text is a member turn.
func TestMergeOfOwnerAndMemberIsMemberTurn(t *testing.T) {
	got := mergedTurnOwners(t, sharedInb("k", "7", "first"), sharedInb("k", "7", "a"), sharedMember("k", "8", "b"))
	if !slices.Equal(got, []bool{true, false}) {
		t.Fatal(got)
	}
}

// A left-over steer of owner texts in a member turn is an owner turn: the flag comes
// from the steers, not from the running turn.
func TestLeftoverOwnerSteerIsOwnerTurn(t *testing.T) {
	got := mergedTurnOwners(t, sharedMember("k", "8", "first"), sharedInb("k", "7", "a"), sharedInb("k", "7", "b"))
	if !slices.Equal(got, []bool{false, true}) {
		t.Fatal(got)
	}
}

// nowMovedOwner is nowMovedUser (busy_test.go) with recordTurnOwners in place of
// recordTurnUsers. It returns the owner flag of the moved turn.
func nowMovedOwner(t *testing.T, steers, late []Inbound) bool {
	t.Helper()
	ctx := context.Background()
	g, fp := gatewayWithSlowProvider(t, 2*time.Second)
	g.debounce = NewDebouncer(time.Millisecond, 10*time.Millisecond)
	owners := recordTurnOwners(g)
	done := make(chan struct{})
	go func() { _ = g.Handle(ctx, sharedInb("k", "7", "first"), (&recFactory{}).new); close(done) }()
	waitFor(t, func() bool { return len(fp.lastUserTexts()) == 1 })
	rs := g.mustRun(t, "k")
	for _, in := range steers {
		_ = g.Handle(ctx, in, (&recFactory{}).new)
		waitFor(t, func() bool { return strings.Contains(rs.Steer.Pending(), in.Text) })
	}
	g.mu.Lock()
	g.debounce = NewDebouncer(time.Hour, time.Hour)
	g.mu.Unlock()
	for _, in := range late {
		_ = g.Handle(ctx, in, (&recFactory{}).new)
	}
	wantNowReply(t, sendNow(t, g, "/now"), nowOneReply)
	waitClosed(t, done, "/now must end the run")
	waitFor(t, func() bool { return len(owners()) == 2 })
	g.mustRun(t, "k").Cancel()
	if !g.Wait(5 * time.Second) {
		t.Fatal("wait")
	}
	return owners()[1]
}

// A /now move of an owner steer and a member text is a member turn.
func TestNowMoveOfMemberTextIsMemberTurn(t *testing.T) {
	if nowMovedOwner(t, []Inbound{sharedInb("k", "7", "a")}, []Inbound{sharedMember("k", "8", "b")}) {
		t.Fatal("a member part must make a member turn")
	}
	if !nowMovedOwner(t, []Inbound{sharedInb("k", "7", "a")}, []Inbound{sharedInb("k", "7", "b")}) {
		t.Fatal("owner parts must make an owner turn")
	}
}

// The marker keeps the owner flag of the debounced text.
func TestBeginStoppingKeepsTheOwnerFlagOfTheMergedText(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g, fp := gatewayWithSlowProvider(t, 5*time.Second)
	g.debounce = NewDebouncer(time.Hour, time.Hour)
	f := &recFactory{}
	g.Submit(ctx, sharedInb("k", "7", "first"), f.new, nil)
	waitFor(t, func() bool { return requests(fp) == 1 })
	g.Submit(ctx, sharedMember("k", "8", "late"), f.new, nil)
	g.BeginStopping(stopCtx(t))
	wantMarker(t, g, "k", []sessions.QueuedText{{Text: "late", Owner: new(false)}})
	cancel()
	g.Wait(5 * time.Second)
}

// memberSteerRun runs the turn of first on the key "k" with a slow provider and a short
// debounce window, sends next while the turn runs, and returns the gateway, the provider,
// the factory, the owner recorder, and the run state.
func memberSteerRun(t *testing.T, first, next Inbound) (*Gateway, *fakeProv, *recFactory, func() []bool, *RunState) {
	t.Helper()
	g, fp := gatewayWithSlowProvider(t, 300*time.Millisecond)
	g.debounce = NewDebouncer(time.Millisecond, 10*time.Millisecond)
	owners := recordTurnOwners(g)
	f := &recFactory{}
	ctx := context.Background()
	done := make(chan struct{})
	go func() { _ = g.Handle(ctx, first, f.new); close(done) }()
	waitFor(t, func() bool { return requests(fp) == 1 })
	rs := g.mustRun(t, "k")
	_ = g.Handle(ctx, next, f.new)
	t.Cleanup(func() {
		<-done
		g.Wait(5 * time.Second)
	})
	return g, fp, f, owners, rs
}

// The running state keeps the owner flag of the turn.
func TestRunStateKeepsOwnerFlag(t *testing.T) {
	for _, tc := range []struct {
		first Inbound
		want  bool
	}{
		{sharedInb("k", "7", "first"), true},
		{sharedMember("k", "8", "first"), false},
	} {
		g, _, _, _, rs := memberSteerRun(t, tc.first, sharedInb("k", "7", "b"))
		g.mu.Lock()
		got := rs.owner
		g.mu.Unlock()
		if got != tc.want {
			t.Fatalf("owner = %v, want %v", got, tc.want)
		}
		g.Wait(5 * time.Second)
	}
}

// A member text during an owner turn in a shared session waits as a member turn.
func TestMemberTextWaitsBehindOwnerTurn(t *testing.T) {
	g, fp, f, owners, rs := memberSteerRun(t, sharedInb("k", "7", "first"), sharedMember("k", "8", "b"))
	waitFor(t, func() bool { return slices.Contains(f.acks(), QueueAck) })
	if p := rs.Steer.Pending(); p != "" {
		t.Fatalf("steer = %q", p)
	}
	g.Wait(5 * time.Second)
	if got := fp.lastUserTexts(); !slices.Equal(got, []string{"first", "b"}) {
		t.Fatal(got)
	}
	if got := owners(); !slices.Equal(got, []bool{true, false}) {
		t.Fatal(got)
	}
}

// A member /steer during an owner turn in a shared session waits as a member turn.
func TestMemberSteerCommandWaitsBehindOwnerTurn(t *testing.T) {
	g, fp, f, owners, rs := memberSteerRun(t, sharedInb("k", "7", "first"), sharedMember("k", "8", "/steer b"))
	waitFor(t, func() bool { return slices.Contains(f.acks(), QueueAck) })
	if slices.Contains(f.acks(), SteerAck) || rs.Steer.Pending() != "" {
		t.Fatal(f.acks(), rs.Steer.Pending())
	}
	g.Wait(5 * time.Second)
	if got := fp.lastUserTexts(); !slices.Equal(got, []string{"first", "b"}) {
		t.Fatal(got)
	}
	if got := owners(); !slices.Equal(got, []bool{true, false}) {
		t.Fatal(got)
	}
}

// A member text steers a member turn.
func TestMemberTextSteersMemberTurn(t *testing.T) {
	_, _, _, _, rs := memberSteerRun(t, sharedMember("k", "8", "first"), sharedMember("k", "9", "b"))
	waitFor(t, func() bool { return rs.Steer.Pending() == "b" })
}

// An owner text steers a member turn.
func TestOwnerTextSteersMemberTurn(t *testing.T) {
	_, _, _, _, rs := memberSteerRun(t, sharedMember("k", "8", "first"), sharedInb("k", "7", "b"))
	waitFor(t, func() bool { return rs.Steer.Pending() == "b" })
}

// In a session of its own, a member text steers the turn as before.
func TestMemberTextSteersOwnTurn(t *testing.T) {
	_, _, _, _, rs := memberSteerRun(t, member("k", "first"), member("k", "b"))
	waitFor(t, func() bool { return rs.Steer.Pending() == "b" })
}

// Refused member steers of one sender merge into one waiting member turn. The queue
// does not grow with each burst.
func TestRefusedMemberSteersMergeIntoOneQueuedTurn(t *testing.T) {
	ctx := t.Context()
	g, fp := gatewayWithSlowProvider(t, 2*time.Second)
	f := &recFactory{}
	g.Submit(ctx, sharedInb("k", "7", "first"), f.new, nil)
	waitFor(t, func() bool { return requests(fp) == 1 })
	want := make([]string, 0, 30)
	for i := range 30 {
		text := "m" + strconv.Itoa(i)
		want = append(want, text)
		g.flushBusyText(ctx, sharedMember("k", "8", text), f.new)
	}
	g.mu.Lock()
	q := g.key("k").queue
	n, text := len(q), ""
	if n > 0 {
		text = q[0].in.Text
	}
	g.mu.Unlock()
	if n != 1 || text != strings.Join(want, "\n") {
		t.Fatalf("queue = %d, text = %q", n, text)
	}
	g.mustRun(t, "k").Cancel()
	g.Wait(5 * time.Second)
}

// A refused member steer that cannot merge obeys the queue limit.
func TestRefusedMemberSteerObeysQueueLimit(t *testing.T) {
	ctx := t.Context()
	g, fp := gatewayWithSlowProvider(t, 2*time.Second)
	f := &recFactory{}
	g.Submit(ctx, sharedInb("k", "7", "first"), f.new, nil)
	waitFor(t, func() bool { return requests(fp) == 1 })
	for range g.Deps.queueLimit() {
		g.Submit(ctx, photoIn("k", "p"), f.new, nil)
	}
	g.flushBusyText(ctx, sharedMember("k", "8", "late"), f.new)
	g.mu.Lock()
	n := len(g.key("k").queue)
	g.mu.Unlock()
	if n != g.Deps.queueLimit() {
		t.Fatalf("queue = %d", n)
	}
	if !slices.Contains(f.acks(), QueueFullReply) {
		t.Fatal(f.acks())
	}
	g.mustRun(t, "k").Cancel()
	g.Wait(5 * time.Second)
}
