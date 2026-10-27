package ctxmgr

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var markerRE = regexp.MustCompile(`\[… (\d+) chars omitted\. Full output: (\S+\.txt)\. Use read_file with offset to see more\.\]`)

func TestShortOutputUnchanged(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spill")
	out, err := Truncate("abc", 50000, dir)
	if err != nil || out != "abc" {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("a short output must not create a spill file")
	}
}

func TestExactlyMaxUnchanged(t *testing.T) {
	in := strings.Repeat("é", 100)
	out, err := Truncate(in, 100, t.TempDir())
	if err != nil || out != in {
		t.Fatal("max runes must stay unchanged", err)
	}
}

func TestHeadTailAndSpill(t *testing.T) {
	dir := t.TempDir()
	in := strings.Repeat("a", 30000) + strings.Repeat("m", 40000) + strings.Repeat("z", 30000)
	out, err := Truncate(in, 50000, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, strings.Repeat("a", 30000)+"\n") || !strings.HasSuffix(out, "\n"+strings.Repeat("z", 15000)) {
		t.Fatal("head/tail")
	}
	m := markerRE.FindStringSubmatch(out)
	if m == nil {
		t.Fatal("marker")
	}
	if m[1] != "55000" {
		t.Fatal("omitted count", m[1])
	}
	full, err := os.ReadFile(m[2])
	if err != nil || string(full) != in {
		t.Fatal("spill must hold everything", err)
	}
	if !strings.HasPrefix(m[2], dir) || !regexp.MustCompile(`^\d{8}-\d{6}[0-9a-f]{6}\.txt$`).MatchString(filepath.Base(m[2])) {
		t.Fatal("spill path", m[2])
	}
}

func TestSpillDirCreatedPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache", "spill")
	if _, err := Truncate(strings.Repeat("x", 200), 100, dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatal(fi, err)
	}
}

func TestSpillWriteFailureIsError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := Truncate(strings.Repeat("x", 200), 100, file)
	if err == nil || out != "" {
		t.Fatal("a failed spill write must return an error and no text", out)
	}
}

// Binary or invalid UTF-8 output gives valid UTF-8 that never splits a rune.
func TestInvalidUTF8NeverSplitsRunes(t *testing.T) {
	in := strings.Repeat("é", 40000) + string([]byte{0xff, 0xfe}) + strings.Repeat("ü", 40000)
	out, err := Truncate(in, 50000, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(out) {
		t.Fatal("output must be valid UTF-8")
	}
	b, _ := json.Marshal(out)
	if !json.Valid(b) {
		t.Fatal("json")
	}
	if !strings.HasPrefix(out, strings.Repeat("é", 30000)) || !strings.HasSuffix(out, strings.Repeat("ü", 15000)) {
		t.Fatal("head/tail counted in runes")
	}
}

func TestInvalidUTF8ShortOutputSanitized(t *testing.T) {
	out, err := Truncate("ok"+string([]byte{0xff})+"ok", 50000, t.TempDir())
	if err != nil || out != "ok�ok" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestSanitizeUTF8(t *testing.T) {
	if got := SanitizeUTF8("a\xffb\xc3"); got != "a�b�" || !utf8.ValidString(got) {
		t.Fatalf("%q", got)
	}
	if got := SanitizeUTF8("héllo"); got != "héllo" {
		t.Fatal(got)
	}
}

func TestCleanupSpillOlderThan7Days(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.txt")
	fresh := filepath.Join(dir, "new.txt")
	os.WriteFile(old, nil, 0o600)
	os.WriteFile(fresh, nil, 0o600)
	now := time.Now()
	os.Chtimes(old, now.Add(-8*24*time.Hour), now.Add(-8*24*time.Hour))
	n, err := CleanupSpill(dir, 7*24*time.Hour, now)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh kept")
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old removed")
	}
}

func TestCleanupSpillMissingDir(t *testing.T) {
	n, err := CleanupSpill(filepath.Join(t.TempDir(), "none"), time.Hour, time.Now())
	if err != nil || n != 0 {
		t.Fatal(n, err)
	}
}
