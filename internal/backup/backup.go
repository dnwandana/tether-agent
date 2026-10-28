package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/security"
)

// Options controls one backup.
type Options struct {
	// DataDir is the data dir to back up.
	DataDir string
	// OutPath is "" for the default path in <DataDir>/backups, "-" for Stdout, or a path.
	OutPath string
	// Stdout receives the archive when OutPath is "-".
	Stdout io.Writer
	// DotEnv holds the values of .env. Each value is a known secret.
	DotEnv map[string]string
	// Environ holds the environment as NAME=value entries, like os.Environ.
	Environ []string
	// Version goes into the manifest as tether_version.
	Version string
	// Now gives the time for the default file name and the manifest. nil means time.Now.
	Now func() time.Time
}

// Result describes a backup that is complete. It holds key paths, names, and counts, never a
// secret value.
type Result struct {
	// Path is the absolute path of the archive, or "-" for Stdout.
	Path string
	// Size is the size of the archive in bytes.
	Size     int64
	Manifest Manifest
	Stats    DBStats
	// Skipped holds the slash paths of the symlinks and special files that the archive omits.
	Skipped []string
	// Replaced lists the literal secrets of config.yaml that the archive holds as ${NAME}.
	Replaced []Replacement
	// Warnings holds the key paths of config.yaml values that look like a secret.
	Warnings []string
}

// testHookAfterPrepare runs after the prepare step and before the archive write. Tests set it.
var testHookAfterPrepare func()

// checkCopy runs the integrity check on the DB copy. Tests replace it.
var checkCopy = checkDB

// Create writes the allow-list state of opts.DataDir to one gzip tar archive with no known
// secret in it.
//
// The live state.db stays open until Create returns. Its shared lock stops a restore between
// the DB snapshot and the reads of the other files. The live connection runs only VACUUM INTO.
func Create(ctx context.Context, opts Options) (*Result, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	// Read the time once, so the file name and the manifest agree.
	now := opts.Now()
	opts.Now = func() time.Time { return now }
	if opts.OutPath == "-" && opts.Stdout == nil {
		return nil, errors.New("there is no writer for the archive on stdout")
	}
	dataDir, err := filepath.Abs(opts.DataDir)
	if err != nil {
		return nil, fmt.Errorf("find the data dir %s: %w", opts.DataDir, err)
	}
	opts.DataDir = dataDir

	livePath := filepath.Join(dataDir, "state.db")
	if _, err := os.Stat(livePath); errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("there is no state.db in %s", dataDir)
	} else if err != nil {
		return nil, err
	}
	live, err := db.OpenExisting(livePath)
	if err != nil {
		return nil, err
	}
	defer live.Close()

	backups := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(backups, 0o700); err != nil {
		return nil, fmt.Errorf("create the folder %s: %w", backups, err)
	}
	// Only one backup at a time may use backups/, because the next step removes the temp
	// entries of a backup that is not complete.
	release, err := lockBackups(backups)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := removeStaleTmp(backups); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(backups, ".tmp-")
	if err != nil {
		return nil, fmt.Errorf("create a temp folder in %s: %w", backups, err)
	}
	defer os.RemoveAll(tmp)

	m, items, res, err := prepare(ctx, opts, live, tmp)
	if err != nil {
		return nil, err
	}
	if testHookAfterPrepare != nil {
		testHookAfterPrepare()
	}

	res.Manifest = *m
	if opts.OutPath == "-" {
		cw := &countWriter{ctx: ctx, w: opts.Stdout}
		if err := writeArchive(cw, *m, items); err != nil {
			return nil, err
		}
		res.Path, res.Size = "-", cw.n
		return res, nil
	}
	out := opts.OutPath
	if out == "" {
		out = filepath.Join(backups, "tether-backup-"+now.Local().Format("20060102-150405")+".tar.gz")
	}
	if out, err = filepath.Abs(out); err != nil {
		return nil, fmt.Errorf("find the output path %s: %w", opts.OutPath, err)
	}
	size, err := writeArchiveFile(ctx, out, *m, items)
	if err != nil {
		return nil, err
	}
	res.Path, res.Size = out, size
	return res, nil
}

// prepare makes the archive copies of state.db, config.yaml, and the text files in tmp, and
// returns the manifest and the items in archive order. The returned Result holds Stats,
// Skipped, Replaced, and Warnings.
func prepare(ctx context.Context, opts Options, live *db.DB, tmp string) (*Manifest, []item, *Result, error) {
	values := security.KnownSecretValues(opts.DotEnv, opts.Environ)
	res := &Result{Skipped: []string{}, Replaced: []Replacement{}, Warnings: []string{}}
	red := Redacted{ConfigKeys: []string{}, ConfigWarnings: []string{}}

	livePath := filepath.Join(opts.DataDir, "state.db")
	liveInfo, err := os.Stat(livePath)
	if err != nil {
		return nil, nil, nil, err
	}
	dbCopy := filepath.Join(tmp, "state.db")
	if err := snapshotDB(ctx, live, dbCopy); err != nil {
		return nil, nil, nil, err
	}
	cells, stats, err := prepareDBCopy(ctx, dbCopy, values)
	if err != nil {
		return nil, nil, nil, err
	}
	red.StateDB = cells
	res.Stats = stats
	items := []item{{Rel: "state.db", Src: dbCopy, Mode: liveInfo.Mode()}}

	cfgItem, ok, err := prepareConfig(opts, tmp, res)
	if err != nil {
		return nil, nil, nil, err
	}
	if ok {
		items = append(items, cfgItem)
		for _, r := range res.Replaced {
			red.ConfigKeys = append(red.ConfigKeys, r.KeyPath)
		}
		red.ConfigWarnings = append(red.ConfigWarnings, res.Warnings...)
	}

	found, skipped, err := collectItems(opts.DataDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("list the data files: %w", err)
	}
	res.Skipped = append(res.Skipped, skipped...)
	r := security.NewRedactor(values)
	for i := range found {
		it := &found[i]
		if it.Dir {
			continue
		}
		dst := filepath.Join(tmp, "files", filepath.FromSlash(it.Rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return nil, nil, nil, fmt.Errorf("create a temp folder for %s: %w", it.Rel, err)
		}
		changed, err := maskTextFile(it.Src, dst, r)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("copy %s: %w", it.Rel, err)
		}
		if changed {
			it.Src = dst
			red.Files++
		}
	}
	items = append(items, found...)

	files, err := fileEntries(items)
	if err != nil {
		return nil, nil, nil, err
	}
	if files == nil {
		files = []FileEntry{}
	}
	m := &Manifest{
		Format:        FormatVersion,
		TetherVersion: opts.Version,
		CreatedAt:     opts.Now().UTC(),
		SchemaVersion: stats.SchemaVersion,
		Files:         files,
		Redacted:      red,
	}
	return m, items, res, nil
}

