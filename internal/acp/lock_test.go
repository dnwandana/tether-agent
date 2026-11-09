package acp

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLockPath(t *testing.T) {
	sum := sha256.Sum256([]byte("s1\x00claude-code"))
	want := filepath.Join("/data", "acp", "locks", hex.EncodeToString(sum[:])[:16]+".lock")
	if got := LockPath("/data", "s1", "claude-code"); got != want {
		t.Fatalf("LockPath = %q, want %q", got, want)
	}
	if LockPath("/data", "s1", "codex") == want {
		t.Fatal("two agents of one session must get two lock files")
	}
	if LockPath("/data", "ab", "c") == LockPath("/data", "a", "bc") {
		t.Fatal("the separator must keep the session id and the agent apart")
	}
	if got := LockPath("/data", "../../etc", "x/y"); filepath.Dir(got) != filepath.Join("/data", "acp", "locks") {
		t.Fatalf("a session id must not make a path: %q", got)
	}
}

func TestAcquireSessionLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	release, err := AcquireSessionLock(dir, "s1", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	// A second open file description in this process acts as a second process.
	if _, err := AcquireSessionLock(dir, "s1", "claude-code"); !errors.Is(err, ErrSessionLocked) {
		t.Fatalf("the second acquire must give ErrSessionLocked, got %v", err)
	}
	// Another key is free.
	other, err := AcquireSessionLock(dir, "s1", "codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := other(); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	again, err := AcquireSessionLock(dir, "s1", "claude-code")
	if err != nil {
		t.Fatal("the lock must be free after the release:", err)
	}
	if err := again(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireSessionLockCreatesPrivateFolder(t *testing.T) {
	dir := t.TempDir()
	release, err := AcquireSessionLock(dir, "s1", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, p := range []string{filepath.Join(dir, "acp"), filepath.Join(dir, "acp", "locks")} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if !st.IsDir() || st.Mode().Perm() != 0o700 {
			t.Fatalf("%s: mode %v, want a folder with mode 0700", p, st.Mode())
		}
	}
	if _, err := os.Stat(LockPath(dir, "s1", "claude-code")); err != nil {
		t.Fatal("the lock file must exist:", err)
	}
}

func TestErrSessionLockedText(t *testing.T) {
	want := "acp: this coding agent session is in use by another tether process. Wait for it to finish, or use /new for a new session."
	if ErrSessionLocked.Error() != want {
		t.Fatal(ErrSessionLocked)
	}
}
