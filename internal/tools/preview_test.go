package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestArgPreview(t *testing.T) {
	long := "https://hono.dev/docs/getting-started/cloudflare-workers"
	cases := []struct{ tool, args, want string }{
		{"web_fetch", `{"urls":["` + long + `"]}`, "https://hono.dev/docs/getting-started..."},
		{"web_fetch", `{"urls":["` + long + `","https://a.org","https://b.org"]}`, "https://hono.dev/docs/getting-star... +2"},
		{"web_fetch", `{"urls":["https://go.dev","https://x.org"]}`, "https://go.dev +1"},
		{"web_fetch", `{"urls":[]}`, ""},
		{"web_fetch", `{"urls":["","https://go.dev"]}`, "https://go.dev +1"},
		{"web_fetch", `{"max_chars":500,"urls":["https://go.dev"]}`, "https://go.dev"},
		{"web_fetch", `{"urls":"https://go.dev"}`, "https://go.dev"},
		{"web_fetch", `{"urls":null}`, ""},
		{"delegate_task", `{"goal":"Find the Hono version"}`, "Find the Hono version"},
		{"delegate_task", `{"context":"c","goal":"g"}`, "g"},
		{"delegate_task", `{"tasks":[{"goal":"only"}]}`, "only"},
		{"delegate_task", `{"tasks":[{"goal":"a"},{"goal":"b"},{"goal":"c"}]}`, "3 tasks"},
		{"delegate_task", `{"tasks":{"goal":"x"}}`, ""},
		{"browser_fill", `{"text":"hunter2","selector":"#password"}`, "#password"},
		{"mcp_x_search", `{"x":"a","query":"b"}`, "b"},
		{"mcp_x_search", `{"x":"a","y":"b"}`, "a"},
		{"echo", `{"n":3,"command":"ls -la","x":"y"}`, "ls -la"},
		{"echo", `{"path":"` + strings.Repeat("a", 60) + `"}`, strings.Repeat("a", 37) + "..."},
		{"echo", `{"a":"line1\nline2"}`, "line1 line2"},
		{"echo", `{"n":1}`, ""},
		{"echo", `{"a":null,"b":"x"}`, "x"},
		{"echo", `not json`, ""},
		{"terminal", `{"command":"ls -la","timeout":`, "ls -la"},
	}
	for _, c := range cases {
		if got := ArgPreview(c.tool, c.args); got != c.want {
			t.Errorf("ArgPreview(%q, %s) = %q, want %q", c.tool, c.args, got, c.want)
		}
	}
}

func TestArgPreviewLimits(t *testing.T) {
	long := "https://hono.dev/docs/getting-started/cloudflare-workers"
	urls := []string{long}
	for i := 0; i < 12; i++ {
		urls = append(urls, "https://x.org")
	}
	got := ArgPreview("web_fetch", `{"urls":["`+strings.Join(urls, `","`)+`"]}`)
	if !strings.HasSuffix(got, "... +12") || utf8.RuneCountInString(got) != previewMax {
		t.Errorf("13 URLs: got %q (%d runes)", got, utf8.RuneCountInString(got))
	}

	got = ArgPreview("web_fetch", `{"urls":["https://例え.jp/ドキュメント/はじめに/クラウドフレア/ワーカーの設定/詳細","https://x.org"]}`)
	if !strings.HasSuffix(got, "... +1") || utf8.RuneCountInString(got) > previewMax || !utf8.ValidString(got) {
		t.Errorf("multibyte URL: got %q (%d runes)", got, utf8.RuneCountInString(got))
	}
}
