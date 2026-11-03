//go:build !linux && !darwin

package skills

import "io/fs"

// statKey gives no key on this platform, so the guard scans on each call.
func statKey(fs.FileInfo) (string, bool) {
	return "", false
}
