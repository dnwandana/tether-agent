package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/pricing"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// ctx is the context for the tests.
var ctx = context.Background()

// step is one scripted model response.
type step struct {
	text  string
	calls []provider.ToolCall
	usage provider.Usage
	// err ends the stream with an EventError after the text and the calls.
	err error
	// stall makes the stream send the text and then wait until the context ends.
	stall bool
	// noClose sends the text and then never closes the channel and ignores the context.
	noClose bool
	// reasoning goes on the EventDone event.
	reasoning *provider.Reasoning
	// provider is the entry name that the step stamps on each event. "" keeps the stamp of
	// fakeProv.
	provider string
}

// fakeProv is a scripted provider.Provider. It records each request.
type fakeProv struct {
	name, model string
	// stamp puts the name and the request model (else model) on every event, like an
	// adapter does.
	stamp bool

	mu        sync.Mutex
	script    []step
	forever   *step
	requests  []provider.Request
	onRequest func(i int)
}

func (f *fakeProv) Name() string  { return f.name }
func (f *fakeProv) Model() string { return f.model }

func (f *fakeProv) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	f.mu.Lock()
	req.Messages = append([]provider.Message(nil), req.Messages...)
	f.requests = append(f.requests, req)
	i := len(f.requests) - 1
	var s step
	switch {
	case len(f.script) > 0:
		s, f.script = f.script[0], f.script[1:]
	case f.forever != nil:
		s = *f.forever
	default:
		s = step{text: "(script ended)"}
	}
	hook := f.onRequest
	f.mu.Unlock()
	if hook != nil {
		hook(i)
	}
	out := make(chan provider.Event, 8+len(s.calls))
	ch := out
	if f.stamp {
		served := req.Model
		if served == "" {
			served = f.model
		}
		ch = make(chan provider.Event, 8+len(s.calls))
		go func() {
			defer close(out)
			for ev := range ch {
				ev.Provider, ev.Model = f.name, served
				out <- ev
			}
		}()
	}
	if s.provider != "" {
		in := make(chan provider.Event, 8+len(s.calls))
		go func(out chan provider.Event) {
			defer close(out)
			for ev := range in {
				ev.Provider, ev.Model = s.provider, f.model
				out <- ev
			}
		}(ch)
		ch = in
	}
	go func() {
		if s.text != "" {
			ch <- provider.Event{Type: provider.EventText, Delta: s.text}
		}
		if s.noClose {
			return
		}
		defer close(ch)
		for _, c := range s.calls {
			c := c
			ch <- provider.Event{Type: provider.EventToolCall, ToolCall: &c}
		}
		if s.stall {
			<-ctx.Done()
			ch <- provider.Event{Type: provider.EventError, Err: ctx.Err()}
			return
		}
		if s.err != nil {
			ch <- provider.Event{Type: provider.EventError, Err: s.err}
			return
		}
		u := s.usage
		finish := "stop"
		if len(s.calls) > 0 {
			finish = "tool_calls"
		}
		ch <- provider.Event{Type: provider.EventDone, FinishReason: finish, Usage: &u, Reasoning: s.reasoning}
	}()
	return out, nil
}

// reqCount returns the number of requests so far.
func (f *fakeProv) reqCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func scripted(steps ...step) *fakeProv {
	return &fakeProv{name: "primary", model: "test-model", script: steps}
}

func scriptedForever(s step) *fakeProv {
	return &fakeProv{name: "primary", model: "test-model", forever: &s}
}

func toolCalls(calls ...provider.ToolCall) step { return step{calls: calls} }
func text(s string) step                        { return step{text: s} }

func tc(id, name, args string) provider.ToolCall {
	return provider.ToolCall{ID: id, Name: name, Args: args}
}

// echoTool returns its "s" argument.
type echoTool struct{}

func (echoTool) Name() string       { return "echo" }
func (echoTool) Toolset() string    { return tools.SetTerminal }
func (echoTool) ParallelSafe() bool { return false }
func (echoTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{Name: "echo", Description: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (echoTool) Run(_ context.Context, args json.RawMessage, _ *tools.Env) (string, error) {
	var a struct{ S string }
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	return a.S, nil
}

// bigTool returns n characters.
type bigTool struct{ n int }

func (bigTool) Name() string       { return "big" }
func (bigTool) Toolset() string    { return tools.SetFile }
func (bigTool) ParallelSafe() bool { return true }
func (bigTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{Name: "big", Description: "big", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (b bigTool) Run(context.Context, json.RawMessage, *tools.Env) (string, error) {
	return strings.Repeat("y", b.n), nil
}

// recSink records the sink calls. It is safe for concurrent use.
type recSink struct {
	mu     sync.Mutex
	deltas []string
	starts []string
	ends   []string
	bounds int
}

func (r *recSink) Delta(s string) { r.mu.Lock(); r.deltas = append(r.deltas, s); r.mu.Unlock() }
func (r *recSink) ToolStart(name, preview string) {
	r.mu.Lock()
	r.starts = append(r.starts, name+":"+preview)
	r.mu.Unlock()
}
func (r *recSink) ToolEnd(name string, isError bool) {
	r.mu.Lock()
	r.ends = append(r.ends, name)
	r.mu.Unlock()
}
func (r *recSink) Boundary()     { r.mu.Lock(); r.bounds++; r.mu.Unlock() }
func (r *recSink) Notice(string) {}

// openTestDB opens a new state.db in a temporary folder.
func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// newAgent returns an Agent over fp with the tools ts, and a new session.
func newAgent(t *testing.T, fp *fakeProv, ts ...tools.Tool) (*Agent, *sessions.Session) {
	t.Helper()
	d := openTestDB(t)
	reg := tools.NewRegistry()
	for _, tool := range ts {
		reg.Register(tool)
	}
	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{"primary": {Format: "openai", Model: "test-model"}}
	a := &Agent{
		Prov:      fp,
		Providers: map[string]provider.Provider{"primary": fp},
		Tools:     reg,
		Store:     sessions.New(d),
		DB:        d,
		Cfg:       &cfg,
		SpillDir:  t.TempDir(),
	}
	sess, err := a.Store.Create(ctx, sessions.Session{Key: "cli:test", Source: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	return a, sess
}

// all returns every toolset.
func all() map[string]bool { return tools.Resolve([]string{tools.SetAll}, nil, nil) }

// lastToolContent returns the content of the last tool message of req.
func lastToolContent(req provider.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == provider.RoleTool {
			return req.Messages[i].Content
		}
	}
	return ""
}

// messages returns the stored messages of the session.
func messages(t *testing.T, a *Agent, sessID string) []sessions.StoredMessage {
	t.Helper()
	msgs, err := a.Store.Messages(ctx, sessID)
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

func TestToolRoundThenFinal(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "echo", `{"s":"x"}`)), text("done"))
	a, sess := newAgent(t, fp, echoTool{})
	sink := &recSink{}
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: sink, System: "SYS"})
	if err != nil || res.Final != "done" || res.ToolRounds != 1 {
		t.Fatal(res, err)
	}
	msgs := messages(t, a, sess.ID) // user, assistant(tool_calls), tool, assistant
	if len(msgs) != 4 || msgs[2].Msg.Role != provider.RoleTool || msgs[2].Msg.Content != "x" {
		t.Fatal(msgs)
	}
	if msgs[2].Msg.ToolCallID != "c1" || msgs[2].Msg.ToolName != "echo" || len(msgs[1].Msg.ToolCalls) != 1 {
		t.Fatal(msgs)
	}
	if msgs[1].Meta.Provider != "primary" || msgs[1].Meta.Model != "test-model" || msgs[1].Meta.FinishReason != "tool_calls" {
		t.Fatalf("%+v", msgs[1].Meta)
	}
	if len(sink.starts) != 1 || sink.starts[0] != "echo:x" || len(sink.ends) != 1 || sink.bounds != 1 {
		t.Fatalf("%+v", sink)
	}
	if strings.Join(sink.deltas, "") != "done" {
		t.Fatal(sink.deltas)
	}
	// The second request holds the history and the tool schemas.
	r := fp.requests[1]
	if r.System != "SYS" || len(r.Messages) != 3 || len(r.Tools) != 1 || r.Tools[0].Name != "echo" {
		t.Fatalf("%+v", r)
	}
}

func TestMaxTurnsNoSystemNote(t *testing.T) {
	fp := scriptedForever(step{text: "working", calls: []provider.ToolCall{tc("c", "echo", `{"s":"x"}`)}})
	a, sess := newAgent(t, fp, echoTool{})
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "loop", Toolsets: all(), Sink: &recSink{}, MaxTurns: 40})
	if err != nil || res.ToolRounds != 40 {
		t.Fatal(res, err)
	}
	for i, req := range fp.requests {
		for _, m := range req.Messages {
			if m.Role == provider.RoleSystem {
				t.Fatalf("request %d has a system message: %q", i+1, m.Content)
			}
		}
	}
}

