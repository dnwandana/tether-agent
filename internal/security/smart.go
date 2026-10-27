package security

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// smartTimeout limits one call of the smart judge.
const smartTimeout = 30 * time.Second

// smartMaxTokens is enough for the verdict and a short reason.
const smartMaxTokens = 100

// smartPrompt is the prompt of Hermes _smart_approve (tools/approval.py).
// Hermes asks for one word: APPROVE, DENY, or ESCALATE. The Judge interface
// has no "deny" result, so this prompt joins DENY into ESCALATE, and the
// model also gives a short reason.
const smartPrompt = `You are a security reviewer for an AI coding agent. A terminal command was flagged by pattern matching as potentially dangerous.

Command: %s
Flagged reason: %s

Assess the ACTUAL risk of this command. Many flagged commands are false positives — for example, ` + "`python -c \"print('hello')\"`" + ` is flagged as "script execution via -c flag" but is completely harmless.

Rules:
- APPROVE if the command is clearly safe (benign script execution, safe file operations, development tools, package installs, git operations, etc.)
- ESCALATE if the command could genuinely damage the system (recursive delete of important paths, overwriting system files, fork bombs, wiping disks, dropping databases, etc.)
- ESCALATE if you're uncertain

Respond with exactly one line: "APPROVE: <short reason>" or "ESCALATE: <short reason>"`

// ProviderJudge is the smart-mode Judge. It asks the model of the session if
// a flagged command is risky. Each failure counts as risky, so the gate then
// asks the user.
type ProviderJudge struct {
	Prov provider.Provider
}

// Judge returns risky=false only when the model gives an APPROVE verdict, for
// example "APPROVE: <reason>". An error also returns risky=true.
func (j ProviderJudge) Judge(ctx context.Context, command, description string) (bool, error) {
	if j.Prov == nil {
		return true, errors.New("smart approval: no provider is set")
	}
	ctx, cancel := context.WithTimeout(ctx, smartTimeout)
	defer cancel()
	ch, err := j.Prov.Stream(ctx, provider.Request{
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: fmt.Sprintf(smartPrompt, command, description)}},
		MaxTokens: smartMaxTokens,
	})
	if err != nil {
		return true, fmt.Errorf("smart approval: model call failed: %w", err)
	}
	res, err := provider.Collect(ch, nil)
	if err != nil {
		return true, fmt.Errorf("smart approval: model call failed: %w", err)
	}
	return !approves(res.Text), nil
}

// approves reports whether the answer is an APPROVE verdict. The first line
// that is not empty must be "APPROVE" or "APPROVE" followed by ":", "-", or
// ".". No later line may give a different verdict.
func approves(answer string) bool {
	first := true
	for line := range strings.Lines(answer) {
		v := verdict(line)
		if v == "" && first {
			if strings.TrimSpace(line) == "" {
				continue
			}
			return false
		}
		if first {
			if v != "APPROVE" {
				return false
			}
			first = false
			continue
		}
		if v != "" && v != "APPROVE" {
			return false
		}
	}
	return !first
}

// verdict returns the verdict word at the start of line, or "".
func verdict(line string) string {
	s := strings.ToUpper(strings.Trim(strings.TrimSpace(line), "*`'\"_ "))
	for _, w := range []string{"APPROVE", "ESCALATE", "DENY"} {
		rest, ok := strings.CutPrefix(s, w)
		if !ok {
			continue
		}
		rest = strings.TrimLeft(rest, "*`'\"_ ")
		if rest == "" || rest[0] == ':' || rest[0] == '-' || rest[0] == '.' {
			return w
		}
	}
	return ""
}
