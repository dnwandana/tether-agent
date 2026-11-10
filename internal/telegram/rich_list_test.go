package telegram

import (
	"strings"
	"testing"
)

func TestRichList(t *testing.T) {
	cases := []struct {
		name   string
		blocks string
		want   []string
	}{
		{"the three markers", `[{"type":"list","items":[
			{"has_checkbox":true,"is_checked":true,"blocks":[{"type":"paragraph","text":"done"}]},
			{"has_checkbox":true,"is_checked":false,"blocks":[{"type":"paragraph","text":"open"}]},
			{"label":"1.","value":1,"type":"1","blocks":[{"type":"paragraph","text":"one"}]},
			{"label":"a)","value":1,"type":"a","blocks":[{"type":"paragraph","text":"alpha"}]},
			{"label":"•","blocks":[{"type":"paragraph","text":"dot"}]},
			{"label":"","blocks":[{"type":"paragraph","text":"empty label"}]},
			{"blocks":[{"type":"paragraph","text":"no label"}]}]}]`,
			[]string{"- [x] done", "- [ ] open", "1. one", "a) alpha", "- dot", "- empty label", "- no label"}},
		{"a checkbox wins over a label", `[{"type":"list","items":[{"label":"1.","has_checkbox":true,"blocks":[{"type":"paragraph","text":"x"}]}]}]`,
			[]string{"- [ ] x"}},
		{"label with a line feed", `[{"type":"list","items":[{"label":"1.\n2.","blocks":[{"type":"paragraph","text":"x"}]}]}]`,
			[]string{"1.2. x"}},
		{"label with spaces", `[{"type":"list","items":[{"label":" 7. ","blocks":[{"type":"paragraph","text":"x"}]}]}]`,
			[]string{"7. x"}},
		{"label of 40 characters", `[{"type":"list","items":[{"label":"0123456789012345678901234567890123456789","blocks":[{"type":"paragraph","text":"x"}]}]}]`,
			[]string{"0123456789012345 x"}},
		{"label of 40 characters that are not ASCII", `[{"type":"list","items":[{"label":"éééééééééééééééééééééééééééééééééééééééé","blocks":[{"type":"paragraph","text":"x\ny"}]}]}]`,
			[]string{"éééééééééééééééé x", "                 y"}},
		{"styles in an item", `[{"type":"list","items":[{"label":"1.","blocks":[{"type":"paragraph","text":["a ",{"type":"bold","text":"b"}]}]}]}]`,
			[]string{"1. a **b**"}},
		{"item with a line feed", `[{"type":"list","items":[
			{"label":"1.","blocks":[{"type":"paragraph","text":"a\nb"}]},
			{"has_checkbox":true,"blocks":[{"type":"paragraph","text":"c\nd"}]},
			{"blocks":[{"type":"paragraph","text":"e\nf"}]}]}]`,
			[]string{"1. a", "   b", "- [ ] c", "      d", "- e", "  f"}},
		{"item with several blocks", `[{"type":"list","items":[
			{"label":"1.","blocks":[{"type":"paragraph","text":"a"},{"type":"pre","language":"go","text":"x\n\ny"},{"type":"paragraph","text":"b"}]},
			{"label":"2.","blocks":[{"type":"paragraph","text":"c"}]}]}]`,
			[]string{"1. a", "", "   ```go", "   x", "", "   y", "   ```", "", "   b", "2. c"}},
		{"nested list", `[{"type":"list","items":[
			{"label":"1.","blocks":[{"type":"paragraph","text":"outer"},{"type":"list","items":[
				{"label":"a)","blocks":[{"type":"paragraph","text":"inner"}]},
				{"label":"b)","blocks":[{"type":"list","items":[{"blocks":[{"type":"paragraph","text":"deep"}]}]}]}]}]},
			{"label":"2.","blocks":[{"type":"paragraph","text":"next"}]}]}]`,
			[]string{"1. outer", "", "   a) inner", "   b) - deep", "2. next"}},
		{"list in a quotation", `[{"type":"blockquote","blocks":[{"type":"list","items":[
			{"label":"1.","blocks":[{"type":"paragraph","text":"a\nb"}]},
			{"label":"2.","blocks":[{"type":"paragraph","text":"c"}]}]}],"credit":"who"}]`,
			[]string{"> 1. a", ">    b", "> 2. c", "> — who"}},
		{"quotation in a list item", `[{"type":"list","items":[{"blocks":[
			{"type":"blockquote","blocks":[{"type":"paragraph","text":"q1"},{"type":"paragraph","text":"q2"}],"credit":"who"}]}]}]`,
			[]string{"- > q1", "  >", "  > q2", "  > — who"}},
		{"item with no blocks", `[{"type":"list","items":[
			{"label":"1.","type":"1","text":["a ",{"type":"italic","text":"b"}]},
			{"type":"a","text":"c"},
			{"label":"3.","blocks":[],"text":"d"}]}]`,
			[]string{"1. a *b*", "- c", "3. d"}},
		{"item with no content", `[{"type":"list","items":[
			{"label":"1."},
			{"has_checkbox":true,"blocks":[{"type":"paragraph","text":""}]},
			{"label":"3.","blocks":[{"type":"paragraph","text":"x"}]}]}]`,
			[]string{"3. x"}},
		{"list between paragraphs", `[{"type":"paragraph","text":"before"},{"type":"list","items":[
			{"blocks":[{"type":"paragraph","text":"a"}]},{"blocks":[{"type":"paragraph","text":"b"}]}]},{"type":"paragraph","text":"after"}]`,
			[]string{"before", "", "- a", "- b", "", "after"}},
		{"items of the wrong type", `[{"type":"list","items":5}]`, nil},
		{"no items", `[{"type":"list"}]`, nil},
		{"empty list", `[{"type":"list","items":[]}]`, nil},
		{"item of the wrong type", `[{"type":"list","items":[5,"x",null,[],{"label":"1.","blocks":5,"text":"ok"}]}]`, []string{"1. ok"}},
		{"unknown block with items", `[{"type":"sidebar","text":"t","items":[
			{"label":"1.","blocks":[{"type":"paragraph","text":"x"}]},
			{"has_checkbox":true,"is_checked":true,"blocks":[{"type":"paragraph","text":"y"}]}],"blocks":[{"type":"paragraph","text":"p"}]}]`,
			[]string{"t", "", "1. x", "- [x] y", "", "p"}},
		{"the list of the docs payload", `[{"type": "list", "items": [
			{"label": "1.", "value": 1, "type": "1", "blocks": [{"type": "paragraph", "text": "first"}]},
			{"label": "", "has_checkbox": true, "is_checked": true, "blocks": [{"type": "paragraph", "text": "done"}]}]}]`,
			[]string{"1. first", "- [x] done"}},
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

func TestRichListUnknown(t *testing.T) {
	cases := []struct{ name, blocks, want string }{
		{"list is a known type", `[{"type":"list","items":[{"label":"1.","type":"1","blocks":[{"type":"paragraph","text":"x"}]}]}]`, ""},
		{"the label style of an item with no blocks is not a block type", `[{"type":"list","items":[{"label":"1.","type":"1","text":"x"},{"type":"a","text":"y"}]}]`, ""},
		{"unknown types in an item", `[{"type":"list","items":[{"blocks":[{"type":"aside","text":{"type":"glow","text":"x"}}]}]}]`, "aside,glow"},
		{"unknown block with items", `[{"type":"sidebar","items":[{"blocks":[{"type":"paragraph","text":"x"}]}]}]`, "sidebar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(richDocOf(t, tc.blocks).Unknown(), ","); got != tc.want {
				t.Errorf("Unknown = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRichListDepthLimit(t *testing.T) {
	blocks := strings.Repeat(`[{"type":"list","items":[{"label":"1.","blocks":`, 200) +
		`[{"type":"paragraph","text":"x"}]` + strings.Repeat(`}]}]`, 200)
	d := richDocOf(t, blocks)
	// The paragraph is below the depth limit, so each item is empty.
	if got := d.Text(""); got != "" {
		t.Errorf("Text = %q, want an empty text", got)
	}
	if d.Failed() {
		t.Error("Failed() = true")
	}
}

func TestRichListItemWithMediaFirst(t *testing.T) {
	// A media part has one prefix for all its lines. So the marker stands on
	// a line of its own, and the media part gets the indent.
	m := &richMedia{Kind: "photo", Placeholder: "[photo]", Caption: "cap"}
	lines := richItem([]richLine{{Media: m}, {}, {Text: "after"}}, "1.")
	got := richJoin(lines, func(m *richMedia) string { return m.Placeholder })
	if want := "1.\n   [photo]\n   cap\n\n   after"; got != want {
		t.Errorf("media first = %q, want %q", got, want)
	}

	m = &richMedia{Kind: "photo", Placeholder: "[photo]", Caption: "cap"}
	lines = richItem([]richLine{{Text: "before"}, {}, {Media: m}}, "- [x]")
	got = richJoin(lines, func(m *richMedia) string { return m.Placeholder })
	if want := "- [x] before\n\n      [photo]\n      cap"; got != want {
		t.Errorf("text first = %q, want %q", got, want)
	}

	if got := richItem(nil, "1."); len(got) != 0 {
		t.Errorf("item with no lines = %+v, want none", got)
	}
}
