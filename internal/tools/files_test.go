package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// writeLines writes a file with n lines "line 1" to "line n" and returns its path.
func writeLines(t *testing.T, n int) string {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return writeFile(t, b.String())
}

// writeFile writes content to a new file and returns its path.
func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadNumberedWithNextOffset(t *testing.T) {
	env := newEnv(t)
	p := writeLines(t, 2500)
	out := runToolEnv(t, ReadFileTool{}, fmt.Sprintf(`{"path":%q}`, p), env)
	if !strings.HasPrefix(out, "     1\tline 1\n") || !strings.Contains(out, "next_offset: 2001") {
		t.Fatal(out[:80])
	}
	if strings.Contains(out, "line 2001\n") {
		t.Fatal("the default limit must be 2000 lines")
	}
}

func TestReadOffsetAndLimit(t *testing.T) {
	p := writeLines(t, 10)
	out := runTool(t, ReadFileTool{}, fmt.Sprintf(`{"path":%q,"offset":4,"limit":2}`, p))
	want := "     4\tline 4\n     5\tline 5\n"
	if !strings.HasPrefix(out, want) || !strings.Contains(out, "next_offset: 6") {
		t.Fatalf("%q", out)
	}
	out = runTool(t, ReadFileTool{}, fmt.Sprintf(`{"path":%q,"offset":9}`, p))
	if out != "     9\tline 9\n    10\tline 10\n" {
		t.Fatalf("%q", out)
	}
}

func TestReadOffsetPastEnd(t *testing.T) {
	p := writeLines(t, 3)
	out := runToolErr(t, ReadFileTool{}, fmt.Sprintf(`{"path":%q,"offset":10}`, p))
	if !strings.Contains(out, "3 lines") {
		t.Fatal(out)
	}
}

func TestReadLongLineCapped(t *testing.T) {
	p := writeFile(t, strings.Repeat("é", 5000)+"\nshort\n")
	out := runTool(t, ReadFileTool{}, fmt.Sprintf(`{"path":%q}`, p))
	first, _, _ := strings.Cut(out, "\n")
	want := "     1\t" + strings.Repeat("é", 2000) + " …[line truncated]"
	if first != want || !strings.Contains(out, "     2\tshort") {
		t.Fatalf("%d runes", utf8.RuneCountInString(first))
	}
}

