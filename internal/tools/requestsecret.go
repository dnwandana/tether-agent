package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// requestSecretWait is the longest wait for the owner. Tests change it.
var requestSecretWait = 5 * time.Minute

// The result texts of request_secret.
const (
	secretNotBound  = "%q is not a bound secret. The owner binds secrets in exec_secrets in config.yaml."
	secretOnlyOwner = "request_secret works only in a chat turn of the owner. Tell the owner that %s is missing."
	secretSaved     = "Saved %s. You did not see the value. Run the command again."
	secretTimeout   = "The owner did not give %s in 5 min."
	// secretReasonRunes is the longest reason, in runes.
	secretReasonRunes = 240
)

// RequestSecretTool asks the owner for the value of a bound secret. The UI saves the value to
// .env. The value never comes back to the tool, so the model never sees it.
type RequestSecretTool struct {
	// Bound maps each bound secret name to its command prefixes (exec_secrets).
	Bound map[string][]string
}

// Name returns "request_secret".
func (RequestSecretTool) Name() string { return "request_secret" }

// Toolset returns the terminal toolset.
func (RequestSecretTool) Toolset() string { return SetTerminal }

// ParallelSafe returns false, because the owner answers one card at a time.
func (RequestSecretTool) ParallelSafe() bool { return false }

// Schema returns the tool schema for the model.
func (RequestSecretTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{
		Name: "request_secret",
		Description: "Ask the owner for the value of a bound secret that is missing or bad. The owner gets a " +
			"private message and pastes the value there. Tether Agent saves the value to .env. You never see the value. " +
			"Use it only when a terminal result says that a bound secret has no value or is marked for a check. " +
			"The bound names are in TOOL NOTES. It works only in a chat turn of the owner.",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "The bound secret name, for example PYPI_TOKEN."},
    "reason": {"type": "string", "description": "One short sentence for the owner that tells why you need the value. At most 240 characters."}
  },
  "required": ["name", "reason"]
}`),
	}
}

type requestSecretArgs struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// cleanReason turns each control character into a space, trims the text, and cuts it to
// secretReasonRunes runes.
func cleanReason(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > secretReasonRunes {
		s = string(r[:secretReasonRunes])
	}
	return s
}

// Run asks the owner for the value of the bound secret name and returns the result text.
func (t RequestSecretTool) Run(ctx context.Context, raw json.RawMessage, env *Env) (string, error) {
	var a requestSecretArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	name := strings.TrimSpace(a.Name)
	cmds, ok := t.Bound[name]
	if !ok {
		return "", toolErr(secretNotBound, name)
	}
	var asker SecretAsker
	if env != nil && env.Kind == "chat" && env.RunRootKind() == "chat" && !env.Member && !env.ScriptCall && env.Ask != nil {
		asker, _ = env.Ask.(SecretAsker)
	}
	if asker == nil {
		return "", toolErr(secretOnlyOwner, name)
	}
	q := SecretQuestion{Name: name, Commands: cmds, Reason: cleanReason(a.Reason)}

	actx, cancel := context.WithTimeout(ctx, requestSecretWait)
	defer cancel()
	type result struct {
		saved bool
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		saved, err := asker.AskSecret(actx, q)
		ch <- result{saved, err}
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
		return "", fmt.Errorf("the secret request stopped: %w", ctx.Err())
	case errors.Is(r.err, context.DeadlineExceeded) || (r.err != nil && actx.Err() != nil):
		return fmt.Sprintf(secretTimeout, name), nil
	case r.err != nil:
		return "", toolErr("%s", r.err.Error())
	case !r.saved:
		return fmt.Sprintf(secretTimeout, name), nil
	}
	return fmt.Sprintf(secretSaved, name), nil
}
