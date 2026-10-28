package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/backup"
	"github.com/dnwandana/tether-agent/internal/db"
)

// dataDirWithDB returns a temp data dir that holds a migrated state.db.
func dataDirWithDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestBackupCommand(t *testing.T) {
	dir := dataDirWithDB(t)
	var out, errb bytes.Buffer
	if code := run([]string{"backup", "--data-dir", dir}, &out, &errb); code != 0 {
		t.Fatal(code, errb.String())
	}
	s := out.String()
	for _, want := range []string{
		"Backup saved: " + filepath.Join(dir, "backups") + "/tether-backup-",
		"The archive holds your messages. Keep it private.",
		"The archive does not hold .env. Copy .env on its own when you move to a new host.",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
}

func TestBackupStdout(t *testing.T) {
	dir := dataDirWithDB(t)
	var out, errb bytes.Buffer
	if code := run([]string{"backup", "--data-dir", dir, "--out", "-"}, &out, &errb); code != 0 {
		t.Fatal(code, errb.String())
	}
	if !bytes.HasPrefix(out.Bytes(), []byte{0x1f, 0x8b}) || !strings.Contains(errb.String(), "Backup written to stdout (") {
		t.Fatal(errb.String())
	}
}

func TestBackupErrors(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"backup", "--data-dir", t.TempDir()}, &out, &errb); code != 1 ||
		!strings.HasPrefix(errb.String(), "backup: there is no state.db in ") {
		t.Fatal(code, errb.String())
	}
	errb.Reset()
	if code := run([]string{"backup", "extra"}, &out, &errb); code != 2 {
		t.Fatal(code)
	}
}

func TestPrintBackupReport(t *testing.T) {
	r := &backup.Result{
		Path: "/data/backups/tether-backup-20261028-174210.tar.gz",
		Size: 1363149,
		Manifest: backup.Manifest{
			Files: []backup.FileEntry{
				{Path: "state.db", Size: 5033165},
				{Path: "config.yaml", Size: 100},
				{Path: "memory/memory.md", Size: 10},
				{Path: "memory/user_profile.md", Size: 10},
				{Path: "skills/a/SKILL.md", Size: 10},
				{Path: "skills/a/ref.md", Size: 10},
				{Path: "skills/b/SKILL.md", Size: 10},
				{Path: "scripts/x.sh", Size: 10},
			},
			Redacted: backup.Redacted{StateDB: 2, Files: 1},
		},
		Stats:    backup.DBStats{Messages: 1204, Sessions: 38},
		Skipped:  []string{"skills/link"},
		Replaced: []backup.Replacement{{KeyPath: "providers.openai.api_key", Name: "OPENAI_API_KEY"}},
		Warnings: []string{"mcp.servers.search.url"},
	}
	var b bytes.Buffer
	printBackupReport(&b, r)
	want := `state.db      4.8 MB  (1,204 messages, 38 sessions)
memory/       2 files
skills/       2 skills, 3 files
scripts/      1 file
config.yaml   1 literal secret replaced
  providers.openai.api_key -> ${OPENAI_API_KEY}
  The restore needs these names in .env.
config.yaml   1 value looks like a secret and is in the archive
  mcp.servers.search.url
  Move the secret to .env, and use ${NAME} in config.yaml.
Masked 2 known secret values in state.db.
Masked the known secret values in 1 text file.
Skipped the link skills/link.
Backup saved: /data/backups/tether-backup-20261028-174210.tar.gz (1.3 MB)
The archive holds your messages. Keep it private.
The archive does not hold .env. Copy .env on its own when you move to a new host.
`
	if got := b.String(); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestPrintBackupReportEmpty(t *testing.T) {
	r := &backup.Result{
		Path:     "-",
		Size:     2048,
		Manifest: backup.Manifest{Files: []backup.FileEntry{{Path: "state.db", Size: 500}, {Path: "config.yaml", Size: 9}}},
	}
	var b bytes.Buffer
	printBackupReport(&b, r)
	want := `state.db      0.5 KB  (0 messages, 0 sessions)
config.yaml   saved with no change
Backup written to stdout (2.0 KB)
The archive holds your messages. Keep it private.
The archive does not hold .env. Copy .env on its own when you move to a new host.
`
	if got := b.String(); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}
