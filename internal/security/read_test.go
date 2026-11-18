package security

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// argvs returns the Argv of each SimpleCmd of p.
func argvs(p Parsed) [][]string {
	out := make([][]string, 0, len(p.Cmds))
	for _, c := range p.Cmds {
		out = append(out, c.Argv)
	}
	return out
}

// sameArgvs reports whether a and b hold the same Argv lists. A nil list and an empty list
// are the same.
func sameArgvs(a, b [][]string) bool {
	return slices.EqualFunc(a, b, func(x, y []string) bool { return slices.Equal(x, y) })
}

func TestReadCommandLimits(t *testing.T) {
	if MaxCommandBytes != 32768 || MaxNesting != 40 || MaxParseDepth != 4 || MaxWrapperChain != 16 {
		t.Fatal(MaxCommandBytes, MaxNesting, MaxParseDepth, MaxWrapperChain)
	}
}

func TestReadCommandForms(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    [][]string
		runtime bool
	}{
		{"ls -la", [][]string{{"ls", "-la"}}, false},
		{`echo "a b" 'c d' e\ f`, [][]string{{"echo", "a b", "c d", "e f"}}, false},
		{`echo "x\"y" "a\$b" "c\d"`, [][]string{{"echo", `x"y`, "a$b", `c\d`}}, false},
		{`printf $'a\tb\x41\101'`, [][]string{{"printf", "a\tbAA"}}, false},
		{"ls # rm -rf /", [][]string{{"ls"}}, false},
		{"echo $((1+2))", [][]string{{"echo", "$((1+2))"}}, true},
		{"echo $(ls $(pwd))", [][]string{{"echo", "$(ls $(pwd))"}, {"ls", "$(pwd)"}, {"pwd"}}, true},
		{"echo `id`", [][]string{{"echo", "`id`"}, {"id"}}, true},
		{"diff <(ls a) <(ls b)", [][]string{{"diff", "<(ls a)", "<(ls b)"}, {"ls", "a"}, {"ls", "b"}}, true},
		{"cat <<EOF\nhello $(id)\nEOF", [][]string{{"cat"}, {"id"}}, true},
		{"cat <<'EOF'\n$(id)\nEOF", [][]string{{"cat"}}, false},
		{"export FOO=bar BAZ", [][]string{{"export", "FOO=bar", "BAZ"}}, false},
		{`local x="a b"`, [][]string{{"local", "x=a b"}}, false},
		{"time make -j4", [][]string{{"make", "-j4"}}, false},
		{"FOO=bar", nil, false},
		{"FOO=$(id)", [][]string{{"id"}}, true},
		{"FOO=1 make", [][]string{{"make"}}, false},
		{"if true; then rm x; fi", [][]string{{"true"}, {"rm", "x"}}, false},
		{`for f in *.txt; do rm "$f"; done`, [][]string{{"rm", "$f"}}, true},
		{"case $x in a) rm a;; esac", [][]string{{"rm", "a"}}, true},
		{"f() { rm x; }", [][]string{{"rm", "x"}}, false},
		{"[[ -f x ]] && rm x", [][]string{{"rm", "x"}}, false},
		{"(cd /tmp && rm x)", [][]string{{"cd", "/tmp"}, {"rm", "x"}}, false},
		{"{ ls; }", [][]string{{"ls"}}, false},
		{"coproc ls", [][]string{{"ls"}}, false},
		{"let x=1", nil, false},
		{"(( x++ ))", nil, false},
		{"while read l; do echo $l; done < f", [][]string{{"read", "l"}, {"echo", "$l"}}, true},
	} {
		p := ReadCommand(tc.in)
		if !p.Read || p.RuntimeText != tc.runtime || !sameArgvs(argvs(p), tc.want) {
			t.Errorf("%q: read=%v why=%q runtime=%v argv=%q", tc.in, p.Read, p.Why, p.RuntimeText, argvs(p))
		}
	}
}

// Each quote form of a program word gives the same word.
func TestReadCommandQuoteRemoval(t *testing.T) {
	for _, in := range []string{"'sudo' x", `s\udo x`, `$'\x73udo' x`, `"su"do x`, `su"do" x`} {
		p := ReadCommand(in)
		if !p.Read || len(p.Cmds) == 0 || p.Cmds[0].Argv[0] != "sudo" {
			t.Errorf("%q: read=%v why=%q argv=%q", in, p.Read, p.Why, argvs(p))
		}
	}
}

