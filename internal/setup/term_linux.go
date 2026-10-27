package setup

import "golang.org/x/sys/unix"

// The termios requests of Linux.
const (
	ioctlGetTermios = unix.TCGETS
	ioctlSetTermios = unix.TCSETS
)
