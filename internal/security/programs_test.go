package security

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// writeProgram writes a file with the mode at dir/name and returns its real path.
func writeProgram(t *testing.T, dir, name string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
	return RealPath(p)
}

func TestResolvePrograms(t *testing.T) {
	root := t.TempDir()
	bin, work := filepath.Join(root, "bin"), filepath.Join(root, "work")
	for _, d := range []string{bin, work, filepath.Join(work, "sub")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tool := writeProgram(t, bin, "tool", 0o755)
	sudo := writeProgram(t, bin, "sudo", 0o755)
	ssh := writeProgram(t, bin, "ssh", 0o755)
	env := writeProgram(t, bin, "env", 0o755)
	writeProgram(t, bin, "data", 0o644)
	run := writeProgram(t, work, "run.sh", 0o755)
	if err := os.Symlink(filepath.Join(bin, "tool"), filepath.Join(bin, "linked")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cmd, workdir, path string
		want               []string
		ok                 bool
	}{
		{"tool x", work, bin, []string{tool}, true},
		{"linked x", work, bin, []string{tool}, true},
		{"tool a | tool b", work, bin, []string{tool, tool}, true},
		{"cd /tmp && echo hi", work, bin, []string{"builtin:cd", "builtin:echo"}, true},
		{"if true; then tool; fi", work, bin, []string{"builtin:true", tool}, true},
		{"[ -f x ] && tool", work, bin, []string{"builtin:[", tool}, true},
		{"./run.sh", work, bin, []string{run}, true},
		{filepath.Join(work, "run.sh"), "", bin, []string{run}, true},
		{"sudo tool x", work, bin, []string{sudo, tool}, true},
		{"env tool x", work, bin, []string{env, tool}, true},
		{"ssh prod tool", work, bin, []string{ssh, "remote:tool"}, true},
		{"cd sub && tool", work, bin, []string{"builtin:cd", tool}, true},
		// Not eligible for Always.
		{"missing x", work, bin, nil, false},
		{"data", work, bin, nil, false},
		{"./run.sh", "", bin, nil, false},
		{"tool", work, "", nil, false},
		{"PATH=/tmp/p tool x", work, bin, nil, false},
		{"env FOO=1 tool x", work, bin, nil, false},
		{"cd sub && ./run.sh", work, bin, nil, false},
		{"tool 'x", work, bin, nil, false},
		// The command changes how the shell finds a program.
		{"PATH=/tmp/p; tool", work, bin, nil, false},
		{"export PATH=/tmp/p; tool", work, bin, nil, false},
		{"declare -x PATH=/tmp/p; tool", work, bin, nil, false},
		{"PATH+=:/tmp/p; tool", work, bin, nil, false},
		{"read PATH < f; tool", work, bin, nil, false},
		{"hash -p /tmp/p/tool tool; tool", work, bin, nil, false},
		{"enable -n echo; echo", work, bin, nil, false},
		{"BASH_CMDS[tool]=/tmp/p/tool; tool", work, bin, nil, false},
		{"sh -c 'export PATH=/tmp/p; tool'", work, bin, nil, false},
		{"sudo FOO=1 tool", work, bin, nil, false},
		{"export FOO=1; tool", work, bin, []string{"builtin:export", tool}, true},
	} {
		got, ok := ResolvePrograms(ReadCommand(tc.cmd), tc.workdir, tc.path)
		if ok != tc.ok || (ok && !slices.Equal(got, tc.want)) {
			t.Errorf("%q in %q: got %q %v, want %q %v", tc.cmd, tc.workdir, got, ok, tc.want, tc.ok)
		}
	}
	// A relative directory of PATH means the workdir.
	if got, ok := ResolvePrograms(ReadCommand("run.sh"), work, "."); !ok || !slices.Equal(got, []string{run}) {
		t.Errorf("relative PATH entry: %q %v", got, ok)
	}
	if _, ok := ResolvePrograms(ReadCommand("run.sh"), "", "."); ok {
		t.Error("a relative PATH entry with no workdir resolved")
	}
	if _, ok := ResolvePrograms(ReadCommand("cd sub && run.sh"), work, "."); ok {
		t.Error("a relative PATH entry after cd resolved")
	}
}
