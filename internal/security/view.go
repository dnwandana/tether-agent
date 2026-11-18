package security

import (
	"slices"
	"strings"
)

// RuleLine returns the text that the rules read for one simple command: the Argv words and
// the Redirs, joined with one space. A data command gives only its program word and its
// redirects, because its arguments are text that does not run.
func (c SimpleCmd) RuleLine() string {
	var parts []string
	if c.Data && len(c.Argv) > 0 {
		parts = append(parts, c.Argv[0])
	} else {
		parts = append(parts, c.Argv...)
	}
	parts = append(parts, c.Redirs...)
	return strings.Join(parts, " ")
}

// span is a part of the raw text that the view removes: [start, end) in bytes.
type span struct{ start, end int }

// buildView returns text with the spans removed. The spans can come in any order and can
// overlap.
func buildView(text string, spans []span) string {
	if len(spans) == 0 {
		return text
	}
	sorted := slices.Clone(spans)
	slices.SortFunc(sorted, func(a, b span) int { return a.start - b.start })
	var b strings.Builder
	pos := 0
	for _, s := range sorted {
		if s.start < 0 || s.end > len(text) || s.start > s.end {
			continue
		}
		if s.start > pos {
			b.WriteString(text[pos:s.start])
		}
		pos = max(pos, s.end)
	}
	if pos < len(text) {
		b.WriteString(text[pos:])
	}
	return b.String()
}

// dataProgram reports whether name is echo, printf, cat, or tee.
func dataProgram(name string) bool {
	switch name {
	case "echo", "printf", "cat", "tee":
		return true
	}
	return false
}
