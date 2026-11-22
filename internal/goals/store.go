package goals

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
)

// ErrConflict means that another writer changed the goal row after the read.
var ErrConflict = errors.New("goals: the goal changed during the update")

// The Hermes defaults that decodeState applies.
const (
	defaultMaxTurns       = 20
	defaultGateTimeoutS   = 300
	defaultGateMaxRetries = 3
)

// Store keeps the goal of each session in the goals table. Put is a compare-and-set on the
// revision of the row.
type Store struct {
	DB *db.DB
	// GateTimeoutS is the time limit that a stored gate with 0 gets. 0 means defaultGateTimeoutS.
	GateTimeoutS int
	// GateMaxRetries is the retry count that a stored gate with 0 gets. 0 means defaultGateMaxRetries.
	GateMaxRetries int
}

// ActiveGoal is one active goal and the revision of its row.
type ActiveGoal struct {
	SessionID string
	State     State
	Revision  int64
}

// Get returns the goal of the session and the revision of its row. No row gives a zero
// State and revision 0.
func (s *Store) Get(ctx context.Context, sessionID string) (State, int64, error) {
	var raw string
	var rev int64
	err := s.DB.QueryRowContext(ctx, `SELECT state, revision FROM goals WHERE session_id = ?`, sessionID).
		Scan(&raw, &rev)
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, 0, nil
	}
	if err != nil {
		return State{}, 0, fmt.Errorf("goals: get the goal of session %s: %w", sessionID, err)
	}
	st, err := decodeState(raw, s.GateTimeoutS, s.GateMaxRetries)
	if err != nil {
		return State{}, 0, fmt.Errorf("goals: decode the goal of session %s: %w", sessionID, err)
	}
	return st, rev, nil
}

// UpdatedAt returns the time of the last write of the goal row (updated_at, Unix
// milliseconds). No row gives the zero time.
func (s *Store) UpdatedAt(ctx context.Context, sessionID string) (time.Time, error) {
	var ms int64
	err := s.DB.QueryRowContext(ctx, `SELECT updated_at FROM goals WHERE session_id = ?`, sessionID).Scan(&ms)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("goals: read the update time of session %s: %w", sessionID, err)
	}
	if ms == 0 {
		return time.Time{}, nil
	}
	return time.UnixMilli(ms), nil
}

// Put writes the goal and returns the new revision. An expectedRev of 0 inserts a new row.
// Another value updates the row only when its revision is still expectedRev. A row that
// exists on an insert, or a changed or missing row on an update, gives ErrConflict.
func (s *Store) Put(ctx context.Context, sessionID string, st State, expectedRev int64) (int64, error) {
	raw, err := json.Marshal(st)
	if err != nil {
		return 0, fmt.Errorf("goals: encode the goal of session %s: %w", sessionID, err)
	}
	now := time.Now().UnixMilli()
	var res sql.Result
	if expectedRev == 0 {
		res, err = s.DB.ExecContext(ctx, `INSERT INTO goals (session_id, state, revision, updated_at)
			VALUES (?, ?, 1, ?) ON CONFLICT(session_id) DO NOTHING`, sessionID, string(raw), now)
	} else {
		res, err = s.DB.ExecContext(ctx, `UPDATE goals SET state = ?, revision = revision + 1, updated_at = ?
			WHERE session_id = ? AND revision = ?`, string(raw), now, sessionID, expectedRev)
	}
	if err != nil {
		return 0, fmt.Errorf("goals: put the goal of session %s: %w", sessionID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("goals: put the goal of session %s: %w", sessionID, err)
	}
	if n == 0 {
		return 0, fmt.Errorf("goals: put the goal of session %s at revision %d: %w", sessionID, expectedRev, ErrConflict)
	}
	return expectedRev + 1, nil
}

// Delete removes the goal of the session. No row is not an error.
func (s *Store) Delete(ctx context.Context, sessionID string) error {
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM goals WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("goals: delete the goal of session %s: %w", sessionID, err)
	}
	return nil
}

