package telegram

import (
	"maps"
	"strings"
	"unicode"
)

// richMarker returns the Markdown marker of a list item: a checkbox, the
// label when it has a letter or a digit, or a dash.
func richMarker(item map[string]any) string {
	if richBool(item["has_checkbox"]) {
		if richBool(item["is_checked"]) {
			return "- [x]"
		}
		return "- [ ]"
	}
	label := strings.NewReplacer("\n", "", "\r", "").Replace(richStr(item["label"]))
	if r := []rune(label); len(r) > 16 {
		label = string(r[:16])
	}
	label = strings.TrimSpace(label)
	for _, r := range label {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return label
		}
	}
	return "-"
}

// richItem puts the marker before the first line of an item and the indent
// before each other line. It gives no line for an item with no lines.
func richItem(lines []richLine, marker string) []richLine {
	if len(lines) == 0 {
		return nil
	}
	indent := strings.Repeat(" ", len([]rune(marker))+1)
	if lines[0].Media != nil {
		// A media part has one prefix for all its lines, so the marker
		// stands on a line of its own.
		return append([]richLine{{Text: marker}}, richPrefix(lines, indent, indent)...)
	}
	return richPrefix(lines, marker+" ", indent)
}

// list returns the lines of a list block, one item after the other.
func (c *richCtx) list(b map[string]any, depth int) []richLine {
	var out []richLine
	for _, v := range richList(b["items"]) {
		item := richMap(v)
		if item == nil {
			continue
		}
		var lines []richLine
		if blocks := richList(item["blocks"]); len(blocks) > 0 {
			lines = c.blocks(blocks, depth+1)
		} else {
			// The type of an item is a label style, not a block type.
			cp := maps.Clone(item)
			delete(cp, "type")
			lines = c.block(cp, depth+1)
		}
		out = append(out, richItem(lines, richMarker(item))...)
	}
	return out
}
