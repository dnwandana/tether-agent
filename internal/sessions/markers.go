package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// QueuedText is one text that waits behind a running turn. Lost counts the texts that the
// queue dropped, so the resume can tell the model about them.
type QueuedText struct {
	Text string `json:"text"`
	Lost int    `json:"lost"`
}

// TurnMarker is one row of the turn_markers table. It records a busy session key, so the
// next gateway can continue a turn that a restart cut.
type TurnMarker struct {
	SessionKey, SessionID string
	UpdatedAt             time.Time
	Resumes               int
	Queued                []QueuedText
}

// UpsertMarker writes the marker of a busy key. A new row starts with zero resumes. An
// existing row keeps its resumes and gets the new session, queue, and time.
func (s *Store) UpsertMarker(ctx context.Context, key, sessionID string, queued []QueuedText, now time.Time) error {
	if queued == nil {
		queued = []QueuedText{}
	}
	b, err := json.Marshal(queued)
	if err != nil {
		return fmt.Errorf("encode the queue of marker %s: %w", key, err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO turn_markers (session_key, session_id, updated_at, queued)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(session_key) DO UPDATE SET session_id = excluded.session_id,
			updated_at = excluded.updated_at, queued = excluded.queued`,
		key, sessionID, now.UnixMilli(), string(b))
	if err != nil {
		return fmt.Errorf("write marker %s: %w", key, err)
	}
	return nil
}

// TouchMarker sets the time of a marker. A missing marker is not an error.
func (s *Store) TouchMarker(ctx context.Context, key string, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE turn_markers SET updated_at = ? WHERE session_key = ?`,
		now.UnixMilli(), key); err != nil {
		return fmt.Errorf("touch marker %s: %w", key, err)
	}
	return nil
}

// SetMarkerResumes sets the resume count of a marker.
func (s *Store) SetMarkerResumes(ctx context.Context, key string, resumes int) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE turn_markers SET resumes = ? WHERE session_key = ?`,
		resumes, key); err != nil {
		return fmt.Errorf("set resumes of marker %s: %w", key, err)
	}
	return nil
}

// DeleteMarker removes a marker. A missing marker is not an error.
func (s *Store) DeleteMarker(ctx context.Context, key string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM turn_markers WHERE session_key = ?`, key); err != nil {
		return fmt.Errorf("delete marker %s: %w", key, err)
	}
	return nil
}

// Markers returns all markers ordered by session key. It closes the rows before it returns,
// because the database has one connection.
func (s *Store) Markers(ctx context.Context) ([]TurnMarker, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT session_key, session_id, updated_at, resumes, queued
		FROM turn_markers ORDER BY session_key`)
	if err != nil {
		return nil, fmt.Errorf("read markers: %w", err)
	}
	defer rows.Close()
	var out []TurnMarker
	for rows.Next() {
		var m TurnMarker
		var ms int64
		var q string
		if err := rows.Scan(&m.SessionKey, &m.SessionID, &ms, &m.Resumes, &q); err != nil {
			return nil, fmt.Errorf("read markers: %w", err)
		}
		m.UpdatedAt = time.UnixMilli(ms)
		if err := json.Unmarshal([]byte(q), &m.Queued); err != nil {
			return nil, fmt.Errorf("decode the queue of marker %s: %w", m.SessionKey, err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read markers: %w", err)
	}
	return out, nil
}
