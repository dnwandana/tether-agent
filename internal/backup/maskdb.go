package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/security"
)

// freeTextColumns lists the columns of state.db that hold text from the user, the model, or a
// tool. maskDB masks the known secret values in these columns only.
var freeTextColumns = map[string][]string{
	"messages":           {"content", "tool_calls", "reasoning"},
	"compactions":        {"summary"},
	"sessions":           {"title", "prompt_override"},
	"cron_jobs":          {"prompt", "last_output"},
	"cron_runs":          {"output", "delivery_error"},
	"delegation_batches": {"tasks", "results"},
	"turn_markers":       {"queued"},
	"goals":              {"state"},
	"approval_log":       {"preview"},
	"approval_grants":    {"preview"},
	"held_sends":         {"text", "reason", "target_title"},
}

// stateColumns lists every other TEXT column of the schema. maskDB never masks these columns.
// They hold ids, keys, and settings that a restore must give back unchanged. A .env value such
// as a Telegram user id also occurs in sessions.chat_id and pairing.user_id, and a mask there
// would break the state. A test compares the two lists with the live schema.
var stateColumns = map[string][]string{
	"sessions": {"id", "session_key", "source", "chat_id", "thread_id", "user_id",
		"model_override", "parent_id", "kind", "effort"},
	"messages":    {"session_id", "role", "tool_call_id", "tool_name", "model", "provider", "finish_reason"},
	"compactions": {"session_id"},
	"cron_jobs": {"id", "name", "schedule", "script", "deliver", "skills", "model", "toolsets",
		"workdir", "origin", "created_by_session", "last_status"},
	"cron_runs": {"job_id", "status", "delivery", "session_id"},
	"approval_grants": {"id", "pattern_id", "content_sha256", "workdir", "via", "programs", "server",
		"tool_name"},
	"pairing":            {"platform", "user_id", "user_name", "code", "status"},
	"active_session":     {"session_key", "session_id"},
	"turn_markers":       {"session_key", "session_id"},
	"delegation_batches": {"id", "parent_session_key", "parent_session_id", "status"},
	"kv":                 {"key", "value"},
	"schema_version":     {"name"},
	"goals":              {"session_id"},
	"acp_sessions":       {"session_id", "agent", "acp_session_id", "cwd"},
	"approval_log": {"session_id", "scope", "kind", "tool", "server", "tool_name", "rule", "class",
		"decision", "source", "answer", "content_sha256"},
	"chat_modes":  {"scope", "mode"},
	"secret_pins": {"name", "path", "sha256"},
	"held_sends":  {"id", "kind", "job_id", "target", "sources", "card_chat"},
}

// maskCell is one cell that maskDB changes.
type maskCell struct {
	rowid int64
	value string
}

// namedRedactor returns a redactor that replaces each value of known with the label of its
// name. known maps a value to its name, so one name can have two values.
func namedRedactor(known map[string]string) *security.Redactor {
	pairs := map[string]string{}
	var extra []string
	for _, v := range slices.Sorted(maps.Keys(known)) {
		if _, ok := pairs[known[v]]; ok {
			extra = append(extra, v)
			continue
		}
		pairs[known[v]] = v
	}
	r := security.NewNamedRedactor(pairs)
	for _, v := range extra {
		r.Add(known[v], v)
	}
	return r
}

// jsonText returns the JSON text of v without the outer quotes and without the newline. It does
// not escape HTML. The named redactor uses the same rule for the JSON form of a value.
func jsonText(v string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return v
	}
	t := strings.TrimSuffix(b.String(), "\n")
	return t[1 : len(t)-1]
}

// maskDB replaces the known secret values in the free-text columns of the DB copy d with the
// label «NAME». known maps each value to its name. It returns the count of cells that changed.
// Values shorter than 8 characters are ignored.
//
// An UPDATE leaves the old text in free pages of the file, and the FTS5 index keeps the old
// tokens until a merge. When a cell changes, maskDB rebuilds messages_fts and runs VACUUM, so
// the raw bytes of the copy do not hold the secret. Use maskDB only on a copy, never on the
// live state.db.
func maskDB(ctx context.Context, d *db.DB, known map[string]string) (cells int, err error) {
	kept := map[string]string{}
	var args []any
	for _, v := range slices.Sorted(maps.Keys(known)) {
		if utf8.RuneCountInString(v) < 8 {
			continue
		}
		kept[v] = known[v]
		args = append(args, v)
		if j := jsonText(v); j != v {
			args = append(args, j)
		}
	}
	if len(kept) == 0 {
		return 0, nil
	}
	r := namedRedactor(kept)

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("mask the DB copy: %w", err)
	}
	defer tx.Rollback()

	tables := make([]string, 0, len(freeTextColumns))
	for tbl := range freeTextColumns {
		tables = append(tables, tbl)
	}
	slices.Sort(tables)
	for _, tbl := range tables {
		for _, col := range freeTextColumns[tbl] {
			changed, err := maskColumn(ctx, tx, r, tbl, col, args)
			if err != nil {
				return 0, fmt.Errorf("mask %s.%s in the DB copy: %w", tbl, col, err)
			}
			cells += changed
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("mask the DB copy: %w", err)
	}
	if cells == 0 {
		return 0, nil
	}
	if _, err := d.ExecContext(ctx, "INSERT INTO messages_fts(messages_fts) VALUES('rebuild')"); err != nil {
		return 0, fmt.Errorf("rebuild messages_fts in the DB copy: %w", err)
	}
	if _, err := d.ExecContext(ctx, "VACUUM"); err != nil {
		return 0, fmt.Errorf("vacuum the DB copy: %w", err)
	}
	return cells, nil
}

// maskColumn masks the known values in one column and returns the count of changed cells. It
// reads all matching rows and closes the rows before the first UPDATE, because the DB has one
// connection.
func maskColumn(ctx context.Context, tx *sql.Tx, r *security.Redactor, tbl, col string, args []any) (int, error) {
	conds := make([]string, len(args))
	for i := range args {
		conds[i] = fmt.Sprintf("instr(%q, ?) > 0", col)
	}
	q := fmt.Sprintf("SELECT rowid, %q FROM %q WHERE %q IS NOT NULL AND (%s)",
		col, tbl, col, strings.Join(conds, " OR "))
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	var todo []maskCell
	for rows.Next() {
		var c maskCell
		if err := rows.Scan(&c.rowid, &c.value); err != nil {
			rows.Close()
			return 0, err
		}
		if masked := r.RedactExact(c.value); masked != c.value {
			c.value = masked
			todo = append(todo, c)
		}
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	upd := fmt.Sprintf("UPDATE %q SET %q = ? WHERE rowid = ?", tbl, col)
	for _, c := range todo {
		if _, err := tx.ExecContext(ctx, upd, c.value, c.rowid); err != nil {
			return 0, err
		}
	}
	return len(todo), nil
}