func TestReadCommandRedirs(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"dd if=x of=y > /dev/sda 2>&1 < in", []string{">", "/dev/sda", "2>&", "1", "<", "in"}},
		{`x >/de''v/sda`, []string{">", "/dev/sda"}},
		{`echo a >> "notes.md"`, []string{">>", "notes.md"}},
		{"cat <<EOF\nbody\nEOF", []string{"<<", "EOF"}},
		// The rule line writes the clobber operator as a plain redirect.
		{"cat x >| /dev/sda", []string{">", "/dev/sda"}},
		{"cat x 2>| err.log", []string{"2>", "err.log"}},
	} {
		p := ReadCommand(tc.in)
		if !p.Read || len(p.Cmds) == 0 || !slices.Equal(p.Cmds[0].Redirs, tc.want) {
			t.Errorf("%q: read=%v why=%q redirs=%+v", tc.in, p.Read, p.Why, p.Cmds)
		}
	}
}

// An assignment before the program word marks the command. The program word is not the
// assignment.
func TestReadCommandAssignedMark(t *testing.T) {
	p := ReadCommand("PATH=/tmp/p rm x; ls")
	if !p.Read || len(p.Cmds) != 2 || !p.Cmds[0].assigned || p.Cmds[1].assigned ||
		!slices.Equal(p.Cmds[0].Argv, []string{"rm", "x"}) {
		t.Fatalf("%+v", p)
	}
}

func TestReadCommandCannotRead(t *testing.T) {
	const runTime = "the program name is built at run time"
	for _, tc := range []struct{ in, why string }{
		{strings.Repeat("a", MaxCommandBytes+1), "the command is longer than 32 KB"},
		{"$c x", runTime},
		{`"$CMD" x`, runTime},
		{"$(which rm) -rf x", runTime},
		{"${RM} x", runTime},
		{"printf -v c 'rm -rf x'; $c", runTime},
		{`"" x`, "the program name is empty"},
		{"echo " + strings.Repeat("$(echo ", MaxNesting+1) + "x" + strings.Repeat(")", MaxNesting+1),
			"the command nests more than 40 levels"},
		{strings.Repeat("( ", MaxNesting+1) + "ls" + strings.Repeat(" )", MaxNesting+1),
			"the command nests more than 40 levels"},
	} {
		p := ReadCommand(tc.in)
		if p.Read || p.Why != tc.why || p.View != tc.in {
			t.Errorf("%.60q: read=%v why=%q", tc.in, p.Read, p.Why)
		}
	}
	// A syntax error gives the parser message after a fixed start.
	for _, in := range []string{"echo 'x", `echo "x`, "cat <<EOF\nx", "echo $(ls", "if true; then"} {
		p := ReadCommand(in)
		if p.Read || !strings.HasPrefix(p.Why, "the command has a syntax error: ") || p.View != in {
			t.Errorf("%q: read=%v why=%q", in, p.Read, p.Why)
		}
	}
	// 32 KB of nested "$(" stops with no crash.
	if p := ReadCommand(strings.Repeat("$(", MaxCommandBytes/2)); p.Read {
		t.Fatal("32 KB of nested $( was read")
	}
}

// The limits are not off by one.
func TestReadCommandAtLimits(t *testing.T) {
	for _, in := range []string{
		"echo " + strings.Repeat("a", MaxCommandBytes-len("echo ")),
		"echo " + strings.Repeat("$(echo ", MaxNesting) + "x" + strings.Repeat(")", MaxNesting),
	} {
		if p := ReadCommand(in); !p.Read {
			t.Errorf("%.60q: %q", in, p.Why)
		}
	}
}

