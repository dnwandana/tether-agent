package telegram

import (
	"sort"
	"strconv"
	"strings"
)

// richLine is one line of the Markdown text, or one media part when Media
// is not nil.
type richLine struct {
	Text  string
	Media *richMedia
}

// richMedia is one media part of a rich message. Video holds a video and an
// animation.
type richMedia struct {
	Kind        string
	Photo       []PhotoSize
	Video       *Video
	Audio       *Audio
	Document    *Document
	Voice       *Voice
	Placeholder string
	Caption     string
	Prefix      string
}

// Render returns each line of note, then each line of the caption, each with
// the prefix of the part.
func (m *richMedia) Render(note string) string {
	parts := strings.Split(note, "\n")
	if m.Caption != "" {
		parts = append(parts, strings.Split(m.Caption, "\n")...)
	}
	for i, p := range parts {
		parts[i] = m.Prefix + p
	}
	return strings.Join(parts, "\n")
}

// richPrefix puts first before the first line and rest before each other
// line. It changes the lines in place and returns them.
func richPrefix(lines []richLine, first, rest string) []richLine {
	for i := range lines {
		prefix := rest
		if i == 0 {
			prefix = first
		}
		l := &lines[i]
		switch {
		case l.Media != nil:
			// Copy the media part, because other lines can share it.
			m := *l.Media
			m.Prefix = prefix + m.Prefix
			l.Media = &m
		case l.Text == "":
			l.Text = strings.TrimRight(prefix, " ")
		default:
			l.Text = prefix + l.Text
		}
	}
	return lines
}

// richJoin joins the lines with line feeds. A media line gives the text of
// Render with the note of the part.
func richJoin(lines []richLine, note func(m *richMedia) string) string {
	parts := make([]string, len(lines))
	for i, l := range lines {
		if l.Media != nil {
			parts[i] = l.Media.Render(note(l.Media))
		} else {
			parts[i] = l.Text
		}
	}
	return strings.Join(parts, "\n")
}

// Lines returns the lines of the message: text lines in Markdown and media
// parts, in block order.
func (d *richDoc) Lines(botUser string) (out []richLine) {
	if d.failed {
		return nil
	}
	defer d.guard(func() { out = nil })
	out = newRichCtx(botUser).blocks(d.blocks, 0)
	if strings.TrimPrefix(botUser, "@") != "" && !richLinesHaveContent(out) && d.HasText() {
		return d.Lines("")
	}
	return out
}

// richLinesHaveContent reports whether one line has text, or one media part
// has a caption.
func richLinesHaveContent(lines []richLine) bool {
	for _, l := range lines {
		s := l.Text
		if l.Media != nil {
			s = l.Media.Caption
		}
		if strings.TrimSpace(s) != "" {
			return true
		}
	}
	return false
}

// Text returns the Markdown text of the message. Each media part gives its
// placeholder line.
func (d *richDoc) Text(botUser string) (out string) {
	if d.failed {
		return ""
	}
	defer d.guard(func() { out = "" })
	return richJoin(d.Lines(botUser), func(m *richMedia) string { return m.Placeholder })
}

