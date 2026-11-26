package learn

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/skills"
)

// SkillLog keeps the skill copies, the skill reads, the curator flags, and the curator
// run time in state.db.
type SkillLog struct {
	db  *db.DB
	now func() time.Time
}

var (
	_ skills.CopyRecorder = (*SkillLog)(nil)
	_ skills.CuratorStore = (*SkillLog)(nil)
)

// NewSkillLog returns a SkillLog on d.
func NewSkillLog(d *db.DB) *SkillLog { return &SkillLog{db: d, now: time.Now} }

const (
	kvCuratorLastRun    = "skills.curator.last_run"
	kvCuratorBackfilled = "skills.curator.backfilled"
)

// copyStamp returns 4 random lower-case hex characters.
func copyStamp() string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on a supported system. A stamp of zeros still works.
		return "0000"
	}
	return hex.EncodeToString(b[:])
}

// AddCopy stores c and returns it with its ID and Stamp. A copy that is not a create
// and has no Path gets the path .history/<id>.
func (l *SkillLog) AddCopy(c skills.Copy) (skills.Copy, error) {
	if c.Stamp == "" {
		c.Stamp = copyStamp()
	}
	if c.At.IsZero() {
		c.At = l.now()
	}
	ctx := context.Background()
	res, err := l.db.ExecContext(ctx,
		`INSERT INTO skill_copies(stamp, name, rel_dir, at, action, source, session_id, path, hash_after, undone_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Stamp, c.Name, c.RelDir, c.At.Unix(), c.Action, c.Source, c.SessionID, c.Path, c.HashAfter, nullID(c.UndoneBy))
	if err != nil {
		return skills.Copy{}, fmt.Errorf("learn: record skill copy: %w", err)
	}
	c.ID, err = res.LastInsertId()
	if err != nil {
		return skills.Copy{}, err
	}
	if c.Path == "" && c.Action != "create" {
		c.Path = ".history/" + strconv.FormatInt(c.ID, 10)
		if _, err := l.db.ExecContext(ctx, `UPDATE skill_copies SET path = ? WHERE id = ?`, c.Path, c.ID); err != nil {
			return skills.Copy{}, fmt.Errorf("learn: set skill copy path: %w", err)
		}
	}
	return c, nil
}

// FinishCopy sets the hash of the skill folder after the change.
func (l *SkillLog) FinishCopy(id int64, hashAfter string) error {
	return l.update(`UPDATE skill_copies SET hash_after = ? WHERE id = ?`, hashAfter, id)
}

// MarkCopyUndone sets the row that undid copy id. An undoID of 0 writes NULL.
func (l *SkillLog) MarkCopyUndone(id, undoID int64) error {
	return l.update(`UPDATE skill_copies SET undone_by = ? WHERE id = ?`, nullID(undoID), id)
}

// update runs a statement that must change one skill_copies row.
func (l *SkillLog) update(query string, args ...any) error {
	res, err := l.db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("learn: update skill copy: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("learn: skill copy %v: %w", args[len(args)-1], sql.ErrNoRows)
	}
	return nil
}

const copyCols = `id, stamp, name, rel_dir, at, action, source, session_id, path, hash_after, undone_by`

func scanCopy(r rowScanner) (skills.Copy, error) {
	var c skills.Copy
	var at int64
	var undone sql.NullInt64
	if err := r.Scan(&c.ID, &c.Stamp, &c.Name, &c.RelDir, &at, &c.Action, &c.Source, &c.SessionID, &c.Path, &c.HashAfter, &undone); err != nil {
		return skills.Copy{}, err
	}
	c.At = time.Unix(at, 0)
	c.UndoneBy = undone.Int64
	return c, nil
}

// GetCopy returns the copy row with the id. The error wraps sql.ErrNoRows for an unknown id.
func (l *SkillLog) GetCopy(id int64) (skills.Copy, error) {
	c, err := scanCopy(l.db.QueryRow(`SELECT `+copyCols+` FROM skill_copies WHERE id = ?`, id))
	if err != nil {
		return skills.Copy{}, fmt.Errorf("learn: skill copy %d: %w", id, err)
	}
	return c, nil
}

// queryCopies reads all rows of a query and closes the rows before it returns.
func (l *SkillLog) queryCopies(query string, args ...any) ([]skills.Copy, error) {
	rows, err := l.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("learn: list skill copies: %w", err)
	}
	defer rows.Close()
	var out []skills.Copy
	for rows.Next() {
		c, err := scanCopy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListCopies returns the newest copies of a skill. An empty name lists all skills.
// A limit of 0 or less uses 10.
func (l *SkillLog) ListCopies(name string, limit int) ([]skills.Copy, error) {
	if limit <= 0 {
		limit = 10
	}
	if name == "" {
		return l.queryCopies(`SELECT `+copyCols+` FROM skill_copies ORDER BY id DESC LIMIT ?`, limit)
	}
	return l.queryCopies(`SELECT `+copyCols+` FROM skill_copies WHERE name = ? ORDER BY id DESC LIMIT ?`, name, limit)
}

// PruneCopies deletes the rows of name after the newest keep rows. It returns the deleted
// rows, oldest first, so the caller can delete the copy folders. A keep of 0 or less
// deletes nothing.
func (l *SkillLog) PruneCopies(name string, keep int) ([]skills.Copy, error) {
	if keep <= 0 {
		return nil, nil
	}
	old, err := l.queryCopies(`SELECT `+copyCols+` FROM skill_copies WHERE name = ? ORDER BY id DESC LIMIT -1 OFFSET ?`, name, keep)
	if err != nil || len(old) == 0 {
		return nil, err
	}
	for _, c := range old {
		if _, err := l.db.Exec(`DELETE FROM skill_copies WHERE id = ?`, c.ID); err != nil {
			return nil, fmt.Errorf("learn: prune skill copies: %w", err)
		}
	}
	for i, j := 0, len(old)-1; i < j; i, j = i+1, j-1 {
		old[i], old[j] = old[j], old[i]
	}
	return old, nil
}

// RecordRead stores one read of a skill.
func (l *SkillLog) RecordRead(ctx context.Context, name, source, sessionID string) error {
	_, err := l.db.ExecContext(ctx, `INSERT INTO skill_reads(name, at, source, session_id) VALUES (?, ?, ?, ?)`,
		name, l.now().Unix(), source, sessionID)
	if err != nil {
		return fmt.Errorf("learn: record skill read: %w", err)
	}
	return nil
}

const flagCols = `id, name, rel_dir, kept, first_seen, archived_at, archived_path`

func scanFlag(r rowScanner) (skills.Flag, error) {
	var f skills.Flag
	var kept, seen int64
	var archived sql.NullInt64
	if err := r.Scan(&f.ID, &f.Name, &f.RelDir, &kept, &seen, &archived, &f.ArchivedPath); err != nil {
		return skills.Flag{}, err
	}
	f.Kept = kept != 0
	f.FirstSeen = time.Unix(seen, 0)
	if archived.Valid {
		f.ArchivedAt = time.Unix(archived.Int64, 0)
	}
	return f, nil
}

// Flags returns the curator flags, keyed by skill name.
func (l *SkillLog) Flags(ctx context.Context) (map[string]skills.Flag, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT `+flagCols+` FROM skill_flags`)
	if err != nil {
		return nil, fmt.Errorf("learn: list skill flags: %w", err)
	}
	defer rows.Close()
	out := map[string]skills.Flag{}
	for rows.Next() {
		f, err := scanFlag(rows)
		if err != nil {
			return nil, err
		}
		out[f.Name] = f
	}
	return out, rows.Err()
}

