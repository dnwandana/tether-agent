package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/gatewaylock"
	"github.com/dnwandana/tether-agent/internal/memory"
)

// Summary describes a verified archive before the swap. Confirm shows it to the owner.
type Summary struct {
	Manifest Manifest
	Stats    DBStats
	// DataDir is the absolute path of the data dir that the restore replaces.
	DataDir string
	// PreRestoreDir is the folder that gets the old data files.
	PreRestoreDir string
}

// RestoreOptions controls one restore.
type RestoreOptions struct {
	// DataDir is the data dir to restore into. Restore creates it when it does not exist.
	DataDir string
	// Archive gives the gzip tar stream of the backup.
	Archive io.Reader
	// Confirm asks the owner to continue. nil means yes.
	Confirm func(Summary) bool
	// Now gives the time for the name of the pre-restore folder. nil means time.Now.
	Now func() time.Time
}

// RestoreResult describes a restore that is complete.
type RestoreResult struct {
	// PreRestoreDir holds the old data files. It is "" when the data dir had no old data.
	PreRestoreDir string
	Manifest      Manifest
}

// ErrStopped tells that the owner did not confirm the restore, or that a signal stopped it
// before the swap.
var ErrStopped = errors.New("stopped. Nothing changed")

// GatewayRunningError tells that a gateway holds the gateway lock of the data dir.
type GatewayRunningError struct {
	// PID is the number text from the lock file, or "unknown".
	PID string
}

func (e *GatewayRunningError) Error() string {
	return "a gateway is running (pid " + e.PID + "). Stop it first: systemctl --user stop tether"
}

// Restore replaces the allow-list state of opts.DataDir with the archive in opts.Archive. It
// holds the gateway lock and the exclusive DB lock from before the stage step until the swap
// returns, so no tether process uses the data dir during the restore. The old files go to a
// pre-restore folder. An error before the swap leaves the data dir unchanged.
func Restore(ctx context.Context, opts RestoreOptions) (*RestoreResult, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	dataDir, err := filepath.Abs(opts.DataDir)
	if err != nil {
		return nil, fmt.Errorf("find the data dir %s: %w", opts.DataDir, err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create the data dir %s: %w", dataDir, err)
	}

	releaseGateway, err := gatewaylock.Acquire(filepath.Join(dataDir, "gateway.lock"))
	if err != nil {
		var le *gatewaylock.LockedError
		if errors.As(err, &le) {
			return nil, &GatewayRunningError{PID: le.PID}
		}
		return nil, err
	}
	defer releaseGateway()

	releaseDB, err := db.LockExclusive(filepath.Join(dataDir, "state.db"))
	if err != nil {
		if errors.Is(err, db.ErrInUse) {
			return nil, fmt.Errorf("%w, for example tether chat. Close it first", db.ErrInUse)
		}
		return nil, err
	}
	defer releaseDB()

	st, err := stage(ctx, ctxReader{ctx: ctx, r: opts.Archive}, dataDir)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ErrStopped
		}
		return nil, err
	}
	keepStage := false
	defer func() {
		if !keepStage {
			os.RemoveAll(st.Dir)
		}
	}()

	preDir := filepath.Join(dataDir, "pre-restore-"+opts.Now().Format("20060102-150405"))
	sum := Summary{Manifest: st.Manifest, Stats: st.Stats, DataDir: dataDir, PreRestoreDir: preDir}
	if opts.Confirm != nil && !opts.Confirm(sum) {
		return nil, ErrStopped
	}
	// A signal during the confirmation stops the restore before the first rename.
	if ctx.Err() != nil {
		return nil, ErrStopped
	}

	if err := addMemoryLock(st.Dir); err != nil {
		return nil, err
	}
	moved, err := swap(dataDir, st.Dir, preDir)
	if err != nil {
		// After a failed undo the error names the stage folder, so it must stay.
		keepStage = errors.Is(err, errUndoFailed)
		return nil, err
	}
	res := &RestoreResult{Manifest: st.Manifest}
	if moved {
		res.PreRestoreDir = preDir
	}
	return res, nil
}

// addMemoryLock creates the memory folder and an empty lock file of the memory store in the
// stage folder dir. The archive never holds the lock file, and a backup takes the memory lock
// only when the lock file exists. Without this step a store write after the restore and a
// backup could run at the same time until the first store write makes the file. The memory
// store also creates its folder when it opens, so a restored data dir always has one.
func addMemoryLock(dir string) error {
	memDir := filepath.Join(dir, "memory")
	if err := os.Mkdir(memDir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create the memory folder in the stage folder: %w", err)
	}
	path := filepath.Join(memDir, memory.LockFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create the memory lock file in the stage folder: %w", err)
	}
	// Chmod sets the mode that the umask can change.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return fmt.Errorf("set the mode of the memory lock file in the stage folder: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("create the memory lock file in the stage folder: %w", err)
	}
	return nil
}

// ctxReader stops the read when ctx ends, so a signal stops the copy of a large entry.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
