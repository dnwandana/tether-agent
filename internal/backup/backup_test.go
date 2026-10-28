package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
)

const createSecret = "sk-proj-secret-0123456789"

// fixedNow returns 2026-10-28 17:42:10 in the local time of the host.
func fixedNow() time.Time {
	return time.Date(2026, 10, 28, 17, 42, 10, 0, time.Local)
}

// createOpts returns the Options for a backup of dir to the default path.
func createOpts(dir string) Options {
	return Options{DataDir: dir, Version: "v-test", Now: fixedNow}
}

// createArchive is the content of an archive that a test reads back.
type createArchive struct {
	names    []string
	contents map[string][]byte
	manifest Manifest
}

// createReadArchive reads a gzip tar stream and decodes its manifest.
func createReadArchive(t *testing.T, data []byte) createArchive {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	a := createArchive{contents: map[string][]byte{}}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		a.names = append(a.names, h.Name)
		a.contents[h.Name] = b
	}
	if err := json.Unmarshal(a.contents[ManifestName], &a.manifest); err != nil {
		t.Fatal(err)
	}
	return a
}

// createListTmp returns the names in dir that start with ".tmp-".
func createListTmp(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestCreateArchiveContents(t *testing.T) {
	dir, _ := newDataDir(t)
	write(t, dir, ".env", "OPENAI_API_KEY=x\n")
	write(t, dir, ".history", "history\n")
	write(t, dir, "config.yaml", "model: m\n")
	write(t, dir, "memory/memory.md", "memo\n")
	write(t, dir, "skills/web/SKILL.md", "# web\n")
	write(t, dir, "scripts/run.sh", "#!/bin/sh\n")
	write(t, dir, "logs/x", "log\n")

	res, err := Create(ctx, createOpts(dir))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "backups", "tether-backup-20261028-174210.tar.gz")
	if res.Path != want {
		t.Fatalf("Path = %s, want %s", res.Path, want)
	}
	fi, err := os.Stat(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("archive mode = %o", fi.Mode().Perm())
	}
	if res.Size != fi.Size() {
		t.Fatalf("Size = %d, the file has %d", res.Size, fi.Size())
	}
	bi, err := os.Stat(filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	if bi.Mode().Perm() != 0o700 {
		t.Fatalf("backups mode = %o", bi.Mode().Perm())
	}

	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	a := createReadArchive(t, data)
	wantNames := []string{"manifest.json", "state.db", "config.yaml", "memory/", "memory/memory.md",
		"scripts/", "scripts/run.sh", "skills/", "skills/web/", "skills/web/SKILL.md"}
	if !slices.Equal(a.names, wantNames) {
		t.Fatalf("names = %v, want %v", a.names, wantNames)
	}
	if len(a.manifest.Files) != 5 {
		t.Fatalf("manifest files = %v", a.manifest.Files)
	}
	for _, f := range a.manifest.Files {
		sum := sha256.Sum256(a.contents[f.Path])
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			t.Fatalf("the sha256 of %s does not match", f.Path)
		}
		if int64(len(a.contents[f.Path])) != f.Size {
			t.Fatalf("the size of %s does not match", f.Path)
		}
	}
	if a.manifest.Format != FormatVersion || a.manifest.TetherVersion != "v-test" {
		t.Fatalf("manifest = %+v", a.manifest)
	}
	if !a.manifest.CreatedAt.Equal(fixedNow()) || a.manifest.CreatedAt.Location() != time.UTC {
		t.Fatalf("CreatedAt = %v", a.manifest.CreatedAt)
	}
	if a.manifest.SchemaVersion != db.LatestVersion() || res.Stats.SchemaVersion != db.LatestVersion() {
		t.Fatalf("schema version = %d, stats %d", a.manifest.SchemaVersion, res.Stats.SchemaVersion)
	}
	if res.Stats.Messages != 1 || res.Stats.Sessions != 1 {
		t.Fatalf("stats = %+v", res.Stats)
	}
	if string(a.contents[ManifestName]) == "" || !bytes.Contains(a.contents[ManifestName], []byte(`"config_keys": []`)) {
		t.Fatalf("the manifest must have empty lists, not null:\n%s", a.contents[ManifestName])
	}
	if len(createListTmp(t, filepath.Join(dir, "backups"))) != 0 {
		t.Fatal("a .tmp- entry stays in backups/")
	}
}

