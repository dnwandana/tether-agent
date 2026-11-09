package gateway

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// ApprovalPrompt is the input prompt of the CLI approval question.
const ApprovalPrompt = "[o]nce / [s]ession / [a]lways / [d]eny: "

// The input prompts of a CLI approval question with fewer answers. A permission request of
// a coding agent has no "always" answer, and some requests also have no "session" answer.
const (
	approvalPromptNoAlways  = "[o]nce / [s]ession / [d]eny: "
	approvalPromptNoSession = "[o]nce / [d]eny: "
)

// The lines that ask again after an answer that the question does not permit.
const (
	cliRetry          = "Type o, s, a, or d."
	cliRetryNoAlways  = "Type o, s, or d."
	cliRetryNoSession = "Type o or d."
)

// approvalChoices returns the input prompt, the retry line, and the answers that req
// permits. NoSession wins over NoAlways, the same rule as the Telegram keyboard.
func approvalChoices(req security.ApprovalRequest) (prompt, retry string, session, always bool) {
	switch {
	case req.NoSession:
		return approvalPromptNoSession, cliRetryNoSession, false, false
	case req.NoAlways:
		return approvalPromptNoAlways, cliRetryNoAlways, true, false
	default:
		return ApprovalPrompt, cliRetry, true, true
	}
}

// The result lines of the CLI approval question. The Telegram headers have the same words.
const (
	cliAllowedOnce    = "✅ Approval allowed · once"
	cliAllowedSession = "✅ Approval allowed · this session"
	cliAllowedAlways  = "✅ Approval allowed · always"
	cliDenied         = "❌ Approval denied"
	cliNoAnswer       = "⌛ Approval denied · no answer"
)

// CLIAsk asks the user on the terminal. It is the approval Prompter and the clarify
// Asker of the CLI. It asks one question at a time. While CLI.Run runs, CLIAsk reads its
// lines through the input of the REPL, so the two never read the same line.
type CLIAsk struct {
	// In is the input when no CLI is attached. Nil means that no user can answer.
	In *bufio.Reader
	// Out receives the questions. Nil discards them.
	Out io.Writer

	mu sync.Mutex
	// read reads one line through the REPL. CLI.Run sets it.
	read func(ctx context.Context, prompt string) (string, error)
	// pending is a read of In that did not finish. The next question gets its line.
	pending chan lineResult
}

var (
	_ security.Prompter = (*CLIAsk)(nil)
	_ tools.Asker       = (*CLIAsk)(nil)
)

// PromptApproval shows the summary, the reason for the flag, and the dangerous command,
// and reads the answer. The permitted answers follow NoAlways and NoSession of req. An
// unknown or not permitted answer asks again. The end of the input denies the command.
// When ctx ends first, it returns ScopeTimeout and ctx.Err().
func (c *CLIAsk) PromptApproval(ctx context.Context, req security.ApprovalRequest) (security.Scope, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.out()
	pattern, _, _ := req.FallbackParts()
	fmt.Fprintf(out, "⚠ Approval needed\n  %s\n  Flagged: %s\n  %s\n",
		cliSummary(req), pattern, strings.ReplaceAll(security.ShowHidden(req.Command), "\n", "\n  "))
	if req.ChildName != "" {
		fmt.Fprintf(out, "  (from subagent %s)\n", req.ChildName)
	}
	prompt, retry, session, always := approvalChoices(req)
	for {
		line, err := c.readLine(ctx, prompt)
		switch {
		case ctx.Err() != nil:
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				fmt.Fprintln(out, "\n"+cliNoAnswer)
			}
			return security.ScopeTimeout, ctx.Err()
		case errors.Is(err, io.EOF):
			fmt.Fprintln(out, "\n"+cliDenied)
			return security.ScopeDeny, nil
		case err != nil:
			return security.ScopeDeny, err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "o", "once":
			fmt.Fprintln(out, cliAllowedOnce)
			return security.ScopeOnce, nil
		case "s", "session":
			if session {
				fmt.Fprintln(out, cliAllowedSession)
				return security.ScopeSession, nil
			}
		case "a", "always":
			if always {
				fmt.Fprintln(out, cliAllowedAlways)
				return security.ScopeAlways, nil
			}
		case "d", "deny":
			fmt.Fprintln(out, cliDenied)
			return security.ScopeDeny, nil
		}
		fmt.Fprintln(out, retry)
	}
}

