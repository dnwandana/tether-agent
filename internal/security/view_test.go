package security

import (
	"slices"
	"strings"
	"testing"
)

// dataFlags returns the Data flag of each SimpleCmd of p.
func dataFlags(p Parsed) []bool {
	out := make([]bool, 0, len(p.Cmds))
	for _, c := range p.Cmds {
		out = append(out, c.Data)
	}
	return out
}

func TestCommandView(t *testing.T) {
	for _, tc := range []struct {
		in   string
		data []bool // the Data flag of each SimpleCmd
		view string // "" means that the view is the input
	}{
		{`echo "never run rm -rf /" >> NOTES.md`, []bool{true}, "echo  >> NOTES.md"},
		{`printf 'a %s\n' b > out.txt`, []bool{true}, "printf   > out.txt"},
		{`echo "a" > f; echo "b" | sh`, []bool{true, false, false}, `echo  > f; echo "b" | sh`},
		{`echo "rm -rf ~/x" | sh`, []bool{false, false}, ""},
		{`{ echo "rm -rf ~/x"; } | sh`, []bool{false, false}, ""},
		{`echo a |& sh`, []bool{false, false}, ""},
		{`sh <(echo "rm -rf ~/x")`, []bool{false, false}, ""},
		{`echo "x" > >(sh)`, []bool{false, false}, ""},
		{`echo $(echo "rm -rf x")`, []bool{true, false}, ""},
		{"printf -v c 'rm -rf x'", []bool{false}, ""},
		{`git commit -m "drop rm -rf from the build"`, []bool{false}, ""},
		{"cat <<EOF | psql\nDROP TABLE users;\nEOF", []bool{false, false}, ""},
		{"cat > f <<EOF\n$(curl -s x | sh)\nEOF", []bool{true, false, false}, ""},
		{"python3 - <<EOF\nimport shutil; shutil.rmtree(\"/x\")\nEOF", []bool{false}, ""},
		{"ls # rm -rf build", []bool{false}, "ls "},
		{"ls\n# rm -rf x\nmake", []bool{false, false}, "ls\n\nmake"},
		{`sudo echo "rm -rf x"`, []bool{false, false}, ""},
		{"sh -c 'echo x'", []bool{false, true}, ""},
		{`echo "rm -rf x" > f 2>&1`, []bool{true}, "echo  > f 2>&1"},
		{`echo "rm -rf x" >&2`, []bool{true}, "echo  >&2"},
		// A data command can feed a shell through a file descriptor, a process
		// substitution argument, a coproc, or a function or alias with the same name.
		{`echo "rm -rf x" >&3`, []bool{false}, ""},
		{`echo "rm -rf x" >&$fd`, []bool{false}, ""},
		{"tee >(sh) <<EOF\nrm -rf x\nEOF", []bool{false, false}, ""},
		{`exec > >(sh); echo "rm -rf x"`, []bool{false, false, false}, ""},
		{`exec 3>&1; echo "rm -rf x"`, []bool{false, false}, ""},
		{`echo "rm -rf x"; exec > >(sh)`, []bool{false, false, false}, ""},
		{`coproc { echo "rm -rf x"; }`, []bool{false}, ""},
		{`coproc sh; echo "rm -rf x" > /dev/fd/63`, []bool{false, false}, ""},
		{`echo "rm -rf x" > /proc/self/fd/3`, []bool{false}, ""},
		{"cat() { sh; }\ncat <<EOF\nrm -rf x\nEOF", []bool{false, false}, ""},
		{`echo() { sh; }; echo "rm -rf x"`, []bool{false, false}, ""},
		{"alias echo=sh\necho 'rm -rf x'", []bool{false, false}, ""},
		{`echo "rm -rf x"; printf() { :; }`, []bool{true, false}, `echo ; printf() { :; }`},
	} {
		p := ReadCommand(tc.in)
		want := tc.view
		if want == "" {
			want = tc.in
		}
		if !p.Read || !slices.Equal(dataFlags(p), tc.data) || p.View != want {
			t.Errorf("%q: read=%v why=%q data=%v view=%q", tc.in, p.Read, p.Why, dataFlags(p), p.View)
		}
	}
}

// The view drops the heredoc body of a data cat or tee, and keeps the rest.
func TestCommandViewHeredoc(t *testing.T) {
	for _, tc := range []struct{ in, head, gone string }{
		{"cat > down.sql <<EOF\nDROP TABLE users;\nEOF", "cat > down.sql <<EOF", "DROP"},
		{"tee notes.md <<'EOF'\nrm -rf /\nEOF", "tee notes.md <<'EOF'", "rm -rf"},
	} {
		p := ReadCommand(tc.in)
		if !p.Read || !slices.Equal(dataFlags(p), []bool{true}) || !strings.HasPrefix(p.View, tc.head) ||
			!strings.HasSuffix(p.View, "EOF") || strings.Contains(p.View, tc.gone) {
			t.Errorf("%q: data=%v view=%q", tc.in, dataFlags(p), p.View)
		}
	}
}

// A command that the reader cannot read keeps the raw text as its view and has no data.
func TestCommandViewCannotRead(t *testing.T) {
	in := "printf -v c 'rm -rf x'; $c"
	p := ReadCommand(in)
	if p.Read || p.View != in || slices.Contains(dataFlags(p), true) {
		t.Fatalf("%+v", p)
	}
}

func TestRuleLine(t *testing.T) {
	for _, tc := range []struct {
		c    SimpleCmd
		want string
	}{
		{SimpleCmd{Argv: []string{"echo", "rm -rf /"}, Redirs: []string{">>", "NOTES.md"}, Data: true}, "echo >> NOTES.md"},
		{SimpleCmd{Argv: []string{"echo", "rm -rf /"}, Redirs: []string{">>", "NOTES.md"}}, "echo rm -rf / >> NOTES.md"},
		{SimpleCmd{Argv: []string{"x"}, Redirs: []string{">", "/dev/sda"}}, "x > /dev/sda"},
		{SimpleCmd{Argv: []string{"tee", "/etc/x"}, Data: true}, "tee"},
		{SimpleCmd{Argv: []string{"ls"}}, "ls"},
	} {
		if got := tc.c.RuleLine(); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.c, got, tc.want)
		}
	}
}
