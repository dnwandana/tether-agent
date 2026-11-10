package telegram

import (
	"reflect"
	"strings"
	"testing"
)

func TestRichBlocksTypes(t *testing.T) {
	cases := []struct {
		name   string
		blocks string
		want   []string
	}{
		{"paragraph", `[{"type":"paragraph","text":["a ",{"type":"bold","text":"b"}]}]`, []string{"a **b**"}},
		{"paragraph with a line feed", `[{"type":"paragraph","text":"a\nb"}]`, []string{"a", "b"}},
		{"heading", `[{"type":"heading","size":2,"text":"Title"}]`, []string{"## Title"}},
		{"heading with size 0", `[{"type":"heading","size":0,"text":"T"}]`, []string{"# T"}},
		{"heading with size 9", `[{"type":"heading","size":9,"text":"T"}]`, []string{"###### T"}},
		{"heading with no size", `[{"type":"heading","text":"T"}]`, []string{"# T"}},
		{"heading with a line feed", `[{"type":"heading","size":3,"text":"a\nb"}]`, []string{"### a b"}},
		{"heading with no text", `[{"type":"heading","size":1,"text":""}]`, nil},
		{"pre", `[{"type":"pre","language":"go","text":"fmt.Println(1)"}]`, []string{"```go", "fmt.Println(1)", "```"}},
		{"pre with no language", `[{"type":"pre","text":"if x {\n\ty()\n}\n"}]`, []string{"```", "if x {", "\ty()", "}", "```"}},
		{"pre with spans", `[{"type":"pre","text":[{"type":"bold","text":"a"}," = ",{"type":"url","text":"b","url":"https://t.me/"}]}]`, []string{"```", "a = b", "```"}},
		{"pre with backticks", `[{"type":"pre","text":"a\n` + "```" + `\nb"}]`, []string{"````", "a", "```", "b", "````"}},
		{"pre with a bad language", `[{"type":"pre","language":"go\n` + "```" + `","text":"x"}]`, []string{"```go", "x", "```"}},
		{"pre with a long language", `[{"type":"pre","language":"c++.#-_0123456789012345678901234567890","text":"x"}]`, []string{"```c++.#-_0123456789012345678901234", "x", "```"}},
		{"pre with no text", `[{"type":"pre","language":"go","text":""}]`, nil},
		{"footer", `[{"type":"footer","text":["f ",{"type":"italic","text":"g"}]}]`, []string{"f *g*"}},
		{"divider", `[{"type":"divider"}]`, []string{"---"}},
		{"mathematical_expression", `[{"type":"mathematical_expression","expression":"E=mc^2"}]`, []string{"$$E=mc^2$$"}},
		{"anchor", `[{"type":"anchor","name":"top"}]`, nil},
		{"blockquote", `[{"type":"blockquote","blocks":[{"type":"paragraph","text":"a"},{"type":"paragraph","text":"b"}],"credit":"credit"}]`,
			[]string{"> a", ">", "> b", "> — credit"}},
		{"blockquote with no credit", `[{"type":"blockquote","blocks":[{"type":"paragraph","text":"a\nb"}]}]`, []string{"> a", "> b"}},
		{"blockquote with a credit only", `[{"type":"blockquote","credit":["The ",{"type":"bold","text":"Author"}]}]`, []string{"> — The **Author**"}},
		{"expandable_blockquote", `[{"type":"expandable_blockquote","text":"hidden\ntext","credit":"who"}]`, []string{"> hidden", "> text", "> — who"}},
		{"pullquote", `[{"type":"pullquote","text":["pulled ",{"type":"italic","text":"text"}]}]`, []string{"> pulled *text*"}},
		{"collage", `[{"type":"collage","blocks":[{"type":"paragraph","text":"a"},{"type":"paragraph","text":"b"}],"caption":{"text":"cap","credit":"cr"}}]`,
			[]string{"a", "", "b", "", "cap — cr"}},
		{"slideshow", `[{"type":"slideshow","blocks":[{"type":"paragraph","text":"a"}]}]`, []string{"a"}},
		{"slideshow with a caption only", `[{"type":"slideshow","blocks":[],"caption":{"text":"cap"}}]`, []string{"cap"}},
		{"details", `[{"type":"details","is_open":true,"summary":["More ",{"type":"code","text":"info"}],"blocks":[{"type":"paragraph","text":"inside"},{"type":"divider"}]}]`,
			[]string{"**More `info`**", "", "inside", "", "---"}},
		{"details with no summary", `[{"type":"details","blocks":[{"type":"paragraph","text":"inside"}]}]`, []string{"inside"}},
		{"map", `[{"type":"map","location":{"latitude":48.8584,"longitude":2.2945},"zoom":12,"caption":{"text":"Tower","credit":"Maps"}}]`,
			[]string{"[map: latitude 48.8584, longitude 2.2945]", "Tower — Maps"}},
		{"map with no caption", `[{"type":"map","location":{"latitude":-6.2,"longitude":106}}]`, []string{"[map: latitude -6.2, longitude 106]"}},
		{"map with no location", `[{"type":"map","caption":{"text":"Tower"}}]`, []string{"Tower"}},
		{"buttons", `[{"type":"buttons","align":"center","buttons":[{"text":"Open","url":"https://t.me/"},{"text":["Vote ",{"type":"bold","text":"now"}],"callback_data":"v"},{}]}]`,
			[]string{"[button: Open](https://t.me/)", "[button: Vote **now**]"}},
		{"thinking", `[{"type":"thinking","text":"t"}]`, []string{"t"}},
		{"unknown type", `[{"type":"sidebar","text":["t ",{"type":"bold","text":"u"}],"summary":"s","expression":"e","blocks":[{"type":"paragraph","text":"p"},{"type":"divider"}]}]`,
			[]string{"t **u**", "", "s", "", "e", "", "p", "", "---"}},
		{"no type", `[{"text":"x"}]`, []string{"x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := richDocOf(t, tc.blocks)
			if got, want := d.Text(""), strings.Join(tc.want, "\n"); got != want {
				t.Errorf("Text =\n%s\nwant\n%s", got, want)
			}
			if d.Failed() {
				t.Error("Failed() = true")
			}
		})
	}
}

