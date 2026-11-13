package acp

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// realTemp returns a new temporary folder with its symlinks resolved. t.TempDir can be
// under a symlink, for example /var on macOS.
func realTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRequestPaths(t *testing.T) {
	home, cwd, out := realTemp(t), realTemp(t), realTemp(t)
	t.Setenv("HOME", home)
	if err := os.Symlink(out, filepath.Join(cwd, "link")); err != nil {
		t.Fatal(err)
	}
	in := permissionInput{
		Locations: []string{"a.go", "/abs/x.txt", ""},
		Diffs:     []diffItem{{Path: "a.go"}, {Path: "~/notes.txt"}},
		RawPaths:  []string{"sub/../../x", "~", "link/y.txt"},
	}
	want := []string{
		filepath.Join(cwd, "a.go"),            // relative: joined to cwd; the second a.go is a duplicate
		"/abs/x.txt",                          // absolute: cleaned
		filepath.Join(home, "notes.txt"),      // "~/" is the home folder
		filepath.Join(filepath.Dir(cwd), "x"), // ".." can leave cwd
		home,                                  // "~" alone
		filepath.Join(out, "y.txt"),           // a link in cwd resolves to its target
	}
	if got := requestPaths(in, cwd); !reflect.DeepEqual(got, want) {
		t.Fatalf("requestPaths = %q\nwant %q", got, want)
	}
	// An empty cwd keeps a relative path relative.
	if got := requestPaths(permissionInput{Locations: []string{"a/../b.go"}}, ""); !reflect.DeepEqual(got, []string{"b.go"}) {
		t.Fatal(got)
	}
	// A ".." after a link applies to the target of the link, as in the kernel.
	if got := requestPaths(permissionInput{RawPaths: []string{"link/../z.txt"}}, cwd); !reflect.DeepEqual(got, []string{filepath.Join(filepath.Dir(out), "z.txt")}) {
		t.Fatal("link/..:", got)
	}
	if got := requestPaths(permissionInput{Kind: "edit"}, cwd); got != nil {
		t.Fatal(got)
	}
}
