package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSecondLockFailsWithPid(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gateway.lock")
	rel, err := AcquireLock(p)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("%q", b)
	}
	_, err = AcquireLock(p)
	want := fmt.Sprintf("another gateway is running (pid %d); stop it first or use tether chat", os.Getpid())
	if err == nil || err.Error() != want {
		t.Fatal(err)
	}
	if err := rel(); err != nil {
		t.Fatal(err)
	}
	rel2, err := AcquireLock(p)
	if err != nil {
		t.Fatal("released:", err)
	}
	_ = rel2()
}
