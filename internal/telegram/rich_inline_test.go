package telegram

import (
	"sort"
	"strings"
	"testing"
)

// inlineSpanOf returns the RichText value of spanJSON, as the parse gives it.
func inlineSpanOf(t *testing.T, spanJSON string) any {
	t.Helper()
	d := richDocOf(t, `[{"type":"paragraph","text":`+spanJSON+`}]`)
	if len(d.blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(d.blocks))
	}
	return richMap(d.blocks[0])["text"]
}

// inlineUnknownOf returns the sorted names in c.unknown.
func inlineUnknownOf(c *richCtx) []string {
	var out []string
	for name := range c.unknown {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func TestRichInlineSpanTypes(t *testing.T) {
	cases := []struct{ name, span, want string }{
		{"bold", `{"type":"bold","text":"b"}`, "**b**"},
		{"italic", `{"type":"italic","text":"i"}`, "*i*"},
		{"underline", `{"type":"underline","text":"u"}`, "u"},
		{"strikethrough", `{"type":"strikethrough","text":"s"}`, "~~s~~"},
		{"spoiler", `{"type":"spoiler","text":"sp"}`, "sp"},
		{"subscript", `{"type":"subscript","text":"2"}`, "2"},
		{"superscript", `{"type":"superscript","text":"3"}`, "3"},
		{"marked", `{"type":"marked","text":"m"}`, "m"},
		{"code", `{"type":"code","text":"c"}`, "`c`"},
		{"url", `{"type":"url","text":"a link","url":"https://t.me/"}`, "[a link](https://t.me/)"},
		{"mention", `{"type":"mention","text":"@ana","username":"ana"}`, "@ana"},
		{"text_mention", `{"type":"text_mention","text":"Ana","user":{"id":123,"first_name":"Ana"}}`, "Ana"},
		{"bot_command", `{"type":"bot_command","text":"/status","bot_command":"status"}`, "/status"},
		{"hashtag", `{"type":"hashtag","text":"#go","hashtag":"go"}`, "#go"},
		{"cashtag", `{"type":"cashtag","text":"$TON","cashtag":"TON"}`, "$TON"},
		{"email_address", `{"type":"email_address","text":"inline e-mail","email_address":"user@example.com"}`, "inline e-mail (user@example.com)"},
		{"phone_number", `{"type":"phone_number","text":"call +15550100","phone_number":"+15550100"}`, "call +15550100"},
		{"bank_card_number", `{"type":"bank_card_number","text":"my card","bank_card_number":"4242424242424242"}`, "my card (4242424242424242)"},
		{"ton_address", `{"type":"ton_address","text":"wallet","address":"UQAbc"}`, "wallet (UQAbc)"},
		{"date_time", `{"type":"date_time","text":"10 Nov 2026","unix_time":1794268800,"date_time_format":"d"}`, "10 Nov 2026"},
		{"custom_emoji", `{"type":"custom_emoji","custom_emoji_id":"5368324170671202286","alternative_text":"👍"}`, "👍"},
		{"mathematical_expression", `{"type":"mathematical_expression","expression":"x^2"}`, "$x^2$"},
		{"button", `{"type":"button","button":{"text":"Open","url":"https://t.me/"}}`, "[button: Open]"},
		{"anchor", `{"type":"anchor","name":"top"}`, ""},
		{"anchor_link", `{"type":"anchor_link","text":"go up","anchor_name":"top"}`, "go up"},
		{"reference", `{"type":"reference","text":"[1]","name":"n1"}`, "[1]"},
		{"reference_link", `{"type":"reference_link","text":"see 1","reference_name":"n1"}`, "see 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newRichCtx("")
			if got := c.inline(inlineSpanOf(t, tc.span), 0); got != tc.want {
				t.Errorf("inline = %q, want %q", got, tc.want)
			}
			if len(c.unknown) != 0 {
				t.Errorf("unknown = %v, want none", inlineUnknownOf(c))
			}
		})
	}
}

func TestRichInlineStringAndArray(t *testing.T) {
	cases := []struct{ name, span, want string }{
		{"string, no escape", `"2 * 3 _is_ #6"`, "2 * 3 _is_ #6"},
		{"array", `["a", "b", {"type":"bold","text":"c"}, ["d", "e"]]`, "ab**c**de"},
		{"empty array", `[]`, ""},
		{"number", `5`, ""},
		{"null", `null`, ""},
		{"object with no type", `{"text":"x"}`, "x"},
		{"wrong text type", `{"type":"bold","text":5}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newRichCtx("")
			if got := c.inline(inlineSpanOf(t, tc.span), 0); got != tc.want {
				t.Errorf("inline = %q, want %q", got, tc.want)
			}
			if len(c.unknown) != 0 {
				t.Errorf("unknown = %v, want none", inlineUnknownOf(c))
			}
		})
	}
}

func TestRichInlineMarkers(t *testing.T) {
	cases := []struct{ name, span, want string }{
		{"bold in italic in a link",
			`{"type":"url","url":"https://example.com","text":{"type":"italic","text":["see ",{"type":"bold","text":"this"}]}}`,
			"[*see **this***](https://example.com)"},
		{"bold with italic", `{"type":"bold","text":["bold ",{"type":"italic","text":"bold italic"}]}`, "**bold *bold italic***"},
		{"white space at the end", `{"type":"bold","text":"word "}`, "**word** "},
		{"white space at the start", `{"type":"italic","text":" word"}`, " *word*"},
		{"white space on the two sides", `{"type":"strikethrough","text":"\n old \t"}`, "\n ~~old~~ \t"},
		{"white space only", `{"type":"bold","text":"  "}`, "  "},
		{"empty bold", `{"type":"bold","text":""}`, ""},
		{"empty italic, no text key", `{"type":"italic"}`, ""},
		{"empty bold around an empty array", `{"type":"bold","text":[]}`, ""},
		{"empty code", `{"type":"code","text":""}`, ""},
		{"empty expression", `{"type":"mathematical_expression"}`, ""},
		{"button with no text", `{"type":"button","button":{"url":"https://t.me/"}}`, ""},
		{"button with a wrong value", `{"type":"button","button":5}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := newRichCtx("").inline(inlineSpanOf(t, tc.span), 0); got != tc.want {
				t.Errorf("inline = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRichInlineCode(t *testing.T) {
	cases := []struct{ name, span, want string }{
		{"one backtick inside", `{"type":"code","text":"a` + "`" + `b"}`, "``a`b``"},
		{"two backticks inside", `{"type":"code","text":"a` + "``" + `b"}`, "```a``b```"},
		{"backtick at the start", `{"type":"code","text":"` + "`" + `a"}`, "`` `a ``"},
		{"backtick at the end", `{"type":"code","text":"a` + "`" + `"}`, "`` a` ``"},
		{"white space stays inside", `{"type":"code","text":" a "}`, "` a `"},
		{"a span inside gives plain text", `{"type":"code","text":["x = ",{"type":"bold","text":"1"},{"type":"url","text":" link","url":"https://t.me/"}]}`, "`x = 1 link`"},
		{"markers of the user stay", `{"type":"code","text":"**a**"}`, "`**a**`"},
		{"code inside bold", `{"type":"bold","text":["run ",{"type":"code","text":"go test"}]}`, "**run `go test`**"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := newRichCtx("").inline(inlineSpanOf(t, tc.span), 0); got != tc.want {
				t.Errorf("inline = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRichInlineURL(t *testing.T) {
	cases := []struct{ name, span, want string }{
		{"the text is the URL", `{"type":"url","text":"https://t.me/","url":"https://t.me/"}`, "https://t.me/"},
		{"the text is the URL with a space", `{"type":"url","text":"https://t.me/ ","url":"https://t.me/"}`, "https://t.me/ "},
		{"white space goes outside", `{"type":"url","text":"docs ","url":"https://t.me/"}`, "[docs](https://t.me/) "},
		{"no URL", `{"type":"url","text":"docs"}`, "docs"},
		{"no text", `{"type":"url","url":"https://t.me/"}`, "https://t.me/"},
		{"no text and no URL", `{"type":"url"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := newRichCtx("").inline(inlineSpanOf(t, tc.span), 0); got != tc.want {
				t.Errorf("inline = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRichInlineValueSpans(t *testing.T) {
	cases := []struct{ name, span, want string }{
		{"the text is the value", `{"type":"email_address","text":"user@example.com","email_address":"user@example.com"}`, "user@example.com"},
		{"the text contains the value", `{"type":"email_address","text":"mail user@example.com now","email_address":"user@example.com"}`, "mail user@example.com now"},
		{"no value", `{"type":"phone_number","text":"my phone"}`, "my phone"},
		{"no text", `{"type":"phone_number","phone_number":"+15550100"}`, "+15550100"},
		{"white space goes outside", `{"type":"bank_card_number","text":"card ","bank_card_number":"4242"}`, "card (4242) "},
		{"a style in the text", `{"type":"ton_address","text":{"type":"bold","text":"wallet"},"address":"UQAbc"}`, "**wallet** (UQAbc)"},
		{"ton_address ignores the key ton_address", `{"type":"ton_address","text":"wallet","ton_address":"UQAbc"}`, "wallet"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := newRichCtx("").inline(inlineSpanOf(t, tc.span), 0); got != tc.want {
				t.Errorf("inline = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRichInlineUnknownType(t *testing.T) {
	cases := []struct{ name, span, want string }{
		{"text", `{"type":"glow","text":["a",{"type":"bold","text":"b"}]}`, "a**b**"},
		{"children", `{"type":"glow","children":["a",{"type":"italic","text":"b"}]}`, "a*b*"},
		{"text wins over children", `{"type":"glow","text":"t","children":"c"}`, "t"},
		{"url", `{"type":"glow","url":"https://t.me/"}`, "https://t.me/"},
		{"expression", `{"type":"glow","expression":"x^2"}`, "x^2"},
		{"alternative_text", `{"type":"glow","alternative_text":"alt"}`, "alt"},
		{"no known key", `{"type":"glow","name":"n"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newRichCtx("")
			if got := c.inline(inlineSpanOf(t, tc.span), 0); got != tc.want {
				t.Errorf("inline = %q, want %q", got, tc.want)
			}
			if got := strings.Join(inlineUnknownOf(c), ","); got != "glow" {
				t.Errorf("unknown = %q, want %q", got, "glow")
			}
		})
	}
}

func TestRichInlineUnknownNames(t *testing.T) {
	c := newRichCtx("")
	v := inlineSpanOf(t, `[{"type":"glow","text":"a"},{"type":"bold","text":{"type":"wave","text":"b"}},{"type":"code","text":{"type":"dim","text":"c"}},{"type":"glow","text":"d"}]`)
	if got, want := c.inline(v, 0), "a**b**`c`d"; got != want {
		t.Errorf("inline = %q, want %q", got, want)
	}
	if got, want := strings.Join(inlineUnknownOf(c), ","), "dim,glow,wave"; got != want {
		t.Errorf("unknown = %q, want %q", got, want)
	}
}

func TestRichInlinePlain(t *testing.T) {
	cases := []struct{ name, span, want string }{
		{"string", `"a *b*"`, "a *b*"},
		{"array with styles", `["a ",{"type":"bold","text":["b ",{"type":"italic","text":"c"}]},{"type":"code","text":" d"}]`, "a b c d"},
		{"url gives the text only", `{"type":"url","text":"docs","url":"https://t.me/"}`, "docs"},
		{"email_address gives the text only", `{"type":"email_address","text":"mail","email_address":"user@example.com"}`, "mail"},
		{"custom_emoji", `{"type":"custom_emoji","alternative_text":"👍"}`, "👍"},
		{"mathematical_expression", `{"type":"mathematical_expression","expression":"x^2"}`, "x^2"},
		{"button", `{"type":"button","button":{"text":"Open"}}`, "Open"},
		{"anchor", `{"type":"anchor","name":"top"}`, ""},
		{"unknown type with children", `{"type":"glow","children":["a","b"]}`, "ab"},
		{"number", `5`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := newRichCtx("").plain(inlineSpanOf(t, tc.span), 0); got != tc.want {
				t.Errorf("plain = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRichInlineDepthLimit(t *testing.T) {
	nested := func(n int) string {
		return strings.Repeat(`{"type":"bold","text":`, n) + `"x"` + strings.Repeat(`}`, n)
	}
	c := newRichCtx("")
	if got, want := c.inline(inlineSpanOf(t, nested(3)), 0), "******x******"; got != want {
		t.Errorf("inline of 3 levels = %q, want %q", got, want)
	}
	// The text of 200 levels is below the depth limit, so each span is empty.
	if got := c.inline(inlineSpanOf(t, nested(200)), 0); got != "" {
		t.Errorf("inline of 200 levels = %q, want an empty text", got)
	}
	if got := c.plain(inlineSpanOf(t, nested(200)), 0); got != "" {
		t.Errorf("plain of 200 levels = %q, want an empty text", got)
	}
	if got, want := c.plain(inlineSpanOf(t, nested(3)), 0), "x"; got != want {
		t.Errorf("plain of 3 levels = %q, want %q", got, want)
	}
	if got := c.inline("x", maxRichDepth+1); got != "" {
		t.Errorf("inline below the limit = %q, want an empty text", got)
	}
	if got, want := c.inline("x", maxRichDepth), "x"; got != want {
		t.Errorf("inline at the limit = %q, want %q", got, want)
	}
}

func TestRichInlineTicks(t *testing.T) {
	cases := []struct {
		s    string
		min  int
		want string
	}{
		{"", 1, "`"},
		{"abc", 1, "`"},
		{"a`b", 1, "``"},
		{"a``b`c", 1, "```"},
		{"`", 1, "``"},
		{"", 3, "```"},
		{"a`b", 3, "```"},
		{"a```b", 3, "````"},
		{"````", 3, "`````"},
	}
	for _, tc := range cases {
		if got := richTicks(tc.s, tc.min); got != tc.want {
			t.Errorf("richTicks(%q, %d) = %q, want %q", tc.s, tc.min, got, tc.want)
		}
	}
}

// TestRichInlineMarkersPerLine checks that a span with a line feed gets its
// markers on each line, because the output lines are split later.
func TestRichInlineMarkersPerLine(t *testing.T) {
	d := richDocOf(t, `[{"type":"paragraph","text":["a ",{"type":"bold","text":"one\ntwo\n\nthree "},"end"]}]`)
	want := "a **one**\n**two**\n\n**three** end"
	if got := d.Text(""); got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}
}
