package update

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// candidatePattern names the temp files next to the binary. The same prefix in
// CheckWritable and Install lets a user find and remove a file that stays after a crash.
const candidatePattern = ".tether-update-*"

// ExecutablePath returns the real file of the running binary. A symlink to the binary
// stays, and only its target changes.
func ExecutablePath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot find the path of the tether binary: %w", err)
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("cannot resolve the path %s: %w", p, err)
	}
	return real, nil
}

// CheckWritable creates and removes one temp file in the folder of binPath. The updater
// calls it before the download, so a user with no write permission does not wait.
func CheckWritable(binPath string) error {
	dir := filepath.Dir(binPath)
	f, err := os.CreateTemp(dir, candidatePattern)
	if err != nil {
		return writeError(dir, err)
	}
	name := f.Name()
	closeErr := f.Close()
	removeErr := os.Remove(name)
	if closeErr != nil {
		return writeError(dir, closeErr)
	}
	if removeErr != nil {
		return writeError(dir, removeErr)
	}
	return nil
}

// Install writes data to a temp file next to binPath, runs check on it, and renames it
// over binPath. The temp file is in the same folder, so the rename is atomic. On each
// failure, the old binary stays and the temp file is removed.
func Install(binPath string, data []byte, check func(candidate string) error) error {
	dir := filepath.Dir(binPath)
	f, err := os.CreateTemp(dir, candidatePattern)
	if err != nil {
		return writeError(dir, err)
	}
	candidate := f.Name()
	done := false
	defer func() {
		if !done {
			os.Remove(candidate)
		}
	}()

	if _, err := f.Write(data); err != nil {
		f.Close()
		return writeError(dir, err)
	}
	if err := f.Close(); err != nil {
		return writeError(dir, err)
	}
	if err := os.Chmod(candidate, 0o755); err != nil {
		return writeError(dir, err)
	}
	if err := check(candidate); err != nil {
		return fmt.Errorf("new binary failed its check: %w", err)
	}
	if err := os.Rename(candidate, binPath); err != nil {
		return writeError(dir, err)
	}
	done = true
	return nil
}

// writeError gives the sudo hint for a permission error and keeps the cause for others.
func writeError(dir string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("cannot write to %s: permission denied; run: sudo tether update", dir)
	}
	return fmt.Errorf("cannot write to %s: %w", dir, err)
}
