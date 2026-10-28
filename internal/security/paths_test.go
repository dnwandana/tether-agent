package security

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProtectedPathsRefused(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, ".tether")
	p := ProtectedPaths(data, home)
	for _, f := range []string{
		filepath.Join(home, ".ssh/authorized_keys"),
		filepath.Join(home, ".ssh"),
		filepath.Join(data, ".env"),
		filepath.Join(data, "config.yaml"),
		filepath.Join(data, "state.db"),
		filepath.Join(data, "state.db-wal"),
		filepath.Join(data, "state.db-shm"),
		filepath.Join(data, "skills/x/SKILL.md"),
		filepath.Join(data, "scripts"),
		filepath.Join(data, "scripts/a.sh"),
	} {
		err := CheckWritable(f, p, false)
		if err == nil {
			t.Fatalf("%s must be refused", f)
		}
		want := "refused: " + f + " is a protected path (see SECURITY.md)"
		if err.Error() != want {
			t.Fatalf("got %q, want %q", err, want)
		}
	}
	if err := CheckWritable(filepath.Join(data, "skills/x/SKILL.md"), p, true); err != nil {
		t.Fatalf("skill_manage may write skills/: %v", err)
	}
	if err := CheckWritable(filepath.Join(data, ".env"), p, true); err == nil {
		t.Fatal("allowSkills must not open .env")
	}
	if err := CheckWritable(filepath.Join(data, "scripts/a.sh"), p, true); err == nil {
		t.Fatal("allowSkills must not open scripts/")
	}
	for _, f := range []string{
		filepath.Join(data, "scripts-old/a.sh"),
		filepath.Join(data, "memory/memory.md"),
		filepath.Join(data, "memory.md"),
		filepath.Join(data, "skills-old/x"),
		filepath.Join(home, ".sshx"),
		filepath.Join(data, "config.yaml.bak"),
	} {
		if err := CheckWritable(f, p, false); err != nil {
			t.Fatalf("%s must be writable: %v", f, err)
		}
	}
}

func TestProtectedPathsSkipsEmptyRoots(t *testing.T) {
	if got := ProtectedPaths("", ""); len(got) != 0 {
		t.Fatalf("got %v, want no paths", got)
	}
	if got := ProtectedPaths("", "/home/d"); len(got) != 1 || got[0] != "/home/d/.ssh/" {
		t.Fatalf("got %v", got)
	}
}

func TestDotDotEscapeRefused(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, ".tether")
	p := ProtectedPaths(data, home)
	f := filepath.Join(data, "scripts", "..", ".env")
	if err := CheckWritable(f, p, false); err == nil {
		t.Fatal("a path with .. into .env must be refused")
	}
}

func TestSymlinkEscapeRefused(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, ".tether")
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatal(err)
	}
	p := ProtectedPaths(data, home)
	other := t.TempDir()

	// A folder link: other/link -> ~/.ssh; a new file in it is refused.
	dirLink := filepath.Join(other, "link")
	if err := os.Symlink(ssh, dirLink); err != nil {
		t.Fatal(err)
	}
	if err := CheckWritable(filepath.Join(dirLink, "authorized_keys"), p, false); err == nil {
		t.Fatal("a write through a folder link into ~/.ssh must be refused")
	}

	// A file link: other/keys -> ~/.ssh/authorized_keys (the target exists).
	keys := filepath.Join(ssh, "authorized_keys")
	if err := os.WriteFile(keys, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fileLink := filepath.Join(other, "keys")
	if err := os.Symlink(keys, fileLink); err != nil {
		t.Fatal(err)
	}
	if err := CheckWritable(fileLink, p, false); err == nil {
		t.Fatal("a write through a file link to ~/.ssh must be refused")
	}

	// A dangling file link: other/dangling -> ~/.ssh/new (no target yet).
	dangling := filepath.Join(other, "dangling")
	if err := os.Symlink(filepath.Join(ssh, "new"), dangling); err != nil {
		t.Fatal(err)
	}
	if err := CheckWritable(dangling, p, false); err == nil {
		t.Fatal("a write through a dangling link into ~/.ssh must be refused")
	}

	// A normal file in another folder is writable.
	if err := CheckWritable(filepath.Join(other, "ok.txt"), p, false); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedHomeIsLink(t *testing.T) {
	// The configured home is a link to the real folder; a write by the
	// real path must still be refused.
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	p := ProtectedPaths(filepath.Join(link, ".tether"), link)
	if err := os.MkdirAll(filepath.Join(real, ".tether"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CheckWritable(filepath.Join(real, ".tether", ".env"), p, false); err == nil {
		t.Fatal("the real path of a protected file must be refused")
	}
}

func TestShellWriteToProtectedIsDangerous(t *testing.T) {
	t.Cleanup(func() { AddProtectedPathPatterns(nil) })
	AddProtectedPathPatterns(ProtectedPaths("/home/d/.tether", "/home/d"))
	for _, c := range []string{
		"echo x >> /home/d/.ssh/authorized_keys",
		"tee /home/d/.tether/config.yaml < x",
		"cp /tmp/a /home/d/.tether/state.db-wal",
		"sed -i s/a/b/ /home/d/.tether/skills/x/SKILL.md",
		"truncate -s 0 /home/d/.tether/state.db",
		"cat x > /home/d/.tether/scripts/x.sh",
		"cp /tmp/x.sh /home/d/.tether/scripts/",
	} {
		if _, ok := MatchDangerous(NormalizeCommand(c)); !ok {
			t.Errorf("%q must be dangerous", c)
		}
	}
	if p, ok := MatchDangerous("echo x > /home/d/.tether/notes.txt"); ok {
		t.Fatalf("a write to a normal file matched %s", p.ID)
	}
}

func TestCheckWritableErrorNamesSecurityDoc(t *testing.T) {
	err := CheckWritable("/home/d/.ssh/id_rsa", []string{"/home/d/.ssh/"}, false)
	if err == nil || !strings.Contains(err.Error(), "protected path (see SECURITY.md)") {
		t.Fatal(err)
	}
}

func TestProtectedPathsBackupsAndDBLock(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, ".tether")
	p := ProtectedPaths(data, home)
	for _, f := range []string{
		filepath.Join(data, "backups"),
		filepath.Join(data, "backups/tether-backup-20261028-174210.tar.gz"),
		filepath.Join(data, "state.db.lock"),
	} {
		if err := CheckWritable(f, p, true); !errors.Is(err, ErrProtected) {
			t.Fatalf("%s: %v", f, err)
		}
	}
	for _, f := range []string{
		filepath.Join(data, "pre-restore-20261029-091500/state.db"),
		filepath.Join(data, "backups-old/x"),
	} {
		if err := CheckWritable(f, p, false); err != nil {
			t.Fatalf("%s must be writable: %v", f, err)
		}
	}
}
