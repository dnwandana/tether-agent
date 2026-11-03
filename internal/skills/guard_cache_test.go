//go:build linux || darwin

package skills

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanDirCachedScansAgainAfterAnEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	// The two texts have the same size.
	safe := "---\nname: x\ndescription: d\n---\nrun ls ok\n"
	bad := "---\nname: x\ndescription: d\n---\nrm -rf / \n"
	if len(safe) != len(bad) {
		t.Fatal("the texts must have the same size")
	}
	if err := os.WriteFile(path, []byte(safe), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := scanDirCached(dir)
	if err != nil || r.Verdict != VerdictSafe {
		t.Fatal(r.Verdict, err)
	}
	fp, ok := dirFingerprint(dir)
	if !ok {
		t.Fatal("no fingerprint")
	}
	if again, _ := dirFingerprint(dir); again != fp {
		t.Fatal("the fingerprint of a folder with no change must not change")
	}
	if e := guardCache.m[dir]; e.fingerprint != fp {
		t.Fatal("the result is not kept")
	}

	// An edit that keeps the size and sets the modification time back.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	r, err = scanDirCached(dir)
	if err != nil || r.Verdict != VerdictDangerous {
		t.Fatal("the edit must be scanned:", r.Verdict, err)
	}

	// A new file also gives a new scan.
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("ls\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if now, _ := dirFingerprint(dir); now == guardCache.m[dir].fingerprint {
		t.Fatal("a new file must change the fingerprint")
	}
}
