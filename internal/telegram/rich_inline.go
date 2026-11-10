package telegram

import (
	"regexp"
	"strings"
	"unicode"
)

// richCtx holds the state of one conversion of a rich message to Markdown.
type richCtx struct {
	botUser string
	unknown map[string]bool
	// mention matches a mention of the bot. Nil means that no mention is removed.
	mention *regexp.Regexp
	// tail says what the next plain string can lose at its start, because a
	// mention ended the plain string before it.
	tail int
	// keep is more than 0 in a span whose text must not change.
	keep int
}

// newRichCtx returns a context with an empty set of unknown type names.
func newRichCtx(botUser string) *richCtx {
	return &richCtx{botUser: botUser, unknown: map[string]bool{}, mention: richMentionRegex(botUser)}
}

// The values of richCtx.tail.
const (
	richTailNone   = iota
	richTailSeps   // "," ":" "-", and then white space
	richTailSpaces // white space only
)

// richSpaces holds the characters that \s matches in a Go regexp.
const richSpaces = " \t\n\f\r"

var (
	richTailSepsRE   = regexp.MustCompile(`^[,:\-]*\s*`)
	richTailSpacesRE = regexp.MustCompile(`^\s*`)
)

// richMentionRegex returns the regex of StripMention for the bot user name,
// or nil for an empty name. It shares mentionCache with StripMention, so the
// two patterns must stay the same.
func richMentionRegex(botUser string) *regexp.Regexp {
	botUser = strings.TrimPrefix(botUser, "@")
	if botUser == "" {
		return nil
	}
	if v, ok := mentionCache.Load(botUser); ok {
		return v.(*regexp.Regexp)
	}
	re := regexp.MustCompile(`(?i)@` + regexp.QuoteMeta(botUser) + `\b[,:\-]*\s*`)
	mentionCache.Store(botUser, re)
	return re
}

// dropMention removes each mention of the bot from the plain string s. A
// match can end at the end of s. Then the rest of the match goes away at the
// start of the next plain string.
func (c *richCtx) dropMention(s string) string {
	if c.mention == nil || s == "" {
		return s
	}
	if c.keep > 0 {
		c.tail = richTailNone
		return s
	}
	if c.tail != richTailNone {
		re := richTailSepsRE
		if c.tail == richTailSpaces {
			re = richTailSpacesRE
		}
		cut := re.FindString(s)
		s = s[len(cut):]
		if s == "" {
			if strings.ContainsAny(cut, richSpaces) {
				c.tail = richTailSpaces
			}
			return ""
		}
		c.tail = richTailNone
	}
	locs := c.mention.FindAllStringIndex(s, -1)
	if len(locs) == 0 {
		return s
	}
	if last := locs[len(locs)-1]; last[1] == len(s) {
		c.tail = richTailSeps
		if strings.ContainsAny(s[last[0]:], richSpaces) {
			c.tail = richTailSpaces
		}
	}
	return c.mention.ReplaceAllString(s, "")
}

// richTicks returns a run of backticks that is longer than each run in s and
// has min characters or more.
func richTicks(s string, min int) string {
	longest, run := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(longest+1, min))
}

// richTextOnly lists the span types that give only their inner text.
var richTextOnly = map[string]bool{
	"bold": true, "italic": true, "strikethrough": true, "code": true, "url": true,
	"underline": true, "spoiler": true, "marked": true, "subscript": true,
	"superscript": true, "date_time": true, "mention": true, "text_mention": true,
	"bot_command": true, "hashtag": true, "cashtag": true, "anchor_link": true,
	"reference": true, "reference_link": true,
	"email_address": true, "phone_number": true, "bank_card_number": true, "ton_address": true,
}

// richFallback returns the first value that is not empty, for a span of an
// unknown type. It records the type name when the name is not empty.
func (c *richCtx) richFallback(span map[string]any, typ string, depth int, conv func(any, int) string) string {
	if typ != "" {
		c.unknown[typ] = true
	}
	if s := conv(span["text"], depth+1); s != "" {
		return s
	}
	if s := conv(span["children"], depth+1); s != "" {
		return s
	}
	for _, k := range []string{"url", "expression", "alternative_text"} {
		if s := richStr(span[k]); s != "" {
			return s
		}
	}
	return ""
}

