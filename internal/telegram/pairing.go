package telegram

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
)

// The pairing limits. They are the values of Hermes gateway/pairing.py.
const (
	// PairAlphabet has 32 letters and digits. It has no 0, O, 1, or I.
	PairAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	// PairCodeLen is the number of characters in a pairing code.
	PairCodeLen = 8
	// PairCodeTTL is the time until a pairing code expires.
	PairCodeTTL = time.Hour
	// PairRateLimit is the minimum time between two requests of one user.
	PairRateLimit = 10 * time.Minute
	// PairLockout is the time that a platform stays locked after too many failed approvals.
	PairLockout = time.Hour
	// PairMaxPending is the maximum number of pending codes for each platform.
	PairMaxPending = 3
	// PairMaxFailures is the number of failed approvals that starts a lockout.
	PairMaxFailures = 5
)

// PairBusyReply is the reply when the bot cannot give a new code.
const PairBusyReply = "⏳ **Too many pairing requests**\nTry again later."

// The pairing status values in the pairing table.
const (
	PairPending  = "pending"
	PairApproved = "approved"
	PairRevoked  = "revoked"
)

// The pairing errors.
var (
	// ErrPairRateLimited means that the user asked for a code less than 10 min ago.
	// The bot must not reply.
	ErrPairRateLimited = errors.New("pairing request too soon after the last request")
	// ErrTooManyPending means that the platform has the maximum number of pending codes.
	ErrTooManyPending = errors.New("too many pending pairing requests")
	// ErrPairLockedOut means that the platform is locked after too many failed approvals.
	ErrPairLockedOut = errors.New("pairing is locked after too many failed approvals")
	// ErrPairCodeNotFound means that no pending code matches.
	ErrPairCodeNotFound = errors.New("pairing code not found")
	// ErrPairCodeExpired means that the code matches, but it is older than 1 h.
	ErrPairCodeExpired = errors.New("pairing code expired")
	// ErrPairUserNotFound means that the user has no approved or pending pairing.
	ErrPairUserNotFound = errors.New("pairing user not found")
)

// Pairing keeps the pairing codes and the approved users in the pairing table
// of state.db (Hermes gateway/pairing.py). It holds no cache, so the
// running gateway sees an approval from the CLI at once.
//
// The table keeps a salted SHA-256 hash of each code, not the code. The kv
// table keeps the time of the last request of each user, the failed approval
// count of each platform, and the end of the lockout of each platform.
type Pairing struct {
	DB *db.DB
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time
	// Rand gives the random bytes for the codes and the salts. Nil means crypto/rand.
	Rand io.Reader
}

// PairRow is one pending or approved user.
type PairRow struct {
	Platform   string
	UserID     string
	UserName   string
	Status     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	ApprovedAt time.Time // zero for a pending user
}

// PairReply returns the reply that gives a pairing code to the user.
func PairReply(platform, code string) string {
	return "🔐 **Pairing needed**\nI do not know you yet. Your pairing code is `" + code + "`.\n\n" +
		"Ask the owner to run this command:\n" +
		"`tether pairing approve " + platform + " " + code + "`\n" +
		"_The code expires in " + durationText(PairCodeTTL) + "._"
}

