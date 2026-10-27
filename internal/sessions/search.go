package sessions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrQuerySyntax marks an FTS5 query that SQLite cannot parse.
var ErrQuerySyntax = errors.New(`FTS5 query syntax: quote phrases with "..."`)

// Hit is one message that matches a search.
type Hit struct {
	SessionID, Title string
	Date             time.Time
	MessageID        int64
	Snippet          string
}

// Search runs the FTS5 query over the content of all messages, observed and
// archived ones included. It returns at most limit hits, newest first. The
// snippet marks the matches with "[" and "]" and holds about 12 tokens.
func (s *Store) Search(ctx context.Context, query string, limit int) ([]Hit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("sessions: search: the query is empty")
	}
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.session_id, s.title, m.timestamp, m.id,
		snippet(messages_fts, 0, '[', ']', '…', 12)
		FROM messages_fts JOIN messages m ON m.id = messages_fts.rowid
		JOIN sessions s ON s.id = m.session_id
		WHERE messages_fts MATCH ? ORDER BY m.id DESC LIMIT ?`, query, limit)
	if err != nil {
		return nil, searchErr(err)
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var (
			h  Hit
			at int64
		)
		if err := rows.Scan(&h.SessionID, &h.Title, &at, &h.MessageID, &h.Snippet); err != nil {
			return nil, searchErr(err)
		}
		h.Date = time.UnixMilli(at).UTC()
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, searchErr(err)
	}
	return out, nil
}

// searchErr wraps err. A query parse error also wraps ErrQuerySyntax, so its
// text tells the model how to fix the query.
func searchErr(err error) error {
	msg := strings.ToLower(err.Error())
	for _, w := range []string{"fts5", "syntax error", "unterminated", "no such column", "unknown special query"} {
		if strings.Contains(msg, w) {
			return fmt.Errorf("sessions: search: %w (%v)", ErrQuerySyntax, err)
		}
	}
	return fmt.Errorf("sessions: search: %w", err)
}

// Around returns the message messageID of the session and at most window
// messages before and after it, in id order. The error wraps ErrNotFound
// when the session has no message with that id.
func (s *Store) Around(ctx context.Context, sessionID string, messageID int64, window int) ([]StoredMessage, error) {
	if window < 0 {
		window = 0
	}
	var owner string
	err := s.db.QueryRowContext(ctx, `SELECT session_id FROM messages WHERE id = ?`, messageID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && owner != sessionID) {
		return nil, fmt.Errorf("%w message %d in session %s", ErrNotFound, messageID, sessionID)
	}
	if err != nil {
		return nil, fmt.Errorf("sessions: around %d: %w", messageID, err)
	}
	before, err := s.queryMessages(ctx, `SELECT `+messageCols+` FROM messages
		WHERE session_id = ? AND id < ? ORDER BY id DESC LIMIT ?`, sessionID, messageID, window)
	if err != nil {
		return nil, err
	}
	after, err := s.queryMessages(ctx, `SELECT `+messageCols+` FROM messages
		WHERE session_id = ? AND id >= ? ORDER BY id LIMIT ?`, sessionID, messageID, window+1)
	if err != nil {
		return nil, err
	}
	out := make([]StoredMessage, 0, len(before)+len(after))
	for i := len(before) - 1; i >= 0; i-- {
		out = append(out, before[i])
	}
	return append(out, after...), nil
}

// queryMessages runs a query that selects messageCols and reads all rows.
func (s *Store) queryMessages(ctx context.Context, query string, args ...any) ([]StoredMessage, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sessions: messages: %w", err)
	}
	defer rows.Close()
	var out []StoredMessage
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("sessions: messages: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sessions: messages: %w", err)
	}
	return out, nil
}