func TestCreateNoSecrets(t *testing.T) {
	dir, live := newDataDir(t)
	write(t, dir, ".env", "OPENAI_API_KEY="+createSecret+"\n")
	write(t, dir, "memory/memory.md", "the key is "+createSecret+"\n")
	write(t, dir, "config.yaml", "providers:\n  openai:\n    api_key: "+createSecret+"\n")
	if _, err := live.Exec("INSERT INTO messages(session_id, role, content) VALUES('s1', 'user', ?)",
		"my key is "+createSecret); err != nil {
		t.Fatal(err)
	}
	opts := createOpts(dir)
	opts.DotEnv = map[string]string{"OPENAI_API_KEY": createSecret}

	res, err := Create(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	a := createReadArchive(t, data)
	for name, b := range a.contents {
		if bytes.Contains(b, []byte(createSecret)) {
			t.Fatalf("the entry %s holds the secret", name)
		}
	}
	if len(res.Replaced) != 1 || res.Replaced[0].Name != "OPENAI_API_KEY" {
		t.Fatalf("Replaced = %+v", res.Replaced)
	}
	if !bytes.Contains(a.contents["config.yaml"], []byte("${OPENAI_API_KEY}")) {
		t.Fatalf("config.yaml = %s", a.contents["config.yaml"])
	}
	r := a.manifest.Redacted
	if r.StateDB != 1 || r.Files != 1 {
		t.Fatalf("Redacted = %+v", r)
	}
	if !slices.Equal(r.ConfigKeys, []string{"providers.openai.api_key"}) {
		t.Fatalf("ConfigKeys = %v", r.ConfigKeys)
	}
	if res.Manifest.Redacted.StateDB != 1 || res.Manifest.Redacted.Files != 1 {
		t.Fatalf("Result.Manifest.Redacted = %+v", res.Manifest.Redacted)
	}
	// The live data does not change.
	if !strings.Contains(readFile(t, filepath.Join(dir, "memory/memory.md")), createSecret) {
		t.Fatal("the backup changed the live memory file")
	}
}

func TestCreateWhileWriterOpen(t *testing.T) {
	dir, _ := newDataDir(t)
	w, err := db.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	tx, err := w.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO kv(key, value) VALUES('during', 'x')"); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, createOpts(dir)); err != nil {
		t.Fatal("the backup failed while a writer was open:", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal("the writer failed after the backup:", err)
	}
}

func TestCreateHoldsSharedLock(t *testing.T) {
	dir, live := newDataDir(t)
	// Close the connection of the test, so only Create holds a shared lock.
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	var lockErr error
	called := false
	testHookAfterPrepare = func() {
		called = true
		release, err := db.LockExclusive(filepath.Join(dir, "state.db"))
		if err == nil {
			release()
		}
		lockErr = err
	}
	t.Cleanup(func() { testHookAfterPrepare = nil })
	if _, err := Create(ctx, createOpts(dir)); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("Create did not call testHookAfterPrepare")
	}
	if !errors.Is(lockErr, db.ErrInUse) {
		t.Fatalf("LockExclusive error = %v, want db.ErrInUse", lockErr)
	}
	// After Create returns, the shared lock is free.
	release, err := db.LockExclusive(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal("Create kept the shared lock:", err)
	}
	release()
}

func TestCreateFreshInstall(t *testing.T) {
	dir, _ := newDataDir(t)
	res, err := Create(ctx, createOpts(dir))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	a := createReadArchive(t, data)
	if !slices.Equal(a.names, []string{"manifest.json", "state.db"}) {
		t.Fatalf("names = %v", a.names)
	}
	if len(res.Skipped) != 0 || len(res.Replaced) != 0 || len(res.Warnings) != 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestCreateNoStateDB(t *testing.T) {
	dir := t.TempDir()
	_, err := Create(ctx, createOpts(dir))
	if err == nil || err.Error() != "there is no state.db in "+dir {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "backups")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Create made backups/ with no state.db:", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "state.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Create made state.db:", err)
	}
}

func TestCreateOutExists(t *testing.T) {
	dir, _ := newDataDir(t)
	out := filepath.Join(t.TempDir(), "a.tar.gz")
	write(t, filepath.Dir(out), "a.tar.gz", "keep")
	opts := createOpts(dir)
	opts.OutPath = out
	_, err := Create(ctx, opts)
	if err == nil || !strings.HasSuffix(err.Error(), "exists. Choose another path with --out") {
		t.Fatalf("err = %v", err)
	}
	if got := readFile(t, out); got != "keep" {
		t.Fatalf("the file changed: %q", got)
	}
	if tmp := createListTmp(t, filepath.Dir(out)); len(tmp) != 0 {
		t.Fatalf("temp files stay: %v", tmp)
	}
}

func TestCreateOutPath(t *testing.T) {
	dir, _ := newDataDir(t)
	out := filepath.Join(t.TempDir(), "my backup.tar.gz")
	opts := createOpts(dir)
	opts.OutPath = out
	res, err := Create(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != out {
		t.Fatalf("Path = %s", res.Path)
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 || fi.Size() != res.Size {
		t.Fatalf("mode %o, size %d, Size %d", fi.Mode().Perm(), fi.Size(), res.Size)
	}
	if tmp := createListTmp(t, filepath.Dir(out)); len(tmp) != 0 {
		t.Fatalf("temp files stay: %v", tmp)
	}
}

func TestCreateSameSecondTwice(t *testing.T) {
	dir, _ := newDataDir(t)
	first, err := Create(ctx, createOpts(dir))
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Create(ctx, createOpts(dir))
	if err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("err = %v", err)
	}
	after, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("the second run changed the first archive")
	}
	createReadArchive(t, after)
	if tmp := createListTmp(t, filepath.Join(dir, "backups")); len(tmp) != 0 {
		t.Fatalf("temp entries stay: %v", tmp)
	}
}