// cliSummary returns the summary line of the CLI approval question: the clean summary of
// the model, or the pattern name and one line of the command when the model gave none.
func cliSummary(req security.ApprovalRequest) string {
	// A terminal runs ANSI escapes and bidi controls, so clean the summary again here.
	if s := security.CleanSummary(req.Summary); s != "" {
		return s
	}
	pattern, line, more := req.FallbackParts()
	s := pattern
	if line != "" {
		s += ": " + line
	}
	if more > 0 {
		s += fmt.Sprintf(" (+%d lines)", more)
	}
	return s
}

// Ask shows the question and its numbered choices, and reads the answer. A valid choice
// number (several, separated by commas or spaces, with Multi) gives Answer.Choices. Other
// text gives Answer.Text. An empty line asks again. A Question.Timeout above zero limits
// the wait.
func (c *CLIAsk) Ask(ctx context.Context, q tools.Question) (tools.Answer, error) {
	if q.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, q.Timeout)
		defer cancel()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.out()
	fmt.Fprintf(out, "❓ %s\n", q.Text)
	for i, ch := range q.Choices {
		fmt.Fprintf(out, "  %d. %s\n", i+1, ch)
	}
	prompt := "Answer: "
	switch {
	case len(q.Choices) > 0 && q.Multi:
		prompt = fmt.Sprintf("Choose one or more of 1-%d (for example 1,2), or type your own answer: ", len(q.Choices))
	case len(q.Choices) > 0:
		prompt = fmt.Sprintf("Choose 1-%d, or type your own answer: ", len(q.Choices))
	}
	for {
		line, err := c.readLine(ctx, prompt)
		if ctx.Err() != nil {
			return tools.Answer{}, ctx.Err()
		}
		if err != nil {
			return tools.Answer{}, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if picked, ok := pickChoices(line, q.Choices, q.Multi); ok {
			return tools.Answer{Choices: picked}, nil
		}
		return tools.Answer{Text: line}, nil
	}
}

// pickChoices returns the choices that line selects by number. ok is false when line is
// not a valid selection, for example free text or a number out of range.
func pickChoices(line string, choices []string, multi bool) ([]string, bool) {
	if len(choices) == 0 {
		return nil, false
	}
	fields := strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	if len(fields) == 0 || (!multi && len(fields) > 1) {
		return nil, false
	}
	var out []string
	seen := map[int]bool{}
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 || n > len(choices) {
			return nil, false
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, choices[n-1])
		}
	}
	return out, true
}

// attach makes c read its lines through read and write to out, while a CLI runs. The
// returned function restores the old input and output.
func (c *CLIAsk) attach(out io.Writer, read func(ctx context.Context, prompt string) (string, error)) (detach func()) {
	c.mu.Lock()
	oldOut, oldRead := c.Out, c.read
	c.Out, c.read = out, read
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		c.Out, c.read = oldOut, oldRead
		c.mu.Unlock()
	}
}

// readLine reads one answer line. The caller holds c.mu.
func (c *CLIAsk) readLine(ctx context.Context, prompt string) (string, error) {
	if c.read != nil {
		return c.read(ctx, prompt)
	}
	if c.In == nil {
		return "", errors.New("cli ask: no input is available for the question")
	}
	_, _ = io.WriteString(c.out(), prompt)
	if c.pending == nil {
		ch := make(chan lineResult, 1)
		in := c.In
		go func() {
			s, err := in.ReadString('\n')
			if err == io.EOF && s != "" {
				err = nil
			}
			ch <- lineResult{strings.TrimRight(s, "\r\n"), err}
		}()
		c.pending = ch
	}
	select {
	case r := <-c.pending:
		c.pending = nil
		return r.line, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (c *CLIAsk) out() io.Writer {
	if c.Out == nil {
		return io.Discard
	}
	return c.Out
}
