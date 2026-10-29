package telegram

import "strings"

// This file splits formatted MarkdownV2 text into messages. It adds a cut
// that keeps each entity whole to the split rules of Hermes truncate_message
// (gateway/platforms/base.py). SplitUTF16 stays the split for plain text.

// minMarkdownSplit is the smallest limit for SplitMarkdownV2. A smaller limit
// has no room for the fence lines and for a safe cut.
const minMarkdownSplit = 64

// mdv2CloseFence ends a part that a cut inside a code block ends.
const mdv2CloseFence = "\n```"

// SplitMarkdownV2 splits formatted MarkdownV2 text, the output of
// ToMarkdownV2, into parts of at most limit UTF-16 units. It cuts where no
// entity is open, and it prefers a newline, then a space. It drops the one
// newline or space at a cut. A cut inside a code block closes the block at
// the end of the part and opens it again, with the same language, at the
// start of the next part. It never returns an empty part, but an empty s
// gives one empty part, as in SplitUTF16. When limit is less than
// minMarkdownSplit, it returns SplitUTF16(s, limit).
func SplitMarkdownV2(s string, limit int) []string {
	if limit < minMarkdownSplit {
		return SplitUTF16(s, limit)
	}
	if UTF16Len(s) <= limit {
		return []string{s}
	}
	var parts []string
	for s != "" {
		if UTF16Len(s) <= limit {
			parts = append(parts, s)
			break
		}
		cut := mdv2CutPoint(s, limit)
		if cut.pre {
			// This is the carry_lang rule of Hermes truncate_message. Drop
			// only the newline at the cut. A space or a tab is the
			// indentation of the code.
			parts = append(parts, s[:cut.at]+mdv2CloseFence)
			s = mdv2ReopenFence(cut.lang, limit) + strings.TrimPrefix(s[cut.at:], "\n")
			continue
		}
		parts = append(parts, s[:cut.at])
		s = s[cut.at:]
		if s != "" && (s[0] == '\n' || s[0] == ' ') {
			s = s[1:]
		}
	}
	return parts
}

// mdv2ReopenFence returns the fence line that opens a code block again after
// a cut. When the line with the language is longer than a quarter of limit,
// the line has no language. The code stays whole, and only the highlight is
// lost.
func mdv2ReopenFence(lang string, limit int) string {
	line := "```" + lang + "\n"
	if UTF16Len(line) > limit/4 {
		return "```\n"
	}
	return line
}

// mdv2Cut is the end of the next part.
type mdv2Cut struct {
	at   int    // the byte index where the part ends
	pre  bool   // the cut is inside a code block
	lang string // the language of that code block
}

// mdv2CutPoint returns the end of the next part of s. It takes the first of
// these cuts that it finds:
//  1. the last newline with no open entity, or with only a code block open,
//     in the second half of the limit;
//  2. the last space with no open entity;
//  3. the last offset with no open entity;
//  4. the last offset in a code block, else the last offset in the limit
//     (a hard cut);
//  5. one rune.
//
// A cut inside a code block keeps room for the close fence. It is not at the
// start or at the end of the code, because that gives an empty code block. A
// hard cut outside a code block happens only when one entity is longer than
// the limit. Telegram then rejects the part, and the plain-text fallback
// sends it.
func mdv2CutPoint(s string, limit int) mdv2Cut {
	units, prev, prevPre := 0, 0, false
	var newline, space, safe, hard mdv2Cut
	scanMDV2(s, func(i int, st mdv2State) bool {
		units += UTF16Len(s[prev:i])
		prev = i
		if units > limit {
			return false
		}
		bodyStart := st.Pre && !prevPre
		prevPre = st.Pre
		if i == 0 || i == len(s) {
			return true
		}
		switch {
		case st.Open == 0 && st.Pre:
			if bodyStart || units+UTF16Len(mdv2CloseFence) > limit || mdv2FenceAfter(s, i) {
				return true
			}
			c := mdv2Cut{at: i, pre: true, lang: st.Lang}
			hard = c
			if s[i] == '\n' && units >= limit/2 {
				newline = c
			}
		case st.Open == 0:
			c := mdv2Cut{at: i}
			hard, safe = c, c
			switch {
			case s[i] == '\n' && units >= limit/2:
				newline = c
			case s[i] == ' ':
				space = c
			}
		case !hard.pre:
			// A cut in a code block gives valid parts, so a cut in an
			// entity, for example in the close fence, does not replace it.
			hard = mdv2Cut{at: i}
		}
		return true
	})
	for _, cut := range []mdv2Cut{newline, space, safe, hard} {
		if cut.at > 0 {
			return cut
		}
	}
	return mdv2Cut{at: runeSize(s, 0)}
}

// mdv2FenceAfter reports whether the close fence of a code block comes right
// after a cut at s[i], so that the next part has no code.
func mdv2FenceAfter(s string, i int) bool {
	return strings.HasPrefix(strings.TrimPrefix(s[i:], "\n"), "```")
}
