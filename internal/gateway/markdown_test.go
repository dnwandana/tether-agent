package gateway

import "testing"

func TestStripMarkdown(t *testing.T) {
	for in, want := range map[string]string{
		"🧠 **Model: default**":            "🧠 Model: default",
		"__bold__ and _it_":               "bold and it",
		"send `/rename <new title>`.":     "send /rename <new title>.",
		"> Answer in Indonesian.":         "> Answer in Indonesian.",
		"_2 min · step 4/90 · web_fetch_": "2 min · step 4/90 · web_fetch",
		"a snake_case_word stays":         "a snake_case_word stays",
		`**Fix my\_project**`:             "Fix my_project",
		"read /tmp/a_b/c_d.go":            "read /tmp/a_b/c_d.go",
		"2 * 3 * 4":                       "2 * 3 * 4",
		"**unclosed":                      "**unclosed",
		"`a_b_` stays in code":            "a_b_ stays in code",
		"(_note_)":                        "(note)",
	} {
		if got := StripMarkdown(in); got != want {
			t.Errorf("StripMarkdown(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEscapeMarkdownRoundTrip(t *testing.T) {
	title := "my_project *draft* [x]"
	if got := StripMarkdown("**" + EscapeMarkdown(title) + "**"); got != title {
		t.Fatalf("got %q, want %q", got, title)
	}
	for _, s := range []string{"a|b", "c:\\dir\\x", "`code`", "__x__", "~~old~~ plan"} {
		if got := StripMarkdown(EscapeMarkdown(s)); got != s {
			t.Errorf("round trip of %q = %q", s, got)
		}
	}
}

func TestEscapeMarkdownTilde(t *testing.T) {
	if got, want := EscapeMarkdown("~~old~~"), `\~\~old\~\~`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
