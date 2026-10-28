package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
		"approvals_always", "pairing", "active_session", "delegation_batches", "kv", "schema_version"} {
		var n int
		if err := d.QueryRow("SELECT count(*) FROM sqlite_master WHERE name=?", tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatal("missing table", tbl)
		}
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
	if n != 1 || v != 1 {
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