// SaveFlag inserts or updates the flag of f.Name and returns the stored row. A zero
// FirstSeen writes the current time on insert and keeps the stored value on update.
func (l *SkillLog) SaveFlag(ctx context.Context, f skills.Flag) (skills.Flag, error) {
	var seen, archived any
	if !f.FirstSeen.IsZero() {
		seen = f.FirstSeen.Unix()
	}
	if !f.ArchivedAt.IsZero() {
		archived = f.ArchivedAt.Unix()
	}
	kept := 0
	if f.Kept {
		kept = 1
	}
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO skill_flags(name, rel_dir, kept, first_seen, archived_at, archived_path)
		 VALUES (?, ?, ?, COALESCE(?, ?), ?, ?)
		 ON CONFLICT(name) DO UPDATE SET rel_dir = excluded.rel_dir, kept = excluded.kept,
		   first_seen = COALESCE(?, skill_flags.first_seen),
		   archived_at = excluded.archived_at, archived_path = excluded.archived_path`,
		f.Name, f.RelDir, kept, seen, l.now().Unix(), archived, f.ArchivedPath, seen)
	if err != nil {
		return skills.Flag{}, fmt.Errorf("learn: save skill flag: %w", err)
	}
	return scanFlag(l.db.QueryRowContext(ctx, `SELECT `+flagCols+` FROM skill_flags WHERE name = ?`, f.Name))
}

// LastRun returns the time of the last curator run. ok is false when it never ran.
func (l *SkillLog) LastRun(ctx context.Context) (time.Time, bool, error) {
	v, ok, err := l.db.KVGet(ctx, kvCuratorLastRun)
	if err != nil || !ok {
		return time.Time{}, false, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, false, nil
	}
	return time.Unix(n, 0), true, nil
}

// SetLastRun stores the time of the last curator run.
func (l *SkillLog) SetLastRun(ctx context.Context, t time.Time) error {
	return l.db.KVSet(ctx, kvCuratorLastRun, strconv.FormatInt(t.Unix(), 10))
}

// LastActivity returns the newest time that the owner or the agent used the skill. Only
// the reads view, command, and cron outside a review session count, and only the copies
// with source chat or owner. ok is false when there is no such time.
func (l *SkillLog) LastActivity(ctx context.Context, name string) (time.Time, bool, error) {
	var read, copied sql.NullInt64
	if err := l.db.QueryRowContext(ctx,
		`SELECT max(r.at) FROM skill_reads r LEFT JOIN sessions s ON s.id = r.session_id
		 WHERE r.name = ? AND r.source IN ('view', 'command', 'cron') AND (s.kind IS NULL OR s.kind != 'review')`,
		name).Scan(&read); err != nil {
		return time.Time{}, false, fmt.Errorf("learn: skill reads: %w", err)
	}
	if err := l.db.QueryRowContext(ctx,
		`SELECT max(at) FROM skill_copies WHERE name = ? AND source IN ('chat', 'owner')`,
		name).Scan(&copied); err != nil {
		return time.Time{}, false, fmt.Errorf("learn: skill copies: %w", err)
	}
	var best int64
	ok := false
	for _, v := range []sql.NullInt64{read, copied} {
		if v.Valid && (!ok || v.Int64 > best) {
			best, ok = v.Int64, true
		}
	}
	if !ok {
		return time.Time{}, false, nil
	}
	return time.Unix(best, 0), true, nil
}

var (
	cronSkillRe   = regexp.MustCompile(`\[Skill: ([^\]\n]+)\]`)
	commandPrefix = `[IMPORTANT: The user has invoked the "`
)

