//go:build !linux && !darwin

package setup

import "errors"

// isTerminal is always false on this system, so the wizard shows the secrets as it reads
// them.
func isTerminal(int) bool { return false }

// echoOff is not available on this system.
func echoOff(int) (func() error, error) {
	return nil, errors.New("setup: hidden input is not available on this system")
}
