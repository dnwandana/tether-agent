// Package gateway connects the channels (the CLI and Telegram) to the agent runs.
package gateway

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"

	"github.com/ergochat/readline"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// CLI is the interactive chat on the terminal.
type CLI struct {
	In  io.Reader
	Out io.Writer
	// Store holds the sessions. Nil uses Deps.Store.
	Store *sessions.Store
	// Key is the session key, cli:<os user name>.
	Key string
	Log *slog.Logger
	// HistoryFile keeps the readline history. Empty means no saved history.
	HistoryFile string
	// Deps gives the agent turns and the slash commands their data. With Deps.Agent set,
	// each line runs an agent turn with tools.
	Deps *Deps
	// Turn runs one user message and writes the answer to out. Nil uses AgentTurn, which
	// needs Deps.Agent.
	Turn func(ctx context.Context, sess *sessions.Session, text string, out io.Writer) error
	// Interrupts receives one value for each Ctrl-C. Nil listens for SIGINT of the process.
	Interrupts <-chan os.Signal

	stopMu sync.Mutex
	// stopTurn stops the running turn. It is nil when no turn runs.
	stopTurn func()
}

// lineReader gives one input line for each call and io.EOF at the end.
type lineReader interface {
	ReadLine() (string, error)
	Close() error
}

// promptSetter is a lineReader that shows a prompt, for example readline.
type promptSetter interface {
	SetPrompt(prompt string)
}

// replPrompt is the readline prompt of the REPL.
const replPrompt = "> "

// errInterrupted is the result of a read that a Ctrl-C ended.
var errInterrupted = errors.New("interrupted")

// StoppedReply is the line that the CLI prints after Ctrl-C stops a turn.
const StoppedReply = "⚡ Stopped. You can continue this session."

// Run reads lines until /exit, /quit, EOF, or Ctrl-C at the prompt. A slash command runs at once.
// Each other line runs one turn. Ctrl-C during a turn stops the turn, the same as /stop,
// and the chat continues. A turn error prints one "error: ..." line, and the loop continues.
func (c *CLI) Run(ctx context.Context) error {
	store := c.store()
	sess, err := store.ActiveOrCreate(ctx, c.Key, sessions.Session{Source: "cli", Kind: "chat"})
	if err != nil {
		return err
	}
	lr, err := c.newLineReader()
	if err != nil {
		return err
	}
	defer lr.Close()
	ints := c.Interrupts
	if ints == nil {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt)
		defer signal.Stop(ch)
		ints = ch
	}
	feed := newLineFeed(lr)
	defer feed.close()
	if ask := c.deps().CLIAsk; ask != nil {
		defer ask.attach(c.Out, c.askReader(lr, feed))()
	}
	turn := c.Turn
	switch {
	case turn != nil:
	case c.Deps != nil && c.Deps.Agent != nil:
		turn = c.AgentTurn
	default:
		return errors.New("cli: no agent is set: set Deps.Agent or Turn")
	}
	for ctx.Err() == nil {
		line, err := feed.next(ctx, ints)
		if errors.Is(err, io.EOF) || errors.Is(err, readline.ErrInterrupt) || errors.Is(err, errInterrupted) {
			return nil
		}
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return fmt.Errorf("cli: cannot read the input: %w", err)
		}
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		if text == "/exit" || text == "/quit" {
			return nil
		}
		if name, args, ok := IsCommand(text); ok {
			sess, err = c.command(ctx, sess, name, args, feed, ints, turn)
			if err != nil {
				return err
			}
			continue
		}
		c.runTurn(ctx, turn, sess, text, ints)
	}
	return nil
}

// runTurn runs one turn. A Ctrl-C during the turn cancels the context of the turn.
func (c *CLI) runTurn(ctx context.Context, turn func(context.Context, *sessions.Session, string, io.Writer) error,
	sess *sessions.Session, text string, ints <-chan os.Signal) {
	tctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	stopped := make(chan bool, 1)
	stopReq := make(chan struct{}, 1)
	c.setStopTurn(func() {
		select {
		case stopReq <- struct{}{}:
		default:
		}
	})
	defer c.setStopTurn(nil)
	go func() {
		select {
		case <-ints:
		case <-stopReq:
		case <-done:
			stopped <- false
			return
		}
		cancel()
		stopped <- true
	}()
	err := turn(tctx, sess, text, c.Out)
	close(done)
	if <-stopped && ctx.Err() == nil {
		c.logger().Info("turn stopped by the user", "session", sess.ID)
		fmt.Fprintln(c.Out, StoppedReply)
		return
	}
	if err != nil && ctx.Err() == nil {
		c.logger().Error("turn failed", "session", sess.ID, "err", err.Error())
		fmt.Fprintf(c.Out, "error: %v\n", err)
	}
}

func (c *CLI) setStopTurn(f func()) {
	c.stopMu.Lock()
	c.stopTurn = f
	c.stopMu.Unlock()
}

