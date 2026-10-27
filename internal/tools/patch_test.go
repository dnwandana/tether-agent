package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExactMatch(t *testing.T) {
	m, _ := FuzzyFind("a\nfoo()\nb\n", "foo()")
	if len(m) != 1 {
		t.Fatal(m)
	}
}

func TestIndentationDriftMatches(t *testing.T) {
	content := "func f() {\n\t\tif x {\n\t\t\treturn 1\n\t\t}\n}\n"
	m, err := FuzzyFind(content, "    if x {\n        return 1\n    }")
	if err != nil || len(m) != 1 {
		t.Fatal(m, err)
	}
	if got := content[m[0].Start:m[0].End]; got != "\t\tif x {\n\t\t\treturn 1\n\t\t}" {
		t.Fatalf("%q", got)
	}
}

func TestTrailingWhitespaceDriftMatches(t *testing.T) {
	m, err := FuzzyFind("a\nx = 1   \nb\n", "x = 1\n")
	if err != nil || len(m) != 1 {
		t.Fatal(m, err)
	}
	out, n, err := applyPatch("a\nx = 1   \nb\n", "x = 1\n", "x = 2\n", false)
	if err != nil || n != 1 || out != "a\nx = 2\nb\n" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestFuzzyNeverMatchesDifferentLineCount(t *testing.T) {
	// The blank line in the file must not be skipped.
	_, err := FuzzyFind("  a\n\n  b\n", "a\nb")
	if err == nil {
		t.Fatal("a window with a different number of non-blank lines must not match")
	}
	m, err := FuzzyFind("  a\n\n  b\n", "a\n\nb")
	if err != nil || len(m) != 1 {
		t.Fatal(m, err)
	}
}

func TestAmbiguousIsErrorAndFileUnchanged(t *testing.T) {
	env := newEnv(t)
	p := writeFile(t, "x()\ny\nx()\n")
	out := runToolEnvErr(t, PatchTool{}, fmt.Sprintf(`{"path":%q,"old_string":"x()","new_string":"z()"}`, p), env)
	b, _ := os.ReadFile(p)
	if !strings.Contains(out, "matches 2 places (lines 1, 3)") || string(b) != "x()\ny\nx()\n" {
		t.Fatal(out)
	}
}

func TestReplaceAllAndDiff(t *testing.T) {
	env := newEnv(t)
	p := writeFile(t, "x()\ny\nx()\n")
	out := runToolEnv(t, PatchTool{}, fmt.Sprintf(`{"path":%q,"old_string":"x()","new_string":"z()","replace_all":true}`, p), env)
	if !strings.Contains(out, "-x()") || !strings.Contains(out, "+z()") {
		t.Fatal(out)
	}
	if b, _ := os.ReadFile(p); string(b) != "z()\ny\nz()\n" {
		t.Fatal(string(b))
	}
}

func TestNoMatchShowsClosestLine(t *testing.T) {
	content := "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"
	_, err := FuzzyFind(content, "fmt.Printn(\"hello\")")
	if err == nil || !strings.Contains(err.Error(), "no match") || !strings.Contains(err.Error(), "line 4") {
		t.Fatal(err)
	}
}

func TestPatchMissingFile(t *testing.T) {
	out := runToolErr(t, PatchTool{}, `{"path":"/no/such/file.go","old_string":"a","new_string":"b"}`)
	if !strings.Contains(out, "does not exist") {
		t.Fatal(out)
	}
}

func TestPatchCountsAsReadAndWrite(t *testing.T) {
	env := newEnv(t)
	p := writeFile(t, "a\nb\n")
	runToolEnv(t, PatchTool{}, fmt.Sprintf(`{"path":%q,"old_string":"a","new_string":"c"}`, p), env)
	// write_file after a patch needs no new read_file.
	runToolEnv(t, WriteFileTool{}, fmt.Sprintf(`{"path":%q,"content":"z"}`, p), env)
}

func TestPatchProtectedPath(t *testing.T) {
	p := writeFile(t, "a\n")
	tool := PatchTool{Protected: func(string) error { return fmt.Errorf("protected path") }}
	out := runToolErr(t, tool, fmt.Sprintf(`{"path":%q,"old_string":"a","new_string":"b"}`, p))
	if b, _ := os.ReadFile(p); !strings.Contains(out, "protected path") || string(b) != "a\n" {
		t.Fatal(out)
	}
}

func TestPatchRefusesScriptsDir(t *testing.T) {
	env := newEnv(t)
	env.DataDir = t.TempDir()
	dir := filepath.Join(env.DataDir, "scripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "x.sh")
	if err := os.WriteFile(p, []byte("echo a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runToolEnv(t, ReadFileTool{}, fmt.Sprintf(`{"path":%q}`, p), env)
	out := runToolEnvErr(t, PatchTool{}, fmt.Sprintf(`{"path":%q,"old_string":"a","new_string":"b"}`, p), env)
	if b, _ := os.ReadFile(p); !strings.Contains(out, "is a protected path") || string(b) != "echo a\n" {
		t.Fatal(out)
	}
}

func TestUnifiedDiffContext(t *testing.T) {
	var before, after strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&before, "l%d\n", i)
		if i == 10 {
			after.WriteString("changed\n")
			continue
		}
		fmt.Fprintf(&after, "l%d\n", i)
	}
	d := UnifiedDiff("f.txt", before.String(), after.String())
	want := "--- a/f.txt\n+++ b/f.txt\n@@ -7,7 +7,7 @@\n l7\n l8\n l9\n-l10\n+changed\n l11\n l12\n l13\n"
	if d != want {
		t.Fatalf("got:\n%s\nwant:\n%s", d, want)
	}
}

func TestUnifiedDiffTwoHunks(t *testing.T) {
	var before, after strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&before, "l%d\n", i)
		switch i {
		case 2, 28:
			fmt.Fprintf(&after, "x%d\n", i)
		default:
			fmt.Fprintf(&after, "l%d\n", i)
		}
	}
	d := UnifiedDiff("f", before.String(), after.String())
	if strings.Count(d, "@@ -") != 2 || !strings.Contains(d, "@@ -1,5 +1,5 @@") || !strings.Contains(d, "@@ -25,6 +25,6 @@") {
		t.Fatal(d)
	}
}