func TestCreateStdout(t *testing.T) {
	dir, _ := newDataDir(t)
	var buf bytes.Buffer
	opts := createOpts(dir)
	opts.OutPath = "-"
	opts.Stdout = &buf
	res, err := Create(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != "-" {
		t.Fatalf("Path = %s", res.Path)
	}
	b := buf.Bytes()
	if len(b) < 2 || b[0] != 0x1f || b[1] != 0x8b {
		t.Fatal("stdout does not start with the gzip magic bytes")
	}
	if res.Size != int64(len(b)) {
		t.Fatalf("Size = %d, wrote %d", res.Size, len(b))
	}
	createReadArchive(t, b)
	ents, err := os.ReadDir(filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		t.Fatalf("backups/ holds %v", ents)
	}
}

func TestCreateCleansStaleTmp(t *testing.T) {
	dir, _ := newDataDir(t)
	write(t, dir, "backups/.tmp-old/state.db", "old")
	if _, err := Create(ctx, createOpts(dir)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "backups", ".tmp-old")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(".tmp-old stays:", err)
	}
}

func TestCreateQuickCheckFails(t *testing.T) {
	dir, _ := newDataDir(t)
	checkCopy = func(ctx2 context.Context, d *db.DB) error { return errors.New("check state.db: bad page") }
	t.Cleanup(func() { checkCopy = checkDB })
	if _, err := Create(ctx, createOpts(dir)); err == nil || !strings.Contains(err.Error(), "bad page") {
		t.Fatalf("err = %v", err)
	}
	ents, err := os.ReadDir(filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		t.Fatalf("backups/ holds %v", ents)
	}
}

func TestCreateWhileBackupRuns(t *testing.T) {
	dir, _ := newDataDir(t)
	var secondErr error
	testHookAfterPrepare = func() {
		// Run the second backup once only, while the first one holds its temp files.
		testHookAfterPrepare = nil
		opts := createOpts(dir)
		opts.OutPath = filepath.Join(t.TempDir(), "second.tar.gz")
		_, secondErr = Create(ctx, opts)
	}
	t.Cleanup(func() { testHookAfterPrepare = nil })
	opts := createOpts(dir)
	opts.OutPath = filepath.Join(t.TempDir(), "first.tar.gz")
	res, err := Create(ctx, opts)
	if err != nil {
		t.Fatal("the second backup damaged the first one:", err)
	}
	if secondErr == nil || !strings.Contains(secondErr.Error(), "another backup is running") {
		t.Fatalf("second backup err = %v", secondErr)
	}
	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	createReadArchive(t, data)
	if tmp := createListTmp(t, filepath.Join(dir, "backups")); len(tmp) != 0 {
		t.Fatalf("temp entries stay: %v", tmp)
	}
}

func TestCreateReplacesKnownConfigValue(t *testing.T) {
	dir, _ := newDataDir(t)
	write(t, dir, "config.yaml", "mcp:\n  servers:\n    search:\n      url: https://x.test/?k="+createSecret+"\n")
	opts := createOpts(dir)
	opts.DotEnv = map[string]string{"SEARCH_KEY": createSecret}
	res, err := Create(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	want := []Replacement{{KeyPath: "mcp.servers.search.url", Name: "SEARCH_KEY"}}
	if !slices.Equal(res.Replaced, want) {
		t.Fatalf("Replaced = %v, want %v", res.Replaced, want)
	}
	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	a := createReadArchive(t, data)
	cfg := a.contents["config.yaml"]
	if bytes.Contains(cfg, []byte(createSecret)) {
		t.Fatal("config.yaml in the archive holds the secret")
	}
	// config.Load expands the reference, so the url works again after a restore.
	if !bytes.Contains(cfg, []byte("url: https://x.test/?k=${SEARCH_KEY}")) {
		t.Fatalf("config.yaml = %s", cfg)
	}
	if !slices.Equal(a.manifest.Redacted.ConfigKeys, []string{"mcp.servers.search.url"}) {
		t.Fatalf("ConfigKeys = %v", a.manifest.Redacted.ConfigKeys)
	}
}

func TestCreateStopsOnCancel(t *testing.T) {
	dir, _ := newDataDir(t)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// The signal comes after the prepare step, so the archive write must stop.
	testHookAfterPrepare = cancel
	t.Cleanup(func() { testHookAfterPrepare = nil })
	if _, err := Create(cctx, createOpts(dir)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	ents, err := os.ReadDir(filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		t.Fatalf("backups/ holds %v", ents)
	}
}