// durationText returns d as whole hours, for example "1 hour", or else as whole minutes,
// for example "30 minutes".
func durationText(d time.Duration) string {
	n, unit := int(d/time.Minute), "minute"
	if d >= time.Hour && d%time.Hour == 0 {
		n, unit = int(d/time.Hour), "hour"
	}
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

// Request makes a new pairing code for an unknown user and returns the code
// and the reply for the user. Send reply when it is not empty.
//
// On ErrPairRateLimited, reply is empty, so the bot stays silent. On
// ErrTooManyPending and ErrPairLockedOut, reply is PairBusyReply, and the
// user cannot ask again for 10 min. A new request after 10 min replaces the
// old code of the user. For an approved user, Request does nothing and
// returns empty values.
func (p *Pairing) Request(ctx context.Context, platform, userID, userName string) (code string, reply string, err error) {
	now := p.now()
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", "", fmt.Errorf("pairing: request: %w", err)
	}
	defer tx.Rollback()

	rateKey := pairRateKey(platform, userID)
	last, err := kvTime(ctx, tx, rateKey)
	if err != nil {
		return "", "", err
	}
	if now.Sub(last) < PairRateLimit {
		return "", "", ErrPairRateLimited
	}
	var status string
	err = tx.QueryRowContext(ctx, "SELECT status FROM pairing WHERE platform=? AND user_id=?",
		platform, userID).Scan(&status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return "", "", fmt.Errorf("pairing: request: %w", err)
	case status == PairApproved:
		return "", "", nil
	}

	limitErr, err := p.requestLimit(ctx, tx, platform, userID, now)
	if err != nil {
		return "", "", err
	}
	if limitErr != nil {
		// Hermes records a rate limit here, so later messages get no reply.
		if err := kvSetTime(ctx, tx, rateKey, now); err != nil {
			return "", "", err
		}
		if err := tx.Commit(); err != nil {
			return "", "", fmt.Errorf("pairing: request: %w", err)
		}
		return "", PairBusyReply, limitErr
	}

	code, err = newPairCode(p.rand())
	if err != nil {
		return "", "", err
	}
	hash, err := p.hashNew(code)
	if err != nil {
		return "", "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO pairing(platform, user_id, user_name, code, status, created_at, expires_at, approved_at)
VALUES (?, ?, ?, ?, ?, ?, ?, NULL)
ON CONFLICT(platform, user_id) DO UPDATE SET user_name=excluded.user_name, code=excluded.code,
  status=excluded.status, created_at=excluded.created_at, expires_at=excluded.expires_at, approved_at=NULL`,
		platform, userID, userName, hash, PairPending, now.UnixMilli(), now.Add(PairCodeTTL).UnixMilli())
	if err != nil {
		return "", "", fmt.Errorf("pairing: request: %w", err)
	}
	if err := kvSetTime(ctx, tx, rateKey, now); err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", fmt.Errorf("pairing: request: %w", err)
	}
	return code, PairReply(platform, code), nil
}

// requestLimit returns ErrPairLockedOut or ErrTooManyPending when the platform
// cannot give a new code, else nil. The pending code of userID does not count,
// because the new code replaces it.
func (p *Pairing) requestLimit(ctx context.Context, tx *sql.Tx, platform, userID string, now time.Time) (error, error) {
	until, err := kvTime(ctx, tx, pairLockoutKey(platform))
	if err != nil {
		return nil, err
	}
	if now.Before(until) {
		return ErrPairLockedOut, nil
	}
	var n int
	err = tx.QueryRowContext(ctx,
		"SELECT count(*) FROM pairing WHERE platform=? AND status=? AND expires_at>? AND user_id<>?",
		platform, PairPending, now.UnixMilli(), userID).Scan(&n)
	if err != nil {
		return nil, fmt.Errorf("pairing: count pending codes: %w", err)
	}
	if n >= PairMaxPending {
		return ErrTooManyPending, nil
	}
	return nil, nil
}

// Approve approves the pending code and returns the id of the user. It
// ignores the case of the code and the spaces around it. A code that does not
// match, or that expired, counts as a failed approval. After 5 failed
// approvals, the platform is locked for 1 h, and Approve returns
// ErrPairLockedOut also for a correct code.
func (p *Pairing) Approve(ctx context.Context, platform, code string) (userID string, err error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	now := p.now()
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("pairing: approve: %w", err)
	}
	defer tx.Rollback()

	until, err := kvTime(ctx, tx, pairLockoutKey(platform))
	if err != nil {
		return "", err
	}
	if now.Before(until) {
		return "", ErrPairLockedOut
	}
	matchID, expires, err := matchPending(ctx, tx, platform, code)
	if err != nil {
		return "", err
	}
	if matchID == "" || !now.Before(expires) {
		if err := recordPairFailure(ctx, tx, platform, now); err != nil {
			return "", err
		}
		if err := tx.Commit(); err != nil {
			return "", fmt.Errorf("pairing: approve: %w", err)
		}
		if matchID == "" {
			return "", ErrPairCodeNotFound
		}
		return "", ErrPairCodeExpired
	}
	_, err = tx.ExecContext(ctx, "UPDATE pairing SET status=?, approved_at=?, code='' WHERE platform=? AND user_id=?",
		PairApproved, now.UnixMilli(), platform, matchID)
	if err != nil {
		return "", fmt.Errorf("pairing: approve: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("pairing: approve: %w", err)
	}
	return matchID, nil
}

// matchPending returns the user and the expiry time of the pending row whose
// code hash matches code. userID is empty when no row matches. The hash
// compare takes constant time.
func matchPending(ctx context.Context, tx *sql.Tx, platform, code string) (userID string, expires time.Time, err error) {
	rows, err := tx.QueryContext(ctx, "SELECT user_id, code, expires_at FROM pairing WHERE platform=? AND status=?",
		platform, PairPending)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("pairing: read pending codes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var uid, stored string
		var exp int64
		if err := rows.Scan(&uid, &stored, &exp); err != nil {
			return "", time.Time{}, fmt.Errorf("pairing: read pending codes: %w", err)
		}
		if userID == "" && pairHashMatches(stored, code) {
			userID, expires = uid, time.UnixMilli(exp)
		}
	}
	if err := rows.Err(); err != nil {
		return "", time.Time{}, fmt.Errorf("pairing: read pending codes: %w", err)
	}
	return userID, expires, nil
}

// recordPairFailure adds one failed approval for the platform. At 5 failures,
// it starts a 1 h lockout and sets the count to zero again (Hermes).
func recordPairFailure(ctx context.Context, tx *sql.Tx, platform string, now time.Time) error {
	key := pairFailuresKey(platform)
	v, _, err := kvGetTx(ctx, tx, key)
	if err != nil {
		return err
	}
	fails, _ := strconv.Atoi(v)
	fails++
	if fails >= PairMaxFailures {
		if err := kvSetTime(ctx, tx, pairLockoutKey(platform), now.Add(PairLockout)); err != nil {
			return err
		}
		fails = 0
	}
	return kvSetTx(ctx, tx, key, strconv.Itoa(fails))
}

// IsApproved reports whether pairing approved the user on the platform. It
// reads the table on each call. A database error gives false.
func (p *Pairing) IsApproved(ctx context.Context, platform, userID string) bool {
	var one int
	err := p.DB.QueryRowContext(ctx, "SELECT 1 FROM pairing WHERE platform=? AND user_id=? AND status=?",
		platform, userID, PairApproved).Scan(&one)
	return err == nil
}

// List returns the approved users and the pending codes that did not expire.
// An empty platform lists all platforms.
func (p *Pairing) List(ctx context.Context, platform string) ([]PairRow, error) {
	rows, err := p.DB.QueryContext(ctx, `SELECT platform, user_id, user_name, status, created_at, expires_at, approved_at
FROM pairing WHERE (?='' OR platform=?) AND (status=? OR (status=? AND expires_at>?))
ORDER BY platform, status, created_at, user_id`,
		platform, platform, PairApproved, PairPending, p.now().UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("pairing: list: %w", err)
	}
	defer rows.Close()
	var out []PairRow
	for rows.Next() {
		var r PairRow
		var created, expires int64
		var approved sql.NullInt64
		if err := rows.Scan(&r.Platform, &r.UserID, &r.UserName, &r.Status, &created, &expires, &approved); err != nil {
			return nil, fmt.Errorf("pairing: list: %w", err)
		}
		r.CreatedAt, r.ExpiresAt = time.UnixMilli(created), time.UnixMilli(expires)
		if approved.Valid {
			r.ApprovedAt = time.UnixMilli(approved.Int64)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pairing: list: %w", err)
	}
	return out, nil
}

// Revoke removes the approval or the pending code of the user. It returns
// ErrPairUserNotFound when the user has neither.
func (p *Pairing) Revoke(ctx context.Context, platform, userID string) error {
	res, err := p.DB.ExecContext(ctx,
		"UPDATE pairing SET status=?, code='' WHERE platform=? AND user_id=? AND status IN (?, ?)",
		PairRevoked, platform, userID, PairApproved, PairPending)
	if err != nil {
		return fmt.Errorf("pairing: revoke: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("pairing: revoke: %w", err)
	} else if n == 0 {
		return ErrPairUserNotFound
	}
	return nil
}

// newPairCode reads random bytes from r and maps them onto PairAlphabet. It
// rejects the bytes above the largest multiple of the alphabet size, so each
// letter has the same chance.
func newPairCode(r io.Reader) (string, error) {
	const n = len(PairAlphabet)
	limit := 256 - 256%n
	out := make([]byte, 0, PairCodeLen)
	buf := make([]byte, PairCodeLen)
	for len(out) < PairCodeLen {
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", fmt.Errorf("pairing: read random bytes: %w", err)
		}
		for _, b := range buf {
			if int(b) < limit && len(out) < PairCodeLen {
				out = append(out, PairAlphabet[int(b)%n])
			}
		}
	}
	return string(out), nil
}

// hashNew returns "<salt hex>$<hash hex>" for code, with a new 16-byte salt.
func (p *Pairing) hashNew(code string) (string, error) {
	salt := make([]byte, 16)
	if _, err := io.ReadFull(p.rand(), salt); err != nil {
		return "", fmt.Errorf("pairing: read random bytes: %w", err)
	}
	return hex.EncodeToString(salt) + "$" + pairHash(salt, code), nil
}

// pairHash returns the hex SHA-256 of salt and code (Hermes _hash_code).
func pairHash(salt []byte, code string) string {
	sum := sha256.Sum256(append(append([]byte{}, salt...), code...))
	return hex.EncodeToString(sum[:])
}

// pairHashMatches reports whether stored is the hash of code. A malformed
// stored value never matches.
func pairHashMatches(stored, code string) bool {
	saltHex, want, ok := strings.Cut(stored, "$")
	if !ok {
		return false
	}
	salt, err := hex.DecodeString(saltHex)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(pairHash(salt, code)), []byte(want)) == 1
}

func (p *Pairing) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Pairing) rand() io.Reader {
	if p.Rand != nil {
		return p.Rand
	}
	return rand.Reader
}

// The kv keys of the pairing limits.
func pairRateKey(platform, userID string) string {
	return "pairing.last_request:" + platform + ":" + userID
}
func pairFailuresKey(platform string) string { return "pairing.failures:" + platform }
func pairLockoutKey(platform string) string  { return "pairing.lockout_until:" + platform }

func kvGetTx(ctx context.Context, tx *sql.Tx, key string) (string, bool, error) {
	var v string
	err := tx.QueryRowContext(ctx, "SELECT value FROM kv WHERE key=?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("pairing: read kv %q: %w", key, err)
	}
	return v, true, nil
}

func kvSetTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx,
		"INSERT INTO kv(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	if err != nil {
		return fmt.Errorf("pairing: write kv %q: %w", key, err)
	}
	return nil
}

// kvTime reads a Unix millisecond time from kv. A missing or bad value gives
// the zero time.
func kvTime(ctx context.Context, tx *sql.Tx, key string) (time.Time, error) {
	v, ok, err := kvGetTx(ctx, tx, key)
	if err != nil || !ok {
		return time.Time{}, err
	}
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, nil
	}
	return time.UnixMilli(ms), nil
}

func kvSetTime(ctx context.Context, tx *sql.Tx, key string, t time.Time) error {
	return kvSetTx(ctx, tx, key, strconv.FormatInt(t.UnixMilli(), 10))
}
