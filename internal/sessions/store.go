// Package sessions stores the conversations and their messages in state.db.
package sessions

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/security"
)

// ErrNotFound is the error when a session id has no row.
var ErrNotFound = errors.New("no session")

// Session is one row of the sessions table. Kind is chat, cron, subagent, or review.
type Session struct {
	ID, Key, Source, ChatID, ThreadID, UserID, Title, ModelOverride, PromptOverride, ParentID, Kind string
	StartedAt, LastActive                                                                           time.Time
	Archived                                                                                        bool
}

// MessageMeta holds the data about one message that does not go to the provider.
// A CostUSD of 0 means that the price is not known, and the store writes NULL.
type MessageMeta struct {
	Usage                         provider.Usage
	Model, Provider, FinishReason string
	CostUSD                       float64
	Observed                      bool
}

// StoredMessage is one row of the messages table.
type StoredMessage struct {
	ID        int64
	Msg       provider.Message
	Meta      MessageMeta
	Compacted bool
	At        time.Time
}

// Store reads and writes the sessions, the messages, and the active session keys.
// It is safe for concurrent use.
type Store struct {
	db *db.DB
	// Redactor masks the secrets in an export. A nil Redactor applies the patterns only.
	Redactor *security.Redactor
}

// SessionSummary is a session with its message count, for the session lists.
type SessionSummary struct {
	Session
	MessageCount int
}

// New returns a Store over d.
func New(d *db.DB) *Store { return &Store{db: d} }

// sessionCols is the column list that scanSession reads, in order.
const sessionCols = `id, session_key, source, chat_id, thread_id, user_id, title, model_override,
	prompt_override, parent_id, kind, started_at, last_active, archived`

// newID returns the UTC time in milliseconds as 13 hex characters plus 6 random hex
// characters. The ids sort by creation time.
func newID(now time.Time) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%013x%s", now.UnixMilli(), hex.EncodeToString(b[:]))
}

// Create inserts a new session. It sets the id, StartedAt, and LastActive. An empty Kind
// becomes "chat".
func (s *Store) Create(ctx context.Context, in Session) (*Session, error) {
	now := time.Now().UTC()
	in.ID = newID(now)
	in.StartedAt, in.LastActive = now, now
	if in.Kind == "" {
		in.Kind = "chat"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (`+sessionCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.Key, in.Source, in.ChatID, in.ThreadID, in.UserID, in.Title, in.ModelOverride,
		in.PromptOverride, in.ParentID, in.Kind, now.UnixMilli(), now.UnixMilli(), boolInt(in.Archived))
	if err != nil {
		return nil, fmt.Errorf("sessions: create: %w", err)
	}
	return &in, nil
}

// Get returns the session with the id. The error wraps ErrNotFound when no row has the id.
func (s *Store) Get(ctx context.Context, id string) (*Session, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+sessionCols+` FROM sessions WHERE id = ?`, id)
	sess, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("sessions: get %s: %w", id, err)
	}
	return sess, nil
}

// Active returns the session that key uses now. It returns nil, nil when the key has no
// session, or when the session of the key does not exist any more.
func (s *Store) Active(ctx context.Context, key string) (*Session, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+prefixCols("s.")+` FROM active_session a
		JOIN sessions s ON s.id = a.session_id WHERE a.session_key = ?`, key)
	sess, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sessions: active %s: %w", key, err)
	}
	return sess, nil
}

// SetActive points key at the session with sessionID.
func (s *Store) SetActive(ctx context.Context, key, sessionID string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO active_session (session_key, session_id) VALUES (?, ?)
		ON CONFLICT(session_key) DO UPDATE SET session_id = excluded.session_id`, key, sessionID)
	if err != nil {
		return fmt.Errorf("sessions: set active %s: %w", key, err)
	}
	return nil
}

// ActiveOrCreate returns the active session of key. When the key has no session, it creates
// one from tmpl with the key set and makes it active. Two callers at the same time get the
// same session: INSERT OR IGNORE keeps the first mapping, and the loser deletes its own row.
func (s *Store) ActiveOrCreate(ctx context.Context, key string, tmpl Session) (*Session, error) {
	if sess, err := s.Active(ctx, key); sess != nil || err != nil {
		return sess, err
	}
	tmpl.Key = key
	created, err := s.Create(ctx, tmpl)
	if err != nil {
		return nil, err
	}
	// A mapping to a deleted session blocks the INSERT OR IGNORE. Remove it first.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM active_session WHERE session_key = ?
		AND session_id NOT IN (SELECT id FROM sessions)`, key); err != nil {
		return nil, fmt.Errorf("sessions: active or create %s: %w", key, err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO active_session (session_key, session_id)
		VALUES (?, ?)`, key, created.ID); err != nil {
		return nil, fmt.Errorf("sessions: active or create %s: %w", key, err)
	}
	sess, err := s.Active(ctx, key)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, fmt.Errorf("sessions: active or create %s: the key has no session after the insert", key)
	}
	if sess.ID != created.ID {
		// Another caller won. Its session is the active one, so remove the unused row.
		if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, created.ID); err != nil {
			return nil, fmt.Errorf("sessions: active or create %s: %w", key, err)
		}
	}
	return sess, nil
}

