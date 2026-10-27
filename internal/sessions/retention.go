package sessions

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// AutomaticKinds are the session kinds that the program makes without a user: one session
// for each cron run, each delegate_task child, and each background review. PruneAutomatic
// deletes only these kinds. It never deletes a chat session.
var AutomaticKinds = []string{"cron", "subagent", "review"}

// pruneBatch is the maximum number of sessions that one transaction of PruneAutomatic
// deletes. Small transactions keep the write lock of state.db short.
const pruneBatch = 100

// batchRunning is the value of delegation_batches.status while a batch runs
// (agent.BatchRunning; sessions cannot import agent).
const batchRunning = "running"

// PruneAutomatic deletes the sessions of AutomaticKinds whose last_active is before cutoff,
// with all their related rows. It skips a session that is the parent of a running
// delegation batch. Each transaction deletes at most pruneBatch sessions and selects them
// again under the write lock, so a session that becomes active again stays. It returns the
// ids of the deleted sessions, also when a later transaction fails.
func (s *Store) PruneAutomatic(ctx context.Context, cutoff time.Time) ([]string, error) {
	var deleted []string
	for {
		ids, err := s.pruneOnce(ctx, cutoff)
		deleted = append(deleted, ids...)
		if err != nil {
			return deleted, fmt.Errorf("sessions: prune the sessions before %s: %w", cutoff.UTC().Format(time.RFC3339), err)
		}
		if len(ids) < pruneBatch {
			return deleted, nil
		}
	}
}

// pruneOnce selects and deletes one batch of old automatic sessions in one transaction.
func (s *Store) pruneOnce(ctx context.Context, cutoff time.Time) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	args := make([]any, 0, len(AutomaticKinds)+3)
	for _, k := range AutomaticKinds {
		args = append(args, k)
	}
	args = append(args, cutoff.UnixMilli(), batchRunning, pruneBatch)
	ids, err := queryStrings(ctx, tx, `SELECT id FROM sessions
		WHERE kind IN (`+placeholders(len(AutomaticKinds))+`) AND last_active < ?
		AND id NOT IN (SELECT parent_session_id FROM delegation_batches WHERE status = ?)
		ORDER BY last_active, id LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if err := deleteSessions(ctx, tx, ids); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

// deleteSessions removes the sessions with ids and their related rows inside tx:
//   - the messages (the FTS triggers remove their index rows),
//   - the compaction rows and the key mappings,
//   - the delegation batches that the sessions started and that do not run now.
//
// It keeps the child sessions (forks, children, reviews) and clears their parent_id, as
// Hermes does: a fork is a user session, and an old automatic child goes in a later prune.
func deleteSessions(ctx context.Context, tx *sql.Tx, ids []string) error {
	in := placeholders(len(ids))
	idArgs := make([]any, len(ids))
	for i, id := range ids {
		idArgs[i] = id
	}
	for _, q := range []string{
		`UPDATE sessions SET parent_id = '' WHERE parent_id IN (` + in + `)`,
		`DELETE FROM messages WHERE session_id IN (` + in + `)`,
		`DELETE FROM compactions WHERE session_id IN (` + in + `)`,
		`DELETE FROM active_session WHERE session_id IN (` + in + `)`,
	} {
		if _, err := tx.ExecContext(ctx, q, idArgs...); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM delegation_batches WHERE status != ?
		AND parent_session_id IN (`+in+`)`, append([]any{batchRunning}, idArgs...)...); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id IN (`+in+`)`, idArgs...)
	return err
}

// placeholders returns n question marks that commas separate.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// queryStrings returns the string column of each row of the query.
func queryStrings(ctx context.Context, q querier, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