func TestReadRelativePath(t *testing.T) {
	env := newEnv(t)
	if err := os.WriteFile(filepath.Join(env.WorkDir, "a.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runToolEnv(t, ReadFileTool{}, `{"path":"a.txt"}`, env)
	if out != "     1\thi\n" {
		t.Fatalf("%q", out)
	}
}

func TestReadBinaryRefused(t *testing.T) {
	p := writeFile(t, "\x7fELF\x02\x01\x01\x00\x00\x00binary")
	out := runToolErr(t, ReadFileTool{}, fmt.Sprintf(`{"path":%q}`, p))
	if !strings.Contains(out, "binary file; use run_command with xxd or file") {
		t.Fatal(out)
	}
}

func TestReadInvalidUTF8IsSanitized(t *testing.T) {
	p := writeFile(t, "caf\xe9 latin-1\n")
	out := runTool(t, ReadFileTool{}, fmt.Sprintf(`{"path":%q}`, p))
	if !utf8.ValidString(out) || !strings.Contains(out, "caf� latin-1") {
		t.Fatalf("%q", out)
	}
}

func TestReadMissingFile(t *testing.T) {
	out := runToolErr(t, ReadFileTool{}, `{"path":"/no/such/file.txt"}`)
	if !strings.Contains(out, "/no/such/file.txt") || !strings.Contains(out, "does not exist") {
		t.Fatal(out)
	}
}

func TestReadEmptyFile(t *testing.T) {
	p := writeFile(t, "")
	out := runTool(t, ReadFileTool{}, fmt.Sprintf(`{"path":%q}`, p))
	if !strings.Contains(out, "empty") {
		t.Fatal(out)
	}
}

func TestWriteNewFileNoReadNeeded(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "dir", "new.txt")
	out := runTool(t, WriteFileTool{}, fmt.Sprintf(`{"path":%q,"content":"hello"}`, p))
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "hello" || out != "wrote 5 bytes to "+p {
		t.Fatal(out, err)
	}
}

func TestWriteExistingNeedsRead(t *testing.T) {
	env := newEnv(t)
	p := writeLines(t, 3)
	out := runToolEnvErr(t, WriteFileTool{}, fmt.Sprintf(`{"path":%q,"content":"x"}`, p), env)
	if !strings.Contains(out, "read_file first") {
		t.Fatal(out)
	}
}

func TestWriteAfterReadThenWriteAgain(t *testing.T) {
	env := newEnv(t)
	p := writeLines(t, 3)
	runToolEnv(t, ReadFileTool{}, fmt.Sprintf(`{"path":%q}`, p), env)
	runToolEnv(t, WriteFileTool{}, fmt.Sprintf(`{"path":%q,"content":"one"}`, p), env)
	// The write updates the tracker, so a second write needs no new read.
	runToolEnv(t, WriteFileTool{}, fmt.Sprintf(`{"path":%q,"content":"two"}`, p), env)
	if b, _ := os.ReadFile(p); string(b) != "two" {
		t.Fatal(string(b))
	}
}

func TestWriteRefusedWhenChangedAfterRead(t *testing.T) {
	env := newEnv(t)
	p := writeLines(t, 3)
	runToolEnv(t, ReadFileTool{}, fmt.Sprintf(`{"path":%q}`, p), env)
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(p, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runToolEnvErr(t, WriteFileTool{}, fmt.Sprintf(`{"path":%q,"content":"x"}`, p), env)
	if !strings.Contains(out, "changed since you read it") {
		t.Fatal(out)
	}
}

func TestWriteRefusedWhenSameSizeButNewMtime(t *testing.T) {
	env := newEnv(t)
	p := writeFile(t, "aaaa")
	runToolEnv(t, ReadFileTool{}, fmt.Sprintf(`{"path":%q}`, p), env)
	if err := os.WriteFile(p, []byte("bbbb"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(5 * time.Second)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
	out := runToolEnvErr(t, WriteFileTool{}, fmt.Sprintf(`{"path":%q,"content":"x"}`, p), env)
	if !strings.Contains(out, "changed since you read it") {
		t.Fatal(out)
	}
}

func TestWriteProtectedPath(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.txt")
	tool := WriteFileTool{Protected: func(string) error { return errors.New("protected path") }}
	out := runToolErr(t, tool, fmt.Sprintf(`{"path":%q,"content":"x"}`, p))
	if !strings.Contains(out, "protected path") {
		t.Fatal(out)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("a protected path must not be written")
	}
}

func TestWriteKeepsModeAndSymlink(t *testing.T) {
	env := newEnv(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "target.sh")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.sh")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	runToolEnv(t, ReadFileTool{}, fmt.Sprintf(`{"path":%q}`, link), env)
	runToolEnv(t, WriteFileTool{}, fmt.Sprintf(`{"path":%q,"content":"new"}`, link), env)
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink must stay a symlink")
	}
	fi, _ := os.Stat(target)
	b, _ := os.ReadFile(target)
	if string(b) != "new" || fi.Mode().Perm() != 0o755 {
		t.Fatal(string(b), fi.Mode())
	}
}

func TestWriteFileRefusesProtected(t *testing.T) {
	env := newEnv(t)
	env.DataDir = t.TempDir()
	p := filepath.Join(env.DataDir, ".env")
	out := runToolEnvErr(t, WriteFileTool{}, fmt.Sprintf(`{"path":%q,"content":"x"}`, p), env)
	if !strings.Contains(out, "refused: "+p+" is a protected path (see SECURITY.md)") {
		t.Fatal(out)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("a protected path must not be written")
	}
	// A cron script job runs with no approval, so only the user can place a script.
	script := filepath.Join(env.DataDir, "scripts", "a.sh")
	out = runToolEnvErr(t, WriteFileTool{}, fmt.Sprintf(`{"path":%q,"content":"x"}`, script), env)
	if !strings.Contains(out, "is a protected path") {
		t.Fatal(out)
	}
	ok := filepath.Join(env.DataDir, "memory", "notes.md")
	runToolEnv(t, WriteFileTool{}, fmt.Sprintf(`{"path":%q,"content":"x"}`, ok), env)
}

func TestWriteFileRefusesSymlinkIntoSkills(t *testing.T) {
	env := newEnv(t)
	env.DataDir = t.TempDir()
	skills := filepath.Join(env.DataDir, "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(env.WorkDir, "s")
	if err := os.Symlink(skills, link); err != nil {
		t.Fatal(err)
	}
	out := runToolEnvErr(t, WriteFileTool{}, `{"path":"s/x/SKILL.md","content":"x"}`, env)
	if !strings.Contains(out, "protected path") {
		t.Fatal(out)
	}
}
