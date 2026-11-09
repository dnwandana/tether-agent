package acp

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrSessionLocked is the error of AcquireSessionLock when another tether process holds the
// lock of the session and the agent.
//
//lint:ignore ST1005 the text is a fixed user message with full sentences.
var ErrSessionLocked = errors.New("acp: this coding agent session is in use by another tether process. Wait for it to finish, or use /new for a new session.")

// LockPath returns the lock file of the session and the agent. The name is a hash of the key,
// so a session id or an agent id cannot make a path.
func LockPath(dataDir, sessionID, agent string) string {
	sum := sha256.Sum256([]byte(sessionID + "\x00" + agent))
	name := hex.EncodeToString(sum[:])[:16]
	return filepath.Join(dataDir, "acp", "locks", name+".lock")
}

// AcquireSessionLock takes an exclusive, non-blocking flock on the lock file of the session and
// the agent. It creates the lock folder with mode 0700. The lock ends when the process exits,
// so a crashed process leaves no stale lock. release frees the lock and keeps the file.
func AcquireSessionLock(dataDir, sessionID, agent string) (release func() error, err error) {
	path := LockPath(dataDir, sessionID, agent)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("acp: cannot create the lock folder %s: %w", dir, err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("acp: cannot open the lock file %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrSessionLocked
		}
		return nil, fmt.Errorf("acp: cannot lock %s: %w", path, err)
	}
	return func() error {
		uerr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return errors.Join(uerr, f.Close())
	}, nil
}
