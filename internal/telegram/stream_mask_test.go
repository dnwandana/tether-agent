package telegram

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
)

// scanMask masks with the detectors and the redactor patterns, like the channel in a group.
func scanMask(text string) (string, []security.Finding) {
	f := security.Scan(text, security.NewRedactor(nil), nil)
	return security.RedactFindings(text, f), f
}

// streamTexts returns the text of each send and edit, without the MarkdownV2 markup.
func streamTexts(api *fakeBot) []string {
	var out []string
	for _, c := range api.all() {
		if c.Method != "sendMessage" && c.Method != "editMessageText" {
			continue
		}
		s, _ := c.Body["text"].(string)
		if c.Body["parse_mode"] == "MarkdownV2" {
			s = StripMarkdownV2(s)
		}
		out = append(out, s)
	}
	return out
}

// A card number in three deltas, with a tool call after them: no send or edit of the group
// message shows more than the label, and the label goes out above the tool status.
func TestStreamMaskCardAcrossDeltas(t *testing.T) {
	s, api, clk := streamer(t, false)
	s.Mask = scanMask
	for _, d := range []string{"Here is the card:\n", "Pay with 4111 1111", " 1111 11", "11 today."} {
		clk.Advance(time.Second)
		s.Delta(ctx, d)
	}
	s.Boundary(ctx) // the tool call
	clk.Advance(time.Second)
	s.Delta(ctx, "The payment is done.")
	if err := s.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	const label = "[card ••••1111]"
	texts := streamTexts(api)
	lastLabel, firstAfter := -1, -1
	for i, txt := range texts {
		if strings.Contains(txt, label) {
			lastLabel = i
		}
		if strings.Contains(txt, "The payment is done.") && firstAfter < 0 {
			firstAfter = i
		}
		if rest := strings.ReplaceAll(txt, label, ""); strings.ContainsAny(rest, "14") {
			t.Fatalf("a send shows a part of the card: %q", txt)
		}
	}
	if lastLabel < 0 || firstAfter < 0 || lastLabel > firstAfter {
		t.Fatalf("the label must go out before the text after the tool call: %q", texts)
	}
}

func TestStreamMaskHoldsPEMUntilEnd(t *testing.T) {
	s, api, clk := streamer(t, false)
	s.Mask = scanMask
	for _, d := range []string{"Key:\n-----BEGIN " + "PRIVATE KEY-----\nMIIEvQIBADAN",
		"BgkqhkiG9w0BAQEFAASC\n-----END PRIVATE KEY-----\n", "Done."} {
		clk.Advance(time.Second)
		s.Delta(ctx, d)
	}
	if err := s.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	for _, txt := range streamTexts(api) {
		if strings.Contains(txt, "MIIE") || strings.Contains(txt, "BgkqhkiG") {
			t.Fatalf("a send shows the key: %q", txt)
		}
	}
}

func TestStreamMaskHoldsLongExactValue(t *testing.T) {
	s, api, clk := streamer(t, false)
	v := "pypi-" + strings.Repeat("AgEIcHlwaS5vcmc", 7)
	s.Mask = func(text string) (string, []security.Finding) {
		var f []security.Finding
		if i := strings.Index(text, v); i >= 0 {
			f = append(f, security.Finding{Kind: "secret", Start: i, End: i + len(v), Label: "«PYPI_TOKEN»"})
		}
		return security.RedactFindings(text, f), f
	}
	s.Partial = func(tail string) int {
		for k := min(len(tail), len(v)); k > 0; k-- {
			if strings.HasSuffix(tail, v[:k]) {
				return k
			}
		}
		return 0
	}
	text := "Token:\n" + v + "\nend"
	for i := 0; i < len(text); i += 20 {
		clk.Advance(time.Second)
		s.Delta(ctx, text[i:min(i+20, len(text))])
	}
	if err := s.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	texts := streamTexts(api)
	for _, txt := range texts {
		if strings.Contains(txt, "pypi-AgEI") {
			t.Fatalf("a send shows a part of the value: %q", txt)
		}
	}
	if !slices.ContainsFunc(texts, func(s string) bool { return strings.Contains(s, "«PYPI_TOKEN»") }) {
		t.Fatalf("no send shows the label: %q", texts)
	}
}

// With no mask, the stream does not hold a tail.
func TestStreamNoMaskHoldsNothing(t *testing.T) {
	s, api, clk := streamer(t, false)
	clk.Advance(time.Second)
	s.Delta(ctx, "Pay with 4111 1111")
	if got := api.lastText(); got != "Pay with 4111 1111 ▉" {
		t.Fatal(got)
	}
}

func TestHoldCut(t *testing.T) {
	for _, tc := range []struct {
		rest string
		want int
	}{
		{"Done.\n", 6},
		{"Here:\nPay 4111", 6},
		{"a,  b", 4},
		{strings.Repeat("x", 100), 36},
		{"Key:\n-----BEGIN RSA " + "PRIVATE KEY-----\nabc\n", 5},
		{"k\n-----BEGIN X-----\na\n-----END X-----\n", 38},
	} {
		if got := holdCut(tc.rest, nil); got != tc.want {
			t.Errorf("%q: got %d, want %d", tc.rest, got, tc.want)
		}
	}
	if got := rawOffset([]security.Finding{{Start: 4, End: 23, Label: "[card ••••1111]"}}, 4+len("[card ••••1111]")+3); got != 26 {
		t.Fatal(got)
	}
}

