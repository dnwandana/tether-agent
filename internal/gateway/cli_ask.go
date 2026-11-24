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

// approvalPromptRedacted is the input prompt of an MCP send with sensitive data.
const approvalPromptRedacted = "[r] send redacted / [o]nce / [d]eny: "

// cliRetryRedacted asks again after an answer that a send with sensitive data does not permit.
const cliRetryRedacted = "Type r, o, or d."

// approvalChoices returns the input prompt, the retry line, and the answers that req
// permits. A request with findings gets the "send redacted" answer and no other. NoSession
// wins over NoAlways, the same rule as the Telegram keyboard.
func approvalChoices(req security.ApprovalRequest) (prompt, retry string, session, always, redacted bool) {
	switch {
	case len(req.Findings) > 0:
		return approvalPromptRedacted, cliRetryRedacted, false, false, true
	case req.NoSession:
		return approvalPromptNoSession, cliRetryNoSession, false, false, false
	case req.NoAlways:
		return approvalPromptNoAlways, cliRetryNoAlways, true, false, false
	default:
		return ApprovalPrompt, cliRetry, true, true, false
	}
}

// findingItems returns the labels of the findings, each without its outer "[" and "]",
// joined with ", ".
func findingItems(f []security.Finding) string {
	items := make([]string, 0, len(f))
	for _, x := range f {
		l := x.Label
		if strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]") {
			l = l[1 : len(l)-1]
		}
		items = append(items, l)
	}
	return strings.Join(items, ", ")
}

// The input prompt and the retry line of a pin request.
const (
	approvalPromptPin = "[p] allow this program  [d] deny: "
	cliRetryPin       = "Type p or d."
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
	// Save saves a secret value to .env. Nil refuses each AskSecret.
	Save func(name, value string) error
	// readSecret reads one masked line through the REPL. CLI.Run sets it. Nil refuses each
	// AskSecret.
	readSecret func(ctx context.Context, prompt string) (string, error)
	// pending is a read of In that did not finish. The next question gets its line.
	pending chan lineResult
}

var (
	_ security.Prompter = (*CLIAsk)(nil)
	_ tools.Asker       = (*CLIAsk)(nil)
	_ tools.SecretAsker = (*CLIAsk)(nil)
)

// PromptApproval shows the summary, the reason for the flag, and the dangerous command,
// and reads the answer. The permitted answers follow NoAlways and NoSession of req. An
// unknown or not permitted answer asks again. The end of the input denies the command.
// When ctx ends first, it returns ScopeTimeout and ctx.Err().
func (c *CLIAsk) PromptApproval(ctx context.Context, req security.ApprovalRequest) (security.Scope, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.out()
	pin := req.PinName != ""
	prompt, retry, session, always, redacted := approvalChoices(req)
	kind := "approval"
	result := func(scope string) string { return CardResultHeader(CardResult{Kind: kind, Scope: scope}) }
	if pin {
		// A terminal runs ANSI escapes and bidi controls, so clean each line.
		fmt.Fprintf(out, "%s\n  %s\n  sha256 %s\n  Command: %s\n", security.CleanSummary(PinQuestion(req)),
			security.CleanSummary(req.PinPath), security.CleanSummary(ShortHash(req.PinHash)),
			security.CleanSummary(req.Command))
		prompt, retry = approvalPromptPin, cliRetryPin
		kind = "pin"
	} else {
		// A terminal runs ANSI escapes and bidi controls, so clean the flagged text too.
		fmt.Fprintf(out, "%s\n  %s\n  Flagged: %s\n", cliHeader(req),
			cliSummary(req), security.CleanSummary(ApprovalFlagged(req)))
		for _, l := range ApprovalSecretLines(req) {
			fmt.Fprintf(out, "  %s\n", security.CleanSummary(l))
		}
		fmt.Fprintf(out, "  %s\n", strings.ReplaceAll(security.ShowHidden(req.Command), "\n", "\n  "))
	}
	if req.ChildName != "" {
		fmt.Fprintf(out, "  (from subagent %s)\n", req.ChildName)
	}
	if len(req.Findings) > 0 {
		fmt.Fprintf(out, "  ⚠️ Found: %s\n", security.CleanSummary(findingItems(req.Findings)))
	}
	for {
		line, err := c.readLine(ctx, prompt)
		switch {
		case ctx.Err() != nil:
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				fmt.Fprintln(out, "\n"+result("timeout"))
			} else {
				fmt.Fprintln(out, "\n"+CardResultHeader(CardResult{Stopped: true}))
			}
			return security.ScopeTimeout, ctx.Err()
		case errors.Is(err, io.EOF):
			fmt.Fprintln(out, "\n"+result("deny"))
			return security.ScopeDeny, nil
		case err != nil:
			return security.ScopeDeny, err
		}
		switch ans := strings.ToLower(strings.TrimSpace(line)); {
		case pin && (ans == "p" || ans == "allow"), !pin && (ans == "o" || ans == "once"):
			fmt.Fprintln(out, result("once"))
			return security.ScopeOnce, nil
		case !pin && redacted && (ans == "r" || ans == "redacted"):
			fmt.Fprintln(out, result("redacted"))
			return security.ScopeRedacted, nil
		case !pin && session && (ans == "s" || ans == "session"):
			fmt.Fprintln(out, result("session"))
			return security.ScopeSession, nil
		case !pin && always && (ans == "a" || ans == "always"):
			fmt.Fprintln(out, result("always"))
			return security.ScopeAlways, nil
		case ans == "d" || ans == "deny":
			fmt.Fprintln(out, result("deny"))
			return security.ScopeDeny, nil
		}
		fmt.Fprintln(out, retry)
	}
}