// Unknown returns the sorted names of the block types and the span types
// that the converter does not know.
func (d *richDoc) Unknown() (out []string) {
	if d.failed {
		return nil
	}
	defer d.guard(func() { out = nil })
	c := newRichCtx("")
	c.blocks(d.blocks, 0)
	for name := range c.unknown {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// richTextLines returns the lines of s. It removes the line feeds at the
// start and the end, and it gives no line for a text with only white space.
func richTextLines(s string) []richLine {
	s = strings.Trim(s, "\n")
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	out := make([]richLine, len(parts))
	for i, p := range parts {
		out[i] = richLine{Text: p}
	}
	return out
}

// richOneLine replaces each line feed with a space and trims the result.
func richOneLine(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
}

// richGap joins the parts that have lines, with one empty line between two
// parts.
func richGap(parts ...[]richLine) []richLine {
	var out []richLine
	for _, p := range parts {
		if len(p) == 0 {
			continue
		}
		if len(out) > 0 {
			out = append(out, richLine{})
		}
		out = append(out, p...)
	}
	return out
}

// richLanguage keeps the characters of a code language that are safe in a
// fence line, at most 32 of them.
func richLanguage(s string) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < 32; i++ {
		ch := s[i]
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' ||
			strings.IndexByte("_+.#-", ch) >= 0 {
			b.WriteByte(ch)
		}
	}
	return b.String()
}

// caption returns the text of a caption object: the inline text, and the
// credit after a dash. The text can have line feeds.
func (c *richCtx) caption(v any, depth int) string {
	m := richMap(v)
	if m == nil {
		return ""
	}
	text := strings.TrimSpace(c.inline(m["text"], depth+1))
	credit := richOneLine(c.inline(m["credit"], depth+1))
	switch {
	case text != "" && credit != "":
		return text + " — " + credit
	case text != "":
		return text
	case credit != "":
		return "— " + credit
	}
	return ""
}

// blocks returns the lines of the blocks, with one empty line between two
// blocks that give lines.
func (c *richCtx) blocks(list []any, depth int) []richLine {
	var out []richLine
	for _, item := range list {
		ls := c.block(richMap(item), depth)
		if len(ls) == 0 {
			continue
		}
		if len(out) > 0 {
			out = append(out, richLine{})
		}
		out = append(out, ls...)
	}
	return out
}

// quote puts the quotation marker before the lines and adds the credit line.
func (c *richCtx) quote(lines []richLine, b map[string]any, depth int) []richLine {
	lines = richPrefix(lines, "> ", "> ")
	if credit := richOneLine(c.inline(b["credit"], depth+1)); credit != "" {
		lines = append(lines, richLine{Text: "> — " + credit})
	}
	return lines
}

// block returns the lines of one block.
func (c *richCtx) block(b map[string]any, depth int) []richLine {
	c.tail = richTailNone
	if depth > maxRichDepth {
		return nil
	}
	switch richStr(b["type"]) {
	case "paragraph", "footer":
		return richTextLines(c.inline(b["text"], depth+1))
	case "heading":
		text := richOneLine(c.inline(b["text"], depth+1))
		if text == "" {
			return nil
		}
		size := min(max(richInt(b["size"]), 1), 6)
		return []richLine{{Text: strings.Repeat("#", size) + " " + text}}
	case "pre":
		content := strings.TrimRight(c.plain(b["text"], depth+1), "\n")
		if strings.TrimSpace(content) == "" {
			return nil
		}
		fence := richTicks(content, 3)
		out := []richLine{{Text: fence + richLanguage(richStr(b["language"]))}}
		for p := range strings.SplitSeq(content, "\n") {
			out = append(out, richLine{Text: p})
		}
		return append(out, richLine{Text: fence})
	case "divider":
		return []richLine{{Text: "---"}}
	case "mathematical_expression":
		e := strings.TrimSpace(richStr(b["expression"]))
		if e == "" {
			return nil
		}
		return richTextLines("$$" + e + "$$")
	case "anchor":
		return nil
	case "blockquote":
		return c.quote(c.blocks(richList(b["blocks"]), depth+1), b, depth)
	case "expandable_blockquote", "pullquote":
		return c.quote(richTextLines(c.inline(b["text"], depth+1)), b, depth)
	case "collage", "slideshow":
		return richGap(c.blocks(richList(b["blocks"]), depth+1),
			richTextLines(c.caption(b["caption"], depth+1)))
	case "details":
		var summary []richLine
		if s := richOneLine(c.inline(b["summary"], depth+1)); s != "" {
			summary = []richLine{{Text: "**" + s + "**"}}
		}
		return richGap(summary, c.blocks(richList(b["blocks"]), depth+1))
	case "map":
		var out []richLine
		var loc Location
		if richObject(b["location"], &loc) {
			out = append(out, richLine{Text: "[map: latitude " +
				strconv.FormatFloat(loc.Latitude, 'f', -1, 64) + ", longitude " +
				strconv.FormatFloat(loc.Longitude, 'f', -1, 64) + "]"})
		}
		return append(out, richTextLines(c.caption(b["caption"], depth+1))...)
	case "buttons":
		return c.buttons(b, depth)
	case "list":
		return c.list(b, depth)
	case "table":
		return c.table(b, depth)
	case "photo", "video", "animation", "audio", "document", "voice_note":
		return c.media(b, depth)
	}
	return c.unknownBlock(b, depth)
}

// buttons returns one line for each button that has a text or a URL.
func (c *richCtx) buttons(b map[string]any, depth int) []richLine {
	var out []richLine
	for _, item := range richList(b["buttons"]) {
		btn := richMap(item)
		text := richOneLine(c.inline(btn["text"], depth+1))
		u := richStr(btn["url"])
		switch {
		case text == "" && u == "":
		case u == "":
			out = append(out, richLine{Text: "[button: " + text + "]"})
		default:
			out = append(out, richLine{Text: "[button: " + text + "](" + u + ")"})
		}
	}
	return out
}

// unknownBlock reads the fields of a block of an unknown type that can hold
// text, and records the type name.
func (c *richCtx) unknownBlock(b map[string]any, depth int) []richLine {
	if name := richStr(b["type"]); name != "" {
		c.unknown[name] = true
	}
	return richGap(
		richTextLines(c.inline(b["text"], depth+1)),
		richTextLines(c.inline(b["summary"], depth+1)),
		richTextLines(richStr(b["expression"])),
		c.table(b, depth),
		c.list(b, depth),
		c.blocks(richList(b["blocks"]), depth+1),
	)
}
