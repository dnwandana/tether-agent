package sessions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// auxTitle is the title of each aux session.
const auxTitle = "side calls"

// auxOrphanSource is the source of the aux session with no parent. The sticker description of
// the Telegram inbox is the only call with no session.
const auxOrphanSource = "telegram"

// ResultMeta returns the meta of a model call with no price. The stamp of the result wins over
// the name of the provider.
func ResultMeta(prov provider.Provider, out provider.Result) MessageMeta {
	m := MessageMeta{
		Usage:        out.Usage,
		Model:        prov.Model(),
		Provider:     prov.Name(),
		FinishReason: out.FinishReason,
	}
	if out.Provider != "" {
		m.Provider, m.Model = out.Provider, out.Model
	}
	return m
}

// AuxLog records the usage of the model calls that run outside a turn.
type AuxLog struct {
	Store *Store
	// Meta gives the meta of the row. Nil uses the stamp of the result with no price.
	Meta func(prov provider.Provider, out provider.Result) MessageMeta

	mu  sync.Mutex
	ids map[string]string // parent session ID -> aux session ID
}

// Record appends one assistant row for the call to the aux session of sessionID. The content
// of the row is the task name. Record creates the aux session when it does not exist. A call
// with no session, or whose session is gone, goes to the aux session with no parent.
func (l *AuxLog) Record(ctx context.Context, sessionID, task string, prov provider.Provider, out provider.Result) error {
	// The provider already billed the call, so a cancel must not stop the record.
	ctx = context.WithoutCancel(ctx)
	wrap := func(err error) error {
		return fmt.Errorf("sessions: record the %s call of session %q: %w", task, sessionID, err)
	}
	parent := sessionID
	tmpl := Session{Source: auxOrphanSource}
	if parent != "" {
		p, err := l.Store.Get(ctx, parent)
		switch {
		case errors.Is(err, ErrNotFound):
			// /delete or retention removed the parent during the call.
			parent = ""
		case err != nil:
			return wrap(err)
		default:
			tmpl = Session{Source: p.Source, ChatID: p.ChatID, ThreadID: p.ThreadID, UserID: p.UserID}
		}
	}
	id, err := l.auxID(ctx, parent, tmpl)
	if err != nil {
		return wrap(err)
	}
	meta := ResultMeta(prov, out)
	if l.Meta != nil {
		meta = l.Meta(prov, out)
	}
	if _, err := l.Store.Append(ctx, id, provider.Message{Role: provider.RoleAssistant, Content: task}, meta); err != nil {
		return wrap(err)
	}
	return nil
}

// auxID finds or creates the aux session of parent. It holds the lock for the whole step, so
// two calls at the same time, for example two view_image calls of one tool round, share one
// session.
func (l *AuxLog) auxID(ctx context.Context, parent string, tmpl Session) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ids == nil {
		l.ids = make(map[string]string)
	}
	id := l.ids[parent]
	if id != "" {
		// Retention deletes an old aux session. Append ignores a missing session, so the
		// row would have no session.
		_, err := l.Store.Get(ctx, id)
		switch {
		case errors.Is(err, ErrNotFound):
			id = ""
			delete(l.ids, parent)
		case err != nil:
			return "", err
		}
	}
	if id == "" {
		q := `SELECT id FROM sessions WHERE kind = ? AND parent_id = ?`
		args := []any{KindAux, parent}
		if parent == "" {
			// /delete clears parent_id of the aux child of a chat, so that child also has no
			// parent. Only the aux session with no parent has no chat ID and the orphan source.
			q += ` AND session_key = '' AND title = ? AND chat_id = '' AND source = ?`
			args = append(args, auxTitle, auxOrphanSource)
		}
		q += ` ORDER BY started_at DESC LIMIT 1`
		err := l.Store.db.QueryRowContext(ctx, q, args...).Scan(&id)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	if id == "" {
		// The session has no key, so no channel makes it active and /sessions does not show it.
		tmpl.Title, tmpl.Kind, tmpl.ParentID = auxTitle, KindAux, parent
		sess, err := l.Store.Create(ctx, tmpl)
		if err != nil {
			return "", err
		}
		id = sess.ID
	}
	l.ids[parent] = id
	return id, nil
}
