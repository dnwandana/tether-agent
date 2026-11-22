package dashboard

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
)

// ErrNoSession is the error of a missing, wrong, expired, or revoked device session.
var ErrNoSession = errors.New("dashboard: no valid device session")

// ErrBadCode is the error of a sign-in code that is wrong, used, or expired.
var ErrBadCode = errors.New("dashboard: the sign-in code is not valid or has expired")

// touchEvery is the shortest time between two writes of last_seen.
const touchEvery = 5 * time.Minute

// codeAlphabet is the Crockford base32 alphabet. It has no I, L, O, or U.
const codeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// Device is one device session.
type Device struct {
	ID, Label, UserAgent, IP   string
	Created, LastSeen, Expires time.Time
	Revoked                    bool
}

// Store keeps the device sessions and the sign-in codes in state.db.
type Store struct{ db *db.DB }

// NewStore returns a Store over d.
func NewStore(d *db.DB) *Store { return &Store{db: d} }

// hashOf returns the SHA-256 hex of v.
func hashOf(v string) string {
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:])
}

// randBytes returns n random bytes.
func randBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// CreateSession stores a new device session and returns its id and the two raw values. The raw
// values are not stored, so this is the only time that the caller can read them. It also deletes
// the sessions that are already expired.
func (s *Store) CreateSession(ctx context.Context, label, ua, ip string, ttl time.Duration, now time.Time) (id, cookie, header string, err error) {
	raw, err := randBytes(16 + 32 + 32)
	if err != nil {
		return "", "", "", err
	}
	id = hex.EncodeToString(raw[:16])
	cookie = base64.RawURLEncoding.EncodeToString(raw[16:48])
	header = base64.RawURLEncoding.EncodeToString(raw[48:])
	if _, err = s.db.ExecContext(ctx, `DELETE FROM dashboard_sessions WHERE expires_at <= ?`, now.UnixMilli()); err != nil {
		return "", "", "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO dashboard_sessions
		(id, cookie_hash, header_hash, label, user_agent, ip, created_at, last_seen, expires_at, revoked)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		id, hashOf(cookie), hashOf(header), label, ua, ip, now.UnixMilli(), now.UnixMilli(), now.Add(ttl).UnixMilli())
	if err != nil {
		return "", "", "", err
	}
	return id, cookie, header, nil
}

// Lookup returns the device of a cookie value and a header value. It returns ErrNoSession when a
// value is missing or wrong, or when the session is expired or revoked.
func (s *Store) Lookup(ctx context.Context, cookie, header string, now time.Time) (Device, error) {
	if header == "" {
		return Device{}, ErrNoSession
	}
	return s.lookup(ctx, cookie, header, true, now)
}

// lookupCookie returns the device of a cookie value alone. Only a GET of /api/files/{id} uses it,
// because an <img>, <audio>, or <video> tag cannot send the session header.
func (s *Store) lookupCookie(ctx context.Context, cookie string, now time.Time) (Device, error) {
	return s.lookup(ctx, cookie, "", false, now)
}

// lookup finds the session of a cookie value. It compares the header value only when
// checkHeader is true.
func (s *Store) lookup(ctx context.Context, cookie, header string, checkHeader bool, now time.Time) (Device, error) {
	if cookie == "" {
		return Device{}, ErrNoSession
	}
	var (
		d                      Device
		headerHash             string
		created, seen, expires int64
		revoked                int
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, header_hash, label, user_agent, ip, created_at, last_seen, expires_at, revoked
		FROM dashboard_sessions WHERE cookie_hash = ?`, hashOf(cookie)).
		Scan(&d.ID, &headerHash, &d.Label, &d.UserAgent, &d.IP, &created, &seen, &expires, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNoSession
	}
	if err != nil {
		return Device{}, err
	}
	if checkHeader && subtle.ConstantTimeCompare([]byte(headerHash), []byte(hashOf(header))) != 1 {
		return Device{}, ErrNoSession
	}
	if revoked != 0 || expires <= now.UnixMilli() {
		return Device{}, ErrNoSession
	}
	d.Created, d.LastSeen, d.Expires = time.UnixMilli(created), time.UnixMilli(seen), time.UnixMilli(expires)
	return d, nil
}

// Touch sets last_seen of a session to now. It writes nothing when last_seen is less than 5
// minutes old.
func (s *Store) Touch(ctx context.Context, id string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dashboard_sessions SET last_seen = ? WHERE id = ? AND last_seen <= ?`,
		now.UnixMilli(), id, now.Add(-touchEvery).UnixMilli())
	return err
}

// Revoke revokes one session. It returns ErrNoSession for an unknown id.
func (s *Store) Revoke(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE dashboard_sessions SET revoked = 1 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNoSession
	}
	return nil
}

// RevokeOthers revokes each session except keepID that is live at now. It returns the count.
func (s *Store) RevokeOthers(ctx context.Context, keepID string, now time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE dashboard_sessions SET revoked = 1 WHERE revoked = 0 AND expires_at > ? AND id <> ?`,
		now.UnixMilli(), keepID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// RevokeAll revokes each session that is live at now. It returns the count.
func (s *Store) RevokeAll(ctx context.Context, now time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE dashboard_sessions SET revoked = 1 WHERE revoked = 0 AND expires_at > ?`, now.UnixMilli())
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// List returns the sessions that are not revoked and not expired, the newest sign-in first.
func (s *Store) List(ctx context.Context, now time.Time) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, label, user_agent, ip, created_at, last_seen, expires_at
		FROM dashboard_sessions WHERE revoked = 0 AND expires_at > ? ORDER BY created_at DESC, rowid DESC`, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		var created, seen, expires int64
		if err := rows.Scan(&d.ID, &d.Label, &d.UserAgent, &d.IP, &created, &seen, &expires); err != nil {
			return nil, err
		}
		d.Created, d.LastSeen, d.Expires = time.UnixMilli(created), time.UnixMilli(seen), time.UnixMilli(expires)
		out = append(out, d)
	}
	return out, rows.Err()
}

// AddCode stores a new one-time sign-in code and returns it as XXXX-XXXX. It deletes the
// expired codes first.
func (s *Store) AddCode(ctx context.Context, ttl time.Duration, now time.Time) (code string, err error) {
	raw, err := randBytes(5)
	if err != nil {
		return "", err
	}
	var bits uint64
	for _, b := range raw {
		bits = bits<<8 | uint64(b)
	}
	chars := make([]byte, 8)
	for i := range chars {
		chars[i] = codeAlphabet[(bits>>(5*uint(7-i)))&31]
	}
	if _, err = s.db.ExecContext(ctx, `DELETE FROM dashboard_codes WHERE expires_at <= ?`, now.UnixMilli()); err != nil {
		return "", err
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO dashboard_codes (code_hash, expires_at) VALUES (?, ?)`,
		hashOf(string(chars)), now.Add(ttl).UnixMilli()); err != nil {
		return "", err
	}
	return string(chars[:4]) + "-" + string(chars[4:]), nil
}

// ConsumeCode deletes a code and returns nil when the code is valid. A code works once. It
// accepts lower case and a code with no dash.
func (s *Store) ConsumeCode(ctx context.Context, code string, now time.Time) error {
	c := strings.ToUpper(strings.ReplaceAll(code, "-", ""))
	if len(c) != 8 {
		return ErrBadCode
	}
	for i := 0; i < len(c); i++ {
		if strings.IndexByte(codeAlphabet, c[i]) < 0 {
			return ErrBadCode
		}
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM dashboard_codes WHERE code_hash = ? AND expires_at > ?`,
		hashOf(c), now.UnixMilli())
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrBadCode
	}
	return nil
}
