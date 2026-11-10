package telegram

import "strings"

// richMaxColumns is the largest number of columns of a table.
const richMaxColumns = 20

// table returns the lines of a GFM table and its caption.
func (c *richCtx) table(b map[string]any, depth int) []richLine {
	var rows [][]string
	var firstAlign []string
	header := false
	width := 0
	for _, r := range richList(b["cells"]) {
		var row, align []string
		isHeader := false
		for _, item := range richList(r) {
			if len(row) >= richMaxColumns {
				break
			}
			cell := richMap(item)
			text := strings.ReplaceAll(c.inline(cell["text"], depth+1), "\n", " ")
			text = strings.TrimSpace(strings.ReplaceAll(text, "|", `\|`))
			row = append(row, text)
			align = append(align, richAlign(richStr(cell["align"])))
			if richBool(cell["is_header"]) {
				isHeader = true
			}
			for n := min(richInt(cell["colspan"]), richMaxColumns); n > 1 && len(row) < richMaxColumns; n-- {
				row = append(row, "")
				align = append(align, "---")
			}
		}
		if len(row) == 0 {
			continue
		}
		if len(rows) == 0 {
			header = isHeader
			firstAlign = align
		}
		rows = append(rows, row)
		width = max(width, len(row))
	}
	var out []richLine
	if width > 0 {
		rule := make([]string, width)
		for i := range rule {
			rule[i] = "---"
			if i < len(firstAlign) {
				rule[i] = firstAlign[i]
			}
		}
		head := make([]string, width)
		if header {
			head, rows = richPad(rows[0], width), rows[1:]
		}
		out = append(out, richLine{Text: richRow(head)}, richLine{Text: richRow(rule)})
		for _, row := range rows {
			out = append(out, richLine{Text: richRow(richPad(row, width))})
		}
	}
	caption := richOneLine(c.inline(b["caption"], depth+1))
	if caption == "" {
		return out
	}
	if len(out) > 0 {
		out = append(out, richLine{})
	}
	return append(out, richLine{Text: caption})
}

// richAlign returns the alignment cell of the rule row.
func richAlign(a string) string {
	switch a {
	case "left":
		return ":---"
	case "center":
		return ":---:"
	case "right":
		return "---:"
	}
	return "---"
}

// richPad adds empty cells to a row up to the width.
func richPad(row []string, width int) []string {
	out := make([]string, width)
	copy(out, row)
	return out
}

// richRow joins the cells of one row.
func richRow(cells []string) string {
	return "| " + strings.Join(cells, " | ") + " |"
}
