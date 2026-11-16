package cron

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// silenceMarkers is LIVE_GATEWAY_SILENT_MARKERS of Hermes gateway/response_filters.py.
var silenceMarkers = map[string]bool{
	"[SILENT]": true, "SILENT": true, "NO_REPLY": true, "NO REPLY": true,
	"[静默]": true, "静默": true, "[沉默]": true, "沉默": true,
}

// bracketedMarkers are the markers that start with "[". Only these drive the prefix rule.
var bracketedMarkers = []string{"[SILENT]", "[沉默]", "[静默]"}

// markerMaxRunes is _MARKER_LENGTH_CAP of Hermes.
const markerMaxRunes = 64

// IsSilent reports whether an agent answer means "nothing to report". It ports
// is_autonomous_silence_response of Hermes gateway/response_filters.py. The answer is silent
// when it starts with a bracketed marker, or when its first or last non-blank line is a marker.
func IsSilent(text string) bool {
	t := strings.TrimFunc(text, isPySpace)
	if t == "" {
		return false
	}
	upper := strings.ToUpper(t)
	for _, m := range bracketedMarkers {
		if strings.HasPrefix(upper, m) {
			return true
		}
	}
	if isExactSilence(t) {
		return true
	}
	var lines []string
	for _, l := range strings.FieldsFunc(t, isLineBreak) {
		if l = strings.TrimFunc(l, isPySpace); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return false
	}
	return isExactSilence(lines[0]) || isExactSilence(lines[len(lines)-1])
}

// isExactSilence ports is_intentional_silence_response of Hermes.
func isExactSilence(text string) bool {
	for _, f := range silenceForms(text) {
		if silenceMarkers[f] {
			return true
		}
	}
	return false
}

// silenceForms ports _canonical_silence_candidates of Hermes.
func silenceForms(text string) []string {
	t := strings.TrimFunc(text, isPySpace)
	if n := utf8.RuneCountInString(t); n == 0 || n > markerMaxRunes {
		return nil
	}
	forms := []string{canonicalSilence(t)}
	if s := stripEdgePunct(t); s != t {
		forms = append(forms, canonicalSilence(s))
	}
	return forms
}

// canonicalSilence ports _canonical_silence_candidate of Hermes.
func canonicalSilence(text string) string {
	t := strings.ToUpper(strings.TrimFunc(text, isPySpace))
	return strings.Join(strings.FieldsFunc(t, isPySpace), " ")
}

// stripEdgePunct ports _strip_edge_silence_punctuation of Hermes. It keeps square brackets.
func stripEdgePunct(text string) string {
	edge := func(r rune) bool { return r != '[' && r != ']' && unicode.IsPunct(r) }
	return strings.TrimFunc(strings.TrimFunc(text, edge), isPySpace)
}

// isPySpace is Python str.isspace. It adds U+001C to U+001F to unicode.IsSpace.
func isPySpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// isLineBreak is a line boundary of Python str.splitlines.
func isLineBreak(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}
