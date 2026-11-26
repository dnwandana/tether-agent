// Package learn stores the memory history, the notices, the held writes, and the skill records in state.db.
package learn

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/memory"
)

// MemoryHistory keeps the memory edits and the block list in state.db.
type MemoryHistory struct {
	db *db.DB
}

var _ memory.History = (*MemoryHistory)(nil)

// NewMemoryHistory returns a MemoryHistory on d.
func NewMemoryHistory(d *db.DB) *MemoryHistory { return &MemoryHistory{db: d} }

// editStamp returns 4 random lower-case hex characters.
func editStamp() string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on a supported system. A stamp of zeros still works.
		return "0000"
	}
	return hex.EncodeToString(b[:])
}

// nullID returns NULL for 0, else the id.
func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// Record stores e and returns the id of the new row.
func (h *MemoryHistory) Record(ctx context.Context, e memory.Edit) (int64, error) {
	if e.Stamp == "" {
		e.Stamp = editStamp()
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	res, err := h.db.ExecContext(ctx,
		`INSERT INTO memory_edits(stamp, at, target, idx, dest, action, old_text, new_text, source, session_id, quote, outside, group_id, undone_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Stamp, e.At.Unix(), e.Target, e.Idx, e.Dest, e.Action, e.OldText, e.NewText, e.Source,
		e.SessionID, e.Quote, strings.Join(e.Outside, ","), nullID(e.GroupID), nullID(e.UndoneBy))
	if err != nil {
		return 0, fmt.Errorf("learn: record memory edit: %w", err)
	}
	return res.LastInsertId()
}

const editCols = `id, stamp, at, target, idx, dest, action, old_text, new_text, source, session_id, quote, outside, group_id, undone_by`

type rowScanner interface{ Scan(dest ...any) error }

func scanEdit(r rowScanner) (memory.Edit, error) {
	var e memory.Edit
	var at int64
	var outside string
	var group, undone sql.NullInt64
	if err := r.Scan(&e.ID, &e.Stamp, &at, &e.Target, &e.Idx, &e.Dest, &e.Action, &e.OldText,
		&e.NewText, &e.Source, &e.SessionID, &e.Quote, &outside, &group, &undone); err != nil {
		return memory.Edit{}, err
	}
	e.At = time.Unix(at, 0)
	if outside != "" {
		e.Outside = strings.Split(outside, ",")
	}
	e.GroupID, e.UndoneBy = group.Int64, undone.Int64
	return e, nil
}

// Get returns the edit with the id. A missing row wraps sql.ErrNoRows.
func (h *MemoryHistory) Get(ctx context.Context, id int64) (memory.Edit, error) {
	e, err := scanEdit(h.db.QueryRowContext(ctx, `SELECT `+editCols+` FROM memory_edits WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return memory.Edit{}, fmt.Errorf("learn: memory edit %d: %w", id, sql.ErrNoRows)
	}
	if err != nil {
		return memory.Edit{}, fmt.Errorf("learn: read memory edit %d: %w", id, err)
	}
	return e, nil
}

// MarkUndone sets undone_by of edit id. An undoID of 0 clears it.
func (h *MemoryHistory) MarkUndone(ctx context.Context, id, undoID int64) error {
	res, err := h.db.ExecContext(ctx, `UPDATE memory_edits SET undone_by = ? WHERE id = ?`, nullID(undoID), id)
	if err != nil {
		return fmt.Errorf("learn: mark memory edit %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("learn: memory edit %d: %w", id, sql.ErrNoRows)
	}
	return nil
}

// Recent returns the newest edits first. A limit of 0 or less uses 10.
func (h *MemoryHistory) Recent(ctx context.Context, limit int) ([]memory.Edit, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := h.db.QueryContext(ctx, `SELECT `+editCols+` FROM memory_edits ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("learn: list memory edits: %w", err)
	}
	defer rows.Close()
	var out []memory.Edit
	for rows.Next() {
		e, err := scanEdit(rows)
		if err != nil {
			return nil, fmt.Errorf("learn: read memory edit: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddBlock stores a block row.
func (h *MemoryHistory) AddBlock(ctx context.Context, b memory.Block) error {
	if b.At.IsZero() {
		b.At = time.Now()
	}
	if _, err := h.db.ExecContext(ctx, `INSERT INTO memory_blocks(at, text_norm, text, edit_id) VALUES (?, ?, ?, ?)`,
		b.At.Unix(), b.Norm, b.Text, b.EditID); err != nil {
		return fmt.Errorf("learn: add memory block: %w", err)
	}
	return nil
}

// matchingBlocks returns the ids of the block rows that match norm.
func (h *MemoryHistory) matchingBlocks(ctx context.Context, norm string) ([]int64, error) {
	if norm == "" {
		return nil, nil
	}
	rows, err := h.db.QueryContext(ctx, `SELECT id, text_norm FROM memory_blocks`)
	if err != nil {
		return nil, fmt.Errorf("learn: list memory blocks: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		var n string
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("learn: read memory block: %w", err)
		}
		if blockMatch(n, norm) {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// Blocked reports whether a block row matches norm.
func (h *MemoryHistory) Blocked(ctx context.Context, norm string) (bool, error) {
	ids, err := h.matchingBlocks(ctx, norm)
	return len(ids) > 0, err
}

// ClearBlock deletes each block row that matches norm.
func (h *MemoryHistory) ClearBlock(ctx context.Context, norm string) error {
	ids, err := h.matchingBlocks(ctx, norm)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := h.db.ExecContext(ctx, `DELETE FROM memory_blocks WHERE id = ?`, id); err != nil {
			return fmt.Errorf("learn: clear memory block: %w", err)
		}
	}
	return nil
}

// DeleteBlockOf deletes the block rows of one edit.
func (h *MemoryHistory) DeleteBlockOf(ctx context.Context, editID int64) error {
	if _, err := h.db.ExecContext(ctx, `DELETE FROM memory_blocks WHERE edit_id = ?`, editID); err != nil {
		return fmt.Errorf("learn: delete memory block: %w", err)
	}
	return nil
}

// blockMatch reports whether a and b are the same fact for the block list.
func blockMatch(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	long, short := a, b
	if utf8.RuneCountInString(short) > utf8.RuneCountInString(long) {
		long, short = short, long
	}
	return utf8.RuneCountInString(short) >= 20 && strings.Contains(" "+long+" ", " "+short+" ")
}
