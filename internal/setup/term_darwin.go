package setup

import "golang.org/x/sys/unix"

// The termios requests of macOS.
const (
	ioctlGetTermios = unix.TIOCGETA
	ioctlSetTermios = unix.TIOCSETA
)
