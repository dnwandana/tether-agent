package update

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
)

// ParseChecksums returns a map from the asset name to the lowercase hex SHA-256 digest.
// Each non-empty line of text must be "<digest> <asset name>", with one or two spaces.
func ParseChecksums(text []byte) (map[string]string, error) {
	sums := make(map[string]string)
	for i, line := range strings.Split(string(text), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return nil, fmt.Errorf("checksums.txt line %d: want 2 fields, got %d", i+1, len(fields))
		}
		digest := strings.ToLower(fields[0])
		if !isSHA256Hex(digest) {
			return nil, fmt.Errorf("checksums.txt line %d: digest is not 64 hex characters", i+1)
		}
		sums[fields[1]] = digest
	}
	return sums, nil
}

// VerifySHA256 returns an error if the SHA-256 of data is not wantHex.
// The case of wantHex does not matter.
func VerifySHA256(data []byte, wantHex string) error {
	want := strings.ToLower(wantHex)
	if !isSHA256Hex(want) {
		return fmt.Errorf("bad checksum %q: want 64 hex characters", wantHex)
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
		return fmt.Errorf("checksum mismatch: want %s…, got %s…", want[:12], got[:12])
	}
	return nil
}

// isSHA256Hex returns true if s is 64 hex characters.
func isSHA256Hex(s string) bool {
	if len(s) != 2*sha256.Size {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
