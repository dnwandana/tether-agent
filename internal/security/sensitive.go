package security

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"sort"
	"strings"
)

// Finding is one piece of sensitive data in a text: its kind, its byte span, and the label
// that replaces it.
type Finding struct {
	Kind       string
	Start, End int
	Label      string
}

const (
	scanLongString = 64 << 10
	scanEdge       = 8 << 10
	bullets4       = "••••"
	bullets5       = "•••••"
)

// addressKeys are the argument keys whose phone numbers are the target of a send.
var addressKeys = map[string]bool{
	"to": true, "cc": true, "bcc": true, "recipient": true, "recipients": true,
	"phone": true, "number": true, "chat_id": true,
}

// Scan returns the sensitive data in text, in order, with no overlap. r gives the secret
// matches and can be nil. handleValues are the values of the handles of the run.
func Scan(text string, r *Redactor, handleValues []string) []Finding {
	return scanText(text, r, handleValues, true)
}

func scanText(text string, r *Redactor, handleValues []string, phone bool) []Finding {
	if text == "" {
		return nil
	}
	var all []Finding
	for _, m := range r.Find(text) {
		label := "[secret]"
		if m.Kind == "exact" && m.Name != "" {
			label = Label(m.Name)
		}
		all = append(all, Finding{Kind: "secret", Start: m.Start, End: m.End, Label: label})
	}
	for _, v := range handleValues {
		if len(v) < 8 {
			continue
		}
		for from := 0; ; {
			i := strings.Index(text[from:], v)
			if i < 0 {
				break
			}
			all = append(all, Finding{Kind: "secret", Start: from + i, End: from + i + len(v), Label: "[secret]"})
			from += i + len(v)
		}
	}
	all = append(all, findCards(text)...)
	all = append(all, findIBANs(text)...)
	if phone {
		all = append(all, findPhones(text)...)
	}
	return resolveFindings(all)
}

// resolveFindings keeps the longest of two findings that overlap, or the earlier one on the
// same length. It returns the result in text order.
func resolveFindings(all []Finding) []Finding {
	if len(all) == 0 {
		return nil
	}
	sort.SliceStable(all, func(i, j int) bool {
		li, lj := all[i].End-all[i].Start, all[j].End-all[j].Start
		if li != lj {
			return li > lj
		}
		return all[i].Start < all[j].Start
	})
	var out []Finding
	for _, f := range all {
		clash := false
		for _, k := range out {
			if f.Start < k.End && k.Start < f.End {
				clash = true
				break
			}
		}
		if !clash {
			out = append(out, f)
		}
	}
	slices.SortFunc(out, func(a, b Finding) int { return a.Start - b.Start })
	return out
}

// RedactFindings replaces each finding with its label.
func RedactFindings(text string, f []Finding) string {
	if len(f) == 0 {
		return text
	}
	var b strings.Builder
	prev := 0
	for _, x := range f {
		if x.Start < prev || x.End > len(text) || x.End < x.Start {
			continue
		}
		b.WriteString(text[prev:x.Start])
		b.WriteString(x.Label)
		prev = x.End
	}
	b.WriteString(text[prev:])
	return b.String()
}

// FindingKinds counts the findings of each kind.
func FindingKinds(f []Finding) map[string]int {
	m := map[string]int{}
	for _, x := range f {
		m[x.Kind]++
	}
	return m
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// digitsOf returns the digits of s.
func digitsOf(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if isDigit(s[i]) {
			b = append(b, s[i])
		}
	}
	return string(b)
}

func luhnOK(d string) bool {
	sum := 0
	for i, alt := 0, false; i < len(d); i++ {
		n := int(d[len(d)-1-i] - '0')
		if alt {
			if n *= 2; n > 9 {
				n -= 9
			}
		}
		sum += n
		alt = !alt
	}
	return sum%10 == 0
}

