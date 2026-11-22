package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dnwandana/tether-agent/internal/dashboard"
)

func gendashRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/dnwandana/tether-agent\n\ngo 1.26.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return dir
}

func TestGenDashboardTypesWritesFile(t *testing.T) {
	dir := gendashRoot(t)
	var out, errb bytes.Buffer
	if code := run([]string{"gen-dashboard-types"}, &out, &errb); code != 0 {
		t.Fatal(code, errb.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "web", "src", "api", "types.gen.ts"))
	if err != nil || string(got) != dashboard.GenerateTypes() {
		t.Fatalf("the file must hold GenerateTypes(): %v", err)
	}
}

func TestGenDashboardHashWritesFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	dir := gendashRoot(t)
	if err := os.MkdirAll(filepath.Join(dir, "web", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web", "src", "main.ts"), []byte("export {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	var out, errb bytes.Buffer
	if code := run([]string{"gen-dashboard-hash"}, &out, &errb); code != 0 {
		t.Fatal(code, errb.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "internal", "dashboard", "dist", ".source-hash"))
	want, herr := dashboard.SourceHash(dir)
	if err != nil || herr != nil || string(got) != want+"\n" {
		t.Fatalf("%q %v %v", got, err, herr)
	}
}

func TestGenDashboardArgsAndRoot(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, name := range []string{"gen-dashboard-types", "gen-dashboard-hash"} {
		var out, errb bytes.Buffer
		if code := run([]string{name, "x"}, &out, &errb); code != 2 || errb.String() != "usage: tether "+name+"\n" {
			t.Errorf("%s x: %d %q", name, code, errb.String())
		}
		errb.Reset()
		if code := run([]string{name}, &out, &errb); code != 1 ||
			errb.String() != name+": run this command in the repository root.\n" {
			t.Errorf("%s: %d %q", name, code, errb.String())
		}
	}
}