func TestRichBlocksGap(t *testing.T) {
	d := richDocOf(t, `[
		{"type":"paragraph","text":"a"},
		{"type":"anchor","name":"n"},
		{"type":"paragraph","text":""},
		{"type":"paragraph","text":" "},
		{"type":"paragraph","text":"b"}]`)
	want := []richLine{{Text: "a"}, {}, {Text: "b"}}
	if got := d.Lines(""); !reflect.DeepEqual(got, want) {
		t.Errorf("Lines = %+v, want %+v", got, want)
	}
	if got, want := d.Text(""), "a\n\nb"; got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}
	if got := richDocOf(t, `[]`).Lines(""); len(got) != 0 {
		t.Errorf("Lines of no blocks = %+v, want none", got)
	}
}

func TestRichBlocksNested(t *testing.T) {
	cases := []struct {
		name   string
		blocks string
		want   []string
	}{
		{"three quotations", `[{"type":"blockquote","blocks":[{"type":"blockquote","blocks":[{"type":"blockquote","blocks":[{"type":"paragraph","text":"x"}]}],"credit":"c"}]}]`,
			[]string{"> > > x", "> > — c"}},
		{"pre in a quotation", `[{"type":"blockquote","blocks":[{"type":"pre","text":"a\n\nb"}]}]`,
			[]string{"> ```", "> a", ">", "> b", "> ```"}},
		{"caption with two lines in a quotation", `[{"type":"blockquote","blocks":[{"type":"collage","blocks":[{"type":"paragraph","text":"a"}],"caption":{"text":"c1\nc2","credit":"cr"}}]}]`,
			[]string{"> a", ">", "> c1", "> c2 — cr"}},
		{"map in a quotation", `[{"type":"blockquote","blocks":[{"type":"map","location":{"latitude":1,"longitude":2},"caption":{"text":"c1\nc2"}}]}]`,
			[]string{"> [map: latitude 1, longitude 2]", "> c1", "> c2"}},
		{"quotation in details", `[{"type":"details","summary":"S","blocks":[{"type":"pullquote","text":"q"}]}]`,
			[]string{"**S**", "", "> q"}},
		{"heading in a quotation", `[{"type":"blockquote","blocks":[{"type":"heading","size":2,"text":"a\nb"}]}]`,
			[]string{"> ## a b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, want := richDocOf(t, tc.blocks).Text(""), strings.Join(tc.want, "\n"); got != want {
				t.Errorf("Text =\n%s\nwant\n%s", got, want)
			}
		})
	}
}

