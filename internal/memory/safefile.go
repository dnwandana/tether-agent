package memory

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// errNotRegular tells that a memory file is a symlink, a folder, a FIFO, or a device.
// The store reads only regular files, so it never reads outside the memory folder
// and a FIFO cannot stop a read.
var errNotRegular = errors.New("not a regular file")

// errFileTooLarge tells that a memory file is larger than the limit of the read.
var errFileTooLarge = errors.New("file too large")

// readFlags open a memory file for a read. O_NOFOLLOW refuses a symlink at the last
// part of the path. O_NONBLOCK stops the open of a FIFO from waiting for a writer; it
// has no effect on the read of a regular file.
const readFlags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK

// checkOpened checks that the file f, opened after the Lstat that gave before, is
// that same regular file. A file that was replaced between the Lstat and the open
// gives errNotRegular.
func checkOpened(f *os.File, before fs.FileInfo, max int64) (fs.FileInfo, error) {
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, errNotRegular
	}
	if max > 0 && after.Size() > max {
		return nil, errFileTooLarge
	}
	return after, nil
}

// openRegular opens the regular file name in root for a read. The size check comes
// before the open, so that the store never reads a large file. A max of 0 or less
// has no limit. The caller closes the file.
func openRegular(root *os.Root, name string, max int64) (*os.File, fs.FileInfo, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, nil, errNotRegular
	}
	if max > 0 && before.Size() > max {
		return nil, nil, errFileTooLarge
	}
	f, err := root.OpenFile(name, readFlags, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, nil, errNotRegular
		}
		return nil, nil, err
	}
	fi, err := checkOpened(f, before, max)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, fi, nil
}

// readRegularPath reads the regular file at path. It does not follow a symlink at
// path, and it refuses a file that is not regular with errNotRegular.
func readRegularPath(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errNotRegular
	}
	f, err := os.OpenFile(path, readFlags, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, errNotRegular
		}
		return nil, err
	}
	defer f.Close()
	if _, err := checkOpened(f, before, 0); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

// writeAtomicIn writes text to the file name of root through a temp file and a
// rename, the same as writeAtomic. The temp file and the rename stay in root.
func writeAtomicIn(root *os.Root, name, text string) error {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return fmt.Errorf("cannot write %s: %w", name, err)
	}
	tmp := strings.Replace(tempPattern, "*", hex.EncodeToString(rnd[:]), 1)
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("cannot write %s: %w", name, err)
	}
	fail := func(err error) error {
		f.Close()
		root.Remove(tmp)
		return fmt.Errorf("cannot write %s: %w", name, err)
	}
	if _, err := f.WriteString(text); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		root.Remove(tmp)
		return fmt.Errorf("cannot write %s: %w", name, err)
	}
	if err := root.Rename(tmp, filepath.Base(name)); err != nil {
		root.Remove(tmp)
		return fmt.Errorf("cannot write %s: %w", name, err)
	}
	return nil
}
