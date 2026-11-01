package gateway

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ergochat/readline"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// A wake at the REPL prompt runs the continuation. The pending read then gets the next line.
func TestCLIWakeAtPromptRunsTurn(t *testing.T) {
	d := cliGoalDeps(t, &fakeProv{})
	ctx := context.Background()
	pr, pw := io.Pipe()
	var (
		mu  sync.Mutex
		got []string
	)
	turns := make(chan string, 4)
	var out bytes.Buffer
	c := &CLI{In: pr, Out: &out, Key: "cli:test", Deps: d, Interrupts: make(chan os.Signal),
		Turn: func(_ context.Context, _ *sessions.Session, text string, _ io.Writer) (agent.TurnResult, error) {
			mu.Lock()
			got = append(got, text)
			mu.Unlock()
			if text == "hello" {
				// The goal starts after the start check, and it waits 600 s.
				setCLIGoal(t, d, "cli:test", "fix the cron tests", func(st *goals.State) {
					st.WaitFor(600, 0, "build runs", time.Now())
				})
			}
			turns <- text
			return agent.TurnResult{Final: "ok"}, nil
		}}
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	if _, err := io.WriteString(pw, "hello\n"); err != nil {
		t.Fatal(err)
	}
	<-turns
	sess, _ := d.Store.Active(ctx, "cli:test")
	st, _, _ := d.Goals.Load(ctx, sess.ID)
	w := c.GoalWaker()
	err := w.Fire(ctx, "cli:test", sess.ID, goals.Continuation{SessionID: sess.ID, Text: "wake text",
		Epoch: st.Epoch, LineKind: goals.LineLifted, Used: 1, Max: 20})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-turns:
		if text != "wake text" {
			t.Fatal(text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a wake at the prompt must run the continuation")
	}
	if _, err := io.WriteString(pw, "later\n"); err != nil {
		t.Fatal(err)
	}
	<-turns
	pw.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 || got[2] != "later" {
		t.Fatalf("%q", got)
	}
}

// A wake with an old epoch runs no turn.
func TestCLIWakeStaleEpochDropped(t *testing.T) {
	d := cliGoalDeps(t, &fakeProv{})
	ctx := context.Background()
	sess, _ := d.Store.ActiveOrCreate(ctx, "cli:test", sessions.Session{Source: "cli", Kind: "chat"})
	var got []string
	c := &CLI{In: bytes.NewReader(nil), Out: io.Discard, Key: "cli:test", Deps: d,
		Interrupts: make(chan os.Signal), Turn: workTurn(&got)}
	if err := c.GoalWaker().Fire(ctx, "cli:test", sess.ID, goals.Continuation{SessionID: sess.ID, Text: "old", Epoch: 99}); err != nil {
		t.Fatal(err)
	}
	if err := c.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("%q", got)
	}
}