// prepareDBCopy masks the known values in the DB copy at path, checks it, and reads its stats.
// It closes the copy before it returns, so the tar step can read the file.
func prepareDBCopy(ctx context.Context, path string, values []string) (cells int, stats DBStats, err error) {
	c, err := db.OpenExisting(path)
	if err != nil {
		return 0, DBStats{}, err
	}
	defer func() {
		if cerr := c.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close the copy of state.db: %w", cerr)
		}
	}()
	if cells, err = maskDB(ctx, c, values); err != nil {
		return 0, DBStats{}, err
	}
	if err = checkCopy(ctx, c); err != nil {
		return 0, DBStats{}, err
	}
	if stats, err = readStats(ctx, c); err != nil {
		return 0, DBStats{}, err
	}
	return cells, stats, nil
}

// prepareConfig writes the archive copy of config.yaml to tmp and records the replacements and
// the warnings in res. ok is false when the data dir has no config.yaml.
func prepareConfig(opts Options, tmp string, res *Result) (it item, ok bool, err error) {
	src := filepath.Join(opts.DataDir, "config.yaml")
	fi, err := os.Stat(src)
	if errors.Is(err, fs.ErrNotExist) {
		return item{}, false, nil
	}
	if err != nil {
		return item{}, false, err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return item{}, false, err
	}
	known := security.KnownSecretNames(opts.DotEnv, opts.Environ)
	cr, err := redactConfig(data, opts.DotEnv, environLookup(opts.Environ), known)
	if err != nil {
		return item{}, false, err
	}
	dst := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(dst, cr.Data, 0o600); err != nil {
		return item{}, false, fmt.Errorf("write the copy of config.yaml: %w", err)
	}
	res.Replaced = append(res.Replaced, cr.Replaced...)
	res.Warnings = append(res.Warnings, cr.Warnings...)
	return item{Rel: "config.yaml", Src: dst, Mode: fi.Mode()}, true, nil
}

// environLookup returns a lookup function for NAME=value entries. A later entry wins, like
// in os.Environ.
func environLookup(environ []string) func(string) (string, bool) {
	env := make(map[string]string, len(environ))
	for _, kv := range environ {
		if name, v, ok := strings.Cut(kv, "="); ok {
			env[name] = v
		}
	}
	return func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}
}

// lockBackups takes an exclusive flock on the folder dir without a wait. The kernel releases
// it when the process exits, so a crashed backup leaves no stale lock.
func lockBackups(dir string) (release func(), err error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("open the folder %s: %w", dir, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another backup is running in %s. Wait for it to end, then try again", dir)
		}
		return nil, fmt.Errorf("lock the folder %s: %w", dir, err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// removeStaleTmp removes the .tmp-* entries in dir that a crashed run left. The caller holds
// the lock of lockBackups, so no running backup owns these entries.
func removeStaleTmp(dir string) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read the folder %s: %w", dir, err)
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), ".tmp-") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if err := os.RemoveAll(p); err != nil {
			return fmt.Errorf("remove the old temp entry %s: %w", p, err)
		}
	}
	return nil
}

// writeArchiveFile writes the archive to a temp file next to out and renames it to out. It does
// not overwrite a file that exists. A failed write leaves no partial file.
func writeArchiveFile(ctx context.Context, out string, m Manifest, items []item) (size int64, err error) {
	if _, err := os.Lstat(out); err == nil {
		return 0, fmt.Errorf("%s exists. Choose another path with --out", out)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	part := filepath.Join(filepath.Dir(out), ".tmp-"+filepath.Base(out))
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, fmt.Errorf("create the archive: %w", err)
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(part)
		}
	}()
	cw := &countWriter{ctx: ctx, w: f}
	if err = writeArchive(cw, m, items); err != nil {
		return 0, fmt.Errorf("write the archive: %w", err)
	}
	if err = f.Sync(); err != nil {
		return 0, fmt.Errorf("write the archive: %w", err)
	}
	if err = f.Close(); err != nil {
		return 0, fmt.Errorf("write the archive: %w", err)
	}
	// Check again, because another run can create out during the write.
	if _, lerr := os.Lstat(out); lerr == nil {
		err = fmt.Errorf("%s exists. Choose another path with --out", out)
		return 0, err
	}
	if err = os.Rename(part, out); err != nil {
		return 0, fmt.Errorf("save the archive: %w", err)
	}
	return cw.n, nil
}

// countWriter counts the bytes that pass through it. It stops the write when ctx ends, so a
// signal stops a long archive write.
type countWriter struct {
	ctx context.Context
	w   io.Writer
	n   int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
