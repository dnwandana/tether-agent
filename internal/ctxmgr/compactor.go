package ctxmgr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// SummaryPrefix starts the message that replaces the compacted messages in the model view.
const SummaryPrefix = "[conversation summary]\n"

// TodoSnapshotHeader starts the task list at the end of a summary. The next compaction
// removes the list from this header to the end, so the summary keeps one list (Hermes
// TODO_INJECTION_HEADER).
const TodoSnapshotHeader = "[Your active task list was preserved across context compression]"

// StripTodoSnapshot returns summary without the task list snapshot: it removes the text
// from the first TodoSnapshotHeader to the end, and the white space before the header.
func StripTodoSnapshot(summary string) string {
	before, _, found := strings.Cut(summary, TodoSnapshotHeader)
	if !found {
		return summary
	}
	return strings.TrimRightFunc(before, unicode.IsSpace)
}

// maxFailures is the number of failed compactions in a row after which the user is told.
const maxFailures = 3

// The limits of the summary size in tokens (Hermes _MIN_SUMMARY_TOKENS and
// _SUMMARY_TOKENS_CEILING).
const (
	minSummaryTokens = 2000
	maxSummaryTokens = 12000
)

// The limits of one serialized message in the summary request (Hermes
// _serialize_for_summary).
const (
	contentMax  = 6000
	contentHead = 4000
	contentTail = 1500
	argsMax     = 1500
	argsHead    = 1200
)

// summaryTimeout ends a summary call that takes too long.
const summaryTimeout = 5 * time.Minute

// ErrCompactionFailed is the error of Maybe after maxFailures failed compactions in a row.
var ErrCompactionFailed = errors.New("compaction failed")

// SummaryPrompt is the system prompt of the summary call. It follows the
// structure of the Hermes context compressor.
const SummaryPrompt = `You make a context checkpoint for an agent conversation. The turns that you get are the source material. The agent continues the conversation with your summary in place of these turns.

Rules:
- Write only the summary. Do not add a greeting, a preamble, or a prefix.
- Write in the language that the user used in the conversation.
- Keep facts, decisions, file paths, commands, URLs, identifiers, numbers, error messages, and open tasks word for word.
- Be concrete. Do not write "made some changes"; write what changed and where.
- Never write API keys, tokens, passwords, or other secrets. Write [REDACTED] in their place.
- When the turns contain a PREVIOUS SUMMARY, keep all its information that is still true, and add the new turns to it.

Use this structure:

## Active Task
The most recent request of the user that is not done yet, in the exact words of the user. Write "None." only when the last request is fully done.

## Goal
What the user wants to do overall.

## Constraints and Preferences
The preferences, the constraints, and the important decisions of the user.

## Completed Actions
A numbered list: the action, the target, the result, and the tool.

## Active State
The working directory, the files that changed, the test status, and the running processes.

## Open Problems
The errors and the blockers that are not solved, with the exact error text.

## Pending User Asks
The questions and the requests of the user that have no answer yet. Write "None." when there are none.

## Relevant Files
The files that the agent read, changed, or created, with one short note for each.

## Critical Context
The values, the configuration details, and the data that the agent needs to continue.`

// Compaction is one row of the compactions table. Its summary replaces the stored
// messages from FromMessageID to UptoMessageID in the model view.
type Compaction struct {
	ID                           int64
	UptoMessageID, FromMessageID int64
	Summary                      string
	CreatedAt                    time.Time
}

// Compactor replaces the middle of a long conversation with a summary. It
// never changes the stored messages. It is safe for concurrent use.
type Compactor struct {
	Store *sessions.Store
	// DB is the state.db handle. Maybe reads and writes the compactions table with it.
	DB  *db.DB
	Cfg config.ContextConfig
	Log *slog.Logger
	// OnCompact runs one time for each compaction, after Maybe saves the summary and before
	// Maybe returns. view is the full model view before the compaction, and view[start:end]
	// is the part that the saved summary replaces. A failed attempt does not call it. It
	// must return at once, so it starts long work in its own goroutine. It can be nil.
	OnCompact func(ctx context.Context, s *sessions.Session, view []provider.Message, start, end int)
	// Todo returns the task list snapshot of the session for the end of the summary, or "".
	// It can be nil.
	Todo func(ctx context.Context, sessionID string) string
	// Record records the usage of the summary call. It can be nil.
	Record provider.RecordUsage

	mu       sync.Mutex
	failures map[string]int
	thrash   map[string]*thrashState
}

// maxIneffective is the number of compactions in a row that leave the view over the
// threshold before the automatic check stops. Hermes agent/context_compressor.py uses the
// same limit.
const maxIneffective = 2

