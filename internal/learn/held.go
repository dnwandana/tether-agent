package learn

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/memory"
)

// HeldWrite is a memory write that waits for the owner. State is "held", "saved", "dropped",
// or "swept".
type HeldWrite struct {
	ID            int64
	Stamp         string
	At, ExpiresAt time.Time
	Op            memory.Op
	MatchedEntry  string
	Source        string
	SessionID     string
	SessionKey    string
	Sources       []string
	Hosts         []string
	State         string
}

// Held stores the held memory writes on state.db.
type Held struct {
	db  *db.DB
	now func() time.Time
}

// NewHeld returns a Held on d.
func NewHeld(d *db.DB) *Held { return &Held{db: d, now: time.Now} }

// heldOp mirrors memory.Op without the func field Mask, which json cannot store.
type heldOp struct {
	Action       string   `json:"action"`
	Target       string   `json:"target"`
	Content      string   `json:"content"`
	OldText      string   `json:"old_text"`
	Topic        string   `json:"topic"`
	Exact        bool     `json:"exact"`
	Source       string   `json:"source"`
	SessionID    string   `json:"session_id"`
	Quote        string   `json:"quote"`
	QuoteOK      bool     `json:"quote_ok"`
	Until        string   `json:"until"`
	Outside      []string `json:"outside"`
	To           string   `json:"to"`
	MatchedEntry string   `json:"matched_entry"`
	Hosts        []string `json:"hosts,omitempty"`
}

const heldCols = `id, stamp, at, expires_at, op, source, session_id, session_key, sources, state`

// Add stores a held write and returns the row as stored. Empty stamp, zero times, and an
// empty state get defaults.
func (h *Held) Add(ctx context.Context, w HeldWrite) (HeldWrite, error) {
	if w.Stamp == "" {
		w.Stamp = noticeStamp()
	}
	if w.At.IsZero() {
		w.At = h.now()
	}
	if w.ExpiresAt.IsZero() {
		w.ExpiresAt = w.At.Add(24 * time.Hour)
	}
	if w.State == "" {
		w.State = "held"
	}
	op := w.Op
	rawOp, err := json.Marshal(heldOp{Action: op.Action, Target: op.Target, Content: op.Content,
		OldText: op.OldText, Topic: op.Topic, Exact: op.Exact, Source: op.Source,
		SessionID: op.SessionID, Quote: op.Quote, QuoteOK: op.QuoteOK, Until: op.Until,
		Outside: op.Outside, To: op.To, MatchedEntry: w.MatchedEntry, Hosts: w.Hosts})
	if err != nil {
		return HeldWrite{}, err
	}
	rawSources, err := json.Marshal(w.Sources)
	if err != nil {
		return HeldWrite{}, err
	}
	res, err := h.db.ExecContext(ctx,
		`INSERT INTO memory_held(stamp, at, expires_at, op, source, session_id, session_key, sources, state)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.Stamp, w.At.Unix(), w.ExpiresAt.Unix(), string(rawOp), w.Source, w.SessionID,
		w.SessionKey, string(rawSources), w.State)
	if err != nil {
		return HeldWrite{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return HeldWrite{}, err
	}
	return h.Get(ctx, id)
}

// heldScanner is the shared part of sql.Row and sql.Rows.
type heldScanner interface{ Scan(dest ...any) error }

func heldScan(s heldScanner) (HeldWrite, error) {
	var (
		w                 HeldWrite
		at, exp           int64
		rawOp, rawSources string
		stored            heldOp
	)
	if err := s.Scan(&w.ID, &w.Stamp, &at, &exp, &rawOp, &w.Source, &w.SessionID, &w.SessionKey, &rawSources, &w.State); err != nil {
		return HeldWrite{}, err
	}
	w.At, w.ExpiresAt = time.Unix(at, 0), time.Unix(exp, 0)
	if err := json.Unmarshal([]byte(rawOp), &stored); err != nil {
		return HeldWrite{}, fmt.Errorf("held write %d op: %w", w.ID, err)
	}
	if rawSources != "" {
		if err := json.Unmarshal([]byte(rawSources), &w.Sources); err != nil {
			return HeldWrite{}, fmt.Errorf("held write %d sources: %w", w.ID, err)
		}
	}
	w.Op = memory.Op{Action: stored.Action, Target: stored.Target, Content: stored.Content,
		OldText: stored.OldText, Topic: stored.Topic, Exact: stored.Exact, Source: stored.Source,
		SessionID: stored.SessionID, Quote: stored.Quote, QuoteOK: stored.QuoteOK,
		Until: stored.Until, Outside: stored.Outside, To: stored.To}
	w.MatchedEntry = stored.MatchedEntry
	w.Hosts = stored.Hosts
	return w, nil
}

// Get returns a held write. The error wraps sql.ErrNoRows when the id is unknown.
func (h *Held) Get(ctx context.Context, id int64) (HeldWrite, error) {
	w, err := heldScan(h.db.QueryRowContext(ctx, `SELECT `+heldCols+` FROM memory_held WHERE id = ?`, id))
	if err != nil {
		return HeldWrite{}, fmt.Errorf("held write %d: %w", id, err)
	}
	return w, nil
}

// List returns the held writes with the state, or all of them for "", in id order.
func (h *Held) List(ctx context.Context, state string) ([]HeldWrite, error) {
	if state == "" {
		return h.read(ctx, `SELECT `+heldCols+` FROM memory_held ORDER BY id`)
	}
	return h.read(ctx, `SELECT `+heldCols+` FROM memory_held WHERE state = ? ORDER BY id`, state)
}

// Due returns the held writes that expire at or before now, in id order.
func (h *Held) Due(ctx context.Context, now time.Time) ([]HeldWrite, error) {
	return h.read(ctx, `SELECT `+heldCols+` FROM memory_held WHERE state = 'held' AND expires_at <= ? ORDER BY id`, now.Unix())
}

// SetState sets the state to "held", "saved", "dropped", or "swept".
func (h *Held) SetState(ctx context.Context, id int64, state string) error {
	switch state {
	case "held", "saved", "dropped", "swept":
	default:
		return fmt.Errorf("held state %q is not held, saved, dropped, or swept", state)
	}
	res, err := h.db.ExecContext(ctx, `UPDATE memory_held SET state = ? WHERE id = ?`, state, id)
	if err != nil {
		return err
	}
	if c, err := res.RowsAffected(); err == nil && c == 0 {
		return fmt.Errorf("held write %d: %w", id, sql.ErrNoRows)
	}
	return nil
}

// read runs the query and closes the rows before it returns.
func (h *Held) read(ctx context.Context, query string, args ...any) ([]HeldWrite, error) {
	rs, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []HeldWrite
	for rs.Next() {
		w, err := heldScan(rs)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rs.Err()
}
