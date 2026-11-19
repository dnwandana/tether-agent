package security

import (
	"context"
	"errors"
)

// ErrNoPrompter tells that no user can answer a pin question in this run.
var ErrNoPrompter = errors.New("no prompter")

// The tools and the rules of the pin rows and the handle rows of approval_log.
const (
	toolSecretPin    = "exec_secret_pin"
	toolSecretHandle = "exec_secret_handle"
)

// AskPin asks the owner to allow one program for a bound secret (req.PinName), or to allow the
// printed credentials of req.HandleNames after the run read outside content. The answers are
// allow and deny. It writes one log row. It never reads or writes a grant.
func (g *Gate) AskPin(ctx context.Context, req ApprovalRequest, p Prompter) (bool, error) {
	if req.PinName != "" {
		req.Tool, req.PatternID = toolSecretPin, toolSecretPin
		req.Description = "a bound secret for a program with no pin"
	} else {
		req.Tool, req.PatternID = toolSecretHandle, toolSecretHandle
		req.Description = "a printed credential after outside content"
	}
	req.NoAlways, req.NoSession = true, true
	if p == nil {
		g.record(ctx, req, DecisionBlocked, sourceNoPrompter, "")
		return false, ErrNoPrompter
	}
	if req.Member {
		g.record(ctx, req, DecisionBlocked, sourceMember, "")
		return false, nil
	}
	scope, err := g.prompt(ctx, req, p)
	switch {
	case ctx.Err() != nil:
		g.record(ctx, req, DecisionDenied, sourceStopped, "")
		return false, ctx.Err()
	case scope == ScopeTimeout || errors.Is(err, context.DeadlineExceeded):
		g.record(ctx, req, DecisionDenied, sourceTimeout, "")
		return false, nil
	case err != nil:
		g.record(ctx, req, DecisionDenied, sourcePromptError, "")
		return false, nil
	}
	if scope != ScopeOnce && scope != ScopeSession && scope != ScopeAlways {
		g.record(ctx, req, DecisionDenied, sourceOwner, "")
		return false, nil
	}
	// The owner can change the mode while the prompt waits. Read it again before the secret goes out.
	if g.ModeOf(ctx, req.Scope) == ModeReadOnly {
		g.record(ctx, req, DecisionBlocked, sourceModeReadOnly, "")
		return false, nil
	}
	g.record(ctx, req, DecisionAllowed, sourceOwner, string(ScopeOnce))
	return true, nil
}

// RecordDecision writes one row of approval_log for a decision that the gate did not make.
func (g *Gate) RecordDecision(ctx context.Context, req ApprovalRequest, decision, source, answer string) {
	g.record(ctx, req, decision, source, answer)
}
