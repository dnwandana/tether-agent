// Package textmatch holds the word rules that memory, skills, and the quote check share.
package textmatch

import (
	"strings"
	"unicode"
)

var stopWords = map[string]bool{}

func init() {
	for w := range strings.FieldsSeq("with that from this when what have will your there their they them then than were been into also only some more most such each other about") {
		stopWords[w] = true
	}
}

func isSep(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }

// Words returns the words of s that have 4 or more runes, in lower case, with no stop words.
func Words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), isSep) {
		if len([]rune(w)) >= 4 && !stopWords[w] {
			out[w] = true
		}
	}
	return out
}

// Similar counts the words that are in both sets. It reports ok when the count is 3 or more
// and is at least 30% of the words of the smaller set.
func Similar(a, b map[string]bool) (shared int, ok bool) {
	for w := range a {
		if b[w] {
			shared++
		}
	}
	return shared, shared >= 3 && shared*100 >= 30*min(len(a), len(b))
}

// Normalize returns s in lower case. Each run of runes that are not letters and not digits
// becomes one space, and the two ends are trimmed.
func Normalize(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), isSep), " ")
}

// ContainsOnWords reports whether short is inside long on word boundaries. Both texts must be
// normalized. An empty short never matches.
func ContainsOnWords(long, short string) bool {
	if short == "" {
		return false
	}
	return strings.Contains(" "+long+" ", " "+short+" ")
}
