package security

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// maxSummaryRunes is the limit of the summary, with the final "…".
	maxSummaryRunes = 200
	// maxFallbackLineRunes is the limit of the command line in the fallback
	// summary, with the final "…".
	maxFallbackLineRunes = 60
)

// CleanSummary returns s as one line of safe text for the approval message.
// An empty result means that there is no summary.
func CleanSummary(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	s = ansiRe.ReplaceAllLiteralString(s, "")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case unicode.In(r, unicode.Cc, unicode.Cf):
			// A control or format rune can hide or reorder text, for
			// example U+202E. Drop it.
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	return cutRunes(s, maxSummaryRunes)
}

// ShowHidden returns s with each control rune and each format rune written as a visible
// mark, for example "<U+202E>". The approval message shows the command with it, because such
// a rune can hide or reorder the text that the user approves. "\n" and "\t" stay.
func ShowHidden(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r != '\n' && r != '\t' && unicode.In(r, unicode.Cc, unicode.Cf) {
			fmt.Fprintf(&b, "<U+%04X>", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// FallbackParts returns the parts of the fallback summary. Each renderer formats them.
func (r ApprovalRequest) FallbackParts() (pattern, line string, more int) {
	pattern = r.Description
	if pattern == "" {
		pattern = "dangerous command"
	}
	var lines []string
	for _, l := range strings.Split(r.Command, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return pattern, "", 0
	}
	// Show the line that the gate flagged. A pattern can match only across
	// lines, so the first line is the fallback.
	line = lines[0]
	for _, l := range lines {
		if _, ok := MatchDangerous(l); ok {
			line = l
			break
		}
	}
	return pattern, cutRunes(ShowHidden(line), maxFallbackLineRunes), len(lines) - 1
}

// cutRunes returns s with at most n runes. A cut keeps n-1 runes and adds "…".
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