// maxStagedLines is the most paths that the staged-files lines of an approval prompt show.
const maxStagedLines = 20

// PinQuestion returns the header of a pin request: "🔑 Give <name> to this program?".
func PinQuestion(req security.ApprovalRequest) string {
	return "🔑 Give " + req.PinName + " to this program?"
}

// ShortHash returns the first 4 and the last 4 characters of a sha256 hex with "…" between them.
// A hash of 8 characters or fewer stays as it is.
func ShortHash(hash string) string {
	if len(hash) <= 8 {
		return hash
	}
	return hash[:4] + "…" + hash[len(hash)-4:]
}

// ApprovalSecretLines returns the raw lines about secrets of req, in this order: the "🔑 Gets"
// line, the "🔑 Uses a printed credential" line, and the "This turn read" line. The lines
// about staged credential files follow. Each UI cleans or escapes them.
func ApprovalSecretLines(req security.ApprovalRequest) []string {
	var lines []string
	if len(req.Secrets) > 0 {
		lines = append(lines, "🔑 Gets: "+strings.Join(req.Secrets, ", "))
	}
	if len(req.HandleNames) > 0 {
		lines = append(lines, "🔑 Uses a printed credential: "+strings.Join(req.HandleNames, ", "))
		if len(req.OutsideSources) > 0 {
			lines = append(lines, "This turn read: "+strings.Join(req.OutsideSources, ", "))
		}
	}
	if len(req.StagedCredentials) > 0 {
		lines = append(lines, "This stages credential files:")
		for i, p := range req.StagedCredentials {
			if i == maxStagedLines {
				break
			}
			lines = append(lines, "• "+p)
		}
	}
	if req.StageUnchecked {
		lines = append(lines, "Tether Agent could not check which files this command stages.")
	}
	return lines
}

// maxFlaggedRunes is the limit of the flagged text of an approval prompt, with the final "…".
const maxFlaggedRunes = 300

// humanOnlySentence follows the subject of a human-only request.
const humanOnlySentence = ". No mode, grant, or judge can approve this. Tap each time."

// cutFlagged cuts s at maxFlaggedRunes runes. A cut keeps 299 runes and adds "…".
func cutFlagged(s string) string {
	if rs := []rune(s); len(rs) > maxFlaggedRunes {
		return string(rs[:maxFlaggedRunes-1]) + "…"
	}
	return s
}

