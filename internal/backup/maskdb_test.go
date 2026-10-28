package backup

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/security"
)

const maskSecret = "sk-proj-backuptest-0123456789"

// maskOpenDB opens a new state.db with the full schema in a temp folder.
func maskOpenDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// maskExec runs one statement and fails the test on an error.
func maskExec(t *testing.T, d *db.DB, query string, args ...any) {
	t.Helper()
	if _, err := d.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// maskInsertSession adds a session row with the given id and chat_id.
func maskInsertSession(t *testing.T, d *db.DB, id, chatID string) {
	t.Helper()
	maskExec(t, d, "INSERT INTO sessions(id, chat_id) VALUES(?, ?)", id, chatID)
}

// maskInsertMessage adds a user message. An empty toolCalls gives a NULL tool_calls cell.
func maskInsertMessage(t *testing.T, d *db.DB, sessionID, content, toolCalls string) {
	t.Helper()
	var tc any
	if toolCalls != "" {
		tc = toolCalls
	}
	maskExec(t, d, "INSERT INTO messages(session_id, role, content, tool_calls) VALUES(?, 'user', ?, ?)",
		sessionID, content, tc)
}

// maskTextColumns returns each TEXT column of each ordinary table in the live schema, by table.
func maskTextColumns(t *testing.T, d *db.DB) map[string][]string {
	t.Helper()
	rows, err := d.Query(`SELECT name FROM sqlite_master WHERE type='table'
		AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'messages_fts%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, tbl := range tables {
		rows, err := d.Query(fmt.Sprintf("PRAGMA table_info(%q)", tbl))
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var (
				cid, notNull, pk int
				name, typ        string
				dflt             sql.NullString
			)
			if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
				t.Fatal(err)
			}
			if strings.EqualFold(typ, "TEXT") {
				out[tbl] = append(out[tbl], name)
			}
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// maskAssertNoCell fails when a TEXT cell of any table still holds value.
func maskAssertNoCell(t *testing.T, d *db.DB, value string) {
	t.Helper()
	for tbl, cols := range maskTextColumns(t, d) {
		for _, col := range cols {
			var n int
			q := fmt.Sprintf("SELECT count(*) FROM %q WHERE instr(%q, ?) > 0", tbl, col)
			if err := d.QueryRow(q, value).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Errorf("%s.%s still holds the value in %d rows", tbl, col, n)
			}
		}
	}
}

func TestMaskDBFreeText(t *testing.T) {
	ctx := context.Background()
	d := maskOpenDB(t)
	maskInsertSession(t, d, "s1", "123456789")
	maskInsertMessage(t, d, "s1", "my key is "+maskSecret+" please test it", `[{"args":"`+maskSecret+`"}]`)
	maskExec(t, d, "INSERT INTO compactions(session_id, summary) VALUES('s1', ?)", "summary "+maskSecret)
	maskExec(t, d, "INSERT INTO cron_jobs(id, schedule, prompt) VALUES('j1', '@daily', ?)", "use "+maskSecret)
	n, err := maskDB(ctx, d, []string{maskSecret})
	if err != nil || n != 4 {
		t.Fatal(n, err)
	}
	maskAssertNoCell(t, d, maskSecret)
	var content string
	if err := d.QueryRow("SELECT content FROM messages").Scan(&content); err != nil {
		t.Fatal(err)
	}
	if content != "my key is "+security.Mask(maskSecret)+" please test it" {
		t.Fatalf("%q", content)
	}
	var hits int
	if err := d.QueryRow("SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'please'").Scan(&hits); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatal("FTS lost the message", hits)
	}
	if err := d.QueryRow(`SELECT count(*) FROM messages_fts WHERE messages_fts MATCH '"backuptest"'`).Scan(&hits); err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Fatal("FTS still finds the secret")
	}
}

func TestMaskDBKeepsStateColumns(t *testing.T) {
	d := maskOpenDB(t)
	maskInsertSession(t, d, "s1", "123456789")
	maskExec(t, d, "INSERT INTO pairing(platform, user_id, status) VALUES('telegram', '123456789', 'approved')")
	maskInsertMessage(t, d, "s1", "chat 123456789", "")
	n, err := maskDB(context.Background(), d, []string{"123456789"})
	if err != nil || n != 1 { // only messages.content
		t.Fatal(n, err)
	}
	var chat, user string
	if err := d.QueryRow("SELECT chat_id FROM sessions").Scan(&chat); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow("SELECT user_id FROM pairing").Scan(&user); err != nil {
		t.Fatal(err)
	}
	if chat != "123456789" || user != "123456789" {
		t.Fatal(chat, user)
	}
}

func TestMaskDBLeavesNoOldBytes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "copy.db")
	d, err := db.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	maskInsertSession(t, d, "s1", "")
	// The padding forces the message onto an overflow page.
	maskInsertMessage(t, d, "s1", strings.Repeat("padding ", 600)+maskSecret, "")
	if _, err := maskDB(context.Background(), d, []string{maskSecret}); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{p, p + "-wal"} {
		raw, err := os.ReadFile(f)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(maskSecret)) || bytes.Contains(raw, []byte("backuptest")) {
			t.Fatalf("old bytes stay in %s", filepath.Base(f))
		}
	}
}

func TestMaskDBShortValueIgnored(t *testing.T) {
	d := maskOpenDB(t)
	maskInsertSession(t, d, "s1", "")
	maskInsertMessage(t, d, "s1", "value 1234567", "")
	n, err := maskDB(context.Background(), d, []string{"1234567"})
	if err != nil || n != 0 {
		t.Fatal(n, err)
	}
}

func TestMaskDBNoMatch(t *testing.T) {
	d := maskOpenDB(t)
	maskInsertSession(t, d, "s1", "")
	maskInsertMessage(t, d, "s1", "nothing here", "")
	n, err := maskDB(context.Background(), d, []string{maskSecret})
	if err != nil || n != 0 {
		t.Fatal(n, err)
	}
}

func TestTextColumnsCovered(t *testing.T) {
	d := maskOpenDB(t)
	live := maskTextColumns(t, d)
	seen := map[string]bool{}
	for tbl, cols := range live {
		for _, col := range cols {
			key := tbl + "." + col
			seen[key] = true
			inFree := maskHasColumn(freeTextColumns, tbl, col)
			inState := maskHasColumn(stateColumns, tbl, col)
			if inFree == inState {
				t.Errorf("%s: put the column in exactly one of freeTextColumns and stateColumns", key)
			}
		}
	}
	// A listed column that is not a TEXT column in the schema is a stale entry.
	for _, m := range []map[string][]string{freeTextColumns, stateColumns} {
		for tbl, cols := range m {
			for _, col := range cols {
				if !seen[tbl+"."+col] {
					t.Errorf("%s.%s is not a TEXT column of the schema", tbl, col)
				}
			}
		}
	}
}

// maskHasColumn reports whether m lists col for tbl.
func maskHasColumn(m map[string][]string, tbl, col string) bool {
	for _, c := range m[tbl] {
		if c == col {
			return true
		}
	}
	return false
}
