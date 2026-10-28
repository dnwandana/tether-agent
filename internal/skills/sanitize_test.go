package skills

import "testing"

func TestSanitize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain text", "plain text"},
		{"line1\nline2\tcol", "line1\nline2\tcol"},
		{"\x1b[31mred\x1b[0m", "red"},
		{"\x1b]0;title\x07after", "after"},
		{"\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\", "link"},
		{"bare\x1bescape", "bareescape"},
		{"cr\rreturn", "crreturn"},
		{"c1\u009bcontrol", "c1control"},
		{"del\x7fchar", "delchar"},
		{"unicode ok: é 日本", "unicode ok: é 日本"},
	}
	for _, c := range cases {
		if got := Sanitize(c.in); got != c.want {
			t.Errorf("Sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
