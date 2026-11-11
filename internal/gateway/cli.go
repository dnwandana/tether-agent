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
	"sync/atomic"
	"time"

	"github.com/ergochat/readline"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/goals"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
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
	// Turn runs one user message, writes the answer to out, and returns the result of the
	// turn. Nil uses AgentTurn, which needs Deps.Agent.
	Turn func(ctx context.Context, sess *sessions.Session, text string, out io.Writer) (agent.TurnResult, error)
	// Interrupts receives one value for each Ctrl-C. Nil listens for SIGINT of the process.
	Interrupts <-chan os.Signal

	stopMu sync.Mutex
	// stopTurn stops the running turn. It is nil when no turn runs.
	stopTurn func()
	// stopPending is true after a Ctrl-C that came between two turns of a wake loop. The
	// next turn or step of the loop does not run, so the loop stops. stopMu guards it.
	stopPending bool

	wakeOnce sync.Once
	waker    *GoalWaker
	// wakes gives the continuations of the waker to the REPL loop. It holds one wake.
	wakes chan goals.Continuation
	// slashText is the slash text of the skill command that runs now. AgentTurn marks the
	// turn with this text as a slash command, and then clears the field.
	slashText string
}

// refresher is a lineReader that can show its prompt again, for example readline.
type refresher interface {
	Refresh()
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

// cliInterruptReason is the paused reason of a goal after Ctrl-C stops a turn or a step.
// Hermes uses "user-interrupted (Ctrl+C)".
const cliInterruptReason = "interrupted by Ctrl-C"

// goalInterruptedLine is the Hermes line after Ctrl-C pauses a goal.
const goalInterruptedLine = "⏸ Goal paused — turn was interrupted. Use /goal resume to continue, or /goal clear to stop."

// cliTurnFunc is the type of CLI.Turn.
type cliTurnFunc = func(context.Context, *sessions.Session, string, io.Writer) (agent.TurnResult, error)

// Run reads lines until /exit, /quit, EOF, or Ctrl-C at the prompt. A slash command runs at once.
// Each other line runs one turn. Ctrl-C during a turn stops the turn and pauses an active
// goal. After each turn, the goal step runs, and a continuation runs at once. A turn error
// prints one "error: ..." line, and the loop continues.
func (c *CLI) Run(ctx context.Context) error {
	store := c.store()
	sess, err := store.ActiveOrCreate(ctx, c.Key, sessions.Session{Source: "cli", Kind: "chat"})
	if err != nil {
		return err
	}
	// A crash or a SIGHUP can leave a goal of the CLI active. No turn runs it now.
	if c.pauseChatEnded(ctx, sess) {
		if st, ok, err := c.deps().Goals.Load(ctx, sess.ID); err == nil && ok {
			fmt.Fprintln(c.Out, goals.StatusLine(st, time.Now()))
		}
	}
	// A command can change the session, so the exit reads sess when it runs.
	defer func() {
		if c.pauseChatEnded(ctx, sess) {
			fmt.Fprintln(c.Out, GoalPausedLine)
		}
	}()
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
	feed := newLineFeed(lr, c.interruptWake)
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
	if c.deps().Goals != nil {
		defer c.GoalWaker().Stop()
	}
	wakes := c.wakeChan()
	for ctx.Err() == nil {
		line, wake, err := feed.next(ctx, ints, wakes)
		if errors.Is(err, io.EOF) || errors.Is(err, readline.ErrInterrupt) || errors.Is(err, errInterrupted) {
			return nil
		}
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return fmt.Errorf("cli: cannot read the input: %w", err)
		}
		if wake != nil {
			c.runWake(ctx, turn, sess, *wake, ints, lr, feed)
			continue
		}
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		if text == "/exit" || text == "/quit" {
			return nil
		}
		if name, args, ok := IsCommand(text); ok {
			sess, err = c.command(ctx, sess, name, args, text, feed, ints, turn)
			if err != nil {
				return err
			}
			continue
		}
		c.turnLoop(ctx, turn, sess, text, "", ints)
	}
	return nil
}

