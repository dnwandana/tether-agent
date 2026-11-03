package backup

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
)

// write creates dir/rel with the given content and makes the parent folders.
func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeTmp writes content to a new file in a temp folder and returns its path.
func writeTmp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "in")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// readFile returns the content of path as a string.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// rels returns the Rel of each item, in order.
func rels(items []item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Rel)
	}
	return out
}

// find returns the item with the given Rel, or a zero item.
func find(items []item, rel string) item {
	for _, it := range items {
		if it.Rel == rel {
			return it
		}
	}
	return item{}
}

func TestCollectItemsAllowList(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".env", "K=v")
	write(t, dir, ".history", "x")
	write(t, dir, "logs/tether.log", "x")
	write(t, dir, "cache/a", "x")
	write(t, dir, "exports/s.jsonl", "x")
	write(t, dir, "backups/old.tar.gz", "x")
	write(t, dir, "memory/.memory.lock", "")
	write(t, dir, "memory/memory.md", "m")
	write(t, dir, "skills/web/SKILL.md", "s")
	write(t, dir, "scripts/run.sh", "#!/bin/sh")
	if err := os.Chmod(filepath.Join(dir, "scripts/run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	items, skipped, err := collectItems(dir)
	if err != nil || len(skipped) != 0 {
		t.Fatal(err, skipped)
	}
	want := []string{"memory", "memory/memory.md", "scripts", "scripts/run.sh", "skills", "skills/web", "skills/web/SKILL.md"}
	if got := rels(items); !slices.Equal(got, want) {
		t.Fatal(got)
	}
	if m := find(items, "scripts/run.sh").Mode.Perm(); m != 0o755 {
		t.Fatal(m)
	}
	if it := find(items, "skills/web"); !it.Dir {
		t.Fatal("skills/web is not a folder item")
	}
	if it := find(items, "memory/memory.md"); it.Dir || it.Src != filepath.Join(dir, "memory", "memory.md") {
		t.Fatal(it)
	}
}

func TestCollectItemsSkipsSymlink(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "skills/a/SKILL.md", "s")
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "skills/a/link")); err != nil {
		t.Fatal(err)
	}
	items, skipped, err := collectItems(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(skipped, []string{"skills/a/link"}) || slices.Contains(rels(items), "skills/a/link") {
		t.Fatal(skipped)
	}
}

func TestCollectItemsSkipsSymlinkedFolder(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	write(t, outside, "secret.txt", "x")
	write(t, dir, "scripts/run.sh", "x")
	if err := os.Symlink(outside, filepath.Join(dir, "scripts/out")); err != nil {
		t.Fatal(err)
	}
	items, skipped, err := collectItems(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(skipped, []string{"scripts/out"}) {
		t.Fatal(skipped)
	}
	if got := rels(items); !slices.Equal(got, []string{"scripts", "scripts/run.sh"}) {
		t.Fatal(got)
	}
}

func TestCollectItemsSymlinkedTopFolder(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	write(t, outside, "SKILL.md", "x")
	if err := os.Symlink(outside, filepath.Join(dir, "skills")); err != nil {
		t.Fatal(err)
	}
	items, skipped, err := collectItems(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 || !slices.Equal(skipped, []string{"skills"}) {
		t.Fatal(rels(items), skipped)
	}
}

func TestCollectItemsFreshInstall(t *testing.T) {
	items, skipped, err := collectItems(t.TempDir())
	if err != nil || len(items) != 0 || len(skipped) != 0 {
		t.Fatal(items, skipped, err)
	}
}

func TestMaskTextFile(t *testing.T) {
	r := security.NewRedactor([]string{"supersecret-123456789"})
	src := writeTmp(t, "token=supersecret-123456789\n")
	dst := filepath.Join(t.TempDir(), "out")
	changed, err := maskTextFile(src, dst, r)
	if err != nil || !changed || readFile(t, dst) != "token="+security.Mask("supersecret-123456789")+"\n" {
		t.Fatal(changed, err)
	}
	if fi, _ := os.Stat(dst); fi.Mode().Perm() != 0o600 {
		t.Fatal(fi.Mode())
	}
}

func TestMaskTextFileNoMatch(t *testing.T) {
	r := security.NewRedactor([]string{"supersecret-123456789"})
	src := writeTmp(t, "plain text\n")
	dst := filepath.Join(t.TempDir(), "out")
	changed, err := maskTextFile(src, dst, r)
	if err != nil || changed || readFile(t, dst) != "plain text\n" {
		t.Fatal(changed, err)
	}
}

func TestMaskTextFileBinary(t *testing.T) {
	r := security.NewRedactor([]string{"supersecret-123456789"})
	in := "\x00\x01supersecret-123456789"
	src := writeTmp(t, in)
	dst := filepath.Join(t.TempDir(), "out")
	changed, err := maskTextFile(src, dst, r)
	if err != nil || changed || readFile(t, dst) != in {
		t.Fatal("a binary file changed", err)
	}
}

func TestMaskTextFileRefusesSymlink(t *testing.T) {
	r := security.NewRedactor(nil)
	outside := writeTmp(t, "a file outside the data dir\n")
	src := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.Symlink(outside, src); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "out")
	if _, err := maskTextFile(src, dst, r); err == nil {
		t.Fatal("maskTextFile must refuse a symlink")
	}
	if _, err := os.Lstat(dst); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("maskTextFile wrote the copy of the symlink target:", err)
	}
}

