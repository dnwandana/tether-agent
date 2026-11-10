package telegram

import (
	"path/filepath"
	"strings"
)

// media returns the lines of a media block: one media part, or the caption
// lines when the block has no usable object.
func (c *richCtx) media(b map[string]any, depth int) []richLine {
	m := &richMedia{Kind: richType(b)}
	ok := false
	switch m.Kind {
	case "photo":
		for _, item := range richList(b["photo"]) {
			var p PhotoSize
			if richObject(item, &p) && p.FileID != "" {
				m.Photo = append(m.Photo, p)
			}
		}
		ok = len(m.Photo) > 0
		m.Placeholder = "[photo]"
	case "video", "animation":
		var v Video
		if richObject(b[m.Kind], &v) && v.FileID != "" {
			m.Video = &v
			ok = true
		}
		m.Placeholder = "[" + m.Kind + "]"
		if ok && m.Kind == "video" && v.FileName != "" {
			m.Placeholder = "[video: " + displayName(v.FileName, "") + "]"
		}
	case "audio":
		var a Audio
		if richObject(b["audio"], &a) && a.FileID != "" {
			m.Audio = &a
			ok = true
			m.Placeholder = "[audio]"
			if a.FileName != "" {
				m.Placeholder = "[audio: " + displayName(a.FileName, "") + "]"
			}
		}
	case "document":
		var d Document
		if richObject(b["document"], &d) && d.FileID != "" {
			m.Document = &d
			ok = true
			ext := strings.ToLower(filepath.Ext(d.FileName))
			if ext == "" {
				ext = extByMime[strings.ToLower(d.MimeType)]
			}
			m.Placeholder = "[document: " + displayName(d.FileName, ext) + "]"
		}
	case "voice_note":
		var v Voice
		if richObject(b["voice_note"], &v) && v.FileID != "" {
			m.Voice = &v
			ok = true
			m.Placeholder = "[voice note]"
		}
	}
	m.Caption = c.caption(b["caption"], depth+1)
	if ok {
		return []richLine{{Media: m}}
	}
	return richTextLines(m.Caption)
}

// KindName returns the name of the kind for a note.
func (m *richMedia) KindName() string {
	switch m.Kind {
	case "audio":
		return "audio file"
	case "voice_note":
		return "voice note"
	}
	return m.Kind
}

// MediaCount returns the number of media parts of the message.
func (d *richDoc) MediaCount() (n int) {
	defer d.guard(func() { n = 0 })
	for _, l := range d.Lines("") {
		if l.Media != nil {
			n++
		}
	}
	return n
}
