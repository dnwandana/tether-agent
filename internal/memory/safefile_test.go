package memory

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTier1SymlinkIsNotRead(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("Injected rule from outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, memoryFile)); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, 2200, 1375)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Memory(); got != "" {
		t.Fatalf("a symlinked memory.md must not reach the prompt: %q", got)
	}
	if out, _ := s.Search("Injected"); strings.Contains(out, "outside") {
		t.Fatal(out)
	}
	_, err = s.Apply(Op{Action: "add", Target: TargetMemory, Content: "fact"})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatal(err)
	}
}

func TestNoteFIFODoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "notes", "pipe.md"), 0o600); err != nil {
		t.Skip("no FIFO support:", err)
	}
	s, err := Open(dir, 2200, 1375)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.Read("pipe", 1, 0)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO is not a note")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the read of a FIFO note blocks")
	}
}

func TestNotesFolderSymlinkIsRefused(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "x.md"), []byte("outside note"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "notes")); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, 2200, 1375)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.openNotes(); err != errNotesNotFolder {
		t.Fatal(err)
	}
	if _, err := s.Apply(Op{Action: "add", Target: TargetMemory, Topic: "x", Content: "fact"}); err == nil {
		t.Fatal("a write must not go through a symlinked notes folder")
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "x.md")); string(b) != "outside note" {
		t.Fatal("the outside file changed:", string(b))
	}
}
