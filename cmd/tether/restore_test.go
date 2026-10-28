package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/backup"
)

// restoreHooks sets the stdin hooks of the restore command for one test.
func restoreHooks(t *testing.T, stdin io.Reader, terminal bool) {
	t.Helper()
	oldStdin, oldTerminal := restoreStdin, stdinIsTerminal
	t.Cleanup(func() { restoreStdin, stdinIsTerminal = oldStdin, oldTerminal })
	restoreStdin = stdin
	stdinIsTerminal = func() bool { return terminal }
}

// restoreUnsetEnv removes the names from the process environment for one test.
func restoreUnsetEnv(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		t.Setenv(n, "")
		if err := os.Unsetenv(n); err != nil {
			t.Fatal(err)
		}
	}
}

// restoreArchiveFile returns the path of an archive from tether backup. The config.yaml in
// the archive refers to OPENAI_API_KEY and TELEGRAM_BOT_TOKEN.
func restoreArchiveFile(t *testing.T) string {
	t.Helper()
	dir := dataDirWithDB(t)
	cfg := "providers:\n  openai:\n    api_key: ${OPENAI_API_KEY}\ntelegram:\n  bot_token: ${TELEGRAM_BOT_TOKEN}\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "tether-backup-20261028-174210.tar.gz")
	var out, errb bytes.Buffer
	if code := run([]string{"backup", "--data-dir", dir, "--out", path}, &out, &errb); code != 0 {
		t.Fatal(code, errb.String())
	}
	return path
}

// restoreHasPreRestore reports whether dir holds a pre-restore folder.
func restoreHasPreRestore(t *testing.T, dir string) bool {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "pre-restore-*"))
	if err != nil {
		t.Fatal(err)
	}
	return len(m) > 0
}

func TestRestoreStdinNeedsYes(t *testing.T) {
	restoreHooks(t, strings.NewReader(""), false)
	var out, errb bytes.Buffer
	if code := run([]string{"restore", "-"}, &out, &errb); code != 2 ||
		!strings.HasPrefix(errb.String(), `restore: "-" needs --yes`) {
		t.Fatal(code, errb.String())
	}
}

func TestRestoreNoTerminalNeedsYes(t *testing.T) {
	restoreHooks(t, strings.NewReader("restore\n"), false)
	var out, errb bytes.Buffer
	if code := run([]string{"restore", "--data-dir", t.TempDir(), "x.tar.gz"}, &out, &errb); code != 2 ||
		!strings.Contains(errb.String(), "--yes") {
		t.Fatal(code, errb.String())
	}
}

func TestRestoreNoFile(t *testing.T) {
	restoreHooks(t, strings.NewReader(""), true)
	var out, errb bytes.Buffer
	if code := run([]string{"restore"}, &out, &errb); code != 2 {
		t.Fatal(code, errb.String())
	}
	errb.Reset()
	if code := run([]string{"restore", "a", "b"}, &out, &errb); code != 2 {
		t.Fatal(code, errb.String())
	}
}

func TestRestoreAnswerNo(t *testing.T) {
	file := restoreArchiveFile(t)
	target := dataDirWithDB(t)
	restoreHooks(t, strings.NewReader("no\n"), true)
	var out, errb bytes.Buffer
	if code := run([]string{"restore", "--data-dir", target, file}, &out, &errb); code != 1 {
		t.Fatal(code, errb.String())
	}
	if got := errb.String(); got != "restore: stopped. Nothing changed\n" {
		t.Fatalf("stderr: %q", got)
	}
	if restoreHasPreRestore(t, target) {
		t.Fatal("a pre-restore folder exists after the answer no")
	}
}

