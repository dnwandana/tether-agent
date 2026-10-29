package telegram

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// checkParts checks that each part is not empty, fits in limit UTF-16 units,
// is valid UTF-8, and is valid MarkdownV2 with no open entity.
func checkParts(t *testing.T, parts []string, limit int) {
	t.Helper()
	if len(parts) == 0 {
		t.Fatal("no parts")
	}
	for i, p := range parts {
		if p == "" {
			t.Errorf("part %d is empty", i)
		}
		if n := UTF16Len(p); n > limit {
			t.Errorf("part %d has %d units, limit %d", i, n, limit)
		}
		if !utf8.ValidString(p) {
			t.Errorf("part %d is not valid UTF-8", i)
		}
		if err := checkMDV2(p); err != nil {
			t.Errorf("part %d: %v\npart: %q", i, err, p)
		}
	}
}

// squash removes each space and newline, so that a test can compare the text
// of the parts with the input.
func squash(s string) string {
	return strings.NewReplacer(" ", "", "\n", "").Replace(s)
}

func TestSplitMarkdownV2ProbeCases(t *testing.T) {
	budget := maxMessageLen - numberRoom
	pad := strings.Repeat("a", 4070)
	cases := []struct{ name, in, part2Prefix string }{
		{"link", pad + " see [LinkSwift](https://github.com/x/y) end", "[LinkSwift](https://github.com/x/y)"},
		{"bold", pad + " **very bold words here** end", "*very bold words here*"},
		{"inline code", strings.Repeat("b", 4060) + " run `go test ./internal/... -run X` end", "`go test"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			formatted := ToMarkdownV2(c.in)
			parts := SplitMarkdownV2(formatted, budget)
			if len(parts) != 2 {
				t.Fatalf("got %d parts, want 2", len(parts))
			}
			checkParts(t, parts, budget)
			if !strings.HasPrefix(parts[1], c.part2Prefix) {
				t.Errorf("part 2 starts with %q, want %q", head(parts[1], 40), c.part2Prefix)
			}
			if squash(strings.Join(parts, "")) != squash(formatted) {
				t.Error("the parts lose or change text")
			}
		})
	}
}

func TestSplitMarkdownV2KeepsWords(t *testing.T) {
	in := strings.Repeat("word ", 1000)
	parts := SplitMarkdownV2(ToMarkdownV2(in), maxMessageLen-numberRoom)
	if len(parts) < 2 {
		t.Fatalf("got %d parts, want 2 or more", len(parts))
	}
	checkParts(t, parts, maxMessageLen-numberRoom)
	for i, p := range parts {
		if i < len(parts)-1 && !strings.HasSuffix(p, "word") {
			t.Errorf("part %d ends with %q", i, tail(p, 10))
		}
		if i > 0 && !strings.HasPrefix(p, "word") {
			t.Errorf("part %d starts with %q", i, head(p, 10))
		}
	}
}

