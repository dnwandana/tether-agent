package telegram

import (
	"strings"
	"testing"
)

// stateAt returns the state that scanMDV2 gives at offset i of s. It returns
// false when i is not a cut offset.
func stateAt(s string, i int) (mdv2State, bool) {
	var got mdv2State
	found := false
	scanMDV2(s, func(j int, st mdv2State) bool {
		if j == i {
			got, found = st, true
			return false
		}
		return j < i
	})
	return got, found
}

func TestScanMDV2(t *testing.T) {
	cases := []struct {
		name, s, at string // at: the text just before the offset to check
		want        mdv2State
	}{
		{"start", "a *b* c", "", mdv2State{}},
		{"inside bold", "a *b* c", "a *", mdv2State{Open: 1}},
		{"after bold", "a *b* c", "a *b*", mdv2State{}},
		{"italic", "_i_ x", "_", mdv2State{Open: 1}},
		{"underline", "__u__ x", "__", mdv2State{Open: 1}},
		{"inside underline marker", "__u__ x", "_", mdv2State{Open: 1}},
		{"strike", "~s~ x", "~", mdv2State{Open: 1}},
		{"spoiler", "||s|| x", "||", mdv2State{Open: 1}},
		{"inside spoiler marker", "||s|| x", "|", mdv2State{Open: 1}},
		{"inside link text", "x [ab](http://y\\)z) w", "x [a", mdv2State{Open: 1}},
		{"between link text and url", "x [ab](http://y\\)z) w", "x [ab]", mdv2State{Open: 2}},
		{"inside link url after escaped paren", "x [ab](http://y\\)z) w", "x [ab](http://y\\)", mdv2State{Open: 1}},
		{"after link", "x [ab](http://y\\)z) w", "x [ab](http://y\\)z)", mdv2State{}},
		{"marker in link url", "[a](http://x_y*z) w", "[a](http://x_", mdv2State{Open: 1}},
		{"after link with markers in url", "[a](http://x_y*z) w", "[a](http://x_y*z)", mdv2State{}},
		{"marker in inline code", "`a*b` c", "`a*", mdv2State{Open: 1}},
		{"after inline code", "`a*b` c", "`a*b`", mdv2State{}},
		{"link in bold", "*b [l](u) b*", "*b [l", mdv2State{Open: 2}},
		{"inside fence marker", "```go\nx\n```\nz", "``", mdv2State{Open: 1}},
		{"inside fence line", "```go\nx\n```\nz", "```", mdv2State{Open: 1}},
		{"end of fence line", "```go\nx\n```\nz", "```go", mdv2State{Open: 1}},
		{"in code block", "```go\nx\n```\nz", "```go\n", mdv2State{Pre: true, Lang: "go"}},
		{"code block without language", "```\nx\n```", "```\n", mdv2State{Pre: true}},
		{"marker in code block", "```\na*b\n```", "```\na*", mdv2State{Pre: true}},
		{"inside closing fence", "```go\nx\n```\nz", "```go\nx\n``", mdv2State{Open: 1, Pre: true, Lang: "go"}},
		{"after code block", "```go\nx\n```\nz", "```go\nx\n```", mdv2State{}},
		{"inside quote line", "> q line\nnext", "> q", mdv2State{Open: 1}},
		{"end of quote line", "> q line\nnext", "> q line", mdv2State{}},
		{"between quote lines", "> a\n> b", "> a\n", mdv2State{}},
		{"bold over quote lines", "> *a\n> b*", "> *a", mdv2State{Open: 1}},
		{"inside expandable quote", "**>a\n>b||\nc", "**>a", mdv2State{Open: 1}},
		{"inside expandable quote marker", "**>a\n>b||\nc", "*", mdv2State{Open: 1}},
		{"after expandable quote", "**>a\n>b||\nc", "**>a\n>b||", mdv2State{}},
		{"spoiler in quote", "> a ||b||\nc", "> a ||b", mdv2State{Open: 2}},
		{"empty bold", "a**_b_", "a**", mdv2State{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.HasPrefix(c.s, c.at) {
				t.Fatalf("%q is not a prefix of %q", c.at, c.s)
			}
			st, ok := stateAt(c.s, len(c.at))
			if !ok {
				t.Fatalf("offset %d of %q is not a cut offset", len(c.at), c.s)
			}
			if st != c.want {
				t.Fatalf("state at %d of %q = %+v, want %+v", len(c.at), c.s, st, c.want)
			}
		})
	}
}