// echoRounds returns a provider with n steps that each call echo, and then the steps of after.
func echoRounds(n int, after ...step) *fakeProv {
	steps := make([]step, 0, n+len(after))
	for i := range n {
		steps = append(steps, toolCalls(tc(fmt.Sprintf("c%d", i+1), "echo", `{"s":"x"}`)))
	}
	return scripted(append(steps, after...)...)
}

// afterLastTool returns the stored messages after the last tool message.
func afterLastTool(msgs []sessions.StoredMessage) []provider.Message {
	last := -1
	for i, m := range msgs {
		if m.Msg.Role == provider.RoleTool {
			last = i
		}
	}
	var out []provider.Message
	for _, m := range msgs[last+1:] {
		out = append(out, m.Msg)
	}
	return out
}

// hasNudge tells if a stored message holds NoteSummaryRequest.
func hasNudge(msgs []sessions.StoredMessage) bool {
	for _, m := range msgs {
		if strings.Contains(m.Msg.Content, NoteSummaryRequest) {
			return true
		}
	}
	return false
}

func TestMaxTurnsFromConfig(t *testing.T) {
	fp := scriptedForever(toolCalls(tc("c", "echo", `{"s":"x"}`)))
	a, sess := newAgent(t, fp, echoTool{})
	a.Cfg.Agent.MaxTurns = 3
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "loop", Toolsets: all(), Sink: &recSink{}})
	// 3 rounds, the summary call, and the retry.
	if err != nil || len(fp.requests) != 5 || res.ToolRounds != 3 || !res.HitLimit || !res.NoSummary {
		t.Fatal(len(fp.requests), res, err)
	}
	if res.Final != fmt.Sprintf(NoteNoSummary, 3) {
		t.Fatal(res.Final)
	}
}

func TestMaxTurnsSummary(t *testing.T) {
	fp := echoRounds(3, text("summary"))
	a, sess := newAgent(t, fp, echoTool{})
	sink := &noticeSink{}
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "loop", Toolsets: all(), Sink: sink, System: "SYS", MaxTurns: 3})
	if err != nil || res.Final != "summary" || res.ToolRounds != 3 || !res.HitLimit || res.NoSummary {
		t.Fatal(res, err)
	}
	if len(fp.requests) != 4 {
		t.Fatal(len(fp.requests))
	}
	r3, r4 := fp.requests[2], fp.requests[3]
	if r4.System != "SYS" || r4.System != r3.System || len(r4.Tools) != 1 || !reflect.DeepEqual(r4.Tools, r3.Tools) {
		t.Fatalf("the summary request keeps the system prompt and the tools: %+v", r4)
	}
	if r4.CacheKey != r3.CacheKey || r4.Effort != r3.Effort || r4.StartAt != r3.StartAt {
		t.Fatalf("%+v", r4)
	}
	// Request 4 is request 3, the answer and the tool result of round 3, and the nudge.
	n := len(r3.Messages)
	if len(r4.Messages) != n+3 || !reflect.DeepEqual(r4.Messages[:n], r3.Messages) || r4.Messages[n+1].Role != provider.RoleTool {
		t.Fatal(len(r4.Messages), n)
	}
	if last := r4.Messages[n+2]; last.Role != provider.RoleUser || last.Content != NoteSummaryRequest {
		t.Fatalf("%+v", last)
	}
	msgs := messages(t, a, sess.ID)
	last := msgs[len(msgs)-1]
	if last.Msg.Role != provider.RoleAssistant || last.Msg.Content != "summary" || len(last.Msg.ToolCalls) != 0 || last.Msg.Reasoning != nil {
		t.Fatalf("%+v", last.Msg)
	}
	if last.Meta.Provider != "primary" || last.Meta.Model != "test-model" {
		t.Fatalf("%+v", last.Meta)
	}
	if hasNudge(msgs) {
		t.Fatal("the nudge must not be saved")
	}
	if len(sink.notices) != 1 || sink.notices[0] != fmt.Sprintf(NoteLimitNotice, 3) {
		t.Fatal(sink.notices)
	}
}

func TestMaxTurnsSummaryToolCallDiscarded(t *testing.T) {
	fp := echoRounds(2, step{text: "done", calls: []provider.ToolCall{tc("c3", "echo", `{"s":"x"}`)}})
	a, sess := newAgent(t, fp, echoTool{})
	sink := &recSink{}
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "loop", Toolsets: all(), Sink: sink, MaxTurns: 2})
	if err != nil || res.Final != "done" || !res.HitLimit || len(fp.requests) != 3 {
		t.Fatal(len(fp.requests), res, err)
	}
	if len(sink.starts) != 2 {
		t.Fatal("echo runs 2 times, not 3:", sink.starts)
	}
	got := afterLastTool(messages(t, a, sess.ID))
	if len(got) != 1 || got[0].Role != provider.RoleAssistant || got[0].Content != "done" || len(got[0].ToolCalls) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestMaxTurnsSummaryEmptyRetry(t *testing.T) {
	// A text with only white space is empty.
	fp := echoRounds(2, text(" \n"), text("ok"))
	a, sess := newAgent(t, fp, echoTool{})
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "loop", Toolsets: all(), Sink: &recSink{}, MaxTurns: 2})
	if err != nil || res.Final != "ok" || !res.HitLimit || res.NoSummary || len(fp.requests) != 4 {
		t.Fatal(len(fp.requests), res, err)
	}
	if !reflect.DeepEqual(fp.requests[3], fp.requests[2]) {
		t.Fatal("the retry must send the same summary request")
	}
	got := afterLastTool(messages(t, a, sess.ID))
	if len(got) != 1 || got[0].Content != "ok" {
		t.Fatalf("no empty assistant message after the last tool result: %+v", got)
	}
}

func TestMaxTurnsSummaryEmptyTwice(t *testing.T) {
	// An answer with only tool calls is empty.
	fp := echoRounds(2, text(""), toolCalls(tc("c3", "echo", `{"s":"x"}`)))
	a, sess := newAgent(t, fp, echoTool{})
	sink := &recSink{}
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "loop", Toolsets: all(), Sink: sink, MaxTurns: 2})
	if err != nil || len(fp.requests) != 4 || !res.HitLimit || !res.NoSummary || res.ToolRounds != 2 {
		t.Fatal(len(fp.requests), res, err)
	}
	if res.Final != fmt.Sprintf(NoteNoSummary, 2) {
		t.Fatal(res.Final)
	}
	if len(sink.starts) != 2 {
		t.Fatal(sink.starts)
	}
}

func TestMaxTurnsSummaryCallFails(t *testing.T) {
	boom := errors.New("provider down")
	fp := echoRounds(2, step{text: "partial", err: boom})
	a, sess := newAgent(t, fp, echoTool{})
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "loop", Toolsets: all(), Sink: &recSink{}, MaxTurns: 2})
	if err == nil || !strings.Contains(err.Error(), "the model call to") || !errors.Is(err, boom) {
		t.Fatal(err)
	}
	// A call error gets no retry.
	if len(fp.requests) != 3 || res.Final != "partial" || !res.HitLimit || res.NoSummary {
		t.Fatal(len(fp.requests), res)
	}
	msgs := messages(t, a, sess.ID)
	if last := msgs[len(msgs)-1].Msg; last.Role != provider.RoleAssistant || last.Content != "partial" {
		t.Fatalf("%+v", last)
	}
}

func TestMaxTurnsSummaryCancel(t *testing.T) {
	fp := echoRounds(2, step{text: "half", stall: true})
	a, sess := newAgent(t, fp, echoTool{})
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	fp.onRequest = func(i int) {
		if i == 2 {
			time.AfterFunc(50*time.Millisecond, cancel)
		}
	}
	res, err := a.RunTurn(cctx, Turn{Session: sess, UserText: "loop", Toolsets: all(), Sink: &recSink{}, MaxTurns: 2})
	if !errors.Is(err, context.Canceled) || len(fp.requests) != 3 || res.Final != "half" {
		t.Fatal(len(fp.requests), res, err)
	}
	msgs := messages(t, a, sess.ID)
	if last := msgs[len(msgs)-1].Msg; last.Role != provider.RoleAssistant || last.Content != "half" {
		t.Fatalf("%+v", last)
	}
}