// plain returns the text of a RichText value with no markers.
func (c *richCtx) plain(v any, depth int) string {
	if depth > maxRichDepth {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var b strings.Builder
		for _, part := range x {
			b.WriteString(c.plain(part, depth+1))
		}
		return b.String()
	case map[string]any:
		typ := richStr(x["type"])
		switch {
		case typ == "anchor":
			return ""
		case typ == "custom_emoji":
			return richStr(x["alternative_text"])
		case typ == "mathematical_expression":
			return richStr(x["expression"])
		case typ == "button":
			return c.plain(richMap(x["button"])["text"], depth+1)
		case richTextOnly[typ]:
			return c.plain(x["text"], depth+1)
		}
		return c.richFallback(x, typ, depth, c.plain)
	}
	return ""
}

// richWrap puts the markers around the core of s. White space at the start
// and the end of s stays outside. An empty core gives s with no marker. Each
// line of s gets its own markers, because the output lines are split later.
func richWrap(s, open, closing string) string {
	if strings.Contains(s, "\n") {
		lines := strings.Split(s, "\n")
		for i, l := range lines {
			lines[i] = richWrap(l, open, closing)
		}
		return strings.Join(lines, "\n")
	}
	core := strings.TrimFunc(s, unicode.IsSpace)
	if core == "" {
		return s
	}
	i := strings.Index(s, core)
	return s[:i] + open + core + closing + s[i+len(core):]
}

// inline returns the Markdown of a RichText value.
func (c *richCtx) inline(v any, depth int) string {
	if depth > maxRichDepth {
		return ""
	}
	switch x := v.(type) {
	case string:
		return c.dropMention(x)
	case []any:
		var b strings.Builder
		for _, part := range x {
			b.WriteString(c.inline(part, depth+1))
		}
		return b.String()
	case map[string]any:
		return c.inlineSpan(x, depth)
	}
	return ""
}

func (c *richCtx) inlineSpan(span map[string]any, depth int) string {
	typ := richStr(span["type"])
	switch typ {
	case "bold":
		return richWrap(c.inline(span["text"], depth+1), "**", "**")
	case "italic":
		return richWrap(c.inline(span["text"], depth+1), "*", "*")
	case "strikethrough":
		return richWrap(c.inline(span["text"], depth+1), "~~", "~~")
	case "code":
		c.tail = richTailNone
		text := c.plain(span["text"], depth+1)
		if text == "" {
			return ""
		}
		ticks := richTicks(text, 1)
		if strings.HasPrefix(text, "`") || strings.HasSuffix(text, "`") {
			text = " " + text + " "
		}
		return ticks + text + ticks
	case "url":
		c.keep++
		inner := c.inline(span["text"], depth+1)
		c.keep--
		u := richStr(span["url"])
		trimmed := strings.TrimSpace(inner)
		switch {
		case u == "":
			return inner
		case trimmed == u:
			return inner
		case trimmed == "":
			return u
		}
		return richWrap(inner, "[", "]("+u+")")
	case "mathematical_expression":
		c.tail = richTailNone
		e := richStr(span["expression"])
		if e == "" {
			return ""
		}
		return "$" + e + "$"
	case "custom_emoji":
		c.tail = richTailNone
		return richStr(span["alternative_text"])
	case "button":
		t := strings.TrimSpace(c.inline(richMap(span["button"])["text"], depth+1))
		if t == "" {
			return ""
		}
		return "[button: " + t + "]"
	case "anchor":
		return ""
	case "email_address", "phone_number", "bank_card_number", "ton_address":
		inner := c.inline(span["text"], depth+1)
		key := typ
		if typ == "ton_address" {
			key = "address"
		}
		value := richStr(span[key])
		if value == "" || strings.Contains(inner, value) {
			return inner
		}
		if strings.TrimSpace(inner) == "" {
			return value
		}
		return richWrap(inner, "", " ("+value+")")
	}
	if richTextOnly[typ] {
		return c.inline(span["text"], depth+1)
	}
	return c.richFallback(span, typ, depth, c.inline)
}
