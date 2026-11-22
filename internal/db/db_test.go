package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestOpenReadOnly(t *testing.T) {
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "state.db")
	if d, err := OpenReadOnly(missing); err == nil {
		d.Close()
		t.Fatal("a missing file must give an error")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("OpenReadOnly created the file: %v", err)
	}

	path := filepath.Join(t.TempDir(), "state.db")
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.KVSet(ctx, "k", "v"); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if v, ok, err := r.KVGet(ctx, "k"); err != nil || !ok || v != "v" {
		t.Fatalf("KVGet = %q, %v, %v", v, ok, err)
	}
	if err := r.KVSet(ctx, "k", "x"); err == nil {
		t.Fatal("a write through the read-only handle must fail")
	}
}

func TestOpenCreatesSchemaAndWAL(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var mode string
	if err := d.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatal(mode)
	}
	for _, tbl := range []string{"sessions", "messages", "messages_fts", "compactions", "cron_jobs",
		"approval_grants", "pairing", "active_session", "delegation_batches", "kv", "schema_version", "goals",
		"acp_sessions", "chat_modes", "approval_log", "secret_pins", "held_sends",
		"dashboard_sessions", "dashboard_codes"} {
		var n int
		if err := d.QueryRow("SELECT count(*) FROM sqlite_master WHERE name=?", tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatal("missing table", tbl)
		}
	}
}

func TestTurnMarkersHaveInStep(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(`INSERT INTO turn_markers (session_key, session_id, updated_at) VALUES ('k', 's', 1)`); err != nil {
		t.Fatal(err)
	}
	var inStep int
	if err := d.QueryRow(`SELECT in_step FROM turn_markers WHERE session_key = 'k'`).Scan(&inStep); err != nil || inStep != 0 {
		t.Fatal(inStep, err)
	}
}

func TestPragmasOnEveryConnection(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var busy, fk int
	if err := d.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if busy != 5000 || fk != 1 {
		t.Fatal(busy, fk)
	}
}

func TestExtraMessageColumns(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, c := range [][2]string{{"messages", "cost_usd"}, {"messages", "latency_first_ms"}, {"compactions", "from_message_id"}} {
		var n int
		q := fmt.Sprintf("SELECT count(*) FROM pragma_table_info('%s') WHERE name=?", c[0])
		if err := d.QueryRow(q, c[1]).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatal("missing column", c)
		}
	}
}

func TestMigrationsIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.db")
	d1, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	d1.Close()
	d2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	var n, v int
	if err := d2.QueryRow("SELECT count(*), max(version) FROM schema_version").Scan(&n, &v); err != nil {
		t.Fatal(err)
	}
	if n != LatestVersion() || v != LatestVersion() {
		t.Fatal(n, v)
	}
}

func TestKV(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	if _, ok, err := d.KVGet(ctx, "telegram.offset"); ok || err != nil {
		t.Fatal("empty", err)
	}
	if err := d.KVSet(ctx, "telegram.offset", "42"); err != nil {
		t.Fatal(err)
	}
	if err := d.KVSet(ctx, "telegram.offset", "43"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := d.KVGet(ctx, "telegram.offset")
	if err != nil || !ok || v != "43" {
		t.Fatal(v, err)
	}
}

// The gateway and the chat process write to the same state.db at the same time.
func TestTwoConnectionsConcurrentWritesDoNotFailBusy(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.db")
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 400)
	for _, d := range []*DB{a, b} {
		wg.Add(1)
		go func(d *DB) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				errs <- d.KVSet(context.Background(), fmt.Sprint("k", i), "v")
			}
		}(d)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// A read-modify-write transaction in two processes must wait, not fail busy.
func TestConcurrentTransactionsDoNotFailBusy(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.db")
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := context.Background()
	if err := a.KVSet(ctx, "n", "0"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for _, d := range []*DB{a, b} {
		wg.Add(1)
		go func(d *DB) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				errs <- incr(ctx, d)
			}
		}(d)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	v, _, _ := a.KVGet(ctx, "n")
	if v != "200" {
		t.Fatal("lost update", v)
	}
}

