package obs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/sessions"
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

// PinnedEntry returns the entry name of the pin (the part before ":"). It returns
// "primary" when the session has no pin, because a turn with no pin starts on primary.
func (l LastAnswer) PinnedEntry() string {
	if l.Pin == "" {
		return "primary"
	}
	name, _, _ := strings.Cut(l.Pin, ":")
	return name
}

// ReadLastAnswer returns the newest assistant row with a model. ok is false when no row
// exists. It skips the rows of the hidden child sessions (sessions.HiddenChildKinds), so a
// side call or a judge call on another entry does not look like the newest answer.
func ReadLastAnswer(ctx context.Context, d *db.DB) (l LastAnswer, ok bool, err error) {
	kinds := sessions.HiddenChildKinds
	args := make([]any, len(kinds))
	for i, k := range kinds {
		args[i] = k
	}
	// The LEFT JOIN and the COALESCE keep a row whose session row does not exist.
	q := `SELECT messages.provider, messages.model, messages.timestamp,
		COALESCE(sessions.model_override, '')
		FROM messages LEFT JOIN sessions ON sessions.id = messages.session_id
		WHERE messages.role = 'assistant' AND messages.model != ''
		AND COALESCE(sessions.kind, '') NOT IN (` + strings.TrimSuffix(strings.Repeat("?,", len(kinds)), ",") + `)
		ORDER BY messages.timestamp DESC, messages.id DESC LIMIT 1`
	var ms int64
	err = d.QueryRowContext(ctx, q, args...).Scan(&l.Provider, &l.Model, &ms, &l.Pin)
	if errors.Is(err, sql.ErrNoRows) {
		return LastAnswer{}, false, nil
	}
	if err != nil {
		return LastAnswer{}, false, fmt.Errorf("read last answer: %w", err)
	}
	l.At = time.UnixMilli(ms)
	return l, true, nil
}