// The reader of a CLIAsk question does not return on a wake, and the wake stays.
func TestLineFeedQuestionIgnoresWake(t *testing.T) {
	ctx := context.Background()
	pr, pw := io.Pipe()
	defer pw.Close()
	sc := bufio.NewScanner(pr)
	f := newLineFeed(&scanReader{sc: sc}, nil)
	defer f.close()
	wakes := make(chan goals.Continuation, 1)
	wakes <- goals.Continuation{Text: "w"}
	got := make(chan string, 1)
	go func() {
		line, _ := f.nextPrompt(ctx, nil, "? ")
		got <- line
	}()
	select {
	case line := <-got:
		t.Fatalf("a question must not return on a wake: %q", line)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := io.WriteString(pw, "yes\n"); err != nil {
		t.Fatal(err)
	}
	if line := <-got; line != "yes" || len(wakes) != 1 {
		t.Fatal(line, len(wakes))
	}
	line, wake, err := f.next(ctx, nil, wakes)
	if err != nil || line != "" || wake == nil || wake.Text != "w" {
		t.Fatal(line, wake, err)
	}
}

// chanReader is an input of the CLI that gives the results of res as lines. It acts as
// readline, so a test can send readline.ErrInterrupt. A closed res gives io.EOF.
type chanReader struct{ res chan lineResult }

func (r *chanReader) Read([]byte) (int, error) { return 0, io.EOF }

func (r *chanReader) ReadLine() (string, error) {
	x, ok := <-r.res
	if !ok {
		return "", io.EOF
	}
	return x.line, x.err
}

func (r *chanReader) Close() error { return nil }

// Ctrl-C during a wake turn stops the turn and pauses the goal. Readline reads during the
// turn, so Ctrl-C comes as readline.ErrInterrupt, not as a signal. The CLI stays open.
func TestCLICtrlCDuringWakeTurnPausesGoal(t *testing.T) {
	d := cliGoalDeps(t, &fakeProv{})
	ctx := context.Background()
	in := &chanReader{res: make(chan lineResult)}
	turns := make(chan string, 4)
	started := make(chan struct{})
	var out syncBuffer
	c := &CLI{In: in, Out: &out, Key: "cli:test", Deps: d, Interrupts: make(chan os.Signal),
		Turn: func(tctx context.Context, _ *sessions.Session, text string, _ io.Writer) (agent.TurnResult, error) {
			switch text {
			case "hello":
				// The goal starts after the start check, and it waits 600 s.
				setCLIGoal(t, d, "cli:test", "fix the cron tests", func(st *goals.State) {
					st.WaitFor(600, 0, "build runs", time.Now())
				})
			case "wake text":
				close(started)
				<-tctx.Done()
				turns <- text
				return agent.TurnResult{}, tctx.Err()
			}
			turns <- text
			return agent.TurnResult{Final: "ok"}, nil
		}}
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	in.res <- lineResult{line: "hello"}
	<-turns
	sess, _ := d.Store.Active(ctx, "cli:test")
	st, _, _ := d.Goals.Load(ctx, sess.ID)
	err := c.GoalWaker().Fire(ctx, "cli:test", sess.ID, goals.Continuation{SessionID: sess.ID, Text: "wake text",
		Epoch: st.Epoch, LineKind: goals.LineLifted, Used: 1, Max: 20})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the wake turn must start")
	}
	in.res <- lineResult{err: readline.ErrInterrupt}
	select {
	case <-turns:
	case <-time.After(5 * time.Second):
		t.Fatal("Ctrl-C must stop the wake turn")
	}
	select {
	case err := <-done:
		t.Fatalf("Ctrl-C during a turn must not end the chat: %v", err)
	case in.res <- lineResult{line: "later"}:
	case <-time.After(5 * time.Second):
		t.Fatal("the CLI must read the next line")
	}
	if text := <-turns; text != "later" {
		t.Fatal(text)
	}
	close(in.res)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if o := out.String(); !strings.Contains(o, StoppedReply+"\n"+interruptedLine+"\n") || strings.Contains(o, GoalPausedLine) {
		t.Fatalf("%q", o)
	}
	if st := goalOf(t, d, "cli:test"); st.Status != goals.StatusPaused || *st.PausedReason != cliInterruptReason {
		t.Fatalf("%+v", st)
	}
}

// callReader is a chanReader that sends one value to calls at the start of each ReadLine.
type callReader struct {
	chanReader
	calls chan struct{}
}

func (r *callReader) ReadLine() (string, error) {
	r.calls <- struct{}{}
	return r.chanReader.ReadLine()
}

// gateWriter is a syncBuffer that holds the first dim line until release is closed. It
// closes reached when that line comes.
type gateWriter struct {
	syncBuffer
	once             sync.Once
	reached, release chan struct{}
}

func (w *gateWriter) Write(p []byte) (int, error) {
	if bytes.HasPrefix(p, []byte(ansiDim)) {
		w.once.Do(func() {
			close(w.reached)
			<-w.release
		})
	}
	return w.syncBuffer.Write(p)
}

