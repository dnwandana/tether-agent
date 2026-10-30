package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// specs returns n child specs with the goals g1 to gn.
func specs(n int) []ChildSpec {
	out := make([]ChildSpec, n)
	for i := range out {
		out[i] = ChildSpec{Goal: fmt.Sprintf("g%d", i+1), Name: fmt.Sprintf("%d/%d", i+1, n)}
	}
	return out
}

// slowRun returns a child run that takes d, or ends with ChildCancelled when ctx ends.
func slowRun(d time.Duration) func(context.Context, ChildSpec) ChildResult {
	return func(c context.Context, s ChildSpec) ChildResult {
		select {
		case <-time.After(d):
			return ChildResult{Status: ChildOK, Summary: "did " + s.Goal, ToolCalls: 1, DurationS: d.Seconds()}
		case <-c.Done():
			return ChildResult{Status: ChildCancelled, Error: "the child was cancelled"}
		}
	}
}

// sessionsFor returns a session row for key. The source is the part before the first colon.
func sessionsFor(key string) sessions.Session {
	src, _, _ := strings.Cut(key, ":")
	return sessions.Session{Key: key, Source: src}
}

// insertRunningBatch writes a background batch row with status running.
func insertRunningBatch(t *testing.T, d *db.DB, id, key string) {
	t.Helper()
	_, err := d.ExecContext(ctx, `INSERT INTO delegation_batches (id, parent_session_key, parent_session_id, background, status, tasks, created_at)
		VALUES (?, ?, 's0', 1, 'running', '[]', 1)`, id, key)
	if err != nil {
		t.Fatal(err)
	}
}

// batchRow returns the status and the results of a batch row.
func batchRow(t *testing.T, d *db.DB, id string) (status, results string) {
	t.Helper()
	var res *string
	if err := d.QueryRowContext(ctx, `SELECT status, results FROM delegation_batches WHERE id = ?`, id).Scan(&status, &res); err != nil {
		t.Fatal(err)
	}
	if res != nil {
		results = *res
	}
	return status, results
}

// enqueued records the Enqueue calls of a Batches.
type enqueued struct {
	mu  sync.Mutex
	got []string
}

func (e *enqueued) fn(_ context.Context, k, txt string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.got = append(e.got, k+"|"+txt)
	return nil
}

func (e *enqueued) list() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.got...)
}

func TestBackgroundReturnsImmediatelyThenEnqueues(t *testing.T) {
	d := openTestDB(t)
	q := &enqueued{}
	b := &Batches{DB: d, Enqueue: q.fn}
	id, err := b.Start(ctx, "telegram:dm:1", "s1", specs(3), slowRun(100*time.Millisecond))
	if err != nil || id == "" || len(q.list()) != 0 {
		t.Fatal("must return before children finish", id, err)
	}
	if st, _ := batchRow(t, d, id); st != "running" {
		t.Fatal("the row is written before the children start:", st)
	}
	waitUntil(t, func() bool { return len(q.list()) == 1 })
	key, txt, _ := strings.Cut(q.list()[0], "|")
	if key != "telegram:dm:1" || !strings.HasPrefix(txt, "[Background delegation "+id+" finished]\n") {
		t.Fatal(key, txt)
	}
	if strings.Count(txt, `"status":"ok"`) != 3 || !strings.Contains(txt, `"summary":"did g2"`) {
		t.Fatal(txt)
	}
	st, results := batchRow(t, d, id)
	if st != "done" || !json.Valid([]byte(results)) || !strings.Contains(results, "did g3") {
		t.Fatal(st, results)
	}
}