func TestUnifiedDiffInsertAndDelete(t *testing.T) {
	d := UnifiedDiff("f", "a\nb\n", "a\nnew\nb\n")
	if d != "--- a/f\n+++ b/f\n@@ -1,2 +1,3 @@\n a\n+new\n b\n" {
		t.Fatalf("%q", d)
	}
	d = UnifiedDiff("f", "a\nb\n", "")
	if d != "--- a/f\n+++ b/f\n@@ -1,2 +0,0 @@\n-a\n-b\n" {
		t.Fatalf("%q", d)
	}
	if UnifiedDiff("f", "same\n", "same\n") != "" {
		t.Fatal("no change must give an empty diff")
	}
}

// The cases below are ported from Hermes tests/tools/test_fuzzy_match.py.
// Cases for the Hermes strategies that Tether Agent does not copy (escape
// normalization, Unicode folding, block anchor, context-aware similarity)
// are not ported.

func TestHermesExactSingleReplacement(t *testing.T) {
	out, n, err := applyPatch("hello world", "hello", "hi", false)
	if err != nil || n != 1 || out != "hi world" {
		t.Fatal(out, n, err)
	}
}

func TestHermesNoMatch(t *testing.T) {
	out, n, err := applyPatch("hello world", "xyz", "abc", false)
	if err == nil || n != 0 || out != "hello world" {
		t.Fatal(out, n, err)
	}
}

func TestHermesEmptyOldString(t *testing.T) {
	if _, n, err := applyPatch("abc", "", "x", false); err == nil || n != 0 {
		t.Fatal(n, err)
	}
}

func TestHermesIdenticalStrings(t *testing.T) {
	_, n, err := applyPatch("abc", "abc", "abc", false)
	if err == nil || n != 0 || !strings.Contains(err.Error(), "identical") {
		t.Fatal(n, err)
	}
}

func TestHermesMultilineExact(t *testing.T) {
	out, n, err := applyPatch("line1\nline2\nline3", "line1\nline2", "replaced", false)
	if err != nil || n != 1 || out != "replaced\nline3" {
		t.Fatal(out, n, err)
	}
}

func TestHermesExtraSpacesMatch(t *testing.T) {
	out, n, err := applyPatch("def  foo(  x,  y  ):", "def foo( x, y ):", "def bar(x, y):", false)
	if err != nil || n != 1 || !strings.Contains(out, "bar") {
		t.Fatal(out, n, err)
	}
}

func TestHermesDifferentIndentation(t *testing.T) {
	out, n, err := applyPatch("    def foo():\n        pass", "def foo():\n    pass", "def bar():\n    return 1", false)
	if err != nil || n != 1 || !strings.Contains(out, "bar") {
		t.Fatal(out, n, err)
	}
}

