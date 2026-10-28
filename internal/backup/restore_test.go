package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/gatewaylock"
)

// restoreNow returns 2026-10-29 09:15:00 in the local time of the host.
func restoreNow() time.Time {
	return time.Date(2026, 10, 29, 9, 15, 0, 0, time.Local)
}

// restorePreName is the pre-restore folder name for restoreNow.
const restorePreName = "pre-restore-20261029-091500"

// restoreArchiveOf runs Create on dir and returns the archive bytes.
func restoreArchiveOf(t *testing.T, dir string) []byte {
	t.Helper()
	var buf bytes.Buffer
	opts := createOpts(dir)
	opts.OutPath, opts.Stdout = "-", &buf
	if _, err := Create(ctx, opts); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// restoreSource makes a source data dir with one message, a skill, and memory.md, and returns
// the dir and its archive.
func restoreSource(t *testing.T) (string, []byte) {
	t.Helper()
	dir, _ := newDataDir(t)
	write(t, dir, "skills/new/SKILL.md", "new skill")
	write(t, dir, "memory/memory.md", "new memory")
	return dir, restoreArchiveOf(t, dir)
}

// restoreSnapshotTree maps each path under dir to the sha256 of its content, or to "dir" for a
// folder. It omits gateway.lock and state.db.lock, because Restore takes the locks.
func restoreSnapshotTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." || rel == "gateway.lock" || rel == "state.db.lock" {
			return nil
		}
		if d.IsDir() {
			out[rel] = "dir"
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// restoreAssertSame fails when the tree of dir is not equal to before.
func restoreAssertSame(t *testing.T, dir string, before map[string]string) {
	t.Helper()
	if after := restoreSnapshotTree(t, dir); !maps.Equal(before, after) {
		t.Fatalf("the data dir changed:\nbefore %v\nafter  %v", before, after)
	}
}

// restoreTarget makes a target data dir with a closed state.db, its -wal and -shm files, an
// old skill, and a script.
func restoreTarget(t *testing.T) string {
	t.Helper()
	dir, live := newDataDir(t)
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state.db-wal", "state.db-shm"} {
		if _, err := os.Stat(filepath.Join(dir, name)); errors.Is(err, fs.ErrNotExist) {
			write(t, dir, name, "old "+name)
		}
	}
	write(t, dir, "skills/old/SKILL.md", "old skill")
	write(t, dir, "scripts/x.sh", "echo old")
	return dir
}

// restoreOpts returns the RestoreOptions for the archive data into dir, with no prompt.
func restoreOpts(dir string, data []byte) RestoreOptions {
	return RestoreOptions{DataDir: dir, Archive: bytes.NewReader(data), Now: restoreNow}
}

// restoreGlob returns the names in dir that start with prefix.
func restoreGlob(t *testing.T, dir, prefix string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), prefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

// restoreCountMessages returns the number of rows in messages of the DB at path.
func restoreCountMessages(t *testing.T, path string) int {
	t.Helper()
	d, err := db.OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var n int
	if err := d.QueryRow("SELECT count(*) FROM messages").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRestoreNewHost(t *testing.T) {
	src, data := restoreSource(t)
	target := filepath.Join(t.TempDir(), "new", ".tether")
	res, err := Restore(ctx, restoreOpts(target, data))
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v, want 0700", fi.Mode().Perm())
	}
	if n := restoreCountMessages(t, filepath.Join(target, "state.db")); n != 1 {
		t.Fatalf("messages = %d, want 1", n)
	}
	for _, rel := range []string{"skills/new/SKILL.md", "memory/memory.md"} {
		got := readFile(t, filepath.Join(target, filepath.FromSlash(rel)))
		if want := readFile(t, filepath.Join(src, filepath.FromSlash(rel))); got != want {
			t.Fatalf("%s = %q, want %q", rel, got, want)
		}
	}
	if pre := restoreGlob(t, target, "pre-restore-"); len(pre) != 0 {
		t.Fatalf("pre-restore folders = %v", pre)
	}
	if tmp := restoreGlob(t, target, ".restore-tmp-"); len(tmp) != 0 {
		t.Fatalf("stage folders = %v", tmp)
	}
	if res.PreRestoreDir != "" {
		t.Fatalf("PreRestoreDir = %q, want empty", res.PreRestoreDir)
	}
	if res.Manifest.Format != FormatVersion {
		t.Fatalf("Manifest = %+v", res.Manifest)
	}
}

