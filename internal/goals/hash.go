package goals

import (
	"crypto/sha256"
	"encoding/hex"
)

// ItemHash returns the short hash of a subgoal or a gate command. The page
// sends it with a remove, so a remove of a changed list does nothing.
func ItemHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:8]
}
