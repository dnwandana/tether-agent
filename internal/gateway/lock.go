package gateway

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

// AcquireLock takes the gateway lock at path and writes the PID of the process into the
// file. The lock is an advisory flock: it ends when the process exits, so a
// crashed gateway leaves no stale lock. When another process holds the lock, the error
// names its PID. release frees the lock and keeps the file.
func AcquireLock(path string) (release func() error, err error) {
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
			return nil, fmt.Errorf("another gateway is running (pid %s); stop it first or use tether chat", pid)
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

// LockHeld reports if a running gateway holds the lock at path. The update command uses it
// to find a running gateway without a write to the data folder. It opens the file read-only
// and never creates it, so a missing file means that no gateway runs. It tries a shared
// lock and releases that lock at once.
func LockHeld(path string) (bool, error) {
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