func TestMaxTurnsFailureStopNoSummary(t *testing.T) {
	fp := scriptedForever(toolCalls(tc("c", "nope", `{}`)))
	a, sess := newAgent(t, fp)
	sink := &noticeSink{}
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "x", Toolsets: all(), Sink: sink, MaxTurns: 3})
	if err == nil || !strings.Contains(err.Error(), "the model called an unknown tool 3 times in a row") || len(fp.requests) != 3 {
		t.Fatal(len(fp.requests), err)
	}
	if res.HitLimit || len(sink.notices) != 0 {
		t.Fatal(res, sink.notices)
	}
}

func TestMaxTurnsSummaryCronText(t *testing.T) {
	fp := echoRounds(1, text(""), text(""))
	a, sess := newAgent(t, fp, echoTool{})
	sess.Kind = "cron"
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "loop", Toolsets: all(), Sink: &recSink{}, MaxTurns: 1})
	if err != nil || !res.NoSummary || res.Final != fmt.Sprintf(NoteNoSummaryCron, 1) {
		t.Fatal(res, err)
	}
}

func TestMaxTurnsSummaryNoReasoning(t *testing.T) {
	r := &provider.Reasoning{Issuer: "responses http://d m", Output: []json.RawMessage{
		json.RawMessage(`{"type":"function_call","call_id":"c2","name":"echo","arguments":"{}"}`),
	}}
	fp := echoRounds(1, step{text: "sum", calls: []provider.ToolCall{tc("c2", "echo", `{"s":"x"}`)}, reasoning: r}, text("next"))
	a, sess := newAgent(t, fp, echoTool{})
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "loop", Toolsets: all(), Sink: &recSink{}, MaxTurns: 1}); err != nil {
		t.Fatal(err)
	}
	got := afterLastTool(messages(t, a, sess.ID))
	if len(got) != 1 || got[0].Content != "sum" || got[0].Reasoning != nil || len(got[0].ToolCalls) != 0 {
		t.Fatalf("%+v", got)
	}
	// The next turn sends no assistant message with Reasoning after the last tool result.
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "more", Toolsets: all(), Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	if len(fp.requests) != 3 {
		t.Fatal(len(fp.requests))
	}
	req := fp.requests[2]
	lastTool := -1
	for i, m := range req.Messages {
		if m.Role == provider.RoleTool {
			lastTool = i
		}
	}
	for _, m := range req.Messages[lastTool+1:] {
		if m.Role == provider.RoleAssistant && (m.Reasoning != nil || len(m.ToolCalls) != 0) {
			t.Fatalf("%+v", m)
		}
	}
}

func TestMaxTurnsNextTurnHistory(t *testing.T) {
	fp := echoRounds(1, text("summary"), text("next answer"))
	a, sess := newAgent(t, fp, echoTool{})
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: &recSink{}, MaxTurns: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "and now?", Toolsets: all(), Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	req := fp.requests[2]
	want := []provider.Role{provider.RoleUser, provider.RoleAssistant, provider.RoleTool, provider.RoleAssistant, provider.RoleUser}
	if len(req.Messages) != len(want) {
		t.Fatalf("%+v", req.Messages)
	}
	for i, m := range req.Messages {
		if m.Role != want[i] || strings.Contains(m.Content, NoteSummaryRequest) {
			t.Fatalf("message %d: %+v", i, m)
		}
	}
	if req.Messages[3].Content != "summary" || !strings.HasSuffix(req.Messages[4].Content, "and now?") {
		t.Fatalf("%+v", req.Messages)
	}
}

func TestMaxTurnsSummaryHook(t *testing.T) {
	fp := echoRounds(2, text(""), text("ok"))
	a, sess := newAgent(t, fp, echoTool{})
	var rounds []int
	a.beforeCall = func(round int) { rounds = append(rounds, round) }
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "loop", Toolsets: all(), Sink: &recSink{}, MaxTurns: 2})
	if err != nil || res.Final != "ok" || len(fp.requests) != 4 {
		t.Fatal(len(fp.requests), res, err)
	}
	if !reflect.DeepEqual(rounds, []int{1, 2, 3}) {
		t.Fatal("the hook gets 1, 2, and 3, and the retry does not call it:", rounds)
	}
}

func TestLargeToolOutputTruncated(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "big", `{}`)), text("done"))
	a, sess := newAgent(t, fp, bigTool{n: 60000})
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	got := messages(t, a, sess.ID)[2].Msg.Content
	if n := utf8.RuneCountInString(got); n > 50000+300 || !strings.Contains(got, "chars omitted. Full output: ") {
		t.Fatal(n, got[30000:30300])
	}
	if !strings.Contains(got, a.SpillDir) {
		t.Fatal("the marker must name the spill file")
	}
}

func TestToolOutputRedacted(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "echo", `{"s":"key=sk-proj-abcdefghijklmnopqrstuvwxyz0123456789"}`)), text("done"))
	a, sess := newAgent(t, fp, echoTool{})
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	if got := messages(t, a, sess.ID)[2].Msg.Content; strings.Contains(got, "abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Fatal("tool output must be redacted:", got)
	}
}

func TestModelOverridePicksNamedProvider(t *testing.T) {
	prim := scripted(text("from primary"))
	cheap := &fakeProv{name: "cheap", model: "cheap-default", script: []step{text("from cheap"), text("again")}}
	a, sess := newAgent(t, prim)
	a.Providers["cheap"] = cheap
	sess.ModelOverride = "cheap"
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "hi", Sink: &recSink{}})
	if err != nil || res.Final != "from cheap" || len(prim.requests) != 0 || len(cheap.requests) != 1 {
		t.Fatal(res, err)
	}
	if cheap.requests[0].Model != "" {
		t.Fatal("a bare name keeps the model of the entry")
	}
	sess.ModelOverride = "cheap:glm-4.7-flash"
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "hi", Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	if cheap.requests[1].Model != "glm-4.7-flash" {
		t.Fatal(cheap.requests[1].Model)
	}
	msgs := messages(t, a, sess.ID)
	if m := msgs[len(msgs)-1].Meta; m.Provider != "cheap" || m.Model != "glm-4.7-flash" {
		t.Fatalf("%+v", m)
	}
}

func TestUnknownOverrideUsesDefault(t *testing.T) {
	prim := scripted(text("from primary"))
	a, sess := newAgent(t, prim)
	sess.ModelOverride = "nope:x"
	if p := a.providerFor(sess); p != provider.Provider(prim) {
		t.Fatal(p)
	}
}

func TestHistoryCarriesOverTurns(t *testing.T) {
	fp := scripted(text("one"), text("two"))
	a, sess := newAgent(t, fp)
	a.RunTurn(ctx, Turn{Session: sess, UserText: "first", Sink: &recSink{}})
	a.RunTurn(ctx, Turn{Session: sess, UserText: "second", Sink: &recSink{}})
	r := fp.requests[1]
	if len(r.Messages) != 3 || r.Messages[0].Content != "first" || r.Messages[1].Content != "one" || r.Messages[2].Content != "second" {
		t.Fatalf("%+v", r.Messages)
	}
	v, err := a.View(ctx, sess.ID)
	if err != nil || len(v) != 4 {
		t.Fatal(v, err)
	}
}

func TestImagesGoToTheTurnRequestOnly(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "echo", `{"s":"x"}`)), text("done"), text("later"))
	a, sess := newAgent(t, fp, echoTool{})
	a.Cfg.Providers["primary"] = config.Provider{Format: "openai", Model: "test-model", Vision: true}
	img := provider.Image{Path: "/tmp/a.png", MIME: "image/png", Data: []byte("png")}
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "see", Images: []provider.Image{img}, Toolsets: all(), Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if m := fp.requests[i].Messages[0]; len(m.Images) != 1 || string(m.Images[0].Data) != "png" {
			t.Fatalf("request %d: %+v", i, m)
		}
	}
	a.RunTurn(ctx, Turn{Session: sess, UserText: "next", Sink: &recSink{}})
	if m := fp.requests[2].Messages[0]; len(m.Images) != 0 || !strings.Contains(m.Content, "[image: /tmp/a.png]") {
		t.Fatalf("a later turn sees only the marker: %+v", m)
	}
}

