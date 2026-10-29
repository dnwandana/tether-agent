package telegram

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// This file ports Hermes gateway/platforms/telegram.py format_message,
// _escape_mdv2, _strip_mdv2, and _wrap_markdown_tables, and the UTF-16 split
// rules of gateway/platforms/base.py truncate_message.

// mdv2Special holds the characters that MarkdownV2 escapes outside code.
const mdv2Special = "_*[]()~`>#+-=|{}.!\\"

// mdEscapes holds the characters that a backslash makes literal in common
// markdown. It is the set that gateway.EscapeMarkdown escapes in user text.
const mdEscapes = "\\*_`[]|~"

var (
	reFenced  = regexp.MustCompile("```(?:[^\\n]*\\n)?[\\s\\S]*?```")
	reLink    = regexp.MustCompile(`\[([^\]]+)\]\(([^()\x00]*(?:\([^()\x00]*\)[^()\x00]*)*)\)`)
	reHeader  = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
	reBold    = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalic  = regexp.MustCompile(`^\*([^*\n]+)\*`)
	reStrike  = regexp.MustCompile(`~~(.+?)~~`)
	reSpoiler = regexp.MustCompile(`\|\|(.+?)\|\|`)
	reQuote   = regexp.MustCompile(`(?m)^((?:\*\*)?>{1,3}) (.+)$`)
	// reQuoteStart matches the start of a line that reQuote converts.
	reQuoteStart = regexp.MustCompile(`^(?:\*\*)?>{1,3} .`)
	// reQuoteBare matches a ">" line with no text.
	reQuoteBare = regexp.MustCompile(`^>{1,3}$`)
	// reQuoteNext matches the start of a line that continues a quote. tdlib
	// continues a quote only when ">" comes after the newline.
	reQuoteNext = regexp.MustCompile(`^\n>{1,3} .`)
	reCodeSplit = regexp.MustCompile("```[\\s\\S]*?```|`[^`]+`")
	rePlacehold = regexp.MustCompile("\x00PH([0-9]+)\x00")
	reTableSep  = regexp.MustCompile(`^\s*\|?\s*:?-+:?\s*(?:\|\s*:?-+:?\s*){1,}\|?\s*$`)

	reUnescape    = regexp.MustCompile(`\\([_*\[\]()~` + "`" + `>#\+\-=|{}.!\\])`)
	reStripBold   = regexp.MustCompile(`\*([^*]+)\*`)
	reStripStrike = regexp.MustCompile(`~([^~]+)~`)
	reStripSpoil  = regexp.MustCompile(`\|\|([^|]+)\|\|`)
)

