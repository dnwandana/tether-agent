package dashboard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/dnwandana/tether-agent/internal/ctxmgr"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/sessions"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// The limits of the sessions routes.
const (
	sessListDefault   = 50
	sessListMax       = 200
	sessSearchDefault = 20
	sessSearchMax     = 50
	sessToolTextMax   = 16384
	sessAroundWindow  = 10
	sessAroundMax     = 100
	// sessEdgeStep is the number of rows that one query reads when a page edge moves.
	sessEdgeStep = 200
	// sessEdgeMax is the maximum number of rows that the edges of one page add.
	sessEdgeMax = 2000
)

// SessionRow is one row of the sessions page.
type SessionRow struct {
	ID          string       `json:"id"`
	Title       string       `json:"title"`
	Kind        string       `json:"kind"`
	ParentID    string       `json:"parent_id"`
	Source      string       `json:"source"`
	SourceLabel string       `json:"source_label"`
	LastActive  time.Time    `json:"last_active"`
	Started     time.Time    `json:"started"`
	Messages    int          `json:"messages"`
	CostUSD     float64      `json:"cost_usd"`
	Archived    bool         `json:"archived"`
	ReadOnly    bool         `json:"read_only"`
	Children    []SessionRow `json:"children"`
}

// SessionPage is one page of the sessions list and the count of all matches.
type SessionPage struct {
	Rows  []SessionRow `json:"rows"`
	Total int          `json:"total"`
}

// MessageView is one saved message of a transcript. A negative ID marks a compaction summary.
type MessageView struct {
	ID              int64      `json:"id"`
	Role            string     `json:"role"`
	Created         time.Time  `json:"created"`
	Text            string     `json:"text"`
	Reasoning       string     `json:"reasoning"`
	ReasoningTokens int        `json:"reasoning_tokens"`
	Model           string     `json:"model"`
	Provider        string     `json:"provider"`
	In              int64      `json:"in"`
	Out             int64      `json:"out"`
	Cached          int64      `json:"cached"`
	LatencyMS       int64      `json:"latency_ms"`
	FirstMS         int64      `json:"first_ms"`
	CostUSD         float64    `json:"cost_usd"`
	Unpriced        bool       `json:"unpriced"`
	AnsweredBy      string     `json:"answered_by"`
	Compacted       bool       `json:"compacted"`
	Files           []FileRef  `json:"files"`
	Tools           []ToolView `json:"tools"`
}

// MessagePage is one page of a transcript. A tool result merges into the view of its call, so
// Rows can have fewer items than the page has saved messages. FirstID and LastID are the
// smallest and the largest saved message ids of the page, tool results included. Send FirstID
// as "before" for the older page and LastID as "after" for the newer page. HasOlder and
// HasNewer tell if saved messages exist before FirstID and after LastID. An empty page has
// the ids 0. A page never puts a tool call and its results on two different pages.
type MessagePage struct {
	Rows     []MessageView `json:"rows"`
	FirstID  int64         `json:"first_id"`
	LastID   int64         `json:"last_id"`
	HasOlder bool          `json:"has_older"`
	HasNewer bool          `json:"has_newer"`
}

// SearchHitView is one full-text search hit.
type SearchHitView struct {
	SessionID string    `json:"session_id"`
	Title     string    `json:"title"`
	Snippet   string    `json:"snippet"`
	MessageID int64     `json:"message_id"`
	Date      time.Time `json:"date"`
	Source    string    `json:"source"`
	Kind      string    `json:"kind"`
}

func init() { registerAPI(SessionRow{}, SessionPage{}, MessageView{}, MessagePage{}, SearchHitView{}) }

// SessionsAPI serves the sessions page, the transcript, the search, and the export. Files nil
// gives no file links. ChatTitle nil gives the chat id as the Telegram label. ChatTitle must
// read a cache only and never call Telegram.
type SessionsAPI struct {
	D         Deps
	Files     *Files
	ChatTitle func(chatID, threadID string) (chat, topic string, ok bool)
}

