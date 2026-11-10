package telegram

import (
	"bytes"
	"strconv"
	"strings"
)

// richMask stands for a span or a block that the addressing text hides. It is
// not white space, not a separator, and not a part of a user name (Hermes
// _CODE_PLACEHOLDER).
const richMask = "\uFFFC"

// richFlatMasked has the types that the addressing text hides: code, and the
// label of a link (Hermes _ADDRESSING_MASK_TYPES).
var richFlatMasked = map[string]bool{"code": true, "pre": true, "url": true, "link": true, "text_link": true}

// richFlatKeys are the keys of a block that give one line each.
var richFlatKeys = [...]string{"text", "summary", "expression"}

// richFlat writes the plain text of a rich message into one buffer, so the
// work is linear and each span has a byte offset.
type richFlat struct {
	b      bytes.Buffer // a bytes.Buffer, because textLine takes a line back with Truncate
	lines  int
	mask   bool
	onSpan func(span map[string]any, start, end int)
}

// richFlatten returns the plain text of the blocks, with one line for each
// paragraph-like block and each table row (Hermes _rich_lines). The text is
// not trimmed. With mask, a span or a block of a masked type gives richMask.
// onSpan gets each span object that has a type and is not in a masked span,
// after its text is written, with the byte offsets of that text.
func richFlatten(blocks []any, mask bool, onSpan func(span map[string]any, start, end int)) string {
	f := &richFlat{mask: mask, onSpan: onSpan}
	prefix := ""
	f.blocks(blocks, &prefix, 0)
	return f.b.String()
}

// line starts a new line. It writes the prefix that waits, and clears it.
func (f *richFlat) line(prefix *string) {
	if f.lines > 0 {
		f.b.WriteByte('\n')
	}
	f.lines++
	f.b.WriteString(*prefix)
	*prefix = ""
}

// textLine writes one line with the plain text of a RichText value. A line
// that has a label and no other text is taken back, and the label waits for
// the next line. So an item with no text gives no line.
func (f *richFlat) textLine(prefix *string, v any) {
	label, start, lines := *prefix, f.b.Len(), f.lines
	f.line(prefix)
	mark := f.b.Len()
	f.inline(v, 0)
	if label != "" && f.b.Len() == mark {
		f.b.Truncate(start)
		f.lines, *prefix = lines, label
	}
}

// blocks writes the lines of a block list. prefix is the label of a list
// item. It goes before the first line that the list gives.
func (f *richFlat) blocks(list []any, prefix *string, depth int) {
	if depth > maxRichDepth {
		return
	}
	for _, v := range list {
		b := richMap(v)
		if b == nil {
			continue
		}
		typ := richType(b)
		if f.mask && richFlatMasked[typ] {
			f.line(prefix)
			f.b.WriteString(richMask)
			continue
		}
		for _, key := range richFlatKeys {
			if val, ok := b[key]; ok {
				f.textLine(prefix, val)
			}
		}
		if loc := richMap(b["location"]); typ == "map" && loc != nil {
			f.line(prefix)
			f.b.WriteString("[map: latitude ")
			f.b.WriteString(richFlatFloat(loc["latitude"]))
			f.b.WriteString(", longitude ")
			f.b.WriteString(richFlatFloat(loc["longitude"]))
			f.b.WriteString("]")
		}
		for _, row := range richList(b["cells"]) {
			cells, ok := row.([]any)
			if !ok {
				continue
			}
			f.line(prefix)
			n := 0
			for _, c := range cells {
				cell := richMap(c)
				if cell == nil {
					continue
				}
				if n > 0 {
					f.b.WriteString(" | ")
				}
				n++
				f.inline(cell["text"], 0)
			}
		}
		for _, it := range richList(b["items"]) {
			item := richMap(it)
			if item == nil {
				continue
			}
			label := richStr(item["label"])
			if label == "" {
				label = "-"
			}
			label += " "
			sub := richList(item["blocks"])
			if len(sub) == 0 {
				sub = []any{item}
			}
			f.blocks(sub, &label, depth+1)
		}
		f.blocks(richList(b["blocks"]), prefix, depth+1)
		if c, ok := b["caption"]; ok {
			if obj := richMap(c); obj != nil && richType(obj) == "" {
				for _, key := range [...]string{"text", "credit"} {
					if val, ok := obj[key]; ok {
						f.textLine(prefix, val)
					}
				}
			} else {
				f.textLine(prefix, c)
			}
		}
		if c, ok := b["credit"]; ok {
			f.textLine(prefix, c)
		}
	}
}

// inline writes the plain text of a RichText value (Hermes _rich_inline). It
// never writes the URL of a link.
func (f *richFlat) inline(v any, depth int) {
	if depth > maxRichDepth {
		return
	}
	switch x := v.(type) {
	case string:
		f.b.WriteString(x)
	case []any:
		for _, part := range x {
			f.inline(part, depth+1)
		}
	case map[string]any:
		typ := richType(x)
		if f.mask && richFlatMasked[typ] {
			f.b.WriteString(richMask)
			return
		}
		start := f.b.Len()
		if t := x["text"]; t != nil {
			f.inline(t, depth+1)
		} else if c := x["children"]; c != nil {
			f.inline(c, depth+1)
		} else if s := richStr(x["expression"]); s != "" {
			f.b.WriteString(s)
		} else {
			f.b.WriteString(richStr(x["alternative_text"]))
		}
		if f.onSpan != nil && typ != "" {
			f.onSpan(x, start, f.b.Len())
		}
	}
}

// richFlatFloat returns a JSON number in the shortest decimal form.
func richFlatFloat(v any) string {
	n, _ := v.(float64)
	return strconv.FormatFloat(n, 'f', -1, 64)
}

// Plain returns the pattern text: the plain text with code kept and with no
// hidden URL (Hermes addressing_text).
func (d *richDoc) Plain() (out string) {
	defer d.guard(func() { out = "" })
	return strings.TrimSpace(richFlatten(d.blocks, false, nil))
}

// HasText reports if the message has text content. A message with only a
// divider, an anchor, or empty paragraphs has none. The plain view and the
// Markdown view must both have content, so a true result means that Text has
// a line with text or a media part with a caption.
func (d *richDoc) HasText() (ok bool) {
	if d.Plain() == "" {
		return false
	}
	defer d.guard(func() { ok = false })
	return richLinesHaveContent(d.Lines(""))
}
