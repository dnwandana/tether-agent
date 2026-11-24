package security

import (
	"context"
	"fmt"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
)

// PinTarget is one program file that a bound secret can reach: the real path and the sha256
// hex of the file. Interpreter is true for the interpreter of a shebang line.
type PinTarget struct {
	Path, SHA256 string
	Interpreter  bool
}

// PinStore keeps the programs that the owner allowed to get a bound secret, in secret_pins.
type PinStore struct {
	DB *db.DB
}

// Allowed reports whether the owner allowed name for the file t: a row with the same name,
// path, and hash.
func (s *PinStore) Allowed(ctx context.Context, name string, t PinTarget) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM secret_pins WHERE name = ? AND path = ? AND sha256 = ?`,
		name, t.Path, t.SHA256).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("secret pins: cannot read the pin of %s: %w", name, err)
	}
	return n > 0, nil
}

// Allow records that the owner allowed name for the file t. It replaces a row of the same
// name and path with an old hash.
func (s *PinStore) Allow(ctx context.Context, name string, t PinTarget) error {
	_, err := s.DB.ExecContext(ctx,
		`INSERT OR REPLACE INTO secret_pins (name, path, sha256, created_at) VALUES (?, ?, ?, ?)`,
		name, t.Path, t.SHA256, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("secret pins: cannot save the pin of %s: %w", name, err)
	}
	return nil
}

// Unpin deletes each pin of name and returns the count of deleted rows.
func (s *PinStore) Unpin(ctx context.Context, name string) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM secret_pins WHERE name = ?`, name)
	if err != nil {
		return 0, fmt.Errorf("secret pins: cannot delete the pins of %s: %w", name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("secret pins: cannot count the deleted pins of %s: %w", name, err)
	}
	return n, nil
}

// UnpinPath deletes the pin of name for the file at path and returns the count of deleted rows.
func (s *PinStore) UnpinPath(ctx context.Context, name, path string) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM secret_pins WHERE name = ? AND path = ?`, name, path)
	if err != nil {
		return 0, fmt.Errorf("secret pins: cannot delete the pin %s of %s: %w", path, name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("secret pins: cannot count the deleted pins of %s: %w", name, err)
	}
	return n, nil
}

// List returns the pins of name, sorted by path. The table has no interpreter flag, so
// Interpreter is false in each result.
func (s *PinStore) List(ctx context.Context, name string) ([]PinTarget, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT path, sha256 FROM secret_pins WHERE name = ? ORDER BY path`, name)
	if err != nil {
		return nil, fmt.Errorf("secret pins: cannot read the pins of %s: %w", name, err)
	}
	defer rows.Close()
	var out []PinTarget
	for rows.Next() {
		var t PinTarget
		if err := rows.Scan(&t.Path, &t.SHA256); err != nil {
			return nil, fmt.Errorf("secret pins: cannot read the pins of %s: %w", name, err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("secret pins: cannot read the pins of %s: %w", name, err)
	}
	return out, nil
}
