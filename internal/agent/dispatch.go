package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// The failure kinds that failureTracker counts.
const (
	kindUnknownTool = "unknown_tool"
	kindInvalidJSON = "invalid_json"
)

// maxSameFailures is the number of failures of the same kind in a row that ends a turn.
const maxSameFailures = 3

// cancelledText is the result of a tool call that did not start because the turn stopped.
const cancelledText = "tool call cancelled: the turn was stopped"

// runRound runs the tool calls of one round. The calls run at the same time, at most
// Cfg.Agent.ToolConcurrency at a time, only when every tool of the round is parallel-safe.
// Otherwise they run in order. The results are in call order. When deny is not empty, a
// call to a tool that is not in visible gets the error text deny. A review run uses it.
func (a *Agent) runRound(ctx context.Context, calls []provider.ToolCall, env *tools.Env, visible map[string]bool, sink Sink, deny string) []tools.Result {
	results, _ := a.runRoundTimed(ctx, calls, env, visible, sink, deny)
	return results
}

type callGateKey struct{}

// withCallGate returns ctx with gate. runRoundTimed calls gate before each call. A non-empty
// result refuses the call: it does not run, and the text is its error result.
func withCallGate(ctx context.Context, gate func(c provider.ToolCall) string) context.Context {
	return context.WithValue(ctx, callGateKey{}, gate)
}

// callGate returns the gate of ctx, or nil.
func callGate(ctx context.Context) func(c provider.ToolCall) string {
	g, _ := ctx.Value(callGateKey{}).(func(c provider.ToolCall) string)
	return g
}

// gateText returns the refusal text of the gate of ctx for c, or "" when the call can run.
func gateText(ctx context.Context, c provider.ToolCall) string {
	if g := callGate(ctx); g != nil {
		return g(c)
	}
	return ""
}

// runRoundTimed is runRound that also returns the run time of each call, in call order. A
// call that did not start has 0.
func (a *Agent) runRoundTimed(ctx context.Context, calls []provider.ToolCall, env *tools.Env, visible map[string]bool, sink Sink, deny string) ([]tools.Result, []time.Duration) {
	results := make([]tools.Result, len(calls))
	durs := make([]time.Duration, len(calls))
	// The sinks are not safe for concurrent use, so the parallel calls share one lock.
	ls := &lockedSink{s: sink}
	run := func(i int) {
		c := calls[i]
		if ctx.Err() != nil {
			results[i] = tools.Result{Text: cancelledText, IsError: true}
			return
		}
		ls.begin(c.ID, c.Name, json.RawMessage(c.Args), tools.ArgPreview(c.Name, c.Args, a.config().Agent.ToolPreviewChars))
		// todo_list is not parallel-safe, so no other call of the round writes the list
		// between the two reads.
		todo := c.Name == "todo_list" && env != nil && env.Kind != KindSubagent
		var w0 int64
		if todo {
			_, _, w0 = tools.TodoStoreFor(env.Values).Change()
		}
		t0 := time.Now()
		if deny != "" && !visible[c.Name] {
			results[i] = tools.Result{Text: deny, IsError: true}
		} else if msg := gateText(ctx, c); msg != "" {
			results[i] = tools.Result{Text: msg, IsError: true}
		} else {
			results[i] = a.dispatch(ctx, c, env, visible)
		}
		durs[i] = time.Since(t0)
		ls.ToolEnd(c.Name, results[i].IsError)
		if todo && !results[i].IsError {
			before, items, w1 := tools.TodoStoreFor(env.Values).Change()
			// The store keeps the list before the last write. A call that wrote nothing
			// changed nothing, so its list before the call is the list now.
			if w1 == w0 {
				before = items
			}
			ls.Plan(before, items)
		}
	}
	if len(calls) < 2 || !a.allParallelSafe(calls, visible) {
		for i := range calls {
			run(i)
		}
		return results, durs
	}
	n := max(a.config().Agent.ToolConcurrency, 1)
	sem := make(chan struct{}, n)
	var wg sync.WaitGroup
	for i := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			run(i)
		}()
	}
	wg.Wait()
	return results, durs
}

// allParallelSafe reports whether every call names a visible tool that is parallel-safe.
func (a *Agent) allParallelSafe(calls []provider.ToolCall, visible map[string]bool) bool {
	if a.Tools == nil {
		return false
	}
	for _, c := range calls {
		t, ok := a.Tools.Get(c.Name)
		if !ok || !visible[c.Name] || !t.ParallelSafe() {
			return false
		}
	}
	return true
}

// dispatch runs one tool call through the registry.
func (a *Agent) dispatch(ctx context.Context, c provider.ToolCall, env *tools.Env, visible map[string]bool) tools.Result {
	reg := a.Tools
	if reg == nil {
		reg = tools.NewRegistry()
	}
	return reg.Dispatch(ctx, c.Name, json.RawMessage(c.Args), env, visible)
}

// failureKind returns the failure kind of a call, or "" for a call that the registry can run.
// A tool that is not visible counts as an unknown tool, because the model cannot use it.
func (a *Agent) failureKind(c provider.ToolCall, visible map[string]bool) string {
	if a.Tools == nil {
		return kindUnknownTool
	}
	if _, ok := a.Tools.Get(c.Name); !ok || !visible[c.Name] {
		return kindUnknownTool
	}
	if args := strings.TrimSpace(c.Args); args != "" && !json.Valid([]byte(args)) {
		return kindInvalidJSON
	}
	return ""
}

// failureTracker counts the failures of the same kind in a row (Hermes).
type failureTracker struct {
	kind  string
	count int
	// limit is the count that stops the turn. 0 or less uses maxSameFailures.
	limit int
}

// Observe records the result of one call. kind is kindUnknownTool, kindInvalidJSON, or ""
// for a success. A success resets the count. It returns true at the failure at the limit
// of the same kind in a row.
func (f *failureTracker) Observe(kind string) (stop bool) {
	switch {
	case kind == "":
		f.kind, f.count = "", 0
	case kind == f.kind:
		f.count++
	default:
		f.kind, f.count = kind, 1
	}
	n := f.limit
	if n <= 0 {
		n = maxSameFailures
	}
	return f.count >= n
}

// Err returns the error that ends the turn after Observe returned true.
func (f *failureTracker) Err() error {
	what := "called an unknown tool"
	if f.kind == kindInvalidJSON {
		what = "sent invalid JSON arguments"
	}
	return fmt.Errorf("stopped: the model %s %d times in a row", what, f.count)
}

// lockedSink serializes the sink calls of a parallel round.
type lockedSink struct {
	mu sync.Mutex
	s  Sink
}

// begin sends ToolCall, when the inner sink is a ToolDetailSink, and then ToolStart, in one
// hold of the lock. So the calls of two parallel tool calls never mix.
func (l *lockedSink) begin(callID, name string, args json.RawMessage, preview string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if d, ok := l.s.(ToolDetailSink); ok {
		d.ToolCall(callID, name, args)
	}
	l.s.ToolStart(name, preview)
}

func (l *lockedSink) ToolStart(name, preview string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.s.ToolStart(name, preview)
}

func (l *lockedSink) ToolEnd(name string, isError bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.s.ToolEnd(name, isError)
}

// Plan forwards the two task lists when the inner sink is a PlanSink. Else it does nothing.
func (l *lockedSink) Plan(before, after []tools.TodoItem) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if p, ok := l.s.(PlanSink); ok {
		p.Plan(before, after)
	}
}
