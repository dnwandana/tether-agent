package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// fixtureText is the answer text in openai_text_stream.sse.
const fixtureText = "Hello, world."

// openTestDB opens a new state.db in a temporary folder. The test closes it at the end.
func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// fixtureSSE starts a fake provider that answers each request with the SSE file at path.
func fixtureSSE(t *testing.T, path string) *httptest.Server {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCLIStreamsAndPersists(t *testing.T) {
	srv := fixtureSSE(t, "../provider/testdata/openai_text_stream.sse")
	st := sessions.New(openTestDB(t))
	var out bytes.Buffer
	c := &CLI{In: strings.NewReader("hello\n/exit\nnot sent\n"), Out: &out, Deps: providerDeps(t, st, provider.NewOpenAI("p", srv.URL, "k", "m", srv.Client())),
		Key: "cli:test", Interrupts: make(chan os.Signal)}
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), fixtureText+"\n") {
		t.Fatalf("%q", out.String())
	}
	sess, err := st.Active(context.Background(), "cli:test")
	if err != nil || sess == nil || sess.Source != "cli" {
		t.Fatal(sess, err)
	}
	msgs, _ := st.Messages(context.Background(), sess.ID)
	if len(msgs) != 2 || msgs[0].Msg.Content != "hello" || msgs[1].Msg.Content != fixtureText {
		t.Fatalf("%+v", msgs)
	}
	m := msgs[1].Meta
	if m.Model != "m" || m.Provider != "p" || m.Usage.InputTokens != 120 || m.Usage.OutputTokens != 5 || m.FinishReason != "stop" {
		t.Fatalf("%+v", m)
	}
}

func TestCLISendsHistory(t *testing.T) {
	b, err := os.ReadFile("../provider/testdata/openai_text_stream.sse")
	if err != nil {
		t.Fatal(err)
	}
	var last atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body bytes.Buffer
		_, _ = body.ReadFrom(r.Body)
		last.Store(body.String())
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	st := sessions.New(openTestDB(t))
	var out bytes.Buffer
	c := &CLI{In: strings.NewReader("first\n\n  second  \n"), Out: &out, Deps: providerDeps(t, st, provider.NewOpenAI("p", srv.URL, "k", "m", srv.Client())),
		Key: "cli:test", Interrupts: make(chan os.Signal)}
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	req, _ := last.Load().(string)
	if !strings.Contains(req, `"first"`) || !strings.Contains(req, fixtureText) || !strings.Contains(req, `"second"`) {
		t.Fatal(req)
	}
	sess, _ := st.Active(context.Background(), "cli:test")
	if msgs, _ := st.Messages(context.Background(), sess.ID); len(msgs) != 4 {
		t.Fatal(len(msgs))
	}
}

