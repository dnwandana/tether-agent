package security

import (
	"path"
	"slices"
	"strings"
)

// secondForm returns prepared in the shape that bash gives to a program.
// Bash removes the quotes and the backslash escapes and expands the globs
// after the gate has checked the text. The protected rules match this form
// too, so a quoted or a glob word cannot hide a protected path. A glob word
// that matches protected paths becomes those paths. The function reads no
// disk.
func secondForm(prepared string, paths []string) string {
	s := stripEscapes(prepared)
	var out strings.Builder
	start := -1
	flush := func(end int) {
		if start >= 0 {
			out.WriteString(expandWord(s[start:end], paths))
			start = -1
		}
	}
	for i := 0; i < len(s); i++ {
		if isWordBreak(s[i]) {
			flush(i)
			out.WriteByte(s[i])
		} else if start < 0 {
			start = i
		}
	}
	flush(len(s))
	return out.String()
}

// stripEscapes removes the quotes. It removes each backslash that comes
// before another character and keeps that character.
func stripEscapes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\'' || c == '"':
		case c == '\\' && i+1 < len(s):
			i++
			b.WriteByte(s[i])
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// isWordBreak reports whether bash ends a word at c. A backtick starts and
// ends a command substitution, so it also ends a word.
func isWordBreak(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\v', '\f', ';', '&', '|', '<', '>', '(', ')', '`':
		return true
	}
	return false
}

// expandWord replaces a glob word with all the protected paths that it
// matches, separated by spaces, like bash does. A glob word with no match
// becomes state.db when its last segment has a literal character and matches
// state.db. A bare "*" does not become state.db, because it is too common in
// other text, for example in SQL. Other words stay as they are.
func expandWord(word string, paths []string) string {
	if !strings.ContainsAny(word, "*?[") {
		return word
	}
	segs := strings.Split(word, "/")
	var found []string
	for _, form := range paths {
		dir := strings.HasSuffix(form, "/")
		fsegs := strings.Split(strings.TrimSuffix(form, "/"), "/")
		n := len(fsegs)
		if len(segs) < n || (!dir && len(segs) != n) {
			continue
		}
		ok := true
		for i, f := range fsegs {
			if !globMatch(segs[i], f) {
				ok = false
				break
			}
		}
		if ok {
			p := strings.Join(append(append([]string{}, fsegs...), segs[n:]...), "/")
			if !slices.Contains(found, p) {
				found = append(found, p)
			}
		}
	}
	if len(found) > 0 {
		return strings.Join(found, " ")
	}
	if last := segs[len(segs)-1]; hasLiteral(last) && globMatch(last, "state.db") {
		return "state.db"
	}
	return word
}

// hasLiteral reports whether the glob pattern has a character that is not
// part of "*", "?", or a bracket expression.
func hasLiteral(pattern string) bool {
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*', '?':
		case '[':
			if end := bracketEnd(pattern, i); end > 0 {
				i = end
				continue
			}
			return true
		default:
			return true
		}
	}
	return false
}

// bracketEnd returns the index of the "]" that closes the bracket expression
// at pattern[start], or -1 when the expression does not close. Like in bash,
// a "]" right after "[", "[!", or "[^" is a literal, and a "[:class:]" is
// one part of the expression.
func bracketEnd(pattern string, start int) int {
	i := start + 1
	if i < len(pattern) && (pattern[i] == '!' || pattern[i] == '^') {
		i++
	}
	if i < len(pattern) && pattern[i] == ']' {
		i++
	}
	for i < len(pattern) {
		if strings.HasPrefix(pattern[i:], "[:") {
			if end := strings.Index(pattern[i+2:], ":]"); end >= 0 {
				i += end + 4
				continue
			}
		}
		if pattern[i] == ']' {
			return i
		}
		i++
	}
	return -1
}

// posixClasses gives the path.Match ranges of the POSIX character classes.
// The command is in lower case, so the upper-case classes also give the
// lower-case letters.
var posixClasses = map[string]string{
	"alnum":  "a-zA-Z0-9",
	"alpha":  "a-zA-Z",
	"blank":  " \t",
	"digit":  "0-9",
	"lower":  "a-zA-Z",
	"upper":  "a-zA-Z",
	"space":  " \t\n\r\v\f",
	"xdigit": "0-9a-fA-F",
	"punct":  "!-/:-@\\[-`{-~",
	"graph":  "!-~",
	"print":  " -~",
	"word":   "a-zA-Z0-9_",
}

// toGoGlob changes the bash bracket forms that path.Match does not know:
// "[!" becomes "[^", and a POSIX class such as "[:lower:]" becomes its
// ranges. An unknown class matches no character.
func toGoGlob(pattern string) string {
	if !strings.Contains(pattern, "[") {
		return pattern
	}
	var b strings.Builder
	for i := 0; i < len(pattern); i++ {
		if pattern[i] != '[' {
			b.WriteByte(pattern[i])
			continue
		}
		end := bracketEnd(pattern, i)
		if end < 0 {
			b.WriteString(pattern[i:])
			break
		}
		b.WriteByte('[')
		j := i + 1
		if pattern[j] == '!' || pattern[j] == '^' {
			b.WriteByte('^')
			j++
		}
		first := j
		for j < end {
			if strings.HasPrefix(pattern[j:], "[:") {
				if k := strings.Index(pattern[j+2:], ":]"); k >= 0 && j+2+k < end {
					b.WriteString(posixClasses[pattern[j+2:j+2+k]])
					j += k + 4
					continue
				}
			}
			// path.Match needs an escape for these characters. A "-" at
			// the start or the end of the expression is a literal in bash.
			if c := pattern[j]; c == ']' || c == '\\' || c == '^' || (c == '-' && (j == first || j+1 == end)) {
				b.WriteByte('\\')
			}
			b.WriteByte(pattern[j])
			j++
		}
		b.WriteByte(']')
		i = end
	}
	return b.String()
}

// globMatch reports whether the pattern matches the name. A pattern that
// starts with a glob character does not match a name that starts with a dot,
// like in bash. A bad pattern does not match.
func globMatch(pattern, name string) bool {
	if pattern != "" && strings.IndexByte("*?[", pattern[0]) >= 0 && strings.HasPrefix(name, ".") {
		return false
	}
	ok, err := path.Match(toGoGlob(pattern), name)
	return err == nil && ok
}
