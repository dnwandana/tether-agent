package memory

import (
	"crypto/sha256"
	"encoding/hex"
)

// EntryID returns the short ID of a memory entry: "m-" and the first 8 hex
// characters of the SHA-256 of the raw entry. The prompt, the tool, `/memory`,
// the doctor, and the dashboard show this ID.
func EntryID(entry string) string {
	sum := sha256.Sum256([]byte(entry))
	return "m-" + hex.EncodeToString(sum[:])[:8]
}
