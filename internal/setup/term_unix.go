//go:build linux || darwin

package setup

import "golang.org/x/sys/unix"

// IsTerminal reports whether fd is a terminal. Commands outside this package use it to
// decide whether they can ask a question.
func IsTerminal(fd int) bool { return isTerminal(fd) }

// isTerminal reports whether fd is a terminal. Only a terminal has termios settings.
func isTerminal(fd int) bool {
	_, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	return err == nil
}

// echoOff turns off the echo of the terminal fd. The returned function puts back the old
// settings. Canonical mode and the signal keys stay on, so Enter ends the line and Ctrl-C
// sends SIGINT.
func echoOff(fd int) (func() error, error) {
	old, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	t := *old
	t.Lflag &^= unix.ECHO
	t.Lflag |= unix.ICANON | unix.ISIG
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &t); err != nil {
		return nil, err
	}
	return func() error { return unix.IoctlSetTermios(fd, ioctlSetTermios, old) }, nil
}