// interruptTurn stops the running turn, the same as Ctrl-C. It returns false when no turn runs.
func (c *CLI) interruptTurn() bool {
	c.stopMu.Lock()
	f := c.stopTurn
	c.stopMu.Unlock()
	if f == nil {
		return false
	}
	f()
	return true
}

// askReader returns the line reader of CLIAsk. It reads through the line feed of the
// REPL with prompt. Ctrl-C at a readline question stops the turn, the same as Ctrl-C
// during the turn.
func (c *CLI) askReader(lr lineReader, feed *lineFeed) func(ctx context.Context, prompt string) (string, error) {
	_, shows := lr.(promptSetter)
	return func(ctx context.Context, prompt string) (string, error) {
		if !shows {
			_, _ = io.WriteString(c.Out, prompt)
		}
		line, err := feed.nextPrompt(ctx, nil, prompt)
		if errors.Is(err, readline.ErrInterrupt) && c.interruptTurn() {
			<-ctx.Done()
			return "", ctx.Err()
		}
		return line, err
	}
}

// command runs one slash command and returns the session that the key uses after it.
func (c *CLI) command(ctx context.Context, sess *sessions.Session, name, args string, feed *lineFeed,
	ints <-chan os.Signal, turn func(context.Context, *sessions.Session, string, io.Writer) error) (*sessions.Session, error) {
	cmd, ok := lookupCommand(name)
	if !ok {
		fmt.Fprintf(c.Out, "Unknown command /%s. The commands are: %s, /exit.\n", name, commandList())
		return sess, nil
	}
	cc := &CommandCtx{
		Deps:    c.deps(),
		Key:     c.Key,
		Session: sess,
		Reply:   func(text string) { fmt.Fprintln(c.Out, text) },
		Confirm: func(prompt string) bool {
			fmt.Fprintf(c.Out, "%s [y/N] ", prompt)
			line, err := feed.next(ctx, ints)
			if err != nil {
				fmt.Fprintln(c.Out)
				return false
			}
			a := strings.ToLower(strings.TrimSpace(line))
			return a == "y" || a == "yes"
		},
		// No turn runs while the REPL reads a command, so /stop has no task (no Stop
		// hook), and /steer runs its text as a normal message.
		Hooks: CommandHooks{
			Steer: func(text string) { c.runTurn(ctx, turn, sess, text, ints) },
		},
	}
	if err := cmd.Run(ctx, cc, args); err != nil {
		c.logger().Error("command failed", "command", name, "err", err.Error())
		fmt.Fprintf(c.Out, "error: %v\n", err)
	}
	// A command can point the key at another session, or delete the current one.
	return c.store().ActiveOrCreate(ctx, c.Key, sessions.Session{Source: "cli", Kind: "chat"})
}

// AgentTurn runs one agent turn with tools. It streams the answer and the dim tool
// progress lines to out.
func (c *CLI) AgentTurn(ctx context.Context, sess *sessions.Session, text string, out io.Writer) error {
	d := c.deps()
	sink := &CLISink{W: out, Line: d.toolLine}
	// A review notice, for example "💾 Memory updated", shows after the answer.
	notify, release := agent.HoldNotices(sink.Notice)
	_, err := d.Turn(ctx, sess, TurnInput{Text: text, Channel: "cli", Sink: sink, Notify: notify})
	sink.Finish()
	release()
	return err
}

// deps returns c.Deps, or a Deps with the store and the log only.
func (c *CLI) deps() *Deps {
	if c.Deps != nil {
		return c.Deps
	}
	return &Deps{Store: c.Store, Log: c.Log}
}

// store returns c.Store, else Deps.Store.
func (c *CLI) store() *sessions.Store {
	if c.Store == nil && c.Deps != nil {
		return c.Deps.Store
	}
	return c.Store
}

// lineFeed reads lines in a goroutine, one line for each request, so that a read can stop
// on Ctrl-C or at the end of the context. It reads only when the loop or a CLIAsk question
// asks for a line, so the prompt does not show during a turn. mu lets one caller read at a
// time, so the REPL and a question never get the same line.
type lineFeed struct {
	mu      sync.Mutex
	req     chan string
	res     chan lineResult
	pending bool
}

type lineResult struct {
	line string
	err  error
}

func newLineFeed(lr lineReader) *lineFeed {
	f := &lineFeed{req: make(chan string), res: make(chan lineResult, 1)}
	go func() {
		for prompt := range f.req {
			sp, ok := lr.(promptSetter)
			if ok && prompt != "" {
				sp.SetPrompt(prompt)
			}
			line, err := lr.ReadLine()
			if ok && prompt != "" {
				sp.SetPrompt(replPrompt)
			}
			f.res <- lineResult{line, err}
		}
	}()
	return f
}

// next returns the next line. It returns errInterrupted on Ctrl-C and ctx.Err() at the end
// of ctx. A read that did not finish gives its line to the next call.
func (f *lineFeed) next(ctx context.Context, ints <-chan os.Signal) (string, error) {
	return f.nextPrompt(ctx, ints, "")
}