// thrashState is the anti-thrash state of one session. The system prompt and the tool
// schemas are a floor that a compaction cannot make smaller. When that floor and the
// protected messages are over the threshold, each round compacts again and makes one more
// summary call.
type thrashState struct {
	verify  bool // a compaction ran, and the next check judges it
	strikes int  // the compactions in a row that left the view over the threshold
}

// allowAuto records the result over of an automatic check of the session. It reports
// whether the check can compact. A view under the threshold clears the state.
func (c *Compactor) allowAuto(sessionID string, over bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !over {
		delete(c.thrash, sessionID)
		return true
	}
	st := c.thrash[sessionID]
	if st == nil {
		return true
	}
	if st.verify {
		st.verify = false
		st.strikes++
		if st.strikes == maxIneffective {
			c.logger().Warn("compaction did not bring the view under the threshold two times in a row; "+
				"automatic compaction stops until the view is under the threshold again. The system prompt "+
				"and the tool schemas can be too large for the context window", "session", sessionID)
		}
	}
	return st.strikes < maxIneffective
}

// compactedNow marks that a compaction of the session ran, so the next check judges it. A
// forced compaction clears the strikes first.
func (c *Compactor) compactedNow(sessionID string, force bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.thrash == nil {
		c.thrash = map[string]*thrashState{}
	}
	st := c.thrash[sessionID]
	if st == nil || force {
		st = &thrashState{}
		c.thrash[sessionID] = st
	}
	st.verify = true
}

// Maybe compacts the session when its model view is above Cfg.CompactThreshold of window,
// or when force is true. It uses prov for the summary. A failed summary call returns
// false and a nil error, and the next call tries again. At the third failure in a row, the
// error wraps ErrCompactionFailed, so the caller can tell the user.
func (c *Compactor) Maybe(ctx context.Context, s *sessions.Session, prov provider.Provider, window int, force bool) (compacted bool, err error) {
	stored, err := c.Store.Messages(ctx, s.ID)
	if err != nil {
		return false, fmt.Errorf("compaction: %w", err)
	}
	return c.MaybeStored(ctx, s, prov, window, force, stored, 0)
}

// MaybeStored is Maybe with the stored messages of the session that the caller read. A
// compaction does not change the stored messages, so the caller can use stored again
// after the compaction. fixed is the token estimate of the system prompt and the tool
// schemas of the next request (EstimateFixed). The check uses it when no provider count
// is available, for example after a compaction or with a provider that gives no usage.
func (c *Compactor) MaybeStored(ctx context.Context, s *sessions.Session, prov provider.Provider, window int, force bool, stored []sessions.StoredMessage, fixed int) (compacted bool, err error) {
	compacted, _, err = c.MaybeStoredView(ctx, s, prov, window, force, stored, fixed)
	return compacted, err
}

// MaybeStoredView is MaybeStored, and it also returns the compactions that the check read
// with LatestCompaction. comps is nil when the read failed. comps is also nil when
// compacted is true, because the new compaction row makes the read old.
func (c *Compactor) MaybeStoredView(ctx context.Context, s *sessions.Session, prov provider.Provider, window int, force bool, stored []sessions.StoredMessage, fixed int) (compacted bool, comps []Compaction, err error) {
	comps, err = LatestCompaction(ctx, c.DB, s.ID)
	if err != nil {
		return false, nil, err
	}
	view := BuildStoredView(stored, comps)
	trigger := int(c.Cfg.CompactThreshold * float64(window))
	if !force {
		last, at := lastPrompt(stored, comps)
		over := OverThreshold(ViewSize(last, at, fixed, view), window, c.Cfg.CompactThreshold)
		if !c.allowAuto(s.ID, over) || !over {
			return false, comps, nil
		}
	}
	msgs := make([]provider.Message, len(view))
	for i, v := range view {
		msgs[i] = v.Msg
	}
	start, end, ok := SelectRange(msgs, c.Cfg.ProtectFirstN, c.Cfg.ProtectLastN)
	if !ok {
		return false, comps, nil
	}
	// The previous summary goes into the new summary, so the view keeps one summary.
	prevIdx := summaryIndex(view)
	if prevIdx >= 0 && prevIdx < start {
		start = prevIdx
	}
	if prevIdx >= end {
		return false, comps, nil
	}
	from, upto := rangeIDs(view[start:end], comps)
	if from == 0 || upto == 0 {
		return false, comps, nil
	}
	summary, err := c.summarize(ctx, prov, s.ID, msgs[start:end], trigger)
	if err != nil {
		if ctx.Err() != nil {
			return false, comps, nil
		}
		return false, comps, c.fail(s.ID, err)
	}
	// A summary model can copy the old snapshot, and the summary must keep one snapshot.
	// The snapshot is part of the stored summary, so each later request has the same bytes.
	summary = StripTodoSnapshot(summary)
	if c.Todo != nil {
		if snap := c.Todo(ctx, s.ID); snap != "" {
			summary += "\n\n" + snap
		}
	}
	if _, err := c.DB.ExecContext(context.WithoutCancel(ctx), `INSERT INTO compactions
		(session_id, from_message_id, upto_message_id, summary, created_at) VALUES (?, ?, ?, ?, ?)`,
		s.ID, from, upto, summary, time.Now().UnixMilli()); err != nil {
		return false, comps, c.fail(s.ID, fmt.Errorf("save the summary: %w", err))
	}
	c.mu.Lock()
	delete(c.failures, s.ID)
	c.mu.Unlock()
	c.compactedNow(s.ID, force)
	c.logger().Info("compaction done", "session", s.ID, "from", from, "upto", upto, "messages", end-start)
	// The hook runs only after the save, so each saved compaction gets one review of the
	// range that it replaces. The call is in this goroutine, so the hook reads the state of
	// the turn before the turn continues.
	if c.OnCompact != nil {
		c.OnCompact(context.WithoutCancel(ctx), s, msgs, start, end)
	}
	return true, nil, nil
}

