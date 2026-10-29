package telegram

import (
	"strings"
	"unicode/utf8"
)

// This file reads the entities of formatted MarkdownV2 text, so that a split
// can cut the text where no entity is open. The input is the output of
// ToMarkdownV2, so each marker without an escape is an entity marker. The
// rules for code blocks and inline code come from Hermes truncate_message.
// The rules for links and emphasis are Tether rules. Hermes does not have
// them.

// mdv2State is the entity state of formatted MarkdownV2 text at one byte offset.
type mdv2State struct {
	Open int    // the open entities that are not a code block
	Pre  bool   // a code block is open, and its fence line is complete
	Lang string // the language of the open code block
}

// mdv2Mode tells which kind of text the scanner reads.
type mdv2Mode int

const (
	mdv2Text  mdv2Mode = iota // text outside code and outside a link URL
	mdv2Code                  // inline code
	mdv2Fence                 // the fence line of a code block
	mdv2Pre                   // the body of a code block
	mdv2URL                   // a link URL
)

// mdv2Quote tells which kind of quote is open.
type mdv2Quote int

const (
	mdv2NoQuote mdv2Quote = iota
	mdv2QuoteLine
	mdv2QuoteExpand
)

// scanMDV2 calls fn for each cut offset i of s, from 0 to len(s), with the state of
// s[:i]. A cut offset is a rune boundary that is not between an escape backslash
// and its character. The scan stops when fn returns false.
//
// An offset inside a marker of more than one byte counts as open. A plain
// quote line is open until its ending "\n", and the state at that "\n" is
// closed. An expandable quote is open until after its "||" mark.
func scanMDV2(s string, fn func(i int, st mdv2State) bool) {
	var (
		stack     []string
		mode      = mdv2Text
		quote     = mdv2NoQuote
		lang      string
		langStart int
		lineStart = true
	)
	state := func(extra int) mdv2State {
		st := mdv2State{Open: len(stack) + extra}
		switch mode {
		case mdv2Code, mdv2Fence, mdv2URL:
			st.Open++
		case mdv2Pre:
			st.Pre, st.Lang = true, lang
		}
		if quote != mdv2NoQuote {
			st.Open++
		}
		return st
	}
	// inner calls fn for the offsets inside the marker of n bytes at i.
	inner := func(i, n int) bool {
		for j := 1; j < n; j++ {
			if !fn(i+j, state(1)) {
				return false
			}
		}
		return true
	}
	toggle := func(m string) {
		if n := len(stack); n > 0 && stack[n-1] == m {
			stack = stack[:n-1]
			return
		}
		stack = append(stack, m)
	}
	top := func() string {
		if len(stack) == 0 {
			return ""
		}
		return stack[len(stack)-1]
	}
	// quoteEnds reports whether a quote ends at byte j. tdlib continues a
	// quote only when a newline has ">" after it.
	quoteEnds := func(j int) bool {
		return j >= len(s) || (s[j] == '\n' && (j+1 == len(s) || s[j+1] != '>'))
	}

	for i := 0; i < len(s); {
		if mode == mdv2Text && s[i] == '\n' {
			// A plain quote line ends at its newline. An expandable quote
			// ends where the quote does not continue.
			if quote == mdv2QuoteLine || (quote == mdv2QuoteExpand && quoteEnds(i)) {
				quote = mdv2NoQuote
			}
		}
		if !fn(i, state(0)) {
			return
		}
		switch mode {
		case mdv2Code:
			switch s[i] {
			case '\\':
				i = skipEscape(s, i)
			case '`':
				mode = mdv2Text
				i++
			default:
				i += runeSize(s, i)
			}
			continue
		case mdv2Fence:
			switch {
			case s[i] == '\n':
				lang = s[langStart:i]
				mode = mdv2Pre
				i++
			case strings.HasPrefix(s[i:], "```"):
				// A code block with no newline and no body.
				if !inner(i, 3) {
					return
				}
				mode = mdv2Text
				i += 3
			default:
				i += runeSize(s, i)
			}
			continue
		case mdv2Pre:
			switch {
			case s[i] == '\\':
				i = skipEscape(s, i)
			case strings.HasPrefix(s[i:], "```"):
				if !inner(i, 3) {
					return
				}
				mode, lang = mdv2Text, ""
				i += 3
			default:
				i += runeSize(s, i)
			}
			continue
		case mdv2URL:
			switch {
			case s[i] == '\\' && i+1 < len(s) && (s[i+1] == ')' || s[i+1] == '\\'):
				i += 2
			case s[i] == ')':
				mode = mdv2Text
				i++
			default:
				i += runeSize(s, i)
			}
			continue
		}

		// Text outside code and outside a link URL.
		if lineStart {
			lineStart = false
			j := i
			if strings.HasPrefix(s[i:], "**>") {
				j += 2
			}
			for j < len(s) && s[j] == '>' {
				j++
			}
			if j > i && s[j-1] == '>' {
				if !inner(i, j-i) {
					return
				}
				if s[i] == '*' {
					quote = mdv2QuoteExpand
				} else if quote == mdv2NoQuote {
					quote = mdv2QuoteLine
				}
				i = j
				continue
			}
		}
		c := s[i]
		switch {
		case c == '\n':
			lineStart = true
			i++
		case c == '\\':
			i = skipEscape(s, i)
		case strings.HasPrefix(s[i:], "```"):
			if !inner(i, 3) {
				return
			}
			mode, langStart = mdv2Fence, i+3
			i += 3
		case c == '`':
			mode = mdv2Code
			i++
		case c == '[':
			stack = append(stack, "[")
			i++
		case c == ']' && top() == "[" && strings.HasPrefix(s[i:], "]("):
			if !inner(i, 2) {
				return
			}
			stack = stack[:len(stack)-1]
			mode = mdv2URL
			i += 2
		case strings.HasPrefix(s[i:], "||"):
			if !inner(i, 2) {
				return
			}
			if quote != mdv2NoQuote && quoteEnds(i+2) && top() != "||" {
				// The mark of an expandable quote ends the quote.
				quote = mdv2NoQuote
			} else {
				toggle("||")
			}
			i += 2
		case strings.HasPrefix(s[i:], "__"):
			if !inner(i, 2) {
				return
			}
			toggle("__")
			i += 2
		case c == '*' || c == '_' || c == '~':
			toggle(string(c))
			i++
		default:
			i += runeSize(s, i)
		}
	}
	if mode == mdv2Text {
		// A quote ends at the end of the text.
		quote = mdv2NoQuote
	}
	fn(len(s), state(0))
}

// skipEscape returns the index after the escape backslash at s[i] and the
// rune that it escapes.
func skipEscape(s string, i int) int {
	if i+1 >= len(s) {
		return i + 1
	}
	return i + 1 + runeSize(s, i+1)
}

// runeSize returns the byte length of the rune at s[i].
func runeSize(s string, i int) int {
	_, n := utf8.DecodeRuneInString(s[i:])
	return n
}
