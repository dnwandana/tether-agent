// Package db opens state.db and applies the schema migrations.
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

//go:embed migrations/*.sql
var migrations embed.FS

// DB is the state.db handle that the gateway and chat processes share.
//
// The pool has one connection, so the writes of one process never compete with each other.
// Do not run a second query while the rows of an earlier query are open, because the second
// query then waits for the connection forever.
//
// Each open DB holds a shared lock on state.db.lock until Close. tether restore takes the
// exclusive lock, so it can find every tether process that has the DB open.
type DB struct {
	*sql.DB
	lock *os.File
}

// Close closes the database, then releases the shared lock on state.db.lock.
func (d *DB) Close() error {
	err := d.DB.Close()
	if d.lock != nil {
		err = errors.Join(err, unlock(d.lock))
		d.lock = nil
	}
	return err
}

// dsnParams sets the pragmas on every new connection. busy_timeout makes a writer wait for the
// lock of another process instead of a fail with SQLITE_BUSY. _txlock=immediate starts each
// transaction with the write lock, so a read-modify-write transaction cannot fail when another
// process commits between its read and its write.
const dsnParams = "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate"

// Open opens or creates the database at path and applies the embedded migrations in name order.
// It takes the shared lock first, so it does not create the database while a restore runs.
func Open(path string) (*DB, error) {
	if strings.ContainsRune(path, '?') {
		return nil, fmt.Errorf("db: open %s: the path must not contain '?'", path)
	}
	lock, err := lockShared(path)
	if err != nil {
		if errors.Is(err, ErrRestoreRunning) {
			return nil, fmt.Errorf("db: %w in %s. Wait for it to end, then try again", ErrRestoreRunning, filepath.Dir(path))
		}
		return nil, err
	}
	sdb, err := sql.Open("sqlite", path+dsnParams)
	if err != nil {
		unlock(lock)
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	sdb.SetMaxOpenConns(1)
	d := &DB{DB: sdb, lock: lock}
	if err := d.migrate(context.Background()); err != nil {
		d.Close()
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	return d, nil
}

// OpenExisting opens the database at path with the shared lock and runs no migrations. backup
// and restore use it, because they must not change the schema of the file that they read.
// A missing file is an error.
func OpenExisting(path string) (*DB, error) {
	if strings.ContainsRune(path, '?') {
		return nil, fmt.Errorf("db: open %s: the path must not contain '?'", path)
	}
	// Check the file before the lock and before sql.Open, because the driver creates a
	// missing file.
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	lock, err := lockShared(path)
	if err != nil {
		if errors.Is(err, ErrRestoreRunning) {
			return nil, fmt.Errorf("db: %w in %s. Wait for it to end, then try again", ErrRestoreRunning, filepath.Dir(path))
		}
		return nil, err
	}
	sdb, err := sql.Open("sqlite", path+dsnParams)
	if err != nil {
		unlock(lock)
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	sdb.SetMaxOpenConns(1)
	return &DB{DB: sdb, lock: lock}, nil
}

// LatestVersion returns the highest version of the embedded migrations. This is the schema
// version that this binary writes.
func LatestVersion() int {
	ms, err := loadMigrations()
	if err != nil {
		// The migration files are embedded in the binary and the tests apply them, so this
		// cannot occur.
		return 0
	}
	v := 0
	for _, m := range ms {
		v = max(v, m.version)
	}
	return v
}

// SchemaVersion returns the highest version in schema_version, or 0 when the table is empty.
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	if err := d.QueryRowContext(ctx, "SELECT coalesce(max(version), 0) FROM schema_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("db: read schema_version: %w", err)
	}
	return v, nil
}

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations returns the embedded migrations in name order. The version is the number
// before the first "_" in the file name.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var out []migration
	for _, e := range entries {
		name := e.Name()
		num, _, ok := strings.Cut(name, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || v <= 0 {
			return nil, fmt.Errorf("migration %s: the name must start with a positive number and '_'", name)
		}
		b, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: name, sql: string(b)})
	}
	return out, nil
}

// migrate applies each migration that schema_version does not record. Each migration runs in
// its own transaction. The version check is inside the transaction, so two processes that
// open the same new file at the same time apply each migration only once.
func (d *DB) migrate(ctx context.Context) error {
	if _, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	for _, m := range ms {
		if err := d.apply(ctx, m); err != nil {
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
	}
	return nil
}

func (d *DB) apply(ctx context.Context, m migration) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var done int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM schema_version WHERE version=?", m.version).Scan(&done); err != nil {
		return err
	}
	if done > 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_version(version, name, applied_at) VALUES (?, ?, ?)",
		m.version, m.name, time.Now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}