func TestMaskTextFileRefusesFIFO(t *testing.T) {
	r := security.NewRedactor(nil)
	src := filepath.Join(t.TempDir(), "run.sh")
	if err := syscall.Mkfifo(src, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := maskTextFile(src, filepath.Join(t.TempDir(), "out"), r)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), src) {
			t.Fatalf("err = %v, want an error that names %s", err, src)
		}
	case <-time.After(2 * time.Second):
		// Open a writer, so the blocked open returns and the test ends.
		if w, err := os.OpenFile(src, os.O_WRONLY, 0); err == nil {
			w.Close()
		}
		t.Fatal("maskTextFile waits on a FIFO")
	}
}

func TestCollectMemoryNotes(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "memory/memory.md", "m")
	write(t, dir, "memory/user_profile.md", "u")
	write(t, dir, "memory/notes/rpc.md", "r")
	write(t, dir, "memory/notes/inbox-user.md", "i")
	items, skipped, err := collectItems(dir)
	if err != nil || len(skipped) != 0 {
		t.Fatal(err, skipped)
	}
	want := []string{"memory", "memory/memory.md", "memory/user_profile.md",
		"memory/notes", "memory/notes/inbox-user.md", "memory/notes/rpc.md"}
	if got := rels(items); !slices.Equal(got, want) {
		t.Fatal(got)
	}
	if it := find(items, "memory/notes"); !it.Dir {
		t.Fatal("memory/notes is not a folder item")
	}
	if it := find(items, "memory/notes/rpc.md"); it.Dir || it.Src != filepath.Join(dir, "memory", "notes", "rpc.md") {
		t.Fatal(it)
	}
}

func TestCollectMemoryNotesOnly(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "memory/notes/rpc.md", "r")
	items, skipped, err := collectItems(dir)
	if err != nil || len(skipped) != 0 {
		t.Fatal(err, skipped)
	}
	if got := rels(items); !slices.Equal(got, []string{"memory", "memory/notes", "memory/notes/rpc.md"}) {
		t.Fatal(got)
	}
}

func TestCollectMemoryNotesSkipped(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "memory/memory.md", "m")
	write(t, dir, "memory/notes/rpc.md", "r")
	write(t, dir, "memory/notes/a.txt", "x")
	write(t, dir, "memory/notes/RPC.md", "x")
	write(t, dir, "memory/notes/sub/b.md", "x")
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "memory/notes/link.md")); err != nil {
		t.Fatal(err)
	}
	items, skipped, err := collectItems(dir)
	if err != nil {
		t.Fatal(err)
	}
	wantSkipped := []string{"memory/notes/RPC.md", "memory/notes/a.txt", "memory/notes/link.md", "memory/notes/sub"}
	if !slices.Equal(skipped, wantSkipped) {
		t.Fatal(skipped)
	}
	if got := rels(items); !slices.Equal(got, []string{"memory", "memory/memory.md", "memory/notes", "memory/notes/rpc.md"}) {
		t.Fatal(got)
	}
}

func TestCollectMemoryNotesNotFolder(t *testing.T) {
	for _, kind := range []string{"symlink", "file"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, "memory/memory.md", "m")
			if kind == "symlink" {
				outside := t.TempDir()
				write(t, outside, "rpc.md", "r")
				if err := os.Symlink(outside, filepath.Join(dir, "memory/notes")); err != nil {
					t.Fatal(err)
				}
			} else {
				write(t, dir, "memory/notes", "x")
			}
			items, skipped, err := collectItems(dir)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(skipped, []string{"memory/notes"}) {
				t.Fatal(skipped)
			}
			if got := rels(items); !slices.Equal(got, []string{"memory", "memory/memory.md"}) {
				t.Fatal(got)
			}
		})
	}
}