// Register adds the routes of the sessions page to mux.
func (a *SessionsAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sessions", a.list)
	mux.HandleFunc("GET /api/sessions/{id}", a.one)
	mux.HandleFunc("GET /api/sessions/{id}/messages", a.messages)
	mux.HandleFunc("GET /api/sessions/{id}/messages/{mid}", a.message)
	mux.HandleFunc("GET /api/sessions/{id}/tools/{call}", a.tool)
	mux.HandleFunc("GET /api/sessions/{id}/export", a.export)
	mux.HandleFunc("GET /api/search", a.search)
}

// red redacts s with Deps.Redact. A nil Redact returns s.
func (a *SessionsAPI) red(s string) string {
	if a.D.Redact == nil {
		return s
	}
	return a.D.Redact(s)
}

// sessQueryInt parses the query value name as a non-negative number. An empty value gives def.
func sessQueryInt(r *http.Request, name string, def int64) (int64, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("the value of %q is not a valid number", name)
	}
	return n, nil
}

// fail writes the error body for err. A missing session or message gives 404.
func (a *SessionsAPI) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, sessions.ErrNotFound) {
		authError(w, http.StatusNotFound, "Not found.")
		return
	}
	if a.D.Log != nil {
		a.D.Log.Warn("dashboard: sessions route failed", "err", err)
	}
	authError(w, http.StatusInternalServerError, "The request failed.")
}

// row converts one summary to a SessionRow. It reads the cost and, when kids is true, the children.
func (a *SessionsAPI) row(ctx context.Context, s sessions.SessionSummary, kids bool) (SessionRow, error) {
	r := SessionRow{
		ID: s.ID, Title: a.red(s.Title), Kind: s.Kind, ParentID: s.ParentID, Source: s.Source, SourceLabel: a.sourceLabel(s.Session),
		LastActive: s.LastActive, Started: s.StartedAt, Messages: s.MessageCount, Archived: s.Archived,
		ReadOnly: s.Key != SessionKey || slices.Contains(sessions.HiddenChildKinds, s.Kind),
		Children: []SessionRow{},
	}
	usd, _, err := a.D.Sessions.CostSum(ctx, s.ID)
	if err != nil {
		return r, err
	}
	r.CostUSD = usd
	if !kids {
		return r, nil
	}
	children, err := a.D.Sessions.Children(ctx, s.ID)
	if err != nil {
		return r, err
	}
	for _, c := range children {
		cr, err := a.row(ctx, c, false)
		if err != nil {
			return r, err
		}
		r.Children = append(r.Children, cr)
	}
	return r, nil
}

// sourceLabel returns the label of the source column of the sessions page.
func (a *SessionsAPI) sourceLabel(s sessions.Session) string {
	switch s.Source {
	case ChannelName:
		return "Dashboard"
	case "cli":
		return "CLI"
	case "cron":
		return "Cron"
	case "telegram":
		if a.ChatTitle != nil {
			if chat, topic, ok := a.ChatTitle(s.ChatID, s.ThreadID); ok {
				label := "Telegram › " + a.red(chat)
				if topic != "" {
					label += " › " + a.red(topic)
				}
				return label
			}
		}
		return "Telegram › " + s.ChatID
	}
	return s.Source
}

func (a *SessionsAPI) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lq := sessions.ListAllQuery{Source: q.Get("source"), Kind: q.Get("kind"), Search: q.Get("search")}
	if v := q.Get("archived"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			authError(w, http.StatusBadRequest, `The value of "archived" is not a valid bool.`)
			return
		}
		lq.Archived = b
	}
	limit, err := sessQueryInt(r, "limit", sessListDefault)
	if err != nil {
		authError(w, http.StatusBadRequest, err.Error())
		return
	}
	offset, err := sessQueryInt(r, "offset", 0)
	if err != nil {
		authError(w, http.StatusBadRequest, err.Error())
		return
	}
	if limit == 0 {
		limit = sessListDefault
	}
	lq.Limit, lq.Offset = int(min(limit, sessListMax)), int(offset)
	list, total, err := a.D.Sessions.ListAll(r.Context(), lq)
	if err != nil {
		a.fail(w, err)
		return
	}
	page := SessionPage{Rows: make([]SessionRow, 0, len(list)), Total: total}
	for _, s := range list {
		row, err := a.row(r.Context(), s, true)
		if err != nil {
			a.fail(w, err)
			return
		}
		page.Rows = append(page.Rows, row)
	}
	writeJSON(w, page)
}

