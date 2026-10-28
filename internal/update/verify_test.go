package update

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestParseChecksums(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("B", 64)
	text := []byte(a + "  tether-agent_0.4.0_linux_amd64.tar.gz\n" + b + " tether-agent_0.4.0_darwin_arm64.tar.gz\n\n")
	m, err := ParseChecksums(text)
	if err != nil || len(m) != 2 {
		t.Fatal(m, err)
	}
	if m["tether-agent_0.4.0_darwin_arm64.tar.gz"] != strings.ToLower(b) {
		t.Fatal("digest must be lowercase")
	}
	if _, err := ParseChecksums([]byte("notahash  x.tar.gz\n")); err == nil {
		t.Fatal("bad digest must fail")
	}
	if _, err := ParseChecksums([]byte(strings.Repeat("g", 64) + "  x.tar.gz\n")); err == nil {
		t.Fatal("non-hex must fail")
	}
	if _, err := ParseChecksums([]byte("onlyonefield\n")); err == nil {
		t.Fatal("one field must fail")
	}
	if _, err := ParseChecksums([]byte(a + "  x.tar.gz extra\n")); err == nil {
		t.Fatal("three fields must fail")
	}
}

func TestVerifySHA256(t *testing.T) {
	sum := sha256.Sum256([]byte("hello"))
	want := hex.EncodeToString(sum[:])
	if err := VerifySHA256([]byte("hello"), want); err != nil {
		t.Fatal(err)
	}
	if err := VerifySHA256([]byte("hello"), strings.ToUpper(want)); err != nil {
		t.Fatal("case must not matter")
	}
	err := VerifySHA256([]byte("hellp"), want)
	if err == nil || !strings.HasPrefix(err.Error(), "checksum mismatch: want "+want[:12]+"…, got ") {
		t.Fatal(err)
	}
	if err := VerifySHA256([]byte("hello"), "abc"); err == nil {
		t.Fatal("short digest must fail")
	}
}
