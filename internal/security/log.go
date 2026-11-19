package security

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"time"
)

// The decisions that the gate writes to approval_log.
const (
	// DecisionAllowed means that the command ran or may run.
	DecisionAllowed = "allowed"
	// DecisionDenied means that the owner, a timeout, or a stop refused the command.
	DecisionDenied = "denied"
	// DecisionBlocked means that a rule or a setting refused the command with no owner answer.
	DecisionBlocked = "blocked"
)

// The sources of a decision.
const (
	sourceOwner        = "owner"
	sourceGrant        = "grant"
	sourceModeOff      = "mode-off"
	sourceModeFull     = "mode-full"
	sourceCronMode     = "cron-mode"
	sourceSmart        = "smart"
	sourceTimeout      = "timeout"
	sourceStopped      = "stopped"
	sourcePromptError  = "prompt-error"
	sourceHardline     = "hardline"
	sourceHumanOnly    = "human-only"
	sourceCouldNotRead = "could-not-read"
	sourceMember       = "member"
	sourceNoPrompter   = "no-prompter"
	sourceModeReadOnly = "mode-read-only"
	sourceDraftOnly    = "draft-only"
	sourceOutboundScan = "outbound-scan"
	sourceModelGuard   = "model-guard"
)

const (
	// maxLogPreviewRunes is the limit of the preview of a log row, with the final "…".
	maxLogPreviewRunes = 300
	// sudoStdinRule is the rule of a log row for the sudo -S guard, which has no pattern.
	sudoStdinRule = "sudo_stdin"
	// toolGoalGate is the tool of a row that a goal gate command writes.
	toolGoalGate = "goal_gate"
	// passwordChangeRule is the built-in human-only rule for a password change.
	passwordChangeRule = "human_only_password_change"
	// logInsertFailedText is the log line for a failed insert.
	logInsertFailedText = "approval: cannot write the decision log row; the decision does not change"
)

// LogRow is one row of approval_log.
type LogRow struct {
	At                                                          time.Time
	SessionID, Scope, Kind, Tool, Server, ToolName, Rule, Class string
	Decision, Source, Answer, ContentSHA256, Preview            string
}

