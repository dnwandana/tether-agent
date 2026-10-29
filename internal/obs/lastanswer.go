package obs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
)

// LastAnswer is the newest stored model answer.
type LastAnswer struct {
	// Provider is the provider entry name that served the answer.
	Provider string
	Model    string
	At       time.Time
	// Pin is the model_override of the session. It is "" when the session has no pin or
	// when the session row does not exist.
	Pin string
}

// PinsFallback reports whether the session pins the entry "fallback" ("fallback" or
// "fallback:<model>").
func (l LastAnswer) PinsFallback() bool {
	return l.Pin == "fallback" || strings.HasPrefix(l.Pin, "fallback:")
}

// ReadLastAnswer returns the newest assistant row with a model. ok is false when no row
// exists.
func ReadLastAnswer(ctx context.Context, d *db.DB) (l LastAnswer, ok bool, err error) {
	// The LEFT JOIN keeps a row whose session row does not exist.
	const q = `SELECT messages.provider, messages.model, messages.timestamp,
		COALESCE(sessions.model_override, '')
		FROM messages LEFT JOIN sessions ON sessions.id = messages.session_id
		WHERE messages.role = 'assistant' AND messages.model != ''
		ORDER BY messages.timestamp DESC, messages.id DESC LIMIT 1`
	var ms int64
	err = d.QueryRowContext(ctx, q).Scan(&l.Provider, &l.Model, &ms, &l.Pin)
	if errors.Is(err, sql.ErrNoRows) {
		return LastAnswer{}, false, nil
	}
	if err != nil {
		return LastAnswer{}, false, fmt.Errorf("read last answer: %w", err)
	}
	l.At = time.UnixMilli(ms)
	return l, true, nil
}