func TestHermesUnindentedInputReindentedToMatchFile(t *testing.T) {
	content := "class Calculator:\n    def add(self, a, b):\n        result = a + b\n        return result\n"
	out, n, err := applyPatch(content, "result = a + b\nreturn result", "result = a + b\nresult *= 2\nreturn result", false)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	for _, marker := range []string{"result = a + b", "result *= 2", "return result"} {
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, marker) {
				if indent := len(line) - len(strings.TrimLeft(line, " ")); indent != 8 {
					t.Fatalf("%q: indent %d, want 8", line, indent)
				}
			}
		}
	}
}

func TestHermesDedentAtStartAnchorsToFileBase(t *testing.T) {
	out, n, err := applyPatch("  return 1\n  return 2\n", "return 1\nreturn 2", "class X:\n  return 99\n  return 100", false)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	lines := strings.Split(out, "\n")
	if lines[0] != "  class X:" || lines[1] != "    return 99" || lines[2] != "    return 100" {
		t.Fatalf("%q", out)
	}
}

func TestHermesExactMatchNoReindent(t *testing.T) {
	out, _, err := applyPatch("    def foo():\n        return 1\n", "    def foo():\n        return 1", "    def foo():\n        return 2", false)
	if err != nil || out != "    def foo():\n        return 2\n" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestHermesZeroIndentShiftsToFileTwoSpace(t *testing.T) {
	out, n, err := applyPatch("  def x():\n    return 1\n", "def x():\n  return 1", "def x():\n  return 99", false)
	lines := strings.Split(strings.Trim(out, "\n"), "\n")
	if err != nil || n != 1 || lines[0] != "  def x():" || lines[1] != "    return 99" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestHermesBlankLinesLeftAlone(t *testing.T) {
	out, n, err := applyPatch("    a = 1\n    b = 2\n", "a = 1\nb = 2", "a = 1\n\nb = 99", false)
	lines := strings.Split(out, "\n")
	if err != nil || n != 1 || lines[0] != "    a = 1" || lines[1] != "" || lines[2] != "    b = 99" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestHermesMultipleMatchesWithoutFlagErrors(t *testing.T) {
	_, n, err := applyPatch("aaa bbb aaa", "aaa", "ccc", false)
	if err == nil || n != 0 || !strings.Contains(err.Error(), "matches 2 places") {
		t.Fatal(n, err)
	}
}

func TestHermesMultipleMatchesWithFlag(t *testing.T) {
	out, n, err := applyPatch("aaa bbb aaa", "aaa", "ccc", true)
	if err != nil || n != 2 || out != "ccc bbb ccc" {
		t.Fatal(out, n, err)
	}
}

func TestHermesCompletelyDifferentMiddleDoesNotMatch(t *testing.T) {
	content := "class Foo:\n    completely = 'unrelated'\n    content = 'here'\n    nothing = 'in common'\n    pass\n"
	pattern := "class Foo:\n    x = 1\n    y = 2\n    z = 3\n    pass"
	if _, n, err := applyPatch(content, pattern, "replaced", false); err == nil || n != 0 {
		t.Fatal("a block with an unrelated middle must not match")
	}
}

func FuzzFuzzyFind(f *testing.F) {
	f.Add("a\n  b\nc\n", "b")
	f.Add("x()\ny\nx()\n", "x()")
	f.Add("\t\tif x {\n\t\t\treturn 1\n\t\t}\n", "if x {\n  return 1\n}")
	f.Fuzz(func(t *testing.T, content, old string) {
		spans, err := FuzzyFind(content, old)
		if err != nil {
			return
		}
		last := 0
		for _, s := range spans {
			if s.Start < last || s.End < s.Start || s.End > len(content) {
				t.Fatalf("bad span %v in %q", s, content)
			}
			last = s.End
		}
	})
}

func TestPatchRefusesProtectedByDefault(t *testing.T) {
	env := newEnv(t)
	env.DataDir = t.TempDir()
	p := filepath.Join(env.DataDir, "config.yaml")
	if err := os.WriteFile(p, []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := runToolEnvErr(t, PatchTool{}, fmt.Sprintf(`{"path":%q,"old_string":"a","new_string":"b"}`, p), env)
	if b, _ := os.ReadFile(p); !strings.Contains(out, "protected path") || string(b) != "a\n" {
		t.Fatal(out)
	}
}