// Append writes one message and updates last_active of the session. It returns the message
// id. Each message is one INSERT with no buffer, so a crash does not lose a written message.
// Image bytes are not stored. The content gets an "[image: <path>]" marker for each image.
func (s *Store) Append(ctx context.Context, sessionID string, m provider.Message, meta MessageMeta) (int64, error) {
	content := m.Content
	for _, img := range m.Images {
		if content != "" {
			content += "\n"
		}
		content += "[image: " + img.Path + "]"
	}
	var toolCalls any
	if len(m.ToolCalls) > 0 {
		b, err := json.Marshal(m.ToolCalls)
		if err != nil {
			return 0, fmt.Errorf("sessions: append to %s: %w", sessionID, err)
		}
		toolCalls = string(b)
	}
	var cost any
	if meta.CostUSD != 0 {
		cost = meta.CostUSD
	}
	now := time.Now().UnixMilli()
	res, err := s.db.ExecContext(ctx, `INSERT INTO messages (session_id, role, content, tool_call_id,
		tool_calls, tool_name, input_tokens, output_tokens, cache_read_tokens, latency_ms,
		latency_first_ms, cost_usd, model, provider, finish_reason, observed, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, string(m.Role), content, m.ToolCallID, toolCalls, m.ToolName,
		meta.Usage.InputTokens, meta.Usage.OutputTokens, meta.Usage.CacheReadTokens,
		meta.Usage.TotalMS, meta.Usage.FirstTokenMS, cost, meta.Model, meta.Provider,
		meta.FinishReason, boolInt(meta.Observed), now)
	if err != nil {
		return 0, fmt.Errorf("sessions: append to %s: %w", sessionID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("sessions: append to %s: %w", sessionID, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_active = ? WHERE id = ?`, now, sessionID); err != nil {
		return id, fmt.Errorf("sessions: update last_active of %s: %w", sessionID, err)
	}
	return id, nil
}

// ListQuery selects one page of the sessions of a key.
type ListQuery struct {
	Offset, Limit int
	Search        string // case-insensitive match on the title; "" for all
	Exclude       string // a session ID to leave out; "" for none
	Archived      bool   // true: only archived sessions; false: only not archived
}