// record writes one decision row. It does nothing without a DB. An insert error only logs,
// so a log failure never changes a decision. It ignores the cancel of ctx, so a stopped
// request is also written.
func (g *Gate) record(ctx context.Context, req ApprovalRequest, decision, source, answer string) {
	if g.DB == nil {
		return
	}
	scope := req.LogScope
	if scope == "" {
		scope = req.Scope
	}
	var sum string
	if req.Tool == "execute_code" {
		sum = sha256Hex(req.Command)
	} else if cmd := strings.TrimSpace(req.Command); cmd != "" {
		sum = sha256Hex(cmd)
	}
	_, err := g.DB.ExecContext(context.WithoutCancel(ctx),
		`INSERT INTO approval_log(at, session_id, scope, kind, tool, server, tool_name, rule, class,
			decision, source, answer, content_sha256, preview) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		time.Now().UnixMilli(), req.SessionID, scope, req.Kind, req.Tool, req.Server, req.ToolName,
		req.PatternID, req.Class, decision, source, answer, sum, g.logPreview(req))
	if err != nil {
		g.logf(logInsertFailedText, "rule", req.PatternID, "err", err)
	}
}

// sha256Hex returns the SHA-256 of s as hex.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// logPreview returns the preview of a log row: one line, redacted, cut at 300 runes. A password
// change keeps only the program word, because its arguments hold a password. A request with
// findings also has its cards, IBANs, and phone numbers replaced, because state.db and the
// backups keep the preview.
func (g *Gate) logPreview(req ApprovalRequest) string {
	if req.PatternID == passwordChangeRule {
		return programWord(req.Command)
	}
	if req.PinName != "" {
		return cutRunes(req.PinName+" "+req.PinPath, maxLogPreviewRunes)
	}
	s := strings.TrimSpace(req.Command)
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
	if len(req.Secrets) > 0 {
		s = "[gets: " + strings.Join(req.Secrets, ", ") + "] " + s
	}
	s = g.Redactor.Redact(s)
	if len(req.Findings) > 0 {
		s = maskCutDigits(RedactFindings(s, Scan(s, nil, nil)))
	}
	return cutRunes(s, maxLogPreviewRunes)
}

// maskCutDigits replaces each digit of a run of digits and single separators that ends at a
// "…". The cut of a long value can split a card or a phone number, and then the scan does not
// find the part before the cut.
func maskCutDigits(s string) string {
	var mask []bool
	for i := strings.Index(s, "…"); i >= 0; {
		for j := i - 1; j >= 0; j-- {
			if isDigit(s[j]) {
				if mask == nil {
					mask = make([]bool, len(s))
				}
				mask[j] = true
			} else if !(s[j] == ' ' || s[j] == '-' || s[j] == '.') || j == 0 || !isDigit(s[j-1]) {
				break
			}
		}
		next := strings.Index(s[i+len("…"):], "…")
		if next < 0 {
			break
		}
		i += len("…") + next
	}
	if mask == nil {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if mask[i] {
			b.WriteString("•")
		} else {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// programWord returns the first program word of command that is a password-change program.
func programWord(command string) string {
	for _, c := range ReadCommand(command).Cmds {
		if len(c.Argv) == 0 {
			continue
		}
		switch name := filepath.Base(c.Argv[0]); name {
		case "passwd", "chpasswd", "usermod", "smbpasswd", "htpasswd":
			return name
		}
	}
	return ""
}

// ListLog returns the newest rows of scope, or of every scope when all is true.
func (g *Gate) ListLog(ctx context.Context, scope string, all bool, limit int) ([]LogRow, error) {
	if g.DB == nil {
		return nil, nil
	}
	q := `SELECT at, session_id, scope, kind, tool, server, tool_name, rule, class, decision, source,
		answer, content_sha256, preview FROM approval_log`
	args := []any{}
	if !all {
		q += " WHERE scope = ?"
		args = append(args, scope)
	}
	q += " ORDER BY at DESC, id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := g.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogRow
	for rows.Next() {
		var r LogRow
		var at int64
		if err := rows.Scan(&at, &r.SessionID, &r.Scope, &r.Kind, &r.Tool, &r.Server, &r.ToolName, &r.Rule,
			&r.Class, &r.Decision, &r.Source, &r.Answer, &r.ContentSHA256, &r.Preview); err != nil {
			return nil, err
		}
		r.At = time.UnixMilli(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneLog deletes the rows that are older than before. It returns the number of rows.
func (g *Gate) PruneLog(ctx context.Context, before time.Time) (int64, error) {
	if g.DB == nil {
		return 0, nil
	}
	res, err := g.DB.ExecContext(ctx, "DELETE FROM approval_log WHERE at < ?", before.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RecordGateAdd writes the row of a goal gate command that the owner adds. decision is one of
// the Decision consts. A block gets the source of the rule that blocks it.
func (g *Gate) RecordGateAdd(ctx context.Context, scope, command, decision string) {
	req := ApprovalRequest{Command: command, Scope: scope, Kind: "chat", Tool: toolGoalGate}
	rule, source := "", sourceOwner
	blockSource := func(r, s string) {
		rule = r
		if decision == DecisionBlocked {
			source = s
		}
	}
	p := ReadCommand(command)
	pats := MatchAllDangerousParsed(command, p)
	if fr, _, err := g.floorMatch(req, g.SudoPasswordSet); err != nil {
		blockSource(fr, sourceHardline)
	} else if pat, ok := MatchHumanOnlyParsed(command, p); ok {
		blockSource(pat.ID, sourceHumanOnly)
	} else if raised, ok := firstRaised(pats); ok {
		blockSource(raised.ID, sourceHumanOnly)
	} else if !p.Read {
		blockSource(CouldNotReadPatternID, sourceCouldNotRead)
	} else if len(pats) > 0 {
		rule = pats[0].ID
	}
	req.PatternID = rule
	g.record(ctx, req, decision, source, "")
}

// firstRaised returns the first rule that the raise list names.
func firstRaised(pats []Pattern) (Pattern, bool) {
	for _, pat := range pats {
		if IsHumanOnlyRaised(pat.ID) {
			return pat, true
		}
	}
	return Pattern{}, false
}

// CheckACPFloor runs the floor for a command of a coding agent. It writes a blocked row when
// the floor blocks. The adapter gets no SUDO_PASSWORD, so callers pass false for sudoPasswordSet.
func (g *Gate) CheckACPFloor(ctx context.Context, req ApprovalRequest, sudoPasswordSet bool) (label string, err error) {
	rule, label, err := g.floorMatch(req, sudoPasswordSet)
	if err != nil {
		req.PatternID = rule
		g.record(ctx, req, DecisionBlocked, sourceHardline, "")
	}
	return label, err
}

// floorMatch runs the floor checks and also returns the rule of the block for the log.
func (g *Gate) floorMatch(req ApprovalRequest, sudoPasswordSet bool) (rule, label string, err error) {
	return g.floorRule(req, sudoPasswordSet, ReadCommand(req.Command))
}