// Ctrl-C between two turns of a wake loop, when no turn runs, stops the loop and pauses the
// goal. The chat stays open, and the next line runs.
func TestCLICtrlCBetweenWakeTurnsPausesGoal(t *testing.T) {
	d := cliGoalDeps(t, &fakeProv{})
	ctx := context.Background()
	in := &callReader{chanReader: chanReader{res: make(chan lineResult)}, calls: make(chan struct{}, 16)}
	out := &gateWriter{reached: make(chan struct{}), release: make(chan struct{})}
	turns := make(chan string, 4)
	c := &CLI{In: in, Out: out, Key: "cli:test", Deps: d, Interrupts: make(chan os.Signal),
		Turn: func(_ context.Context, _ *sessions.Session, text string, _ io.Writer) (agent.TurnResult, error) {
			if text == "hello" {
				// The goal starts after the start check, and it waits 600 s.
				setCLIGoal(t, d, "cli:test", "fix the cron tests", func(st *goals.State) {
					st.WaitFor(600, 0, "build runs", time.Now())
				})
			}
			turns <- text
			return agent.TurnResult{Final: "ok"}, nil
		}}
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	in.res <- lineResult{line: "hello"}
	if text := <-turns; text != "hello" {
		t.Fatal(text)
	}
	sess, _ := d.Store.Active(ctx, "cli:test")
	st, _, _ := d.Goals.Load(ctx, sess.ID)
	err := c.GoalWaker().Fire(ctx, "cli:test", sess.ID, goals.Continuation{SessionID: sess.ID, Text: "wake text",
		Epoch: st.Epoch, LineKind: goals.LineLifted, Used: 1, Max: 20})
	if err != nil {
		t.Fatal(err)
	}
	// The goal line of the wake turn holds the loop before the turn starts.
	select {
	case <-out.reached:
	case <-time.After(5 * time.Second):
		close(out.release)
		t.Fatal("the wake loop must start")
	}
	// The read of "hello" and the read during the wake.
	<-in.calls
	<-in.calls
	in.res <- lineResult{err: readline.ErrInterrupt}
	select {
	case <-in.calls:
	case <-time.After(5 * time.Second):
		close(out.release)
		t.Fatal("the reader must read again after Ctrl-C between two turns")
	}
	close(out.release)
	select {
	case err := <-done:
		t.Fatalf("Ctrl-C between two turns must not end the chat: %v", err)
	case in.res <- lineResult{line: "later"}:
	case <-time.After(5 * time.Second):
		t.Fatal("the CLI must read the next line")
	}
	if text := <-turns; text != "later" {
		t.Fatalf("the wake turn must not run after Ctrl-C: %q", text)
	}
	close(in.res)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if o := out.String(); !strings.Contains(o, StoppedReply+"\n"+interruptedLine+"\n") || strings.Contains(o, GoalPausedLine) {
		t.Fatalf("%q", o)
	}
	if st := goalOf(t, d, "cli:test"); st.Status != goals.StatusPaused || *st.PausedReason != cliInterruptReason {
		t.Fatalf("%+v", st)
	}
}

// promptReader is a chanReader that records each SetPrompt and Refresh call.
type promptReader struct {
	chanReader
	mu  sync.Mutex
	log []string
}

func (r *promptReader) SetPrompt(p string) { r.add("prompt:" + p) }
func (r *promptReader) Refresh()           { r.add("refresh") }

func (r *promptReader) add(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log = append(r.log, event)
}

func (r *promptReader) events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.log...)
}

// A question during a pending REPL read, for example an approval during a wake turn, shows
// its prompt on the reader. The REPL prompt comes back after the answer.
func TestLineFeedQuestionDuringPendingReadShowsPrompt(t *testing.T) {
	ctx := context.Background()
	r := &promptReader{chanReader: chanReader{res: make(chan lineResult)}}
	f := newLineFeed(r, nil)
	defer f.close()
	wakes := make(chan goals.Continuation, 1)
	wakes <- goals.Continuation{Text: "w"}
	if _, wake, err := f.next(ctx, nil, wakes); err != nil || wake == nil {
		t.Fatal(wake, err)
	}
	// The REPL read is still pending, as during a wake turn.
	got := make(chan string, 1)
	go func() {
		line, _ := f.nextPrompt(ctx, nil, "Approve? ")
		got <- line
	}()
	waitFor(t, func() bool { return slices.Contains(r.events(), "prompt:Approve? ") })
	r.res <- lineResult{line: "y"}
	if line := <-got; line != "y" {
		t.Fatal(line)
	}
	want := []string{"prompt:Approve? ", "refresh", "prompt:" + replPrompt, "refresh"}
	if ev := r.events(); !slices.Equal(ev, want) {
		t.Fatalf("%q", ev)
	}
}
