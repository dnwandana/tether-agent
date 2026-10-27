package sessions

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Fork copies the session with the id into a new session with parent_id set to id. The
// copy has all the messages in order and all the compaction rows, with their message ids
// changed to the ids of the copies. An empty title gives "<old title> (fork)", or
// "fork of <id>" when the old session has no title.
// All copies happen in one transaction, so a failure leaves no partial session.
func (s *Store) Fork(ctx context.Context, id, title string) (*Session, error) {
	orig, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if title == "" {
		title = orig.Title + " (fork)"
		if orig.Title == "" {
			title = "fork of " + orig.ID
		}
	}
	now := time.Now().UTC()
	f := *orig
	f.ID = newID(now)
	f.ParentID = orig.ID
	f.Title = title
	f.StartedAt, f.LastActive = now, now
	f.Archived = false

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("sessions: fork %s: %w", id, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (`+sessionCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		f.ID, f.Key, f.Source, f.ChatID, f.ThreadID, f.UserID, f.Title, f.ModelOverride,
		f.PromptOverride, f.ParentID, f.Kind, now.UnixMilli(), now.UnixMilli()); err != nil {
		return nil, fmt.Errorf("sessions: fork %s: %w", id, err)
	}

	// Read all the ids first: the connection pool has one connection, so the rows must
	// close before the next statement.
	ids, err := queryIDs(ctx, tx, `SELECT id FROM messages WHERE session_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, fmt.Errorf("sessions: fork %s: %w", id, err)
	}
	newIDs := make(map[int64]int64, len(ids))
	for _, old := range ids {
		res, err := tx.ExecContext(ctx, `INSERT INTO messages (session_id, role, content, tool_call_id,
			tool_calls, tool_name, input_tokens, output_tokens, cache_read_tokens, latency_ms,
			latency_first_ms, cost_usd, model, provider, finish_reason, compacted, observed, timestamp)
			SELECT ?, role, content, tool_call_id, tool_calls, tool_name, input_tokens, output_tokens,
			cache_read_tokens, latency_ms, latency_first_ms, cost_usd, model, provider, finish_reason,
			compacted, observed, timestamp FROM messages WHERE id = ?`, f.ID, old)
		if err != nil {
			return nil, fmt.Errorf("sessions: fork %s: copy message %d: %w", id, old, err)
		}
		if newIDs[old], err = res.LastInsertId(); err != nil {
			return nil, fmt.Errorf("sessions: fork %s: copy message %d: %w", id, old, err)
		}
	}

	type compaction struct {
		from, upto, at int64
		summary        string
	}
	rows, err := tx.QueryContext(ctx, `SELECT from_message_id, upto_message_id, summary, created_at
		FROM compactions WHERE session_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, fmt.Errorf("sessions: fork %s: %w", id, err)
	}
	var comps []compaction
	for rows.Next() {
		var c compaction
		if err := rows.Scan(&c.from, &c.upto, &c.summary, &c.at); err != nil {
			rows.Close()
			return nil, fmt.Errorf("sessions: fork %s: %w", id, err)
		}
		comps = append(comps, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sessions: fork %s: %w", id, err)
	}
	for _, c := range comps {
		if _, err := tx.ExecContext(ctx, `INSERT INTO compactions (session_id, from_message_id,
			upto_message_id, summary, created_at) VALUES (?, ?, ?, ?, ?)`,
			f.ID, newIDs[c.from], newIDs[c.upto], c.summary, c.at); err != nil {
			return nil, fmt.Errorf("sessions: fork %s: copy a compaction: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("sessions: fork %s: %w", id, err)
	}
	return &f, nil
}

// querier is a *sql.Tx or a *sql.DB.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// queryIDs returns the int64 column of each row of the query.
func queryIDs(ctx context.Context, q querier, query string, args ...any) ([]int64, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