// The status line of a tool shows the argument of its key, not only the first string
// argument. ToolStart runs before the dispatch, so web_fetch needs no registration.
func TestToolStartPreviewUsesToolKey(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "web_fetch", `{"max_chars":500,"urls":["https://go.dev","https://x.org"]}`)), text("done"))
	a, sess := newAgent(t, fp, echoTool{})
	sink := &recSink{}
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: sink, System: "SYS"}); err != nil {
		t.Fatal(err)
	}
	if len(sink.starts) != 1 || sink.starts[0] != "web_fetch:https://go.dev +1" {
		t.Fatal(sink.starts)
	}
}

// A stream that drops after partial text ends the turn with an error, and
// the partial text is saved.
func TestStreamDropAfterPartialTextSavesText(t *testing.T) {
	drop := errors.Join(errors.New("provider primary: the stream ended before the finish event"), io.ErrUnexpectedEOF)
	fp := scripted(step{text: "partial ans", err: drop})
	a, sess := newAgent(t, fp)
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "q", Sink: &recSink{}})
	if !errors.Is(err, io.ErrUnexpectedEOF) || res.Final != "partial ans" {
		t.Fatal(res, err)
	}
	msgs := messages(t, a, sess.ID)
	if len(msgs) != 2 || msgs[1].Msg.Content != "partial ans" || msgs[1].Msg.Role != provider.RoleAssistant {
		t.Fatal(msgs)
	}
}

// A stream that drops in the middle of a tool call does not run the tool.
func TestStreamDropInToolCallRunsNoTool(t *testing.T) {
	fp := scripted(step{text: "let me check", calls: []provider.ToolCall{tc("c1", "echo", `{"s":"x"}`)}, err: io.ErrUnexpectedEOF})
	a, sess := newAgent(t, fp, echoTool{})
	sink := &recSink{}
	_, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "q", Toolsets: all(), Sink: sink})
	if !errors.Is(err, io.ErrUnexpectedEOF) || len(sink.starts) != 0 || len(fp.requests) != 1 {
		t.Fatal(err, sink.starts)
	}
	msgs := messages(t, a, sess.ID)
	if len(msgs) != 2 || len(msgs[1].Msg.ToolCalls) != 0 || msgs[1].Msg.Content != "let me check" {
		t.Fatal("a tool call with no result must not be saved:", msgs)
	}
}

// A stream that stops sending data does not hang the turn.
func TestStalledStreamEndsTurn(t *testing.T) {
	fp := scripted(step{text: "half", stall: true})
	a, sess := newAgent(t, fp)
	a.streamIdle = 100 * time.Millisecond
	start := time.Now()
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "q", Sink: &recSink{}})
	if err == nil || !strings.Contains(err.Error(), "no data") || time.Since(start) > 2*time.Second {
		t.Fatal(res, err)
	}
	if msgs := messages(t, a, sess.ID); len(msgs) != 2 || msgs[1].Msg.Content != "half" {
		t.Fatal(msgs)
	}
}

// A provider that ignores the context must not hang a cancelled turn.
func TestCancelDuringStreamSavesPartial(t *testing.T) {
	fp := scripted(step{text: "partial", noClose: true})
	a, sess := newAgent(t, fp)
	cctx, cancel := context.WithCancel(ctx)
	sink := &recSink{}
	done := make(chan struct{})
	var (
		res TurnResult
		err error
	)
	go func() {
		res, err = a.RunTurn(cctx, Turn{Session: sess, UserText: "q", Sink: sink})
		close(done)
	}()
	for fp.reqCount() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunTurn hangs after cancel")
	}
	if !errors.Is(err, context.Canceled) || res.Final != "partial" {
		t.Fatal(res, err)
	}
	if msgs := messages(t, a, sess.ID); len(msgs) != 2 || msgs[1].Msg.Content != "partial" {
		t.Fatal(msgs)
	}
}

func TestStreamStartErrorReturned(t *testing.T) {
	a, sess := newAgent(t, scripted())
	a.Prov = errProv{}
	a.Providers["primary"] = errProv{}
	_, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "q", Sink: &recSink{}})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatal(err)
	}
	if msgs := messages(t, a, sess.ID); len(msgs) != 1 || msgs[0].Msg.Role != provider.RoleUser {
		t.Fatal("the user message is saved at once:", msgs)
	}
}

// errProv fails each call before the stream starts.
type errProv struct{}

func (errProv) Name() string  { return "primary" }
func (errProv) Model() string { return "m" }
func (errProv) Stream(context.Context, provider.Request) (<-chan provider.Event, error) {
	return nil, errors.New("boom")
}

func TestUsageSummed(t *testing.T) {
	fp := scripted(
		step{calls: []provider.ToolCall{tc("c1", "echo", `{"s":"x"}`)}, usage: provider.Usage{InputTokens: 10, OutputTokens: 2, CacheReadTokens: 1, FirstTokenMS: 5, TotalMS: 50}},
		step{text: "ok", usage: provider.Usage{InputTokens: 20, OutputTokens: 3, FirstTokenMS: 7, TotalMS: 60}},
	)
	a, sess := newAgent(t, fp, echoTool{})
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "q", Toolsets: all(), Sink: &recSink{}})
	u := res.Usage
	if err != nil || u.InputTokens != 30 || u.OutputTokens != 5 || u.CacheReadTokens != 1 || u.TotalMS != 110 || u.FirstTokenMS != 5 {
		t.Fatalf("%+v %v", u, err)
	}
}

func TestCostStampedOnAssistantMessage(t *testing.T) {
	fp := scripted(step{text: "hi", usage: provider.Usage{InputTokens: 1000, OutputTokens: 100}})
	a, sess := newAgent(t, fp)
	a.Cfg.Pricing.Overrides = map[string]config.PriceOverride{"test-model": {Input: 2, Output: 10}}
	a.Prices = pricing.NewService(a.Cfg, t.TempDir(), nil, nil, nil)
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "q", Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	got := messages(t, a, sess.ID)[1].Meta.CostUSD
	if want := 1000*2e-6 + 100*10e-6; got < want-1e-12 || got > want+1e-12 {
		t.Fatal(got, want)
	}
}

// A Coding Plan entry stores the cost at the API price, like any other entry.
func TestCodingPlanStoresCost(t *testing.T) {
	fp := scripted(step{text: "hi", usage: provider.Usage{InputTokens: 1000}})
	a, sess := newAgent(t, fp)
	a.Cfg.Providers["primary"] = config.Provider{Preset: "zai-coding", BaseURL: "https://api.z.ai/api/coding/paas/v4", Model: "test-model"}
	a.Cfg.Pricing.Overrides = map[string]config.PriceOverride{"test-model": {Input: 2}}
	a.Prices = pricing.NewService(a.Cfg, t.TempDir(), nil, nil, nil)
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "q", Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	if got, want := messages(t, a, sess.ID)[1].Meta.CostUSD, 1000*2e-6; got < want-1e-12 || got > want+1e-12 {
		t.Fatal(got, want)
	}
}

func TestPinnedModelPricedByOverrideModel(t *testing.T) {
	prim := scripted()
	cheap := &fakeProv{name: "cheap", model: "cheap-default", script: []step{{text: "x", usage: provider.Usage{OutputTokens: 1000}}}}
	a, sess := newAgent(t, prim)
	a.Providers["cheap"] = cheap
	a.Cfg.Providers["cheap"] = config.Provider{Model: "cheap-default"}
	a.Cfg.Pricing.Overrides = map[string]config.PriceOverride{"glm-4.7-flash": {Output: 1}, "cheap-default": {Output: 100}}
	a.Prices = pricing.NewService(a.Cfg, t.TempDir(), nil, nil, nil)
	sess.ModelOverride = "cheap:glm-4.7-flash"
	a.RunTurn(ctx, Turn{Session: sess, UserText: "q", Sink: &recSink{}})
	if got := messages(t, a, sess.ID)[1].Meta.CostUSD; got < 0.000999 || got > 0.001001 {
		t.Fatal(got)
	}
}

// deadPrimary is a primary provider that fails before the first byte.
type deadPrimary struct{}

func (deadPrimary) Name() string  { return "primary" }
func (deadPrimary) Model() string { return "glm-5.3" }
func (deadPrimary) Stream(context.Context, provider.Request) (<-chan provider.Event, error) {
	return nil, fmt.Errorf("HTTP 503: %w", provider.ErrBeforeFirstByte)
}

