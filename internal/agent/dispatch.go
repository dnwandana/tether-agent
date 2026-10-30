package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

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
	results := make([]tools.Result, len(calls))
	// The sinks are not safe for concurrent use, so the parallel calls share one lock.
	ls := &lockedSink{s: sink}
	run := func(i int) {
		c := calls[i]
		if ctx.Err() != nil {
			results[i] = tools.Result{Text: cancelledText, IsError: true}
			return
		}
		ls.ToolStart(c.Name, tools.ArgPreview(c.Name, c.Args))
		if deny != "" && !visible[c.Name] {
			results[i] = tools.Result{Text: deny, IsError: true}
		} else {
			results[i] = a.dispatch(ctx, c, env, visible)
		}
		ls.ToolEnd(c.Name, results[i].IsError)
	}
	if len(calls) < 2 || !a.allParallelSafe(calls, visible) {
		for i := range calls {
			run(i)
		}
		return results
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
	return results
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
}

// Observe records the result of one call. kind is kindUnknownTool, kindInvalidJSON, or ""
// for a success. A success resets the count. It returns true at the third failure of the
// same kind in a row.
func (f *failureTracker) Observe(kind string) (stop bool) {
	switch {
	case kind == "":
		f.kind, f.count = "", 0
	case kind == f.kind:
		f.count++
	default:
		f.kind, f.count = kind, 1
	}
	return f.count >= maxSameFailures
}

// Err returns the error that ends the turn after Observe returned true.
func (f *failureTracker) Err() error {
	what := "called an unknown tool"
	if f.kind == kindInvalidJSON {
		what = "sent invalid JSON arguments"
	}
	return fmt.Errorf("stopped: the model %s %d times in a row", what, f.count)
}

// lockedSink serializes the ToolStart and ToolEnd calls of a parallel round.
type lockedSink struct {
	mu sync.Mutex
	s  Sink
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
