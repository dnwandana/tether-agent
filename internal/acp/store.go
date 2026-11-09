package acp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
)

// Record is the ACP session of one Tether session and one coding agent.
type Record struct {
	SessionID, Agent, ACPSessionID, CWD string
	CreatedAt, UpdatedAt                time.Time
}

// Store reads and writes the acp_sessions rows.
//
// The DB has one connection, so each method uses one QueryRowContext or ExecContext call and
// keeps no rows open.
type Store struct {
	db *db.DB
}

// NewStore returns a store on d.
func NewStore(d *db.DB) *Store {
	return &Store{db: d}
}

// Get returns the row of the session and the agent. ok is false when no row exists.
func (s *Store) Get(ctx context.Context, sessionID, agent string) (Record, bool, error) {
	var (
		acpID, cwd       string
		created, updated int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT acp_session_id, cwd, created_at, updated_at
		FROM acp_sessions WHERE session_id = ? AND agent = ?`, sessionID, agent).
		Scan(&acpID, &cwd, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("acp: cannot read the session row of %s: %w", agent, err)
	}
	return Record{
		SessionID:    sessionID,
		Agent:        agent,
		ACPSessionID: acpID,
		CWD:          cwd,
		CreatedAt:    time.UnixMilli(created),
		UpdatedAt:    time.UnixMilli(updated),
	}, true, nil
}

// Put writes r and replaces an old row with the same session and agent. A zero CreatedAt or
// UpdatedAt gets the current time.
func (s *Store) Put(ctx context.Context, r Record) error {
	// A zero time.Time gives a large negative Unix ms value, so it must not go to the table.
	now := time.Now()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = now
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO acp_sessions
		(session_id, agent, acp_session_id, cwd, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		r.SessionID, r.Agent, r.ACPSessionID, r.CWD, r.CreatedAt.UnixMilli(), r.UpdatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("acp: cannot save the session row of %s: %w", r.Agent, err)
	}
	return nil
}

// Delete removes the row of the session and the agent. A missing row is not an error.
func (s *Store) Delete(ctx context.Context, sessionID, agent string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM acp_sessions WHERE session_id = ? AND agent = ?`,
		sessionID, agent)
	if err != nil {
		return fmt.Errorf("acp: cannot delete the session row of %s: %w", agent, err)
	}
	return nil
}