// fallbackAgent returns an Agent whose primary fails, so the fallback with the steps s
// serves each call. edit changes the config before the price service reads it.
func fallbackAgent(t *testing.T, edit func(*config.Config), s ...step) (*Agent, *sessions.Session) {
	t.Helper()
	sec := &fakeProv{name: "fallback", model: "deepseek/deepseek-v4.1-flash-1017", stamp: true, script: s}
	a, sess := newAgent(t, scripted())
	a.Prov = provider.NewChain([]provider.ChainEntry{{Name: "primary", P: deadPrimary{}}, {Name: "fallback", P: sec}}, provider.NewHealth(nil), slog.New(slog.DiscardHandler))
	a.Providers = map[string]provider.Provider{"primary": a.Prov, "fallback": sec}
	a.Cfg.Providers = map[string]config.Provider{
		"primary":  {Model: "glm-5.3"},
		"fallback": {Model: "deepseek/deepseek-v4.1-flash-1017"},
	}
	a.Cfg.Pricing.Overrides = map[string]config.PriceOverride{
		"glm-5.3":                           {Input: 0.30, Output: 1.20},
		"deepseek/deepseek-v4.1-flash-1017": {Input: 0.05, Output: 0.20},
	}
	if edit != nil {
		edit(a.Cfg)
	}
	a.Prices = pricing.NewService(a.Cfg, t.TempDir(), nil, nil, nil)
	return a, sess
}

// fallbackStep is one fallback answer with 1M input and 10k output tokens. At the
// fallback price, it costs 0.05 + 0.002 = 0.052 USD.
var fallbackStep = step{text: "hi", usage: provider.Usage{InputTokens: 1_000_000, OutputTokens: 10_000}}

func TestFallbackRowNamesFallback(t *testing.T) {
	a, sess := fallbackAgent(t, nil, fallbackStep)
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "q", Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	row := messages(t, a, sess.ID)[1].Meta
	if row.Provider != "fallback" || row.Model != "deepseek/deepseek-v4.1-flash-1017" {
		t.Fatal(row)
	}
	if got := row.CostUSD; got < 0.052-1e-9 || got > 0.052+1e-9 {
		t.Fatal(got)
	}
}

// A Coding Plan primary does not change the price of a fallback call.
func TestFallbackRowPricedWhenPrimaryCodingPlan(t *testing.T) {
	plan := func(c *config.Config) {
		c.Providers["primary"] = config.Provider{Preset: "zai-coding", Model: "glm-5.3"}
	}
	a, sess := fallbackAgent(t, plan, fallbackStep)
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "q", Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	if got := messages(t, a, sess.ID)[1].Meta.CostUSD; got < 0.052-1e-9 || got > 0.052+1e-9 {
		t.Fatal(got)
	}
}

// A pin names a model of the primary. The fallback uses its own model, and the row says so.
func TestPinnedPrimaryFallbackRow(t *testing.T) {
	a, sess := fallbackAgent(t, nil, fallbackStep)
	sess.ModelOverride = "primary:glm-4.7"
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "q", Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	if row := messages(t, a, sess.ID)[1].Meta; row.Provider != "fallback" || row.Model != "deepseek/deepseek-v4.1-flash-1017" {
		t.Fatal(row)
	}
}

// A partial answer from a fallback stream that stops sending data names the fallback.
func TestStalledFallbackRowNamesFallback(t *testing.T) {
	a, sess := fallbackAgent(t, nil, step{text: "half", noClose: true})
	a.streamIdle = 50 * time.Millisecond
	_, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "q", Sink: &recSink{}})
	// The error names the provider that stalled, not the primary that failed before it.
	want := "agent: the model call to fallback failed: provider fallback: the stream sent no data for 50ms"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	msgs := messages(t, a, sess.ID)
	if len(msgs) != 2 || msgs[1].Msg.Content != "half" || msgs[1].Meta.Provider != "fallback" {
		t.Fatal(msgs)
	}
}

// pngMarkerFile writes a small PNG file and returns its path.
func pngMarkerFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "photo.png")
	// The PNG signature is enough for http.DetectContentType.
	if err := os.WriteFile(p, []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// lastUser returns the last user message of req.
func lastUser(req provider.Request) provider.Message {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == provider.RoleUser {
			return req.Messages[i]
		}
	}
	return provider.Message{}
}

func TestInlineImagesOnlyForVisionProvider(t *testing.T) {
	p := pngMarkerFile(t)
	marker := "[image: " + p + "]"
	for _, vision := range []bool{true, false} {
		fp := scripted(text("ok"))
		a, sess := newAgent(t, fp, echoTool{})
		a.Cfg.Providers["primary"] = config.Provider{Format: "openai", Model: "test-model", Vision: vision}
		// A vision provider exists in both cases, so view_image is available.
		a.Cfg.Providers["eyes"] = config.Provider{Format: "openai", Model: "gpt-4o", Vision: true}
		a.Providers["eyes"] = scripted()
		a.Cfg.Agent.VisionProvider = "eyes"
		// The channel gives the image as a marker and as an inline image, like Telegram.
		inline := provider.Image{Path: p, MIME: "image/png", Data: []byte("bytes")}
		if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "what is this?\n" + marker, Images: []provider.Image{inline}, Toolsets: all(), Sink: &recSink{}}); err != nil {
			t.Fatal(err)
		}
		u := lastUser(fp.requests[0])
		if vision && (len(u.Images) != 1 || string(u.Images[0].Data) != "bytes") {
			t.Fatalf("vision: one inline image: %+v", u)
		}
		if !vision && len(u.Images) != 0 {
			t.Fatalf("a text-only model never receives image parts: %+v", u)
		}
		if strings.Count(u.Content, marker) != 1 {
			t.Fatalf("vision=%v: the marker text stays one time: %q", vision, u.Content)
		}
		stored := messages(t, a, sess.ID)[0].Msg.Content
		if strings.Count(stored, marker) != 1 || strings.Contains(stored, "bytes") {
			t.Fatalf("the transcript keeps only the path: %q", stored)
		}
	}
}

func TestVisionMarkerOnlyFromInbox(t *testing.T) {
	inbox := t.TempDir()
	in := filepath.Join(inbox, "photo.png")
	if err := os.WriteFile(in, []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := pngMarkerFile(t) // in another temporary folder
	link := filepath.Join(inbox, "link.png")
	if err := os.Symlink(out, link); err != nil {
		t.Fatal(err)
	}
	dotdot := filepath.Join(inbox, "..", filepath.Base(filepath.Dir(out)), "photo.png")
	fp := scripted(text("ok"))
	a, sess := newAgent(t, fp, echoTool{})
	a.Cfg.Providers["primary"] = config.Provider{Format: "openai", Model: "test-model", Vision: true}
	a.MediaInbox = inbox
	msg := "see [image: " + in + "] [image: " + out + "] [image: " + link + "] [image: " + dotdot + "]"
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: msg, Toolsets: all(), Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	u := lastUser(fp.requests[0])
	if len(u.Images) != 1 || u.Images[0].Path != in {
		t.Fatalf("only the inbox file loads: %+v", u.Images)
	}
	if u.Content != msg {
		t.Fatalf("the marker text stays: %q", u.Content)
	}
}

func TestVisionMarkerNoInbox(t *testing.T) {
	p := pngMarkerFile(t)
	fp := scripted(text("ok"))
	a, sess := newAgent(t, fp, echoTool{})
	a.Cfg.Providers["primary"] = config.Provider{Format: "openai", Model: "test-model", Vision: true}
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "see [image: " + p + "]", Toolsets: all(), Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	if u := lastUser(fp.requests[0]); len(u.Images) != 0 {
		t.Fatalf("an empty MediaInbox loads no marker: %+v", u.Images)
	}
}

func TestVisionLoadsMarkerFiles(t *testing.T) {
	p := pngMarkerFile(t)
	fp := scripted(toolCalls(tc("c1", "echo", `{"s":"x"}`)), text("ok"))
	a, sess := newAgent(t, fp, echoTool{})
	a.MediaInbox = filepath.Dir(p)
	a.Cfg.Providers["primary"] = config.Provider{Format: "openai", Model: "test-model", Vision: true}
	text := "compare [image: " + p + "] and [image: /no/such/file.png]"
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: text, Toolsets: all(), Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		u := lastUser(fp.requests[i])
		if len(u.Images) != 1 || u.Images[0].Path != p || u.Images[0].MIME != "image/png" {
			t.Fatalf("request %d: the marker file goes inline, and a missing file is skipped: %+v", i, u)
		}
		if u.Content != text {
			t.Fatalf("the marker text stays: %q", u.Content)
		}
	}
}

