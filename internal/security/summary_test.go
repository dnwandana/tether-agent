package security

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleanSummary(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"empty", "", ""},
		{"blank", " \n\t\u200B \r\n ", ""},
		{"new lines and tabs", "List\nthe\tfiles.\r\n", "List the files."},
		{"white space runs", "  Check   the  files.  ", "Check the files."},
		{"ANSI escape", "\x1b[31mRed\x1b[0m text", "Red text"},
		{"bidi controls", "Delete \u202Eold\u202C logs", "Delete old logs"},
		{"zero-width characters", "Che\u200Bck\u200D the\uFEFF files", "Check the files"},
		{"other control characters", "a\x00b\x07c\x7fd", "abcd"},
		{"bad UTF-8", "ok\xffyes", "ok\uFFFDyes"},
		{"200 runes stay", strings.Repeat("é", 200), strings.Repeat("é", 200)},
		{"cut to 200 runes", strings.Repeat("a", 250), strings.Repeat("a", 199) + "…"},
		{"cut counts runes", strings.Repeat("é", 201), strings.Repeat("é", 199) + "…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CleanSummary(tc.in)
			if got != tc.want {
				t.Fatalf("CleanSummary(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if !utf8.ValidString(got) || utf8.RuneCountInString(got) > 200 {
				t.Fatalf("bad result %q", got)
			}
			if strings.ContainsAny(got, "\x1b\u202E\u200B\n\t") {
				t.Fatalf("a control or format character stays in %q", got)
			}
		})
	}
}

func TestFallbackParts(t *testing.T) {
	for _, tc := range []struct {
		name          string
		req           ApprovalRequest
		pattern, line string
		more          int
	}{
		{"one line",
			ApprovalRequest{Command: "rm -rf /tmp/x", Description: "delete in root path"},
			"delete in root path", "rm -rf /tmp/x", 0},
		{"several lines and no line matches",
			ApprovalRequest{Command: "echo a\necho b\necho c", Description: "SQL DROP"},
			"SQL DROP", "echo a", 2},
		{"empty lines",
			ApprovalRequest{Command: "\n\n  \necho a\n\n\necho b\n", Description: "x"},
			"x", "echo a", 1},
		{"matched line in the middle of a script",
			ApprovalRequest{Command: "if true; then\n  rm -rf /tmp/build\nfi", Description: "delete in root path"},
			"delete in root path", "rm -rf /tmp/build", 2},
		{"long line",
			ApprovalRequest{Command: "rm -rf /tmp/" + strings.Repeat("a", 100), Description: "delete in root path"},
			"delete in root path", "rm -rf /tmp/" + strings.Repeat("a", 47) + "…", 0},
		{"empty Description",
			ApprovalRequest{Command: "rm -rf /tmp/x"},
			"dangerous command", "rm -rf /tmp/x", 0},
		{"Summary is not used",
			ApprovalRequest{Command: "echo a", Summary: "List the files."},
			"dangerous command", "echo a", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pattern, line, more := tc.req.FallbackParts()
			if pattern != tc.pattern || line != tc.line || more != tc.more {
				t.Fatalf("FallbackParts() = %q, %q, %d, want %q, %q, %d",
					pattern, line, more, tc.pattern, tc.line, tc.more)
			}
		})
	}
}

func TestShowHidden(t *testing.T) {
	for in, want := range map[string]string{
		"rm -rf /tmp/x":         "rm -rf /tmp/x",
		"a\tb\nc":               "a\tb\nc",
		"rm -rf /a\u202eb":      "rm -rf /a<U+202E>b",
		"rm\u200b -rf":          "rm<U+200B> -rf",
		"\x1b[31mred":           "<U+001B>[31mred",
		"echo a\r":              "echo a<U+000D>",
		"\u2066x\u2069 \ufeffy": "<U+2066>x<U+2069> <U+FEFF>y",
	} {
		if got := ShowHidden(in); got != want {
			t.Errorf("ShowHidden(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFallbackPartsShowsHiddenRunes(t *testing.T) {
	_, line, _ := ApprovalRequest{Command: "rm -rf /tmp/\u202ex"}.FallbackParts()
	if line != "rm -rf /tmp/<U+202E>x" {
		t.Fatalf("%q", line)
	}
}
