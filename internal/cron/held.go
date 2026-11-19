package cron

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
)

// HeldSend is one row of held_sends. Kind is "hold" (a delivery that model_guard held) or
// "original" (the original text of a masked delivery). Target is "source:chat[:thread]".
// RunID 0 means no cron_runs row.
type HeldSend struct {
	ID, Kind, JobID     string
	RunID               int64
	Target, TargetTitle string
	Text, Reason        string
	Sources             []string
	CardChat            string
	CardMsgID           int
	CreatedAt           time.Time
}

// HeldStore reads and writes held_sends.
type HeldStore struct{ DB *db.DB }

const heldReturning = ` RETURNING id, kind, job_id, run_id, target, target_title, text, reason, sources, card_chat, card_msg_id, created_at`

// Insert adds h. card_chat is h.CardChat and card_msg_id is h.CardMsgID.
func (s *HeldStore) Insert(ctx context.Context, h HeldSend) error {
	return insertHeld(ctx, s.DB, h)
}

// execer is the part of *db.DB and *sql.Tx that insertHeld needs.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func insertHeld(ctx context.Context, x execer, h HeldSend) error {
	src, err := json.Marshal(nonNilStrings(h.Sources))
	if err != nil {
		return fmt.Errorf("cron: insert held send %s: %w", h.ID, err)
	}
	var run sql.NullInt64
	if h.RunID != 0 {
		run = sql.NullInt64{Int64: h.RunID, Valid: true}
	}
	_, err = x.ExecContext(ctx, `INSERT INTO held_sends
		(id, kind, job_id, run_id, target, target_title, text, reason, sources, card_chat, card_msg_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		h.ID, h.Kind, h.JobID, run, h.Target, h.TargetTitle, h.Text, h.Reason, string(src),
		h.CardChat, h.CardMsgID, h.CreatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("cron: insert held send %s: %w", h.ID, err)
	}
	return nil
}

// Claim deletes the row id and returns it. ok is false when no row has the id. One DELETE
// RETURNING statement does both, so only one caller gets a row.
func (s *HeldStore) Claim(ctx context.Context, id string) (HeldSend, bool, error) {
	rows, err := s.claim(ctx, `DELETE FROM held_sends WHERE id = ?`+heldReturning, id)
	if err != nil {
		return HeldSend{}, false, fmt.Errorf("cron: claim held send %s: %w", id, err)
	}
	if len(rows) == 0 {
		return HeldSend{}, false, nil
	}
	return rows[0], true, nil
}

// Restore puts a claimed row back with the same id.
//
// A tap can claim a hold row, and the run row can be written before the tap puts the row
// back. In that case linkHeld found no hold row and set the run to none, and the row has
// RunID 0. Restore then links the row to the newest run of the same job that has delivery
// none, that ended at or after the row was created, and that has no hold row. It sets that
// run to held again. All steps are in one transaction.
func (s *HeldStore) Restore(ctx context.Context, h HeldSend) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cron: restore held send %s: %w", h.ID, err)
	}
	defer tx.Rollback()
	if h.Kind == "hold" && h.RunID == 0 {
		var run sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT id FROM cron_runs
			WHERE job_id = ? AND delivery = ? AND finished_at >= ?
			AND id NOT IN (SELECT run_id FROM held_sends WHERE run_id IS NOT NULL)
			ORDER BY id DESC LIMIT 1`, h.JobID, DeliveryNone, h.CreatedAt.UnixMilli()).Scan(&run)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("cron: restore held send %s: %w", h.ID, err)
		}
		if run.Valid {
			if _, err := tx.ExecContext(ctx, `UPDATE cron_runs SET delivery = ? WHERE id = ?`,
				DeliveryHeld, run.Int64); err != nil {
				return fmt.Errorf("cron: restore held send %s: %w", h.ID, err)
			}
			h.RunID = run.Int64
		}
	}
	if err := insertHeld(ctx, tx, h); err != nil {
		return fmt.Errorf("cron: restore held send %s: %w", h.ID, err)
	}
	return tx.Commit()
}

// ClaimExpired deletes and returns each row that was created before before.
func (s *HeldStore) ClaimExpired(ctx context.Context, before time.Time) ([]HeldSend, error) {
	rows, err := s.claim(ctx, `DELETE FROM held_sends WHERE created_at < ?`+heldReturning, before.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("cron: claim expired held sends: %w", err)
	}
	return rows, nil
}

// DeleteJob deletes the rows of the job jobID.
func (s *HeldStore) DeleteJob(ctx context.Context, jobID string) error {
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM held_sends WHERE job_id = ?`, jobID); err != nil {
		return fmt.Errorf("cron: delete held sends of job %s: %w", jobID, err)
	}
	return nil
}

// SetCard records the chat and the message of the owner card of the row id.
func (s *HeldStore) SetCard(ctx context.Context, id, chat string, msgID int) error {
	if _, err := s.DB.ExecContext(ctx, `UPDATE held_sends SET card_chat = ?, card_msg_id = ? WHERE id = ?`,
		chat, msgID, id); err != nil {
		return fmt.Errorf("cron: set card of held send %s: %w", id, err)
	}
	return nil
}

// claim runs a DELETE RETURNING statement. It reads and closes all rows before it returns,
// because state.db has one connection.
func (s *HeldStore) claim(ctx context.Context, query string, arg any) ([]HeldSend, error) {
	rows, err := s.DB.QueryContext(ctx, query, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HeldSend
	for rows.Next() {
		var h HeldSend
		var run sql.NullInt64
		var src string
		var msg, at int64
		if err := rows.Scan(&h.ID, &h.Kind, &h.JobID, &run, &h.Target, &h.TargetTitle, &h.Text,
			&h.Reason, &src, &h.CardChat, &msg, &at); err != nil {
			return nil, err
		}
		h.RunID = run.Int64
		h.CardMsgID = int(msg)
		h.CreatedAt = time.UnixMilli(at)
		if err := json.Unmarshal([]byte(src), &h.Sources); err != nil {
			return nil, fmt.Errorf("sources of %s: %w", h.ID, err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, rows.Close()
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// newHeldID returns a new row id: 16 lowercase hex characters.
func newHeldID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("cron: new held send id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
