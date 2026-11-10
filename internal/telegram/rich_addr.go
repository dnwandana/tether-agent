package telegram

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// richEntity is a mention or a command in the addressing text of a rich
// message. Offset and Length count UTF-16 code units, as in MessageEntity.
// Text is the part of the addressing text that the entity marks.
type richEntity struct {
	Type           string
	Offset, Length int
	Text           string
	User           *User
}

// richAddrMentionRe and richAddrCommandRe find what Telegram marks as a
// mention and as a bot command in a plain text (Hermes _RICH_MENTION_RE and
// _RICH_COMMAND_RE). Go has no lookbehind and no lookahead, so
// richAddrMentionAt and richAddrCommandAt check the characters around each
// match.
var (
	richAddrMentionRe = regexp.MustCompile(`@[A-Za-z0-9_]{2,32}`)
	richAddrCommandRe = regexp.MustCompile(`/[A-Za-z0-9_]{1,64}(?:@[A-Za-z0-9_]{2,32})?`)
)

// richAddrRange is an entity with byte offsets.
type richAddrRange struct {
	typ        string
	start, end int
	user       *User
}

// richAddrWord reports if c is one of [A-Za-z0-9_].
func richAddrWord(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// richAddrMentionAt reports if the match text[start:end] is a mention. The
// character before it must not make an e-mail address, code, or a URL path,
// and a word boundary must follow.
func richAddrMentionAt(text string, start, end int) bool {
	if start > 0 && noMentionBefore(text[start-1]) {
		return false
	}
	return end == len(text) || !richAddrWord(text[end])
}

// richAddrCommandAt reports if the match text[start:end] is a bot command.
// White space or the start of the text must come before it.
func richAddrCommandAt(text string, start, end int) bool {
	if start > 0 {
		if r, _ := utf8.DecodeLastRuneInString(text[:start]); !unicode.IsSpace(r) {
			return false
		}
	}
	if end == len(text) {
		return true
	}
	c := text[end]
	return !richAddrWord(c) && c != '@' && c != '/' && c != '\\'
}

// Addressing returns the addressing text and its entities (Hermes
// addressing_sources). The text has code and link labels as richMask and has
// no URL.
func (d *richDoc) Addressing() (text string, ents []richEntity) {
	defer d.guard(func() { text, ents = "", nil })
	var typed []richAddrRange
	raw := richFlatten(d.blocks, true, func(span map[string]any, start, end int) {
		r := richAddrRange{typ: richType(span), start: start, end: end}
		switch r.typ {
		case "mention", "bot_command":
		case "text_mention":
			var u User
			if !richObject(span["user"], &u) {
				return
			}
			r.user = &u
		default:
			return
		}
		typed = append(typed, r)
	})
	lead := len(raw) - len(strings.TrimLeftFunc(raw, unicode.IsSpace))
	text = strings.TrimSpace(raw)

	var rs []richAddrRange
	for _, loc := range richAddrCommandRe.FindAllStringIndex(text, -1) {
		if richAddrCommandAt(text, loc[0], loc[1]) {
			rs = append(rs, richAddrRange{typ: "bot_command", start: loc[0], end: loc[1]})
		}
	}
	for _, loc := range richAddrMentionRe.FindAllStringIndex(text, -1) {
		if richAddrMentionAt(text, loc[0], loc[1]) {
			rs = append(rs, richAddrRange{typ: "mention", start: loc[0], end: loc[1]})
		}
	}
	for _, r := range typed {
		r.start, r.end = max(r.start-lead, 0), min(r.end-lead, len(text))
		if r.start < r.end {
			rs = append(rs, r)
		}
	}
	slices.SortStableFunc(rs, func(a, b richAddrRange) int { return cmp.Compare(a.start, b.start) })

	// The ranges are in the order of their start, so one pass over the text
	// gives each UTF-16 offset.
	pos, units := 0, 0
	for i, r := range rs {
		// A typed span and the scan can match the same text. Keep one entity.
		// The ranges with the same start are next to each other.
		dup := false
		for j := i - 1; j >= 0 && rs[j].start == r.start; j-- {
			dup = dup || rs[j].typ == r.typ && rs[j].end == r.end
		}
		if dup {
			continue
		}
		units += UTF16Len(text[pos:r.start])
		pos = r.start
		part := text[r.start:r.end]
		ents = append(ents, richEntity{Type: r.typ, Offset: units, Length: UTF16Len(part), Text: part, User: r.user})
	}
	return text, ents
}
