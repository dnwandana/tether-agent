package sessions

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// ListAllQuery selects one page of the sessions of all keys, for the dashboard sessions page.
// An empty Source or Kind matches all. Search matches the title. A Limit of 0 or less gives
// all rows from Offset.
type ListAllQuery struct {
	Source, Kind, Search string
	Archived             bool
	Limit, Offset        int
}

// The page size of MessagesPage.
const (
	pageDefault = 50
	pageMax     = 200
)

// ListAll returns one page of the sessions of all keys, most recently active first, and the
// number of matches on all pages.
func (s *Store) ListAll(ctx context.Context, q ListAllQuery) ([]SessionSummary, int, error) {
	where := `s.archived = ? AND s.kind != ?`
	args := []any{boolInt(q.Archived), KindAux}
	if q.Kind == "" {
		// A hidden child shows under its parent. One that lost its parent stays in the list.
		where += ` AND NOT (s.kind IN (?, ?, ?, ?) AND s.parent_id != '')`
		args = append(args, KindSubagent, KindReview, KindGoalJudge, KindTidy)
	} else {
		where += ` AND s.kind = ?`
		args = append(args, q.Kind)
	}
	if q.Source != "" {
		where += ` AND s.source = ?`
		args = append(args, q.Source)
	}
	if q.Search != "" {
		where += ` AND s.title LIKE ? ESCAPE '\' COLLATE NOCASE`
		args = append(args, "%"+escapeLike(q.Search)+"%")
	}
	// The count query ends before the page query starts. The pool has one connection.
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions s WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("sessions: list all: %w", err)
	}
	if total == 0 || q.Offset >= total {
		return nil, total, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = -1 // SQLite reads a negative LIMIT as no limit.
	}
	page, err := s.querySummaries(ctx, `SELECT `+prefixCols("s.")+`,
		(SELECT count(*) FROM messages m WHERE m.session_id = s.id)
		FROM sessions s WHERE `+where+`
		ORDER BY s.last_active DESC, s.id DESC LIMIT ? OFFSET ?`, append(args, limit, q.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	return page, total, nil
}

// Children returns the hidden child sessions (subagent, review, goal_judge, tidy) of the parent,
// oldest first.
func (s *Store) Children(ctx context.Context, parentID string) ([]SessionSummary, error) {
	return s.querySummaries(ctx, `SELECT `+prefixCols("s.")+`,
		(SELECT count(*) FROM messages m WHERE m.session_id = s.id)
		FROM sessions s WHERE s.parent_id = ? AND s.kind IN (?, ?, ?, ?)
		ORDER BY s.started_at, s.id`, parentID, KindSubagent, KindReview, KindGoalJudge, KindTidy)
}

// querySummaries runs a query that selects the session columns and a message count. It reads
// all rows and closes them before it returns.
func (s *Store) querySummaries(ctx context.Context, query string, args ...any) ([]SessionSummary, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sessions: list all: %w", err)
	}
	defer rows.Close()
	var out []SessionSummary
	for rows.Next() {
		var count int
		sess, err := scanSession(rows, &count)
		if err != nil {
			return nil, fmt.Errorf("sessions: list all: %w", err)
		}
		out = append(out, SessionSummary{Session: *sess, MessageCount: count})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sessions: list all: %w", err)
	}
	return out, nil
}

// MessagesPage returns at most one page of the messages of the session, in id order.
func (s *Store) MessagesPage(ctx context.Context, sessionID string, beforeID, afterID int64, limit int) ([]StoredMessage, error) {
	if beforeID > 0 && afterID > 0 {
		return nil, errors.New("sessions: messages page: use before or after, not both")
	}
	if limit <= 0 {
		limit = pageDefault
	}
	limit = min(limit, pageMax)
	if afterID > 0 {
		return s.queryMessages(ctx, `SELECT `+messageCols+` FROM messages
			WHERE session_id = ? AND id > ? ORDER BY id LIMIT ?`, sessionID, afterID, limit)
	}
	var out []StoredMessage
	var err error
	if beforeID > 0 {
		out, err = s.queryMessages(ctx, `SELECT `+messageCols+` FROM messages
			WHERE session_id = ? AND id < ? ORDER BY id DESC LIMIT ?`, sessionID, beforeID, limit)
	} else {
		out, err = s.queryMessages(ctx, `SELECT `+messageCols+` FROM messages
			WHERE session_id = ? ORDER BY id DESC LIMIT ?`, sessionID, limit)
	}
	if err != nil {
		return nil, err
	}
	slices.Reverse(out)
	return out, nil
}

// CostSum returns the stored cost of the model calls of the session. A row older than the
// start of the session is a fork copy, and it does not count.
func (s *Store) CostSum(ctx context.Context, sessionID string) (usd float64, unpriced bool, err error) {
	var missing int
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(m.cost_usd), 0), COUNT(*) - COUNT(m.cost_usd)
		FROM messages m JOIN sessions s ON s.id = m.session_id
		WHERE m.session_id = ? AND m.role = 'assistant' AND m.model != '' AND m.timestamp >= s.started_at`,
		sessionID).Scan(&usd, &missing)
	if err != nil {
		return 0, false, fmt.Errorf("sessions: cost of %s: %w", sessionID, err)
	}
	return usd, missing > 0, nil
}