func TestRichBlocksDepthLimit(t *testing.T) {
	blocks := strings.Repeat(`[{"type":"blockquote","blocks":`, 200) +
		`[{"type":"paragraph","text":"x"}]` + strings.Repeat(`}]`, 200)
	d := richDocOf(t, blocks)
	// The paragraph is below the depth limit, so each quotation is empty.
	if got := d.Text(""); got != "" {
		t.Errorf("Text = %q, want an empty text", got)
	}
	if got := d.Lines(""); len(got) != 0 {
		t.Errorf("Lines = %d lines, want none", len(got))
	}
	if got := d.Unknown(); got != nil {
		t.Errorf("Unknown = %v, want nil", got)
	}
	if d.Failed() {
		t.Error("Failed() = true")
	}
}

func TestRichBlocksWrongTypes(t *testing.T) {
	d := richDocOf(t, `[5, "x", null, [],
		{"type":"paragraph","text":"ok"},
		{"type":"paragraph","text":5},
		{"type":"blockquote","blocks":5,"credit":5},
		{"type":"collage","blocks":"x","caption":5},
		{"type":"details","summary":5,"blocks":5},
		{"type":"buttons","buttons":5},
		{"type":"buttons","buttons":[5,"x"]},
		{"type":"map","location":5},
		{"type":"mathematical_expression","expression":5},
		{"type":"pre","text":5,"language":5},
		{"type":5,"text":"typed"},
		{"type":"heading","size":"big","text":"h"}]`)
	if got, want := d.Text(""), "ok\n\ntyped\n\n# h"; got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}
	if d.Failed() {
		t.Error("Failed() = true")
	}
}

