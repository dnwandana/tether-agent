package gatewaylock

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSecondLockFailsWithPid(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gateway.lock")
	rel, err := Acquire(p)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("%q", b)
	}
	_, err = Acquire(p)
	want := fmt.Sprintf("another gateway is running (pid %d); stop it first or use tether chat", os.Getpid())
	if err == nil || err.Error() != want {
		t.Fatal(err)
	}
	if err := rel(); err != nil {
		t.Fatal(err)
	}
	rel2, err := Acquire(p)
	if err != nil {
		t.Fatal("released:", err)
	}
	_ = rel2()
}

func TestHeld(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gateway.lock")
	if held, err := Held(p); held || err != nil {
		t.Fatalf("missing file: held=%v err=%v", held, err)
	}
	if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Held must not create the file: %v", err)
	}
	rel, err := Acquire(p)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if held, err := Held(p); !held || err != nil {
		t.Fatalf("lock held: held=%v err=%v", held, err)
	}
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("Held must not write the file: before %q, after %q", before, after)
	}
	if err := rel(); err != nil {
		t.Fatal(err)
	}
	if held, err := Held(p); held || err != nil {
		t.Fatalf("lock released: held=%v err=%v", held, err)
	}
	// The probe must not keep a lock that blocks a later gateway.
	rel2, err := Acquire(p)
	if err != nil {
		t.Fatal("after the probe:", err)
	}
	if err := rel2(); err != nil {
		t.Fatal(err)
	}
}

func TestHeldUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open a file with mode 0o000")
	}
	p := filepath.Join(t.TempDir(), "gateway.lock")
	if err := os.WriteFile(p, nil, 0o000); err != nil {
		t.Fatal(err)
	}
	held, err := Held(p)
	if held || err == nil {
		t.Fatalf("unreadable file: held=%v err=%v", held, err)
	}
}

func TestSecondLockIsLockedError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gateway.lock")
	rel, err := Acquire(p)
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	_, err = Acquire(p)
	var le *LockedError
	if !errors.As(err, &le) {
		t.Fatalf("%T %v", err, err)
	}
	if le.PID != strconv.Itoa(os.Getpid()) {
		t.Fatal(le.PID)
	}
}
