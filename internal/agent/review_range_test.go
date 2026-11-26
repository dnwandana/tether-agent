package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// rangeRow stores one message of the session with the outside sources and returns its id.
func rangeRow(t *testing.T, a *Agent, sess *sessions.Session, i int, outside ...string) int64 {
	t.Helper()
	role := provider.RoleUser
	if i%2 == 0 {
		role = provider.RoleAssistant
	}
	id, err := a.Store.Append(ctx, sess.ID, provider.Message{Role: role, Content: "m" + strconv.Itoa(i)},
		sessions.MessageMeta{Outside: outside})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func rangeKey(t *testing.T, a *Agent, sess *sessions.Session) (string, bool) {
	t.Helper()
	v, ok, err := a.DB.KVGet(ctx, reviewReadKey+sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	return v, ok
}

// rangeReview starts a review and waits for it.
func rangeReview(t *testing.T, a *Agent, sess *sessions.Session) {
	t.Helper()
	a.startReview(ctx, ReviewRequest{Session: sess, Memory: true})
	if !a.WaitReviews(5 * time.Second) {
		t.Fatal("the review did not finish")
	}
}

func TestFirstReviewChecksTheWholeSession(t *testing.T) {
	a, sess, got := reviewAgent(t, scripted())
	rangeRow(t, a, sess, 1)
	rangeRow(t, a, sess, 2, "web_fetch")
	rangeRow(t, a, sess, 3)
	id4 := rangeRow(t, a, sess, 4)
	rangeReview(t, a, sess)
	reqs := got()
	if len(reqs) != 1 || !reflect.DeepEqual(reqs[0].Outside, []string{"web_fetch"}) || len(reqs[0].View) != 4 {
		t.Fatalf("%+v", reqs)
	}
	if v, ok := rangeKey(t, a, sess); !ok || v != strconv.FormatInt(id4, 10) {
		t.Fatal(v, ok)
	}
}

func TestNextReviewChecksOnlyNewRows(t *testing.T) {
	a, sess, got := reviewAgent(t, scripted())
	rangeRow(t, a, sess, 1)
	rangeRow(t, a, sess, 2, "web_fetch")
	rangeReview(t, a, sess)
	rangeRow(t, a, sess, 3)
	rangeReview(t, a, sess)
	id4 := rangeRow(t, a, sess, 4, "mcp_github_search")
	rangeReview(t, a, sess)
	reqs := got()
	if len(reqs) != 3 || reqs[1].Outside != nil || !reflect.DeepEqual(reqs[2].Outside, []string{"mcp_github_search"}) {
		t.Fatalf("%+v", reqs)
	}
	if v, _ := rangeKey(t, a, sess); v != strconv.FormatInt(id4, 10) {
		t.Fatal(v)
	}
}

func TestTerminalSourceDoesNotHold(t *testing.T) {
	a, sess, got := reviewAgent(t, scripted())
	rangeRow(t, a, sess, 1)
	rangeRow(t, a, sess, 2, "terminal:curl", "web_fetch")
	rangeReview(t, a, sess)
	if reqs := got(); len(reqs) != 1 || !reflect.DeepEqual(reqs[0].Outside, []string{"web_fetch"}) {
		t.Fatalf("%+v", reqs)
	}
	b, sessB, gotB := reviewAgent(t, scripted())
	rangeRow(t, b, sessB, 1)
	rangeRow(t, b, sessB, 2, "terminal:git")
	rangeReview(t, b, sessB)
	if reqs := gotB(); len(reqs) != 1 || reqs[0].Outside != nil {
		t.Fatalf("%+v", reqs)
	}
}

func TestBadKeyChecksTheWholeSession(t *testing.T) {
	a, sess, got := reviewAgent(t, scripted())
	rangeRow(t, a, sess, 1)
	rangeRow(t, a, sess, 2, "web_fetch")
	if err := a.DB.KVSet(ctx, reviewReadKey+sess.ID, "x"); err != nil {
		t.Fatal(err)
	}
	rangeReview(t, a, sess)
	if reqs := got(); len(reqs) != 1 || !reflect.DeepEqual(reqs[0].Outside, []string{"web_fetch"}) {
		t.Fatalf("%+v", reqs)
	}
}

func TestCompactionReviewUsesTheFocusSources(t *testing.T) {
	a, sess, got := reviewAgent(t, scripted())
	for i := range 6 {
		userTurnRows(t, a, sess, i)
	}
	if err := a.DB.KVSet(ctx, reviewReadKey+sess.ID, "7"); err != nil {
		t.Fatal(err)
	}
	a.ReviewOnCompaction(ctx, sess, reviewView(t, a, sess), 0, 2, []string{"terminal:git", "web_fetch"})
	if !a.WaitReviews(5 * time.Second) {
		t.Fatal("the review did not finish")
	}
	reqs := got()
	if len(reqs) != 1 || !reflect.DeepEqual(reqs[0].Outside, []string{"web_fetch"}) ||
		reqs[0].FocusRange == nil || *reqs[0].FocusRange != [2]int{0, 2} {
		t.Fatalf("%+v", reqs)
	}
	if v, _ := rangeKey(t, a, sess); v != "7" {
		t.Fatal(v)
	}
}

// userTurnRows stores one user and one assistant row.
func userTurnRows(t *testing.T, a *Agent, sess *sessions.Session, i int) {
	t.Helper()
	for _, m := range []provider.Message{{Role: provider.RoleUser, Content: "q" + strconv.Itoa(i)}, {Role: provider.RoleAssistant, Content: "a" + strconv.Itoa(i)}} {
		if _, err := a.Store.Append(ctx, sess.ID, m, sessions.MessageMeta{}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFailedReviewKeepsTheKey(t *testing.T) {
	a, sess, _ := reviewAgent(t, scripted(step{err: errors.New("boom")}))
	a.reviewHook = nil
	rangeRow(t, a, sess, 1)
	rangeRow(t, a, sess, 2)
	rangeReview(t, a, sess)
	if v, ok := rangeKey(t, a, sess); ok {
		t.Fatal("key set", v)
	}
}

// rangeMemoryTool is a memory tool that records the outside sources of its env.
type rangeMemoryTool struct {
	mu   *sync.Mutex
	seen *[]string
}

func (rangeMemoryTool) Name() string       { return "memory" }
func (rangeMemoryTool) Toolset() string    { return tools.SetMemory }
func (rangeMemoryTool) ParallelSafe() bool { return false }
func (rangeMemoryTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{Name: "memory", Description: "memory", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (o rangeMemoryTool) Run(_ context.Context, _ json.RawMessage, env *tools.Env) (string, error) {
	o.mu.Lock()
	*o.seen = tools.Outside(env).Sources
	o.mu.Unlock()
	return "ok", nil
}

func TestReviewTurnStartsWithTheOutsideSources(t *testing.T) {
	var seen []string
	ot := rangeMemoryTool{mu: &sync.Mutex{}, seen: &seen}
	fp := scripted(toolCalls(tc("r1", "memory", `{}`)), text("done"))
	a, sess := newAgent(t, fp, echoTool{}, ot)
	rangeRow(t, a, sess, 1)
	rangeRow(t, a, sess, 2)
	if _, err := a.RunReview(ctx, ReviewRequest{Session: sess, View: reviewView(t, a, sess), Memory: true,
		Outside: []string{"web_fetch"}}); err != nil {
		t.Fatal(err)
	}
	ot.mu.Lock()
	defer ot.mu.Unlock()
	if !reflect.DeepEqual(seen, []string{"web_fetch"}) {
		t.Fatal(seen)
	}
}