func TestVisionUsesTheOverrideEntry(t *testing.T) {
	p := pngMarkerFile(t)
	prim := scripted()
	eyes := &fakeProv{name: "eyes", model: "gpt-4o", script: []step{text("ok")}}
	a, sess := newAgent(t, prim, echoTool{})
	a.MediaInbox = filepath.Dir(p)
	a.Cfg.Providers["eyes"] = config.Provider{Format: "openai", Model: "gpt-4o", Vision: true}
	a.Providers["eyes"] = eyes
	sess.ModelOverride = "eyes:gpt-4o-mini"
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "[image: " + p + "]", Toolsets: all(), Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	if u := lastUser(eyes.requests[0]); len(u.Images) != 1 {
		t.Fatalf("the provider entry of the session override decides: %+v", u)
	}
}

func TestNoVisionModelMarkerText(t *testing.T) {
	p := pngMarkerFile(t)
	fp := scripted(text("ok"))
	a, sess := newAgent(t, fp, echoTool{})
	img := provider.Image{Path: p, MIME: "image/png", Data: []byte("bytes")}
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "see [image: " + p + "]", Images: []provider.Image{img}, Toolsets: all(), Sink: &recSink{}}); err != nil {
		t.Fatal(err)
	}
	u := lastUser(fp.requests[0])
	if len(u.Images) != 0 || u.Content != "see [image received; no vision model is configured]" {
		t.Fatalf("%+v", u)
	}
	if stored := messages(t, a, sess.ID)[0].Msg.Content; stored != u.Content {
		t.Fatalf("the transcript matches the model view: %q", stored)
	}
}

func TestDefaultModel(t *testing.T) {
	a, _ := newAgent(t, &fakeProv{name: "primary", model: "anthropic/claude-sonnet-5"})
	pc := a.Cfg.Providers["primary"]
	pc.ContextWindow = 200000
	a.Cfg.Providers["primary"] = pc
	if m, w := a.DefaultModel(); m != "anthropic/claude-sonnet-5" || w != 200000 {
		t.Fatal(m, w)
	}
	// A provider with no model name gives the model of its config entry.
	a.Prov = &fakeProv{name: "primary"}
	if m, w := a.DefaultModel(); m != "test-model" || w != 200000 {
		t.Fatal(m, w)
	}
}

// statusProv is a fakeProv that reports a fallback state.
type statusProv struct {
	*fakeProv
	status provider.FallbackStatus
}

func (s statusProv) FallbackStatus() provider.FallbackStatus { return s.status }

// activeStatus is the state of a fallback wrapper after a primary failure.
var activeStatus = provider.FallbackStatus{
	Active: true, PrimaryModel: "glm-5.3", FallbackName: "fallback",
	FallbackModel: "deepseek/deepseek-v4.1-flash-1017", Cause: "HTTP 503",
}

func TestCurrentModelPrimary(t *testing.T) {
	a, _ := newAgent(t, scripted())
	a.Prov = statusProv{fakeProv: &fakeProv{name: "primary", model: "glm-5.3"}}
	a.Cfg.Providers["primary"] = config.Provider{Model: "glm-5.3", ContextWindow: 200000}
	got := a.CurrentModel(nil)
	if got.Model != "glm-5.3" || got.ContextWindow != 200000 || got.Fallback.Active {
		t.Fatal(got)
	}
	if m, w := a.DefaultModel(); got.Model != m || got.ContextWindow != w {
		t.Fatal(got, m, w)
	}
	// A provider with no fallback state gives the default model.
	a.Prov = &fakeProv{name: "primary", model: "glm-5.3"}
	if got := a.CurrentModel(nil); got.Model != "glm-5.3" || got.ContextWindow != 200000 || got.Fallback.Active {
		t.Fatal(got)
	}
	a.Prov = nil
	if got := a.CurrentModel(nil); got != (ModelState{}) {
		t.Fatal(got)
	}
}

func TestCurrentModelFallback(t *testing.T) {
	a, _ := newAgent(t, scripted())
	a.Prov = statusProv{fakeProv: &fakeProv{name: "primary", model: "glm-5.3"}, status: activeStatus}
	a.Providers["fallback"] = &fakeProv{name: "fallback", model: "deepseek/deepseek-v4.1-flash-1017"}
	a.Cfg.Providers["primary"] = config.Provider{Model: "glm-5.3", ContextWindow: 200000}
	a.Cfg.Providers["fallback"] = config.Provider{Model: "deepseek/deepseek-v4.1-flash-1017", ContextWindow: 64000}
	got := a.CurrentModel(nil)
	if got.Model != "deepseek/deepseek-v4.1-flash-1017" || got.ContextWindow != 64000 ||
		!got.Fallback.Active || got.Fallback.Cause != "HTTP 503" {
		t.Fatal(got)
	}
}

func TestCurrentModelFallbackNoEntry(t *testing.T) {
	a, _ := newAgent(t, scripted())
	a.Prov = statusProv{fakeProv: &fakeProv{name: "primary", model: "glm-5.3"}, status: activeStatus}
	a.Cfg.Providers["fallback"] = config.Provider{Model: "deepseek/deepseek-v4.1-flash-1017", ContextWindow: 64000}
	if got := a.CurrentModel(nil); got.Model != "deepseek/deepseek-v4.1-flash-1017" || got.ContextWindow != 64000 {
		t.Fatal(got)
	}
	delete(a.Cfg.Providers, "fallback")
	if got := a.CurrentModel(nil); got.Model != "deepseek/deepseek-v4.1-flash-1017" || got.ContextWindow != pricing.DefaultContextWindow {
		t.Fatal(got)
	}
}