// View returns the model view of the session: the stored messages with the latest
// compaction applied.
func (c *Compactor) View(ctx context.Context, sessionID string) ([]StoredView, error) {
	stored, err := c.Store.Messages(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("compaction: %w", err)
	}
	comps, err := LatestCompaction(ctx, c.DB, sessionID)
	if err != nil {
		return nil, err
	}
	return BuildStoredView(stored, comps), nil
}

// summarize sends msgs to prov and returns the summary text. sessionID is the session
// that the summary call records its usage to.
func (c *Compactor) summarize(ctx context.Context, prov provider.Provider, sessionID string, msgs []provider.Message, trigger int) (string, error) {
	target := min(max(int(c.Cfg.SummaryTargetRatio*float64(trigger)), minSummaryTokens), maxSummaryTokens)
	body := fmt.Sprintf("Make the checkpoint summary of the conversation turns below. Target about %d tokens.\n\nTURNS TO SUMMARIZE:\n\n%s",
		target, serialize(msgs))
	cctx, cancel := context.WithTimeout(ctx, summaryTimeout)
	defer cancel()
	ch, err := prov.Stream(cctx, provider.Request{
		System:    SummaryPrompt,
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: body}},
		MaxTokens: target * 13 / 10,
	})
	if err != nil {
		return "", err
	}
	res, err := provider.Collect(ch, nil)
	if err != nil {
		return "", err
	}
	// The provider billed the call, also when the text is empty. Record the usage before the
	// empty-text check.
	if c.Record != nil {
		c.Record(ctx, sessionID, "compaction", prov, res)
	}
	text := strings.TrimSpace(res.Text)
	if text == "" {
		return "", errors.New("the model sent an empty summary")
	}
	return text, nil
}

// fail counts one failure of the session. It returns an error that wraps
// ErrCompactionFailed at each maxFailures-th failure in a row, else nil.
func (c *Compactor) fail(sessionID string, cause error) error {
	c.mu.Lock()
	if c.failures == nil {
		c.failures = map[string]int{}
	}
	c.failures[sessionID]++
	n := c.failures[sessionID]
	if n >= maxFailures {
		delete(c.failures, sessionID)
	}
	c.mu.Unlock()
	c.logger().Warn("compaction failed; the turn continues without compaction", "session", sessionID, "failures", n, "err", cause.Error())
	if n < maxFailures {
		return nil
	}
	return fmt.Errorf("%w %d times in a row; the conversation continues without compaction: %v", ErrCompactionFailed, n, cause)
}

func (c *Compactor) logger() *slog.Logger {
	if c.Log == nil {
		return slog.Default()
	}
	return c.Log
}

// LoadCompactions returns the compactions of the session in id order.
func LoadCompactions(ctx context.Context, d *db.DB, sessionID string) ([]Compaction, error) {
	return loadCompactions(ctx, d, sessionID, `SELECT id, from_message_id, upto_message_id, summary, created_at
		FROM compactions WHERE session_id = ? ORDER BY id`)
}

// LatestCompaction returns the latest compaction of the session, or no compaction. The
// model view uses only the latest compaction, because each summary also holds the
// previous summary. Thus the read does not get the old summaries.
func LatestCompaction(ctx context.Context, d *db.DB, sessionID string) ([]Compaction, error) {
	return loadCompactions(ctx, d, sessionID, `SELECT id, from_message_id, upto_message_id, summary, created_at
		FROM compactions WHERE session_id = ? ORDER BY id DESC LIMIT 1`)
}