// Brace expansion makes argv words as bash does. An empty unquoted result is dropped.
func TestReadCommandBraces(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want [][]string
	}{
		{"{reboot,}", [][]string{{"reboot"}}},
		{"{rm,-rf,/home}", [][]string{{"rm", "-rf", "/home"}}},
		{"rm {-rf,/home/u/proj}", [][]string{{"rm", "-rf", "/home/u/proj"}}},
		{"echo a{b,c}d e", [][]string{{"echo", "abd", "acd", "e"}}},
		{"echo {a,b}{1,2}", [][]string{{"echo", "a1", "a2", "b1", "b2"}}},
		{"echo {a,{b,c}}", [][]string{{"echo", "a", "b", "c"}}},
		{"echo {1..3} {c..a} {01..3} {1..7..3}", [][]string{{"echo", "1", "2", "3", "c", "b", "a", "01", "02", "03", "1", "4", "7"}}},
		{"echo {a,''} x", [][]string{{"echo", "a", "", "x"}}},
		{`echo {a,"b c"}`, [][]string{{"echo", "a", "b c"}}},
		{"echo '{a,b}' \\{a,b\\} {a} {a..} {,}", [][]string{{"echo", "{a,b}", "{a,b}", "{a}", "{a..}"}}},
		{"echo x{a,b", [][]string{{"echo", "x{a,b"}}},
	} {
		p := ReadCommand(tc.in)
		if !p.Read || !sameArgvs(argvs(p), tc.want) {
			t.Errorf("%q: read=%v why=%q argv=%q", tc.in, p.Read, p.Why, argvs(p))
		}
	}
	// A brace word with an expansion keeps the expansion text in each result.
	p := ReadCommand("echo {a,$x}")
	if !p.Read || !p.RuntimeText || !sameArgvs(argvs(p), [][]string{{"echo", "a", "$x"}}) {
		t.Errorf("%+v", p)
	}
}

// A brace expansion that makes too many words stops the reader quickly.
func TestReadCommandBraceLimit(t *testing.T) {
	const why = "the command expands to more than 256 words"
	for _, in := range []string{
		"echo {1..257}",
		"echo " + strings.Repeat("{a,b}", 9),
		"echo {a,b}{c,d} " + strings.Repeat("{1..2}", 8),
		"echo {1..9223372036854775807}",
		"echo {-9223372036854775808..9223372036854775807}",
	} {
		start := time.Now()
		p := ReadCommand(in)
		if p.Read || p.Why != why {
			t.Errorf("%.60q: read=%v why=%q", in, p.Read, p.Why)
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("%.60q: took %v", in, d)
		}
	}
	// Many open braces in one word stop the reader quickly.
	for _, in := range []string{
		"echo " + strings.Repeat("{", MaxCommandBytes-10),
		"echo " + strings.Repeat("{a,", MaxCommandBytes/3-10),
		"echo " + strings.Repeat("{,", MaxCommandBytes/2-10),
		"echo " + strings.Repeat("{a,b}", MaxCommandBytes/6),
		"echo " + strings.Repeat("{1..1}", MaxCommandBytes/7),
		"echo " + strings.Repeat("{", MaxBraceWords+1),
	} {
		start := time.Now()
		p := ReadCommand(in)
		if p.Read || p.Why != "the command has more than 256 braces in one word" || time.Since(start) > time.Second {
			t.Errorf("%.60q: read=%v why=%q time=%v", in, p.Read, p.Why, time.Since(start))
		}
	}
	if p := ReadCommand("echo " + strings.Repeat("{", MaxBraceWords) + " x"); !p.Read {
		t.Fatal(p.Why)
	}
	if p := ReadCommand("echo {1..256}"); !p.Read || len(p.Cmds[0].Argv) != 257 {
		t.Fatalf("256 words: %q", p.Why)
	}
	// A chain of braces that make one word each is quick.
	start := time.Now()
	if p := ReadCommand("echo " + strings.Repeat("{1..1}", MaxBraceWords)); !p.Read || time.Since(start) > time.Second {
		t.Fatalf("%q %v", p.Why, time.Since(start))
	}
	// Words with no brace do not count.
	if p := ReadCommand("rm" + strings.Repeat(" f", 300)); !p.Read {
		t.Fatal(p.Why)
	}
}

// An unquoted glob in the program word stops the reader. A glob in an argument does not.
func TestReadCommandGlobs(t *testing.T) {
	for _, in := range []string{"rm *.o", "ls -d [a-z]*", "[ -f x ]", "'rebo?t'", `rebo\?t`, "\"pass*\" x",
		"echo ?", "git log -- '*.go'"} {
		if p := ReadCommand(in); !p.Read {
			t.Errorf("%q: %q", in, p.Why)
		}
	}
	for _, in := range []string{"rebo?t", "/usr/bin/pass[w]d", "*", "x*y z", "env pass?d", "nice -n 5 pass[w]d"} {
		if p := ReadCommand(in); p.Read || p.Why != "the program name is built at run time" {
			t.Errorf("%q: read=%v why=%q", in, p.Read, p.Why)
		}
	}
}