func (a *SessionsAPI) one(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := a.D.Sessions.Get(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	// Get returns no message count, so one COUNT query adds it.
	sum := sessions.SessionSummary{Session: *sess}
	if n, err := a.countMessages(r.Context(), id); err == nil {
		sum.MessageCount = n
	}
	row, err := a.row(r.Context(), sum, true)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, row)
}

// countMessages returns the number of messages of the session.
func (a *SessionsAPI) countMessages(ctx context.Context, id string) (n int, err error) {
	err = a.D.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE session_id = ?`, id).Scan(&n)
	return n, err
}

func (a *SessionsAPI) messages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	before, err1 := sessQueryInt(r, "before", 0)
	after, err2 := sessQueryInt(r, "after", 0)
	around, err3 := sessQueryInt(r, "around", 0)
	limit, err4 := sessQueryInt(r, "limit", 0)
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		authError(w, http.StatusBadRequest, err.Error())
		return
	}
	if before > 0 && after > 0 {
		authError(w, http.StatusBadRequest, `Use "before" or "after", not both.`)
		return
	}
	ctx := r.Context()
	if _, err := a.D.Sessions.Get(ctx, id); err != nil {
		a.fail(w, err)
		return
	}
	var page []sessions.StoredMessage
	var err error
	if around > 0 {
		window := sessAroundWindow
		if limit > 0 {
			window = int(min(limit, sessAroundMax))
		}
		page, err = a.D.Sessions.Around(ctx, id, around, window)
	} else {
		page, err = a.D.Sessions.MessagesPage(ctx, id, before, after, int(limit))
	}
	if err == nil {
		page, err = a.whole(ctx, id, page, true)
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	out := MessagePage{}
	if out.Rows, err = a.views(ctx, id, page, true); err != nil {
		a.fail(w, err)
		return
	}
	if len(page) > 0 {
		out.FirstID, out.LastID = page[0].ID, page[len(page)-1].ID
		out.HasOlder, err = a.hasMessage(ctx, id, "<", out.FirstID)
		if err == nil {
			out.HasNewer, err = a.hasMessage(ctx, id, ">", out.LastID)
		}
	} else if before > 0 {
		// No message is older than before. Messages can exist from before on.
		out.HasNewer, err = a.hasMessage(ctx, id, ">=", before)
	} else if after > 0 {
		out.HasOlder, err = a.hasMessage(ctx, id, "<=", after)
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, out)
}

// hasMessage tells if the session has a saved message whose id compares to ref with op. op is
// one of "<", "<=", ">", and ">=".
func (a *SessionsAPI) hasMessage(ctx context.Context, sessionID, op string, ref int64) (bool, error) {
	var ok bool
	err := a.D.DB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM messages WHERE session_id = ? AND id `+op+` ?)`,
		sessionID, ref).Scan(&ok)
	return ok, err
}

// isToolCall tells if m is an assistant message with tool calls.
func isToolCall(m sessions.StoredMessage) bool {
	return m.Msg.Role == provider.RoleAssistant && len(m.Msg.ToolCalls) > 0
}

// isToolResult tells if m is the result of a tool call.
func isToolResult(m sessions.StoredMessage) bool { return m.Msg.Role == provider.RoleTool }

// whole moves the edges of page, so that a tool call and its results stay on the same page.
// The agent saves the results of a call directly after the call. When the page ends with a
// call or a result, the end moves forward over the next results. When back is true and the
// page starts with results, the start moves back to their call. The page then can have rows
// that the client has already, and the client replaces them. page has the rows in id order.
// Each query closes its rows before the next query starts.
func (a *SessionsAPI) whole(ctx context.Context, sessionID string, page []sessions.StoredMessage, back bool) ([]sessions.StoredMessage, error) {
	added := 0
	for back && len(page) > 0 && isToolResult(page[0]) && added < sessEdgeMax {
		prev, err := a.D.Sessions.MessagesPage(ctx, sessionID, page[0].ID, 0, sessEdgeStep)
		if err != nil {
			return nil, err
		}
		i := len(prev)
		for i > 0 && isToolResult(prev[i-1]) {
			i--
		}
		// A row that is not a result, or the start of the session, ends the move.
		stop := i > 0 || len(prev) < sessEdgeStep
		if i > 0 && isToolCall(prev[i-1]) {
			i--
		}
		page = append(slices.Clone(prev[i:]), page...)
		added += len(prev) - i
		if stop {
			break
		}
	}
	for len(page) > 0 && added < sessEdgeMax {
		last := page[len(page)-1]
		if !isToolCall(last) && !isToolResult(last) {
			break
		}
		next, err := a.D.Sessions.MessagesPage(ctx, sessionID, 0, last.ID, sessEdgeStep)
		if err != nil {
			return nil, err
		}
		n := 0
		for n < len(next) && isToolResult(next[n]) {
			n++
		}
		page = append(page, next[:n]...)
		added += n
		if n < len(next) || len(next) < sessEdgeStep {
			break
		}
	}
	return page, nil
}

