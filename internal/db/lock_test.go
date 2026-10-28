package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenTwiceSharesLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.db")
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(p)
	if err != nil {
		t.Fatal("second open:", err)
	}
	defer b.Close()
	fi, err := os.Stat(p + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatal(fi.Mode())
	}
}

func TestLockExclusiveFailsWhileOpen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.db")
	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LockExclusive(p)
	if !errors.Is(err, ErrInUse) {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	rel, err := LockExclusive(p) // Close released the shared lock.
	if err != nil {
		t.Fatal(err)
	}
	if err := rel(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFailsDuringRestore(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.db")
	rel, err := LockExclusive(p) // creates state.db.lock when it is missing
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	_, err = Open(p)
	if !errors.Is(err, ErrRestoreRunning) {
		t.Fatal(err)
	}
	want := "db: a restore is running in " + dir + ". Wait for it to end, then try again"
	if err.Error() != want {
		t.Fatalf("%q", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("Open must not create state.db during a restore")
	}
}

func TestLockPath(t *testing.T) {
	if got := LockPath("/x/state.db"); got != "/x/state.db.lock" {
		t.Fatal(got)
	}
}

func TestOpenExistingSkipsMigrations(t *testing.T) {
	p := filepath.Join(t.TempDir(), "plain.db")
	raw, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("CREATE TABLE t(x)"); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	d, err := OpenExisting(p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var n int
	if err := d.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='schema_version'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("OpenExisting ran the migrations")
	}
}

func TestOpenExistingMissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.db")
	if _, err := OpenExisting(p); err == nil {
		t.Fatal("want an error")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("OpenExisting created the file")
	}
}

func TestOpenExistingTakesSharedLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.db")
	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	e, err := OpenExisting(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockExclusive(p); !errors.Is(err, ErrInUse) {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	rel, err := LockExclusive(p) // Close released the shared lock.
	if err != nil {
		t.Fatal(err)
	}
	if err := rel(); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaVersionMatchesLatest(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	v, err := d.SchemaVersion(context.Background())
	if err != nil || v != LatestVersion() || v < 1 {
		t.Fatal(v, LatestVersion(), err)
	}
}

func TestOpenReadOnlyTakesSharedLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.db")
	w, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReadOnly(p)
	if err != nil {
		t.Fatal(err)
	}
	// A restore must see the reader.
	if _, err := LockExclusive(p); !errors.Is(err, ErrInUse) {
		t.Fatalf("LockExclusive err = %v, want ErrInUse", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	release, err := LockExclusive(p)
	if err != nil {
		t.Fatal("Close kept the shared lock:", err)
	}
	// While a restore runs, the reader gets a clear error.
	if _, err := OpenReadOnly(p); !errors.Is(err, ErrRestoreRunning) {
		t.Fatalf("OpenReadOnly err = %v, want ErrRestoreRunning", err)
	}
	release()
}

func TestOpenReadOnlyMissingFileNoLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.db")
	if d, err := OpenReadOnly(p); err == nil {
		d.Close()
		t.Fatal("a missing file must give an error")
	}
	if _, err := os.Stat(LockPath(p)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenReadOnly made the lock file: %v", err)
	}
}