func TestSplitMarkdownV2Edges(t *testing.T) {
	const limit = 100

	t.Run("fits", func(t *testing.T) {
		in := strings.Repeat("x", 50)
		if got := SplitMarkdownV2(in, limit); len(got) != 1 || got[0] != in {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("escape edge", func(t *testing.T) {
		parts := SplitMarkdownV2(ToMarkdownV2(strings.Repeat("a", 99)+"."), limit)
		checkParts(t, parts, limit)
		for i, p := range parts {
			n := len(p) - len(strings.TrimRight(p, `\`))
			if n%2 == 1 {
				t.Errorf("part %d ends with an escape backslash: %q", i, tail(p, 10))
			}
		}
	})

	t.Run("emoji edge", func(t *testing.T) {
		checkParts(t, SplitMarkdownV2(strings.Repeat("a", 99)+"😀b", limit), limit)
	})

	t.Run("long bold", func(t *testing.T) {
		formatted := ToMarkdownV2("**" + strings.Repeat("b", 300) + "**")
		parts := SplitMarkdownV2(formatted, limit)
		for i, p := range parts {
			if p == "" || UTF16Len(p) > limit {
				t.Errorf("part %d has %d units", i, UTF16Len(p))
			}
		}
		if squash(strings.Join(parts, "")) != squash(formatted) {
			t.Error("the parts lose or change text")
		}
	})

	t.Run("link in bold at the cut", func(t *testing.T) {
		parts := SplitMarkdownV2(ToMarkdownV2(strings.Repeat("a", 80)+" **bold [l](http://u) bold** end"), limit)
		checkParts(t, parts, limit)
		if len(parts) < 2 || !strings.HasPrefix(parts[1], "*bold") {
			t.Fatalf("parts = %q", parts)
		}
	})

	t.Run("quotes", func(t *testing.T) {
		in := strings.TrimSuffix(strings.Repeat("> quote line\n", 30), "\n")
		parts := SplitMarkdownV2(ToMarkdownV2(in), limit)
		checkParts(t, parts, limit)
		if len(parts) < 2 {
			t.Fatalf("got %d parts", len(parts))
		}
		for i, p := range parts[:len(parts)-1] {
			if !strings.HasSuffix(p, "quote line") {
				t.Errorf("part %d ends with %q", i, tail(p, 12))
			}
		}
	})

	t.Run("only newlines", func(t *testing.T) {
		parts := SplitMarkdownV2(ToMarkdownV2(strings.Repeat("\n", 300)), limit)
		if len(parts) < 2 {
			t.Fatalf("got %d parts", len(parts))
		}
		for i, p := range parts {
			if p == "" || UTF16Len(p) > limit {
				t.Errorf("part %d has %d units", i, UTF16Len(p))
			}
		}
	})

	t.Run("only spaces", func(t *testing.T) {
		parts := SplitMarkdownV2(strings.Repeat(" ", 300), limit)
		for i, p := range parts {
			if p == "" || UTF16Len(p) > limit {
				t.Errorf("part %d has %d units", i, UTF16Len(p))
			}
		}
	})

	t.Run("short code block at the cut", func(t *testing.T) {
		parts := SplitMarkdownV2(ToMarkdownV2(strings.Repeat("a", 90)+"\n```\ncode\n```\nend"), limit)
		checkParts(t, parts, limit)
		if len(parts) < 2 || !strings.HasPrefix(parts[1], "```") {
			t.Fatalf("parts = %q", parts)
		}
	})
}

func TestSplitMarkdownV2SmallLimit(t *testing.T) {
	s := "ab😀\ncd\\.e"
	if got, want := SplitMarkdownV2(s, 10), SplitUTF16(s, 10); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	long := strings.Repeat("ab *c* ", 40)
	if got, want := SplitMarkdownV2(long, minMarkdownSplit-1), SplitUTF16(long, minMarkdownSplit-1); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func head(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func TestSplitMarkdownV2FenceCarry(t *testing.T) {
	budget := maxMessageLen - numberRoom
	in := strings.Repeat("line\n", 700) + "```go\n" + strings.Repeat("x := f(1)\n", 300) + "```\n"
	parts := SplitMarkdownV2(ToMarkdownV2(in), budget)
	checkParts(t, parts, budget)
	if len(parts) < 2 {
		t.Fatalf("got %d parts", len(parts))
	}
	if !strings.HasSuffix(parts[0], "\n```") {
		t.Errorf("part 1 ends with %q", tail(parts[0], 20))
	}
	if !strings.HasPrefix(parts[1], "```go\n") {
		t.Errorf("part 2 starts with %q", head(parts[1], 20))
	}
}

func TestSplitMarkdownV2FenceCases(t *testing.T) {
	const limit = 400
	split := func(t *testing.T, in string) []string {
		t.Helper()
		parts := SplitMarkdownV2(ToMarkdownV2(in), limit)
		checkParts(t, parts, limit)
		if len(parts) < 2 {
			t.Fatalf("got %d parts", len(parts))
		}
		return parts
	}

	t.Run("no language", func(t *testing.T) {
		parts := split(t, "```\n"+strings.Repeat("code line\n", 120)+"```")
		for i, p := range parts[1:] {
			if !strings.HasPrefix(p, "```\n") {
				t.Errorf("part %d starts with %q", i+1, head(p, 20))
			}
		}
	})

	t.Run("three parts", func(t *testing.T) {
		parts := split(t, "```go\n"+strings.Repeat("x := f(1)\n", 100)+"```")
		if len(parts) < 3 {
			t.Fatalf("got %d parts, want 3 or more", len(parts))
		}
		for i, p := range parts {
			if !strings.HasPrefix(p, "```go\n") {
				t.Errorf("part %d starts with %q", i, head(p, 20))
			}
			if i < len(parts)-1 && !strings.HasSuffix(p, "\n```") {
				t.Errorf("part %d ends with %q", i, tail(p, 20))
			}
		}
	})

	t.Run("indentation", func(t *testing.T) {
		parts := split(t, "```\n"+strings.Repeat("    indented()\n", 100)+"```")
		for i, p := range parts {
			for _, line := range strings.Split(p, "\n") {
				if !strings.HasPrefix(line, "```") && !strings.HasPrefix(line, "    indented()") {
					t.Errorf("part %d has the line %q", i, line)
				}
			}
		}
	})

	t.Run("long language", func(t *testing.T) {
		parts := split(t, "```"+strings.Repeat("l", 120)+"\n"+strings.Repeat("y\n", 300)+"```")
		if !strings.HasPrefix(parts[0], "```"+strings.Repeat("l", 120)+"\n") {
			t.Errorf("part 1 starts with %q", head(parts[0], 20))
		}
		for i, p := range parts[1:] {
			if !strings.HasPrefix(p, "```\n") {
				t.Errorf("part %d starts with %q", i+1, head(p, 20))
			}
		}
	})

	t.Run("one long code line", func(t *testing.T) {
		parts := split(t, "```\n"+strings.Repeat("y", 1000)+"\n```")
		if !strings.HasPrefix(parts[1], "```\n") {
			t.Errorf("part 2 starts with %q", head(parts[1], 20))
		}
		if got := strings.ReplaceAll(strings.Join(parts, ""), "\n``````\n", ""); !strings.Contains(got, strings.Repeat("y", 1000)) {
			t.Error("the parts lose or change code")
		}
	})

	t.Run("escaped backtick at the edge", func(t *testing.T) {
		split(t, "```\n"+strings.Repeat("`", 600)+"\n```")
	})

	// The plan case above can put the edge between two escape pairs. Each
	// offset of the edge here, from 0 to 3 bytes of text before the pairs,
	// puts the edge between pairs or inside a pair.
	for pre := 0; pre < 4; pre++ {
		t.Run("escape pair at the edge "+strings.Repeat("x", pre), func(t *testing.T) {
			body := strings.Repeat("x", pre) + strings.Repeat("a`", 400)
			parts := split(t, "```\n"+body+"\n```")
			var got strings.Builder
			for i, p := range parts {
				if !strings.HasPrefix(p, "```\n") || !strings.HasSuffix(p, "\n```") {
					t.Errorf("part %d is %q ... %q", i, head(p, 10), tail(p, 10))
				}
				got.WriteString(strings.TrimSuffix(strings.TrimPrefix(p, "```\n"), "\n```"))
			}
			if want := strings.ReplaceAll(body, "`", "\\`"); got.String() != want {
				t.Error("the parts lose or change code")
			}
		})
	}

	t.Run("closing fence at the edge", func(t *testing.T) {
		// The close of the code block ends at unit 399, and words come after
		// it on the same line.
		in := "```\n" + strings.Repeat("y\n", 196) + "```" + strings.Repeat(" word", 40)
		parts := split(t, in)
		for i, p := range parts {
			if strings.Contains(p, "```\n```") {
				t.Errorf("part %d has an empty code block: %q", i, p)
			}
		}
	})

	t.Run("empty first code line at the edge", func(t *testing.T) {
		// The body of the code block starts at unit 387 with an empty line.
		// A cut there gives an empty code block and loses the empty line.
		in := strings.Repeat("a", 380) + "\n```go\n\n" + strings.Repeat("z", 500) + "\n```"
		parts := split(t, in)
		if parts[0] != strings.Repeat("a", 380) {
			t.Errorf("part 1 ends with %q", tail(parts[0], 20))
		}
		if !strings.HasPrefix(parts[1], "```go\n\nz") {
			t.Errorf("part 2 starts with %q", head(parts[1], 20))
		}
	})
}

func TestSplitMarkdownV2NoCutInCloseFence(t *testing.T) {
	// The close fence comes right after the code, at the limit. The cut must
	// stay in the code, not go into the close fence.
	in := "```\n" + strings.Repeat("0", 58) + "```"
	parts := SplitMarkdownV2(in, 64)
	checkParts(t, parts, 64)
	if len(parts) != 2 || !strings.HasSuffix(parts[1], "```") {
		t.Fatalf("%q", parts)
	}
}
