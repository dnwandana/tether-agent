package telegram

import (
	"reflect"
	"strings"
	"testing"
)

// plainDocsPayload is the test payload that is built from the field tables of
// the Bot API.
const plainDocsPayload = `[
  {"type": "heading", "size": 1, "text": "Report"},
  {"type": "paragraph", "text": ["Plain, ", {"type": "bold", "text": ["bold ", {"type": "italic", "text": "bold italic"}]}, " and ", {"type": "url", "text": "a link", "url": "https://t.me/"}]},
  {"type": "list", "items": [
    {"label": "1.", "value": 1, "type": "1", "blocks": [{"type": "paragraph", "text": "first"}]},
    {"label": "", "has_checkbox": true, "is_checked": true, "blocks": [{"type": "paragraph", "text": "done"}]}]},
  {"type": "pre", "language": "go", "text": "fmt.Println(1)"},
  {"type": "blockquote", "blocks": [{"type": "paragraph", "text": "quoted"}], "credit": "The Author"},
  {"type": "table", "is_bordered": true, "caption": "Caption", "cells": [
    [{"text": "H1", "is_header": true, "align": "left", "valign": "top"}, {"text": "H2", "is_header": true, "align": "center", "valign": "top"}],
    [{"text": {"type": "code", "text": "v"}, "colspan": 2, "align": "left", "valign": "middle"}]]},
  {"type": "photo", "photo": [{"file_id": "AgAC", "file_unique_id": "AQAD", "width": 320, "height": 240}], "caption": {"text": "Photo caption", "credit": "Credit"}},
  {"type": "divider"}
]`

// plainHermesPayload has the block and span types whose content is not in
// "text", "items", or "blocks" (Hermes test_telegram_rich_messages.py).
const plainHermesPayload = `[
  {"type": "table", "cells": [[{"text": "Model"}, {"text": "Price"}], [{"text": "HD103UJ"}, {"text": "20 EUR"}]]},
  {"type": "details", "summary": "Specs", "blocks": [{"type": "paragraph", "text": "1TB"}]},
  {"type": "mathematical_expression", "expression": "E=mc^2"},
  {"type": "paragraph", "text": [
    "ad ", {"type": "url", "text": "here", "url": "https://example.com/?u=@tether_bot"}, " ",
    {"type": "url", "text": "https://example.com/a", "url": "https://example.com/a"}, " ",
    {"type": "mathematical_expression", "expression": "x^2"}, " ",
    {"type": "custom_emoji", "custom_emoji_id": "5", "alternative_text": "👍"}]}
]`