func (a *SessionsAPI) message(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	mid, err := strconv.ParseInt(r.PathValue("mid"), 10, 64)
	if err != nil || mid <= 0 {
		authError(w, http.StatusNotFound, "Not found.")
		return
	}
	// The page also reads the results of the calls of the message, so their full text shows.
	page, err := a.D.Sessions.Around(r.Context(), id, mid, 0)
	if err == nil {
		page, err = a.whole(r.Context(), id, page, false)
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	out, err := a.views(r.Context(), id, page, false)
	if err != nil {
		a.fail(w, err)
		return
	}
	for _, v := range out {
		if v.ID == mid {
			writeJSON(w, v)
			return
		}
	}
	authError(w, http.StatusNotFound, "Not found.")
}

// tool answers with one ToolView that has the full result text of the tool call.
func (a *SessionsAPI) tool(w http.ResponseWriter, r *http.Request) {
	id, call := r.PathValue("id"), r.PathValue("call")
	ctx := r.Context()
	var name, text string
	err := a.D.DB.QueryRowContext(ctx, `SELECT tool_name, content FROM messages
		WHERE session_id = ? AND role = 'tool' AND tool_call_id = ? ORDER BY id LIMIT 1`, id, call).Scan(&name, &text)
	if errors.Is(err, sql.ErrNoRows) {
		authError(w, http.StatusNotFound, "Not found.")
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	var args string
	err = a.D.DB.QueryRowContext(ctx, `SELECT json_extract(j.value, '$.Args') FROM messages a, json_each(a.tool_calls) j
		WHERE a.session_id = ? AND a.role = 'assistant' AND json_extract(j.value, '$.ID') = ? LIMIT 1`, id, call).Scan(&args)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		a.fail(w, err)
		return
	}
	writeJSON(w, ToolView{Call: call, Name: name, Emoji: tools.Emoji(name), Preview: a.red(tools.ArgPreview(name, args, 0)),
		Args: a.red(args), State: "done", Text: a.red(text)})
}

func (a *SessionsAPI) export(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	if _, err := a.D.Sessions.Get(ctx, id); err != nil {
		a.fail(w, err)
		return
	}
	f, err := os.CreateTemp("", "tether-export-*.jsonl")
	if err != nil {
		a.fail(w, err)
		return
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}()
	if _, err := a.D.Sessions.ExportJSONL(ctx, id, f); err != nil {
		a.fail(w, err)
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		a.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="`+id+`.jsonl"`)
	_, _ = io.Copy(w, f)
}

func (a *SessionsAPI) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		authError(w, http.StatusBadRequest, `The search needs a "q" value.`)
		return
	}
	limit, err := sessQueryInt(r, "limit", sessSearchDefault)
	if err != nil {
		authError(w, http.StatusBadRequest, err.Error())
		return
	}
	if limit == 0 {
		limit = sessSearchDefault
	}
	offset, err := sessQueryInt(r, "offset", 0)
	if err != nil {
		authError(w, http.StatusBadRequest, err.Error())
		return
	}
	f := sessions.SearchFilter{Limit: int(min(limit, sessSearchMax)), Offset: int(min(offset, int64(1<<30))),
		Source: r.URL.Query().Get("source"), Kind: r.URL.Query().Get("kind")}
	if v := r.URL.Query().Get("archived"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			authError(w, http.StatusBadRequest, `The value of "archived" is not a valid bool.`)
			return
		}
		f.Archived = &b
	}
	hits, err := a.D.Sessions.SearchFiltered(r.Context(), q, f, sessions.Scope{All: true})
	if errors.Is(err, sessions.ErrQuerySyntax) {
		authError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]SearchHitView, 0, len(hits))
	for _, h := range hits {
		out = append(out, SearchHitView{SessionID: h.SessionID, Title: a.red(h.Title), Snippet: a.red(h.Snippet),
			MessageID: h.MessageID, Date: h.Date, Source: h.Source, Kind: h.Kind})
	}
	writeJSON(w, out)
}

// views converts one page of saved messages to MessageView values. cut is false for "Show all".
func (a *SessionsAPI) views(ctx context.Context, sessionID string, page []sessions.StoredMessage, cut bool) ([]MessageView, error) {
	comps, err := ctxmgr.LoadCompactions(ctx, a.D.DB, sessionID)
	if err != nil {
		return nil, err
	}
	toolText := map[string]string{} // tool call id to the result text, for calls on this page
	hasCall := map[string]bool{}
	for _, m := range page {
		for _, c := range m.Msg.ToolCalls {
			hasCall[c.ID] = true
		}
	}
	for _, m := range page {
		if m.Msg.Role == provider.RoleTool && hasCall[m.Msg.ToolCallID] {
			toolText[m.Msg.ToolCallID] = m.Msg.Content
		}
	}
	limit := sessToolTextMax
	if !cut {
		limit = int(^uint(0) >> 1)
	}
	toolView := func(call, name, args, text string) ToolView {
		t, was := cutText(a.red(text), limit)
		return ToolView{Call: call, Name: name, Emoji: tools.Emoji(name), Preview: a.red(tools.ArgPreview(name, args, 0)),
			Args: a.red(args), State: "done", Text: t, Cut: was}
	}
	inRange := func(id int64) bool {
		for _, c := range comps {
			if id >= c.FromMessageID && id <= c.UptoMessageID {
				return true
			}
		}
		return false
	}
	out := make([]MessageView, 0, len(page))
	for _, m := range page {
		if m.Msg.Role == provider.RoleTool && hasCall[m.Msg.ToolCallID] {
			continue
		}
		v := MessageView{ID: m.ID, Role: string(m.Msg.Role), Created: m.At, Compacted: inRange(m.ID), Files: []FileRef{}, Tools: []ToolView{}}
		text := m.Msg.Content
		if a.Files != nil && (m.Msg.Role == provider.RoleUser || m.Msg.Role == provider.RoleAssistant) {
			var files []FileRef
			text, files = a.Files.Rewrite(text, true)
			if files != nil {
				v.Files = files
			}
		}
		if m.Msg.Role == provider.RoleTool {
			v.Tools = append(v.Tools, toolView(m.Msg.ToolCallID, m.Msg.ToolName, "", m.Msg.Content))
		} else {
			v.Text = a.red(text)
		}
		if r := m.Msg.Reasoning; r != nil {
			v.Reasoning = a.red(r.Text)
			v.ReasoningTokens = len(r.Text) / 4
		}
		u := m.Meta.Usage
		v.Model, v.Provider = m.Meta.Model, m.Meta.Provider
		v.In, v.Out, v.Cached = int64(u.InputTokens), int64(u.OutputTokens), int64(u.CacheReadTokens)
		v.LatencyMS, v.FirstMS, v.CostUSD = u.TotalMS, u.FirstTokenMS, m.Meta.CostUSD
		v.Unpriced = m.Msg.Role == provider.RoleAssistant && m.Meta.Model != "" && m.Meta.CostUSD == 0
		for _, c := range m.Msg.ToolCalls {
			v.Tools = append(v.Tools, toolView(c.ID, c.Name, c.Args, toolText[c.ID]))
		}
		out = append(out, v)
		for _, c := range comps {
			if c.UptoMessageID == m.ID {
				out = append(out, MessageView{ID: -c.ID, Role: "summary", Created: m.At, Text: a.red(c.Summary), Compacted: true,
					Files: []FileRef{}, Tools: []ToolView{}})
			}
		}
	}
	return out, nil
}