// ApprovalSubject returns the raw subject of req for the approval texts. Each UI cleans it.
func ApprovalSubject(req security.ApprovalRequest) string {
	var s string
	if req.Class != "" {
		s = "MCP " + req.Class + " (" + req.Server + " · " + req.ToolName + ")"
	} else {
		s, _, _ = req.FallbackParts()
	}
	if req.Via != "" {
		s += " (inside " + req.Via + ")"
	}
	return cutFlagged(s)
}

// ApprovalFlagged returns the raw text after "Flagged: ". A human-only request adds the
// sentence that only a tap approves it. The sentence is never cut.
func ApprovalFlagged(req security.ApprovalRequest) string {
	s := ApprovalSubject(req)
	if req.HumanOnly {
		s += humanOnlySentence
	}
	return s
}

// cliHeader returns the first line of the CLI approval question.
func cliHeader(req security.ApprovalRequest) string {
	switch {
	case req.HumanOnly:
		return "🔒 Human-only action"
	case req.Class == "send":
		return "📤 Send on your behalf?"
	}
	return "⚠ Approval needed"
}

// cliSummary returns the summary line of the CLI approval question: the clean summary of
// the model, or the pattern name and one line of the command when the model gave none.
func cliSummary(req security.ApprovalRequest) string {
	// A terminal runs ANSI escapes and bidi controls, so clean the summary again here.
	if s := security.CleanSummary(req.Summary); s != "" {
		return s
	}
	_, line, more := req.FallbackParts()
	s := security.CleanSummary(ApprovalSubject(req))
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

// attach makes c read its lines through read, its masked lines through readSecret, and
// write to out, while a CLI runs. The returned function restores the old values.
func (c *CLIAsk) attach(out io.Writer, read, readSecret func(ctx context.Context, prompt string) (string, error)) (detach func()) {
	c.mu.Lock()
	oldOut, oldRead, oldSecret := c.Out, c.read, c.readSecret
	c.Out, c.read, c.readSecret = out, read, readSecret
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		c.Out, c.read, c.readSecret = oldOut, oldRead, oldSecret
		c.mu.Unlock()
	}
}

// userError is an error whose text is a full sentence for the user.
type userError string

func (e userError) Error() string { return string(e) }

// AskSecret shows the card of q, reads the value with a masked read, and saves it with Save.
// The value never comes back to the caller and never goes to the output.
func (c *CLIAsk) AskSecret(ctx context.Context, q tools.SecretQuestion) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	busy := userError(fmt.Sprintf("The CLI is busy. Ask the owner to type /secrets set %s at the prompt.", q.Name))
	if c.readSecret == nil || c.Save == nil {
		return false, busy
	}
	out := c.out()
	fmt.Fprintf(out, "🔑 Secret needed: %s\nFor: %s\n", q.Name, strings.Join(q.Commands, ", "))
	if q.Reason != "" {
		fmt.Fprintf(out, "Reason: %s\n", q.Reason)
	}
	line, err := c.readSecret(ctx, "Value for "+q.Name+": ")
	switch {
	case errors.Is(err, errFeedBusy) || errors.Is(err, errNoMaskedRead):
		return false, busy
	case ctx.Err() != nil:
		return false, ctx.Err()
	case errors.Is(err, io.EOF):
		return false, nil
	case err != nil:
		return false, err
	}
	value := strings.TrimSpace(line)
	if value == "" {
		fmt.Fprintln(out, "No value. Nothing was saved.")
		return false, nil
	}
	if err := security.CheckSecretValue(value); err != nil {
		fmt.Fprintln(out, err.Error())
		return false, err
	}
	if err := c.Save(q.Name, value); err != nil {
		err = userError(fmt.Sprintf("I could not save %s: %v. Send it again.", q.Name, err))
		fmt.Fprintln(out, err.Error())
		return false, err
	}
	fmt.Fprintf(out, "✅ Saved %s.\n", q.Name)
	return true, nil
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