func TestBackgroundSurvivesTheEndOfTheTurn(t *testing.T) {
	q := &enqueued{}
	b := &Batches{DB: openTestDB(t), Enqueue: q.fn}
	turnCtx, endTurn := context.WithCancel(ctx)
	if _, err := b.Start(turnCtx, "k", "s1", specs(1), slowRun(100*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	endTurn()
	waitUntil(t, func() bool { return len(q.list()) == 1 })
	if !strings.Contains(q.list()[0], `"status":"ok"`) {
		t.Fatal(q.list())
	}
}

func TestCancelKeyStopsBackgroundChildren(t *testing.T) {
	d := openTestDB(t)
	q := &enqueued{}
	b := &Batches{DB: d, Enqueue: q.fn}
	id, err := b.Start(ctx, "telegram:dm:1", "s1", specs(2), slowRun(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	other, _ := b.Start(ctx, "telegram:dm:2", "s2", specs(1), slowRun(300*time.Millisecond))
	b.CancelKey("telegram:dm:1")
	waitUntil(t, func() bool { return len(q.list()) == 2 })
	got := q.list()
	if !strings.HasPrefix(got[0], "telegram:dm:1|[Background delegation "+id+" finished]") || strings.Count(got[0], `"status":"cancelled"`) != 2 {
		t.Fatal("the turn is still enqueued with the cancelled results:", got)
	}
	if !strings.Contains(got[1], other) || !strings.Contains(got[1], `"status":"ok"`) {
		t.Fatal("another key is not cancelled:", got)
	}
	if st, _ := batchRow(t, d, id); st != "cancelled" {
		t.Fatal(st)
	}
}

func TestRecoverLostAfterRestart(t *testing.T) {
	d := openTestDB(t)
	insertRunningBatch(t, d, "b1", "telegram:dm:1")
	q := &enqueued{}
	b := &Batches{DB: d, Enqueue: q.fn}
	if err := b.RecoverLost(ctx); err != nil {
		t.Fatal(err)
	}
	got := q.list()
	if len(got) != 1 || got[0] != "telegram:dm:1|[Background delegation b1 was lost in a restart]" {
		t.Fatal(got)
	}
	if st, _ := batchRow(t, d, "b1"); st != "lost" {
		t.Fatal(st)
	}
	b.RecoverLost(ctx)
	if len(q.list()) != 1 {
		t.Fatal("a lost batch is reported one time")
	}
}

func TestRecoverLostKeepsOwnAndForeignBatches(t *testing.T) {
	d := openTestDB(t)
	insertRunningBatch(t, d, "cli1", "cli:user")
	q := &enqueued{}
	b := &Batches{DB: d, Enqueue: q.fn, Owns: func(key string) bool { return !strings.HasPrefix(key, "cli:") }}
	live, _ := b.Start(ctx, "telegram:dm:1", "s1", specs(1), slowRun(time.Minute))
	defer b.CancelKey("telegram:dm:1")
	if err := b.RecoverLost(ctx); err != nil {
		t.Fatal(err)
	}
	if len(q.list()) != 0 {
		t.Fatal(q.list())
	}
	if st, _ := batchRow(t, d, "cli1"); st != "running" {
		t.Fatal("a batch of another process stays running:", st)
	}
	if st, _ := batchRow(t, d, live); st != "running" {
		t.Fatal("a batch of this process is not lost:", st)
	}
}

func TestChildDepthKeyMatchesTools(t *testing.T) {
	if ChildDepthKey != tools.DelegateDepthKey {
		t.Fatal("agent and tools must use the same depth key")
	}
}

// delegatorAgent returns an agent with delegate_task over a Delegator, and a parent session.
func delegatorAgent(t *testing.T, fp *fakeProv, ts ...tools.Tool) (*Agent, *Delegator) {
	t.Helper()
	a, _ := newAgent(t, fp, ts...)
	dl := &Delegator{Agent: a, Batches: &Batches{DB: a.DB}}
	a.Tools.Register(tools.DelegateTool{Runner: dl, Background: dl})
	return a, dl
}

func TestDelegatorRunsChildWithTurnToolsets(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "web_x", `{"q":"go"}`)), text("child done"))
	a, dl := delegatorAgent(t, fp, echoTool{}, newNamed("web_x", tools.SetWeb))
	parent, _ := a.Store.Create(ctx, sessionsFor("telegram:dm:1"))
	env := &tools.Env{SessionID: parent.ID, SessionKey: parent.Key, Values: map[string]any{}}
	tools.SetTurnTools(env, tools.Resolve([]string{tools.SetWeb}, nil, nil), nil)
	var starts []string
	r := dl.RunChild(ctx, tools.ChildSpecLite{Goal: "x", Name: "1/1", ParentEnv: env,
		OnToolStart: func(name, preview string) { starts = append(starts, name+":"+preview) }})
	if r.Status != "ok" || r.Summary != "child done" || r.ToolCalls != 1 {
		t.Fatalf("%+v", r)
	}
	if names := schemaNames(fp.requests[0]); names["echo"] || !names["web_x"] || names["delegate_task"] {
		t.Fatal("the child gets the toolsets of the parent turn:", names)
	}
	if len(starts) != 1 || starts[0] != "web_x:go" {
		t.Fatal(starts)
	}
}

func TestDelegatorNoParentSession(t *testing.T) {
	a, dl := delegatorAgent(t, scripted())
	_ = a
	r := dl.RunChild(ctx, tools.ChildSpecLite{Goal: "x", ParentEnv: &tools.Env{SessionID: "missing"}})
	if r.Status != ChildError || !strings.Contains(r.Error, "parent session") {
		t.Fatalf("%+v", r)
	}
}

func TestDelegateTaskEndToEnd(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "delegate_task", `{"goal":"find x"}`)), text("child found x"), text("final"))
	a, _ := delegatorAgent(t, fp, echoTool{})
	sess, _ := a.Store.Create(ctx, sessionsFor("cli:user"))
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: &recSink{}, System: "SYS"})
	if err != nil || res.Final != "final" {
		t.Fatal(res, err)
	}
	out := lastToolContent(fp.requests[2])
	if !strings.Contains(out, `"summary":"child found x"`) || !strings.Contains(out, `"status":"ok"`) {
		t.Fatal(out)
	}
	if fp.requests[1].Messages[0].Content != "find x" || schemaNames(fp.requests[1])["delegate_task"] {
		t.Fatal("the child has a fresh conversation without delegate_task")
	}
}

