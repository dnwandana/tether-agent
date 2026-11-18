package security

import (
	"strings"
	"testing"
)

// FuzzReadCommand checks that the reader never panics, and that each SimpleCmd of a command
// that the reader can read has a program word.
func FuzzReadCommand(f *testing.F) {
	for _, s := range []string{
		"ls -la", "sudo rm -rf /", `sh -c 'ssh prod "rm x"'`, "echo $(", "cat <<EOF\nx\nEOF",
		`"" x`, "env -S 'a b' c", "sudo ''", "docker exec web", "kubectl exec p --",
		strings.Repeat("$(", 100), strings.Repeat("sh -c ", 10) + "ls",
		"{a,b}{c,d}", "echo {1..300}", "{reboot,}", "x >| y", "su root -- -c ls",
		"tee >(sh) <<EOF\nx\nEOF", "flock /tmp/l -c ls", "ssh h -t -- ls",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p := ReadCommand(s)
		if !p.Read {
			return
		}
		for _, c := range p.Cmds {
			if len(c.Argv) == 0 || c.Argv[0] == "" {
				t.Fatalf("%q: a SimpleCmd has no program word: %+v", s, c)
			}
		}
	})
}
