package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
)

// askMaxChoices limits the choices of one question, because a Telegram keyboard needs a
// limit.
const askMaxChoices = 10

// AskUserTool is the ask_user tool. It asks the user of the session a
// question through Env.Ask and waits for the answer.
type AskUserTool struct {
	// Timeout is the wait for an answer (approvals.timeout_s). Zero uses 600 s.
	Timeout time.Duration
}

type askArgs struct {
	Question    string   `json:"question"`
	Choices     []string `json:"choices"`
	MultiSelect bool     `json:"multi_select"`
}

// Name returns "ask_user".
func (AskUserTool) Name() string { return "ask_user" }

// Toolset returns the ask toolset.
func (AskUserTool) Toolset() string { return SetAsk }

// ParallelSafe returns false, because the user answers one question at a time.
func (AskUserTool) ParallelSafe() bool { return false }

// Schema returns the tool schema for the model.
func (AskUserTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{
		Name: "ask_user",
		Description: "Ask the user a question and wait for the answer. Use it when the task is ambiguous, " +
			"when a decision has trade-offs that the user must choose, or to get feedback. " +
			"Give choices for a multiple-choice question; the user can also type a different answer. " +
			"Omit choices for an open question. Do not use it to confirm dangerous commands: " +
			"run_command asks for those itself. When the decision is small, make a reasonable choice yourself.",
		Parameters: json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "properties": {
    "question": {"type": "string", "description": "The question to the user."},
    "choices": {"type": "array", "items": {"type": "string"}, "maxItems": %d, "description": "Optional answer choices. Omit them for an open question."},
    "multi_select": {"type": "boolean", "description": "Let the user pick more than one choice."}
  },
  "required": ["question"]
}`, askMaxChoices)),
	}
}

func (t AskUserTool) timeout() time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	return security.DefaultApprovalTimeout
}

// Run asks the question and returns the answer text, or the picked choices joined with
// ", ". When the user does not answer in time, the result tells the model to continue.
func (t AskUserTool) Run(ctx context.Context, raw json.RawMessage, env *Env) (string, error) {
	var a askArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	q := Question{Text: strings.TrimSpace(a.Question), Multi: a.MultiSelect, Timeout: t.timeout()}
	if q.Text == "" {
		return "", toolErr("question is required")
	}
	for _, c := range a.Choices {
		if c = strings.TrimSpace(c); c != "" {
			q.Choices = append(q.Choices, c)
		}
	}
	if len(q.Choices) > askMaxChoices {
		return "", toolErr("ask_user takes at most %d choices, got %d. Give fewer choices", askMaxChoices, len(q.Choices))
	}
	// The toolset policy hides ask_user in these runs. This check is the second defense.
	if env != nil {
		switch env.Kind {
		case "cron", "subagent", "review":
			return "", toolErr("ask_user: no user is present in a %s run. Continue without the answer", env.Kind)
		}
	}
	if env == nil || env.Ask == nil {
		return "", toolErr("ask_user: no user is present in this run. Continue without the answer")
	}

	actx, cancel := context.WithTimeout(ctx, q.Timeout)
	defer cancel()
	type result struct {
		ans Answer
		err error
	}
	ch := make(chan result, 1)
	go func() {
		ans, err := env.Ask.Ask(actx, q)
		ch <- result{ans, err}
	}()
	var r result
	select {
	case r = <-ch:
	case <-actx.Done():
		// The asker may not obey its context. The tool does not wait for it.
		r.err = actx.Err()
	}
	switch {
	case ctx.Err() != nil:
		return "", fmt.Errorf("the question stopped: %w", ctx.Err())
	case errors.Is(r.err, context.DeadlineExceeded) || (r.err != nil && actx.Err() != nil):
		return askTimeoutText(q.Timeout), nil
	case r.err != nil:
		return "", toolErr("ask_user: cannot ask the user: %v", r.err)
	}
	if len(r.ans.Choices) > 0 {
		return strings.Join(r.ans.Choices, ", "), nil
	}
	if text := strings.TrimSpace(r.ans.Text); text != "" {
		return text, nil
	}
	return "(the user gave an empty answer)", nil
}

// askTimeoutText is the result when the user does not answer within d.
func askTimeoutText(d time.Duration) string {
	return fmt.Sprintf("no answer within %s; continue without it", d)
}