func TestProviderForPinnedChain(t *testing.T) {
	a, sess := newAgent(t, scripted(text("ok")))
	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{
		"primary":  {Format: "openai", BaseURL: "http://p", Model: "m1"},
		"deepseek": {Format: "openai", BaseURL: "http://d", Model: "m2"},
	}
	cfg.Agent.Fallback = []string{"deepseek"}
	set, err := provider.FromConfig(&cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.Cfg, a.Chains = &cfg, set
	sess.ModelOverride = "deepseek:deepseek-reasoner"
	p := a.providerFor(sess)
	if p.Name() != "deepseek" || p.Model() != "deepseek-reasoner" {
		t.Fatal(p.Name(), p.Model())
	}
	if ms := a.CurrentModel(sess); ms.Model != "deepseek-reasoner" || ms.Fallback.FirstName != "deepseek" {
		t.Fatalf("%+v", ms)
	}
	// No pin and an unknown pin use the default chain.
	sess.ModelOverride = "nope"
	if p := a.providerFor(sess); p.Name() != "primary" || p.Model() != "m1" {
		t.Fatal(p.Name(), p.Model())
	}
	if ms := a.CurrentModel(nil); ms.Model != "m1" || ms.Fallback.FirstName != "primary" {
		t.Fatalf("%+v", ms)
	}
}

func TestProviderForSkipsUnusableOverride(t *testing.T) {
	a, sess := newAgent(t, scripted(text("ok")))
	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{
		"primary": {Format: "openai", BaseURL: "http://p", Model: "m1"},
		"zai": {Format: "openai", BaseURL: "http://z", Model: "glm-4.7",
			Unusable: "config: providers.zai needs an API key. Set ZAI_API_KEY in ~/.tether/.env, or set providers.zai.api_key"},
	}
	cfg.Agent.Fallback = []string{"zai"}
	set, err := provider.FromConfig(&cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.Cfg, a.Chains = &cfg, set
	// The override stays, but the chain skips zai, so no call goes to it.
	sess.ModelOverride = "zai:glm-4.7"
	if p := a.providerFor(sess); p.Name() != "primary" || p.Model() != "m1" {
		t.Fatal(p.Name(), p.Model())
	}
	ms := a.CurrentModel(sess)
	if ms.Name != "primary" || ms.Model != "m1" || ms.Fallback.FirstName != "primary" || ms.Fallback.Active {
		t.Fatalf("%+v", ms)
	}
}

// failProv fails each call before the first byte with HTTP 503.
type failProv struct{ name, model string }

func (f failProv) Name() string  { return f.name }
func (f failProv) Model() string { return f.model }
func (f failProv) Stream(context.Context, provider.Request) (<-chan provider.Event, error) {
	return nil, fmt.Errorf("provider %s: %w: %w", f.name, provider.ErrBeforeFirstByte, &provider.HTTPStatusError{Code: 503})
}

// failEntry records an HTTP 503 of the entry name and model in the Health of set.
func failEntry(t *testing.T, set *provider.Set, name, model string) {
	t.Helper()
	c := provider.NewChain([]provider.ChainEntry{{Name: name, P: failProv{name, model}}}, set.Health, slog.New(slog.DiscardHandler))
	if _, err := c.Stream(ctx, provider.Request{}); err == nil {
		t.Fatal("the call must fail")
	}
}

func TestCurrentModelOfEachChain(t *testing.T) {
	a, s1 := newAgent(t, scripted())
	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{
		"primary":    {Format: "openai", BaseURL: "http://p", Model: "glm-4.7", ContextWindow: 200000},
		"deepseek":   {Format: "openai", BaseURL: "http://d", Model: "deepseek-chat", ContextWindow: 128000},
		"openrouter": {Format: "openai", BaseURL: "http://o", Model: "z-ai/glm-4.7", ContextWindow: 64000},
	}
	cfg.Agent.Fallback = []string{"openrouter"}
	set, err := provider.FromConfig(&cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.Cfg, a.Chains = &cfg, set
	failEntry(t, set, "primary", "glm-4.7")
	ms := a.CurrentModel(s1)
	if ms.Name != "openrouter" || ms.Model != "z-ai/glm-4.7" || ms.ContextWindow != 64000 || !ms.Fallback.Active ||
		ms.Fallback.FirstName != "primary" || ms.Fallback.Cause != "HTTP 503" {
		t.Fatalf("%+v", ms)
	}
	// A session pinned to deepseek has its own status.
	s2 := &sessions.Session{ID: "s2", ModelOverride: "deepseek"}
	if ms := a.CurrentModel(s2); ms.Name != "deepseek" || ms.Model != "deepseek-chat" || ms.ContextWindow != 128000 || ms.Fallback.Active {
		t.Fatalf("%+v", ms)
	}
	failEntry(t, set, "deepseek", "deepseek-chat")
	// The chain of s2 is [deepseek, primary, openrouter], and primary also waits.
	if ms := a.CurrentModel(s2); ms.Name != "openrouter" || !ms.Fallback.Active || ms.Fallback.FirstName != "deepseek" {
		t.Fatalf("%+v", ms)
	}
	// A pinned model of primary has its own health key.
	s3 := &sessions.Session{ID: "s3", ModelOverride: "primary:glm-5.3"}
	if ms := a.CurrentModel(s3); ms.Name != "primary" || ms.Model != "glm-5.3" || ms.Fallback.Active {
		t.Fatalf("%+v", ms)
	}
}

func TestTurnRequestFields(t *testing.T) {
	r := &provider.Reasoning{Issuer: "openai http://d m", Text: "plan"}
	fp := scripted(
		step{calls: []provider.ToolCall{tc("c1", "echo", `{}`)}, reasoning: r, provider: "openrouter"},
		text("done"),
	)
	a, sess := newAgent(t, fp, echoTool{})
	sess.Effort = "high"
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "hi", Toolsets: all()}); err != nil {
		t.Fatal(err)
	}
	if len(fp.requests) != 2 {
		t.Fatal(len(fp.requests))
	}
	for _, req := range fp.requests {
		if req.CacheKey != sess.ID || req.Effort != "high" {
			t.Fatalf("%q %q", req.CacheKey, req.Effort)
		}
	}
	if fp.requests[0].StartAt != "" {
		t.Fatal(fp.requests[0].StartAt)
	}
	// The turn stays on the entry that served.
	if fp.requests[1].StartAt != "openrouter" {
		t.Fatal(fp.requests[1].StartAt)
	}
	ms := messages(t, a, sess.ID)
	var saved *provider.Reasoning
	for _, m := range ms {
		if m.Msg.Role == provider.RoleAssistant && len(m.Msg.ToolCalls) == 1 {
			saved = m.Msg.Reasoning
		}
	}
	if !reflect.DeepEqual(saved, r) {
		t.Fatalf("%+v", saved)
	}
}

func TestNextTurnStartsAtFirstEntry(t *testing.T) {
	fp := scripted(step{text: "one", provider: "openrouter"}, text("two"))
	a, sess := newAgent(t, fp)
	_, _ = a.RunTurn(ctx, Turn{Session: sess, UserText: "a", Toolsets: all()})
	_, _ = a.RunTurn(ctx, Turn{Session: sess, UserText: "b", Toolsets: all()})
	if fp.requests[1].StartAt != "" {
		t.Fatal(fp.requests[1].StartAt)
	}
}

func TestPartialAnswerKeepsNoReasoning(t *testing.T) {
	r := &provider.Reasoning{Issuer: "openai http://d m", Text: "plan"}
	fp := scripted(step{text: "part", err: errors.New("cut"), reasoning: r})
	a, sess := newAgent(t, fp)
	if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "a", Toolsets: all()}); err == nil {
		t.Fatal("the turn must fail")
	}
	ms := messages(t, a, sess.ID)
	if last := ms[len(ms)-1].Msg; last.Content != "part" || last.Reasoning != nil {
		t.Fatalf("%+v", last)
	}
}

// effortProv rejects a call that has an effort with an HTTP 400 about the effort.
type effortProv struct{ name, model string }

func (e effortProv) Name() string  { return e.name }
func (e effortProv) Model() string { return e.model }
func (e effortProv) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	if req.Effort != "" {
		return nil, &provider.HTTPStatusError{Code: 400, Body: `{"error":"reasoning_effort is not supported"}`}
	}
	ch := make(chan provider.Event)
	close(ch)
	return ch, nil
}

func TestEffortState(t *testing.T) {
	a, sess := newAgent(t, scripted())
	// With no Chains, the state comes from the session and the config only.
	a.Cfg.Providers["primary"] = config.Provider{Format: "openai", Model: "test-model", Effort: "medium"}
	if got := a.EffortState(sess); got != (EffortState{Level: "medium", Source: "from config", Entry: "primary", Model: "test-model"}) {
		t.Fatalf("%+v", got)
	}
	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{
		"primary":  {Format: "openai", BaseURL: "http://p", Model: "glm-5.3", Effort: "high"},
		"deepseek": {Format: "openai", BaseURL: "http://d", Model: "deepseek-chat"},
	}
	set, err := provider.FromConfig(&cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.Cfg, a.Chains = &cfg, set
	if got := a.EffortState(nil); got != (EffortState{Level: "high", Source: "from config", Entry: "primary", Model: "glm-5.3"}) {
		t.Fatalf("%+v", got)
	}
	sess.ModelOverride = "deepseek:deepseek-reasoner"
	if got := a.EffortState(sess); got != (EffortState{Source: "provider default", Entry: "deepseek", Model: "deepseek-reasoner"}) {
		t.Fatalf("%+v", got)
	}
	sess.Effort = "none"
	if got := a.EffortState(sess); got != (EffortState{Level: "none", Source: "this session", Entry: "deepseek", Model: "deepseek-reasoner"}) {
		t.Fatalf("%+v", got)
	}
	// The entry rejects the effort, so the provider default applies.
	c := provider.NewChain([]provider.ChainEntry{{Name: "deepseek", P: effortProv{"deepseek", "deepseek-reasoner"}}}, set.Health, slog.New(slog.DiscardHandler))
	if _, err := c.Stream(ctx, provider.Request{Effort: "none"}); err != nil {
		t.Fatal(err)
	}
	if got := a.EffortState(sess); !got.Rejected || got.Level != "none" {
		t.Fatalf("%+v", got)
	}
	sess.Effort = ""
	if got := a.EffortState(sess); got.Rejected {
		t.Fatalf("an empty level is never rejected: %+v", got)
	}
}

// The summary row carries the usage of the empty attempt and of the good attempt, so the
// usage report and the cost count both calls.
func TestMaxTurnsSummaryUsageSum(t *testing.T) {
	u1 := provider.Usage{InputTokens: 1000, OutputTokens: 10}
	u2 := provider.Usage{InputTokens: 2000, OutputTokens: 100}
	fp := scripted(toolCalls(tc("c1", "echo", `{"s":"x"}`)), step{usage: u1}, step{text: "ok", usage: u2})
	a, sess := newAgent(t, fp, echoTool{})
	a.Cfg.Pricing.Overrides = map[string]config.PriceOverride{"test-model": {Input: 2, Output: 10}}
	a.Prices = pricing.NewService(a.Cfg, t.TempDir(), nil, nil, nil)
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: &recSink{}, MaxTurns: 1})
	if err != nil || res.Final != "ok" || !res.HitLimit || res.NoSummary {
		t.Fatalf("%+v %v", res, err)
	}
	if res.Usage.InputTokens != 3000 || res.Usage.OutputTokens != 110 {
		t.Fatalf("each attempt counts one time in the turn usage: %+v", res.Usage)
	}
	if len(fp.requests) != 3 || fp.requests[2].Messages[len(fp.requests[2].Messages)-1].Content != NoteSummaryRequest {
		t.Fatal("the retry sends the summary request again:", len(fp.requests))
	}
	msgs := messages(t, a, sess.ID) // user, assistant(tool call), tool, summary
	if len(msgs) != 4 || msgs[2].Msg.Role != provider.RoleTool {
		t.Fatal("one stored row after the tool result:", msgs)
	}
	row := msgs[3]
	if row.Msg.Role != provider.RoleAssistant || row.Msg.Content != "ok" {
		t.Fatalf("%+v", row.Msg)
	}
	if u := row.Meta.Usage; u.InputTokens != 3000 || u.OutputTokens != 110 {
		t.Fatalf("the summary row has U1 + U2: %+v", u)
	}
	if got, want := row.Meta.CostUSD, 3000*2e-6+110*10e-6; got < want-1e-12 || got > want+1e-12 {
		t.Fatal("the cost is the cost of the sum:", got, want)
	}
}