func TestRichBlocksCaption(t *testing.T) {
	cases := []struct{ name, caption, want string }{
		{"text", `{"text":"t"}`, "t"},
		{"text and credit", `{"text":["t ",{"type":"bold","text":"u"}],"credit":"c"}`, "t **u** — c"},
		{"credit only", `{"credit":"c"}`, "— c"},
		{"empty", `{}`, ""},
		{"not an object", `5`, ""},
		{"text with two lines", `{"text":"a\nb","credit":"c\nd"}`, "a\nb — c d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := richDocOf(t, `[{"type":"collage","caption":`+tc.caption+`}]`)
			v := richMap(d.blocks[0])["caption"]
			if got := newRichCtx("").caption(v, 0); got != tc.want {
				t.Errorf("caption = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRichBlocksMediaRender(t *testing.T) {
	cases := []struct {
		name  string
		media richMedia
		note  string
		want  string
	}{
		{"note only", richMedia{Kind: "photo"}, "[photo]", "[photo]"},
		{"note and caption", richMedia{Kind: "photo", Caption: "cap"}, "[photo]", "[photo]\ncap"},
		{"prefix and a caption with two lines", richMedia{Kind: "photo", Caption: "c1\nc2", Prefix: "> "}, "[photo]", "> [photo]\n> c1\n> c2"},
		{"note with two lines", richMedia{Kind: "document", Caption: "cap", Prefix: "   "}, "n1\nn2", "   n1\n   n2\n   cap"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.media.Render(tc.note); got != tc.want {
				t.Errorf("Render = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRichBlocksJoin(t *testing.T) {
	m := &richMedia{Kind: "photo", Placeholder: "[photo]", Caption: "c1\nc2", Prefix: "> "}
	lines := []richLine{{Text: "a"}, {}, {Media: m}, {}, {Text: "b"}}
	got := richJoin(lines, func(m *richMedia) string { return "[note for " + m.Kind + "]" })
	if want := "a\n\n> [note for photo]\n> c1\n> c2\n\nb"; got != want {
		t.Errorf("richJoin = %q, want %q", got, want)
	}
	got = richJoin(lines, func(m *richMedia) string { return m.Placeholder })
	if want := "a\n\n> [photo]\n> c1\n> c2\n\nb"; got != want {
		t.Errorf("richJoin with the placeholder = %q, want %q", got, want)
	}
	if got := richJoin(nil, func(m *richMedia) string { return m.Placeholder }); got != "" {
		t.Errorf("richJoin of no lines = %q, want an empty text", got)
	}
}

func TestRichBlocksPrefix(t *testing.T) {
	m := &richMedia{Kind: "photo", Prefix: "> "}
	lines := []richLine{{Text: "a"}, {}, {Text: "b"}, {Media: m}}
	got := richPrefix(lines, "1. ", "   ")
	if len(got) != 4 {
		t.Fatalf("richPrefix gave %d lines, want 4", len(got))
	}
	if got[0].Text != "1. a" || got[1].Text != "" || got[2].Text != "   b" {
		t.Errorf("text lines = %q, %q, %q", got[0].Text, got[1].Text, got[2].Text)
	}
	if got[3].Media == nil || got[3].Media.Prefix != "   > " {
		t.Errorf("media line = %+v, want the prefix %q", got[3].Media, "   > ")
	}
	quoted := richPrefix([]richLine{{Text: "a"}, {}, {Text: "b"}}, "> ", "> ")
	want := []richLine{{Text: "> a"}, {Text: ">"}, {Text: "> b"}}
	if !reflect.DeepEqual(quoted, want) {
		t.Errorf("quoted = %+v, want %+v", quoted, want)
	}
	if got := richPrefix(nil, "> ", "> "); len(got) != 0 {
		t.Errorf("richPrefix of no lines = %+v, want none", got)
	}
}

func TestRichBlocksUnknown(t *testing.T) {
	cases := []struct {
		name   string
		blocks string
		want   []string
	}{
		{"block type and span type", `[{"type":"sidebar","text":"x"},{"type":"paragraph","text":{"type":"glow","text":"y"}}]`, []string{"glow", "sidebar"}},
		{"one name for each type", `[{"type":"sidebar","text":"x"},{"type":"sidebar","text":"y"}]`, []string{"sidebar"}},
		{"thinking", `[{"type":"thinking","text":"x"}]`, []string{"thinking"}},
		{"span type in pre", `[{"type":"pre","text":{"type":"dim","text":"z"}}]`, []string{"dim"}},
		{"span type in a credit", `[{"type":"blockquote","credit":{"type":"wave","text":"c"}}]`, []string{"wave"}},
		{"block type in a quotation", `[{"type":"blockquote","blocks":[{"type":"aside","text":"x"}]}]`, []string{"aside"}},
		{"known types only", `[{"type":"paragraph","text":{"type":"bold","text":"x"}},{"type":"divider"}]`, nil},
		{"no type", `[{"text":"x"}]`, nil},
		{"no blocks", `[]`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := richDocOf(t, tc.blocks).Unknown(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Unknown = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRichBlocksFailedDoc(t *testing.T) {
	d := richDocOf(t, `[{"type":"sidebar","text":"x"}]`)
	d.failed = true
	if got := d.Lines(""); got != nil {
		t.Errorf("Lines = %+v, want nil", got)
	}
	if got := d.Text(""); got != "" {
		t.Errorf("Text = %q, want an empty text", got)
	}
	if got := d.Unknown(); got != nil {
		t.Errorf("Unknown = %v, want nil", got)
	}
}

func TestRichBlocksDocsPayload(t *testing.T) {
	d := richDocOf(t, `[
		{"type": "heading", "size": 1, "text": "Report"},
		{"type": "paragraph", "text": ["Plain, ", {"type": "bold", "text": ["bold ", {"type": "italic", "text": "bold italic"}]}, " and ", {"type": "url", "text": "a link", "url": "https://t.me/"}]},
		{"type": "pre", "language": "go", "text": "fmt.Println(1)"},
		{"type": "blockquote", "blocks": [{"type": "paragraph", "text": "quoted"}], "credit": "The Author"},
		{"type": "divider"}]`)
	want := strings.Join([]string{
		"# Report",
		"",
		"Plain, **bold *bold italic*** and [a link](https://t.me/)",
		"",
		"```go",
		"fmt.Println(1)",
		"```",
		"",
		"> quoted",
		"> — The Author",
		"",
		"---",
	}, "\n")
	if got := d.Text(""); got != want {
		t.Errorf("Text =\n%s\nwant\n%s", got, want)
	}
	if got := d.Unknown(); got != nil {
		t.Errorf("Unknown = %v, want nil", got)
	}
}