// Numbered preview parts of a masked text keep each byte. The commit of a later part maps its
// masked bytes with the findings of the open text, not with the findings of the text before
// the first part.
func TestStreamMaskPartsKeepText(t *testing.T) {
	s, api, _ := streamer(t, false)
	s.Mask = scanMask
	text := "Pay with 4111 1111 1111 1111 today.\n" + strings.Repeat("word ", 900) + "END"
	s.Delta(ctx, text)
	if err := s.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if n := api.count("editMessageText"); n != 0 {
		t.Fatalf("the test expects only sends: %d edits", n)
	}
	number := regexp.MustCompile(`\s*\(\d+/\d+\)$`)
	var got strings.Builder
	for _, p := range streamTexts(api) {
		got.WriteString(number.ReplaceAllString(p, ""))
	}
	masked, _ := scanMask(text)
	noSpace := func(s string) string { return strings.Join(strings.Fields(s), "") }
	if g, w := noSpace(got.String()), noSpace(masked); g != w {
		t.Fatalf("the parts lose text: got %d bytes, want %d bytes", len(g), len(w))
	}
}

// exactMask returns a mask that replaces each exact value with its label, like the mask of
// the known values of .env.
func exactMask(values, labels []string) func(string) (string, []security.Finding) {
	return func(text string) (string, []security.Finding) {
		var f []security.Finding
		for i, v := range values {
			for at := 0; ; {
				j := strings.Index(text[at:], v)
				if j < 0 {
					break
				}
				f = append(f, security.Finding{Kind: "secret", Start: at + j, End: at + j + len(v), Label: labels[i]})
				at += j + len(v)
			}
		}
		slices.SortFunc(f, func(a, b security.Finding) int { return a.Start - b.Start })
		return security.RedactFindings(text, f), f
	}
}

// exactPartial returns the Partial function of the exact values, like the channel.
func exactPartial(values ...string) func(string) int {
	return func(tail string) int {
		best := 0
		for _, v := range values {
			for n := min(len(v)-1, len(tail)); n > best; n-- {
				if strings.HasSuffix(tail, v[:n]) {
					best = n
					break
				}
			}
		}
		return best
	}
}

// A split of the numbered preview parts never cuts a label, and no part shows a part of a
// secret. The line has no newline, so the split cuts at the limit.
func TestStreamMaskPartsNeverCutLabel(t *testing.T) {
	v1 := "SECRETONE" + strings.Repeat("x", 30)
	v2 := "pypi-" + strings.Repeat("AgEIcHlwaS5vcmc", 3)
	labels := []string{"«A_VERY_LONG_SECRET_NAME_ONE»", "«PYPI_TOKEN»"}
	mask := exactMask([]string{v1, v2}, labels)
	for pad := 3940; pad < 3990; pad++ {
		s, api, clk := streamer(t, false)
		s.Mask = mask
		s.Partial = exactPartial(v1, v2)
		text := strings.Repeat("a", pad) + v1 + " " + v2 + " tail text here."
		clk.Advance(time.Second)
		s.Delta(ctx, text)
		clk.Advance(time.Second)
		if err := s.Finish(ctx, ""); err != nil {
			t.Fatal(err)
		}
		for _, txt := range streamTexts(api) {
			for _, v := range []string{v1, v2} {
				for i := 0; i+8 <= len(v); i++ {
					if strings.Contains(txt, v[i:i+8]) {
						t.Fatalf("pad %d: a message shows a part of a secret: ...%q", pad, txt[max(0, len(txt)-90):])
					}
				}
			}
		}
		for _, txt := range streamTexts(api) {
			if strings.Count(txt, "«") != strings.Count(txt, "»") {
				t.Fatalf("pad %d: a message shows a part of a label: ...%q", pad, txt[max(0, len(txt)-90):])
			}
		}
	}
}

// The held tail is at most 64 bytes, so its cut can fall inside a value that is complete in
// the open text. The preview then holds the full value back and shows no part of it.
func TestStreamMaskHoldCutInsideValue(t *testing.T) {
	v := "pypi-" + strings.Repeat("AgEIcHlwaS5vcmc", 6)
	s, api, clk := streamer(t, false)
	s.Mask = exactMask([]string{v}, []string{"«PYPI_TOKEN»"})
	s.Partial = exactPartial(v)
	clk.Advance(time.Second)
	s.Delta(ctx, "The token is "+v+" and more.")
	for _, txt := range streamTexts(api) {
		if strings.Contains(txt, "pypi-") {
			t.Fatalf("the preview shows a part of the value: %q", txt)
		}
	}
	if err := s.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if got := api.lastText(); strings.Contains(got, "AgEI") || !strings.Contains(StripMarkdownV2(got), "«PYPI_TOKEN»") {
		t.Fatal(got)
	}
}
