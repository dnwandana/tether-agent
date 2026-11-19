package security

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	// maxMCPValueRunes is the longest value of one argument line, with the final "…".
	maxMCPValueRunes = 300
	// maxMCPArgsRunes is the longest argument block, without the "Not shown" line.
	maxMCPArgsRunes = 3000
)

// MCPCall is one MCP tool call that the gate checks. Class is the final class of the tool.
// ConfigHash is the hash of the server entry, so a change of the entry ends the grants.
// Summary is the untrusted description of the model. Args is the JSON object of the arguments.
type MCPCall struct {
	Server, Tool, Class, ConfigHash, Summary string
	Args                                     json.RawMessage
}

// FormatMCPArgs returns the arguments of an MCP call for the approval prompt: one "key: value"
// line for each top-level key, in key order. Each value is one line, redacted with r, and cut
// at 300 characters. The lines stop at 3000 characters. A last line names the keys that the
// block does not show. A nil r masks nothing.
func FormatMCPArgs(args json.RawMessage, r *Redactor) string {
	const none = "(no arguments)"
	raw := bytes.TrimSpace(args)
	if len(raw) == 0 || string(raw) == "null" {
		return none
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "arguments: " + mcpValueLine(compactJSON(raw), r)
	}
	if len(fields) == 0 {
		return none
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var lines []string
	size := 0
	for i, k := range keys {
		v := fields[k]
		var text string
		if err := json.Unmarshal(v, &text); err != nil {
			text = compactJSON(v)
		}
		line := k + ": " + mcpValueLine(text, r)
		add := len([]rune(line))
		if len(lines) > 0 {
			add++
		}
		if size+add > maxMCPArgsRunes {
			lines = append(lines, "Not shown: "+strings.Join(keys[i:], ", "))
			break
		}
		lines = append(lines, line)
		size += add
	}
	return strings.Join(lines, "\n")
}

// compactJSON returns raw without insignificant white space, or raw as text when it is not JSON.
func compactJSON(raw []byte) string {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	return b.String()
}

// mcpValueLine returns v as one redacted line that is cut at maxMCPValueRunes.
func mcpValueLine(v string, r *Redactor) string {
	v = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(v)
	return cutRunes(r.Redact(v), maxMCPValueRunes)
}

// CheckMCP checks one MCP tool call. It returns nil when the call may run, and a
// *BlockedError with the reason for the model otherwise. The first result is nil when the
// call goes out as given. After a "send redacted" answer it holds the arguments to send. The
// order is:
//  1. A read call runs. It writes no log row.
//  2. A draft-only run lets a draft tool run and refuses each other call. No mode, grant, or
//     cron_mode lowers this step.
//  3. A human-only call asks the owner each time. No mode, grant, or cron_mode lowers it.
//  4. A send call with sensitive data in the arguments asks the owner each time, also in mode
//     off. A cron run blocks it.
//  5. A send call of a cron run after outside content goes to the guard, when cron_mode
//     approve would allow it. A hold blocks.
//  6. The live mode: a topic mode of full, or approvals.mode off, runs the call.
//  7. A member request asks each time.
//  8. A write call runs when a session answer or an Always row covers the tool and the config.
//  9. A cron run runs the call only with approvals.cron_mode: approve.
//  10. A send call asks each time.
//  11. The owner decides.
//
// The gate has no smart judge for MCP calls, because the arguments are content that can argue
// with a judge. An unknown class acts as write.
func (g *Gate) CheckMCP(ctx context.Context, req ApprovalRequest, call MCPCall, p Prompter) (json.RawMessage, error) {
	class := call.Class
	switch class {
	case "read", "write", "send", "human-only":
	default:
		class = "write"
	}
	if class == "read" {
		return nil, nil
	}
	name := "mcp:" + call.Server + "." + call.Tool
	req.Tool, req.PatternID = name, name
	req.Server, req.ToolName, req.Class = call.Server, call.Tool, class
	req.Description = fmt.Sprintf("MCP %s (%s · %s)", class, call.Server, call.Tool)
	req.Command = FormatMCPArgs(call.Args, g.Redactor)
	if req.DraftOnly {
		if class == "write" && IsDraftTool(call.Tool) {
			g.record(ctx, req, DecisionAllowed, sourceDraftOnly, "")
			return nil, nil
		}
		g.record(ctx, req, DecisionBlocked, sourceDraftOnly, "")
		return nil, blocked("BLOCKED: this job is draft-only. It can read and write drafts, but it cannot send. Do not try another way.")
	}
	if class == "human-only" {
		req.HumanOnly, req.NoSession, req.NoAlways = true, true, true
		return nil, g.humanOnlyAsk(ctx, req, p)
	}
	if class == "send" {
		findings, redacted := ScanArgs(call.Args, g.Redactor, nil)
		if len(findings) > 0 {
			req.Findings, req.NoSession, req.NoAlways = findings, true, true
			if isCron(req) {
				g.record(ctx, req, DecisionBlocked, sourceOutboundScan, "")
				return nil, blocked("BLOCKED: the arguments of %s.%s hold sensitive data (%s). A cron run cannot send it.",
					call.Server, call.Tool, kindList(findings))
			}
			return g.mcpAsk(ctx, req, call, p, redacted)
		}
		if isCron(req) && g.Guard != nil && len(req.OutsideSources) > 0 && cronApproves(g.CronMode) {
			allow, reason := g.Guard.Check(ctx, GuardInput{SessionID: req.SessionID, JobName: req.JobName,
				JobPurpose: req.JobPurpose, Sources: req.OutsideSources, Hosts: req.OutsideHosts,
				ToolCounts: req.ToolCounts, Action: mcpAction(call)})
			if !allow {
				g.record(ctx, req, DecisionBlocked, sourceModelGuard, "")
				return nil, blocked("BLOCKED: model_guard held %s.%s: %s", call.Server, call.Tool, reason)
			}
			g.record(ctx, req, DecisionAllowed, sourceModelGuard, "")
		}
	}
	if mode, source := g.liveMode(ctx, req.Scope); mode == "off" {
		g.record(ctx, req, DecisionAllowed, source, "")
		return nil, nil
	}
	if req.Member {
		req.NoSession, req.NoAlways = true, true
		return g.mcpAsk(ctx, req, call, p, nil)
	}
	if class == "write" {
		req.GrantKey = name + ":" + call.ConfigHash
		if g.grantsOff.Load() {
			req.NoSession, req.NoAlways = true, true
		} else if g.sessionApproved(sessionOf(req), req.GrantKey) || g.mcpAlwaysGranted(ctx, call) {
			g.record(ctx, req, DecisionAllowed, sourceGrant, "")
			return nil, nil
		}
	}
	if isCron(req) {
		if cronApproves(g.CronMode) {
			g.record(ctx, req, DecisionAllowed, sourceCronMode, "")
			return nil, nil
		}
		g.record(ctx, req, DecisionBlocked, sourceCronMode, "")
		return nil, endBlock(req, "BLOCKED: %s call %s.%s in a cron run. Set approvals.cron_mode: approve "+
			"to allow MCP write and send calls in cron jobs.", class, call.Server, call.Tool)
	}
	if class == "send" {
		req.NoSession, req.NoAlways = true, true
	}
	return g.mcpAsk(ctx, req, call, p, nil)
}

// kindList returns the sorted kinds of the findings, joined with ", ".
func kindList(f []Finding) string {
	kinds := FindingKinds(f)
	names := make([]string, 0, len(kinds))
	for k := range kinds {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// mcpAction describes an MCP call for the guard: the server, the tool, the class, and the
// sorted top-level argument names. It never holds an argument value.
func mcpAction(call MCPCall) string {
	text := fmt.Sprintf("call the MCP tool %s.%s (class %s) with ", call.Server, call.Tool, call.Class)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(call.Args), &fields); err != nil || len(fields) == 0 {
		return text + "no arguments"
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return text + "the arguments: " + strings.Join(keys, ", ")
}

// mcpAsk puts the question to the owner and applies the answer. It follows ask, and it saves
// an Always answer as an MCP row. A "send redacted" answer returns redacted.
func (g *Gate) mcpAsk(ctx context.Context, req ApprovalRequest, call MCPCall, p Prompter, redacted json.RawMessage) (json.RawMessage, error) {
	if p == nil {
		g.record(ctx, req, DecisionBlocked, sourceNoPrompter, "")
		return nil, blocked("BLOCKED: MCP call flagged for approval (%s), and no user is present to approve it. "+
			"Find an alternative approach that avoids this call.", req.Description)
	}
	scope, err := g.prompt(ctx, req, p)
	switch {
	case ctx.Err() != nil:
		g.record(ctx, req, DecisionDenied, sourceStopped, "")
		return nil, blocked("BLOCKED: the approval request for %s stopped (%v). Do NOT retry.", req.Description, ctx.Err())
	case scope == ScopeTimeout || errors.Is(err, context.DeadlineExceeded):
		g.record(ctx, req, DecisionDenied, sourceTimeout, "")
		return nil, blocked("BLOCKED: approval denied (timeout): %s. The user did not answer in time. %s "+
			"Silence is not consent.", req.Description, noConsent)
	case err != nil:
		g.record(ctx, req, DecisionDenied, sourcePromptError, "")
		return nil, blocked("BLOCKED: Failed to send the approval request to the user (%v). Do NOT retry.", err)
	}
	if scope == ScopeOnce || scope == ScopeSession || scope == ScopeAlways || scope == ScopeRedacted {
		// The owner can change the mode while the prompt waits.
		if g.ModeOf(ctx, req.Scope) == ModeReadOnly {
			g.record(ctx, req, DecisionBlocked, sourceModeReadOnly, "")
			return nil, blocked("BLOCKED: The mode of this topic changed to read-only while the approval waited.")
		}
	}
	if scope == ScopeRedacted && len(req.Findings) == 0 {
		scope = ScopeDeny
	}
	switch scope {
	case ScopeRedacted:
		g.record(ctx, req, DecisionAllowed, sourceOwner, string(ScopeRedacted))
		return redacted, nil
	case ScopeOnce:
		g.record(ctx, req, DecisionAllowed, sourceOwner, string(ScopeOnce))
	case ScopeSession, ScopeAlways:
		applied := ScopeOnce
		if !req.NoSession {
			g.approveSession(sessionOf(req), sessionKey(req))
			applied = ScopeSession
			if scope == ScopeAlways && !req.NoAlways && g.DB != nil {
				if err := g.saveMCPAlways(ctx, call); err != nil {
					g.logf("approval: cannot save the Always answer, so it holds for this session only",
						"pattern", req.PatternID, "err", err)
				} else {
					applied = ScopeAlways
				}
			}
		}
		g.record(ctx, req, DecisionAllowed, sourceOwner, string(applied))
	default:
		g.record(ctx, req, DecisionDenied, sourceOwner, "")
		return nil, endBlock(req, "BLOCKED by user: %s. %s", req.Description, noConsent)
	}
	return nil, nil
}

// mcpAlwaysGranted reports whether approval_grants has an Always row for the tool and the
// config hash. An error that is not damage counts as "no grant", so the gate asks.
func (g *Gate) mcpAlwaysGranted(ctx context.Context, call MCPCall) bool {
	if g.DB == nil || g.grantsOff.Load() {
		return false
	}
	pattern := "mcp:" + call.Server + "." + call.Tool
	var one int
	err := g.DB.QueryRowContext(ctx, "SELECT 1 FROM approval_grants WHERE id=?",
		grantID(pattern, call.ConfigHash, "", "")).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		if grantStoreDamaged(err) {
			g.setGrantsOff(err.Error())
		} else {
			g.logf("approval: cannot read approval_grants", "pattern", pattern, "err", err)
		}
		return false
	}
	return true
}

// saveMCPAlways stores an Always answer for an MCP tool and the config hash of its server.
func (g *Gate) saveMCPAlways(ctx context.Context, call MCPCall) error {
	pattern := "mcp:" + call.Server + "." + call.Tool
	_, err := g.DB.ExecContext(ctx, `INSERT INTO approval_grants(id, pattern_id, content_sha256, workdir, via,
		programs, preview, server, tool_name, created_at) VALUES (?, ?, ?, '', '', '[]', '', ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET created_at=excluded.created_at`,
		grantID(pattern, call.ConfigHash, "", ""), pattern, call.ConfigHash, call.Server, call.Tool,
		time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("approval: save Always answer for %s: %w", pattern, err)
	}
	return nil
}
