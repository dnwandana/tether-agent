package telegram

import (
	"encoding/json"
	"testing"
	"unicode/utf8"
)

// fuzzRichDocsBlocks is a block list that is built from the field tables of
// the Bot API docs. It is not a real update.
const fuzzRichDocsBlocks = `[
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

// FuzzRichMessage checks the converter for each message that decodes: no view
// panics, the converter does not fail, and each output is valid UTF-8.
func FuzzRichMessage(f *testing.F) {
	for _, rich := range []string{
		`{"blocks":` + fuzzRichDocsBlocks + `}`,
		`{"blocks":[{"type":"list","items":5}]}`,
		`{"blocks":[{"type":"table","cells":5}]}`,
		`{"blocks":[` + hostileNest(`{"type":"quote","blocks":[`, `{"type":"paragraph","text":"deep"}`, `]}`, 200) + `]}`,
		`{"blocks":[{"type":"paragraph","text":` + hostileNest(`{"type":"bold","children":[`, `"deep"`, `]}`, 200) + `}]}`,
		`{"blocks":[{"type":"paragraph","text":[{"type":"mention","text":"@tether_bot","username":"tether_bot"},", /new@tether_bot 😀 ",{"type":"code","text":"@tether_bot"},{"type":"text_mention","text":"Ana","user":{"id":7}}]}]}`,
		`{"blocks":[{"type":"blockquote","blocks":[{"type":"photo","photo":[{"file_id":"p1"}],"caption":{"text":"@tether_bot a\nb","credit":"c"}}]},{"type":"document","document":{"file_id":"d1","file_name":"a]b.txt"}},{"type":"photo","photo":5}]}`,
		`{"blocks":[{"type":"pre","language":"go\n` + "`" + `","text":"a ` + "`" + ` b"},{"type":"list","items":[{"label":"1.\né","blocks":[{"type":"paragraph","text":"é"}]}]}]}`,
		`5`, `"x"`, `[]`, `null`,
	} {
		f.Add([]byte(`{"message_id":1,"chat":{"id":1,"type":"private"},"rich_message":` + rich + `}`))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var m Message
		if err := json.Unmarshal(data, &m); err != nil {
			t.Skip()
		}
		m.Text, m.Caption = "", ""
		d := richOf(m)
		if d == nil {
			return
		}
		valid := func(name, s string) {
			if !utf8.ValidString(s) {
				t.Fatalf("%s is not valid UTF-8: %q", name, s)
			}
		}
		valid("the pattern text", d.Plain())
		addr, entities := d.Addressing()
		valid("the addressing text", addr)
		for _, e := range entities {
			valid("an entity text", e.Text)
		}
		for _, l := range d.Lines("tether_bot") {
			valid("a line", l.Text)
			if l.Media != nil {
				valid("a placeholder", l.Media.Placeholder)
				valid("a caption", l.Media.Caption)
				valid("a prefix", l.Media.Prefix)
			}
		}
		valid("the Markdown text", d.Text("tether_bot"))
		for _, name := range d.Unknown() {
			valid("an unknown type name", name)
		}
		if n := d.MediaCount(); n < 0 {
			t.Fatalf("MediaCount = %d", n)
		}
		if d.HasText() && d.Text("") == "" && d.MediaCount() == 0 {
			t.Fatalf("HasText is true, but the Markdown text is empty, for %q", data)
		}
		if d.Failed() {
			t.Fatalf("the converter failed for %q", data)
		}
	})
}