// loadCompactions returns the compactions that query gives for the session.
func loadCompactions(ctx context.Context, d *db.DB, sessionID, query string) ([]Compaction, error) {
	if d == nil {
		return nil, nil
	}
	rows, err := d.QueryContext(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("compaction: read the compactions of %s: %w", sessionID, err)
	}
	defer rows.Close()
	var out []Compaction
	for rows.Next() {
		var (
			c  Compaction
			at int64
		)
		if err := rows.Scan(&c.ID, &c.FromMessageID, &c.UptoMessageID, &c.Summary, &at); err != nil {
			return nil, fmt.Errorf("compaction: read the compactions of %s: %w", sessionID, err)
		}
		c.CreatedAt = time.UnixMilli(at).UTC()
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("compaction: read the compactions of %s: %w", sessionID, err)
	}
	return out, nil
}

// lastPrompt returns the prompt tokens of the last response, and the id before that
// response, so ViewSize also counts the response text. A response from before the latest
// compaction does not count, because its prompt held the messages that the summary
// replaced.
func lastPrompt(stored []sessions.StoredMessage, comps []Compaction) (tokens int, atMessage int64) {
	var after time.Time
	if len(comps) > 0 {
		after = comps[len(comps)-1].CreatedAt
	}
	for i := len(stored) - 1; i >= 0; i-- {
		m := stored[i]
		if len(comps) > 0 && !m.At.After(after) {
			return 0, 0
		}
		if m.Msg.Role != provider.RoleAssistant {
			continue
		}
		if n := m.Meta.Usage.InputTokens + m.Meta.Usage.CacheReadTokens; n > 0 {
			return n, m.ID - 1
		}
	}
	return 0, 0
}

// summaryIndex returns the index of the summary message in view, or -1.
func summaryIndex(view []StoredView) int {
	for i, v := range view {
		if v.ID == 0 && strings.HasPrefix(v.Msg.Content, SummaryPrefix) {
			return i
		}
	}
	return -1
}

// rangeIDs returns the first and the last stored message id that part covers. When part
// holds the previous summary, the range starts where the previous compaction started. Other
// messages with id 0, for example a synthetic tool result, do not change the range.
func rangeIDs(part []StoredView, comps []Compaction) (from, upto int64) {
	for _, v := range part {
		if v.ID == 0 {
			isSummary := strings.HasPrefix(v.Msg.Content, SummaryPrefix)
			if isSummary && len(comps) > 0 && (from == 0 || comps[len(comps)-1].FromMessageID < from) {
				from = comps[len(comps)-1].FromMessageID
			}
			continue
		}
		if from == 0 || v.ID < from {
			from = v.ID
		}
		upto = max(upto, v.ID)
	}
	if upto == 0 && len(comps) > 0 {
		upto = comps[len(comps)-1].UptoMessageID
	}
	return from, upto
}

// serialize returns msgs as labeled text for the summary request (Hermes
// _serialize_for_summary). The tool results are already redacted by the agent loop.
func serialize(msgs []provider.Message) string {
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		content := cutMiddle(m.Content)
		switch {
		case m.Role == provider.RoleUser && strings.HasPrefix(m.Content, SummaryPrefix):
			// The old snapshot does not go to the summary model. Maybe adds a new one.
			parts = append(parts, "[PREVIOUS SUMMARY]:\n"+StripTodoSnapshot(strings.TrimPrefix(m.Content, SummaryPrefix)))
		case m.Role == provider.RoleTool:
			parts = append(parts, fmt.Sprintf("[TOOL RESULT %s]: %s", m.ToolCallID, content))
		case m.Role == provider.RoleAssistant:
			if len(m.ToolCalls) > 0 {
				calls := make([]string, len(m.ToolCalls))
				for i, tc := range m.ToolCalls {
					args := tc.Args
					if len(args) > argsMax {
						args = SanitizeUTF8(args[:argsHead]) + "..."
					}
					calls[i] = "  " + tc.Name + "(" + args + ")"
				}
				content += "\n[Tool calls:\n" + strings.Join(calls, "\n") + "\n]"
			}
			parts = append(parts, "[ASSISTANT]: "+content)
		default:
			parts = append(parts, "["+strings.ToUpper(string(m.Role))+"]: "+content)
		}
	}
	return strings.Join(parts, "\n\n")
}

// cutMiddle keeps the head and the tail of a long message body.
func cutMiddle(s string) string {
	if len(s) <= contentMax {
		return s
	}
	return SanitizeUTF8(s[:contentHead]) + "\n...[truncated]...\n" + SanitizeUTF8(s[len(s)-contentTail:])
}