// GoalWaker returns the waker of the CLI process. Its Fire gives the continuation to the
// REPL loop, which runs it at the prompt. Fire also works before Run starts.
func (c *CLI) GoalWaker() *GoalWaker {
	c.wakeOnce.Do(func() {
		c.wakes = make(chan goals.Continuation, 1)
		c.waker = &GoalWaker{Goals: c.deps().Goals, Log: c.logger(), Fire: c.fireWake}
	})
	return c.waker
}

// fireWake gives gc to the REPL loop with no wait. A wake that already waits runs the goal,
// so a second wake returns an error, and the waker keeps the barrier.
func (c *CLI) fireWake(_ context.Context, _, _ string, gc goals.Continuation) error {
	select {
	case c.wakes <- gc:
		return nil
	default:
		return errors.New("cli: a goal wake already waits")
	}
}

// wakeChan returns the channel of the goal wakes, or nil when the goals are off.
func (c *CLI) wakeChan() <-chan goals.Continuation {
	if c.deps().Goals == nil {
		return nil
	}
	c.GoalWaker()
	return c.wakes
}

// runWake runs the continuation of a wake at the prompt. It drops a wake for another
// session or for an old epoch of the goal.
func (c *CLI) runWake(ctx context.Context, turn cliTurnFunc, sess *sessions.Session, wake goals.Continuation,
	ints <-chan os.Signal, lr lineReader, feed *lineFeed) {
	if wake.SessionID != sess.ID || !c.deps().Goals.IsCurrent(wake.SessionID, wake.Epoch) {
		c.logger().Debug("goal wake dropped", "session", wake.SessionID, "epoch", wake.Epoch)
		return
	}
	// The prompt shows, so the output starts on a new line.
	fmt.Fprintln(c.Out)
	// The read of the prompt goes on during the turns, so Ctrl-C comes through the feed.
	feed.inTurn.Store(true)
	c.turnLoop(ctx, turn, sess, wake.Text, cliGoalLine(wake), ints)
	feed.inTurn.Store(false)
	// A Ctrl-C after the last step of the loop must not stop a later turn.
	c.stopMu.Lock()
	c.stopPending = false
	c.stopMu.Unlock()
	if r, ok := lr.(refresher); ok {
		r.Refresh()
	}
}

// interruptible runs f with a context that a Ctrl-C or interruptTurn cancels. It returns
// after f returns. stopped is true when the user stopped f and ctx did not end. After a
// Ctrl-C between two turns of a wake loop, f does not run and stopped is true.
func (c *CLI) interruptible(ctx context.Context, ints <-chan os.Signal, f func(ctx context.Context)) (stopped bool) {
	fctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	result := make(chan bool, 1)
	stopReq := make(chan struct{}, 1)
	pending := c.armStopTurn(func() {
		select {
		case stopReq <- struct{}{}:
		default:
		}
	})
	defer c.setStopTurn(nil)
	if pending {
		return ctx.Err() == nil
	}
	go func() {
		select {
		case <-ints:
		case <-stopReq:
		case <-done:
			result <- false
			return
		}
		cancel()
		result <- true
	}()
	f(fctx)
	close(done)
	return <-result && ctx.Err() == nil
}

// runTurn runs one turn. A Ctrl-C during the turn cancels the context of the turn.
func (c *CLI) runTurn(ctx context.Context, turn cliTurnFunc, sess *sessions.Session, text string,
	ints <-chan os.Signal) (res agent.TurnResult, stopped bool, err error) {
	stopped = c.interruptible(ctx, ints, func(tctx context.Context) {
		res, err = turn(tctx, sess, text, c.Out)
	})
	if stopped {
		c.logger().Info("turn stopped by the user", "session", sess.ID)
		fmt.Fprintln(c.Out, StoppedReply)
		return res, true, err
	}
	if err != nil && ctx.Err() == nil {
		c.logger().Error("turn failed", "session", sess.ID, "err", err.Error())
		fmt.Fprintf(c.Out, "error: %v\n", err)
	}
	return res, false, err
}

