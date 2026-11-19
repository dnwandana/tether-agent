//go:build !linux && !darwin

package security

import "io/fs"

// pinStatKey has no change time on this system, so the pin hash is not cached.
func pinStatKey(fs.FileInfo) (string, bool) { return "", false }