func incr(ctx context.Context, d *DB) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT CAST(value AS INTEGER) FROM kv WHERE key='n'").Scan(&n); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE kv SET value=? WHERE key='n'", fmt.Sprint(n+1)); err != nil {
		return err
	}
	return tx.Commit()
}

func TestFTSTriggersInsertUpdateDelete(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	count := func(q string) int {
		var n int
		if err := d.QueryRow("SELECT count(*) FROM messages_fts WHERE messages_fts MATCH ?", q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	res, err := d.Exec("INSERT INTO messages(session_id, role, content, timestamp) VALUES ('s1', 'user', 'hello banana', 1)")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if count("banana") != 1 {
		t.Fatal("insert trigger")
	}
	if _, err := d.Exec("UPDATE messages SET content='hello cherry' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	if count("banana") != 0 || count("cherry") != 1 {
		t.Fatal("update trigger")
	}
	if _, err := d.Exec("DELETE FROM messages WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	if count("cherry") != 0 {
		t.Fatal("delete trigger")
	}
}

func TestMigrationTurnMarkers(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	rows, err := d.QueryContext(context.Background(), `PRAGMA table_info(turn_markers)`)
	if err != nil {
		t.Fatal(err)
	}
	var cols []string
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	want := []string{"session_key", "session_id", "updated_at", "resumes", "queued", "in_step"}
	if fmt.Sprint(cols) != fmt.Sprint(want) {
		t.Fatalf("columns = %v, want %v", cols, want)
	}
}

// A database at schema version 2 gains messages.reasoning and sessions.effort. An old row
// reads reasoning as NULL and effort as ”.
func TestMigrationReasoningEffortFrom002(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "state.db")
	sdb, err := sql.Open("sqlite", p+dsnParams)
	if err != nil {
		t.Fatal(err)
	}
	sdb.SetMaxOpenConns(1)
	old := &DB{DB: sdb}
	if _, err := old.ExecContext(ctx, `CREATE TABLE schema_version (version INTEGER PRIMARY KEY,
		name TEXT NOT NULL, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.version <= 2 {
			if err := old.apply(ctx, m); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := old.ExecContext(ctx, `INSERT INTO sessions (id) VALUES ('s1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.ExecContext(ctx, `INSERT INTO messages (session_id, role, content, timestamp)
		VALUES ('s1', 'assistant', 'old', 1)`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var reasoning sql.NullString
	if err := d.QueryRowContext(ctx, `SELECT reasoning FROM messages WHERE session_id = 's1'`).Scan(&reasoning); err != nil {
		t.Fatal(err)
	}
	if reasoning.Valid {
		t.Fatalf("reasoning = %q, want NULL", reasoning.String)
	}
	var effort string
	if err := d.QueryRowContext(ctx, `SELECT effort FROM sessions WHERE id = 's1'`).Scan(&effort); err != nil {
		t.Fatal(err)
	}
	if effort != "" {
		t.Fatalf("effort = %q, want ''", effort)
	}
	if v, err := d.SchemaVersion(ctx); err != nil || v != LatestVersion() {
		t.Fatal(v, err)
	}
}

// A database at schema version 6 gains the cron_runs table and cron_jobs.fail_streak. An old
// job reads fail_streak as 0.
func TestMigrationCronRunsFrom006(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "state.db")
	sdb, err := sql.Open("sqlite", p+dsnParams)
	if err != nil {
		t.Fatal(err)
	}
	sdb.SetMaxOpenConns(1)
	old := &DB{DB: sdb}
	if _, err := old.ExecContext(ctx, `CREATE TABLE schema_version (version INTEGER PRIMARY KEY,
		name TEXT NOT NULL, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.version <= 6 {
			if err := old.apply(ctx, m); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := old.ExecContext(ctx, `INSERT INTO cron_jobs (id, schedule) VALUES ('j1', 'every 1h')`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var streak int
	if err := d.QueryRowContext(ctx, `SELECT fail_streak FROM cron_jobs WHERE id = 'j1'`).Scan(&streak); err != nil || streak != 0 {
		t.Fatal("an old job must read fail_streak as 0:", streak, err)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO cron_runs (job_id, started_at, finished_at, status, delivery)
		VALUES ('j1', 1763276400000, 1763276438000, 'ok', 'sent')`); err != nil {
		t.Fatal(err)
	}
	var output, deliveryErr, sessionID string
	if err := d.QueryRowContext(ctx, `SELECT output, delivery_error, session_id FROM cron_runs WHERE job_id = 'j1'`).
		Scan(&output, &deliveryErr, &sessionID); err != nil || output != "" || deliveryErr != "" || sessionID != "" {
		t.Fatal("the text columns must default to '':", output, deliveryErr, sessionID, err)
	}
	var n int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'cron_runs_job'`).Scan(&n); err != nil || n != 1 {
		t.Fatal("missing index cron_runs_job:", n, err)
	}
	if v, err := d.SchemaVersion(ctx); err != nil || v != LatestVersion() {
		t.Fatal(v, LatestVersion(), err)
	}
}

// A database at schema version 7 gains the chat_modes and approval_log tables. The text
// columns of a new row default to ”.
func TestMigrationChatModesLogFrom007(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "state.db")
	sdb, err := sql.Open("sqlite", p+dsnParams)
	if err != nil {
		t.Fatal(err)
	}
	sdb.SetMaxOpenConns(1)
	old := &DB{DB: sdb}
	if _, err := old.ExecContext(ctx, `CREATE TABLE schema_version (version INTEGER PRIMARY KEY,
		name TEXT NOT NULL, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.version <= 7 {
			if err := old.apply(ctx, m); err != nil {
				t.Fatal(err)
			}
		}
	}
	old.Close()

	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// chat_modes: the scope is the key, and set_at defaults to 0.
	if _, err := d.ExecContext(ctx, `INSERT INTO chat_modes (scope, mode) VALUES ('telegram:-1001:5', 'read-only')`); err != nil {
		t.Fatal(err)
	}
	var setAt int64
	if err := d.QueryRowContext(ctx, `SELECT set_at FROM chat_modes WHERE scope = 'telegram:-1001:5'`).Scan(&setAt); err != nil || setAt != 0 {
		t.Fatal("set_at must default to 0:", setAt, err)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO chat_modes (scope, mode) VALUES ('telegram:-1001:5', 'full')`); err == nil {
		t.Fatal("a second row for one scope must fail")
	}

	// approval_log: only at, decision, and source are required.
	if _, err := d.ExecContext(ctx, `INSERT INTO approval_log (at, decision, source) VALUES (1763276400000, 'allowed', 'owner')`); err != nil {
		t.Fatal(err)
	}
	var sessionID, scope, kind, tool, rule, class, answer, sum, preview string
	if err := d.QueryRowContext(ctx, `SELECT session_id, scope, kind, tool, rule, class, answer, content_sha256, preview
		FROM approval_log`).Scan(&sessionID, &scope, &kind, &tool, &rule, &class, &answer, &sum, &preview); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{sessionID, scope, kind, tool, rule, class, answer, sum, preview} {
		if s != "" {
			t.Fatalf("a text column of approval_log must default to '': %q", s)
		}
	}
	for _, idx := range []string{"approval_log_scope_at", "approval_log_at"} {
		var n int
		if err := d.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&n); err != nil || n != 1 {
			t.Fatal("missing index", idx, n, err)
		}
	}
	if v, err := d.SchemaVersion(ctx); err != nil || v != LatestVersion() || v < 8 {
		t.Fatal(v, LatestVersion(), err)
	}
}

func TestACPSessionsKeyIsSessionAndAgent(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	const ins = `INSERT INTO acp_sessions (session_id, agent, acp_session_id, cwd, created_at, updated_at)
		VALUES (?, ?, ?, '/home/user/app', 1762560000000, 1762560000000)`
	if _, err := d.Exec(ins, "s1", "claude-code", "acp-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(ins, "s1", "codex", "acp-2"); err != nil {
		t.Fatal("another agent of the same session must get its own row:", err)
	}
	if _, err := d.Exec(ins, "s1", "claude-code", "acp-3"); err == nil {
		t.Fatal("a second row for (s1, claude-code) must fail")
	}
}

func TestBatchOwnerColumn(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(`INSERT INTO delegation_batches (id) VALUES ('b1')`); err != nil {
		t.Fatal(err)
	}
	var owner int
	if err := d.QueryRow(`SELECT owner FROM delegation_batches WHERE id = 'b1'`).Scan(&owner); err != nil || owner != 1 {
		t.Fatal("a row with no owner value is an owner row:", owner, err)
	}
}

// A database at schema version 8 loses approvals_always. kv keeps the count of the rows that
// /approvals showed, so the owner gets one notice.
func TestMigrationGrantsFrom008(t *testing.T) {
	for _, tc := range []struct {
		ids    []string
		want   string
		wantOK bool
	}{
		{[]string{"recursive_delete", "git_force_push", "protected_path_write_x", "sqlite_client_state_db"}, "2", true},
		{[]string{"protected_path_write_x"}, "", false},
		{nil, "", false},
	} {
		ctx := context.Background()
		p := filepath.Join(t.TempDir(), "state.db")
		sdb, err := sql.Open("sqlite", p+dsnParams)
		if err != nil {
			t.Fatal(err)
		}
		sdb.SetMaxOpenConns(1)
		old := &DB{DB: sdb}
		if _, err := old.ExecContext(ctx, `CREATE TABLE schema_version (version INTEGER PRIMARY KEY,
			name TEXT NOT NULL, applied_at INTEGER NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		ms, err := loadMigrations()
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ms {
			if m.version <= 8 {
				if err := old.apply(ctx, m); err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, id := range tc.ids {
			if _, err := old.ExecContext(ctx, `INSERT INTO approvals_always(pattern_id, created_at) VALUES (?, 1)`, id); err != nil {
				t.Fatal(err)
			}
		}
		old.Close()

		d, err := Open(p)
		if err != nil {
			t.Fatal(err)
		}
		if v, ok, err := d.KVGet(ctx, "approvals_always_removed"); err != nil || ok != tc.wantOK || v != tc.want {
			t.Fatal(tc.ids, v, ok, err)
		}
		// An old binary fails closed: its query of approvals_always gives an error.
		var n int
		if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM approvals_always`).Scan(&n); err == nil {
			t.Fatal("approvals_always still exists")
		}
		var cols []string
		rows, err := d.QueryContext(ctx, `SELECT name FROM pragma_table_info('approval_grants') ORDER BY cid`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				t.Fatal(err)
			}
			cols = append(cols, c)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if got := strings.Join(cols, ","); got != "id,pattern_id,content_sha256,workdir,via,programs,preview,server,tool_name,created_at" {
			t.Fatal(got)
		}
		if v, err := d.SchemaVersion(ctx); err != nil || v != LatestVersion() {
			t.Fatal(v, LatestVersion(), err)
		}
		d.Close()
	}
}

func TestKVDelete(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	if err := d.KVSet(ctx, "secret_check:PYPI_TOKEN", "1763463060000"); err != nil {
		t.Fatal(err)
	}
	if err := d.KVDelete(ctx, "secret_check:PYPI_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := d.KVGet(ctx, "secret_check:PYPI_TOKEN"); ok || err != nil {
		t.Fatal("the key must be gone", err)
	}
	// A delete of a missing key is not an error.
	if err := d.KVDelete(ctx, "secret_check:NONE"); err != nil {
		t.Fatal(err)
	}
}

// A database at schema version 11 gains cron_jobs.draft_only. An old job reads it as 0.
func TestMigrationDraftOnlyFrom011(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "state.db")
	sdb, err := sql.Open("sqlite", p+dsnParams)
	if err != nil {
		t.Fatal(err)
	}
	sdb.SetMaxOpenConns(1)
	old := &DB{DB: sdb}
	if _, err := old.ExecContext(ctx, `CREATE TABLE schema_version (version INTEGER PRIMARY KEY,
		name TEXT NOT NULL, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.version <= 11 {
			if err := old.apply(ctx, m); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := old.ExecContext(ctx, `INSERT INTO cron_jobs (id, schedule) VALUES ('j1', 'every 1h')`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var v int
	if err := d.QueryRowContext(ctx, `SELECT draft_only FROM cron_jobs WHERE id = 'j1'`).Scan(&v); err != nil || v != 0 {
		t.Fatal("an old job must read draft_only as 0:", v, err)
	}
	if v, err := d.SchemaVersion(ctx); err != nil || v != LatestVersion() {
		t.Fatal(v, LatestVersion(), err)
	}
}

// A database at schema version 12 gains the attention columns of migration 013. A job that the
// fail streak paused gets pause_reason 'failures'. An owner pause, an enabled job, and a job
// with no failures keep an empty reason.
func TestMigrationCronAttentionFrom012(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "state.db")
	sdb, err := sql.Open("sqlite", p+dsnParams)
	if err != nil {
		t.Fatal(err)
	}
	sdb.SetMaxOpenConns(1)
	old := &DB{DB: sdb}
	if _, err := old.ExecContext(ctx, `CREATE TABLE schema_version (version INTEGER PRIMARY KEY,
		name TEXT NOT NULL, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.version <= 12 {
			if err := old.apply(ctx, m); err != nil {
				t.Fatal(err)
			}
		}
	}
	paused := "⚠ Cron job a failed: boom\n\n⏸ The job a (j1) is paused: it failed 10 runs in a row. " +
		"Fix the cause. Then ask me to resume the job."
	badSched := `⚠ Cron job e is paused: its stored schedule "x" is not valid. Update the schedule, then resume the job.`
	for _, row := range []struct {
		id      string
		enabled int
		streak  int
		output  string
	}{
		{"j1", 0, 10, paused},      // paused by the streak
		{"j2", 0, 3, "other text"}, // an owner pause after 3 failures
		{"j3", 1, 10, paused},      // resumed, then failed again
		{"j4", 0, 0, paused},       // an edit cleared the count
		{"j5", 0, 0, badSched},     // paused by a bad schedule
		{"j6", 1, 0, badSched},     // resumed after a schedule fix
	} {
		if _, err := old.ExecContext(ctx, `INSERT INTO cron_jobs (id, schedule, enabled, fail_streak, last_output)
			VALUES (?, 'every 1h', ?, ?, ?)`, row.id, row.enabled, row.streak, row.output); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := old.ExecContext(ctx, `INSERT INTO cron_runs (job_id, started_at, finished_at, status, delivery)
		VALUES ('j1', 1, 2, 'error', 'sent')`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for id, want := range map[string]string{"j1": "failures", "j2": "", "j3": "", "j4": "", "j5": "schedule", "j6": ""} {
		var reason string
		if err := d.QueryRowContext(ctx, `SELECT pause_reason FROM cron_jobs WHERE id = ?`, id).Scan(&reason); err != nil || reason != want {
			t.Errorf("%s: pause_reason %q, want %q (%v)", id, reason, want, err)
		}
	}
	var (
		lastErr, errKey, effort string
		watch, continuity       int
		hash, output            sql.NullString
	)
	if err := d.QueryRowContext(ctx, `SELECT last_error, last_error_key, watch, watch_hash, watch_output,
		continuity, effort FROM cron_jobs WHERE id = 'j2'`).Scan(&lastErr, &errKey, &watch, &hash, &output,
		&continuity, &effort); err != nil {
		t.Fatal(err)
	}
	if lastErr != "" || errKey != "" || watch != 0 || hash.Valid || output.Valid || continuity != 0 || effort != "" {
		t.Fatal(lastErr, errKey, watch, hash, output, continuity, effort)
	}
	var (
		cost, median sql.NullFloat64
		flag         int
	)
	if err := d.QueryRowContext(ctx, `SELECT cost_usd, cost_median, cost_flag FROM cron_runs WHERE job_id = 'j1'`).
		Scan(&cost, &median, &flag); err != nil || cost.Valid || median.Valid || flag != 0 {
		t.Fatal("an old run has no cost:", cost, median, flag, err)
	}
	if v, err := d.SchemaVersion(ctx); err != nil || v < 13 || v != LatestVersion() {
		t.Fatal(v, LatestVersion(), err)
	}
}
