package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEscapesOutsideEntities(t *testing.T) {
	if got := ToMarkdownV2("Price: 1.5 (approx) - ok!"); got != `Price: 1\.5 \(approx\) \- ok\!` {
		t.Fatal(got)
	}
}

func TestBoldItalicCodeLink(t *testing.T) {
	got := ToMarkdownV2("**Done** *now* `a_b` [docs](https://go.dev/x_y)")
	if got != "*Done* _now_ `a_b` [docs](https://go.dev/x_y)" {
		t.Fatal(got)
	}
}

func TestFencedCodeKeepsContent(t *testing.T) {
	got := ToMarkdownV2("Run:\n```go\nx := a*b // (ok)!\n```\nDone.")
	if got != "Run:\n```go\nx := a*b // (ok)!\n```\nDone\\." {
		t.Fatal(got)
	}
	// Inside code, only \ and ` are escaped.
	if got := ToMarkdownV2("```\necho `hostname` C:\\x\n```"); got != "```\necho \\`hostname\\` C:\\\\x\n```" {
		t.Fatal(got)
	}
	if got := ToMarkdownV2("```\n```"); got != "```\n```" {
		t.Fatal(got)
	}
}

func TestHeadingBecomesBold(t *testing.T) {
	if got := ToMarkdownV2("## Result"); got != "*Result*" {
		t.Fatal(got)
	}
	if got := ToMarkdownV2("## **Important**"); got != "*Important*" {
		t.Fatal("inner bold must be stripped:", got)
	}
	if got := ToMarkdownV2("# Hello (World)!"); got != `*Hello \(World\)\!*` {
		t.Fatal(got)
	}
	if got := ToMarkdownV2("# First\nSome text\n## Second"); got != "*First*\nSome text\n*Second*" {
		t.Fatal(got)
	}
}

