package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var ownerOrigin = Origin{Source: "owner"}

func undoManager(t *testing.T) (*Manager, *memCopies) {
	t.Helper()
	rec := &memCopies{}
	m := newManager(t, false)
	m.Copies = rec
	if _, err := m.Create(Origin{Source: "chat"}, "deploy", deployMD); err != nil {
		t.Fatal(err)
	}
	return m, rec
}

func undoText(t *testing.T, m *Manager) string {
	t.Helper()
	return readSkill(t, m, "deploy/SKILL.md")
}

func TestUndoPatchRestoresTheFolder(t *testing.T) {
	m, rec := undoManager(t)
	if _, err := m.Patch(Origin{Source: "chat"}, "deploy", "1. build", "1. make"); err != nil {
		t.Fatal(err)
	}
	undo, err := m.UndoCopy(ownerOrigin, 2, "ab12")
	if err != nil {
		t.Fatal(err)
	}
	if undoText(t, m) != deployMD {
		t.Fatalf("text: %q", undoText(t, m))
	}
	if undo.Action != "patch" || undo.Source != "owner" {
		t.Fatalf("row: %+v", undo)
	}
	b, err := os.ReadFile(filepath.Join(m.Dir, ".history", fmt.Sprint(undo.ID), "SKILL.md"))
	if err != nil || !strings.Contains(string(b), "1. make") {
		t.Fatalf("copy: %v %q", err, b)
	}
	if rec.row(2).UndoneBy != undo.ID {
		t.Fatalf("UndoneBy = %d", rec.row(2).UndoneBy)
	}
}

func TestUndoAfterANewerChangeIsRefused(t *testing.T) {
	m, _ := undoManager(t)
	m.Patch(Origin{Source: "chat"}, "deploy", "1. build", "1. make")
	m.Patch(Origin{Source: "chat"}, "deploy", "1. make", "1. ship")
	if _, err := m.UndoCopy(ownerOrigin, 2, "ab12"); !errors.Is(err, ErrCopyChanged) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(undoText(t, m), "1. ship") {
		t.Fatal("the text changed")
	}
}

func TestUndoStampAndTwice(t *testing.T) {
	m, _ := undoManager(t)
	m.Patch(Origin{Source: "chat"}, "deploy", "1. build", "1. make")
	if _, err := m.UndoCopy(ownerOrigin, 2, "ffff"); !errors.Is(err, ErrCopyStamp) {
		t.Fatalf("err = %v", err)
	}
	if _, err := m.UndoCopy(ownerOrigin, 2, "ab12"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UndoCopy(ownerOrigin, 2, "ab12"); !errors.Is(err, ErrCopyUndone) {
		t.Fatalf("err = %v", err)
	}
}

func TestUndoCreateRemovesTheFolder(t *testing.T) {
	m, _ := undoManager(t)
	undo, err := m.UndoCopy(ownerOrigin, 1, "ab12")
	if err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(m.Dir, "deploy")) {
		t.Fatal("the folder stays")
	}
	if undo.Action != "delete" || !exists(filepath.Join(m.Dir, ".history", fmt.Sprint(undo.ID), "SKILL.md")) {
		t.Fatalf("row: %+v", undo)
	}
	if _, err := m.RedoCopy(ownerOrigin, 1, "ab12"); err != nil {
		t.Fatal(err)
	}
	if undoText(t, m) != deployMD {
		t.Fatal("the content differs")
	}
}

func TestUndoDeleteRestores(t *testing.T) {
	m, _ := undoManager(t)
	o := Origin{Source: "chat"}
	if _, err := m.WriteFile(o, "deploy", "references/env.md", "env"); err != nil {
		t.Fatal(err)
	}
	c, err := m.Delete(o, "deploy")
	if err != nil {
		t.Fatal(err)
	}
	undo, err := m.UndoCopy(ownerOrigin, c.ID, "ab12")
	if err != nil {
		t.Fatal(err)
	}
	if undo.Action != "create" || readSkill(t, m, "deploy/references/env.md") != "env" || undoText(t, m) != deployMD {
		t.Fatalf("restore: %+v", undo)
	}
	// A second delete, then a new skill with the name.
	c, err = m.Delete(o, "deploy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(o, "deploy", deployMD); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UndoCopy(ownerOrigin, c.ID, "ab12"); !errors.Is(err, ErrCopyChanged) {
		t.Fatalf("err = %v", err)
	}
	if undoText(t, m) != deployMD || exists(filepath.Join(m.Dir, "deploy", "references")) {
		t.Fatal("the undo changed files")
	}
}

func TestUndoKeepsTheNestedSkill(t *testing.T) {
	m, _ := undoManager(t)
	sub := filepath.Join(m.Dir, "deploy", "sub")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "SKILL.md"), []byte("---\nname: sub\ndescription: s. Use when s.\n---\nbody\n"), 0o644)
	res, err := m.Patch(Origin{Source: "chat"}, "deploy", "1. build", "1. make")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.UndoCopy(ownerOrigin, res.CopyID, "ab12"); err != nil {
		t.Fatal(err)
	}
	if undoText(t, m) != deployMD || !exists(filepath.Join(sub, "SKILL.md")) {
		t.Fatal("the swap lost a file")
	}
	ents, _ := os.ReadDir(filepath.Join(m.Dir))
	for _, e := range ents {
		if strings.Contains(e.Name(), ".undo-") {
			t.Fatalf("leftover %s", e.Name())
		}
	}
}

func TestUndoRedoUndo(t *testing.T) {
	m, _ := undoManager(t)
	m.Patch(Origin{Source: "chat"}, "deploy", "1. build", "1. make")
	patched := undoText(t, m)
	if _, err := m.UndoCopy(ownerOrigin, 2, "ab12"); err != nil || undoText(t, m) != deployMD {
		t.Fatalf("undo: %v", err)
	}
	if _, err := m.RedoCopy(ownerOrigin, 2, "ab12"); err != nil || undoText(t, m) != patched {
		t.Fatalf("redo: %v", err)
	}
	if _, err := m.UndoCopy(ownerOrigin, 2, "ab12"); err != nil || undoText(t, m) != deployMD {
		t.Fatalf("undo again: %v", err)
	}
}

func TestRedoOfARowThatIsNotUndone(t *testing.T) {
	m, _ := undoManager(t)
	if _, err := m.RedoCopy(ownerOrigin, 1, "ab12"); err == nil || !strings.Contains(err.Error(), "is not undone") {
		t.Fatalf("err = %v", err)
	}
}

func TestUndoTakesTheSkillsLock(t *testing.T) {
	m, _ := undoManager(t)
	m.Patch(Origin{Source: "chat"}, "deploy", "1. build", "1. make")
	unlock, err := LockSkills(m.Dir)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := m.UndoCopy(ownerOrigin, 2, "ab12"); done <- err }()
	select {
	case <-done:
		t.Fatal("the undo ran under the lock")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the undo did not finish")
	}
}

func TestUndoWithNoRecorder(t *testing.T) {
	m := newManager(t, false)
	if _, err := m.UndoCopy(ownerOrigin, 1, "ab12"); err == nil || !strings.Contains(err.Error(), "no copy history") {
		t.Fatalf("err = %v", err)
	}
}