func TestRestoreYes(t *testing.T) {
	restoreUnsetEnv(t, "OPENAI_API_KEY", "TELEGRAM_BOT_TOKEN")
	file := restoreArchiveFile(t)
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, ".env"), []byte("TELEGRAM_BOT_TOKEN=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restoreHooks(t, strings.NewReader(""), false)
	var out, errb bytes.Buffer
	if code := run([]string{"restore", "--yes", "--data-dir", target, file}, &out, &errb); code != 0 {
		t.Fatal(code, errb.String())
	}
	s := out.String()
	for _, want := range []string{
		"Restored from tether-backup-20261028-174210.tar.gz.\n",
		"config.yaml needs these variables in .env:\n",
		"  OPENAI_API_KEY        not set\n",
		"  TELEGRAM_BOT_TOKEN    set\n",
		"Start the gateway: systemctl --user start tether\n",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "Create .env") || strings.Contains(s, "pre-restore") {
		t.Fatalf("unexpected lines in:\n%s", s)
	}
	if !strings.HasSuffix(s, "Start the gateway: systemctl --user start tether\n") {
		t.Fatalf("the start line is not last:\n%s", s)
	}
	if _, err := os.Stat(filepath.Join(target, "state.db")); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreReportsPreRestore(t *testing.T) {
	file := restoreArchiveFile(t)
	target := dataDirWithDB(t)
	restoreHooks(t, strings.NewReader(""), false)
	var out, errb bytes.Buffer
	if code := run([]string{"restore", "--yes", "--data-dir", target, file}, &out, &errb); code != 0 {
		t.Fatal(code, errb.String())
	}
	m, err := filepath.Glob(filepath.Join(target, "pre-restore-*"))
	if err != nil || len(m) != 1 {
		t.Fatal(m, err)
	}
	s := out.String()
	for _, want := range []string{
		"The old data is in " + m[0] + "/.\n",
		"Delete that folder when you no longer need it.\n",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
}

func TestRestoreAllVarsSet(t *testing.T) {
	restoreUnsetEnv(t, "OPENAI_API_KEY", "TELEGRAM_BOT_TOKEN")
	file := restoreArchiveFile(t)
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, ".env"), []byte("TELEGRAM_BOT_TOKEN=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "from-env")
	restoreHooks(t, strings.NewReader(""), false)
	var out, errb bytes.Buffer
	if code := run([]string{"restore", "--yes", "--data-dir", target, file}, &out, &errb); code != 0 {
		t.Fatal(code, errb.String())
	}
	s := out.String()
	if !strings.Contains(s, "All variables in config.yaml are set.\n") || strings.Contains(s, "needs these variables") {
		t.Fatalf("got:\n%s", s)
	}
}

func TestPrintRestoreVarsNone(t *testing.T) {
	// No config.yaml, or no ${NAME} in it: there is nothing to report.
	var b bytes.Buffer
	printRestoreVars(&b, nil)
	if b.Len() != 0 {
		t.Fatalf("got %q, want no output", b.String())
	}
}

func TestRestoreAnswerYesFromStdin(t *testing.T) {
	file := restoreArchiveFile(t)
	target := t.TempDir()
	restoreHooks(t, strings.NewReader("restore\n"), true)
	var out, errb bytes.Buffer
	if code := run([]string{"restore", "--data-dir", target, file}, &out, &errb); code != 0 {
		t.Fatal(code, errb.String())
	}
	s := out.String()
	prompt := strings.Index(s, `Type "restore" to continue: `)
	done := strings.Index(s, "Restored from")
	if prompt < 0 || done < 0 || prompt > done {
		t.Fatalf("got:\n%s", s)
	}
	for _, want := range []string{"state.db", "config.yaml", target} {
		if !strings.Contains(s[:prompt], want) {
			t.Fatalf("the summary has no %q:\n%s", want, s)
		}
	}
}

func TestRestoreNoEnvFile(t *testing.T) {
	file := restoreArchiveFile(t)
	target := t.TempDir()
	restoreHooks(t, strings.NewReader(""), false)
	var out, errb bytes.Buffer
	if code := run([]string{"restore", "--yes", "--data-dir", target, file}, &out, &errb); code != 0 {
		t.Fatal(code, errb.String())
	}
	if s := out.String(); !strings.Contains(s, "Create .env, or run tether setup.\n") {
		t.Fatalf("got:\n%s", s)
	}
}

func TestRestoreReadsStdin(t *testing.T) {
	file := restoreArchiveFile(t)
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	restoreHooks(t, bytes.NewReader(b), false)
	var out, errb bytes.Buffer
	if code := run([]string{"restore", "--yes", "--data-dir", target, "-"}, &out, &errb); code != 0 {
		t.Fatal(code, errb.String())
	}
	if s := out.String(); !strings.Contains(s, "Restored from stdin.\n") {
		t.Fatalf("got:\n%s", s)
	}
}

func TestPrintRestoreSummary(t *testing.T) {
	s := backup.Summary{
		Manifest: backup.Manifest{
			TetherVersion: "v0.9.2",
			CreatedAt:     time.Date(2026, 10, 28, 17, 42, 10, 0, time.Local),
			SchemaVersion: 1,
			Files: []backup.FileEntry{
				{Path: "state.db"},
				{Path: "config.yaml"},
				{Path: "memory/memory.md"},
				{Path: "skills/a/SKILL.md"},
				{Path: "skills/a/ref.md"},
				{Path: "scripts/x.sh"},
			},
		},
		Stats:         backup.DBStats{Messages: 1204, Sessions: 38, CronJobs: 1},
		DataDir:       "/home/owner/.tether",
		PreRestoreDir: "/home/owner/.tether/pre-restore-20261029-091500",
	}
	var b bytes.Buffer
	printRestoreSummary(&b, s)
	want := `Backup from 2026-10-28 17:42:10, tether v0.9.2, schema 1.
state.db      1,204 messages, 38 sessions, 1 cron job
config.yaml   1 file
memory/       1 file
skills/       1 skill, 2 files
scripts/      1 file
The restore replaces the data in /home/owner/.tether.
If the data dir has old data, the old data moves to /home/owner/.tether/pre-restore-20261029-091500/.
`
	if got := b.String(); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRestoreMissingFile(t *testing.T) {
	restoreHooks(t, strings.NewReader(""), false)
	var out, errb bytes.Buffer
	missing := filepath.Join(t.TempDir(), "none.tar.gz")
	if code := run([]string{"restore", "--yes", "--data-dir", t.TempDir(), missing}, &out, &errb); code != 1 ||
		!strings.HasPrefix(errb.String(), "restore: ") {
		t.Fatal(code, errb.String())
	}
}
