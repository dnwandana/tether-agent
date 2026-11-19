package security

import (
	"fmt"
	"io/fs"
	"syscall"
)

// pinStatKey returns the device, the inode, and the change time of fi.
func pinStatKey(fi fs.FileInfo) (string, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%d:%d:%d.%d", st.Dev, st.Ino, st.Ctim.Sec, st.Ctim.Nsec), true
}
