package security

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

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

// ScriptPatternID is the synthetic rule ID of a script that execute_code runs.
const ScriptPatternID = "execute_code"

// scriptDescription is the description of a script that matches no literal rule.
const scriptDescription = "run a Python script on the host"

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
	// Summary is one short sentence that tells what the command does. It comes
	// from the description argument of the model and is not trusted. The gate
	// does not read it. The UI shows it next to the command.
	Summary string
	// NoAlways hides the "always" answer. A UI that gets "always" for such a request
	// applies "this session". A permission request of a coding agent sets it.
	NoAlways bool
	// NoSession hides the "this session" answer too. A UI applies "once" for a
	// "this session" or an "always" answer. A request whose rule does not say what runs sets it.
	NoSession bool
	// Member is true for a request of a member turn. The zero value means owner.
	// The gate then reads no earlier answer and saves no grant.
	Member bool
	// Workdir is the resolved workdir of the command. "" means no base folder, and such a
	// request gets no Always answer.
	Workdir string
	// Path is the PATH value of the child environment of the command.
	Path string
	// Scope is the /mode scope of the chat or the topic. "" for a cron run.
	Scope string
	// LogScope is the scope of the log row. A cron run sets it from the origin chat of the job.
	LogScope string
	// Via is the wrapper text of the command, for example "sudo" or "ssh prod".
	Via string
	// GrantKey is the session grant key. The gate sets it.
	GrantKey string
	// Tool is terminal, execute_code, mcp:<server>.<tool>, or acp:<agent>.
	Tool string
	// Server and ToolName are the MCP server and the raw MCP tool name.
	Server, ToolName string
	// Class is the MCP class. "" for other tools.
	Class string
	// HumanOnly and Unread are set by the gate and read by the UIs.
	HumanOnly, Unread bool
}

// Prompter asks the user about one dangerous command. It is the UI of a
// channel. It must return when ctx ends.
type Prompter interface {
	PromptApproval(ctx context.Context, req ApprovalRequest) (Scope, error)
}

// Judge decides if a dangerous command is risky. Smart mode asks the user
// only about a risky command. sessionID is the session of the command. A judge
// that makes a model call records the usage in that session.
type Judge interface {
	Judge(ctx context.Context, command, description, sessionID string) (risky bool, err error)
}

