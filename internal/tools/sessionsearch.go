package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// The limits of the session_search shapes.
const (
	searchDefaultLimit = 5
	searchMaxLimit     = 20
	scrollDefaultWin   = 10
	scrollMaxWin       = 50
	searchDateLayout   = "2006-01-02 15:04"
)

// SessionSearchTool is the session_search tool. It returns
// raw messages and snippets, with no LLM call.
type SessionSearchTool struct {
	Store *sessions.Store
	// MaxChars caps the text of the read shape (context.tool_output_max_chars).
	// A value <= 0 means no cap.
	MaxChars int
}

type sessionSearchArgs struct {
	Query           string `json:"query"`
	Limit           int    `json:"limit"`
	SessionID       string `json:"session_id"`
	AroundMessageID int64  `json:"around_message_id"`
	Window          int    `json:"window"`
}

// Name returns "session_search".
func (SessionSearchTool) Name() string { return "session_search" }

// Toolset returns the memory toolset.
func (SessionSearchTool) Toolset() string { return SetMemory }

// ParallelSafe returns true, because a search changes nothing.
func (SessionSearchTool) ParallelSafe() bool { return true }

// Schema returns the tool schema for the model.
func (SessionSearchTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{
		Name: "session_search",
		Description: "Search the messages of past sessions, or read in one session. The search uses SQLite FTS5 " +
			"and returns real messages, not summaries.\n\n" +
			"THREE SHAPES:\n" +
			"1) DISCOVERY: pass query (and limit). Each hit gives the session id, the title, the date, the message id, " +
			"and a snippet with the match in [brackets]. The newest hits come first.\n" +
			"2) READ: pass session_id only. Returns the messages of the session from the start.\n" +
			"3) SCROLL: pass session_id and around_message_id (and window). Returns the message and the window " +
			"messages before and after it. To scroll forward, pass the last message id; to scroll back, pass the first.\n\n" +
			"FTS5 SYNTAX: all words must match by default. Use OR for more results (alpha OR beta), quotes for an exact " +
			"phrase (\"docker networking\"), NOT to exclude a word, and * for a prefix (deploy*).\n\n" +
			"Use this tool first for questions like 'what did we do about X' or 'where did we stop with Y'.",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "The FTS5 query (discovery shape)."},
    "limit": {"type": "integer", "description": "Discovery shape: the maximum number of hits. The default is 5, the maximum is 20."},
    "session_id": {"type": "string", "description": "Read and scroll shapes: the session to read."},
    "around_message_id": {"type": "integer", "description": "Scroll shape: the message id at the center of the window."},
    "window": {"type": "integer", "description": "Scroll shape: the number of messages on each side. The default is 10, the maximum is 50."}
  }
}`),
	}
}

// Run chooses the shape from the fields that are present: session_id with
// around_message_id scrolls, session_id alone reads, query searches.
func (t SessionSearchTool) Run(ctx context.Context, raw json.RawMessage, _ *Env) (string, error) {
	var a sessionSearchArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if t.Store == nil {
		return "", toolErr("session search is not available in this session")
	}
	a.SessionID = strings.TrimSpace(a.SessionID)
	switch {
	case a.SessionID != "" && a.AroundMessageID != 0:
		return t.scroll(ctx, a)
	case a.SessionID != "":
		return t.read(ctx, a.SessionID)
	case strings.TrimSpace(a.Query) != "":
		return t.discover(ctx, a)
	}
	return "", toolErr("pass query to search, session_id to read a session, or session_id and around_message_id to scroll")
}

func (t SessionSearchTool) discover(ctx context.Context, a sessionSearchArgs) (string, error) {
	limit := a.Limit
	if limit <= 0 {
		limit = searchDefaultLimit
	}
	limit = min(limit, searchMaxLimit)
	hits, err := t.Store.Search(ctx, a.Query, limit)
	if errors.Is(err, sessions.ErrQuerySyntax) {
		return "", toolErr("%s", sessions.ErrQuerySyntax.Error())
	}
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return fmt.Sprintf("no messages match %q", a.Query), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d hits for %q, newest first:\n", len(hits), a.Query)
	for _, h := range hits {
		fmt.Fprintf(&b, "session %s · %s · %s · msg %d: %s\n", h.SessionID, titleOr(h.Title),
			h.Date.Format(searchDateLayout), h.MessageID, oneLine(h.Snippet))
	}
	return b.String(), nil
}

func (t SessionSearchTool) read(ctx context.Context, id string) (string, error) {
	sess, err := t.Store.Get(ctx, id)
	if errors.Is(err, sessions.ErrNotFound) {
		return "", toolErr("no session %s", id)
	}
	if err != nil {
		return "", err
	}
	ms, err := t.Store.Messages(ctx, id)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "session %s · %s · %s · %d messages\n", sess.ID, titleOr(sess.Title),
		sess.StartedAt.Format(searchDateLayout), len(ms))
	for i, m := range ms {
		line := formatStored(m)
		if t.MaxChars > 0 && b.Len()+len(line) > t.MaxChars && i > 0 {
			fmt.Fprintf(&b, "[%d more messages; call again with session_id and around_message_id=%d to read on]\n",
				len(ms)-i, m.ID)
			break
		}
		b.WriteString(line)
	}
	return b.String(), nil
}

func (t SessionSearchTool) scroll(ctx context.Context, a sessionSearchArgs) (string, error) {
	win := a.Window
	if win <= 0 {
		win = scrollDefaultWin
	}
	win = min(win, scrollMaxWin)
	ms, err := t.Store.Around(ctx, a.SessionID, a.AroundMessageID, win)
	if errors.Is(err, sessions.ErrNotFound) {
		return "", toolErr("session %s has no message %d", a.SessionID, a.AroundMessageID)
	}
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "session %s · %d messages around msg %d\n", a.SessionID, len(ms), a.AroundMessageID)
	for _, m := range ms {
		b.WriteString(formatStored(m))
	}
	return b.String(), nil
}

// formatStored returns one message as "msg <id> [role] content" and a line
// end. A tool result names its tool. An assistant message lists its tool
// calls.
func formatStored(m sessions.StoredMessage) string {
	role := string(m.Msg.Role)
	if m.Msg.Role == provider.RoleTool && m.Msg.ToolName != "" {
		role += " " + m.Msg.ToolName
	}
	text := m.Msg.Content
	if len(m.Msg.ToolCalls) > 0 {
		names := make([]string, len(m.Msg.ToolCalls))
		for i, c := range m.Msg.ToolCalls {
			names[i] = c.Name
		}
		calls := "(tool calls: " + strings.Join(names, ", ") + ")"
		if text == "" {
			text = calls
		} else {
			text += "\n" + calls
		}
	}
	return fmt.Sprintf("msg %d [%s] %s\n", m.ID, role, text)
}

// titleOr returns title, or "(no title)" when it is empty.
func titleOr(title string) string {
	if strings.TrimSpace(title) == "" {
		return "(no title)"
	}
	return title
}

// oneLine replaces the line ends in s with spaces.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
