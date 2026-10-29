package telegram

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzMarkdownV2 feeds random markdown to ToMarkdownV2. The output must be
// valid UTF-8, SplitUTF16 must not lose text, and checkMDV2 must accept the
// output: no reserved character stays without an escape outside an entity.
func FuzzMarkdownV2(f *testing.F) {
	f.Add("**a** (b) 1.5!")
	f.Add("# Title\n- item 1.\n* item 2\n> quote\n**> hidden||")
	f.Add("see [the docs](https://example.com/a_(b)) and `x+y=z` {ok}")
	f.Add("```go\nfunc f() { return `x` }\n```\nend.")
	f.Add("~~old~~ ||spoiler|| *it* _u_ #tag a|b c>d e=f")
	f.Add("| a | b |\n|---|---|\n| 1 | 2.5 |\n")
	f.Add("😀 \\ \\. \\\\ " + strings.Repeat("x.", 50))
	f.Add("**Fix my\\_project** \\*draft\\* \\[x\\] a\\|b \\` `c\\_d`")
	f.Add("_2 min · step 4/90 · web_fetch_\n__init__ snake_case _x_ _y_z_")
	f.Add("| a\\|b | `c|d` |\n|---|---|\n| _e_ | f\\_g |\n")
	f.Fuzz(func(t *testing.T, md string) {
		if !utf8.ValidString(md) {
			return
		}
		out := ToMarkdownV2(md)
		if !utf8.ValidString(out) {
			t.Fatalf("output is not valid UTF-8: %q", out)
		}
		if strings.Join(SplitUTF16(out, 4096), "") != out {
			t.Fatal("split is not lossless")
		}
		if err := checkMDV2(out); err != nil {
			t.Fatalf("%v\ninput:  %q\noutput: %q", err, md, out)
		}
	})
}

// FuzzSplitMarkdownV2 feeds random markdown to ToMarkdownV2 and splits the
// output with SplitMarkdownV2. Each part must not be empty, must be valid
// UTF-8, and must fit in the limit. When no entity is longer than a quarter
// of the limit, a split with no open entity is possible, so checkMDV2 must
// also accept each part.
func FuzzSplitMarkdownV2(f *testing.F) {
	f.Add(strings.Repeat("a", 90)+" [LinkSwift](https://github.com/x/y) end", 100)
	f.Add("```go\n"+strings.Repeat("x := f(1)\n", 40)+"```", 120)
	f.Add(strings.Repeat("a", 150)+" [LinkSwift](https://github.com/x/y) end", 160)
	f.Add("**> a\n**> b||\n"+strings.Repeat("c ", 80), 64)
	f.Add(strings.Repeat("d ", 40)+"\n**> hidden text here||\n"+strings.Repeat("c ", 40), 96)
	f.Add(strings.Repeat("> quote line\n", 30)+"**bold** `code` ~~s~~ ||sp||", 80)
	f.Add("```\n"+strings.Repeat("    indented \\ `x`\n", 20)+"```\n😀😀 \\. end", 64)
	f.Fuzz(func(t *testing.T, s string, limit int) {
		if !utf8.ValidString(s) || limit < 64 || limit > 512 {
			return
		}
		md := ToMarkdownV2(s)
		parts := SplitMarkdownV2(md, limit)
		if len(parts) == 0 {
			t.Fatal("no parts")
		}
		for i, p := range parts {
			if p == "" && md != "" {
				t.Fatalf("part %d is empty\noutput: %q", i, md)
			}
			if !utf8.ValidString(p) {
				t.Fatalf("part %d is not valid UTF-8: %q", i, p)
			}
			if n := UTF16Len(p); n > limit {
				t.Fatalf("part %d has %d units, limit %d\noutput: %q", i, n, limit, md)
			}
		}
		if checkMDV2(md) != nil || longestEntity(md) > limit/4 {
			return
		}
		for i, p := range parts {
			if err := checkMDV2(p); err != nil {
				t.Fatalf("part %d: %v\npart:   %q\noutput: %q", i, err, p, md)
			}
		}
	})
}

// longestEntity returns the longest run of s, in UTF-16 units, between two
// cut offsets with no open entity. A code block alone does not count,
// because the split closes it and opens it again at a cut.
func longestEntity(s string) int {
	longest, units, prev, closed := 0, 0, 0, 0
	scanMDV2(s, func(i int, st mdv2State) bool {
		units += UTF16Len(s[prev:i])
		prev = i
		if st.Open == 0 {
			longest = max(longest, units-closed)
			closed = units
		}
		return true
	})
	return max(longest, units-closed)
}

// mdv2Reserved holds the characters that MarkdownV2 reserves outside entities.
const mdv2Reserved = "_*[]()~`>#+-=|{}.!"

