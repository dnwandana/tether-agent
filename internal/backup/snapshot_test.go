package backup

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/dnwandana/tether-agent/internal/db"
)

var ctx = context.Background()

// newDataDir opens <tmp>/state.db with one session row and one message row. The rows stay in
// the WAL, because live stays open until the test ends.
func newDataDir(t *testing.T) (dir string, live *db.DB) {
	t.Helper()
	dir = t.TempDir()
	live, err := db.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { live.Close() })
	if _, err := live.Exec("INSERT INTO sessions(id, title) VALUES('s1', 'snapshot')"); err != nil {
		t.Fatal(err)
	}
	if _, err := live.Exec("INSERT INTO messages(session_id, role, content) VALUES('s1', 'user', 'hello snapshot')"); err != nil {
		t.Fatal(err)
	}
	return dir, live
}

func TestSnapshotHasWALRows(t *testing.T) {
	dir, live := newDataDir(t)
	if fi, _ := os.Stat(filepath.Join(dir, "state.db-wal")); fi == nil || fi.Size() == 0 {
		t.Fatal("the row must still be in the WAL")
	}
	dst := filepath.Join(t.TempDir(), "copy.db")
	if err := snapshotDB(ctx, live, dst); err != nil {
		t.Fatal(err)
	}
	c, err := db.OpenExisting(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var n int
	if err := c.QueryRow("SELECT count(*) FROM messages WHERE content='hello snapshot'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal(n)
	}
}

func TestSnapshotDoesNotBlockWriter(t *testing.T) {
	dir, live := newDataDir(t)
	w, err := db.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	tx, err := w.BeginTx(ctx, nil) // holds the write lock (_txlock=immediate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO kv(key, value) VALUES('during', 'x')"); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "copy.db")
	if err := snapshotDB(ctx, live, dst); err != nil {
		t.Fatal("the snapshot waited for the writer:", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal("the writer failed after the snapshot:", err)
	}
	c, err := db.OpenExisting(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var n int
	if err := c.QueryRow("SELECT count(*) FROM kv WHERE key='during'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("an uncommitted row is in the copy")
	}
}

func TestSnapshotMode0600(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	_, live := newDataDir(t)
	dst := filepath.Join(t.TempDir(), "copy.db")
	if err := snapshotDB(ctx, live, dst); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatal(fi.Mode())
	}
}

func TestSnapshotPathWithQuoteAndSpace(t *testing.T) {
	_, live := newDataDir(t)
	dst := filepath.Join(t.TempDir(), "it's a dir", "copy.db")
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := snapshotDB(ctx, live, dst); err != nil {
		t.Fatal(err)
	}
	c, err := db.OpenExisting(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := checkDB(ctx, c); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotExistingDstFails(t *testing.T) {
	_, live := newDataDir(t)
	dst := writeTmp(t, "keep")
	if err := snapshotDB(ctx, live, dst); err == nil {
		t.Fatal("snapshotDB must not overwrite a file that exists")
	}
	if got := readFile(t, dst); got != "keep" {
		t.Fatalf("snapshotDB changed the file that exists: %q", got)
	}
}

func TestSnapshotFailRemovesDst(t *testing.T) {
	_, live := newDataDir(t)
	dst := filepath.Join(t.TempDir(), "copy.db")
	live.Close()
	if err := snapshotDB(ctx, live, dst); err == nil {
		t.Fatal("snapshotDB on a closed DB must fail")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("the failed snapshot left the file:", err)
	}
}

func TestCheckDBAndStats(t *testing.T) {
	_, live := newDataDir(t)
	if err := checkDB(ctx, live); err != nil {
		t.Fatal(err)
	}
	s, err := readStats(ctx, live)
	if err != nil || s.Messages != 1 || s.Sessions != 1 || s.CronJobs != 0 || s.SchemaVersion != db.LatestVersion() {
		t.Fatal(s, err)
	}
}
