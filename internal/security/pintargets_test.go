package security

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// writeProg writes an executable file and returns its path.
func writeProg(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func sumHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestResolvePinTargets(t *testing.T) {
	bin := t.TempDir()
	const script = "#!/usr/bin/env fakepy\nprint(1)\n"
	const pyBody = "\x7fELF fake python"
	twine := writeProg(t, bin, "twine", script)
	py := writeProg(t, bin, "fakepy", pyBody)
	deploy := writeProg(t, bin, "deploy", "\x7fELF deploy")
	directBody := "#!" + py + " -u\nprint(2)\n"
	direct := writeProg(t, bin, "direct", directBody)
	writeProg(t, bin, "tail", "\x7fELF tail")
	// A symlink in another folder: the pin uses the real path.
	links := t.TempDir()
	if err := os.Symlink(twine, filepath.Join(links, "twine")); err != nil {
		t.Fatal(err)
	}
	twineT := PinTarget{Path: RealPath(twine), SHA256: sumHex(script)}
	pyT := PinTarget{Path: RealPath(py), SHA256: sumHex(pyBody), Interpreter: true}
	for _, tc := range []struct {
		cmd   string
		parts []int
		path  string
		want  []PinTarget
	}{
		{"twine upload x", []int{0}, bin, []PinTarget{twineT, pyT}},
		{"cd /tmp && twine upload x | tail -n 5", []int{1}, bin, []PinTarget{twineT, pyT}},
		{"twine upload x", []int{0}, links + string(os.PathListSeparator) + bin, []PinTarget{twineT, pyT}},
		{"deploy --prod", []int{0}, bin, []PinTarget{{Path: RealPath(deploy), SHA256: sumHex("\x7fELF deploy")}}},
		{"direct run", []int{0}, bin, []PinTarget{{Path: RealPath(direct), SHA256: sumHex(directBody)}, pyT}},
		{"twine upload a && twine upload b", []int{0, 1}, bin, []PinTarget{twineT, pyT}},
	} {
		got, err := ResolvePinTargets(ReadCommand(tc.cmd), tc.parts, "/tmp", tc.path)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: got %+v, %v; want %+v", tc.cmd, got, err, tc.want)
		}
	}
}

func TestResolvePinTargetsNoProgram(t *testing.T) {
	bin := t.TempDir()
	writeProg(t, bin, "broken", "#!/usr/bin/env tether-no-such-interp\n")
	writeProg(t, bin, "badabs", "#!/no/such/interp\n")
	for _, tc := range []struct {
		cmd   string
		parts []int
	}{
		{"cd proj && ./venv/bin/twine upload x", []int{1}},
		{"tether-no-such-program upload x", []int{0}},
		{"broken run", []int{0}},
		{"badabs run", []int{0}},
		{"cd x", []int{0}},
	} {
		_, err := ResolvePinTargets(ReadCommand(tc.cmd), tc.parts, "/tmp", bin)
		if !errors.Is(err, ErrNoProgram) || err.Error() != "the program cannot be found on PATH" {
			t.Errorf("%q: got %v", tc.cmd, err)
		}
	}
}

func TestResolvePinTargetsTooLarge(t *testing.T) {
	bin := t.TempDir()
	f, err := os.OpenFile(filepath.Join(bin, "big"), os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(256<<20 + 1); err != nil { // a sparse file
		t.Fatal(err)
	}
	f.Close()
	_, err = ResolvePinTargets(ReadCommand("big run"), []int{0}, "/tmp", bin)
	if err == nil || !strings.Contains(err.Error(), "is larger than 256 MB") || errors.Is(err, ErrNoProgram) {
		t.Fatal(err)
	}
}

func TestPinHashCache(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("the hash cache needs the change time of linux or darwin")
	}
	bin := t.TempDir()
	p := writeProg(t, bin, "tool", "\x7fELF one")
	before := pinHashReads.Load()
	for range 2 {
		if _, err := ResolvePinTargets(ReadCommand("tool"), []int{0}, "/tmp", bin); err != nil {
			t.Fatal(err)
		}
	}
	if n := pinHashReads.Load() - before; n != 1 {
		t.Fatalf("the file was read %d times, want 1", n)
	}
	// The same size and the old mtime: the change time still shows the change.
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("\x7fELF two"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	got, err := ResolvePinTargets(ReadCommand("tool"), []int{0}, "/tmp", bin)
	if err != nil || got[0].SHA256 != sumHex("\x7fELF two") {
		t.Fatalf("got %+v, %v", got, err)
	}
}
