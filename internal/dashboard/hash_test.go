package dashboard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func hashWrite(t *testing.T, root, rel, text string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func srcHashOf(t *testing.T, root string) string {
	t.Helper()
	h, err := SourceHash(root)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestSourceHash(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	root := t.TempDir()
	hashWrite(t, root, "web/package.json", `{"name":"web"}`)
	hashWrite(t, root, "web/src/a.ts", "export const a = 1\n")
	hashWrite(t, root, "web/src/a.test.ts", "test\n")
	hashWrite(t, root, "web/src/api/types.gen.ts", "export interface X {}\n")
	hashWrite(t, root, "other/b.go", "package b\n")
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	h1 := srcHashOf(t, root)
	if len(h1) != 64 || srcHashOf(t, root) != h1 {
		t.Fatalf("the hash must be 64 hex characters and stable: %q", h1)
	}
	for rel, text := range map[string]string{
		"web/src/a.test.ts":           "test 2\n",
		"web/src/api/types.gen.ts":    "export interface Y {}\n",
		"web/node_modules/x/index.js": "module.exports = 1\n",
		"other/b.go":                  "package b // 2\n",
	} {
		hashWrite(t, root, rel, text)
		if srcHashOf(t, root) != h1 {
			t.Fatalf("a change of %s must not change the hash", rel)
		}
	}
	hashWrite(t, root, "web/src/a.ts", "export const a = 2\n")
	h2 := srcHashOf(t, root)
	if h2 == h1 {
		t.Fatal("a change of a source must change the hash")
	}
	hashWrite(t, root, "web/src/b.ts", "export const b = 1\n")
	if srcHashOf(t, root) == h2 {
		t.Fatal("a new source that git does not track yet must change the hash")
	}
}

// The built page in dist/ comes from the current sources under web/. The check needs git but
// no Node.
func TestDashboardAssetsCurrent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not in PATH")
	}
	needBuiltPage(t)
	want, err := SourceHash("../..")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("dist/.source-hash")
	if err != nil || strings.TrimSpace(string(got)) != want {
		t.Fatal("internal/dashboard/dist is out of date: Run make web")
	}
}
