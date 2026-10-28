package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestReadLockMissingIsEmpty(t *testing.T) {
	l, err := ReadLock(t.TempDir())
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	if l.Version != 1 || len(l.Installed) != 0 || l.Installed == nil {
		t.Fatalf("got %+v, want version 1 and an empty map", l)
	}
}

func fullEntry(name string) LockEntry {
	return LockEntry{
		Source:        "github",
		Identifier:    "example/skills/" + name,
		URL:           "https://github.com/example/skills.git",
		Ref:           "main",
		Commit:        "0123456789abcdef0123456789abcdef01234567",
		SkillPath:     "skills/" + name,
		TrustLevel:    TrustCommunity,
		ScanVerdict:   VerdictSafe,
		OwnerOverride: false,
		ContentHash:   "sha256:" + strings.Repeat("a", 64),
		InstallPath:   name,
		Files:         []string{"SKILL.md"},
		InstalledAt:   "2026-10-28T10:00:00Z",
		UpdatedAt:     "2026-10-28T11:00:00Z",
	}
}

func TestLockRoundTripKeyOrder(t *testing.T) {
	dir := t.TempDir()
	want := map[string]LockEntry{"zeta": fullEntry("zeta"), "alpha": fullEntry("alpha")}
	err := UpdateLock(dir, func(l *LockFile) error {
		l.Installed["zeta"] = want["zeta"]
		l.Installed["alpha"] = want["alpha"]
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateLock: %v", err)
	}
	path := filepath.Join(HubDir(dir), "lock.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Index(s, `"alpha"`) > strings.Index(s, `"zeta"`) {
		t.Errorf("alpha is not before zeta:\n%s", s)
	}
	keys := []string{"source", "identifier", "url", "ref", "commit", "skill_path", "trust_level",
		"scan_verdict", "owner_override", "content_hash", "install_path", "files", "installed_at", "updated_at"}
	start := strings.Index(s, `"alpha"`)
	entry := s[start:strings.Index(s, `"zeta"`)]
	last := -1
	for _, k := range keys {
		i := strings.Index(entry, `"`+k+`"`)
		if i < 0 || i <= last {
			t.Fatalf("key %q is missing or out of order:\n%s", k, entry)
		}
		last = i
	}
	if !strings.HasSuffix(s, "\n") {
		t.Error("the file does not end with a newline")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, want 0600", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(HubDir(dir)); fi.Mode().Perm() != 0o700 {
		t.Errorf(".hub mode %v, want 0700", fi.Mode().Perm())
	}
	l, err := ReadLock(dir)
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	if !reflect.DeepEqual(l.Installed, want) {
		t.Errorf("got %+v\nwant %+v", l.Installed, want)
	}
}

func TestReadLockBadFiles(t *testing.T) {
	for _, tc := range []struct{ content, want string }{
		{"{not json", "lock.json"},
		{`{"version": 2, "installed": {}}`, "version"},
	} {
		dir := t.TempDir()
		if err := os.MkdirAll(HubDir(dir), 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(HubDir(dir), "lock.json")
		if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := ReadLock(dir)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: err = %v, want it to contain %q", tc.content, err, tc.want)
		}
		uerr := UpdateLock(dir, func(*LockFile) error { return nil })
		if uerr == nil {
			t.Errorf("%q: UpdateLock did not fail", tc.content)
		}
		got, _ := os.ReadFile(path)
		if string(got) != tc.content {
			t.Errorf("%q: the file changed to %q", tc.content, got)
		}
	}
}

func TestUpdateLockFnErrorWritesNothing(t *testing.T) {
	dir := t.TempDir()
	stop := errors.New("stop")
	err := UpdateLock(dir, func(l *LockFile) error {
		l.Installed["x"] = fullEntry("x")
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v, want stop", err)
	}
	l, err := ReadLock(dir)
	if err != nil || len(l.Installed) != 0 {
		t.Fatalf("got %+v, %v; want an empty lockfile", l, err)
	}
	if _, err := os.Stat(filepath.Join(HubDir(dir), "lock.json")); !os.IsNotExist(err) {
		t.Errorf("lock.json exists: %v", err)
	}
}

func TestUpdateLockConcurrent(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("skill-%d", i)
			errs <- UpdateLock(dir, func(l *LockFile) error {
				l.Installed[name] = fullEntry(name)
				return nil
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	l, err := ReadLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Installed) != 20 {
		t.Fatalf("got %d entries, want 20", len(l.Installed))
	}
}

func TestCheckEntry(t *testing.T) {
	if err := CheckEntry("pdf", LockEntry{InstallPath: "pdf"}); err != nil {
		t.Errorf("valid entry: %v", err)
	}
	for _, p := range []string{"../x", "a/b", "/abs", "", "other"} {
		if err := CheckEntry("pdf", LockEntry{InstallPath: p}); err == nil {
			t.Errorf("install path %q: no error", p)
		}
	}
	if err := CheckEntry("Bad Name", LockEntry{InstallPath: "Bad Name"}); err == nil {
		t.Error(`name "Bad Name": no error`)
	}
}

func hashOf(t *testing.T, files map[string]string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	writeTree(t, dir, files)
	h, fs, err := ContentHash(dir)
	if err != nil {
		t.Fatalf("ContentHash: %v", err)
	}
	return h, fs
}

func TestContentHash(t *testing.T) {
	base, files := hashOf(t, map[string]string{"SKILL.md": "a", "scripts/x.py": "b"})
	if !strings.HasPrefix(base, "sha256:") || len(base) != 7+64 {
		t.Errorf("hash %q has a bad form", base)
	}
	if !reflect.DeepEqual(files, []string{"SKILL.md", "scripts/x.py"}) {
		t.Errorf("files = %v", files)
	}
	renamed, _ := hashOf(t, map[string]string{"SKILL.md": "a", "scripts/y.py": "b"})
	swapped, _ := hashOf(t, map[string]string{"SKILL.md": "b", "scripts/x.py": "a"})
	same, _ := hashOf(t, map[string]string{"SKILL.md": "a", "scripts/x.py": "b"})
	hidden, _ := hashOf(t, map[string]string{"SKILL.md": "a", "scripts/x.py": "b", ".hidden": ""})
	if renamed == base {
		t.Error("a rename does not change the hash")
	}
	if swapped == base {
		t.Error("a content swap does not change the hash")
	}
	if same != base {
		t.Error("the same content gives a different hash")
	}
	if hidden == base {
		t.Error("a hidden file does not change the hash")
	}
}

func TestContentHashCountsSymlinks(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"SKILL.md": "a"})
	before, _, err := ContentHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{"secret.txt": "s"})
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skip("symlinks are not available:", err)
	}
	after, files, err := ContentHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The link is listed but not followed: secret.txt is not in the list.
	if !reflect.DeepEqual(files, []string{"SKILL.md", "link"}) {
		t.Errorf("files = %v, want SKILL.md and link", files)
	}
	if after == before {
		t.Error("an added symlink must change the hash")
	}
}

func TestShortCommit(t *testing.T) {
	if got := ShortCommit("0123456789abcdef"); got != "0123456" {
		t.Errorf("got %q", got)
	}
	if got := ShortCommit("abc"); got != "abc" {
		t.Errorf("got %q", got)
	}
}