func TestDelegatorBackgroundEnqueuesResults(t *testing.T) {
	fp := scripted(text("child done"))
	a, dl := delegatorAgent(t, fp, echoTool{})
	q := &enqueued{}
	dl.Batches.Enqueue = q.fn
	parent, _ := a.Store.Create(ctx, sessionsFor("telegram:dm:1"))
	env := &tools.Env{SessionID: parent.ID, SessionKey: parent.Key, Values: map[string]any{}, Progress: func(uint64, string, string, string) { t.Error("no progress after the turn") }}
	tools.SetTurnTools(env, all(), nil)
	id, err := dl.StartBackground(ctx, env, []tools.ChildSpecLite{{Goal: "x", Name: "1/1", ParentEnv: env}})
	if err != nil || id == "" {
		t.Fatal(id, err)
	}
	waitUntil(t, func() bool { return len(q.list()) == 1 })
	if got := q.list()[0]; !strings.HasPrefix(got, "telegram:dm:1|[Background delegation "+id+" finished]") || !strings.Contains(got, "child done") {
		t.Fatal(got)
	}
}

func TestDelegatorSharesTheLimit(t *testing.T) {
	a, dl := delegatorAgent(t, scripted())
	a.Cfg.Delegation.MaxConcurrentChildren = 2
	var mu sync.Mutex
	active, peak := 0, 0
	dl.runChild = func(c context.Context, s ChildSpec) ChildResult {
		mu.Lock()
		active++
		peak = max(peak, active)
		mu.Unlock()
		defer func() { mu.Lock(); active--; mu.Unlock() }()
		return slowRun(100*time.Millisecond)(c, s)
	}
	q := &enqueued{}
	dl.Batches.Enqueue = q.fn
	parent, _ := a.Store.Create(ctx, sessionsFor("telegram:dm:1"))
	env := &tools.Env{SessionID: parent.ID, SessionKey: parent.Key, Values: map[string]any{}}
	bg := []tools.ChildSpecLite{{Goal: "a", ParentEnv: env}, {Goal: "b", ParentEnv: env}, {Goal: "c", ParentEnv: env}}
	if _, err := dl.StartBackground(ctx, env, bg); err != nil {
		t.Fatal(err)
	}
	if r := dl.RunChild(ctx, tools.ChildSpecLite{Goal: "d", ParentEnv: env}); r.Status != ChildOK {
		t.Fatalf("%+v", r)
	}
	waitUntil(t, func() bool { return len(q.list()) == 1 })
	if peak != 2 {
		t.Fatal("background and foreground children share max_concurrent_children:", peak)
	}
}

