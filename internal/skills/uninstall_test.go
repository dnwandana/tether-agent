package skills

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installPDF installs a local safe skill "pdf" with Yes and returns the
// installer and its output.
func installPDF(t *testing.T, p *fakePrompt) (*Installer, *bytes.Buffer) {
	t.Helper()
	src := localSkill(t, t.TempDir(), "pdf", safeBody)
	in, out := newInstaller(t, p)
	in.FindGit = noGit
	if _, err := in.Install(context.Background(), InstallRequest{Source: src, Yes: true}); err != nil {
		t.Fatalf("Install: %v\n%s", err, out)
	}
	out.Reset()
	return in, out
}

func TestUninstallInstalledSkill(t *testing.T) {
	in, out := installPDF(t, &fakePrompt{})
	if err := in.Uninstall(context.Background(), UninstallRequest{Name: "pdf", Yes: true}); err != nil {
		t.Fatalf("Uninstall: %v\n%s", err, out)
	}
	if installed(t, in.SkillsDir, "pdf") {
		t.Fatal("skills/pdf exists after the uninstall")
	}
	l, err := ReadLock(in.SkillsDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Installed["pdf"]; ok {
		t.Fatal("the lockfile still lists pdf")
	}
	got := out.String()
	if !strings.Contains(got, "Removed pdf.\n") {
		t.Fatalf("output has no Removed line:\n%s", got)
	}
	if !strings.HasPrefix(got, "pdf — installed from ") || !strings.Contains(got, " on 2026-10-28\n") {
		t.Fatalf("output has no header line:\n%s", got)
	}
	if strings.Contains(got, "(commit") {
		t.Fatalf("header shows a commit for a local skill:\n%s", got)
	}
	if strings.Contains(got, "The agent changed") {
		t.Fatalf("output shows changes for an unchanged skill:\n%s", got)
	}
}

func TestUninstallNotInLock(t *testing.T) {
	in, _ := newInstaller(t, &fakePrompt{})
	err := in.Uninstall(context.Background(), UninstallRequest{Name: "tether-memory", Yes: true})
	want := `"tether-memory" is not in the lockfile. Only installed skills can be removed this way.`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestUninstallBadEntryDeletesNothing(t *testing.T) {
	in, out := newInstaller(t, &fakePrompt{})
	outside := filepath.Join(filepath.Dir(in.SkillsDir), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	err := UpdateLock(in.SkillsDir, func(l *LockFile) error {
		l.Installed["pdf"] = LockEntry{Source: "local", InstallPath: "../outside"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Uninstall(context.Background(), UninstallRequest{Name: "pdf", Yes: true}); err == nil {
		t.Fatal("Uninstall returned no error for a bad entry")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("the outside folder is gone: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("output for a bad entry:\n%s", out)
	}
	lockEntry(t, in.SkillsDir, "pdf")
}

func TestUninstallShowsAgentChanges(t *testing.T) {
	in, out := installPDF(t, &fakePrompt{})
	dir := filepath.Join(in.SkillsDir, "pdf")
	f, err := os.OpenFile(filepath.Join(dir, skillFile), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("More text.\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := in.Uninstall(context.Background(), UninstallRequest{Name: "pdf", Yes: true}); err != nil {
		t.Fatalf("Uninstall: %v\n%s", err, out)
	}
	got := out.String()
	if !strings.Contains(got, "The agent changed 1 file since install: notes.md\n") {
		t.Fatalf("output has no change line:\n%s", got)
	}
}

func TestUninstallShowsContentChange(t *testing.T) {
	in, out := installPDF(t, &fakePrompt{})
	if err := os.WriteFile(filepath.Join(in.SkillsDir, "pdf", skillFile), []byte(skillMD("pdf")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := in.Uninstall(context.Background(), UninstallRequest{Name: "pdf", Yes: true}); err != nil {
		t.Fatalf("Uninstall: %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "The agent changed the content of the files since install.\n") {
		t.Fatalf("output has no content line:\n%s", out)
	}
}

func TestUninstallChangeListLimit(t *testing.T) {
	in, out := installPDF(t, &fakePrompt{})
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		if err := os.WriteFile(filepath.Join(in.SkillsDir, "pdf", n+".txt"), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := in.Uninstall(context.Background(), UninstallRequest{Name: "pdf", Yes: true}); err != nil {
		t.Fatalf("Uninstall: %v\n%s", err, out)
	}
	want := "The agent changed 7 files since install: a.txt, b.txt, c.txt, d.txt, e.txt, and 2 more\n"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("output has no %q:\n%s", want, out)
	}
}

func TestUninstallCronWarning(t *testing.T) {
	in, out := installPDF(t, &fakePrompt{})
	var asked string
	req := UninstallRequest{Name: "pdf", Yes: true, CronUsers: func(name string) []string {
		asked = name
		return []string{`"morning-brief" (a1b2)`}
	}}
	if err := in.Uninstall(context.Background(), req); err != nil {
		t.Fatalf("Uninstall: %v\n%s", err, out)
	}
	if asked != "pdf" {
		t.Fatalf("CronUsers got %q, want pdf", asked)
	}
	want := `Cron job "morning-brief" (a1b2) uses this skill. Its runs will skip it.` + "\n"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("output has no cron line:\n%s", out)
	}
}

func TestUninstallFolderGone(t *testing.T) {
	in, out := installPDF(t, &fakePrompt{})
	if err := os.RemoveAll(filepath.Join(in.SkillsDir, "pdf")); err != nil {
		t.Fatal(err)
	}
	if err := in.Uninstall(context.Background(), UninstallRequest{Name: "pdf", Yes: true}); err != nil {
		t.Fatalf("Uninstall: %v\n%s", err, out)
	}
	l, err := ReadLock(in.SkillsDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Installed["pdf"]; ok {
		t.Fatal("the lockfile still lists pdf")
	}
	got := out.String()
	if !strings.Contains(got, "The folder skills/pdf was already gone.\n") || !strings.Contains(got, "Removed pdf.\n") {
		t.Fatalf("output:\n%s", got)
	}
}

func TestUninstallConfirm(t *testing.T) {
	p := &fakePrompt{term: true, confirm: false}
	in, out := installPDF(t, p)
	err := in.Uninstall(context.Background(), UninstallRequest{Name: "pdf"})
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("err = %v, want ErrCancelled", err)
	}
	if !strings.Contains(out.String(), "Cancelled.\n") {
		t.Fatalf("output has no Cancelled.:\n%s", out)
	}
	if !installed(t, in.SkillsDir, "pdf") {
		t.Fatal("skills/pdf is gone after a cancel")
	}
	lockEntry(t, in.SkillsDir, "pdf")

	p.term = false
	err = in.Uninstall(context.Background(), UninstallRequest{Name: "pdf"})
	want := "no terminal for the confirm prompt. Run the command again with --yes to confirm."
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if !installed(t, in.SkillsDir, "pdf") {
		t.Fatal("skills/pdf is gone with no terminal")
	}

	p.term, p.confirm, p.confirms = true, true, nil
	if err := in.Uninstall(context.Background(), UninstallRequest{Name: "pdf"}); err != nil {
		t.Fatalf("Uninstall: %v\n%s", err, out)
	}
	if installed(t, in.SkillsDir, "pdf") {
		t.Fatal("skills/pdf exists after a confirmed uninstall")
	}
	wantQ := "Remove " + filepath.Join(in.SkillsDir, "pdf") + "?"
	if len(p.confirms) != 1 || p.confirms[0] != wantQ {
		t.Fatalf("confirms = %q, want [%q]", p.confirms, wantQ)
	}
}

func TestUninstallPromptError(t *testing.T) {
	p := &fakePrompt{term: true, err: errors.New("interrupt")}
	in, _ := installPDF(t, p)
	if err := in.Uninstall(context.Background(), UninstallRequest{Name: "pdf"}); err == nil {
		t.Fatal("Uninstall returned no error")
	}
	if !installed(t, in.SkillsDir, "pdf") {
		t.Fatal("skills/pdf is gone after a prompt error")
	}
	lockEntry(t, in.SkillsDir, "pdf")
}

func TestChangedFiles(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{skillFile: skillMD("x"), "a.txt": "a"})
	hash, files, err := ContentHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := LockEntry{ContentHash: hash, Files: files}

	added, removed, changed, err := ChangedFiles(dir, e)
	if err != nil || added != nil || removed != nil || changed {
		t.Fatalf("unchanged: added=%q removed=%q changed=%v err=%v", added, removed, changed, err)
	}

	if err := os.Remove(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal(err)
	}
	writeTree(t, dir, map[string]string{"b.txt": "b"})
	added, removed, changed, err = ChangedFiles(dir, e)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(added, ",") != "b.txt" || strings.Join(removed, ",") != "a.txt" || changed {
		t.Fatalf("added=%q removed=%q changed=%v", added, removed, changed)
	}

	dir2 := t.TempDir()
	writeTree(t, dir2, map[string]string{skillFile: skillMD("x") + "edit\n", "a.txt": "a"})
	added, removed, changed, err = ChangedFiles(dir2, e)
	if err != nil {
		t.Fatal(err)
	}
	if added != nil || removed != nil || !changed {
		t.Fatalf("edited: added=%q removed=%q changed=%v", added, removed, changed)
	}

	if _, _, _, err := ChangedFiles(filepath.Join(dir, "missing"), e); err == nil {
		t.Fatal("ChangedFiles returned no error for a missing folder")
	}
}
