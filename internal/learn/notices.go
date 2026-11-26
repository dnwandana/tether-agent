package learn

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
)

// NoticeItem is one change that a notice covers. Kind is "memory", "skill", or "archive".
type NoticeItem struct {
	Kind string
	ID   int64
}

// Notice is a change card and its state. State is "open", "undone", or "kept".
type Notice struct {
	ID         int64
	Stamp      string
	At         time.Time
	SessionKey string
	Text       string
	Items      []NoticeItem
	State      string
}

// Notices stores the notices on state.db.
type Notices struct {
	db  *db.DB
	now func() time.Time
}

// NewNotices returns a Notices on d.
func NewNotices(d *db.DB) *Notices { return &Notices{db: d, now: time.Now} }

// noticeItemJSON is the stored shape of a NoticeItem.
type noticeItemJSON struct {
	Kind string `json:"kind"`
	ID   int64  `json:"id"`
}

// noticeStamp returns 4 random lower-case hex characters.
func noticeStamp() string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000"
	}
	return hex.EncodeToString(b[:])
}

// Create writes a notice with the state "open" and returns it.
func (n *Notices) Create(ctx context.Context, sessionKey, text string, items []NoticeItem) (Notice, error) {
	list := make([]noticeItemJSON, 0, len(items))
	for _, it := range items {
		list = append(list, noticeItemJSON(it))
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return Notice{}, err
	}
	stamp, at := noticeStamp(), n.now()
	res, err := n.db.ExecContext(ctx,
		`INSERT INTO notices(stamp, at, session_key, text, items, state) VALUES(?, ?, ?, ?, ?, 'open')`,
		stamp, at.Unix(), sessionKey, text, string(raw))
	if err != nil {
		return Notice{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Notice{}, err
	}
	return Notice{ID: id, Stamp: stamp, At: time.Unix(at.Unix(), 0), SessionKey: sessionKey,
		Text: text, Items: append([]NoticeItem(nil), items...), State: "open"}, nil
}

// Get returns a notice. The error wraps sql.ErrNoRows when the id is unknown.
func (n *Notices) Get(ctx context.Context, id int64) (Notice, error) {
	var (
		out      Notice
		at       int64
		rawItems string
	)
	err := n.db.QueryRowContext(ctx,
		`SELECT id, stamp, at, session_key, text, items, state FROM notices WHERE id = ?`, id).
		Scan(&out.ID, &out.Stamp, &at, &out.SessionKey, &out.Text, &rawItems, &out.State)
	if err != nil {
		return Notice{}, fmt.Errorf("notice %d: %w", id, err)
	}
	out.At = time.Unix(at, 0)
	var list []noticeItemJSON
	if err := json.Unmarshal([]byte(rawItems), &list); err != nil {
		return Notice{}, fmt.Errorf("notice %d items: %w", id, err)
	}
	for _, it := range list {
		out.Items = append(out.Items, NoticeItem(it))
	}
	return out, nil
}

// SetState sets the state of a notice to "open", "undone", or "kept".
func (n *Notices) SetState(ctx context.Context, id int64, state string) error {
	switch state {
	case "open", "undone", "kept":
	default:
		return fmt.Errorf("notice state %q is not open, undone, or kept", state)
	}
	res, err := n.db.ExecContext(ctx, `UPDATE notices SET state = ? WHERE id = ?`, state, id)
	if err != nil {
		return err
	}
	if c, err := res.RowsAffected(); err == nil && c == 0 {
		return fmt.Errorf("notice %d: %w", id, sql.ErrNoRows)
	}
	return nil
}

// ExpireOpen sets each open notice that is older than before to "kept". It returns the count.
func (n *Notices) ExpireOpen(ctx context.Context, before time.Time) (int, error) {
	res, err := n.db.ExecContext(ctx, `UPDATE notices SET state = 'kept' WHERE state = 'open' AND at < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	c, err := res.RowsAffected()
	return int(c), err
}
