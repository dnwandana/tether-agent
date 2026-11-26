package learn

import (
	"context"
	"strings"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/textmatch"
)

// OwnerTexts finds the words that the owner typed, to check a quote.
type OwnerTexts struct {
	db      *db.DB
	isOwner func(string) bool
	clean   func(string) string
}

// NewOwnerTexts returns an OwnerTexts. isOwner tells if a user id is an owner. clean removes
// the framing blocks from a user message. Each of them may be nil.
func NewOwnerTexts(d *db.DB, isOwner func(userID string) bool, clean func(text string) string) *OwnerTexts {
	return &OwnerTexts{db: d, isOwner: isOwner, clean: clean}
}

// ownerRowCond skips the rows of a member. A member can talk in a shared session that an
// owner opened, so the user_id of the session does not tell who sent a row.
const ownerRowCond = ` AND m.role = 'user' AND m.observed = 0 AND m.member = 0 AND s.kind = 'chat'`

type ownerRow struct{ content, source, userID string }

// HasQuote reports whether quote (3 or more words) is inside an owner message. With a session
// id, it reads that session, or the chat parent of a review session. With an empty id, it
// reads all owner sessions through the full-text index. A shared session has an empty user_id,
// so its member texts never count. A row that a member sent never counts.
func (o *OwnerTexts) HasQuote(ctx context.Context, sessionID, quote string) (bool, error) {
	q := textmatch.Normalize(quote)
	if len(strings.Fields(q)) < 3 {
		return false, nil
	}
	var rows []ownerRow
	var err error
	if sessionID != "" {
		id, found, err := o.chatParent(ctx, sessionID)
		if err != nil || !found {
			return false, err
		}
		rows, err = o.read(ctx, `SELECT m.content, s.source, s.user_id FROM messages m JOIN sessions s ON s.id = m.session_id WHERE m.session_id = ?`+ownerRowCond, id)
		if err != nil {
			return false, err
		}
	} else {
		rows, err = o.read(ctx, `SELECT m.content, s.source, s.user_id FROM messages_fts JOIN messages m ON m.id = messages_fts.rowid JOIN sessions s ON s.id = m.session_id WHERE messages_fts MATCH ?`+ownerRowCond+` ORDER BY m.id DESC LIMIT 500`, `"`+q+`"`)
		if err != nil {
			return false, err
		}
	}
	for _, r := range rows {
		owner := r.source == "cli" || r.source == "dashboard" ||
			(r.userID != "" && o.isOwner != nil && o.isOwner(r.userID))
		if !owner {
			continue
		}
		text := r.content
		if o.clean != nil {
			text = o.clean(text)
		}
		if textmatch.ContainsOnWords(textmatch.Normalize(text), q) {
			return true, nil
		}
	}
	return false, nil
}

// chatParent follows parent_id from the session to the first session of kind chat.
func (o *OwnerTexts) chatParent(ctx context.Context, id string) (string, bool, error) {
	for range 8 {
		var kind string
		var parent *string
		rs, err := o.db.QueryContext(ctx, `SELECT kind, parent_id FROM sessions WHERE id = ?`, id)
		if err != nil {
			return "", false, err
		}
		found := false
		if rs.Next() {
			found = true
			if err := rs.Scan(&kind, &parent); err != nil {
				rs.Close()
				return "", false, err
			}
		}
		if err := rs.Err(); err != nil {
			rs.Close()
			return "", false, err
		}
		rs.Close()
		if !found {
			return "", false, nil
		}
		if kind == "chat" {
			return id, true, nil
		}
		if parent == nil || *parent == "" {
			return "", false, nil
		}
		id = *parent
	}
	return "", false, nil
}

// read runs the query and closes the rows before it returns.
func (o *OwnerTexts) read(ctx context.Context, query string, args ...any) ([]ownerRow, error) {
	rs, err := o.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []ownerRow
	for rs.Next() {
		var r ownerRow
		var src, uid *string
		if err := rs.Scan(&r.content, &src, &uid); err != nil {
			return nil, err
		}
		if src != nil {
			r.source = *src
		}
		if uid != nil {
			r.userID = *uid
		}
		out = append(out, r)
	}
	return out, rs.Err()
}