// ListPage returns one page of the sessions of key, with the most recently active session
// first, and the number of sessions that match q on all pages. A Limit of 0 or less gives
// all the rows from Offset.
func (s *Store) ListPage(ctx context.Context, key string, q ListQuery) (page []SessionSummary, total int, err error) {
	where := `s.session_key = ? AND s.archived = ?`
	args := []any{key, boolInt(q.Archived)}
	if q.Exclude != "" {
		where += ` AND s.id != ?`
		args = append(args, q.Exclude)
	}
	if q.Search != "" {
		where += ` AND s.title LIKE ? ESCAPE '\' COLLATE NOCASE`
		args = append(args, "%"+escapeLike(q.Search)+"%")
	}
	// The count query is done before the page query starts. The pool has one connection,
	// so a second query while rows are open deadlocks.
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions s WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("sessions: list %s: %w", key, err)
	}
	if total == 0 || q.Offset >= total {
		return nil, total, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = -1 // SQLite reads a negative LIMIT as no limit.
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixCols("s.")+`,
		(SELECT count(*) FROM messages m WHERE m.session_id = s.id)
		FROM sessions s WHERE `+where+`
		ORDER BY s.last_active DESC, s.id DESC LIMIT ? OFFSET ?`, append(args, limit, q.Offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("sessions: list %s: %w", key, err)
	}
	defer rows.Close()
	for rows.Next() {
		var count int
		sess, err := scanSession(rows, &count)
		if err != nil {
			return nil, 0, fmt.Errorf("sessions: list %s: %w", key, err)
		}
		page = append(page, SessionSummary{Session: *sess, MessageCount: count})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("sessions: list %s: %w", key, err)
	}
	return page, total, nil
}

// escapeLike escapes the LIKE wildcards and the escape character, so that the words match
// literally.
func escapeLike(words string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(words)
}

// CountArchived returns the number of archived sessions of key.
func (s *Store) CountArchived(ctx context.Context, key string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions
		WHERE session_key = ? AND archived = 1`, key).Scan(&n); err != nil {
		return 0, fmt.Errorf("sessions: count the archived sessions of %s: %w", key, err)
	}
	return n, nil
}

// CountMessages returns the number of messages of the session with the id. An unknown
// id gives 0.
func (s *Store) CountMessages(ctx context.Context, id string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE session_id = ?`, id).Scan(&n); err != nil {
		return 0, fmt.Errorf("sessions: count the messages of %s: %w", id, err)
	}
	return n, nil
}

// Rename sets the title of the session.
func (s *Store) Rename(ctx context.Context, id, title string) error {
	return s.setColumn(ctx, id, "title", title)
}

// Archive moves the session to the archived list of ListPage. The session and its
// messages stay.
func (s *Store) Archive(ctx context.Context, id string) error {
	return s.setColumn(ctx, id, "archived", 1)
}

// Unarchive moves the session back from the archived list of ListPage.
func (s *Store) Unarchive(ctx context.Context, id string) error {
	return s.setColumn(ctx, id, "archived", 0)
}

// SetModel sets the model override of the session: "name", "name:model", or "" for none.
func (s *Store) SetModel(ctx context.Context, id, v string) error {
	return s.setColumn(ctx, id, "model_override", v)
}

// SetPrompt sets the prompt override of the session. "" removes it.
func (s *Store) SetPrompt(ctx context.Context, id, v string) error {
	return s.setColumn(ctx, id, "prompt_override", v)
}

// titleMax is the maximum number of runes of an automatic title.
const titleMax = 60

