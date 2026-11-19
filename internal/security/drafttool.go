package security

import (
	"strings"
	"unicode"
)

// IsDraftTool reports whether an MCP tool name only writes a draft: its words hold "draft" or
// "drafts" and a draft verb, and no send verb.
func IsDraftTool(name string) bool {
	draft, verb := false, false
	for _, w := range splitToolWords(name) {
		switch w {
		case "send", "submit", "schedule", "dispatch", "publish", "post":
			return false
		case "draft", "drafts":
			draft = true
		case "create", "update", "save", "edit", "new", "add":
			verb = true
		}
	}
	return draft && verb
}

// splitToolWords splits a tool name into lower case words. A word ends at a character that is
// not a letter or a digit, at a change from lower case to upper case, and before the last upper
// case letter of an upper case run that a lower case letter follows.
func splitToolWords(name string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(name)
	for i, r := range rs {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if unicode.IsUpper(r) && len(cur) > 0 {
			prev := rs[i-1]
			nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return words
}
