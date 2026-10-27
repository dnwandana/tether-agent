package security

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
)

// This file is a port of the approval flow in Hermes tools/approval.py
// (check_all_command_guards).

// Scope is the answer of the user to an approval question.
type Scope string

// The answers to an approval question.
const (
	// ScopeOnce allows this command only.
	ScopeOnce Scope = "once"
	// ScopeSession allows the same rule for the rest of the session.
	ScopeSession Scope = "session"
	// ScopeAlways allows the same rule from now on. state.db keeps it.
	ScopeAlways Scope = "always"
	// ScopeDeny refuses the command.
	ScopeDeny Scope = "deny"
	// ScopeTimeout means that the user did not answer in time.
	ScopeTimeout Scope = "timeout"
)

// DefaultApprovalTimeout is the time that the gate waits for an answer when
// Gate.Timeout is zero. The config key is approvals.timeout_s.
const DefaultApprovalTimeout = 600 * time.Second

// ApprovalRequest is one command that the gate checks. Kind is the kind of
// run, for example "chat", "cron", or "subagent". RootKind is the kind of the
// top-level run of a child run. The gate sets PatternID and Description
// before it asks the Prompter.
type ApprovalRequest struct {
	SessionID   string
	SessionKey  string
	Kind        string
	RootKind    string
	ChildName   string
	Command     string
	PatternID   string
	Description string
}

// Prompter asks the user about one dangerous command. It is the UI of a
// channel. It must return when ctx ends.
type Prompter interface {
	PromptApproval(ctx context.Context, req ApprovalRequest) (Scope, error)
}

// Judge decides if a dangerous command is risky. Smart mode asks the user
// only about a risky command.
type Judge interface {
	Judge(ctx context.Context, command, description string) (risky bool, err error)
}

// BlockedError tells the model why the gate did not let a command run.
type BlockedError struct {
	Reason string
}

// Error returns the reason.
func (e *BlockedError) Error() string { return e.Reason }

func blocked(format string, args ...any) error {
	return &BlockedError{Reason: fmt.Sprintf(format, args...)}
}

// noConsent is the Hermes text that stops the model from a retry of a
// refused command in a different form.
const noConsent = "The user has NOT consented to this action. Do NOT retry this command, " +
	"do NOT rephrase it, and do NOT attempt the same outcome via a different command. " +
	"Stop the current workflow and wait for the user to respond before taking any " +
	"further destructive or irreversible action."

// Gate is the approval gate for shell commands. Mode is "manual", "smart",
// or "off". CronMode is "deny" or "approve". A zero Timeout uses
// DefaultApprovalTimeout. A nil DB keeps "Always" answers for the session
// only. A nil Judge makes smart mode ask the user. Log is optional.
type Gate struct {
	Mode            string
	CronMode        string
	Timeout         time.Duration
	DB              *db.DB
	Judge           Judge
	SudoPasswordSet bool
	Log             *slog.Logger

	mu       sync.Mutex
	sessions map[string]map[string]bool
}

// NewGate returns a gate with the settings of the approvals config block.
func NewGate(cfg config.ApprovalsConfig, d *db.DB) *Gate {
	return &Gate{
		Mode:     cfg.Mode,
		CronMode: cfg.CronMode,
		Timeout:  time.Duration(cfg.TimeoutS) * time.Second,
		DB:       d,
	}
}

// Check returns nil when the command may run. Otherwise it returns a
// *BlockedError with the text for the model. The order is the hardline
// list, the sudo guard, then the dangerous rules. No mode and no
// earlier answer can skip the first two steps.
func (g *Gate) Check(ctx context.Context, req ApprovalRequest, p Prompter) error {
	if pat, ok := MatchHardline(req.Command); ok {
		return blocked("BLOCKED (hardline): %s. This command is on the unconditional blocklist. "+
			"No approval mode, cron setting, or earlier answer can allow it. "+
			"If you really need it, the user must run it in a terminal outside the agent.", pat.Description)
	}
	if MatchSudoStdin(req.Command, g.SudoPasswordSet) {
		return blocked("BLOCKED: sudo password guessing via stdin (sudo -S). " +
			"Do not pipe passwords to 'sudo -S'. Set SUDO_PASSWORD in the .env file " +
			"if the agent needs sudo, or run the sudo command in your own terminal.")
	}
	pat, ok := MatchDangerous(req.Command)
	if !ok {
		return nil
	}
	req.PatternID, req.Description = pat.ID, pat.Description

	// Hermes: approvals.mode=off runs every dangerous command, also in cron.
	if g.Mode == "off" {
		return nil
	}
	if g.approved(ctx, sessionOf(req), pat.ID) {
		return nil
	}
	if isCron(req) {
		if cronApproves(g.CronMode) {
			return nil
		}
		return blocked("BLOCKED: Command flagged as dangerous (%s) but cron jobs run without a user "+
			"present to approve it. Find an alternative approach that avoids this command. "+
			"To allow dangerous commands in cron jobs, set approvals.cron_mode: approve in config.yaml.",
			pat.Description)
	}
	if g.Mode == "smart" && g.Judge != nil {
		risky, err := g.Judge.Judge(ctx, req.Command, pat.Description)
		if err != nil {
			g.logf("approval: the smart judge failed, so the gate asks the user", "pattern", pat.ID, "err", err)
		}
		if err == nil && !risky {
			// Hermes gives a smart approval the session scope.
			g.approveSession(sessionOf(req), pat.ID)
			return nil
		}
	}
	return g.ask(ctx, req, p)
}