func TestRestoreMovesOldData(t *testing.T) {
	_, data := restoreSource(t)
	target := restoreTarget(t)
	old := restoreSnapshotTree(t, target)
	res, err := Restore(ctx, restoreOpts(target, data))
	if err != nil {
		t.Fatal(err)
	}
	pre := filepath.Join(target, restorePreName)
	if res.PreRestoreDir != pre {
		t.Fatalf("PreRestoreDir = %q, want %q", res.PreRestoreDir, pre)
	}
	for _, rel := range []string{"state.db", "state.db-wal", "state.db-shm", "skills/old/SKILL.md", "scripts/x.sh"} {
		b, err := os.ReadFile(filepath.Join(pre, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("%s is not in the pre-restore folder: %v", rel, err)
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != old[rel] {
			t.Fatalf("%s in the pre-restore folder has other content", rel)
		}
	}
	for _, rel := range []string{"scripts", "skills/old", "state.db-wal", "state.db-shm"} {
		if _, err := os.Lstat(filepath.Join(target, filepath.FromSlash(rel))); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s is in the target: %v", rel, err)
		}
	}
	if readFile(t, filepath.Join(target, "skills/new/SKILL.md")) != "new skill" {
		t.Fatal("the new skill is not in the target")
	}
	if _, err := os.Stat(filepath.Join(target, "state.db.lock")); err != nil {
		t.Fatal("state.db.lock must stay in the target:", err)
	}
	if _, err := os.Stat(filepath.Join(pre, "state.db.lock")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("state.db.lock moved to the pre-restore folder")
	}
	if tmp := restoreGlob(t, target, ".restore-tmp-"); len(tmp) != 0 {
		t.Fatalf("stage folders = %v", tmp)
	}
}

func TestRestoreRefusesGateway(t *testing.T) {
	_, data := restoreSource(t)
	target := restoreTarget(t)
	release, err := gatewaylock.Acquire(filepath.Join(target, "gateway.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	before := restoreSnapshotTree(t, target)
	_, err = Restore(ctx, restoreOpts(target, data))
	var ge *GatewayRunningError
	if !errors.As(err, &ge) {
		t.Fatalf("err = %v, want *GatewayRunningError", err)
	}
	if ge.PID != strconv.Itoa(os.Getpid()) {
		t.Fatalf("PID = %q, want %d", ge.PID, os.Getpid())
	}
	want := "a gateway is running (pid " + ge.PID + "). Stop it first: systemctl --user stop tether"
	if err.Error() != want {
		t.Fatalf("text = %q, want %q", err.Error(), want)
	}
	restoreAssertSame(t, target, before)
}

func TestRestoreRefusesOpenDB(t *testing.T) {
	_, data := restoreSource(t)
	target := restoreTarget(t)
	d, err := db.Open(filepath.Join(target, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	before := restoreSnapshotTree(t, target)
	_, err = Restore(ctx, restoreOpts(target, data))
	if !errors.Is(err, db.ErrInUse) {
		t.Fatalf("err = %v, want db.ErrInUse", err)
	}
	want := "state.db is open in another tether process, for example tether chat. Close it first"
	if err.Error() != want {
		t.Fatalf("text = %q, want %q", err.Error(), want)
	}
	restoreAssertSame(t, target, before)
}

func TestRestoreDuringBackup(t *testing.T) {
	dir, _ := newDataDir(t)
	var restoreErr error
	called := false
	testHookAfterPrepare = func() {
		called = true
		_, restoreErr = Restore(ctx, restoreOpts(dir, nil))
	}
	t.Cleanup(func() { testHookAfterPrepare = nil })
	if _, err := Create(ctx, createOpts(dir)); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("Create did not call testHookAfterPrepare")
	}
	if !errors.Is(restoreErr, db.ErrInUse) {
		t.Fatalf("Restore error = %v, want db.ErrInUse", restoreErr)
	}
}

func TestRestoreConfirmNo(t *testing.T) {
	_, data := restoreSource(t)
	target := restoreTarget(t)
	before := restoreSnapshotTree(t, target)
	var got Summary
	opts := restoreOpts(target, data)
	opts.Confirm = func(s Summary) bool {
		got = s
		return false
	}
	_, err := Restore(ctx, opts)
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("err = %v, want ErrStopped", err)
	}
	if got.Stats.Messages != 1 {
		t.Fatalf("Summary.Stats = %+v, want 1 message", got.Stats)
	}
	if got.PreRestoreDir != filepath.Join(target, restorePreName) {
		t.Fatalf("Summary.PreRestoreDir = %q", got.PreRestoreDir)
	}
	if got.DataDir != target || got.Manifest.Format != FormatVersion {
		t.Fatalf("Summary = %+v", got)
	}
	restoreAssertSame(t, target, before)
}

func TestRestoreStopsOnCancel(t *testing.T) {
	_, data := restoreSource(t)
	target := restoreTarget(t)
	before := restoreSnapshotTree(t, target)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Restore(cctx, restoreOpts(target, data)); !errors.Is(err, ErrStopped) {
		t.Fatalf("err = %v, want ErrStopped", err)
	}
	restoreAssertSame(t, target, before)
}

func TestRestoreCancelDuringConfirm(t *testing.T) {
	_, data := restoreSource(t)
	target := restoreTarget(t)
	before := restoreSnapshotTree(t, target)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	opts := restoreOpts(target, data)
	// The owner says yes, but a signal comes before the swap.
	opts.Confirm = func(Summary) bool {
		cancel()
		return true
	}
	if _, err := Restore(cctx, opts); !errors.Is(err, ErrStopped) {
		t.Fatalf("err = %v, want ErrStopped", err)
	}
	restoreAssertSame(t, target, before)
}

func TestRestorePreDirExists(t *testing.T) {
	_, data := restoreSource(t)
	target := restoreTarget(t)
	write(t, target, restorePreName+"/keep.txt", "keep")
	before := restoreSnapshotTree(t, target)
	if _, err := Restore(ctx, restoreOpts(target, data)); err == nil {
		t.Fatal("Restore must fail when the pre-restore folder exists")
	}
	restoreAssertSame(t, target, before)
}

// restoreSwapDirs makes a data dir and a stage dir for the swap tests.
func restoreSwapDirs(t *testing.T) (dataDir, stageDir, preDir string) {
	t.Helper()
	dataDir = t.TempDir()
	for _, rel := range []string{"state.db", "state.db-wal", "state.db-shm", "state.db.lock", "config.yaml", "memory/memory.md", "skills/a/SKILL.md", "scripts/x.sh"} {
		write(t, dataDir, rel, "old "+rel)
	}
	stageDir = filepath.Join(dataDir, ".restore-tmp-1")
	for _, rel := range []string{"state.db", "config.yaml", "memory/memory.md", "skills/b/SKILL.md"} {
		write(t, stageDir, rel, "new "+rel)
	}
	return dataDir, stageDir, filepath.Join(dataDir, restorePreName)
}

// restoreFailRename replaces rename with a function that fails on the calls in fail.
func restoreFailRename(t *testing.T, fail ...int) {
	t.Helper()
	n := 0
	rename = func(from, to string) error {
		n++
		for _, f := range fail {
			if n == f {
				return errors.New("rename failed for the test")
			}
		}
		return os.Rename(from, to)
	}
	t.Cleanup(func() { rename = os.Rename })
}

func TestSwapUndo(t *testing.T) {
	dataDir, stageDir, preDir := restoreSwapDirs(t)
	before := restoreSnapshotTree(t, dataDir)
	restoreFailRename(t, 4)
	moved, err := swap(dataDir, stageDir, preDir)
	if err == nil {
		t.Fatal("swap must fail")
	}
	if moved {
		t.Fatal("moved must be false after the undo")
	}
	if !strings.Contains(err.Error(), preDir) || !strings.Contains(err.Error(), stageDir) {
		t.Fatalf("the error does not name the folders: %v", err)
	}
	restoreAssertSame(t, dataDir, before)
}

func TestSwapUndoFails(t *testing.T) {
	dataDir, stageDir, preDir := restoreSwapDirs(t)
	// Call 4 fails, and the undo of the third rename fails too.
	restoreFailRename(t, 4, 5)
	_, err := swap(dataDir, stageDir, preDir)
	if err == nil || !errors.Is(err, errUndoFailed) {
		t.Fatalf("err = %v, want errUndoFailed", err)
	}
	if !strings.Contains(err.Error(), preDir) || !strings.Contains(err.Error(), stageDir) {
		t.Fatalf("the error does not name the folders: %v", err)
	}
	if _, err := os.Stat(preDir); err != nil {
		t.Fatal("the pre-restore folder must stay:", err)
	}
}

func TestSwapSuccess(t *testing.T) {
	dataDir, stageDir, preDir := restoreSwapDirs(t)
	moved, err := swap(dataDir, stageDir, preDir)
	if err != nil {
		t.Fatal(err)
	}
	if !moved {
		t.Fatal("moved = false, want true")
	}
	if got := readFile(t, filepath.Join(dataDir, "state.db")); got != "new state.db" {
		t.Fatalf("state.db = %q", got)
	}
	if got := readFile(t, filepath.Join(dataDir, "state.db.lock")); got != "old state.db.lock" {
		t.Fatalf("state.db.lock = %q", got)
	}
	for _, rel := range []string{"state.db", "state.db-wal", "state.db-shm", "config.yaml", "memory/memory.md", "skills/a/SKILL.md", "scripts/x.sh"} {
		if got := readFile(t, filepath.Join(preDir, filepath.FromSlash(rel))); got != "old "+rel {
			t.Fatalf("%s in the pre-restore folder = %q", rel, got)
		}
	}
	if fi, err := os.Stat(preDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("pre-restore folder: %v %v", fi, err)
	}
}

func TestSwapEmptyDataDir(t *testing.T) {
	dataDir := t.TempDir()
	stageDir := filepath.Join(dataDir, ".restore-tmp-1")
	write(t, stageDir, "state.db", "new")
	preDir := filepath.Join(dataDir, restorePreName)
	moved, err := swap(dataDir, stageDir, preDir)
	if err != nil {
		t.Fatal(err)
	}
	if moved {
		t.Fatal("moved = true, want false")
	}
	if _, err := os.Stat(preDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("the empty pre-restore folder must go:", err)
	}
}
