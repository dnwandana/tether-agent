package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"testing"
)

func TestEntryID(t *testing.T) {
	sum := sha256.Sum256([]byte("VPS = hetzner"))
	want := "m-" + hex.EncodeToString(sum[:])[:8]
	if got := EntryID("VPS = hetzner"); got != want || len(got) != 10 {
		t.Fatalf("got %q, want %q", got, want)
	}
	// The ID is of the raw entry, so a change of white space gives another ID.
	if EntryID("VPS = hetzner ") == want {
		t.Fatal("the ID must use the raw entry")
	}
	if !regexp.MustCompile(`^m-[0-9a-f]{8}$`).MatchString(EntryID("")) {
		t.Fatal(EntryID(""))
	}
}
