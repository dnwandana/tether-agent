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
	st, err := decodeState(raw)
	if err != nil {
		return State{}, 0, fmt.Errorf("goals: decode the goal of session %s: %w", sessionID, err)
	}
	return st, rev, nil
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
		st, err := decodeState(r.raw)
		if err != nil {
			return nil, fmt.Errorf("goals: decode the goal of session %s: %w", r.id, err)
		}
		if st.Status == StatusActive {
			out = append(out, ActiveGoal{SessionID: r.id, State: st, Revision: r.rev})
		}
	}
	return out, nil
}

// decodeState decodes a goal row and applies the defaults of Hermes GoalState.from_json and
// GoalGate.from_dict, so a row from Hermes or from an older program loads.
func decodeState(raw string) (State, error) {
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
				g.TimeoutSeconds = defaultGateTimeoutS
			}
			if g.MaxRetries <= 0 {
				g.MaxRetries = defaultGateMaxRetries
			}
			gates = append(gates, g)
		}
		st.Gates = gates
	}
	return st, nil
}