// cliGoalLine returns the dim line that the CLI prints before the goal turn of gc, or ""
// for no line. A continuation after a judge verdict uses the Hermes CLI text.
func cliGoalLine(gc goals.Continuation) string {
	switch gc.LineKind {
	case "":
		return ""
	case goals.LineContinue:
		return fmt.Sprintf("↻ Continuing toward goal (%d/%d): %s", gc.Used, gc.Max, gc.Reason)
	}
	return gc.StatusLine()
}

// turnLoop runs one turn and then the goal step. A continuation of the step runs at once
// as the next turn, with no read of input. The loop ends when the step gives no
// continuation. Ctrl-C during a turn or during the step pauses an active goal.
func (c *CLI) turnLoop(ctx context.Context, turn cliTurnFunc, sess *sessions.Session, text, goalLine string,
	ints <-chan os.Signal) {
	for {
		if goalLine != "" {
			_, _ = io.WriteString(c.Out, ansiDim+termSafe(goalLine)+ansiReset+"\n")
		}
		res, stopped, err := c.runTurn(ctx, turn, sess, text, ints)
		d := c.deps()
		if d.Goals == nil || ctx.Err() != nil {
			// At the end of ctx, the exit of Run pauses the goal.
			return
		}
		if stopped {
			// No step runs after a stopped turn.
			c.pauseInterrupted(ctx, sess)
			return
		}
		var step goals.StepResult
		out := goals.TurnOutcome{Key: c.Key, Session: sess, Final: res.Final, Err: err}
		// interruptible waits for the step to return, so the pause below is the last write.
		if c.interruptible(ctx, ints, func(sctx context.Context) { step = d.Goals.AfterTurn(sctx, out) }) {
			c.logger().Info("goal step stopped by the user", "session", sess.ID)
			fmt.Fprintln(c.Out, StoppedReply)
			c.pauseInterrupted(ctx, sess)
			return
		}
		if ctx.Err() != nil {
			return
		}
		for _, n := range step.Notices {
			fmt.Fprintln(c.Out, StripMarkdown(FormatNotice(n).Text))
		}
		c.afterStep(ctx, sess, step)
		if step.Continuation == nil {
			return
		}
		text, goalLine = step.Continuation.Text, cliGoalLine(*step.Continuation)
	}
}

// afterStep handles the parts of a step result that wait: a parked goal and a retry.
// The timers fire through the waker of the CLI.
func (c *CLI) afterStep(ctx context.Context, sess *sessions.Session, res goals.StepResult) {
	if !res.Parked && res.RetryAfter <= 0 {
		return
	}
	st, ok, err := c.deps().Goals.Load(ctx, sess.ID)
	if err != nil {
		c.logger().Error("cannot load the goal to arm the wake", "session", sess.ID, "err", err.Error())
		return
	}
	if !ok {
		return
	}
	if res.Parked {
		c.GoalWaker().Arm(c.Key, sess.ID, st)
	}
	if res.RetryAfter > 0 {
		c.GoalWaker().ArmRetry(c.Key, sess.ID, st.Epoch, res.RetryAfter)
	}
}

// pauseInterrupted pauses an active goal of sess after Ctrl-C stopped a turn or a step,
// and prints the Hermes line. The context of the step can be cancelled, so the pause does
// not use its cancel.
func (c *CLI) pauseInterrupted(ctx context.Context, sess *sessions.Session) {
	paused, err := pauseGoal(context.WithoutCancel(ctx), c.deps(), sess.ID, cliInterruptReason)
	if err != nil {
		c.logger().Error("cannot pause the goal after Ctrl-C", "session", sess.ID, "err", err.Error())
		return
	}
	if paused {
		fmt.Fprintln(c.Out, goalInterruptedLine)
	}
}

// pauseStopped pauses an active goal of sess for /stop and /stop keep. It returns true when
// it paused a goal. A failure is logged, and the command goes on.
func (c *CLI) pauseStopped(ctx context.Context, sess *sessions.Session) bool {
	paused, err := pauseGoal(ctx, c.deps(), sess.ID, ReasonStopped)
	if err != nil {
		c.logger().Error("cannot pause the goal for /stop", "session", sess.ID, "err", err.Error())
	}
	return paused
}

