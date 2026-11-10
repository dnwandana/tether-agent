package telegram

import (
	"strings"
	"testing"
)

// richMediaTestParts returns the media parts of d, in block order.
func richMediaTestParts(d *richDoc) []*richMedia {
	var out []*richMedia
	for _, l := range d.Lines("") {
		if l.Media != nil {
			out = append(out, l.Media)
		}
	}
	return out
}

func TestRichMediaPlaceholders(t *testing.T) {
	cases := []struct {
		name, block, kind, kindName, placeholder string
	}{
		{"photo", `{"type":"photo","photo":[{"file_id":"p1","width":90,"height":60},{"file_id":"p2","width":320,"height":240}]}`, "photo", "photo", "[photo]"},
		{"video", `{"type":"video","video":{"file_id":"v1"}}`, "video", "video", "[video]"},
		{"video with a name", `{"type":"video","video":{"file_id":"v1","file_name":"clip.mp4"}}`, "video", "video", "[video: clip.mp4]"},
		{"animation", `{"type":"animation","animation":{"file_id":"g1","file_name":"fun.gif"}}`, "animation", "animation", "[animation]"},
		{"audio", `{"type":"audio","audio":{"file_id":"a1"}}`, "audio", "audio file", "[audio]"},
		{"audio with a name", `{"type":"audio","audio":{"file_id":"a1","file_name":"song.mp3"}}`, "audio", "audio file", "[audio: song.mp3]"},
		{"document", `{"type":"document","document":{"file_id":"d1","file_name":"report.pdf"}}`, "document", "document", "[document: report.pdf]"},
		{"document with an unsafe name", `{"type":"document","document":{"file_id":"d1","file_name":"a]b\nc.txt"}}`, "document", "document", "[document: a_b_c.txt]"},
		{"document with no name", `{"type":"document","document":{"file_id":"d1","mime_type":"application/pdf"}}`, "document", "document", "[document: document.pdf]"},
		{"voice note", `{"type":"voice_note","voice_note":{"file_id":"n1"}}`, "voice_note", "voice note", "[voice note]"},
	}
	for _, c := range cases {
		d := richDocOf(t, "["+c.block+"]")
		parts := richMediaTestParts(d)
		if len(parts) != 1 {
			t.Errorf("%s: %d media parts, want 1", c.name, len(parts))
			continue
		}
		m := parts[0]
		if m.Kind != c.kind || m.KindName() != c.kindName || m.Placeholder != c.placeholder || m.Caption != "" || m.Prefix != "" {
			t.Errorf("%s: %+v, kind name %q", c.name, m, m.KindName())
		}
		if got := d.Text(""); got != c.placeholder {
			t.Errorf("%s: text %q, want %q", c.name, got, c.placeholder)
		}
	}
}

func TestRichMediaObjects(t *testing.T) {
	d := richDocOf(t, `[
		{"type":"photo","photo":[{"file_id":"p1","file_unique_id":"u1","width":90,"height":60},{"file_id":"p2","file_unique_id":"u2","width":320,"height":240,"file_size":900}]},
		{"type":"video","video":{"file_id":"v1","file_unique_id":"uv","file_name":"clip.mp4","mime_type":"video/mp4","file_size":7}},
		{"type":"animation","animation":{"file_id":"g1","file_unique_id":"ug","file_name":"fun.gif","mime_type":"image/gif"}},
		{"type":"audio","audio":{"file_id":"a1","file_name":"song.mp3"}},
		{"type":"document","document":{"file_id":"d1","file_name":"notes.txt","mime_type":"text/plain"}},
		{"type":"voice_note","voice_note":{"file_id":"n1","duration":3}}]`)
	p := richMediaTestParts(d)
	if len(p) != 6 {
		t.Fatalf("%d media parts, want 6", len(p))
	}
	if len(p[0].Photo) != 2 || p[0].Photo[1] != (PhotoSize{FileID: "p2", FileUniqueID: "u2", Width: 320, Height: 240, FileSize: 900}) {
		t.Errorf("photo: %+v", p[0].Photo)
	}
	if p[1].Video == nil || *p[1].Video != (Video{FileID: "v1", FileUniqueID: "uv", FileName: "clip.mp4", MimeType: "video/mp4", FileSize: 7}) {
		t.Errorf("video: %+v", p[1].Video)
	}
	if p[2].Video == nil || *p[2].Video != (Video{FileID: "g1", FileUniqueID: "ug", FileName: "fun.gif", MimeType: "image/gif"}) {
		t.Errorf("animation: %+v", p[2].Video)
	}
	if p[3].Audio == nil || p[3].Audio.FileID != "a1" || p[3].Audio.FileName != "song.mp3" {
		t.Errorf("audio: %+v", p[3].Audio)
	}
	if p[4].Document == nil || p[4].Document.FileID != "d1" || p[4].Document.MimeType != "text/plain" {
		t.Errorf("document: %+v", p[4].Document)
	}
	if p[5].Voice == nil || p[5].Voice.FileID != "n1" || p[5].Voice.Duration != 3 {
		t.Errorf("voice note: %+v", p[5].Voice)
	}
	if n := d.MediaCount(); n != 6 {
		t.Errorf("MediaCount = %d, want 6", n)
	}
}

