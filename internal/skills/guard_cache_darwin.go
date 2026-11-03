package skills

import (
	"fmt"
	"io/fs"
	"syscall"
)

// statKey returns the inode and the change time of fi.
func statKey(fi fs.FileInfo) (string, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%d:%d:%d.%d", st.Dev, st.Ino, st.Ctimespec.Sec, st.Ctimespec.Nsec), true
}
