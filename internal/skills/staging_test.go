package skills

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewStagingLayout(t *testing.T) {
	skills := t.TempDir()
	s, err := NewStaging(skills, time.Now())
	if err != nil {
		t.Fatalf("NewStaging: %v", err)
	}
	stagingDir := filepath.Join(skills, ".hub", "staging")
	if got := filepath.Dir(s.Root); got != stagingDir {
		t.Fatalf("parent of Root = %q, want %q", got, stagingDir)
	}
	if got, want := s.Repo(), filepath.Join(s.Root, "repo"); got != want {
		t.Errorf("Repo() = %q, want %q", got, want)
	}
	if got, want := s.Skill(), filepath.Join(s.Root, "skill"); got != want {
		t.Errorf("Skill() = %q, want %q", got, want)
	}
	if got, want := s.Old(), filepath.Join(s.Root, "old"); got != want {
		t.Errorf("Old() = %q, want %q", got, want)
	}
	for _, dir := range []string{filepath.Join(skills, ".hub"), stagingDir, s.Root} {
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if mode := fi.Mode().Perm(); mode != 0o700 {
			t.Errorf("mode of %s = %o, want 700", dir, mode)
		}
	}

	s2, err := NewStaging(skills, time.Now())
	if err != nil {
		t.Fatalf("second NewStaging: %v", err)
	}
	if s2.Root == s.Root {
		t.Errorf("two calls gave the same Root %q", s.Root)
	}

	if err := s.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(s.Root); !os.IsNotExist(err) {
		t.Errorf("Root still exists after Remove: err=%v", err)
	}

	var nilStaging *Staging
	if err := nilStaging.Remove(); err != nil {
		t.Errorf("Remove on nil receiver = %v, want nil", err)
	}
}

func TestCleanStale(t *testing.T) {
	skills := t.TempDir()
	now := time.Now()
	stagingDir := filepath.Join(skills, ".hub", "staging")
	old := filepath.Join(stagingDir, "old1")
	fresh := filepath.Join(stagingDir, "new1")
	for _, dir := range []string{old, fresh} {
		if err := os.MkdirAll(filepath.Join(dir, "repo"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(old, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(fresh, now.Add(-10*time.Minute), now.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}

	if err := CleanStale(skills, now, time.Hour); err != nil {
		t.Fatalf("CleanStale: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("stale folder still exists: err=%v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh folder was removed: %v", err)
	}

	if err := CleanStale(t.TempDir(), now, time.Hour); err != nil {
		t.Errorf("CleanStale with no .hub = %v, want nil", err)
	}
}

func TestCopyTree(t *testing.T) {
	src := t.TempDir()
	body := []byte("---\nname: demo\n---\nbody\n")
	mustWrite := func(rel string, data []byte, mode os.FileMode) {
		t.Helper()
		p := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("SKILL.md", body, 0o644)
	mustWrite("scripts/run.sh", []byte("#!/bin/sh\necho hi\n"), 0o755)
	mustWrite(".git/config", []byte("[core]\n"), 0o644)
	if err := os.Symlink("SKILL.md", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "out")
	if err := CopyTree(src, dst); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("SKILL.md = %q, want %q", got, body)
	}
	fi, err := os.Stat(filepath.Join(dst, "scripts", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o755 {
		t.Errorf("mode of run.sh = %o, want 755", mode)
	}
	if _, err := os.Lstat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Errorf(".git was copied: err=%v", err)
	}
	li, err := os.Lstat(filepath.Join(dst, "link"))
	if err != nil {
		t.Fatal(err)
	}
	if li.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link is not a symlink: mode %v", li.Mode())
	}
	if target, err := os.Readlink(filepath.Join(dst, "link")); err != nil || target != "SKILL.md" {
		t.Errorf("link target = %q, %v; want \"SKILL.md\"", target, err)
	}

	if err := CopyTree(src, dst); err == nil {
		t.Error("CopyTree into an existing dst: want error, got nil")
	}
}

func TestCopyTreeDoesNotFollowFolderSymlink(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(src, "out")); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "copy")
	if err := CopyTree(src, dst); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	li, err := os.Lstat(filepath.Join(dst, "out"))
	if err != nil {
		t.Fatal(err)
	}
	if li.Mode()&os.ModeSymlink == 0 {
		t.Errorf("folder symlink was followed: mode %v", li.Mode())
	}
}
