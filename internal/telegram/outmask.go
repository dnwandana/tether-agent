package telegram

import (
	"strings"

	"github.com/dnwandana/tether-agent/internal/security"
)

// holdCut returns the byte offset in rest where the held tail starts. The text before it
// cannot grow into a pattern match, a known value, or a PEM block.
func holdCut(rest string, partial func(string) int) int {
	i := len(rest)
	for i > 0 {
		c := rest[i-1]
		if c == ' ' && i >= 2 && rest[i-2] == ' ' {
			break
		}
		if !holdChar(c) {
			break
		}
		i--
	}
	cut := max(i, len(rest)-64)
	if partial != nil {
		cut = min(cut, len(rest)-partial(rest))
	}
	if b := strings.LastIndex(rest, "-----BEGIN"); b >= 0 && !strings.Contains(rest[b:], "\n-----END") {
		cut = min(cut, b)
	}
	return max(cut, 0)
}

// holdChar reports whether c can be part of a pattern that is not complete yet.
func holdChar(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' ||
		strings.IndexByte("+-._/= ", c) >= 0
}

// rawOffset changes n, a byte offset in the masked form of a text, into the offset in the raw
// text. f are the findings of the mask, in order. An offset inside a label moves to the end of
// its finding.
func rawOffset(f []security.Finding, n int) int {
	raw, masked := 0, 0 // the position in the raw text and in the masked text
	for _, x := range f {
		gap := x.Start - raw
		if n <= masked+gap {
			return raw + (n - masked)
		}
		masked += gap
		if n < masked+len(x.Label) {
			return x.End
		}
		masked += len(x.Label)
		raw = x.End
	}
	return raw + (n - masked)
}

// labelSpans returns the start and the end of each label in the masked form of a text. f are
// the findings of the mask, in order.
func labelSpans(f []security.Finding) [][2]int {
	spans := make([][2]int, 0, len(f))
	raw, masked := 0, 0 // the position in the raw text and in the masked text
	for _, x := range f {
		masked += x.Start - raw
		spans = append(spans, [2]int{masked, masked + len(x.Label)})
		masked += len(x.Label)
		raw = x.End
	}
	return spans
}