func TestRichMediaNoUsableObject(t *testing.T) {
	cases := []struct{ name, block, want string }{
		{"photo is a number", `{"type":"photo","photo":5}`, ""},
		{"photo is a number, with a caption", `{"type":"photo","photo":5,"caption":{"text":"cap"}}`, "cap"},
		{"empty photo array, with a caption", `{"type":"photo","photo":[],"caption":{"text":"cap","credit":"Ana"}}`, "cap — Ana"},
		{"photo sizes with no file id", `{"type":"photo","photo":[{"file_id":""},7]}`, ""},
		{"missing object", `{"type":"voice_note","caption":{"text":"listen"}}`, "listen"},
		{"object is a string", `{"type":"document","document":"x"}`, ""},
		{"empty file id", `{"type":"video","video":{"file_id":""},"caption":{"text":"watch"}}`, "watch"},
		{"wrong key", `{"type":"animation","video":{"file_id":"v1"}}`, ""},
		{"file id is a number", `{"type":"audio","audio":{"file_id":5}}`, ""},
	}
	for _, c := range cases {
		d := richDocOf(t, "["+c.block+"]")
		if n := d.MediaCount(); n != 0 {
			t.Errorf("%s: MediaCount = %d, want 0", c.name, n)
		}
		if got := d.Text(""); got != c.want {
			t.Errorf("%s: text %q, want %q", c.name, got, c.want)
		}
		if u := d.Unknown(); u != nil {
			t.Errorf("%s: unknown types %v", c.name, u)
		}
	}
	// One usable size is sufficient for a media part.
	d := richDocOf(t, `[{"type":"photo","photo":[7,{"file_id":""},{"file_id":"p1"}]}]`)
	if p := richMediaTestParts(d); len(p) != 1 || len(p[0].Photo) != 1 || p[0].Photo[0].FileID != "p1" {
		t.Errorf("one usable size: %+v", p)
	}
}

func TestRichMediaCount(t *testing.T) {
	if n := richDocOf(t, `[]`).MediaCount(); n != 0 {
		t.Errorf("no blocks: MediaCount = %d", n)
	}
	if n := richDocOf(t, `[{"type":"paragraph","text":"hi"},{"type":"divider"}]`).MediaCount(); n != 0 {
		t.Errorf("text only: MediaCount = %d", n)
	}
	d := richDocOf(t, `[
		{"type":"paragraph","text":"hi"},
		{"type":"photo","photo":[{"file_id":"p1"}]},
		{"type":"photo","photo":5},
		{"type":"blockquote","blocks":[{"type":"voice_note","voice_note":{"file_id":"n1"}}]},
		{"type":"list","items":[{"label":"1.","blocks":[{"type":"document","document":{"file_id":"d1","file_name":"a.txt"}}]}]}]`)
	if n := d.MediaCount(); n != 3 {
		t.Errorf("MediaCount = %d, want 3", n)
	}
}

func TestRichMediaTypesAreKnown(t *testing.T) {
	d := richDocOf(t, `[{"type":"photo"},{"type":"video"},{"type":"animation"},{"type":"audio"},
		{"type":"document"},{"type":"voice_note"},{"type":"sidebar","text":"x"}]`)
	if u := d.Unknown(); len(u) != 1 || u[0] != "sidebar" {
		t.Errorf("unknown types %v, want [sidebar]", u)
	}
}

func TestRichMediaText(t *testing.T) {
	d := richDocOf(t, `[
		{"type":"paragraph","text":"Before"},
		{"type":"photo","photo":[{"file_id":"AgAC","file_unique_id":"AQAD","width":320,"height":240}],"caption":{"text":"Photo caption","credit":"Credit"}},
		{"type":"paragraph","text":"After"}]`)
	if got, want := d.Text(""), "Before\n\n[photo]\nPhoto caption — Credit\n\nAfter"; got != want {
		t.Errorf("text:\n%s\nwant:\n%s", got, want)
	}
	lines := d.Lines("")
	if len(lines) != 5 || lines[2].Media == nil || lines[2].Media.Caption != "Photo caption — Credit" {
		t.Errorf("lines: %+v", lines)
	}
}

