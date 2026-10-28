package db

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// The lock on <db path>.lock is a flock. It is independent of the fcntl locks of SQLite, so
// it does not change how SQLite locks the database. The kernel releases it when the process
// exits, so a crashed process leaves no stale lock.

// ErrRestoreRunning tells that tether restore holds the exclusive lock on the DB.
var ErrRestoreRunning = errors.New("a restore is running")

// ErrInUse tells that another tether process holds a shared lock on the DB.
var ErrInUse = errors.New("state.db is open in another tether process")

// LockPath returns the path of the lock file for the DB at dbPath.
func LockPath(dbPath string) string {
	return dbPath + ".lock"
}

// lockShared takes a shared lock on the lock file of dbPath. Each open DB holds one, so many
// processes can use the DB at the same time. When a restore holds the exclusive lock, the
// error wraps both ErrRestoreRunning and syscall.EWOULDBLOCK.
func lockShared(dbPath string) (*os.File, error) {
	f, err := flockFile(dbPath, syscall.LOCK_SH)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return nil, fmt.Errorf("%w: %w", ErrRestoreRunning, err)
	}
	return f, err
}

// LockExclusive takes the exclusive lock on the lock file of dbPath, and creates the file
// when it is missing. tether restore uses it to make sure that no other tether process has
// the DB open. It wraps ErrInUse when another process holds a lock. release unlocks and
// closes the file, and keeps the file.
func LockExclusive(dbPath string) (release func() error, err error) {
	f, err := flockFile(dbPath, syscall.LOCK_EX)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return nil, fmt.Errorf("db: %w", ErrInUse)
	}
	if err != nil {
		return nil, err
	}
	return func() error { return unlock(f) }, nil
}

// flockFile opens or creates the lock file of dbPath with mode 0600 and locks it without a
// wait. On EWOULDBLOCK it returns the raw syscall error, so the callers can map it.
func flockFile(dbPath string, how int) (*os.File, error) {
	path := LockPath(dbPath)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("db: cannot open the lock file %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, err
		}
		return nil, fmt.Errorf("db: cannot lock %s: %w", path, err)
	}
	return f, nil
}

// unlock releases the flock on f and closes f.
func unlock(f *os.File) error {
	uerr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return errors.Join(uerr, f.Close())
}