func TestCLIProviderErrorPrintsAndContinues(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, `{"error":{"message":"bad model"}}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	st := sessions.New(openTestDB(t))
	var out bytes.Buffer
	c := &CLI{In: strings.NewReader("one\ntwo\n"), Out: &out, Deps: providerDeps(t, st, provider.NewOpenAI("p", srv.URL, "k", "m", srv.Client())),
		Key: "cli:test", Interrupts: make(chan os.Signal)}
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "error:") != 2 || calls.Load() != 2 {
		t.Fatalf("%d calls: %q", calls.Load(), out.String())
	}
}

// providerDeps returns the deps of an agent with no tools over the provider p.
func providerDeps(t *testing.T, st *sessions.Store, p provider.Provider) *Deps {
	t.Helper()
	cfg := config.Default()
	ag := &agent.Agent{Prov: p, Providers: map[string]provider.Provider{"primary": p}, Tools: tools.NewRegistry(), Store: st,
		Cfg: &cfg, SpillDir: t.TempDir()}
	return &Deps{Cfg: &cfg, Store: st, Agent: ag, Providers: ag.Providers, Log: slog.New(slog.DiscardHandler)}
}

func TestCLIWithoutAgentFails(t *testing.T) {
	c := &CLI{In: strings.NewReader("hi\n"), Out: io.Discard, Store: sessions.New(openTestDB(t)), Key: "cli:test"}
	if err := c.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "no agent") {
		t.Fatal(err)
	}
}

func TestCLITurnSeam(t *testing.T) {
	st := sessions.New(openTestDB(t))
	var out bytes.Buffer
	var got []string
	c := &CLI{In: strings.NewReader("a\nb\n"), Out: &out, Store: st, Key: "cli:test",
		Turn: func(_ context.Context, sess *sessions.Session, text string, w io.Writer) error {
			got = append(got, sess.Key+"|"+text)
			_, err := io.WriteString(w, "ok\n")
			return err
		}}
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "cli:test|a,cli:test|b" || out.String() != "ok\nok\n" {
		t.Fatal(got, out.String())
	}
}

// step is one scripted answer of fakeProv: a text, tool calls, or both.
type step struct {
	text  string
	calls []provider.ToolCall
}

// fakeProv is a scripted provider. Each call takes the next step; after the script it
// answers "ok". Each answer waits delay first. It counts the streams that run at once.
type fakeProv struct {
	delay time.Duration

	mu        sync.Mutex
	script    []step
	reqs      []provider.Request
	active    int
	maxActive int
}

func (f *fakeProv) Name() string  { return "primary" }
func (f *fakeProv) Model() string { return "fake-model" }

func (f *fakeProv) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	f.mu.Lock()
	st := step{text: "ok"}
	if len(f.script) > 0 {
		st, f.script = f.script[0], f.script[1:]
	}
	f.reqs = append(f.reqs, req)
	f.active++
	f.maxActive = max(f.maxActive, f.active)
	f.mu.Unlock()
	ch := make(chan provider.Event, 8)
	go func() {
		defer close(ch)
		defer func() { f.mu.Lock(); f.active--; f.mu.Unlock() }()
		if f.delay > 0 {
			select {
			case <-time.After(f.delay):
			case <-ctx.Done():
				ch <- provider.Event{Type: provider.EventError, Err: ctx.Err()}
				return
			}
		}
		if st.text != "" {
			ch <- provider.Event{Type: provider.EventText, Delta: st.text}
		}
		for i := range st.calls {
			ch <- provider.Event{Type: provider.EventToolCall, ToolCall: &st.calls[i]}
		}
		ch <- provider.Event{Type: provider.EventDone, FinishReason: "stop", Usage: &provider.Usage{}}
	}()
	return ch, nil
}

// lastUserTexts returns the last user message of each request.
func (f *fakeProv) lastUserTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.reqs {
		for i := len(r.Messages) - 1; i >= 0; i-- {
			if r.Messages[i].Role == provider.RoleUser {
				out = append(out, r.Messages[i].Content)
				break
			}
		}
	}
	return out
}

func (f *fakeProv) max() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxActive
}

// testDeps returns a Deps with a real agent over fp and a registry with read_file.
func testDeps(t *testing.T, fp *fakeProv) *Deps {
	t.Helper()
	st := sessions.New(openTestDB(t))
	reg := tools.NewRegistry()
	reg.Register(tools.ReadFileTool{})
	cfg := config.Default()
	ag := &agent.Agent{Prov: fp, Providers: map[string]provider.Provider{"primary": fp, "cheap": fp}, Tools: reg, Store: st,
		Cfg: &cfg, SpillDir: t.TempDir()}
	return &Deps{Cfg: &cfg, Store: st, Agent: ag, Providers: ag.Providers, Log: slog.New(slog.DiscardHandler)}
}

// runCLI runs the CLI with the input lines over the agent of d and returns the output.
func runCLI(t *testing.T, d *Deps, input string) string {
	t.Helper()
	var out bytes.Buffer
	c := &CLI{In: strings.NewReader(input), Out: &out, Key: "cli:test", Deps: d, Interrupts: make(chan os.Signal)}
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestCLIRunsToolLoopAndShowsProgress(t *testing.T) {
	p := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(p, []byte("secret plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"path": p})
	fp := &fakeProv{script: []step{
		{text: "Let me look.", calls: []provider.ToolCall{{ID: "c1", Name: "read_file", Args: string(args)}}},
		{text: "done"},
	}}
	d := testDeps(t, fp)
	out := runCLI(t, d, "read the file\n/exit\n")
	if !strings.Contains(out, `read_file: "`) || !strings.Contains(out, "done") {
		t.Fatal(out)
	}
	if !strings.Contains(out, "Let me look.\n\x1b[2m🔧 read_file: \"") || !strings.HasSuffix(out, "done\n") {
		t.Fatalf("%q", out)
	}
	sess, _ := d.Store.Active(context.Background(), "cli:test")
	msgs, _ := d.Store.Messages(context.Background(), sess.ID)
	if len(msgs) != 4 || !strings.Contains(msgs[2].Msg.Content, "secret plan") {
		t.Fatalf("%+v", msgs)
	}
	if sess.Title != "read the file" {
		t.Fatalf("auto title: %q", sess.Title)
	}
}

func TestCLISinkLines(t *testing.T) {
	var out bytes.Buffer
	s := &CLISink{W: &out, Line: func(tool, preview string) string { return "💻 " + tool + ": " + preview }}
	s.ToolStart("run_command", "ls")
	s.Delta("abc")
	s.Notice("compacting")
	s.Delta("def\n")
	s.Finish()
	want := "\x1b[2m💻 run_command: ls\x1b[0m\nabc\n\x1b[2mcompacting\x1b[0m\ndef\n"
	if out.String() != want {
		t.Fatalf("%q", out.String())
	}
}

