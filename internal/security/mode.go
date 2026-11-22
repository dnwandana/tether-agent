package security

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The values of /mode for a chat or a topic. No row in chat_modes means ModeGuarded.
const (
	ModeReadOnly = "read-only"
	ModeGuarded  = "guarded"
	ModeFull     = "full"
)

// ModeOf returns the /mode value of scope: ModeReadOnly, ModeFull, or "" for guarded. A nil
// DB or an empty scope gives "" with no query. A query error gives "" and one log line, so
// an error never gives full.
func (g *Gate) ModeOf(ctx context.Context, scope string) string {
	mode, err := g.ModeCheck(ctx, scope)
	if err != nil {
		g.logf("mode: read failed, the chat counts as guarded", "scope", scope, "err", err)
		return ""
	}
	return mode
}

// ModeCheck returns the /mode value of scope, like ModeOf, and the query error. sql.ErrNoRows
// is not an error. ModeOf calls it, logs an error, and gives "". Dispatch uses the error to
// refuse the tools that are not on the read-only allow list.
func (g *Gate) ModeCheck(ctx context.Context, scope string) (string, error) {
	if g == nil || g.DB == nil || scope == "" {
		return "", nil
	}
	var mode string
	err := g.DB.QueryRowContext(ctx, "SELECT mode FROM chat_modes WHERE scope=?", scope).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("mode: read the mode of %q: %w", scope, err)
	}
	if mode != ModeReadOnly && mode != ModeFull {
		return "", nil
	}
	return mode, nil
}

// SetMode saves the /mode value of scope. ModeGuarded deletes the row. Another value, an
// empty scope, or a nil DB is an error.
func (g *Gate) SetMode(ctx context.Context, scope, mode string) error {
	if g == nil || g.DB == nil {
		return errors.New("mode: no state.db, cannot save the mode")
	}
	if scope == "" {
		return errors.New("mode: the scope is empty")
	}
	switch mode {
	case ModeGuarded:
		if _, err := g.DB.ExecContext(ctx, "DELETE FROM chat_modes WHERE scope=?", scope); err != nil {
			return fmt.Errorf("mode: clear the mode of %q: %w", scope, err)
		}
	case ModeReadOnly, ModeFull:
		_, err := g.DB.ExecContext(ctx,
			"INSERT INTO chat_modes(scope, mode, set_at) VALUES (?, ?, ?) ON CONFLICT(scope) DO UPDATE SET mode=excluded.mode, set_at=excluded.set_at",
			scope, mode, time.Now().UnixMilli())
		if err != nil {
			return fmt.Errorf("mode: save the mode of %q: %w", scope, err)
		}
	default:
		return fmt.Errorf("mode: unknown mode %q; use read-only, guarded, or full", mode)
	}
	return nil
}

// ScopeMode is one row of chat_modes: a scope that is not guarded, its mode, and the time of
// the last /mode change.
type ScopeMode struct {
	Scope, Mode string
	SetAt       time.Time
}

// ScopeModes returns the scopes with a read-only or full row, ordered by scope. A nil DB gives
// nil. It closes the rows before it returns.
func (g *Gate) ScopeModes(ctx context.Context) ([]ScopeMode, error) {
	if g == nil || g.DB == nil {
		return nil, nil
	}
	rows, err := g.DB.QueryContext(ctx, "SELECT scope, mode, set_at FROM chat_modes ORDER BY scope")
	if err != nil {
		return nil, fmt.Errorf("mode: list the scopes: %w", err)
	}
	defer rows.Close()
	var out []ScopeMode
	for rows.Next() {
		var r ScopeMode
		var at int64
		if err := rows.Scan(&r.Scope, &r.Mode, &at); err != nil {
			return nil, fmt.Errorf("mode: list the scopes: %w", err)
		}
		if r.Mode != ModeReadOnly && r.Mode != ModeFull {
			continue
		}
		r.SetAt = time.UnixMilli(at)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mode: list the scopes: %w", err)
	}
	return out, nil
}
