package security

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// writeDotEnv writes text to a new .env file with mode 0600 and returns its path.
func writeDotEnv(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func readText(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSaveDotEnvLines(t *testing.T) {
	for _, tc := range []struct{ name, before, key, value, after string }{
		{"new file", "", "PYPI_TOKEN", "pypi-AgEIcHlwaS5vcmc", "PYPI_TOKEN=pypi-AgEIcHlwaS5vcmc\n"},
		{"new line", "# keys\nA=1\n", "B", "two", "# keys\nA=1\nB=two\n"},
		{"changed line", "A=1\nB=2\n", "A", "new", "A=new\nB=2\n"},
		{"duplicate lines", "A=1\nB=2\nexport A=3\n# end\n", "A", "new", "B=2\nA=new\n# end\n"},
		{"no final newline", "A=1", "B", "2", "A=1\nB=2\n"},
		{"space and hash", "", "A", "has space#x", "A=\"has space#x\"\n"},
		{"quote and backslash", "", "A", `a"b\c`, `A="a\"b\\c"` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), ".env")
			if tc.before != "" {
				p = writeDotEnv(t, tc.before)
			}
			if err := SaveDotEnv(p, tc.key, tc.value); err != nil {
				t.Fatal(err)
			}
			if got := readText(t, p); got != tc.after {
				t.Fatalf("got %q, want %q", got, tc.after)
			}
			fi, err := os.Stat(p)
			if err != nil || fi.Mode().Perm() != 0o600 {
				t.Fatal("the mode must be 0600:", fi.Mode(), err)
			}
			m, err := LoadDotEnv(p)
			if err != nil || m[tc.key] != tc.value {
				t.Fatalf("LoadDotEnv gives %q, %v", m[tc.key], err)
			}
		})
	}
}

func TestCheckSecretValue(t *testing.T) {
	const refused = "The value has a line break or is longer than 4096 bytes. Send it again."
	for _, v := range []string{"a\nb", "a\rb", "x\n", strings.Repeat("x", 4097)} {
		if err := CheckSecretValue(v); err == nil || err.Error() != refused {
			t.Errorf("%q: got %v", v, err)
		}
	}
	for _, v := range []string{"pypi-AgEIcHlwaS5vcmc", "has space", strings.Repeat("x", 4096)} {
		if err := CheckSecretValue(v); err != nil {
			t.Errorf("%q: got %v", v, err)
		}
	}
}

func TestSaveDotEnvRefuses(t *testing.T) {
	p := writeDotEnv(t, "A=1\n")
	if err := SaveDotEnv(p, "B", "two\nlines"); err == nil || !strings.Contains(err.Error(), "line break") {
		t.Fatal(err)
	}
	for _, name := range []string{"", "BAD NAME", "1ABC", "A=B"} {
		if err := SaveDotEnv(p, name, "v"); err == nil || !strings.Contains(err.Error(), "is not a valid .env name") {
			t.Fatalf("%q: %v", name, err)
		}
	}
	if got := readText(t, p); got != "A=1\n" {
		t.Fatalf("a refused save changed the file: %q", got)
	}
}

// The save writes to the target of a symlink and keeps the link.
func TestSaveDotEnvSymlink(t *testing.T) {
	real := writeDotEnv(t, "A=1\n")
	link := filepath.Join(t.TempDir(), ".env")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := SaveDotEnv(link, "B", "2"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link must stay a link:", err)
	}
	if got := readText(t, real); got != "A=1\nB=2\n" {
		t.Fatalf("%q", got)
	}
}

// Two writers of different names at the same time: each line stays.
func TestSaveDotEnvTwoWriters(t *testing.T) {
	p := writeDotEnv(t, "A=1\n")
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 10 {
		wg.Add(2)
		go func() { defer wg.Done(); errs <- SaveDotEnv(p, fmt.Sprintf("SETUP_%d", i), "setup-value") }()
		go func() { defer wg.Done(); errs <- SaveDotEnv(p, fmt.Sprintf("GATEWAY_%d", i), "gateway-value") }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	m, err := LoadDotEnv(p)
	if err != nil || len(m) != 21 {
		t.Fatalf("got %d names, want 21: %v", len(m), err)
	}
}

func TestSaveDotEnvLockTimeout(t *testing.T) {
	p := writeDotEnv(t, "A=1\n")
	old := dotEnvLockWait
	dotEnvLockWait = 100 * time.Millisecond
	t.Cleanup(func() { dotEnvLockWait = old })
	f, err := os.OpenFile(p+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	err = SaveDotEnv(p, "B", "2")
	if !errors.Is(err, ErrDotEnvLocked) || err.Error() != ".env is locked by another process. Try again." {
		t.Fatal(err)
	}
	if got := readText(t, p); got != "A=1\n" {
		t.Fatalf("%q", got)
	}
}