// The cases below are ported from Hermes tests/gateway/test_telegram_format.py.
func TestFormatHermesCases(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"Hello world", "Hello world"},
		{"Use `my_var` here", "Use `my_var` here"},
		{"Run `rm -rf ./*` carefully", "Run `rm -rf ./*` carefully"},
		{`Check ` + "`" + `C:\ProgramData\VMware\` + "`" + ` path`, `Check ` + "`" + `C:\\ProgramData\\VMware\\` + "`" + ` path`},
		{`Use ` + "`" + `\\server\share` + "`", `Use ` + "`" + `\\\\server\\share` + "`"},
		{"**hello.world!**", `*hello\.world\!*`},
		{"*hello.world*", `_hello\.world_`},
		{"🔄 **MCP Servers Reloaded**\n➕ Added: alpha*prod, agent_one, tool[beta]",
			"🔄 *MCP Servers Reloaded*\n➕ Added: alpha\\*prod, agent\\_one, tool\\[beta\\]"},
		{"[Hello!](https://example.com)", `[Hello\!](https://example.com)`},
		{"[link](https://example.com/path_(1))", `[link](https://example.com/path_\(1\))`},
		{"Visit [Google](https://google.com) today.", `Visit [Google](https://google.com) today\.`},
		{"Start *across\nlines* end", "Start \\*across\nlines\\* end"},
		{"This is ~~deleted~~ text", "This is ~deleted~ text"},
		{"`~~not struck~~`", "`~~not struck~~`"},
		{"||hello.world!||", `||hello\.world\!||`},
		{"> Hello (world)!", `> Hello \(world\)\!`},
		{">> Nested quote", ">> Nested quote"},
		{"5 > 3", `5 \> 3`},
		{"**> Hidden content||", "**> Hidden content||"},
		{"*> not a quote", `\*\> not a quote`},
		{"> not expandable||", `> not expandable\|\|`},
		{"```\n**not bold**\n```", "```\n**not bold**\n```"},
		{"`[not a link](url)`", "`[not a link](url)`"},
		{"****", `\*\*\*\*`},
		{"a\x00PH0\x00b", "aPH0b"},
	}
	for _, c := range cases {
		if got := ToMarkdownV2(c.in); got != c.want {
			t.Errorf("ToMarkdownV2(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestFormatBulletListNotItalic(t *testing.T) {
	got := ToMarkdownV2("* Item one\n* Item two")
	if got != "\\* Item one\n\\* Item two" {
		t.Fatal(got)
	}
}

func TestFormatPlaceholderCollision(t *testing.T) {
	in := "# Header\n**bold1** *italic1* `code1`\n**bold2** *italic2* `code2`\n```\nblock\n```\n[link](https://url.com)"
	want := "*Header*\n*bold1* _italic1_ `code1`\n*bold2* _italic2_ `code2`\n```\nblock\n```\n[link](https://url.com)"
	if got := ToMarkdownV2(in); got != want {
		t.Fatalf("got %q", got)
	}
}

func TestFormatTables(t *testing.T) {
	in := "Scores:\n\n| Player | Score |\n|--------|-------|\n| Alice  | 150   |\n| Bob    | 120   |\n\nEnd."
	want := "Scores:\n\n**Alice**\n• Score: 150\n\n**Bob**\n• Score: 120\n\nEnd."
	if got := wrapMarkdownTables(in); got != want {
		t.Fatalf("got %q", got)
	}
	label := "|        | Score | Rank |\n|--------|-------|------|\n| Alice  | 150   | 1    |\n"
	if got := wrapMarkdownTables(label); !strings.Contains(got, "**Alice**\n• Score: 150\n• Rank: 1") {
		t.Fatalf("row label: %q", got)
	}
	for _, same := range []string{
		"Use the | pipe operator to chain commands.",
		"Section A\n\n---\n\nSection B",
		"```\n| a | b |\n|---|---|\n| 1 | 2 |\n```",
		"a | b\nc | d",
		"| a |\n| - |\n| b |",
	} {
		if got := wrapMarkdownTables(same); got != same {
			t.Errorf("changed %q to %q", same, got)
		}
	}
	got := ToMarkdownV2("| A | B |\n|---|---|\n| 1 | 2 |\n\nNice **work** team!")
	if got != "*1*\n• B: 2\n\nNice *work* team\\!" {
		t.Fatalf("got %q", got)
	}
}

func TestSplitPrefersNewline(t *testing.T) {
	s := strings.Repeat("a", 3000) + "\n" + strings.Repeat("b", 3000)
	p := SplitUTF16(s, 4096)
	if len(p) != 2 || !strings.HasSuffix(p[0], "a") || strings.Join(p, "") != s {
		t.Fatal(len(p))
	}
}

func TestSplitHardWithoutNewline(t *testing.T) {
	s := strings.Repeat("a", 10000)
	p := SplitUTF16(s, 4096)
	if len(p) != 3 || UTF16Len(p[0]) != 4096 || UTF16Len(p[1]) != 4096 || strings.Join(p, "") != s {
		t.Fatal(len(p))
	}
	// A newline in the first half of the window gives too small a part.
	s = "intro\n" + strings.Repeat("a", 5000)
	if p := SplitUTF16(s, 4096); UTF16Len(p[0]) != 4096 {
		t.Fatal(UTF16Len(p[0]))
	}
	if p := SplitUTF16("short", 4096); len(p) != 1 || p[0] != "short" {
		t.Fatal(p)
	}
}

// A split never cuts a surrogate pair at the border.
func TestSplitNeverCutsSurrogatePair(t *testing.T) {
	for _, pad := range []int{4094, 4095, 4096} {
		s := strings.Repeat("x", pad) + "😀" + strings.Repeat("y", 10) // 😀 = 2 UTF-16 units
		parts := SplitUTF16(s, 4096)
		for _, part := range parts {
			if !utf8.ValidString(part) || UTF16Len(part) > 4096 {
				t.Fatal("bad part at pad", pad)
			}
		}
		if strings.Join(parts, "") != s {
			t.Fatal("lossless at pad", pad)
		}
	}
	// Only emoji: every part has an even number of units and valid UTF-8.
	s := strings.Repeat("👍🏽", 3000)
	parts := SplitUTF16(s, 4096)
	if strings.Join(parts, "") != s || len(parts) < 2 {
		t.Fatal("lossless emoji")
	}
	for _, part := range parts {
		if !utf8.ValidString(part) || UTF16Len(part) > 4096 {
			t.Fatal("bad emoji part")
		}
	}
	// UTF16Len counts surrogate pairs as 2 and BMP runes as 1.
	if UTF16Len("a😀é") != 4 {
		t.Fatal(UTF16Len("a😀é"))
	}
}

func TestSplitKeepsEscapeWithItsCharacter(t *testing.T) {
	s := strings.Repeat("a", 4095) + `\.` + "b"
	p := SplitUTF16(s, 4096)
	if strings.HasSuffix(p[0], `\`) || strings.Join(p, "") != s {
		t.Fatalf("part ends with an escape: %q", p[0][len(p[0])-3:])
	}
}

func TestNumberPartsEscaped(t *testing.T) {
	if got := NumberParts([]string{"a", "b"}, true); got[0] != `a \(1/2\)` || got[1] != `b \(2/2\)` {
		t.Fatal(got)
	}
	if got := NumberParts([]string{"a", "b"}, false); got[0] != "a (1/2)" {
		t.Fatal(got)
	}
	if got := NumberParts([]string{"a"}, true); got[0] != "a" {
		t.Fatal("one part gets no suffix:", got)
	}
}

func TestStripMarkdownV2(t *testing.T) {
	cases := []struct{ in, want string }{
		{`*Done* 1\.5`, "Done 1.5"},
		{`hello\.world\!`, "hello.world!"},
		{"*bold* and _italic_", "bold and italic"},
		{"my_variable_name", "my_variable_name"},
		{"some_func_call here", "some_func_call here"},
		{"~struck text~", "struck text"},
		{"||hidden text||", "hidden text"},
		{"", ""},
	}
	for _, c := range cases {
		if got := StripMarkdownV2(c.in); got != c.want {
			t.Errorf("StripMarkdownV2(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func FuzzSplitUTF16(f *testing.F) {
	f.Add("ab😀\ncd\\.e", 3)
	f.Add(strings.Repeat("x", 9)+"😀", 10)
	f.Fuzz(func(t *testing.T, s string, limit int) {
		if !utf8.ValidString(s) || limit < 2 || limit > 64 {
			return
		}
		parts := SplitUTF16(s, limit)
		if strings.Join(parts, "") != s {
			t.Fatal("split is not lossless")
		}
		for _, p := range parts {
			if !utf8.ValidString(p) || UTF16Len(p) > limit || (p == "" && s != "") {
				t.Fatalf("bad part %q", p)
			}
		}
		_ = ToMarkdownV2(s)
		_ = StripMarkdownV2(s)
	})
}

// tdlib ends a quote at a newline that has no ">" after it. At that newline, an
// open entity is an error (parse_markdown_v2 in td/telegram/MessageEntity.cpp).
func TestFormatQuoteEnd(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"link text over two lines in a quote", "> [a\nb](https://x.y)", "> [a b](https://x.y)"},
		{"link text over two quote lines", "> [a\n> b](https://x.y)", "> [a \\> b](https://x.y)"},
		{"link text over two lines outside a quote", "[a\nb](https://x.y)", "[a\nb](https://x.y)"},
		// The mark opens a spoiler when the quote continues on the next line.
		{"mark before a quote line", "**> a||\n> b", "**> a\\|\\|\n> b"},
		{"mark before a text line", "**> a||\nb", "**> a||\nb"},
		{"mark before an escaped >", "**> a||\n>b", "**> a||\n\\>b"},
		{"mark before an expandable quote", "**> a||\n**> b||", "**> a||\n**> b||"},
	}
	for _, c := range cases {
		got := ToMarkdownV2(c.in)
		if err := checkMDV2(got); err != nil {
			t.Errorf("%s: checkMDV2(%q) = %v", c.name, got, err)
		}
		if got != c.want {
			t.Errorf("%s: ToMarkdownV2(%q)\n got %q\nwant %q", c.name, c.in, got, c.want)
		}
	}
}

// The cases below are inputs that FuzzMarkdownV2 found. Each output must be
// valid MarkdownV2 for checkMDV2 and must match the expected text.
func TestFormatFuzzRegressions(t *testing.T) {
	longURL := "0" + strings.Repeat("L", 2000)
	cases := []struct{ name, in, want string }{
		// Telegram reads "__" as underline. An empty bold entity separates
		// two italic entities (Bot API, MarkdownV2 style).
		{"adjacent italics", "*0**0*", "_0_**_0_"},
		// Inline code must not match across the placeholder of a fenced
		// block, or the code spans nest.
		{"inline code around a fence", "`0```0````", "\\`0```\n0```\\`"},
		// Telegram reads the text after ``` as the language. A language
		// with "`" is not possible, so the line becomes code text.
		{"backtick on the language line", "````\n```", "```\n\\`\n```"},
		{"language keeps its name", "```go \nx\n```", "```go\nx\n```"},
		{"language with a space", "```go run\nx\n```", "```\ngo run\nx\n```"},
		{"one-line fence", "```go x```", "```\ngo x```"},
		// The close of a long link URL must stay without an escape.
		{"long link url", "[0](" + longURL + ")", "[0](" + longURL + ")"},
		// A link URL is plain text. Code in it does not make a link.
		{"code in a link url", "[0](`0`)", "\\[0\\]\\(`0`\\)"},
	}
	for _, c := range cases {
		got := ToMarkdownV2(c.in)
		if err := checkMDV2(got); err != nil {
			t.Errorf("%s: checkMDV2(%q) = %v", c.name, got, err)
		}
		if got != c.want {
			t.Errorf("%s: ToMarkdownV2(%q)\n got %q\nwant %q", c.name, c.in, got, c.want)
		}
	}
}

// Reply text escapes user text with gateway.EscapeMarkdown. A backslash before one of
// \ * _ ` [ ] | gives the literal character.
func TestFormatBackslashEscapes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"**Fix my\\_project**", `*Fix my\_project*`},
		{`a\*b\*c`, `a\*b\*c`},
		{`\*\*draft\*\*`, `\*\*draft\*\*`},
		{`\[x\](y)`, `\[x\]\(y\)`},
		{`a\|b`, `a\|b`},
		{`\|\|not a spoiler\|\|`, `\|\|not a spoiler\|\|`},
		{`C:\\dir`, `C:\\dir`},
		{"\\`x\\` and `code`", "\\`x\\` and `code`"},
		{"`a\\_b`", "`a\\\\_b`"},
		{`\(x\)`, `\\\(x\\\)`},
		{"_Fix my\\_project_", `_Fix my\_project_`},
		{"# Title \\*x\\*", `*Title \*x\**`},
		{"[a\\]b](https://x.y)", `[a\]b](https://x.y)`},
		{`end\`, `end\\`},
	}
	for _, c := range cases {
		got := ToMarkdownV2(c.in)
		if err := checkMDV2(got); err != nil {
			t.Errorf("checkMDV2(%q) = %v", got, err)
		}
		if got != c.want {
			t.Errorf("ToMarkdownV2(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

// "_x_" at the edges of a word is italic. A "_" inside a word stays literal.
func TestFormatUnderscoreItalic(t *testing.T) {
	cases := []struct{ in, want string }{
		{"_2 min · step 4/90 · web_fetch_", `_2 min · step 4/90 · web\_fetch_`},
		{"_a_ and _b_", "_a_ and _b_"},
		{"(_note_)", `\(_note_\)`},
		{"**bold** _it_", "*bold* _it_"},
		{"> _quoted_", "> _quoted_"},
		{"agent_one", `agent\_one`},
		{"snake_case_name", `snake\_case\_name`},
		{"__init__", `\_\_init\_\_`},
		{"_private_var", `\_private\_var`},
		{"_ not italic _", `\_ not italic \_`},
		{"_open only", `\_open only`},
		{"_x\ny_", "\\_x\ny\\_"},
		{"_a__", `\_a\_\_`},
		{"_a_*b*", "_a_**_b_"},
		{"*a*_b_", "_a_**_b_"},
	}
	for _, c := range cases {
		got := ToMarkdownV2(c.in)
		if err := checkMDV2(got); err != nil {
			t.Errorf("checkMDV2(%q) = %v", got, err)
		}
		if got != c.want {
			t.Errorf("ToMarkdownV2(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestFormatTableEscapedPipe(t *testing.T) {
	got := ToMarkdownV2("| A | B |\n|---|---|\n| a\\|b | 2 |")
	if got != "*a\\|b*\n• B: 2" {
		t.Fatalf("got %q", got)
	}
}

// A bare ">" line keeps a quote open over an empty line, as in a quoted
// prompt with two paragraphs.
func TestFormatQuoteEmptyLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"> a\n>\n> b", "> a\n> \n> b"},
		{"> a\n>\n>\n> b", "> a\n> \n> \n> b"},
		{"**> a||\n>\n> b", "**> a\\|\\|\n> \n> b"},
		{"**> a\n>\n> b||", "**> a\n> \n> b\\|\\|"},
		// A bare ">" that does not follow a quote line is text.
		{"a\n>\nb", "a\n\\>\nb"},
	}
	for _, c := range cases {
		got := ToMarkdownV2(c.in)
		if err := checkMDV2(got); err != nil {
			t.Errorf("checkMDV2(%q) = %v", got, err)
		}
		if got != c.want {
			t.Errorf("ToMarkdownV2(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

// An escaped tilde shows literally and does not start a strikethrough.
func TestFormatEscapedTilde(t *testing.T) {
	got := ToMarkdownV2(`**\~\~old\~\~ plan**`)
	if want := `*\~\~old\~\~ plan*`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
