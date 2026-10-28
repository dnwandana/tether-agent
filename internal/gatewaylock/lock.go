// Package gatewaylock holds the gateway lock of a data dir. It is a small package, so tether
// restore can check for a running gateway without an import of the gateway runtime.
package gatewaylock

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// LockedError is the error of Acquire when another process holds the lock. tether restore
// reads PID from it.
type LockedError struct {
	// PID is the number text from the lock file, or "unknown".
	PID string
}

func (e *LockedError) Error() string {
	return "another gateway is running (pid " + e.PID + "); stop it first or use tether chat"
}

// Acquire takes the gateway lock at path and writes the PID of the process into the file. The
// lock is an advisory flock: it ends when the process exits, so a crashed gateway leaves no
// stale lock. When another process holds the lock, the error names its PID. release frees the
// lock and keeps the file.
func Acquire(path string) (release func() error, err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("gateway: cannot open the lock file %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		defer f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			b, _ := io.ReadAll(io.LimitReader(f, 32))
			pid := strings.TrimSpace(string(b))
			if _, perr := strconv.Atoi(pid); perr != nil {
				pid = "unknown"
			}
			return nil, &LockedError{PID: pid}
		}
		return nil, fmt.Errorf("gateway: cannot lock %s: %w", path, err)
	}
	err = f.Truncate(0)
	if err == nil {
		_, err = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("gateway: cannot write the PID to %s: %w", path, err)
	}
	return func() error {
		uerr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return errors.Join(uerr, f.Close())
	}, nil
}

// Held reports if a running gateway holds the lock at path. The update command uses it to find
// a running gateway without a write to the data folder. It opens the file read-only and never
// creates it, so a missing file means that no gateway runs. It tries a shared lock and
// releases that lock at once.
func Held(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("gateway: cannot open the lock file %s: %w", path, err)
	}
	defer f.Close()
	fd := int(f.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return true, nil
		}
		return false, fmt.Errorf("gateway: cannot probe the lock %s: %w", path, err)
	}
	// The deferred Close also releases the shared lock, so an unlock error changes nothing.
	_ = syscall.Flock(fd, syscall.LOCK_UN)
	return false, nil
}