func TestScanMDV2SkipsEscapesAndRunes(t *testing.T) {
	if _, ok := stateAt("a\\.b", len("a\\")); ok {
		t.Error("the offset between an escape backslash and its character is a cut offset")
	}
	if st, ok := stateAt("a\\.b", len("a\\.")); !ok || st != (mdv2State{}) {
		t.Errorf("state after an escape = %+v, %v", st, ok)
	}
	if _, ok := stateAt("a\\*b", len("a\\")); ok {
		t.Error("the offset inside an escaped marker is a cut offset")
	}
	if st, _ := stateAt("a\\*b", len("a\\*")); st.Open != 0 {
		t.Errorf("an escaped marker opens an entity: %+v", st)
	}

	code := "```\na\\`b\n```"
	if _, ok := stateAt(code, len("```\na\\")); ok {
		t.Error("the offset inside an escaped backtick in a code block is a cut offset")
	}
	if st, ok := stateAt(code, len("```\na\\`")); !ok || !st.Pre || st.Open != 0 {
		t.Errorf("the escaped backtick closes the code block: %+v, %v", st, ok)
	}
	if st, ok := stateAt("`a\\`b` c", len("`a\\`")); !ok || st.Open != 1 {
		t.Errorf("the escaped backtick closes the inline code: %+v, %v", st, ok)
	}

	if _, ok := stateAt("é*x*", 1); ok {
		t.Error("an offset inside a rune is a cut offset")
	}
	if _, ok := stateAt("a\\😀b", len("a\\")+2); ok {
		t.Error("an offset inside an escaped rune is a cut offset")
	}

	for _, s := range []string{"", "plain", "*open", "```go\nx", "a\\", "> q", "[a](b"} {
		last, calls := -1, 0
		scanMDV2(s, func(i int, _ mdv2State) bool {
			if i <= last {
				t.Errorf("%q: offset %d after offset %d", s, i, last)
			}
			last = i
			calls++
			return true
		})
		if last != len(s) {
			t.Errorf("%q: the last offset is %d, want %d", s, last, len(s))
		}
		if calls == 0 {
			t.Errorf("%q: fn is not called", s)
		}
	}
}

func TestScanMDV2Stops(t *testing.T) {
	var seen []int
	scanMDV2("abcdef", func(i int, _ mdv2State) bool {
		seen = append(seen, i)
		return i < 2
	})
	if len(seen) != 3 || seen[2] != 2 {
		t.Fatalf("offsets = %v, want [0 1 2]", seen)
	}
	// A stop inside a marker also stops the scan.
	seen = nil
	scanMDV2("```go\nx\n```", func(i int, _ mdv2State) bool {
		seen = append(seen, i)
		return i < 1
	})
	if len(seen) != 2 || seen[1] != 1 {
		t.Fatalf("offsets = %v, want [0 1]", seen)
	}
}

// TestScanMDV2AgreesWithCheck checks the scanner against checkMDV2: at each
// offset where no entity is open, the text before the offset is valid.
func TestScanMDV2AgreesWithCheck(t *testing.T) {
	inputs := []string{
		"**a** (b) 1.5!",
		"# Title\n- item 1.\n* item 2\n> quote\n**> hidden||",
		"see [the docs](https://example.com/a_(b)) and `x+y=z` {ok}",
		"```go\nfunc f() { return `x` }\n```\nend.",
		"~~old~~ ||spoiler|| *it* _u_ #tag a|b c>d e=f",
		"| a | b |\n|---|---|\n| 1 | 2.5 |\n",
		"😀 \\ \\. \\\\ " + strings.Repeat("x.", 50),
		"**Fix my\\_project** \\*draft\\* \\[x\\] a\\|b \\` `c\\_d`",
		"_2 min · step 4/90 · web_fetch_\n__init__ snake_case _x_ _y_z_",
		"> one\n> two **bold** [l](http://u)\n>\n> three\n\nafter",
		"**> a\n> b ||c||\n> d||\nend",
	}
	for _, in := range inputs {
		s := ToMarkdownV2(in)
		if err := checkMDV2(s); err != nil {
			t.Fatalf("checkMDV2(%q) = %v", s, err)
		}
		scanMDV2(s, func(i int, st mdv2State) bool {
			if st.Open == 0 && !st.Pre {
				if err := checkMDV2(s[:i]); err != nil {
					t.Errorf("%q: offset %d is closed, but checkMDV2 = %v", s, i, err)
				}
			}
			return true
		})
	}
}