func TestRichMediaInContainer(t *testing.T) {
	// A photo in a quotation. Each line of the caption has the prefix.
	d := richDocOf(t, `[{"type":"blockquote","blocks":[{"type":"photo","photo":[{"file_id":"p1"}],"caption":{"text":"line one\nline two"}}]}]`)
	if got, want := d.Text(""), "> [photo]\n> line one\n> line two"; got != want {
		t.Errorf("quotation: %q, want %q", got, want)
	}
	if p := richMediaTestParts(d); len(p) != 1 || p[0].Prefix != "> " {
		t.Errorf("quotation: parts %+v", p)
	}

	// Two quotations give two prefixes.
	d = richDocOf(t, `[{"type":"blockquote","blocks":[{"type":"blockquote","blocks":[{"type":"photo","photo":[{"file_id":"p1"}]}]}]}]`)
	if got, want := d.Text(""), "> > [photo]"; got != want {
		t.Errorf("two quotations: %q, want %q", got, want)
	}

	// A photo after a paragraph in a list item has the indent of the item.
	d = richDocOf(t, `[{"type":"list","items":[{"label":"1.","blocks":[
		{"type":"paragraph","text":"see"},
		{"type":"photo","photo":[{"file_id":"p1"}],"caption":{"text":"cap"}}]}]}]`)
	p := richMediaTestParts(d)
	if len(p) != 1 || p[0].Prefix != "   " {
		t.Fatalf("list item: parts %+v", p)
	}
	if got, want := p[0].Render("[note]\nmore"), "   [note]\n   more\n   cap"; got != want {
		t.Errorf("list item: render %q, want %q", got, want)
	}
	if got := d.Text(""); !strings.HasPrefix(got, "1. see\n") || !strings.HasSuffix(got, "\n   [photo]\n   cap") {
		t.Errorf("list item: text %q", got)
	}

	// A block with no usable object gives each line of its caption with the prefix.
	d = richDocOf(t, `[{"type":"blockquote","blocks":[{"type":"photo","photo":[],"caption":{"text":"line one\nline two"}}]}]`)
	if got, want := d.Text(""), "> line one\n> line two"; got != want {
		t.Errorf("caption only: %q, want %q", got, want)
	}
}

func TestRichMediaInCollage(t *testing.T) {
	d := richDocOf(t, `[{"type":"collage","blocks":[
		{"type":"photo","photo":[{"file_id":"p1"}],"caption":{"text":"own"}},
		{"type":"photo","photo":[{"file_id":"p2"}]}],
		"caption":{"text":"Trip","credit":"Ana"}}]`)
	if n := d.MediaCount(); n != 2 {
		t.Errorf("MediaCount = %d, want 2", n)
	}
	got := d.Text("")
	if !strings.HasPrefix(got, "[photo]\nown\n") || strings.Count(got, "[photo]") != 2 || !strings.HasSuffix(got, "\nTrip — Ana") {
		t.Errorf("collage: %q", got)
	}
}

// richMediaDocsBlocks is a block list that is built from the field tables of the
// Bot API docs. It is not a real update.
const richMediaDocsBlocks = `[
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

func TestRichMediaDocsPayload(t *testing.T) {
	d := richDocOf(t, richMediaDocsBlocks)
	want := strings.Join([]string{
		"# Report",
		"",
		"Plain, **bold *bold italic*** and [a link](https://t.me/)",
		"",
		"1. first",
		"- [x] done",
		"",
		"```go",
		"fmt.Println(1)",
		"```",
		"",
		"> quoted",
		"> — The Author",
		"",
		"| H1 | H2 |",
		"| :--- | :---: |",
		"| `v` |  |",
		"",
		"Caption",
		"",
		"[photo]",
		"Photo caption — Credit",
		"",
		"---",
	}, "\n")
	if got := d.Text(""); got != want {
		t.Errorf("text:\n%s\nwant:\n%s", got, want)
	}
	if n := d.MediaCount(); n != 1 {
		t.Errorf("MediaCount = %d, want 1", n)
	}
	if u := d.Unknown(); u != nil {
		t.Errorf("unknown types %v", u)
	}
}
