package telegram

import (
	"strings"
	"testing"
)

func TestRichTable(t *testing.T) {
	cases := []struct {
		name   string
		blocks string
		want   []string
	}{
		{"the table of the docs payload", `[{"type": "table", "is_bordered": true, "caption": "Caption", "cells": [
			[{"text": "H1", "is_header": true, "align": "left", "valign": "top"}, {"text": "H2", "is_header": true, "align": "center", "valign": "top"}],
			[{"text": {"type": "code", "text": "v"}, "colspan": 2, "align": "left", "valign": "middle"}]]}]`,
			[]string{"| H1 | H2 |", "| :--- | :---: |", "| `v` |  |", "", "Caption"}},
		{"no header cell", `[{"type":"table","cells":[
			[{"text":"a","align":"right"},{"text":"b"}],
			[{"text":"c"},{"text":"d"}]]}]`,
			[]string{"|  |  |", "| ---: | --- |", "| a | b |", "| c | d |"}},
		{"one header cell is enough", `[{"type":"table","cells":[
			[{"text":"a"},{"text":"b","is_header":true}],
			[{"text":"c","is_header":true},{"text":"d"}]]}]`,
			[]string{"| a | b |", "| --- | --- |", "| c | d |"}},
		{"a header cell in row 2 only", `[{"type":"table","cells":[
			[{"text":"a"}],
			[{"text":"b","is_header":true}]]}]`,
			[]string{"|  |", "| --- |", "| a |", "| b |"}},
		{"the four alignments", `[{"type":"table","cells":[
			[{"text":"l","is_header":true,"align":"left"},{"text":"c","is_header":true,"align":"center"},{"text":"r","is_header":true,"align":"right"},{"text":"n","is_header":true},{"text":"u","is_header":true,"align":"justify"}],
			[{"text":"1","align":"right"},{"text":"2"},{"text":"3"},{"text":"4"},{"text":"5"}]]}]`,
			[]string{"| l | c | r | n | u |", "| :--- | :---: | ---: | --- | --- |", "| 1 | 2 | 3 | 4 | 5 |"}},
		{"styles in a cell", `[{"type":"table","cells":[[{"text":["a ",{"type":"bold","text":"b"}],"is_header":true}],[{"text":{"type":"url","text":"docs","url":"https://t.me/"}}]]}]`,
			[]string{"| a **b** |", "| --- |", "| [docs](https://t.me/) |"}},
		{"a pipe in a cell", `[{"type":"table","cells":[[{"text":"a|b","is_header":true}],[{"text":{"type":"code","text":"x || y"}}]]}]`,
			[]string{`| a\|b |`, "| --- |", "| `x \\|\\| y` |"}},
		{"a line feed in a cell", `[{"type":"table","cells":[[{"text":"a\nb","is_header":true}],[{"text":" c\n"}]]}]`,
			[]string{"| a b |", "| --- |", "| c |"}},
		{"colspan", `[{"type":"table","cells":[
			[{"text":"a","is_header":true,"colspan":2,"align":"center"},{"text":"b","is_header":true,"align":"right"}],
			[{"text":"c"},{"text":"d","colspan":2}],
			[{"text":"e","colspan":0},{"text":"f","colspan":-3},{"text":"g","colspan":1}]]}]`,
			[]string{"| a |  | b |", "| :---: | --- | ---: |", "| c | d |  |", "| e | f | g |"}},
		{"rowspan is ignored", `[{"type":"table","cells":[[{"text":"a","is_header":true,"rowspan":2},{"text":"b","is_header":true}],[{"text":"c"}]]}]`,
			[]string{"| a | b |", "| --- | --- |", "| c |  |"}},
		{"ragged rows", `[{"type":"table","cells":[
			[{"text":"a"},{"text":"b"},{"text":"c"}],
			[{"text":"d"}],
			[{"text":"e"},{"text":"f"}]]}]`,
			[]string{"|  |  |  |", "| --- | --- | --- |", "| a | b | c |", "| d |  |  |", "| e | f |  |"}},
		{"a short header row", `[{"type":"table","cells":[
			[{"text":"h","is_header":true,"align":"center"}],
			[{"text":"a"},{"text":"b"},{"text":"c"}]]}]`,
			[]string{"| h |  |  |", "| :---: | --- | --- |", "| a | b | c |"}},
		{"a cell with no text", `[{"type":"table","cells":[[{"is_header":true},{"text":"h","is_header":true}],[{"text":"a"},{}]]}]`,
			[]string{"|  | h |", "| --- | --- |", "| a |  |"}},
		{"cells of the wrong type", `[{"type":"table","cells":5}]`, nil},
		{"no cells", `[{"type":"table"}]`, nil},
		{"no rows", `[{"type":"table","cells":[]}]`, nil},
		{"rows with no cells", `[{"type":"table","cells":[[],5,"x",null]}]`, nil},
		{"a row and a cell of the wrong type", `[{"type":"table","cells":[5,[],[{"text":"h","is_header":true},5],"x",[{"text":"a"},"y"]]}]`,
			[]string{"| h |  |", "| --- | --- |", "| a |  |"}},
		{"a caption and no cells", `[{"type":"table","cells":5,"caption":"Caption"}]`, []string{"Caption"}},
		{"a caption with a style and a line feed", `[{"type":"table","caption":["Table ",{"type":"bold","text":"1"},"\nnext"],"cells":[[{"text":"a","is_header":true}]]}]`,
			[]string{"| a |", "| --- |", "", "Table **1** next"}},
		{"table in a quotation", `[{"type":"blockquote","blocks":[{"type":"table","caption":"Caption","cells":[
			[{"text":"H1","is_header":true},{"text":"H2","is_header":true}],
			[{"text":"a"},{"text":"b"}]]}]}]`,
			[]string{"> | H1 | H2 |", "> | --- | --- |", "> | a | b |", ">", "> Caption"}},
		{"table between paragraphs", `[{"type":"paragraph","text":"before"},{"type":"table","cells":[[{"text":"h","is_header":true}],[{"text":"a"}]]},{"type":"paragraph","text":"after"}]`,
			[]string{"before", "", "| h |", "| --- |", "| a |", "", "after"}},
		{"unknown block with cells", `[{"type":"grid","text":"t","cells":[[{"text":"h","is_header":true}],[{"text":"a"}]],"items":[{"blocks":[{"type":"paragraph","text":"i"}]}],"blocks":[{"type":"paragraph","text":"p"}]}]`,
			[]string{"t", "", "| h |", "| --- |", "| a |", "", "- i", "", "p"}},
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

func TestRichTableColumnLimit(t *testing.T) {
	// A colspan of 1000000000 gives 20 columns: the cell and 19 empty cells.
	wide := "| x |" + strings.Repeat("  |", 19)
	rule := "|" + strings.Repeat(" --- |", 20)
	d := richDocOf(t, `[{"type":"table","cells":[[{"text":"x","is_header":true,"colspan":1000000000},{"text":"cut","is_header":true}]]}]`)
	if got, want := d.Text(""), wide+"\n"+rule; got != want {
		t.Errorf("Text =\n%s\nwant\n%s", got, want)
	}

	// A row of 25 cells keeps its first 20 cells, and a short row gets 20 cells.
	var cells []string
	for range 25 {
		cells = append(cells, `{"text":"c","is_header":true}`)
	}
	d = richDocOf(t, `[{"type":"table","cells":[[`+strings.Join(cells, ",")+`],[{"text":"x","colspan":3}]]}]`)
	want := "|" + strings.Repeat(" c |", 20) + "\n" + rule + "\n" + wide
	if got := d.Text(""); got != want {
		t.Errorf("Text =\n%s\nwant\n%s", got, want)
	}
	for line := range strings.SplitSeq(d.Text(""), "\n") {
		if n := strings.Count(line, "|"); n != 21 {
			t.Errorf("line %q has %d pipes, want 21", line, n)
		}
	}
}

func TestRichTableUnknown(t *testing.T) {
	cases := []struct{ name, blocks, want string }{
		{"table is a known type", `[{"type":"table","cells":[[{"text":"a"}]]}]`, ""},
		{"unknown span type in a cell and in the caption", `[{"type":"table","caption":{"type":"wave","text":"c"},"cells":[[{"text":{"type":"glow","text":"a"}}]]}]`, "glow,wave"},
		{"unknown block with cells", `[{"type":"grid","cells":[[{"text":"a"}]]}]`, "grid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(richDocOf(t, tc.blocks).Unknown(), ","); got != tc.want {
				t.Errorf("Unknown = %q, want %q", got, tc.want)
			}
		})
	}
}