func TestRichFlattenLines(t *testing.T) {
	cases := []struct {
		name, blocks, plain, masked string
	}{
		{"docs payload", plainDocsPayload,
			"Report\nPlain, bold bold italic and a link\n1. first\n- done\nfmt.Println(1)\nquoted\nThe Author\nH1 | H2\nv\nCaption\nPhoto caption\nCredit",
			"Report\nPlain, bold bold italic and \uFFFC\n1. first\n- done\n\uFFFC\nquoted\nThe Author\nH1 | H2\n\uFFFC\nCaption\nPhoto caption\nCredit"},
		{"hermes payload", plainHermesPayload,
			"Model | Price\nHD103UJ | 20 EUR\nSpecs\n1TB\nE=mc^2\nad here https://example.com/a x^2 👍",
			"Model | Price\nHD103UJ | 20 EUR\nSpecs\n1TB\nE=mc^2\nad \uFFFC \uFFFC x^2 👍"},
		{"children span and labelled item",
			`[{"type":"paragraph","text":["See ",{"type":"bold","children":["the plan"]}]},
			  {"type":"list","items":[{"label":"1.","blocks":[{"type":"paragraph","text":"ship it"}]}]}]`,
			"See the plan\n1. ship it", "See the plan\n1. ship it"},
		{"wrong types", `[{"type":"list","items":5},{"type":"table","cells":5},{"type":"paragraph","text":"ok"},5,"x",null]`,
			"ok", "ok"},
		{"item with no blocks", `[{"type":"list","items":[{"label":"a)","text":"alpha"},{"text":"beta"},5]}]`,
			"a) alpha\n- beta", "a) alpha\n- beta"},
		{"label goes to the first line only",
			`[{"type":"list","items":[{"label":"1.","blocks":[{"type":"blockquote","blocks":[{"type":"paragraph","text":"one"}]},{"type":"paragraph","text":"two"}]}]}]`,
			"1. one\ntwo", "1. one\ntwo"},
		{"quotation credit", `[{"type":"pullquote","text":"Stay hungry","credit":["Steve ",{"type":"italic","text":"Jobs"}]}]`,
			"Stay hungry\nSteve Jobs", "Stay hungry\nSteve Jobs"},
		{"collage caption", `[{"type":"collage","blocks":[{"type":"photo","photo":[{"file_id":"AgAC"}]},{"type":"photo","photo":[{"file_id":"AgAD"}],"caption":{"text":"second"}}],"caption":{"text":"both","credit":"me"}}]`,
			"second\nboth\nme", "second\nboth\nme"},
		{"buttons give no line", `[{"type":"paragraph","text":["tap ",{"type":"button","button":{"text":"Go","url":"https://example.com/"}}]},{"type":"buttons","buttons":[{"text":"Open","url":"https://example.com/"}]}]`,
			"tap ", "tap "},
		{"unknown span keys", `[{"type":"paragraph","text":[{"type":"new_a","text":"t","children":["c"]},{"type":"new_b","children":["c"]},{"type":"new_c","expression":"e","alternative_text":"a"},{"type":"new_d","alternative_text":"a"},{"type":"new_e","url":"https://example.com/"},5,null]}]`,
			"tcea", "tcea"},
		{"masked blocks and spans", `[{"type":"pre","text":"@tether_bot"},{"type":"paragraph","text":[{"type":"code","text":"a"},{"type":"link","text":"b"},{"type":"text_link","text":"c"},{"type":"url","text":["d",{"type":"bold","text":"e"}],"url":"https://example.com/"}]}]`,
			"@tether_bot\nabcde", "\uFFFC\n\uFFFC\uFFFC\uFFFC\uFFFC"},
		{"map", `[{"type":"map","location":{"latitude":48.8584,"longitude":2.2945},"zoom":12,"caption":{"text":"Tower"}}]`,
			"[map: latitude 48.8584, longitude 2.2945]\nTower", "[map: latitude 48.8584, longitude 2.2945]\nTower"},
		{"map with no location", `[{"type":"map","location":5}]`, "", ""},
		{"not trimmed", `[{"type":"paragraph","text":"  a  "},{"type":"heading","size":2,"text":""}]`, "  a  \n", "  a  \n"},
	}
	for _, c := range cases {
		d := richDocOf(t, c.blocks)
		if got := richFlatten(d.blocks, false, nil); got != c.plain {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.plain)
		}
		if got := richFlatten(d.blocks, true, nil); got != c.masked {
			t.Errorf("%s, masked:\n got %q\nwant %q", c.name, got, c.masked)
		}
	}
}

// plainSpanCall is one call of onSpan.
type plainSpanCall struct {
	Type       string
	Start, End int
}

func plainSpanCalls(t *testing.T, blocksJSON string, mask bool) (string, []plainSpanCall) {
	t.Helper()
	var calls []plainSpanCall
	text := richFlatten(richDocOf(t, blocksJSON).blocks, mask, func(span map[string]any, start, end int) {
		calls = append(calls, plainSpanCall{richType(span), start, end})
	})
	return text, calls
}

