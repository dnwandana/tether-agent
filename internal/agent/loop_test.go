package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
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
}

// fakeProv is a scripted provider.Provider. It records each request.
type fakeProv struct {
	name, model string

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
	ch := make(chan provider.Event, 8+len(s.calls))
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
		ch <- provider.Event{Type: provider.EventDone, FinishReason: finish, Usage: &u}
	}()
	return ch, nil
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

// lastSystemNote returns the content of the last system message of req.
func lastSystemNote(req provider.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == provider.RoleSystem {
			return req.Messages[i].Content
		}
	}
	return ""
}

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

func TestMaxTurnsNudgeAt39(t *testing.T) {
	fp := scriptedForever(step{text: "working", calls: []provider.ToolCall{tc("c", "echo", `{"s":"x"}`)}})
	a, sess := newAgent(t, fp, echoTool{})
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "loop", Toolsets: all(), Sink: &recSink{}, MaxTurns: 40})
	if err != nil {
		t.Fatal(err)
	}
	req39 := fp.requests[38]
	if !strings.Contains(lastSystemNote(req39), "Stop calling tools and give your final answer now.") {
		t.Fatal("nudge at round 39")
	}
	if lastSystemNote(fp.requests[37]) != "" || lastSystemNote(fp.requests[39]) != "" {
		t.Fatal("the nudge goes to request 39 only")
	}
	if len(fp.requests) > 41 {
		t.Fatal("hard stop after 40 rounds")
	}
	if res.ToolRounds != 40 || res.Final != "working\n\n[stopped: max tool rounds reached]" {
		t.Fatalf("%+v", res)
	}
	for _, m := range messages(t, a, sess.ID) {
		if m.Msg.Role == provider.RoleSystem {
			t.Fatal("the nudge must not be saved")
		}
	}
}

func TestMaxTurnsFromConfig(t *testing.T) {
	fp := scriptedForever(toolCalls(tc("c", "echo", `{"s":"x"}`)))
	a, sess := newAgent(t, fp, echoTool{})
	a.Cfg.Agent.MaxTurns = 3
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "loop", Toolsets: all(), Sink: &recSink{}})
	if err != nil || len(fp.requests) != 3 || res.ToolRounds != 3 || res.Final != "[stopped: max tool rounds reached]" {
		t.Fatal(len(fp.requests), res, err)
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

func TestToolArgsPreview(t *testing.T) {
	cases := map[string]string{
		`{"n":3,"command":"ls -la","x":"y"}`:         "ls -la",
		`{"path":"` + strings.Repeat("a", 60) + `"}`: strings.Repeat("a", 37) + "...",
		`{"a":"line1\nline2"}`:                       "line1 line2",
		`{"n":1}`:                                    "",
		`not json`:                                   "",
	}
	for in, want := range cases {
		if got := argPreview(in); got != want {
			t.Errorf("argPreview(%q) = %q, want %q", in, got, want)
		}
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
	old := streamIdleTimeout
	streamIdleTimeout = 100 * time.Millisecond
	t.Cleanup(func() { streamIdleTimeout = old })
	fp := scripted(step{text: "half", stall: true})
	a, sess := newAgent(t, fp)
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

func TestFlatRateStoresNoCost(t *testing.T) {
	fp := scripted(step{text: "hi", usage: provider.Usage{InputTokens: 1000}})
	a, sess := newAgent(t, fp)
	a.Cfg.Providers["primary"] = config.Provider{Model: "test-model", FlatRate: true}
	a.Cfg.Pricing.Overrides = map[string]config.PriceOverride{"test-model": {Input: 2}}
	a.Prices = pricing.NewService(a.Cfg, t.TempDir(), nil, nil, nil)
	a.RunTurn(ctx, Turn{Session: sess, UserText: "q", Sink: &recSink{}})
	if got := messages(t, a, sess.ID)[1].Meta.CostUSD; got != 0 {
		t.Fatal(got)
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

func TestVisionLoadsMarkerFiles(t *testing.T) {
	p := pngMarkerFile(t)
	fp := scripted(toolCalls(tc("c1", "echo", `{"s":"x"}`)), text("ok"))
	a, sess := newAgent(t, fp, echoTool{})
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
