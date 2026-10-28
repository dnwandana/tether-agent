package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// noCandidates fails the test if a .tether-update-* file stays in dir.
func noCandidates(t *testing.T, dir string) {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, ".tether-update-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 0 {
		t.Fatalf("candidate left behind: %v", m)
	}
}

func TestInstallReplacesAndRunsCheck(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "tether")
	if err := os.WriteFile(bin, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	var checked string
	if err := Install(bin, []byte("new"), func(c string) error { checked = c; return nil }); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(bin); string(b) != "new" {
		t.Fatal(string(b))
	}
	if filepath.Dir(checked) != dir || !strings.HasPrefix(filepath.Base(checked), ".tether-update-") {
		t.Fatal(checked)
	}
	st, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Fatal(st.Mode())
	}
	noCandidates(t, dir)
}

func TestInstallKeepsOldOnFailedCheck(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "tether")
	if err := os.WriteFile(bin, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := Install(bin, []byte("new"), func(string) error { return errors.New("boom") })
	if err == nil || err.Error() != "new binary failed its check: boom" {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(bin); string(b) != "old" {
		t.Fatal(string(b))
	}
	noCandidates(t, dir)
}

func TestInstallFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	binDir := filepath.Join(dir, "bin")
	for _, d := range []string{realDir, binDir} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	real := filepath.Join(realDir, "tether")
	if err := os.WriteFile(real, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(binDir, "tether")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	p, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	if err := Install(p, []byte("new"), func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	st, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&os.ModeSymlink == 0 {
		t.Fatal("link is not a symlink any more:", st.Mode())
	}
	if b, _ := os.ReadFile(link); string(b) != "new" {
		t.Fatal(string(b))
	}
	noCandidates(t, realDir)
	noCandidates(t, binDir)
}

func TestCheckWritable(t *testing.T) {
	dir := t.TempDir()
	if err := CheckWritable(filepath.Join(dir, "tether")); err != nil {
		t.Fatal(err)
	}
	noCandidates(t, dir)
}

func TestInstallPermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	bin := filepath.Join(dir, "tether")
	want := "cannot write to " + dir + ": permission denied; run: sudo tether update"
	if err := CheckWritable(bin); err == nil || err.Error() != want {
		t.Fatal(err)
	}
	if err := Install(bin, []byte("new"), func(string) error { return nil }); err == nil || err.Error() != want {
		t.Fatal(err)
	}
}

func TestExecutablePath(t *testing.T) {
	p, err := ExecutablePath()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(p) {
		t.Fatal("not absolute:", p)
	}
	st, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&os.ModeSymlink != 0 {
		t.Fatal("result is a symlink:", p)
	}
}