// findCards finds card numbers: 13 to 19 digits with single spaces or dashes, a first digit
// of 2 to 6, and a passing Luhn check. A card starts after a character that is not a letter or
// a digit. A letter after a backslash does not count, as for an IBAN.
func findCards(s string) []Finding {
	var out []Finding
	for i := 0; i < len(s); {
		if !isDigit(s[i]) || i > 0 && !ibanStart(s, i) {
			i++
			continue
		}
		// The longest run of digits and single separators.
		end := i
		var ends []int // the end of the run before each separator
		for end < len(s) {
			if isDigit(s[end]) {
				end++
			} else if (s[end] == ' ' || s[end] == '-') && end+1 < len(s) && isDigit(s[end+1]) {
				ends = append(ends, end)
				end++
			} else {
				break
			}
		}
		ends = append(ends, end)
		// A digit group before the card, such as an order number or a date, does not hide the
		// card. Thus each group start of the run is a start, the first group first. After a
		// card, the next start is the group after the card.
		for g := 0; g < len(ends); g++ {
			start := i
			if g > 0 {
				start = ends[g-1] + 1
			}
			if k, ok := cardAt(s, start, ends[g:]); ok {
				f := ends[g+k]
				d := digitsOf(s[start:f])
				out = append(out, Finding{Kind: "card", Start: start, End: f, Label: "[card " + bullets4 + d[len(d)-4:] + "]"})
				g += k
			}
		}
		i = end
	}
	return out
}

// maxCardSpan is the longest text of a card: 19 digits and 18 separators.
const maxCardSpan = 19 + 18

// cardAt reports whether a card starts at start, and returns the index in ends of the
// longest one. ends is in text order. An end past maxCardSpan cannot end a card, so a long
// run costs little.
func cardAt(s string, start int, ends []int) (int, bool) {
	n := 0
	for n < len(ends) && ends[n]-start <= maxCardSpan {
		n++
	}
	for k := n - 1; k >= 0; k-- {
		d := digitsOf(s[start:ends[k]])
		if len(d) < 13 {
			break
		}
		if len(d) <= 19 && d[0] >= '2' && d[0] <= '6' && luhnOK(d) {
			return k, true
		}
	}
	return 0, false
}

// ibanStart reports whether an IBAN can start at i. A letter after a backslash, as in the
// JSON form of a new line, is not a letter for this test.
func ibanStart(s string, i int) bool {
	if i == 0 {
		return true
	}
	c := s[i-1]
	if !isLetter(c) && !isDigit(c) {
		return true
	}
	return isLetter(c) && i >= 2 && s[i-2] == '\\'
}

// findIBANs finds IBANs: a known country, the length of that country, and a passing mod-97
// check. Single spaces can come between the characters.
func findIBANs(s string) []Finding {
	var out []Finding
	for i := 0; i+4 <= len(s); i++ {
		if !isLetter(s[i]) || !isLetter(s[i+1]) || !isDigit(s[i+2]) || !isDigit(s[i+3]) || !ibanStart(s, i) {
			continue
		}
		cc := strings.ToUpper(s[i : i+2])
		want, ok := ibanLengths[cc]
		if !ok {
			continue
		}
		var compact []byte
		end := i
		for end < len(s) && len(compact) < want {
			c := s[end]
			if isLetter(c) || isDigit(c) {
				compact = append(compact, c)
				end++
			} else if c == ' ' && len(compact) > 0 && end+1 < len(s) && (isLetter(s[end+1]) || isDigit(s[end+1])) {
				end++
			} else {
				break
			}
		}
		if len(compact) != want || end < len(s) && (isLetter(s[end]) || isDigit(s[end])) {
			continue
		}
		up := strings.ToUpper(string(compact))
		if !validIBAN(up) {
			continue
		}
		out = append(out, Finding{Kind: "iban", Start: i, End: end, Label: "[IBAN " + up[:4] + bullets4 + up[len(up)-4:] + "]"})
		i = end - 1
	}
	return out
}