// Active returns the active goals in the order of the session ID.
func (s *Store) Active(ctx context.Context) ([]ActiveGoal, error) {
	type row struct {
		id, raw string
		rev     int64
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT session_id, state, revision FROM goals ORDER BY session_id`)
	if err != nil {
		return nil, fmt.Errorf("goals: list the goals: %w", err)
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.raw, &r.rev); err != nil {
			rows.Close()
			return nil, fmt.Errorf("goals: list the goals: %w", err)
		}
		all = append(all, r)
	}
	err = rows.Err()
	// state.db has one connection. Close the rows before the next query can need it.
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("goals: list the goals: %w", err)
	}
	var out []ActiveGoal
	for _, r := range all {
		st, err := decodeState(r.raw, s.GateTimeoutS, s.GateMaxRetries)
		if err != nil {
			return nil, fmt.Errorf("goals: decode the goal of session %s: %w", r.id, err)
		}
		if st.Status == StatusActive {
			out = append(out, ActiveGoal{SessionID: r.id, State: st, Revision: r.rev})
		}
	}
	return out, nil
}

// PausedGoal is one paused goal and the last write time of its row.
type PausedGoal struct {
	SessionID string
	State     State
	UpdatedAt time.Time
}

// Paused returns the paused goals in the order of the session ID. UpdatedAt comes from
// updated_at (Unix milliseconds), for an old row with no PausedAt.
func (s *Store) Paused(ctx context.Context) ([]PausedGoal, error) {
	type row struct {
		id, raw string
		upd     int64
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT session_id, state, updated_at FROM goals ORDER BY session_id`)
	if err != nil {
		return nil, fmt.Errorf("goals: list the paused goals: %w", err)
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.raw, &r.upd); err != nil {
			rows.Close()
			return nil, fmt.Errorf("goals: list the paused goals: %w", err)
		}
		all = append(all, r)
	}
	err = rows.Err()
	// state.db has one connection. Close the rows before the next query can need it.
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("goals: list the paused goals: %w", err)
	}
	var out []PausedGoal
	for _, r := range all {
		st, err := decodeState(r.raw, s.GateTimeoutS, s.GateMaxRetries)
		if err != nil {
			return nil, fmt.Errorf("goals: decode the goal of session %s: %w", r.id, err)
		}
		if st.Status == StatusPaused {
			out = append(out, PausedGoal{SessionID: r.id, State: st, UpdatedAt: time.UnixMilli(r.upd)})
		}
	}
	return out, nil
}

// GoalRow is one goal with the title and the source of its session, for the dashboard list.
type GoalRow struct {
	SessionID, Title, Source string
	State                    State
	UpdatedAt                time.Time
}

// ListAll returns the active and paused goals, and the done and cleared goals with an
// updated_at at or after finishedSince, newest first. It reads and closes all rows before it
// decodes, because state.db has one connection.
func (s *Store) ListAll(ctx context.Context, finishedSince time.Time) ([]GoalRow, error) {
	type row struct {
		id, raw, title, source string
		upd                    int64
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT g.session_id, g.state, g.updated_at,
		COALESCE(s.title, ''), COALESCE(s.source, '')
		FROM goals g LEFT JOIN sessions s ON s.id = g.session_id
		ORDER BY g.updated_at DESC, g.session_id`)
	if err != nil {
		return nil, fmt.Errorf("goals: list all goals: %w", err)
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.raw, &r.upd, &r.title, &r.source); err != nil {
			rows.Close()
			return nil, fmt.Errorf("goals: list all goals: %w", err)
		}
		all = append(all, r)
	}
	err = rows.Err()
	// state.db has one connection. Close the rows before the next query can need it.
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("goals: list all goals: %w", err)
	}
	var out []GoalRow
	for _, r := range all {
		st, err := decodeState(r.raw, s.GateTimeoutS, s.GateMaxRetries)
		if err != nil {
			return nil, fmt.Errorf("goals: decode the goal of session %s: %w", r.id, err)
		}
		upd := time.UnixMilli(r.upd)
		if (st.Status == StatusDone || st.Status == StatusCleared) && upd.Before(finishedSince) {
			continue
		}
		out = append(out, GoalRow{SessionID: r.id, Title: r.title, Source: r.source, State: st, UpdatedAt: upd})
	}
	return out, nil
}

// decodeState decodes a goal row and applies the defaults of Hermes GoalState.from_json and
// GoalGate.from_dict, so a row from Hermes or from an older program loads. A gate with no
// time limit or retry count gets gateTimeoutS and gateMaxRetries. A value of 0 or less uses
// the const.
func decodeState(raw string, gateTimeoutS, gateMaxRetries int) (State, error) {
	if gateTimeoutS <= 0 {
		gateTimeoutS = defaultGateTimeoutS
	}
	if gateMaxRetries <= 0 {
		gateMaxRetries = defaultGateMaxRetries
	}
	st := State{Status: StatusActive}
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return State{}, err
	}
	if st.MaxTurns <= 0 {
		st.MaxTurns = defaultMaxTurns
	}
	if st.Subgoals != nil {
		subs := st.Subgoals[:0:0]
		for _, sg := range st.Subgoals {
			if sg = strings.TrimSpace(sg); sg != "" {
				subs = append(subs, sg)
			}
		}
		st.Subgoals = subs
	}
	if st.Gates != nil {
		gates := st.Gates[:0:0]
		for _, g := range st.Gates {
			if strings.TrimSpace(g.Command) == "" {
				continue
			}
			if g.TimeoutSeconds <= 0 {
				g.TimeoutSeconds = gateTimeoutS
			}
			if g.MaxRetries <= 0 {
				g.MaxRetries = gateMaxRetries
			}
			gates = append(gates, g)
		}
		st.Gates = gates
	}
	return st, nil
}