// nextPrompt is next with a readline prompt for this line. An empty prompt keeps the
// REPL prompt. A nil ints ignores Ctrl-C.
func (f *lineFeed) nextPrompt(ctx context.Context, ints <-chan os.Signal, prompt string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.pending {
		f.req <- prompt
		f.pending = true
	}
	select {
	case r := <-f.res:
		f.pending = false
		return r.line, r.err
	case <-ints:
		return "", errInterrupted
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (f *lineFeed) close() { close(f.req) }

func (c *CLI) logger() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

// newLineReader returns a readline prompt when In is a terminal, and a plain line scanner
// for other input, for example a pipe or a test.
func (c *CLI) newLineReader() (lineReader, error) {
	if f, ok := c.In.(*os.File); ok && isTerminal(f) {
		if c.HistoryFile != "" {
			// readline makes the file with mode 0666. The history can hold secrets, so make
			// the file first with mode 0600.
			h, err := os.OpenFile(c.HistoryFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
			if err != nil {
				return nil, fmt.Errorf("cli: cannot open the history file: %w", err)
			}
			h.Close()
		}
		rl, err := readline.NewFromConfig(&readline.Config{
			Prompt:            replPrompt,
			HistoryFile:       c.HistoryFile,
			HistorySearchFold: true,
			Stdin:             f,
			Stdout:            c.Out,
		})
		if err != nil {
			return nil, fmt.Errorf("cli: cannot start the prompt: %w", err)
		}
		return rl, nil
	}
	sc := bufio.NewScanner(c.In)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return &scanReader{sc: sc}, nil
}

// isTerminal reports whether f is a character device, for example a terminal.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// scanReader reads lines with a bufio.Scanner.
type scanReader struct{ sc *bufio.Scanner }

func (r *scanReader) ReadLine() (string, error) {
	if r.sc.Scan() {
		return r.sc.Text(), nil
	}
	if err := r.sc.Err(); err != nil {
		return "", err
	}
	return "", io.EOF
}

func (r *scanReader) Close() error { return nil }

// The ANSI codes of a dim line.
const (
	ansiDim   = "\x1b[2m"
	ansiReset = "\x1b[0m"
)

// CLISink writes the live output of a turn to the terminal. The answer text goes to W as
// it streams. Each tool call and each notice is one dim line. It is safe for concurrent
// use. Call Finish after the turn.
type CLISink struct {
	W io.Writer
	// Line returns the progress line of a tool call. Nil uses `🔧 tool: "preview"`.
	Line func(tool, preview string) string

	mu sync.Mutex
	// midLine is true when the last text that W got does not end with a newline.
	midLine bool
}

// Delta writes a part of the answer, without the control characters.
func (s *CLISink) Delta(text string) {
	text = termSafe(text)
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = io.WriteString(s.W, text)
	s.midLine = !strings.HasSuffix(text, "\n")
}

// ToolStart writes the dim progress line of a tool call.
func (s *CLISink) ToolStart(name, preview string) {
	line := defaultToolLine(name, preview)
	if s.Line != nil {
		line = s.Line(name, preview)
	}
	s.dimLine(line)
}

// ToolEnd writes nothing. The progress line is enough.
func (s *CLISink) ToolEnd(string, bool) {}

// Boundary ends the line of the streamed text before the tool lines.
func (s *CLISink) Boundary() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endLine()
}

// Notice writes a status text as a dim line.
func (s *CLISink) Notice(text string) { s.dimLine(text) }

// Finish ends the last line of the answer.
func (s *CLISink) Finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endLine()
}

func (s *CLISink) dimLine(text string) {
	text = strings.ReplaceAll(termSafe(text), "\n", " ")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endLine()
	_, _ = io.WriteString(s.W, ansiDim+text+ansiReset+"\n")
}

// reEscape matches an ANSI escape sequence: CSI (ESC [ ... final byte), OSC (ESC ] ... BEL
// or ESC \), or ESC and one character.
var reEscape = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])`)

// termSafe removes the escape sequences and the control characters from text that the model
// or a tool wrote, except "\n" and "\t". Such text must not move the cursor, clear the
// screen, or change the terminal title. A sequence that a stream splits loses its ESC, so
// the rest shows as plain text.
func termSafe(text string) string {
	if !strings.ContainsFunc(text, isTermControl) {
		return text
	}
	text = reEscape.ReplaceAllString(text, "")
	return strings.Map(func(r rune) rune {
		if isTermControl(r) {
			return -1
		}
		return r
	}, text)
}

// isTermControl reports whether r is a C0 control (not "\n" or "\t"), DEL, or a C1 control.
func isTermControl(r rune) bool {
	return (r < 0x20 && r != '\n' && r != '\t') || (r >= 0x7f && r <= 0x9f)
}

// endLine writes a newline when the cursor is not at the start of a line. The caller holds mu.
func (s *CLISink) endLine() {
	if s.midLine {
		_, _ = io.WriteString(s.W, "\n")
		s.midLine = false
	}
}