// escapeMDV2 puts a backslash before each MarkdownV2 special character.
func escapeMDV2(s string) string {
	var b strings.Builder
	b.Grow(len(s) + len(s)/8)
	for _, r := range s {
		if r < utf8.RuneSelf && strings.IndexByte(mdv2Special, byte(r)) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// replaceGroups replaces each match of re in s with the result of fn. fn gets
// the full match in g[0] and the capture groups after it.
func replaceGroups(re *regexp.Regexp, s string, fn func(g []string) string) string {
	return replaceGroupsAt(re, s, func(_ []int, g []string) string { return fn(g) })
}

// replaceGroupsAt is replaceGroups, and fn also gets the byte offsets of the
// match in s, as FindAllStringSubmatchIndex gives them.
func replaceGroupsAt(re *regexp.Regexp, s string, fn func(m []int, g []string) string) string {
	idx := re.FindAllStringSubmatchIndex(s, -1)
	if idx == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range idx {
		g := make([]string, len(m)/2)
		for i := range g {
			if m[2*i] >= 0 {
				g[i] = s[m[2*i]:m[2*i+1]]
			}
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(fn(m, g))
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// ToMarkdownV2 converts common markdown to Telegram MarkdownV2. It first
// moves code, links, and emphasis behind placeholders, then escapes the rest
// of the text, then puts the placeholders back.
func ToMarkdownV2(md string) string {
	if md == "" {
		return md
	}
	// The placeholders use NUL bytes, so the input must not contain them.
	text := strings.ReplaceAll(md, "\x00", "")
	var stash []string
	ph := func(v string) string {
		stash = append(stash, v)
		return "\x00PH" + strconv.Itoa(len(stash)-1) + "\x00"
	}

	// 0) Rewrite pipe tables as row groups.
	text = wrapMarkdownTables(text)

	// 1) Fenced code. Inside pre and code, MarkdownV2 escapes only \ and `.
	text = replaceGroups(reFenced, text, func(g []string) string {
		raw := g[0]
		lang, body := "", raw[3:len(raw)-3]
		if nl := strings.IndexByte(raw[3:], '\n'); nl >= 0 && 3+nl < len(raw)-3 {
			info := strings.TrimSpace(raw[3 : 3+nl])
			body = raw[3+nl+1 : len(raw)-3]
			if isFenceLang(info) {
				lang = info
			} else if info != "" {
				body = raw[3:3+nl] + "\n" + body
			}
		}
		body = strings.ReplaceAll(body, `\`, `\\`)
		body = strings.ReplaceAll(body, "`", "\\`")
		// Telegram reads the text after ``` up to a space as the language,
		// and it drops one newline after it. The newline keeps the body out
		// of the language.
		return ph("```" + lang + "\n" + body + "```")
	})

	// 2) Inline code and backslash escapes.
	text = replaceCodeAndEscapes(text, ph)

	// 3) Links. Inside the URL, only ) and \ need an escape. The URL regex
	// excludes NUL, because a URL is plain text and cannot hold code.
	text = replaceGroupsAt(reLink, text, func(m []int, g []string) string {
		url := strings.ReplaceAll(g[2], `\`, `\\`)
		url = strings.ReplaceAll(url, ")", `\)`)
		display := g[1]
		if strings.Contains(display, "\n") && reQuoteStart.MatchString(text[lineStart(text, m[0]):]) {
			// tdlib ends a quote at a newline without ">" after it, and an
			// open link at that newline is an error. Keep the link on one line.
			display = strings.ReplaceAll(display, "\n", " ")
		}
		return ph("[" + escapeMDV2(display) + "](" + url + ")")
	})

	// 4) Headings become bold. A bold marker inside a heading is removed.
	text = replaceGroups(reHeader, text, func(g []string) string {
		inner := reBold.ReplaceAllString(strings.TrimSpace(g[1]), "$1")
		return ph("*" + escapeMDV2(inner) + "*")
	})

	// 5) to 8) Bold, italic (one line only, so * bullet lists stay), strike,
	// and spoiler.
	text = replaceGroups(reBold, text, func(g []string) string { return ph("*" + escapeMDV2(g[1]) + "*") })
	text = replaceItalic(text, ph)
	text = replaceGroups(reStrike, text, func(g []string) string { return ph("~" + escapeMDV2(g[1]) + "~") })
	text = replaceGroups(reSpoiler, text, func(g []string) string { return ph("||" + escapeMDV2(g[1]) + "||") })

	// 9) Block quotes. A bare ">" line after a quote line keeps the quote open over
	// an empty line. It gets an empty placeholder, so reQuote converts it too.
	text = quoteBareLines(text, ph)
	// "**>" starts an expandable quote and a final "||"
	// ends it. tdlib reads the "||" as the mark only where the quote ends,
	// so the mark cannot come before a line that continues the quote.
	text = replaceGroupsAt(reQuote, text, func(m []int, g []string) string {
		prefix, content := g[1], g[2]
		if strings.HasPrefix(prefix, "**") && strings.HasSuffix(content, "||") &&
			!reQuoteNext.MatchString(text[m[1]:]) {
			return ph(prefix + " " + escapeMDV2(content[:len(content)-2]) + "||")
		}
		return ph(prefix + " " + escapeMDV2(content))
	})

	// 10) Escape the remaining text.
	text = escapeMDV2(text)

	// 11) Put the placeholders back. A value can hold earlier placeholders.
	var expand func(string) string
	expand = func(s string) string {
		return rePlacehold.ReplaceAllStringFunc(s, func(m string) string {
			n, _ := strconv.Atoi(m[3 : len(m)-1])
			return expand(stash[n])
		})
	}
	text = expand(text)

	// 12) Safety net: escape a bare ( ) { } outside code.
	return escapeBareBrackets(text)
}

// quoteBareLines adds an empty placeholder after each bare ">" line that comes after
// a quote line. tdlib ends a quote at a newline without ">" after it, so an
// escaped ">" would split the quote in two.
func quoteBareLines(text string, ph func(string) string) string {
	lines := strings.Split(text, "\n")
	for i := 1; i < len(lines); i++ {
		if reQuoteBare.MatchString(lines[i]) && reQuoteStart.MatchString(lines[i-1]) {
			lines[i] += " " + ph("")
		}
	}
	return strings.Join(lines, "\n")
}

// lineStart returns the byte index of the start of the line that holds s[i].
func lineStart(s string, i int) int {
	return strings.LastIndexByte(s[:i], '\n') + 1
}

// replaceCodeAndEscapes moves inline code spans and backslash escapes behind
// placeholders, in one scan from left to right. A backslash escape outside
// code gives the literal character, so "\_" cannot start italic and "\`"
// cannot start code. Inside code, a backslash is literal. A code span cannot
// hold NUL, so it cannot hold the placeholder of a fenced block.
func replaceCodeAndEscapes(text string, ph func(string) string) string {
	if !strings.ContainsAny(text, "`\\") {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); {
		c := text[i]
		if c == '\\' && i+1 < len(text) && strings.IndexByte(mdEscapes, text[i+1]) >= 0 {
			// Each character in mdEscapes is special in MarkdownV2.
			b.WriteString(ph(text[i : i+2]))
			i += 2
			continue
		}
		if c == '`' {
			j := strings.IndexByte(text[i+1:], '`')
			if j > 0 && strings.IndexByte(text[i+1:i+1+j], 0) < 0 {
				b.WriteString(ph(strings.ReplaceAll(text[i:i+j+2], `\`, `\\`)))
				i += j + 2
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// replaceItalic converts *x* and _x_ to MarkdownV2 italic. The text of *x* is
// on one line. A "_" opens italic at the start of a word and closes it at the
// end of a word, so a "_" in snake_case stays literal. Telegram reads "__" as
// underline, so an empty bold entity separates two italic entities that touch
// (Bot API, MarkdownV2 style).
func replaceItalic(text string, ph func(string) string) string {
	if !strings.ContainsAny(text, "*_") {
		return text
	}
	var b strings.Builder
	last, prevEnd := 0, -1
	for i := 0; i < len(text); {
		end, inner := -1, ""
		switch text[i] {
		case '*':
			if m := reItalic.FindStringSubmatchIndex(text[i:]); m != nil {
				end, inner = i+m[1], text[i+m[2]:i+m[3]]
			}
		case '_':
			if j := closeUnderscore(text, i); j > 0 {
				end, inner = j+1, text[i+1:j]
			}
		}
		if end < 0 {
			i++
			continue
		}
		b.WriteString(text[last:i])
		v := "_" + escapeMDV2(inner) + "_"
		if prevEnd == i {
			v = "**" + v
		}
		b.WriteString(ph(v))
		last, prevEnd, i = end, end, end
	}
	b.WriteString(text[last:])
	return b.String()
}

// closeUnderscore returns the index of the "_" that closes the italic span
// that the "_" at s[i] opens, or -1. The rules are the rules of
// gateway.StripMarkdown, on one line, and a "__" is never a marker:
//   - The open "_" has no word character before it, and a character that is
//     not a space and not "_" after it.
//   - The close "_" has a character that is not a space and not "_" before
//     it, and no word character after it.
func closeUnderscore(s string, i int) int {
	if wordBefore(s, i) || i+1 >= len(s) || s[i+1] == '_' {
		return -1
	}
	if r, _ := utf8.DecodeRuneInString(s[i+1:]); unicode.IsSpace(r) {
		return -1
	}
	for j := i + 2; j < len(s); j++ {
		switch s[j] {
		case '\n':
			return -1
		case '_':
			if s[j-1] == '_' || wordAfter(s, j+1) {
				continue
			}
			if r, _ := utf8.DecodeLastRuneInString(s[:j]); unicode.IsSpace(r) {
				continue
			}
			return j
		}
	}
	return -1
}

// isFenceLang reports whether info can be the language of a pre entity.
// Telegram ends the language at a space or a "`", and it does not read
// escapes in the language, so a "\" is not safe.
func isFenceLang(info string) bool {
	return info != "" && !strings.ContainsAny(info, "`\\ \t\r\n\v\f")
}

// escapeBareBrackets escapes ( ) { } that have no escape, outside code spans
// and fences. It keeps the ( and ) of a link URL.
func escapeBareBrackets(text string) string {
	idx := reCodeSplit.FindAllStringIndex(text, -1)
	var b strings.Builder
	last := 0
	for _, m := range idx {
		b.WriteString(escapeBareSegment(text[last:m[0]]))
		b.WriteString(text[m[0]:m[1]])
		last = m[1]
	}
	b.WriteString(escapeBareSegment(text[last:]))
	return b.String()
}

func escapeBareSegment(seg string) string {
	if !strings.ContainsAny(seg, "(){}") {
		return seg
	}
	var b strings.Builder
	for i := 0; i < len(seg); i++ {
		ch := seg[i]
		if (ch == '(' || ch == ')' || ch == '{' || ch == '}') && !keepBracket(seg, i) {
			b.WriteByte('\\')
		}
		b.WriteByte(ch)
	}
	return b.String()
}

// keepBracket reports whether the bracket at seg[s] needs no extra escape.
func keepBracket(seg string, s int) bool {
	// An odd run of backslashes before the bracket escapes it.
	n := 0
	for j := s - 1; j >= 0 && seg[j] == '\\'; j-- {
		n++
	}
	if n%2 == 1 {
		return true
	}
	ch := seg[s]
	if ch == '(' && s > 0 && seg[s-1] == ']' {
		return true // opens a link URL
	}
	if ch == ')' && strings.Contains(seg[:s], "](") {
		// Close of a link URL: find the open bracket that is not matched.
		depth := 0
		// The scan stops at the first ( that is not matched, so it reads
		// only the URL. The scan has no fixed look-back limit, because such
		// a limit escapes the close of a long URL.
		for j := s - 1; j >= 0; j-- {
			switch seg[j] {
			case '(':
				depth--
				if depth < 0 {
					return j > 0 && seg[j-1] == ']'
				}
			case ')':
				depth++
			}
		}
	}
	return false
}

// StripMarkdownV2 removes the MarkdownV2 escapes and the emphasis markers. The
// result is the plain-text fallback when Telegram rejects the entities.
func StripMarkdownV2(s string) string {
	s = reUnescape.ReplaceAllString(s, "$1")
	s = reStripBold.ReplaceAllString(s, "$1")
	s = stripItalic(s)
	s = reStripStrike.ReplaceAllString(s, "$1")
	return reStripSpoil.ReplaceAllString(s, "$1")
}

// stripItalic removes _x_ markers that are not part of a word, so snake_case
// stays. It is the Go form of the Hermes regex (?<!\w)_([^_]+)_(?!\w).
func stripItalic(s string) string {
	if !strings.Contains(s, "_") {
		return s
	}
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '_' && !wordBefore(s, i) {
			j := strings.IndexByte(s[i+1:], '_')
			if j > 0 && !wordAfter(s, i+1+j+1) {
				b.WriteString(s[i+1 : i+1+j])
				i = i + 1 + j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsNumber(r)
}

func wordBefore(s string, i int) bool {
	if i == 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return isWordRune(r)
}

func wordAfter(s string, i int) bool {
	if i >= len(s) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return isWordRune(r)
}

// UTF16Len returns the length of s in UTF-16 code units. Telegram counts the
// message limit in these units.
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		n += runeUnits(r)
	}
	return n
}

func runeUnits(r rune) int {
	if n := utf16.RuneLen(r); n > 0 {
		return n
	}
	return 1
}

// SplitUTF16 splits s into parts of at most limit UTF-16 code units. It splits
// before the last newline in the second half of the window, else it does a
// hard split. It never cuts a rune or a surrogate pair, and it does not end a
// part with an escape backslash. The joined parts equal s.
func SplitUTF16(s string, limit int) []string {
	if limit <= 0 || UTF16Len(s) <= limit {
		return []string{s}
	}
	var parts []string
	for UTF16Len(s) > limit {
		cut := splitPoint(s, limit)
		parts = append(parts, s[:cut])
		s = s[cut:]
	}
	if s != "" {
		parts = append(parts, s)
	}
	return parts
}

// splitPoint returns the byte index where the next part of s ends.
func splitPoint(s string, limit int) int {
	units, hard, newline := 0, 0, -1
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		n := runeUnits(r)
		if units+n > limit {
			break
		}
		if r == '\n' && units >= limit/2 && i > 0 {
			newline = i
		}
		units += n
		i += size
		hard = i
	}
	if newline > 0 {
		return newline
	}
	if hard == 0 {
		// One rune is wider than the limit. Send it alone.
		_, size := utf8.DecodeRuneInString(s)
		return size
	}
	// Keep an escape backslash with the character that it escapes.
	if hard < len(s) {
		n := 0
		for j := hard - 1; j >= 0 && s[j] == '\\'; j-- {
			n++
		}
		if n%2 == 1 && hard > 1 {
			hard--
		}
	}
	return hard
}

// NumberParts adds a " (i/n)" suffix to each part when there are two or more
// parts. With escaped set, the brackets get MarkdownV2 escapes, and a part
// that ends with the mark of an expandable quote gets the suffix on its own
// line. Text after that mark on the same line makes tdlib read the mark as
// a spoiler that does not close.
func NumberParts(parts []string, escaped bool) []string {
	out := make([]string, len(parts))
	copy(out, parts)
	if len(parts) < 2 {
		return out
	}
	format := " (%d/%d)"
	if escaped {
		format = ` \(%d/%d\)`
	}
	for i := range out {
		f := format
		if escaped && endsWithQuoteMark(out[i]) {
			f = "\n" + strings.TrimPrefix(format, " ")
		}
		out[i] += fmt.Sprintf(f, i+1, len(parts))
	}
	return out
}

// endsWithQuoteMark reports whether the formatted MarkdownV2 part ends with
// the "||" mark of an expandable quote: the "||" has no escape, and the last
// line is in a quote whose first line starts with "**>".
func endsWithQuoteMark(part string) bool {
	if !strings.HasSuffix(part, "||") {
		return false
	}
	n := 0
	for j := len(part) - 3; j >= 0 && part[j] == '\\'; j-- {
		n++
	}
	if n%2 == 1 {
		return false
	}
	for rest := part; ; {
		i := strings.LastIndexByte(rest, '\n')
		line := rest[i+1:]
		if strings.HasPrefix(line, "**>") {
			return true
		}
		if i < 0 || !strings.HasPrefix(line, ">") {
			return false
		}
		rest = rest[:i]
	}
}

// cutMarkdownV2 returns formatted MarkdownV2 text cut to one message of at
// most limit UTF-16 units. The cut keeps each entity whole and closes an
// open code block.
func cutMarkdownV2(s string, limit int) string {
	return SplitMarkdownV2(s, limit)[0]
}

// wrapMarkdownTables rewrites pipe tables as a bold heading and a bullet list
// for each row, because MarkdownV2 has no table syntax. It does not change a
// table inside a fenced code block.
func wrapMarkdownTables(text string) string {
	if !strings.Contains(text, "|") || !strings.Contains(text, "-") {
		return text
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	for i := 0; i < len(lines); {
		line := lines[i]
		if strings.HasPrefix(strings.TrimLeftFunc(line, unicode.IsSpace), "```") {
			inFence = !inFence
			out = append(out, line)
			i++
			continue
		}
		if inFence {
			out = append(out, line)
			i++
			continue
		}
		if strings.Contains(line, "|") && i+1 < len(lines) && reTableSep.MatchString(lines[i+1]) {
			block := []string{line, lines[i+1]}
			j := i + 2
			for j < len(lines) && isTableRow(lines[j]) {
				block = append(block, lines[j])
				j++
			}
			out = append(out, renderTable(block))
			i = j
			continue
		}
		out = append(out, line)
		i++
	}
	return strings.Join(out, "\n")
}

func isTableRow(line string) bool {
	s := strings.TrimSpace(line)
	return s != "" && strings.Contains(s, "|")
}

// splitTableRow returns the cells of a table row. A "|" after a backslash
// escape is part of the cell, as in GitHub Flavored Markdown.
func splitTableRow(line string) []string {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "|")
	var cells []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '|':
			cells = append(cells, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	if rest := strings.TrimSpace(s[start:]); rest != "" || len(cells) == 0 {
		cells = append(cells, rest)
	}
	return cells
}

func renderTable(block []string) string {
	if len(block) < 3 {
		return strings.Join(block, "\n")
	}
	headers := splitTableRow(block[0])
	if len(headers) < 2 {
		return strings.Join(block, "\n")
	}
	// A row-label column has no header: the data rows have one more cell.
	hasLabel := len(splitTableRow(block[2])) == len(headers)+1
	groups := make([]string, 0, len(block)-2)
	for idx, row := range block[2:] {
		cells := splitTableRow(row)
		var heading string
		var data []string
		if hasLabel {
			if len(cells) > 0 && cells[0] != "" {
				heading = cells[0]
			}
			if len(cells) > 0 {
				data = cells[1:]
			}
		} else {
			for _, c := range cells {
				if c != "" {
					heading = c
					break
				}
			}
			data = cells
		}
		if heading == "" {
			heading = fmt.Sprintf("Row %d", idx+1)
		}
		for len(data) < len(headers) {
			data = append(data, "")
		}
		data = data[:len(headers)]
		lines := []string{"**" + heading + "**"}
		for k, h := range headers {
			// Without a label column, the heading is the first cell. Do not
			// show it again as a bullet.
			if !hasLabel && data[k] == heading {
				continue
			}
			lines = append(lines, "• "+h+": "+data[k])
		}
		groups = append(groups, strings.Join(lines, "\n"))
	}
	return strings.Join(groups, "\n\n")
}