func TestRichFlattenOnSpan(t *testing.T) {
	// "é" has 2 bytes, so the offsets are byte offsets.
	const para = `[{"type":"paragraph","text":["é ",{"type":"bold","text":["a ",{"type":"mention","text":"@bot"}]}," ",{"type":"code","text":"x"},{"text":"!"}]}]`
	text, calls := plainSpanCalls(t, para, false)
	want := []plainSpanCall{{"mention", 5, 9}, {"bold", 3, 9}, {"code", 10, 11}}
	if text != "é a @bot x!" || !reflect.DeepEqual(calls, want) {
		t.Errorf("plain: %q %+v, want %+v", text, calls, want)
	}
	text, calls = plainSpanCalls(t, para, true)
	want = []plainSpanCall{{"mention", 5, 9}, {"bold", 3, 9}}
	if text != "é a @bot \uFFFC!" || !reflect.DeepEqual(calls, want) {
		t.Errorf("masked: %q %+v, want %+v", text, calls, want)
	}

	// A span in a masked span gives no call.
	const link = `[{"type":"paragraph","text":{"type":"url","text":{"type":"mention","text":"@bot"},"url":"https://example.com/"}}]`
	if text, calls = plainSpanCalls(t, link, true); text != "\uFFFC" || len(calls) != 0 {
		t.Errorf("masked link: %q %+v", text, calls)
	}
	text, calls = plainSpanCalls(t, link, false)
	if want = []plainSpanCall{{"mention", 0, 4}, {"url", 0, 4}}; text != "@bot" || !reflect.DeepEqual(calls, want) {
		t.Errorf("plain link: %q %+v, want %+v", text, calls, want)
	}

	// The offsets count the earlier lines, the line feeds, and the list label.
	const lines = `[{"type":"paragraph","text":"ab"},
		{"type":"list","items":[{"label":"1.","blocks":[{"type":"paragraph","text":{"type":"bold","text":"x"}}]}]},
		{"type":"table","cells":[[{"text":"k"},{"text":{"type":"italic","text":"v"}}]],"caption":{"type":"bold","text":"cap"}},
		{"type":"photo","photo":[{"file_id":"AgAC"}],"caption":{"text":{"type":"mention","text":"@bot"},"credit":{"type":"italic","text":"me"}}},
		{"type":"paragraph","text":{"type":"custom_emoji","custom_emoji_id":"5","alternative_text":"👍"}}]`
	text, calls = plainSpanCalls(t, lines, true)
	want = []plainSpanCall{{"bold", 6, 7}, {"italic", 12, 13}, {"bold", 14, 17}, {"mention", 18, 22}, {"italic", 23, 25}, {"custom_emoji", 26, 30}}
	if text != "ab\n1. x\nk | v\ncap\n@bot\nme\n👍" || !reflect.DeepEqual(calls, want) {
		t.Errorf("lines: %q %+v, want %+v", text, calls, want)
	}
}

// plainNestedBlocks returns a paragraph with the text "leaf" in n "quote"
// blocks.
func plainNestedBlocks(n int) string {
	return strings.Repeat(`{"type":"quote","blocks":[`, n) + `{"type":"paragraph","text":"leaf"}` + strings.Repeat(`]}`, n)
}

// plainNestedSpans returns the string "deep" in n "bold" spans.
func plainNestedSpans(n int, key string) string {
	open, end := `{"type":"bold","`+key+`":`, `}`
	if key == "children" {
		open, end = open+`[`, `]}`
	}
	return strings.Repeat(open, n) + `"deep"` + strings.Repeat(end, n)
}

func TestRichFlattenDepthLimit(t *testing.T) {
	cases := []struct{ name, blocks, want string }{
		{"64 block levels", `[` + plainNestedBlocks(64) + `]`, "leaf"},
		{"65 block levels", `[` + plainNestedBlocks(65) + `]`, ""},
		{"64 span levels", `[{"type":"paragraph","text":` + plainNestedSpans(64, "text") + `}]`, "deep"},
		{"65 span levels", `[{"type":"paragraph","text":` + plainNestedSpans(65, "text") + `}]`, ""},
		// The Hermes payload: the content below the limit goes away, and the
		// other content stays.
		{"200 levels", `[{"type":"paragraph","text":["top ",{"type":"bold","text":"line"}]},` + plainNestedBlocks(200) +
			`,{"type":"paragraph","text":` + plainNestedSpans(200, "children") + `}]`, "top line"},
	}
	for _, c := range cases {
		d := richDocOf(t, c.blocks)
		if got := d.Plain(); got != c.want || d.Failed() {
			t.Errorf("%s: %q, failed %v, want %q", c.name, got, d.Failed(), c.want)
		}
	}
}