// ask puts the question to the user and applies the answer.
func (g *Gate) ask(ctx context.Context, req ApprovalRequest, p Prompter) error {
	if p == nil {
		return blocked("BLOCKED: Command flagged as dangerous (%s), and no user is present to approve it. "+
			"Find an alternative approach that avoids this command.", req.Description)
	}
	scope, err := g.prompt(ctx, req, p)
	switch {
	case ctx.Err() != nil:
		return blocked("BLOCKED: the approval request for %s stopped (%v). Do NOT retry.", req.Description, ctx.Err())
	case scope == ScopeTimeout || errors.Is(err, context.DeadlineExceeded):
		return blocked("BLOCKED: approval denied (timeout): %s. The user did not answer in time. %s "+
			"Silence is not consent.", req.Description, noConsent)
	case err != nil:
		return blocked("BLOCKED: Failed to send the approval request to the user (%v). Do NOT retry.", err)
	}
	switch scope {
	case ScopeOnce:
		return nil
	case ScopeSession:
		g.approveSession(sessionOf(req), req.PatternID)
		return nil
	case ScopeAlways:
		g.approveSession(sessionOf(req), req.PatternID)
		if err := g.saveAlways(ctx, req.PatternID); err != nil {
			g.logf("approval: cannot save the Always answer, so it holds for this session only",
				"pattern", req.PatternID, "err", err)
		}
		return nil
	default:
		return blocked("BLOCKED by user: %s. %s", req.Description, noConsent)
	}
}

// prompt calls p under the gate timeout. It also stops at the timeout when
// p does not obey its context.
func (g *Gate) prompt(ctx context.Context, req ApprovalRequest, p Prompter) (Scope, error) {
	pctx, cancel := context.WithTimeout(ctx, g.timeout())
	defer cancel()
	type answer struct {
		scope Scope
		err   error
	}
	ch := make(chan answer, 1)
	go func() {
		s, err := p.PromptApproval(pctx, req)
		ch <- answer{s, err}
	}()
	select {
	case a := <-ch:
		if a.err != nil && pctx.Err() != nil {
			return ScopeTimeout, pctx.Err()
		}
		return a.scope, a.err
	case <-pctx.Done():
		return ScopeTimeout, pctx.Err()
	}
}

// ResetSession removes the "This session" answers of sessionID.
func (g *Gate) ResetSession(sessionID string) {
	g.mu.Lock()
	delete(g.sessions, sessionID)
	g.mu.Unlock()
}

func (g *Gate) timeout() time.Duration {
	if g.Timeout <= 0 {
		return DefaultApprovalTimeout
	}
	return g.Timeout
}

// sessionOf returns the key for the session answers of req.
func sessionOf(req ApprovalRequest) string {
	if req.SessionID != "" {
		return req.SessionID
	}
	return req.SessionKey
}

func (g *Gate) approveSession(session, patternID string) {
	if session == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sessions == nil {
		g.sessions = map[string]map[string]bool{}
	}
	if g.sessions[session] == nil {
		g.sessions[session] = map[string]bool{}
	}
	g.sessions[session][patternID] = true
}

// approved reports whether the session or an "Always" answer allows the
// rule. A database error counts as "not approved", so the gate asks.
func (g *Gate) approved(ctx context.Context, session, patternID string) bool {
	g.mu.Lock()
	ok := session != "" && g.sessions[session][patternID]
	g.mu.Unlock()
	if ok || g.DB == nil {
		return ok
	}
	var n int
	err := g.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM approvals_always WHERE pattern_id=?", patternID).Scan(&n)
	if err != nil {
		g.logf("approval: cannot read approvals_always", "pattern", patternID, "err", err)
		return false
	}
	return n > 0
}

// saveAlways stores an "Always" answer in approvals_always.
func (g *Gate) saveAlways(ctx context.Context, patternID string) error {
	if g.DB == nil {
		return errors.New("no state.db")
	}
	_, err := g.DB.ExecContext(ctx,
		"INSERT INTO approvals_always(pattern_id, created_at) VALUES (?, ?) ON CONFLICT(pattern_id) DO NOTHING",
		patternID, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("approval: save Always answer for %s: %w", patternID, err)
	}
	return nil
}

func (g *Gate) logf(msg string, args ...any) {
	if g.Log != nil {
		g.Log.Warn(msg, args...)
	}
}

// isCron reports whether req comes from a cron run or from a child of a cron
// run. No user is present in either run. Hermes sets HERMES_CRON_SESSION for
// the full process, so the children of a cron job also follow cron_mode.
func isCron(req ApprovalRequest) bool {
	return req.Kind == "cron" || req.RootKind == "cron"
}

// cronApproves reads approvals.cron_mode like Hermes _get_cron_approval_mode:
// "approve", "off", "allow", and "yes" run dangerous commands. All other
// values deny them.
func cronApproves(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "approve", "off", "allow", "yes":
		return true
	}
	return false
}