// checkMDV2 returns an error when s is not valid Telegram MarkdownV2. It is a
// small model of parse_markdown_v2 in tdlib td/telegram/MessageEntity.cpp. It
// is stricter than tdlib in some places, but it never accepts text that tdlib
// rejects:
//   - A backslash escapes the next ASCII character.
//   - Code and pre spans escape only "\" and "`".
//   - A link is [text](url), and the url escapes only ")" and "\".
//   - "*", "_", "__", "~", and "||" open an entity, or close the entity on the
//     top of the stack.
//   - ">" and "**>" start a quote only at the start of a line. A newline ends
//     the quote when the next byte is not ">". At that newline, no other
//     entity can be open.
//   - A "||" that opens at the end of a quote is the mark of an expandable
//     quote.
//   - Any other reserved character must have an escape.
//
// It accepts a run of ">" at the start of a line, because the converter keeps
// up to three of them.
func checkMDV2(s string) error {
	var stack []string
	toggle := func(m string) {
		if n := len(stack); n > 0 && stack[n-1] == m {
			stack = stack[:n-1]
			return
		}
		stack = append(stack, m)
	}
	// quoteEnds reports whether the quote ends at byte i. tdlib continues a
	// quote only when a newline has ">" after it.
	quoteEnds := func(i int) bool {
		return i >= len(s) || (s[i] == '\n' && (i+1 == len(s) || s[i+1] != '>'))
	}
	lineStart, inQuote := true, false
	for i := 0; i < len(s); {
		if lineStart {
			lineStart = false
			j := i
			if strings.HasPrefix(s[i:], "**>") {
				j += 2
			}
			for j < len(s) && s[j] == '>' {
				j++
			}
			if j > i && s[j-1] == '>' {
				inQuote = true
				i = j
				continue
			}
		}
		c := s[i]
		switch {
		case c == '\n':
			if inQuote && quoteEnds(i) {
				if len(stack) > 0 {
					return fmt.Errorf("byte %d: the quote ends, but entities are open: %v", i, stack)
				}
				inQuote = false
			}
			lineStart = true
			i++
		case c == '\\':
			if i+1 >= len(s) || s[i+1] == 0 || s[i+1] >= utf8.RuneSelf {
				return fmt.Errorf("byte %d: a backslash does not escape an ASCII character", i)
			}
			i += 2
		case strings.HasPrefix(s[i:], "```"):
			end, err := scanMDV2Code(s, i+3, "```")
			if err != nil {
				return err
			}
			i = end
		case c == '`':
			end, err := scanMDV2Code(s, i+1, "`")
			if err != nil {
				return err
			}
			i = end
		case c == '[':
			stack = append(stack, "[")
			i++
		case c == ']':
			if n := len(stack); n == 0 || stack[n-1] != "[" {
				return fmt.Errorf("byte %d: \"]\" does not close a link text (open: %v)", i, stack)
			}
			stack = stack[:len(stack)-1]
			if i+1 >= len(s) || s[i+1] != '(' {
				return fmt.Errorf("byte %d: a link text has no url after it", i)
			}
			end, err := scanMDV2URL(s, i+2)
			if err != nil {
				return err
			}
			i = end
		case strings.HasPrefix(s[i:], "||"):
			if n := len(stack); inQuote && quoteEnds(i+2) && (n == 0 || stack[n-1] != "||") {
				// The mark of an expandable quote.
				i += 2
				continue
			}
			toggle("||")
			i += 2
		case strings.HasPrefix(s[i:], "__"):
			toggle("__")
			i += 2
		case c == '*' || c == '_' || c == '~':
			toggle(string(c))
			i++
		case strings.IndexByte(mdv2Reserved, c) >= 0:
			return fmt.Errorf("byte %d: the reserved character %q has no escape", i, c)
		default:
			i++
		}
	}
	if len(stack) > 0 {
		return fmt.Errorf("entities are not closed: %v", stack)
	}
	return nil
}

// scanMDV2Code returns the index after the code or pre span that starts at
// i, and that ends with end.
func scanMDV2Code(s string, i int, end string) (int, error) {
	start := i
	for i < len(s) {
		switch {
		case s[i] == '\\':
			i += 2
		case strings.HasPrefix(s[i:], end):
			return i + len(end), nil
		case s[i] == '`':
			return 0, fmt.Errorf("byte %d: a code span has a \"`\" without an escape", i)
		default:
			i++
		}
	}
	return 0, fmt.Errorf("byte %d: the code span that starts here has no end %q", start, end)
}

// scanMDV2URL returns the index after the link url that starts at i.
func scanMDV2URL(s string, i int) (int, error) {
	start := i
	for i < len(s) {
		switch s[i] {
		case '\\':
			i += 2
		case ')':
			return i + 1, nil
		default:
			i++
		}
	}
	return 0, fmt.Errorf("byte %d: the link url that starts here has no \")\"", start)
}

func TestCheckMDV2(t *testing.T) {
	good := []string{
		"plain text",
		`a\.b\!`,
		"*bold* _it_ __u__ ~s~ ||sp||",
		"*bold _it_*",
		"[a\\]b](https://x.y/a_(b\\))",
		"`a\\`b` ```go\nx\\`y```",
		"> quote\n**> hidden||\nend",
		"> *a\n> b*",
		"> a ||b||\nc",
		"> a||",
		"**> a\n> b||\nc",
		"> `a\nb` c\nd",
	}
	for _, s := range good {
		if err := checkMDV2(s); err != nil {
			t.Errorf("checkMDV2(%q) = %v, want nil", s, err)
		}
	}
	bad := []string{
		"a.b",
		"1+1",
		"a > b",
		"*open",
		"_a__b_",
		"[text]",
		"[text](url",
		"`code",
		"```a`b\nc```",
		"x\\",
		"(",
		"a|b",
		// tdlib ends a quote at a newline without ">" after it.
		"> [a\nb](https://x.y)",
		"> *a\nb*",
		// The mark opens a spoiler when the quote does not end after it.
		"**> a||\n> b",
		"> quote\n**> hidden||\n>> two",
	}
	for _, s := range bad {
		if err := checkMDV2(s); err == nil {
			t.Errorf("checkMDV2(%q) = nil, want an error", s)
		}
	}
}