// TestRichHasTextNeedsMarkdown checks that a shape that only the plain view
// reads does not count as text.
func TestRichHasTextNeedsMarkdown(t *testing.T) {
	for name, blocks := range map[string]string{
		"known span with children": `[{"type":"paragraph","text":{"type":"bold","children":["x"]}}]`,
		"map with a string number": `[{"type":"map","location":{"latitude":"1","longitude":"2"}}]`,
	} {
		d := richDocOf(t, blocks)
		if d.HasText() && d.Text("") == "" {
			t.Errorf("%s: HasText is true and Text is empty", name)
		}
	}
}

func TestRichPlainAndHasText(t *testing.T) {
	const photo = `"photo":[{"file_id":"AgAC","file_unique_id":"AQAD","width":320,"height":240}]`
	cases := []struct {
		name, blocks, plain string
	}{
		{"paragraph", `[{"type":"paragraph","text":"  hello  "}]`, "hello"},
		{"docs payload", plainDocsPayload, "Report\nPlain, bold bold italic and a link\n1. first\n- done\nfmt.Println(1)\nquoted\nThe Author\nH1 | H2\nv\nCaption\nPhoto caption\nCredit"},
		{"code is kept", `[{"type":"paragraph","text":["run ",{"type":"code","text":"hey tether"}]}]`, "run hey tether"},
		{"hidden URL is left out", `[{"type":"paragraph","text":{"type":"url","text":"here","url":"https://example.com/hey-tether"}}]`, "here"},
		{"no blocks", `[]`, ""},
		{"divider only", `[{"type":"divider"}]`, ""},
		{"anchor only", `[{"type":"anchor","name":"top"}]`, ""},
		{"empty heading", `[{"type":"heading","size":1,"text":""}]`, ""},
		{"empty paragraphs", `[{"type":"paragraph","text":" "},{"type":"paragraph","text":[]},{"type":"paragraph","text":"\n"}]`, ""},
		{"empty list", `[{"type":"list","items":[]}]`, ""},
		{"list item with no blocks", `[{"type":"list","items":[{"label":"1.","blocks":[]}]}]`, ""},
		{"list item with an empty paragraph", `[{"type":"list","items":[{"label":"1.","blocks":[{"type":"paragraph","text":""}]}]}]`, ""},
		{"label waits for the first line with text", `[{"type":"list","items":[{"label":"1.","blocks":[{"type":"paragraph","text":""},{"type":"paragraph","text":"x"}]}]}]`, "1. x"},
		{"photo with no caption", `[{"type":"photo",` + photo + `}]`, ""},
		{"photo with a caption", `[{"type":"photo",` + photo + `,"caption":{"text":"Photo caption"}}]`, "Photo caption"},
		{"buttons only", `[{"type":"buttons","buttons":[{"text":"Open","url":"https://example.com/"}]}]`, ""},
		{"map only", `[{"type":"map","location":{"latitude":-6.2,"longitude":106.816666},"zoom":10}]`, "[map: latitude -6.2, longitude 106.816666]"},
	}
	for _, c := range cases {
		d := richDocOf(t, c.blocks)
		if got := d.Plain(); got != c.plain {
			t.Errorf("%s: Plain = %q, want %q", c.name, got, c.plain)
		}
		if got := d.HasText(); got != (c.plain != "") {
			t.Errorf("%s: HasText = %v", c.name, got)
		}
	}
}