func TestCLISinkStripsControlCharacters(t *testing.T) {
	// Model text and tool previews must not move the cursor, clear the screen, or set the
	// terminal title.
	var out bytes.Buffer
	s := &CLISink{W: &out}
	s.ToolStart("run_command", "ls\x1b[2J\x1b]0;pwned\x07 -la\r\x00")
	s.Delta("red \x1b[31mtext\x1b[0m\tok\b\x7f\u009b1m")
	s.Delta("\x1b")
	s.Delta("[Kend\n")
	s.Notice("note\x1b[1A")
	s.Finish()
	got := out.String()
	want := "\x1b[2m🔧 run_command: \"ls -la\"\x1b[0m\nred text\tok1m[Kend\n\x1b[2mnote\x1b[0m\n"
	if got != want {
		t.Fatalf("%q", got)
	}
}

func TestCLICtrlCDuringTurnStopsTurnOnly(t *testing.T) {
	st := sessions.New(openTestDB(t))
	ints := make(chan os.Signal, 1)
	started := make(chan struct{})
	var got []string
	var out bytes.Buffer
	c := &CLI{In: strings.NewReader("slow\nnext\n"), Out: &out, Store: st, Key: "cli:test", Interrupts: ints,
		Turn: func(ctx context.Context, _ *sessions.Session, text string, _ io.Writer) error {
			got = append(got, text)
			if text == "slow" {
				close(started)
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		}}
	go func() { <-started; ints <- os.Interrupt }()
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "slow,next" {
		t.Fatal(got)
	}
	if !strings.Contains(out.String(), StoppedReply) || strings.Contains(out.String(), "error:") {
		t.Fatalf("%q", out.String())
	}
}

func TestCLICtrlCAtPromptExits(t *testing.T) {
	st := sessions.New(openTestDB(t))
	pr, pw := io.Pipe()
	defer pw.Close()
	ints := make(chan os.Signal, 1)
	ints <- os.Interrupt
	c := &CLI{In: pr, Out: io.Discard, Store: st, Key: "cli:test", Interrupts: ints,
		Turn: func(context.Context, *sessions.Session, string, io.Writer) error { t.Fatal("no turn"); return nil }}
	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ctrl-C at the prompt must end the chat")
	}
}

func TestCLICommandsSwitchSession(t *testing.T) {
	d := testDeps(t, &fakeProv{})
	out := runCLI(t, d, "hello\n/new\nsecond\n/sessions\n/nope\n")
	if !strings.Contains(out, "✨ Starting new session.\nSend a message to begin.\n") ||
		!strings.Contains(out, "💡 Tip: ") || !strings.Contains(out, "Unknown command /nope") {
		t.Fatal(out)
	}
	list, _, _ := d.Store.ListPage(context.Background(), "cli:test", sessions.ListQuery{Limit: 10})
	if len(list) != 2 || list[0].MessageCount != 2 || list[1].MessageCount != 2 {
		t.Fatalf("%+v", list)
	}
}

func TestCLIDeleteAsksToConfirm(t *testing.T) {
	d := testDeps(t, &fakeProv{})
	ctx := context.Background()
	old, _ := d.Store.Create(ctx, sessions.Session{Key: "cli:test", Source: "cli"})
	out := runCLI(t, d, "/delete "+old.ID+"\nn\n/delete "+old.ID+"\ny\n")
	if !strings.Contains(out, "Not deleted.") || !strings.Contains(out, "🗑 Deleted \"New session\"") {
		t.Fatal(out)
	}
	if _, err := d.Store.Get(ctx, old.ID); err == nil {
		t.Fatal("not deleted")
	}
}

// registerTestCommand adds cmd to the command table and removes it at the end of the test.
func registerTestCommand(t *testing.T, cmd Command) {
	t.Helper()
	old, had := lookupCommand(cmd.Name)
	Register(cmd)
	t.Cleanup(func() {
		if had {
			Register(old)
			return
		}
		commandsMu.Lock()
		delete(commands, cmd.Name)
		commandsMu.Unlock()
	})
}

func TestCLIUnknownCommandPointsToHelp(t *testing.T) {
	out := runCLI(t, testDeps(t, &fakeProv{}), "/nope\n")
	if !strings.Contains(out, "Unknown command /nope. Send /help to see the commands.\n") {
		t.Fatal(out)
	}
}

func TestCLIReplyHasNoMarkdownMarkers(t *testing.T) {
	registerTestCommand(t, Command{Name: "t-md", Run: func(_ context.Context, c *CommandCtx, _ string) error {
		c.Reply("🧠 **Model: default**")
		return nil
	}})
	out := runCLI(t, testDeps(t, &fakeProv{}), "/t-md\n")
	if !strings.Contains(out, "🧠 Model: default\n") || strings.Contains(out, "**") {
		t.Fatal(out)
	}
}