// phoneGap parses the text between two digit groups of a phone number at p. It returns the
// position of the next digit, or -1 when the text there is not a gap.
func phoneGap(s string, p int, open, closed *bool) int {
	if p >= len(s) {
		return -1
	}
	switch c := s[p]; {
	case c == ')' && *open && !*closed:
		*closed = true
		p++
		if p < len(s) && (s[p] == ' ' || s[p] == '-' || s[p] == '.') {
			p++
		}
	case c == '(' && !*open:
		*open = true
		p++
	case c == ' ' || c == '-' || c == '.':
		p++
		if p < len(s) && s[p] == '(' && !*open {
			*open = true
			p++
		}
	default:
		return -1
	}
	if p < len(s) && isDigit(s[p]) {
		return p
	}
	return -1
}

// findPhones finds international numbers: a +, then 8 to 15 digits with single separators and
// one optional pair of parentheses.
func findPhones(s string) []Finding {
	var out []Finding
	for i := 0; i < len(s); i++ {
		if s[i] != '+' || i+1 >= len(s) || !isDigit(s[i+1]) || i > 0 && isDigit(s[i-1]) {
			continue
		}
		type cut struct {
			end    int
			digits int
			ok     bool // the parentheses are balanced
		}
		var cuts []cut
		var open, closed bool
		p, digits := i+1, 0
		end := p
		for {
			for p < len(s) && isDigit(s[p]) {
				p++
				digits++
			}
			end = p
			cuts = append(cuts, cut{end, digits, open == closed})
			// A cut at the end of this group: a longer run needs a gap and more digits.
			o, c := open, closed
			np := phoneGap(s, p, &o, &c)
			if np < 0 {
				break
			}
			open, closed = o, c
			p = np
		}
		found := false
		for _, c := range slices.Backward(cuts) {
			if c.digits < 8 {
				break
			}
			if c.digits <= 15 && c.ok {
				d := digitsOf(s[i:c.end])
				out = append(out, Finding{Kind: "phone", Start: i, End: c.end, Label: "[phone +" + d[:2] + bullets5 + d[len(d)-4:] + "]"})
				i = c.end - 1
				found = true
				break
			}
		}
		if !found {
			i = end - 1
		}
	}
	return out
}

// ScanArgs scans each string value of the MCP arguments, and returns the findings and the
// arguments with each finding replaced. redacted is nil when nothing is found.
func ScanArgs(args json.RawMessage, r *Redactor, handleValues []string) (findings []Finding, redacted json.RawMessage) {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, nil
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, nil
	}
	nv := walkArgs(v, false, r, handleValues, &findings)
	if len(findings) == 0 {
		return nil, nil
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if enc.Encode(nv) != nil {
		return nil, nil
	}
	return findings, bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

func walkArgs(v any, addr bool, r *Redactor, hv []string, out *[]Finding) any {
	switch x := v.(type) {
	case string:
		f := scanString(x, r, hv, !addr)
		if len(f) == 0 {
			return x
		}
		*out = append(*out, f...)
		return RedactFindings(x, f)
	case []any:
		for i := range x {
			x[i] = walkArgs(x[i], addr, r, hv, out)
		}
	case map[string]any:
		for k, c := range x {
			x[k] = walkArgs(c, addressKeys[strings.ToLower(k)], r, hv, out)
		}
	}
	return v
}

// scanString scans a string value. A string over 64 KB is scanned at its first and last 8 KB.
func scanString(s string, r *Redactor, hv []string, phone bool) []Finding {
	if len(s) <= scanLongString {
		return scanText(s, r, hv, phone)
	}
	f := scanText(s[:scanEdge], r, hv, phone)
	off := len(s) - scanEdge
	for _, x := range scanText(s[off:], r, hv, phone) {
		x.Start += off
		x.End += off
		f = append(f, x)
	}
	return f
}

// PartialExact returns the byte length of the longest suffix of tail that is the start of a
// known exact value or of its JSON form, at least 4 bytes. It returns 0 when none is.
func (r *Redactor) PartialExact(tail string) int {
	set := r.loadExact()
	if set == nil {
		return 0
	}
	best := 0
	for _, o := range set.olds {
		for n := min(len(o.text)-1, len(tail)); n >= 4 && n > best; n-- {
			if strings.HasSuffix(tail, o.text[:n]) {
				best = n
				break
			}
		}
	}
	return best
}