// ScriptJudge is an optional interface of Gate.Judge. It decides if a script
// is risky. Without it, smart mode sends a script to the user.
type ScriptJudge interface {
	JudgeScript(ctx context.Context, script, description, sessionID string) (risky bool, err error)
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

// GatewaySelfBlocked is the text for the model when a command would stop the
// gateway that runs it.
const GatewaySelfBlocked = "BLOCKED (gateway): this command stops or restarts the gateway that runs it. " +
	"The stop waits for this turn, so the command cannot finish. " +
	"Ask the user to send /restart in the chat, or to run \"systemctl --user restart tether\" from a separate shell."

// Gate is the approval gate for shell commands. Mode is "manual", "smart",
// or "off". CronMode is "deny" or "approve". A zero Timeout uses
// DefaultApprovalTimeout. A nil DB keeps "Always" answers for the session
// only. A nil Judge makes smart mode ask the user. Log is optional.
type Gate struct {
	Mode     string
	CronMode string
	Timeout  time.Duration
	DB       *db.DB
	Judge    Judge
	// SmartScriptMaxChars is the longest script that goes to the judge. A longer
	// script goes to the user. 0 uses the const SmartScriptMaxChars.
	SmartScriptMaxChars int
	SudoPasswordSet     bool
	// InGateway is true in the gateway process. The gate then blocks a command
	// that stops or restarts the gateway, except in a run that cron started.
	InGateway bool
	// ScriptsDir is the scripts folder of the data folder. The cron block text
	// of CheckScript names it. An empty value gives a general text.
	ScriptsDir string
	Log        *slog.Logger
	// Redactor masks the secrets in the previews of the gate. A command that it changes holds
	// a credential and gets no grant. A nil Redactor masks nothing.
	Redactor *Redactor

	grantsOff atomic.Bool
	offCause  string // under mu

	mu       sync.Mutex
	sessions map[string]map[string]bool
}

// GrantsOff reports whether the gate turned off all saved answers, because the grant store is
// damaged. cause is the first error. A restart clears it.
func (g *Gate) GrantsOff() (off bool, cause string) {
	if !g.grantsOff.Load() {
		return false, ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return true, g.offCause
}

// setGrantsOff turns off all saved answers. It keeps the first cause and logs it one time.
func (g *Gate) setGrantsOff(cause string) {
	g.mu.Lock()
	first := !g.grantsOff.Load()
	if first {
		g.offCause = cause
		g.grantsOff.Store(true)
	}
	g.mu.Unlock()
	if first {
		g.logf("approval: the grant store is damaged, so the gate asks each time and saves no answer", "cause", cause)
	}
}

// grantStoreDamaged reports whether err shows a damaged grant store. A stop, a timeout, and a
// busy database are not damage.
func grantStoreDamaged(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	text := err.Error()
	if strings.Contains(text, "SQLITE_BUSY") || strings.Contains(text, "database is locked") {
		return false
	}
	for _, s := range []string{"no such table", "no such column", "sql: Scan error"} {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}

// grantKey returns the session grant key of a rule: the rule ID, plus the wrapper text.
func grantKey(patternID, via string) string {
	if via == "" {
		return patternID
	}
	return patternID + " via " + via
}

// scriptGrantKey returns the session grant key of a script: the SHA-256 hex of the exact bytes.
func scriptGrantKey(code string) string {
	sum := sha256.Sum256([]byte(code))
	return ScriptPatternID + ":" + hex.EncodeToString(sum[:])
}

// shellExpansion matches a variable expansion in a command.
var shellExpansion = regexp.MustCompile(`\$\{[^}]*\}|\$[A-Za-z_][A-Za-z0-9_]*`)

// hasCredential reports whether the redactor masks a part of the command. A variable expansion
// is not a secret, so it does not count.
func (g *Gate) hasCredential(command string) bool {
	if g.Redactor == nil {
		return false
	}
	text := shellExpansion.ReplaceAllString(command, "***")
	if g.Redactor.redactExceptFlags(text) != text {
		return true
	}
	// The flag form counts only in the command view, which drops echo text and comments.
	view := ReadCommand(command).View
	view = shellExpansion.ReplaceAllString(view, "***")
	return redactFlags(view) != view
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

// CouldNotReadPatternID is the synthetic rule of a command that the gate could not read.
const CouldNotReadPatternID = "could_not_read"

// blockSentence ends each block text of the gate.
const blockSentence = "Do not retry it with a different spelling or another tool. Tell the owner what you tried to run."

// writeFileHint is the hint for a command that the gate could not read.
const writeFileHint = "Use write_file for large content."

// endBlock returns a *BlockedError. The text is the formatted message, then writeFileHint when
// req.Unread, then blockSentence.
func endBlock(req ApprovalRequest, format string, args ...any) error {
	text := fmt.Sprintf(format, args...)
	if req.Unread {
		text += " " + writeFileHint
	}
	return &BlockedError{Reason: text + " " + blockSentence}
}

// humanOnlyBlocked returns the block text of a human-only request that no owner can answer.
func humanOnlyBlocked(req ApprovalRequest) error {
	return endBlock(req, "BLOCKED (human-only): %s. This action needs the owner to tap Approve each time, "+
		"and no owner can answer here.", req.Description)
}

// liveMode returns the mode that applies now, and the log source for the mode "off". A topic
// mode of full counts as off. Any other topic mode follows approvals.mode.
func (g *Gate) liveMode(ctx context.Context, scope string) (mode, source string) {
	if g.ModeOf(ctx, scope) == ModeFull {
		return "off", sourceModeFull
	}
	return g.Mode, sourceModeOff
}

// humanOnlyCommand returns the first human-only rule of a command: a built-in rule, else the first
// dangerous rule that the raise list names.
func humanOnlyCommand(raw string, p Parsed, pats []Pattern) (Pattern, bool) {
	if pat, ok := MatchHumanOnlyParsed(raw, p); ok {
		return pat, true
	}
	for _, pat := range pats {
		if IsHumanOnlyRaised(pat.ID) {
			return pat, true
		}
	}
	return Pattern{}, false
}

// Check returns nil when the command may run. Otherwise it returns a
// *BlockedError with the text for the model. The order is the hardline
// list, the sudo guard, the gateway self-kill rules, the human-only rules, the
// "could not read" step, the live mode, the grants, cron, the smart judge, and the question to
// the owner. No mode and no earlier answer can skip the floor or lower a human-only rule. A
// human-only request and a request that the gate could not read ask each time, with no grant, no
// saved answer, and no judge. A state.db rule asks each time too. Else the gate checks the grant
// of each protected rule that matches and of the first general rule. Thus a
// grant for a protected path in one part of a compound command cannot cover a
// different danger in another part. The gate asks about the first rule that
// has no grant. A protected rule never saves an "Always" answer and never
// reads one, because an older version saved such rows. A member request reads
// no earlier answer and saves none. A protected rule never goes to the smart
// judge. The gate asks the user.
func (g *Gate) Check(ctx context.Context, req ApprovalRequest, p Prompter) error {
	parsed := ReadCommand(req.Command)
	req.Via = parsed.ViaKey()
	if rule, _, err := g.floorRule(req, g.SudoPasswordSet, parsed); err != nil {
		req.PatternID = rule
		g.record(ctx, req, DecisionBlocked, sourceHardline, "")
		return err
	}
	pats := MatchAllDangerousParsed(req.Command, parsed)
	if pat, ok := humanOnlyCommand(req.Command, parsed, pats); ok {
		req.PatternID, req.Description = pat.ID, pat.Description
		req.HumanOnly, req.NoSession, req.NoAlways = true, true, true
		return g.humanOnlyAsk(ctx, req, p)
	}
	if len(pats) == 0 && parsed.Read {
		return nil
	}
	if !parsed.Read {
		req.Unread, req.NoSession, req.NoAlways = true, true, true
		if len(pats) == 0 {
			req.PatternID = CouldNotReadPatternID
			req.Description = "could not read the command: " + parsed.Why
		} else {
			req.PatternID = pats[0].ID
			req.Description = pats[0].Description + " (the gate could not read the full command: " + parsed.Why + ")"
		}
	}
	// Hermes: approvals.mode=off runs every dangerous command, also in cron.
	mode, offSource := g.liveMode(ctx, req.Scope)
	if mode == "off" {
		if parsed.Read {
			req.PatternID = pats[0].ID
		}
		g.record(ctx, req, DecisionAllowed, offSource, "")
		return nil
	}
	var bind *alwaysBind
	pat := Pattern{ID: req.PatternID, Description: req.Description}
	if parsed.Read {
		if !req.Member {
			if b, ok := g.bindAlways(req, parsed); ok {
				bind = &b
			}
		}
		credential := g.hasCredential(req.Command)
		pat = pats[0]
		stateDB := IsStateDBPatternID(pat.ID)
		if !stateDB && !req.Member && !credential {
			var ok bool
			if pat, ok = g.firstUngranted(ctx, sessionOf(req), pats, req.Via, bind); !ok {
				req.PatternID = pats[0].ID
				g.record(ctx, req, DecisionAllowed, sourceGrant, "")
				return nil
			}
		}
		req.PatternID, req.Description = pat.ID, pat.Description
		req.GrantKey = grantKey(pat.ID, req.Via)
		if credential || g.grantsOff.Load() {
			req.NoSession, req.NoAlways = true, true
		}
		if IsProtectedPatternID(pat.ID) || bind == nil {
			req.NoAlways = true
		}
		if stateDB {
			req.NoSession = true
		}
		if req.Member {
			req.NoSession, req.NoAlways = true, true
		}
	}

	if isCron(req) {
		if req.Unread {
			g.record(ctx, req, DecisionBlocked, sourceCouldNotRead, "")
			return endBlock(req, "BLOCKED: %s. A cron job has no user present to approve a command that the gate cannot read, "+
				"also with approvals.cron_mode: approve.", req.Description)
		}
		if cronApproves(g.CronMode) {
			g.record(ctx, req, DecisionAllowed, sourceCronMode, "")
			return nil
		}
		g.record(ctx, req, DecisionBlocked, sourceCronMode, "")
		return endBlock(req, "BLOCKED: Command flagged as dangerous (%s) but cron jobs run without a user "+
			"present to approve it. Find an alternative approach that avoids this command. "+
			"To allow dangerous commands in cron jobs, set approvals.cron_mode: approve in config.yaml.",
			pat.Description)
	}
	if mode == "smart" && g.Judge != nil && !IsProtectedPatternID(pat.ID) && !req.Unread {
		risky, err := g.Judge.Judge(ctx, req.Command, pat.Description, req.SessionID)
		if err != nil {
			g.logf("approval: the smart judge failed, so the gate asks the user", "pattern", pat.ID, "err", err)
		}
		if err == nil && !risky {
			// A smart approval covers this command only, as in Hermes. A pattern
			// grant would let one safe command skip the review of later commands.
			g.record(ctx, req, DecisionAllowed, sourceSmart, "")
			return nil
		}
	}
	return g.ask(ctx, req, p, bind)
}

// Floor runs the checks that no mode and no earlier answer can skip: the
// hardline list, the sudo guard, and the gateway self-stop rules.
func (g *Gate) Floor(req ApprovalRequest) error {
	_, err := g.FloorLabel(req, g.SudoPasswordSet)
	return err
}

// FloorLabel runs the same checks as Floor. Returns the short label for a progress line and a
// log line. The ACP bridge calls it with sudoPasswordSet false, because the adapter does not
// get SUDO_PASSWORD.
func (g *Gate) FloorLabel(req ApprovalRequest, sudoPasswordSet bool) (label string, err error) {
	_, label, err = g.floorMatch(req, sudoPasswordSet)
	return label, err
}

// floorRule is floorMatch with a command that the caller already read.
func (g *Gate) floorRule(req ApprovalRequest, sudoPasswordSet bool, p Parsed) (rule, label string, err error) {
	if pat, ok := MatchHardlineParsed(req.Command, p); ok {
		return pat.ID, "hardline: " + pat.Description, endBlock(req, "BLOCKED (hardline): %s. This command is on the unconditional blocklist. "+
			"No approval mode, cron setting, or earlier answer can allow it. "+
			"If you really need it, the user must run it in a terminal outside the agent.", pat.Description)
	}
	if MatchSudoStdin(req.Command, sudoPasswordSet) {
		return sudoStdinRule, "sudo -S", blocked("BLOCKED: sudo password guessing via stdin (sudo -S). " +
			"Do not pipe passwords to 'sudo -S'. Set SUDO_PASSWORD in the .env file " +
			"if the agent needs sudo, or run the sudo command in your own terminal.")
	}
	if g.InGateway && !isCron(req) {
		if pat, ok := MatchGatewaySelf(req.Command); ok {
			return pat.ID, "gateway self-stop", blocked("%s", GatewaySelfBlocked)
		}
	}
	return "", "", nil
}

// CheckScript returns nil when the script in req.Command may run. It asks one
// time for each script with the rule ScriptPatternID. A literal match of a
// danger rule makes the gate ask each time. A human-only match asks each time in each mode. In a
// cron run, each script follows approvals.cron_mode. A script never reads or writes
// approval_grants. A member request reads no earlier answer and saves none. A script is Python,
// so the gate does not read it as a shell command.
func (g *Gate) CheckScript(ctx context.Context, req ApprovalRequest, p Prompter) error {
	if rule, _, err := g.floorRule(req, g.SudoPasswordSet, Parsed{}); err != nil {
		req.PatternID = rule
		g.record(ctx, req, DecisionBlocked, sourceHardline, "")
		return err
	}
	pats := MatchAllDangerousParsed(req.Command, Parsed{})
	req.PatternID, req.Description, req.NoAlways = ScriptPatternID, scriptDescription, true
	if pat, ok := humanOnlyScript(req.Command, pats); ok {
		req.PatternID = pat.ID
		req.Description = scriptDescription + " (" + pat.Description + ")"
		req.HumanOnly, req.NoSession = true, true
		return g.humanOnlyAsk(ctx, req, p)
	}
	mode, offSource := g.liveMode(ctx, req.Scope)
	if mode == "off" {
		g.record(ctx, req, DecisionAllowed, offSource, "")
		return nil
	}
	var pat Pattern
	literal := len(pats) > 0
	if literal {
		pat = pats[0]
		req.NoSession = true
		req.Description = scriptDescription + " (" + pat.Description + ")"
	} else {
		req.GrantKey = scriptGrantKey(req.Command)
		if !req.Member && g.sessionApproved(sessionOf(req), req.GrantKey) {
			g.record(ctx, req, DecisionAllowed, sourceGrant, "")
			return nil
		}
	}
	if req.Member || g.grantsOff.Load() {
		req.NoSession = true
	}
	if isCron(req) {
		if cronApproves(g.CronMode) {
			g.record(ctx, req, DecisionAllowed, sourceCronMode, "")
			return nil
		}
		g.record(ctx, req, DecisionBlocked, sourceCronMode, "")
		if literal {
			return endBlock(req, "BLOCKED: Command flagged as dangerous (%s) but cron jobs run without a user "+
				"present to approve it. Find an alternative approach that avoids this command. "+
				"To allow dangerous commands in cron jobs, set approvals.cron_mode: approve in config.yaml.",
				pat.Description)
		}
		return blocked("%s", cronScriptBlocked(g.ScriptsDir))
	}
	// A script with a protected literal match never goes to the judge, the same as a command.
	protected := literal && IsProtectedPatternID(pat.ID)
	if sj, ok := g.Judge.(ScriptJudge); ok && mode == "smart" && !protected &&
		utf8.RuneCountInString(req.Command) <= g.scriptMax() {
		risky, err := sj.JudgeScript(ctx, req.Command, req.Description, req.SessionID)
		if err != nil {
			g.logf("approval: the smart judge failed, so the gate asks the user", "pattern", ScriptPatternID, "err", err)
		}
		if err == nil && !risky {
			g.record(ctx, req, DecisionAllowed, sourceSmart, "")
			return nil
		}
	}
	return g.ask(ctx, req, p, nil)
}

// humanOnlyAsk handles a human-only request: only a present owner can answer it.
func (g *Gate) humanOnlyAsk(ctx context.Context, req ApprovalRequest, p Prompter) error {
	source := ""
	switch {
	case isCron(req):
		source = sourceHumanOnly
	case req.Member:
		source = sourceMember
	case p == nil:
		source = sourceNoPrompter
	default:
		return g.ask(ctx, req, p, nil)
	}
	g.record(ctx, req, DecisionBlocked, source, "")
	return humanOnlyBlocked(req)
}

// humanOnlyScript returns the first human-only rule of a script: a built-in script rule, else
// the first literal rule that the raise list names.
func humanOnlyScript(code string, pats []Pattern) (Pattern, bool) {
	if pat, ok := MatchHumanOnlyScript(code); ok {
		return pat, true
	}
	for _, pat := range pats {
		if IsHumanOnlyRaised(pat.ID) {
			return pat, true
		}
	}
	return Pattern{}, false
}

// cronScriptBlocked returns the block text for a script with no literal match
// in a cron run. It names the scripts folder when scriptsDir is set.
func cronScriptBlocked(scriptsDir string) string {
	where := "the scripts folder of the data folder"
	if scriptsDir != "" {
		where = scriptsDir + "/"
	}
	return "BLOCKED: execute_code runs Python on the host, and cron jobs run without a user present to approve it. " +
		"Ask the user to move the code to a cron script in " + where + ", or to set approvals.cron_mode: approve in config.yaml. " +
		blockSentence
}

// scriptMax returns the longest script, in runes, that goes to the judge.
func (g *Gate) scriptMax() int {
	if g.SmartScriptMaxChars > 0 {
		return g.SmartScriptMaxChars
	}
	return SmartScriptMaxChars
}

// ask puts the question to the user and applies the answer.
func (g *Gate) ask(ctx context.Context, req ApprovalRequest, p Prompter, b *alwaysBind) error {
	if p == nil {
		g.record(ctx, req, DecisionBlocked, sourceNoPrompter, "")
		return blocked("BLOCKED: Command flagged as dangerous (%s), and no user is present to approve it. "+
			"Find an alternative approach that avoids this command.", req.Description)
	}
	scope, err := g.prompt(ctx, req, p)
	switch {
	case ctx.Err() != nil:
		g.record(ctx, req, DecisionDenied, sourceStopped, "")
		return blocked("BLOCKED: the approval request for %s stopped (%v). Do NOT retry.", req.Description, ctx.Err())
	case scope == ScopeTimeout || errors.Is(err, context.DeadlineExceeded):
		g.record(ctx, req, DecisionDenied, sourceTimeout, "")
		return blocked("BLOCKED: approval denied (timeout): %s. The user did not answer in time. %s "+
			"Silence is not consent.", req.Description, noConsent)
	case err != nil:
		g.record(ctx, req, DecisionDenied, sourcePromptError, "")
		return blocked("BLOCKED: Failed to send the approval request to the user (%v). Do NOT retry.", err)
	}
	if scope == ScopeOnce || scope == ScopeSession || scope == ScopeAlways {
		// The owner can change the mode while the prompt waits. Read it again before the
		// command runs and before the gate saves a grant.
		if g.ModeOf(ctx, req.Scope) == ModeReadOnly {
			g.record(ctx, req, DecisionBlocked, sourceModeReadOnly, "")
			return blocked("BLOCKED: The mode of this topic changed to read-only while the approval waited.")
		}
	}
	switch scope {
	case ScopeOnce:
		g.record(ctx, req, DecisionAllowed, sourceOwner, string(ScopeOnce))
		return nil
	case ScopeSession:
		applied := ScopeOnce
		if !req.NoSession {
			g.approveSession(sessionOf(req), sessionKey(req))
			applied = ScopeSession
		}
		g.record(ctx, req, DecisionAllowed, sourceOwner, string(applied))
		return nil
	case ScopeAlways:
		if req.NoSession {
			g.record(ctx, req, DecisionAllowed, sourceOwner, string(ScopeOnce))
			return nil
		}
		g.approveSession(sessionOf(req), sessionKey(req))
		applied := ScopeSession
		if !req.NoAlways && b != nil {
			if err := g.saveAlways(ctx, req.PatternID, *b); err != nil {
				g.logf("approval: cannot save the Always answer, so it holds for this session only",
					"pattern", req.PatternID, "err", err)
			} else {
				applied = ScopeAlways
			}
		}
		g.record(ctx, req, DecisionAllowed, sourceOwner, string(applied))
		return nil
	default:
		g.record(ctx, req, DecisionDenied, sourceOwner, "")
		return endBlock(req, "BLOCKED by user: %s. %s", req.Description, noConsent)
	}
}

// sessionKey returns the key of the session grant of req.
func sessionKey(req ApprovalRequest) string {
	if req.GrantKey != "" {
		return req.GrantKey
	}
	return req.PatternID
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

// AskACP asks the user about one permission request of a coding agent. It
// does not use the danger patterns, approvals.mode, smart mode, the Judge, or
// approvals.cron_mode. It asks each time, except when the session memory of
// the gate has a "this session" answer for req.PatternID. It never reads or
// writes approval_grants. The returns are:
//   - once or session: (ScopeOnce or ScopeSession, nil).
//   - deny: (ScopeDeny, nil).
//   - no answer in the gate timeout: (ScopeTimeout, nil).
//   - the end of ctx: (ScopeTimeout, ctx.Err()).
//   - an error of p: (ScopeDeny, err).
//   - a nil p: (ScopeDeny, nil).
//
// An "always" answer becomes ScopeSession. With req.NoSession, a "this
// session" or "always" answer becomes ScopeOnce, and the gate saves nothing. A
// member request reads no earlier answer and saves none.
func (g *Gate) AskACP(ctx context.Context, req ApprovalRequest, p Prompter) (Scope, error) {
	if p == nil {
		g.record(ctx, req, DecisionBlocked, sourceNoPrompter, "")
		return ScopeDeny, nil
	}
	req.NoAlways = true
	req.GrantKey = req.PatternID
	if req.Member || g.grantsOff.Load() {
		req.NoSession = true
	}
	session := sessionOf(req)
	if !req.NoSession && g.sessionApproved(session, req.PatternID) {
		g.record(ctx, req, DecisionAllowed, sourceGrant, "")
		return ScopeSession, nil
	}
	scope, err := g.prompt(ctx, req, p)
	switch {
	case ctx.Err() != nil:
		g.record(ctx, req, DecisionDenied, sourceStopped, "")
		return ScopeTimeout, ctx.Err()
	case scope == ScopeTimeout || errors.Is(err, context.DeadlineExceeded):
		g.record(ctx, req, DecisionDenied, sourceTimeout, "")
		return ScopeTimeout, nil
	case err != nil:
		g.record(ctx, req, DecisionDenied, sourcePromptError, "")
		return ScopeDeny, err
	}
	if scope == ScopeOnce || scope == ScopeSession || scope == ScopeAlways {
		// The owner can change the mode while the prompt waits. Read it again before the
		// coding agent gets the permission and before the gate saves an answer.
		if g.ModeOf(ctx, req.Scope) == ModeReadOnly {
			g.record(ctx, req, DecisionBlocked, sourceModeReadOnly, "")
			return ScopeDeny, nil
		}
	}
	switch scope {
	case ScopeOnce:
		g.record(ctx, req, DecisionAllowed, sourceOwner, string(ScopeOnce))
		return ScopeOnce, nil
	case ScopeSession, ScopeAlways:
		if req.NoSession {
			g.record(ctx, req, DecisionAllowed, sourceOwner, string(ScopeOnce))
			return ScopeOnce, nil
		}
		g.approveSession(session, req.PatternID)
		g.record(ctx, req, DecisionAllowed, sourceOwner, string(ScopeSession))
		return ScopeSession, nil
	default:
		g.record(ctx, req, DecisionDenied, sourceOwner, "")
		return ScopeDeny, nil
	}
}

// sessionApproved reports whether the session memory has a "this session"
// answer for the rule. Unlike granted, it does not read approval_grants.
func (g *Gate) sessionApproved(session, patternID string) bool {
	if session == "" || g.grantsOff.Load() {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sessions[session][patternID]
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

// firstUngranted returns the first rule in pats that has no grant. It checks
// each protected rule and the first general rule. Like Hermes, it ignores the
// general rules after the first. It returns false when each checked rule has a
// grant. pats must be in the order of MatchAllDangerous. via is the wrapper text of the
// command. A nil b means that no Always row can cover the request.
func (g *Gate) firstUngranted(ctx context.Context, session string, pats []Pattern, via string, b *alwaysBind) (Pattern, bool) {
	for _, pat := range pats {
		if !g.granted(ctx, session, pat.ID, via, b) {
			return pat, true
		}
		if !IsProtectedPatternID(pat.ID) {
			break
		}
	}
	return Pattern{}, false
}

// granted reports whether an earlier answer allows the rule. A state.db rule
// never has a grant. A protected rule reads only the session answers. A general
// rule reads the session answers and, with a bind, the Always rows.
func (g *Gate) granted(ctx context.Context, session, patternID, via string, b *alwaysBind) bool {
	key := grantKey(patternID, via)
	switch {
	case g.grantsOff.Load(), IsStateDBPatternID(patternID):
		return false
	case IsProtectedPatternID(patternID):
		return g.sessionApproved(session, key)
	}
	if g.sessionApproved(session, key) {
		return true
	}
	return b != nil && g.alwaysGranted(ctx, patternID, *b)
}

// alwaysBind is what an Always row binds: the command bytes, the workdir, the wrappers, and
// the real paths of the programs.
type alwaysBind struct {
	Content, Workdir, Via, Preview string
	Programs                       []string
}

// commandHash returns the SHA-256 hex of the command with the outer white space trimmed.
func commandHash(command string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(command)))
	return hex.EncodeToString(sum[:])
}

// grantID returns the row id of a grant: the SHA-256 hex of the four values joined with "\x00".
func grantID(patternID, content, workdir, via string) string {
	sum := sha256.Sum256([]byte(patternID + "\x00" + content + "\x00" + workdir + "\x00" + via))
	return hex.EncodeToString(sum[:])
}

// grantPreview returns the one-line text that /approvals shows for a command.
func (g *Gate) grantPreview(command string) string {
	if g.Redactor != nil {
		command = g.Redactor.Redact(command)
	}
	line := ShowHidden(strings.TrimSpace(command))
	line = strings.NewReplacer("\n", " ", "\r", " ").Replace(line)
	return cutRunes(line, 60)
}

// bindAlways returns the bind of an Always answer for the request. It returns false when the
// request is not eligible: no workdir, a command that the reader could not read, or a program
// that the gate cannot resolve.
func (g *Gate) bindAlways(req ApprovalRequest, p Parsed) (alwaysBind, bool) {
	if req.Workdir == "" || !p.Read || p.RuntimeText || g.hasCredential(req.Command) {
		return alwaysBind{}, false
	}
	programs, ok := ResolvePrograms(p, req.Workdir, req.Path)
	if !ok {
		return alwaysBind{}, false
	}
	return alwaysBind{
		Content:  commandHash(req.Command),
		Workdir:  req.Workdir,
		Via:      p.ViaKey(),
		Preview:  g.grantPreview(req.Command),
		Programs: programs,
	}, true
}

// programsJSON returns the JSON list of programs. A nil list gives "[]".
func programsJSON(programs []string) string {
	if programs == nil {
		programs = []string{}
	}
	out, err := json.Marshal(programs)
	if err != nil {
		return "[]"
	}
	return string(out)
}

// alwaysGranted reports whether approval_grants has a row for the rule and the bind, and the
// row has the same programs. A database error counts as "no grant", so the gate asks.
func (g *Gate) alwaysGranted(ctx context.Context, patternID string, b alwaysBind) bool {
	if g.DB == nil || g.grantsOff.Load() {
		return false
	}
	id := grantID(patternID, b.Content, b.Workdir, b.Via)
	var raw string
	err := g.DB.QueryRowContext(ctx, "SELECT programs FROM approval_grants WHERE id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		if grantStoreDamaged(err) {
			g.setGrantsOff(err.Error())
		} else {
			g.logf("approval: cannot read approval_grants", "pattern", patternID, "err", err)
		}
		return false
	}
	var stored []string
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		g.setGrantsOff("the programs value of the grant " + id + " is not a JSON list of strings")
		return false
	}
	return slices.Equal(stored, b.Programs)
}

// saveAlways stores an "Always" answer in approval_grants. A row with the same id gets the new
// programs, preview, and time.
func (g *Gate) saveAlways(ctx context.Context, patternID string, b alwaysBind) error {
	if g.DB == nil {
		return errors.New("no state.db")
	}
	_, err := g.DB.ExecContext(ctx, `INSERT INTO approval_grants(id, pattern_id, content_sha256, workdir, via,
		programs, preview, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET programs=excluded.programs, preview=excluded.preview,
		created_at=excluded.created_at`,
		grantID(patternID, b.Content, b.Workdir, b.Via), patternID, b.Content, b.Workdir, b.Via,
		programsJSON(b.Programs), b.Preview, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("approval: save Always answer for %s: %w", patternID, err)
	}
	return nil
}

// removedAlwaysKey is the kv key of the count of the Always rows that an update removed.
const removedAlwaysKey = "approvals_always_removed"

// RemovedAlwaysCount returns the count of the old Always answers that the update removed. 0
// means that no notice is due. A nil DB gives 0.
func (g *Gate) RemovedAlwaysCount(ctx context.Context) (int, error) {
	if g.DB == nil {
		return 0, nil
	}
	v, ok, err := g.DB.KVGet(ctx, removedAlwaysKey)
	if err != nil {
		return 0, fmt.Errorf("approval: read the count of removed Always answers: %w", err)
	}
	if !ok {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("approval: the count of removed Always answers is not a number: %w", err)
	}
	return n, nil
}

// ClearRemovedAlways deletes the notice count, so the notice shows one time only.
func (g *Gate) ClearRemovedAlways(ctx context.Context) error {
	if g.DB == nil {
		return nil
	}
	if _, err := g.DB.ExecContext(ctx, "DELETE FROM kv WHERE key = '"+removedAlwaysKey+"'"); err != nil {
		return fmt.Errorf("approval: clear the count of removed Always answers: %w", err)
	}
	return nil
}

// AlwaysRow is one stored "Always" answer. ID is the row id. Preview is the redacted command
// and Workdir is the folder of the command. Server and ToolName are set for an MCP row.
type AlwaysRow struct {
	ID, PatternID, Preview, Workdir, Server, ToolName string
	CreatedAt                                         time.Time
}

// ListAlways returns the stored "Always" answers, newest first. It leaves out
// the rows of protected rules, because the gate never reads them.
func (g *Gate) ListAlways(ctx context.Context) ([]AlwaysRow, error) {
	if g.DB == nil {
		return nil, nil
	}
	rows, err := g.DB.QueryContext(ctx, `SELECT id, pattern_id, preview, workdir, server, tool_name, programs, created_at
		FROM approval_grants ORDER BY created_at DESC, pattern_id, id`)
	if err != nil {
		g.noteDamage(err)
		return nil, fmt.Errorf("approval: list the Always answers: %w", err)
	}
	defer rows.Close()
	var out []AlwaysRow
	for rows.Next() {
		var r AlwaysRow
		var ms int64
		var programs string
		if err := rows.Scan(&r.ID, &r.PatternID, &r.Preview, &r.Workdir, &r.Server, &r.ToolName, &programs, &ms); err != nil {
			g.noteDamage(err)
			return nil, fmt.Errorf("approval: read an Always answer: %w", err)
		}
		var list []string
		if err := json.Unmarshal([]byte(programs), &list); err != nil {
			g.setGrantsOff("the programs value of the grant " + r.ID + " is not a JSON list of strings")
			continue
		}
		if IsProtectedPatternID(r.PatternID) {
			continue
		}
		r.CreatedAt = time.UnixMilli(ms)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		g.noteDamage(err)
		return nil, fmt.Errorf("approval: list the Always answers: %w", err)
	}
	return out, nil
}

// noteDamage turns off all saved answers when err shows a damaged grant store.
func (g *Gate) noteDamage(err error) {
	if grantStoreDamaged(err) {
		g.setGrantsOff(err.Error())
	}
}

// RemoveAlways removes the "Always" row with the row id. It also removes the "this session"
// answers for the rule of the row from each session of this gate: the keys with the rule ID,
// with the rule ID and a wrapper text, and, for an MCP rule, the keys of its content hashes.
func (g *Gate) RemoveAlways(ctx context.Context, id string) error {
	if g.DB == nil {
		return nil
	}
	var patternID string
	err := g.DB.QueryRowContext(ctx, "SELECT pattern_id FROM approval_grants WHERE id = ?", id).Scan(&patternID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("approval: find the Always answer %s: %w", id, err)
	}
	if _, err := g.DB.ExecContext(ctx, "DELETE FROM approval_grants WHERE id = ?", id); err != nil {
		return fmt.Errorf("approval: remove the Always answer for %s: %w", patternID, err)
	}
	g.mu.Lock()
	for _, m := range g.sessions {
		for k := range m {
			if k == patternID || strings.HasPrefix(k, patternID+" via ") ||
				(strings.HasPrefix(patternID, "mcp:") && strings.HasPrefix(k, patternID+":")) {
				delete(m, k)
			}
		}
	}
	g.mu.Unlock()
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

// cronApproves reports whether approvals.cron_mode runs dangerous commands. Only "approve"
// does, after the gate trims the value and makes it lower case. load.go accepts only "deny"
// and "approve", so any other value denies.
func cronApproves(mode string) bool {
	return strings.ToLower(strings.TrimSpace(mode)) == "approve"
}