// After two empty summary attempts, one empty assistant row carries the usage of both.
func TestMaxTurnsSummaryUsageEmptyTwice(t *testing.T) {
	u1 := provider.Usage{InputTokens: 500, OutputTokens: 3, CacheReadTokens: 400}
	u2 := provider.Usage{InputTokens: 600, OutputTokens: 4, CacheReadTokens: 450}
	fp := scripted(toolCalls(tc("c1", "echo", `{"s":"x"}`)), step{usage: u1}, step{usage: u2})
	a, sess := newAgent(t, fp, echoTool{})
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: &recSink{}, MaxTurns: 1})
	if err != nil || res.Final != fmt.Sprintf(NoteNoSummary, 1) || !res.HitLimit || !res.NoSummary {
		t.Fatalf("%+v %v", res, err)
	}
	if len(fp.requests) != 3 {
		t.Fatal(len(fp.requests))
	}
	msgs := messages(t, a, sess.ID) // user, assistant(tool call), tool, empty assistant
	if len(msgs) != 4 || msgs[2].Msg.Role != provider.RoleTool {
		t.Fatal("one stored row after the tool result:", msgs)
	}
	row := msgs[3]
	if row.Msg.Role != provider.RoleAssistant || row.Msg.Content != "" || len(row.Msg.ToolCalls) != 0 || row.Msg.Reasoning != nil {
		t.Fatalf("one empty assistant row: %+v", row.Msg)
	}
	if u := row.Meta.Usage; u.InputTokens != 1100 || u.OutputTokens != 7 || u.CacheReadTokens != 850 {
		t.Fatalf("the empty row has U1 + U2: %+v", u)
	}
}

func TestConfigOfZeroAgentHasNoRace(t *testing.T) {
	a := &Agent{}
	var wg sync.WaitGroup
	got := make([]*config.Config, 2)
	for i := range got {
		wg.Go(func() { got[i] = a.config() })
	}
	wg.Wait()
	if a.Cfg != nil {
		t.Fatal("config() wrote a.Cfg")
	}
	want := config.Default()
	for _, c := range got {
		if c == nil || c.Agent.MaxTurns != want.Agent.MaxTurns || c.Delegation.MaxConcurrentChildren != want.Delegation.MaxConcurrentChildren {
			t.Fatalf("%+v", c)
		}
	}
}

func TestStreamIdleLimit(t *testing.T) {
	a := &Agent{}
	if got := a.streamIdleLimit(); got != 300*time.Second {
		t.Fatal("no config gives 300 s:", got)
	}
	cfg := config.Default()
	cfg.Agent.StreamIdleTimeoutS = 7
	a.Cfg = &cfg
	if got := a.streamIdleLimit(); got != 7*time.Second {
		t.Fatal(got)
	}
	cfg.Agent.StreamIdleTimeoutS = 0
	if got := a.streamIdleLimit(); got != 300*time.Second {
		t.Fatal("0 keeps 300 s:", got)
	}
	a.streamIdle = 20 * time.Millisecond
	cfg.Agent.StreamIdleTimeoutS = 7
	if got := a.streamIdleLimit(); got != 20*time.Millisecond {
		t.Fatal("the test hook wins over the key:", got)
	}
}

func TestStalledStreamUsesConfig(t *testing.T) {
	fp := scripted(step{text: "half", stall: true})
	a, sess := newAgent(t, fp)
	a.Cfg.Agent.StreamIdleTimeoutS = 1
	start := time.Now()
	_, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "q", Sink: &recSink{}})
	if err == nil || !strings.Contains(err.Error(), "the stream sent no data for 1s") || time.Since(start) > 3*time.Second {
		t.Fatal(err, time.Since(start))
	}
}

func TestSameFailureLimitFromConfig(t *testing.T) {
	fp := scriptedForever(toolCalls(tc("c", "nope", `{}`)))
	a, sess := newAgent(t, fp)
	a.Cfg.Agent.MaxSameToolFailures = 2
	_, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "x", Toolsets: all(), Sink: &noticeSink{}, MaxTurns: 5})
	if err == nil || !strings.Contains(err.Error(), "the model called an unknown tool 2 times in a row") || len(fp.requests) != 2 {
		t.Fatal(len(fp.requests), err)
	}
}

func TestDefaultContextWindowFromConfig(t *testing.T) {
	a, _ := newAgent(t, scripted())
	if got := a.modelWindow("primary", ""); got != pricing.DefaultContextWindow {
		t.Fatal(got)
	}
	a.Cfg.Provider.DefaultContextWindow = 200000
	if got := a.modelWindow("primary", ""); got != 200000 {
		t.Fatal(got)
	}
	a.Cfg.Provider.DefaultContextWindow = 0
	if got := a.modelWindow("primary", ""); got != pricing.DefaultContextWindow {
		t.Fatal("0 keeps the const:", got)
	}
	a.Cfg.Provider.DefaultContextWindow = 200000
	a.Cfg.Providers["primary"] = config.Provider{Format: "openai", Model: "test-model", ContextWindow: 64000}
	if got := a.modelWindow("primary", ""); got != 64000 {
		t.Fatal("the entry wins:", got)
	}
}

func TestMarkerImageFollowsVisionMaxBytes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "big.png")
	data := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), make([]byte, 2048)...)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for limit, want := range map[int]int{1024: 0, 4096: 1} {
		fp := scripted(text("ok"))
		a, sess := newAgent(t, fp, echoTool{})
		a.Cfg.Providers["primary"] = config.Provider{Format: "openai", Model: "test-model", Vision: true}
		a.MediaInbox = filepath.Dir(p)
		a.Cfg.Media.VisionMaxBytes = limit
		if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "see [image: " + p + "]", Toolsets: all(), Sink: &recSink{}}); err != nil {
			t.Fatal(err)
		}
		if got := len(lastUser(fp.requests[0]).Images); got != want {
			t.Fatalf("limit %d: %d inline images, want %d", limit, got, want)
		}
	}
}

func TestToolPreviewFollowsConfig(t *testing.T) {
	for limit, want := range map[int]string{
		10: "echo:abcdefg...",
		40: "echo:abcdefghijklmnopqrstuvwxyz",
	} {
		fp := scripted(toolCalls(tc("c1", "echo", `{"s":"abcdefghijklmnopqrstuvwxyz"}`)), text("ok"))
		a, sess := newAgent(t, fp, echoTool{})
		a.Cfg.Agent.ToolPreviewChars = limit
		sink := &recSink{}
		if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "go", Toolsets: all(), Sink: sink}); err != nil {
			t.Fatal(err)
		}
		if len(sink.starts) != 1 || sink.starts[0] != want {
			t.Fatalf("limit %d: %q", limit, sink.starts)
		}
	}
}
