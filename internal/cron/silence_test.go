package cron_test

import (
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/cron"
)

// TestIsSilent ports the cases of the Hermes tests for is_autonomous_silence_response and
// is_intentional_silence_response (tests/gateway/test_response_filters.py,
// tests/gateway/test_gateway_silence_tokens.py, and TestSilentDelivery in
// tests/cron/test_scheduler.py). Each want value is the result of the Hermes code.
func TestIsSilent(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		// The exact markers, in any case and with any whitespace.
		{"[SILENT]", true},
		{"[silent]", true},
		{" SILENT ", true},
		{"SILENT", true},
		{"NO_REPLY", true},
		{"no_reply", true},
		{"NO REPLY", true},
		{"no reply", true},
		{"no  reply", true},
		{"NO\treply", true},
		{"NO REPLY", true},
		{"　[SILENT]　", true},
		// Edge punctuation goes away.
		{"*NO_REPLY*", true},
		{"NO_REPLY.", true},
		{"No reply.", true},
		{"SILENT!!!", true},
		{"( SILENT )", true},
		{"-- silent --", true},
		{`"[SILENT]"`, true},
		{"'NO_REPLY'", true},
		// Square brackets stay, so a broken bracket is not a marker.
		{"[SILENT", false},
		{"SILENT]", false},
		{"[ SILENT ]", false},
		// The prefix rule: the text starts with a bracketed marker.
		{"[SILENT] nothing new", true},
		{"[silent] nothing new", true},
		{"[SILENT] No changes detected", true},
		// A marker on its own first or last line.
		{"Report…\n\n[SILENT]", true},
		{"[SILENT]\n\nNothing new this tick.", true},
		{"2 deals filtered\n\n[SILENT]", true},
		{"2 deals filtered out (like<10, reply<15).\n\n[SILENT]", true},
		{"no_reply\nduplicate inbound, already handled", true},
		{"nothing new\nNO_REPLY.", true},
		{"Report\n**NO REPLY**", true},
		// A marker on a middle line only is not silent.
		{"Report\n[SILENT]\nMore", false},
		// The translated markers.
		{"静默", true},
		{"[静默]", true},
		{"**沉默**", true},
		{"【静默】", true},
		{"静默。", true},
		{"【沉默】", true},
		{"沉默。", true},
		{"**[静默]**", true},
		{"【SILENT】", true},
		{"[静默]\n\nNothing new this tick.", true},
		{"2 deals filtered\n\n[沉默]", true},
		{"status: 静默 means the lane is quiet", false},
		{"the lane said 静默 mid-sentence and kept talking", false},
		// Prose that mentions a marker.
		{"Use NO_REPLY when no answer is needed.", false},
		{"The reply was [SILENT], intentionally.", false},
		{"Silent retry succeeded", false},
		{"silently", false},
		// An empty answer is not silent.
		{"", false},
		{"   ", false},
		{"   \n\t  ", false},
		// The cap is 64 runes after the trim, not 64 bytes.
		{"SILENT" + strings.Repeat(".", 58), true},
		{"SILENT" + strings.Repeat(".", 59), false},
		{strings.Repeat(".", 58) + "SILENT", true},
		{"静默" + strings.Repeat("。", 62), true},
		{"静默" + strings.Repeat("。", 63), false},
		{strings.Repeat("x", 65), false},
		{"[SILENT]" + strings.Repeat(" ", 60), true},
		// U+001F is whitespace for Python str.strip, but not for strings.TrimSpace.
		{"Report\n\x1fNO_REPLY\x1f", true},
		{"NO_REPLY\x1f", true},
	}
	for _, c := range cases {
		if got := cron.IsSilent(c.text); got != c.want {
			t.Errorf("IsSilent(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

// TestIsSilentQuoteInSentence checks that a report that quotes a marker inside a sentence
// still delivers (Hermes test_report_quoting_marker_mid_sentence_still_delivers).
func TestIsSilentQuoteInSentence(t *testing.T) {
	for _, text := range []string{
		"The log says [SILENT] mode is on.",
		"I considered staying [SILENT] but here is the summary: 3 items merged.",
		"Use [SILENT] when no answer is needed.",
		"Disk / is at 86%.\nThe log says [SILENT] mode is on.",
	} {
		if cron.IsSilent(text) {
			t.Errorf("IsSilent(%q) = true, want false: a quote inside a sentence must deliver", text)
		}
	}
}

// TestIsSilentLineBreaks checks each line boundary of Python str.splitlines. A marker on
// the first or the last line is silent with each boundary.
func TestIsSilentLineBreaks(t *testing.T) {
	for _, sep := range []string{"\n", "\r\n", "\r", "\v", "\f", "\x1c", "\x1d", "\x1e",
		"\u0085", " ", " "} {
		for _, text := range []string{
			"Report" + sep + "[SILENT]",
			"Report" + sep + "NO_REPLY",
			"NO_REPLY" + sep + "Report",
		} {
			if !cron.IsSilent(text) {
				t.Errorf("IsSilent(%q) = false, want true", text)
			}
		}
	}
	if !cron.IsSilent("Report\r\n\r\n[SILENT]\r\n") {
		t.Error("a CRLF answer with a last [SILENT] line must be silent")
	}
	// U+001F is whitespace, but it is not a line boundary.
	for _, text := range []string{"Report\x1f[SILENT]", "NO_REPLY\x1fReport"} {
		if cron.IsSilent(text) {
			t.Errorf("IsSilent(%q) = true, want false", text)
		}
	}
}