type backfillRead struct {
	name, source, session string
	at                    int64
}

// Backfill fills skill_reads from the old messages. It runs once.
func (l *SkillLog) Backfill(ctx context.Context) error {
	if _, ok, err := l.db.KVGet(ctx, kvCuratorBackfilled); err != nil || ok {
		return err
	}
	reads, err := l.scanBackfill(ctx)
	if err != nil {
		return err
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("learn: backfill skill reads: %w", err)
	}
	defer tx.Rollback()
	for _, r := range reads {
		if _, err := tx.ExecContext(ctx, `INSERT INTO skill_reads(name, at, source, session_id) VALUES (?, ?, ?, ?)`,
			r.name, r.at, r.source, r.session); err != nil {
			return fmt.Errorf("learn: backfill skill reads: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("learn: backfill skill reads: %w", err)
	}
	return l.db.KVSet(ctx, kvCuratorBackfilled, strconv.FormatInt(l.now().Unix(), 10))
}

// scanBackfill reads the messages, closes the rows, and returns the reads that it found.
func (l *SkillLog) scanBackfill(ctx context.Context) ([]backfillRead, error) {
	rows, err := l.db.QueryContext(ctx,
		`SELECT m.session_id, m.role, m.content, COALESCE(m.tool_calls, ''), m.timestamp, s.kind
		 FROM messages m JOIN sessions s ON s.id = m.session_id
		 WHERE s.kind IN ('chat', 'cron')
		   AND ((m.role = 'assistant' AND s.kind = 'chat' AND m.tool_calls LIKE '%"skill_view"%')
		     OR (m.role = 'user' AND s.kind = 'chat' AND m.content LIKE ? || '%')
		     OR (m.role = 'user' AND s.kind = 'cron' AND m.content LIKE '%[Skill: %'))
		 ORDER BY m.id`, commandPrefix)
	if err != nil {
		return nil, fmt.Errorf("learn: backfill skill reads: %w", err)
	}
	defer rows.Close()
	var out []backfillRead
	for rows.Next() {
		var sid, role, content, calls, kind string
		var ts int64
		if err := rows.Scan(&sid, &role, &content, &calls, &ts, &kind); err != nil {
			return nil, err
		}
		add := func(name, source string) {
			if name != "" {
				out = append(out, backfillRead{name: name, source: source, session: sid, at: ts / 1000})
			}
		}
		switch {
		case role == "assistant" && kind == "chat":
			var tcs []struct{ Name, Args string }
			if json.Unmarshal([]byte(calls), &tcs) != nil {
				continue
			}
			for _, tc := range tcs {
				if tc.Name != "skill_view" {
					continue
				}
				var a struct {
					Name string `json:"name"`
				}
				if json.Unmarshal([]byte(tc.Args), &a) == nil {
					add(a.Name, "view")
				}
			}
		case role == "user" && kind == "chat":
			if rest, ok := strings.CutPrefix(content, commandPrefix); ok {
				if i := strings.IndexByte(rest, '"'); i > 0 {
					add(rest[:i], "command")
				}
			}
		case role == "user" && kind == "cron":
			for _, m := range cronSkillRe.FindAllStringSubmatch(content, -1) {
				add(strings.TrimSpace(m[1]), "cron")
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
