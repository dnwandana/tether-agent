package gateway

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// markdownEscapes holds the characters that EscapeMarkdown escapes. A backslash before one
// of them makes it a plain character.
const markdownEscapes = "\\*_`[]|~"

// EscapeMarkdown puts a backslash before each Markdown marker character. User text, for
// example a session title, then shows literally in a reply.
func EscapeMarkdown(s string) string {
	if !strings.ContainsAny(s, markdownEscapes) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(markdownEscapes, s[i]) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// StripMarkdown removes the Markdown markers from a reply for a text-only channel such as
// the CLI. "**x**", "__x__", "_x_", and "`x`" become "x". A "> " quote prefix stays. A "_"
// inside a word, for example in snake_case, is not a marker. A backslash escape gives the
// plain character.
func StripMarkdown(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	stripTo(&b, s)
	return b.String()
}

// stripTo writes s without its Markdown markers to b.
func stripTo(b *strings.Builder, s string) {
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s) && strings.IndexByte(markdownEscapes, s[i+1]) >= 0:
			b.WriteByte(s[i+1])
			i += 2
		case c == '`':
			// The text of a code span is literal.
			if j := strings.IndexByte(s[i+1:], '`'); j > 0 {
				b.WriteString(s[i+1 : i+1+j])
				i += j + 2
				continue
			}
			b.WriteByte(c)
			i++
		case (c == '*' || c == '_') && strings.HasPrefix(s[i:], string([]byte{c, c})):
			delim := s[i : i+2]
			if j := closeMarker(s, i+2, delim); j > i+2 {
				stripTo(b, s[i+2:j])
				i = j + 2
				continue
			}
			b.WriteString(delim)
			i += 2
		case c == '_' && opensItalic(s, i):
			if j := closeItalic(s, i+1); j > 0 {
				stripTo(b, s[i+1:j])
				i = j + 1
				continue
			}
			b.WriteByte(c)
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
}

// closeMarker returns the index of the first delim in s at or after from, or -1. It skips
// backslash escapes and code spans.
func closeMarker(s string, from int, delim string) int {
	for i := from; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s) && strings.IndexByte(markdownEscapes, s[i+1]) >= 0:
			i++
		case s[i] == '`':
			if j := strings.IndexByte(s[i+1:], '`'); j > 0 {
				i += j + 1
			}
		case strings.HasPrefix(s[i:], delim):
			return i
		}
	}
	return -1
}

// opensItalic reports whether the "_" at s[i] can start an italic span. The "_" must be at
// the start of a word, and a non-space character must come after it.
func opensItalic(s string, i int) bool {
	if i > 0 {
		if r, _ := utf8.DecodeLastRuneInString(s[:i]); isWordRune(r) {
			return false
		}
	}
	if i+1 >= len(s) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[i+1:])
	return !unicode.IsSpace(r)
}

// closeItalic returns the index of the "_" that ends the italic span that starts before
// from, or -1. The "_" must be at the end of a word, after a non-space character.
func closeItalic(s string, from int) int {
	for i := from; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s) && strings.IndexByte(markdownEscapes, s[i+1]) >= 0:
			i++
		case s[i] == '`':
			if j := strings.IndexByte(s[i+1:], '`'); j > 0 {
				i += j + 1
			}
		case s[i] == '_' && i > from:
			if r, _ := utf8.DecodeLastRuneInString(s[:i]); unicode.IsSpace(r) {
				continue
			}
			if i+1 < len(s) {
				if r, _ := utf8.DecodeRuneInString(s[i+1:]); isWordRune(r) {
					continue
				}
			}
			return i
		}
	}
	return -1
}

// isWordRune reports whether r is part of a word: a letter, a digit, or "_".
func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