// pauseChatEnded pauses an active goal of sess, because no chat runs its turns. It
// returns true when it paused a goal.
func (c *CLI) pauseChatEnded(ctx context.Context, sess *sessions.Session) bool {
	d := c.deps()
	if d.Goals == nil || sess == nil {
		return false
	}
	paused, err := pauseGoal(context.WithoutCancel(ctx), d, sess.ID, ReasonChatEnded)
	if err != nil {
		c.logger().Error("cannot pause the goal at the end of the chat", "session", sess.ID, "err", err.Error())
		return false
	}
	return paused
}

func (c *CLI) setStopTurn(f func()) {
	c.stopMu.Lock()
	c.stopTurn = f
	c.stopMu.Unlock()
}

// armStopTurn sets f as the stop of the turn that starts. It returns true when a Ctrl-C
// came before the turn, and then the turn must not run.
func (c *CLI) armStopTurn(f func()) (pending bool) {
	c.stopMu.Lock()
	defer c.stopMu.Unlock()
	c.stopTurn = f
	pending = c.stopPending
	c.stopPending = false
	return pending
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

// interruptWake gets a readline Ctrl-C during a wake loop. It stops the running turn. When
// no turn runs, the loop is between two turns, so the next turn or step of the loop does
// not run. It always returns true, because the Ctrl-C must not end the chat.
func (c *CLI) interruptWake() bool {
	c.stopMu.Lock()
	f := c.stopTurn
	if f == nil {
		c.stopPending = true
	}
	c.stopMu.Unlock()
	if f != nil {
		f()
	}
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
func (c *CLI) command(ctx context.Context, sess *sessions.Session, name, args, text string, feed *lineFeed,
	ints <-chan os.Signal, turn cliTurnFunc) (*sessions.Session, error) {
	cmd, ok := lookupCommand(name)
	if !ok {
		return c.skillCommand(ctx, sess, name, text, ints, turn), nil
	}
	// A goal turn that the command starts runs after the reply of the command.
	var kick *goals.Continuation
	// Only the StopKeep hook sets goalPaused, so the replies of the other commands do not
	// change.
	goalPaused := false
	cc := &CommandCtx{
		Deps:    c.deps(),
		Key:     c.Key,
		Owner:   true,
		Session: sess,
		Reply:   func(text string) { fmt.Fprintln(c.Out, StripMarkdown(stopGoalText(text, goalPaused))) },
		Confirm: func(prompt string) bool {
			fmt.Fprintf(c.Out, "%s [y/N] ", prompt)
			line, _, err := feed.next(ctx, ints, nil)
			if err != nil {
				fmt.Fprintln(c.Out)
				return false
			}
			a := strings.ToLower(strings.TrimSpace(line))
			return a == "y" || a == "yes"
		},
		// No turn runs while the REPL reads a command. So /stop and /stop keep find no
		// task, but they pause a parked goal, so that its wake starts no turn. /now finds
		// no waiting message (no Now hook), and /steer runs its text as a normal message.
		// The CLI has no queue, so no goal turn waits to be dropped.
		Hooks: CommandHooks{
			Stop: func() {
				fmt.Fprintln(c.Out, StripMarkdown(stopGoalText(NoTaskReply, c.pauseStopped(ctx, sess))))
			},
			StopKeep: func() (bool, int) {
				goalPaused = c.pauseStopped(ctx, sess)
				return false, 0
			},
			Steer:         func(text string) { c.turnLoop(ctx, turn, sess, text, "", ints) },
			StartGoalTurn: func(gc goals.Continuation) { kick = &gc },
			DropGoalTurns: func() {},
			ArmWake:       func(id string, st goals.State) { c.GoalWaker().Arm(c.Key, id, st) },
		},
	}
	if err := cmd.Run(ctx, cc, args); err != nil {
		c.logger().Error("command failed", "command", name, "err", err.Error())
		fmt.Fprintf(c.Out, "error: %v\n", err)
	}
	// A command can point the key at another session, or delete the current one.
	next, err := c.store().ActiveOrCreate(ctx, c.Key, sessions.Session{Source: "cli", Kind: "chat"})
	if err != nil || kick == nil || kick.SessionID != next.ID {
		return next, err
	}
	c.turnLoop(ctx, turn, next, kick.Text, cliGoalLine(*kick), ints)
	return c.store().ActiveOrCreate(ctx, c.Key, sessions.Session{Source: "cli", Kind: "chat"})
}

// skillCommand runs text as a skill turn when name is a skill command of the CLI. Else it
// prints the Unknown command line. It returns the session of the key.
func (c *CLI) skillCommand(ctx context.Context, sess *sessions.Session, name, text string,
	ints <-chan os.Signal, turn cliTurnFunc) *sessions.Session {
	sc, ok := ResolveSkill(c.deps().SkillCommandsFor("cli"), name)
	if !ok {
		fmt.Fprintf(c.Out, "Unknown command /%s. Send /help to see the commands.\n", name)
		return sess
	}
	_, _ = io.WriteString(c.Out, ansiDim+termSafe("⚡ Loading skill: "+sc.Skill.Name)+ansiReset+"\n")
	// Deps.Turn resolves the skill again and builds the text, because the turn is marked.
	c.slashText = text
	c.turnLoop(ctx, turn, sess, text, "", ints)
	// A custom Turn does not call AgentTurn, so the field is cleared here too.
	c.slashText = ""
	return sess
}

// AgentTurn runs one agent turn with tools. It streams the answer and the dim tool
// progress lines to out. The fixed text of a turn with no summary does not stream, so
// AgentTurn prints it.
func (c *CLI) AgentTurn(ctx context.Context, sess *sessions.Session, text string, out io.Writer) (agent.TurnResult, error) {
	d := c.deps()
	sink := &CLISink{W: out, Line: d.toolLine}
	// A review notice, for example "💾 Memory updated", shows after the answer. A summary
	// with several lines prints one dim line for each line.
	notify, release := agent.HoldNotices(sink.Notices)
	// Only the first turn after a skill command is marked. A goal continuation of the loop
	// has another text, and the field is empty then.
	slash := text != "" && text == c.slashText
	c.slashText = ""
	res, err := d.Turn(ctx, sess, TurnInput{Text: text, Channel: "cli", Sink: sink, Notify: notify, Slash: slash})
	sink.Finish()
	// The stream already showed each other final text.
	if res.NoSummary {
		fmt.Fprintln(out, res.Final)
	}
	release()
	return res, err
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
	lr      lineReader
	req     chan string
	res     chan lineResult
	pending bool
	// interrupt stops the running turn and returns false when no turn runs. Nil stops
	// nothing.
	interrupt func() bool
	// inTurn is true while a turn runs during a read, for example a wake turn.
	inTurn atomic.Bool
}

type lineResult struct {
	line string
	err  error
}

// newLineFeed starts the reads of lr. interrupt gets each readline Ctrl-C that comes while
// inTurn is true.
func newLineFeed(lr lineReader, interrupt func() bool) *lineFeed {
	f := &lineFeed{lr: lr, req: make(chan string), res: make(chan lineResult, 1), interrupt: interrupt}
	go func() {
		for prompt := range f.req {
			line, err := readPrompt(lr, prompt)
			// A wake turn runs while readline reads, and readline turns off the signals of
			// the terminal. So Ctrl-C comes here as ErrInterrupt and not as SIGINT. It stops
			// the turn, or the loop when it comes between two turns. It must not end the
			// chat, so the read starts again, and a line typed later still goes to the next
			// caller.
			for errors.Is(err, readline.ErrInterrupt) && f.inTurn.Load() && f.interrupt != nil && f.interrupt() {
				line, err = readPrompt(lr, "")
			}
			f.res <- lineResult{line, err}
		}
	}()
	return f
}

// readPrompt reads one line from lr with prompt. An empty prompt keeps the REPL prompt.
func readPrompt(lr lineReader, prompt string) (string, error) {
	sp, ok := lr.(promptSetter)
	if ok && prompt != "" {
		sp.SetPrompt(prompt)
	}
	line, err := lr.ReadLine()
	if ok && prompt != "" {
		sp.SetPrompt(replPrompt)
	}
	return line, err
}

// next returns the next line, or a goal wake from wakes. It returns errInterrupted on
// Ctrl-C and ctx.Err() at the end of ctx. A read that did not finish gives its line to the
// next call, also after a wake. A nil wakes gets no wake.
func (f *lineFeed) next(ctx context.Context, ints <-chan os.Signal, wakes <-chan goals.Continuation) (line string, wake *goals.Continuation, err error) {
	return f.read(ctx, ints, wakes, "")
}

// nextPrompt is next with a readline prompt for this line and no wake. A CLIAsk question
// uses it, so a wake waits for the end of the question. An empty prompt keeps the REPL
// prompt. A nil ints ignores Ctrl-C.
func (f *lineFeed) nextPrompt(ctx context.Context, ints <-chan os.Signal, prompt string) (string, error) {
	line, _, err := f.read(ctx, ints, nil, prompt)
	return line, err
}

// read asks for one line with prompt and waits for the line, a Ctrl-C, a wake, or the end
// of ctx.
func (f *lineFeed) read(ctx context.Context, ints <-chan os.Signal, wakes <-chan goals.Continuation,
	prompt string) (string, *goals.Continuation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.pending {
		f.req <- prompt
		f.pending = true
	} else if prompt != "" {
		// The pending read shows the REPL prompt, for example during a wake turn. A question
		// must show its own prompt, so the reader shows the question until the answer.
		f.showPrompt(prompt)
		defer f.showPrompt(replPrompt)
	}
	select {
	case r := <-f.res:
		f.pending = false
		return r.line, nil, r.err
	case w := <-wakes:
		return "", &w, nil
	case <-ints:
		return "", nil, errInterrupted
	case <-ctx.Done():
		return "", nil, ctx.Err()
	}
}

// showPrompt shows prompt on a reader that reads now. A reader with no prompt shows
// nothing.
func (f *lineFeed) showPrompt(prompt string) {
	sp, ok := f.lr.(promptSetter)
	if !ok {
		return
	}
	sp.SetPrompt(prompt)
	if r, ok := f.lr.(refresher); ok {
		r.Refresh()
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
	// A test gives its own line reader as In.
	if lr, ok := c.In.(lineReader); ok {
		return lr, nil
	}
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
	// lastTodo is the preview of the last todo_list call. Plan reads the kind of the
	// call from it, because the two lists do not tell the kind of the call.
	lastTodo string
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
	if name == "todo_list" {
		s.mu.Lock()
		s.lastTodo = preview
		s.mu.Unlock()
	}
	s.dimLine(line)
}

// Plan prints the Hermes plan row, so the terminal shows the progress of the task list.
func (s *CLISink) Plan(_, items []tools.TodoItem) {
	s.mu.Lock()
	kind := s.lastTodo
	s.mu.Unlock()
	done, total := tools.TodoSummary(items)
	n := len(items)
	var detail string
	switch {
	case strings.HasPrefix(kind, "reading"):
		detail = "reading tasks"
	case strings.HasPrefix(kind, "updating") && done > 0:
		detail = fmt.Sprintf("update %d/%d ✓", done, total)
	case strings.HasPrefix(kind, "updating"):
		detail = fmt.Sprintf("update %d task(s)", n)
	case done > 0:
		detail = fmt.Sprintf("%d/%d task(s)", done, total)
	default:
		detail = fmt.Sprintf("%d task(s)", n)
	}
	s.dimLine(fmt.Sprintf("┊ 📋 %-9s %s", "plan", detail))
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

// Notices prints each line of text as a dim line, in one write. A review summary uses it,
// so its lines stay together.
func (s *CLISink) Notices(text string) {
	var b strings.Builder
	for line := range strings.SplitSeq(text, "\n") {
		b.WriteString(ansiDim)
		b.WriteString(termSafe(line))
		b.WriteString(ansiReset)
		b.WriteString("\n")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endLine()
	_, _ = io.WriteString(s.W, b.String())
}

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