// SetTitleIfEmpty sets the title from the first user text when the session has no title.
// The title is the text on one line, cut to 60 runes at the last space.
func (s *Store) SetTitleIfEmpty(ctx context.Context, id, firstUserText string) error {
	title := autoTitle(firstUserText)
	if title == "" {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET title = ? WHERE id = ? AND title = ''`, title, id); err != nil {
		return fmt.Errorf("sessions: set the title of %s: %w", id, err)
	}
	return nil
}

// autoTitle returns text on one line, cut to titleMax runes at the last space.
func autoTitle(text string) string {
	r := []rune(strings.Join(strings.Fields(text), " "))
	if len(r) <= titleMax {
		return string(r)
	}
	if r[titleMax] == ' ' {
		return string(r[:titleMax])
	}
	cut := r[:titleMax]
	for i := len(cut) - 1; i > 0; i-- {
		if cut[i] == ' ' {
			return strings.TrimSpace(string(cut[:i]))
		}
	}
	return string(cut)
}

// Delete removes the session, its messages, its compaction rows, the key mappings to it,
// and its finished delegation batches, in one transaction. The FTS triggers remove the
// messages from the index. The child sessions (forks, children, reviews) stay with an
// empty parent_id, as in Hermes.
func (s *Store) Delete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sessions: delete %s: %w", id, err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE id = ?`, id).Scan(&n); err != nil {
		return fmt.Errorf("sessions: delete %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("%w %s", ErrNotFound, id)
	}
	if err := deleteSessions(ctx, tx, []string{id}); err != nil {
		return fmt.Errorf("sessions: delete %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sessions: delete %s: %w", id, err)
	}
	return nil
}

// setColumn sets one column of the session row. col is a constant of this file.
func (s *Store) setColumn(ctx context.Context, id, col string, v any) error {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET `+col+` = ? WHERE id = ?`, v, id)
	if err != nil {
		return fmt.Errorf("sessions: set %s of %s: %w", col, id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sessions: set %s of %s: %w", col, id, err)
	}
	if n == 0 {
		return fmt.Errorf("%w %s", ErrNotFound, id)
	}
	return nil
}

// messageCols is the column list that scanMessage reads, in order.
const messageCols = `id, role, content, tool_call_id, tool_calls, tool_name, input_tokens,
	output_tokens, cache_read_tokens, latency_ms, latency_first_ms, cost_usd, model, provider,
	finish_reason, compacted, observed, timestamp`

// Messages returns all the messages of the session in id order.
func (s *Store) Messages(ctx context.Context, sessionID string) ([]StoredMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+messageCols+` FROM messages
		WHERE session_id = ? ORDER BY id`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("sessions: messages of %s: %w", sessionID, err)
	}
	defer rows.Close()
	var out []StoredMessage
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("sessions: messages of %s: %w", sessionID, err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sessions: messages of %s: %w", sessionID, err)
	}
	return out, nil
}

// scanner is a *sql.Row or a *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

// scanSession reads the sessionCols columns, then the columns of extra, if any.
func scanSession(r scanner, extra ...any) (*Session, error) {
	var (
		s                Session
		started, lastAct int64
		archived         int
	)
	dest := append(make([]any, 0, 14+len(extra)), &s.ID, &s.Key, &s.Source, &s.ChatID, &s.ThreadID, &s.UserID, &s.Title,
		&s.ModelOverride, &s.PromptOverride, &s.ParentID, &s.Kind, &started, &lastAct, &archived)
	if err := r.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	s.StartedAt = time.UnixMilli(started).UTC()
	s.LastActive = time.UnixMilli(lastAct).UTC()
	s.Archived = archived != 0
	return &s, nil
}

func scanMessage(r scanner) (StoredMessage, error) {
	var (
		m                   StoredMessage
		role                string
		toolCalls           sql.NullString
		cost                sql.NullFloat64
		compacted, observed int
		at                  int64
	)
	if err := r.Scan(&m.ID, &role, &m.Msg.Content, &m.Msg.ToolCallID, &toolCalls, &m.Msg.ToolName,
		&m.Meta.Usage.InputTokens, &m.Meta.Usage.OutputTokens, &m.Meta.Usage.CacheReadTokens,
		&m.Meta.Usage.TotalMS, &m.Meta.Usage.FirstTokenMS, &cost, &m.Meta.Model, &m.Meta.Provider,
		&m.Meta.FinishReason, &compacted, &observed, &at); err != nil {
		return m, err
	}
	m.Msg.Role = provider.Role(role)
	if toolCalls.Valid && toolCalls.String != "" {
		if err := json.Unmarshal([]byte(toolCalls.String), &m.Msg.ToolCalls); err != nil {
			return m, fmt.Errorf("message %d: bad tool_calls JSON: %w", m.ID, err)
		}
	}
	m.Meta.CostUSD = cost.Float64
	m.Compacted = compacted != 0
	m.Meta.Observed = observed != 0
	m.At = time.UnixMilli(at).UTC()
	return m, nil
}

// prefixCols returns sessionCols with each column name prefixed by p.
func prefixCols(p string) string {
	cols := strings.Split(sessionCols, ",")
	for i, c := range cols {
		cols[i] = p + strings.TrimSpace(c)
	}
	return strings.Join(cols, ", ")
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