func TestHiddenInChildren(t *testing.T) {
	if !slices.Contains(ChildBlockedTools, tools.ExecuteCodeTool{}.Name()) || !slices.Contains(ChildBlockedTools, tools.DelegateTool{}.Name()) {
		t.Fatal("a child never sees execute_code, and a leaf never sees delegate_task")
	}
}

func TestBatchesCtxEndStopsBatchesForRecoverLost(t *testing.T) {
	d := openTestDB(t)
	q := &enqueued{}
	base, shutdown := context.WithCancel(ctx)
	b := &Batches{DB: d, Enqueue: q.fn, Ctx: base}
	type key struct{}
	turnCtx := context.WithValue(ctx, key{}, "v")
	var (
		mu    sync.Mutex
		value any
	)
	id, err := b.Start(turnCtx, "telegram:dm:1", "s1", specs(2), func(c context.Context, s ChildSpec) ChildResult {
		mu.Lock()
		value = c.Value(key{})
		mu.Unlock()
		return slowRun(time.Minute)(c, s)
	})
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return b.isRunning("telegram:dm:1", id) })
	shutdown()
	if !b.Wait(5 * time.Second) {
		t.Fatal("the children did not stop at the end of Batches.Ctx")
	}
	mu.Lock()
	defer mu.Unlock()
	if value != "v" {
		t.Fatal("the children lost the values of the turn context:", value)
	}
	if got := q.list(); len(got) != 0 {
		t.Fatal("a stopped process must not start a result turn:", got)
	}
	if st, _ := batchRow(t, d, id); st != BatchRunning {
		t.Fatal("the row must stay running, so that the next start reports it as lost:", st)
	}
	if _, err := b.Start(ctx, "k", "s1", specs(1), slowRun(0)); err == nil {
		t.Fatal("Start must refuse a batch after the end of Batches.Ctx")
	}
	next := &Batches{DB: d, Enqueue: q.fn}
	if err := next.RecoverLost(ctx); err != nil {
		t.Fatal(err)
	}
	if got := q.list(); len(got) != 1 || !strings.Contains(got[0], id+" was lost in a restart") {
		t.Fatal(got)
	}
}

func TestBatchesWaitHasABound(t *testing.T) {
	b := &Batches{DB: openTestDB(t), Enqueue: (&enqueued{}).fn}
	release := make(chan struct{})
	if _, err := b.Start(ctx, "k", "s1", specs(1), func(context.Context, ChildSpec) ChildResult {
		<-release
		return ChildResult{Status: ChildOK}
	}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if b.Wait(50 * time.Millisecond) {
		t.Fatal("Wait must return false while a child runs")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("Wait did not keep its bound")
	}
	close(release)
	if !b.Wait(5 * time.Second) {
		t.Fatal("Wait must return true after the children end")
	}
}
