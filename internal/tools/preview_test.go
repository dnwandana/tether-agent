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
		{"todo_list", `{}`, "reading task list"},
		{"todo_list", `{"todos":null,"merge":true}`, "reading task list"},
		{"todo_list", `{"todos":[{"id":"1"},{"id":"2"},{"id":"3"}]}`, "planning 3 task(s)"},
		{"todo_list", `{"todos":[{"id":"1"}],"merge":true}`, "updating 1 task(s)"},
		{"todo_list", `{"todos":[],"merge":false}`, "planning 0 task(s)"},
		{"todo_list", `{"todos":"[{\"id\":\"1\"},{\"id\":\"2\"}]"}`, "planning 2 task(s)"},
		{"todo_list", `{"todos":42}`, "planning 0 task(s)"},
		{"todo_list", `{"todos":[{"id":"1"}],"merge":`, "planning 1 task(s)"},
	}
	for _, c := range cases {
		if got := ArgPreview(c.tool, c.args, 0); got != c.want {
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
	got := ArgPreview("web_fetch", `{"urls":["`+strings.Join(urls, `","`)+`"]}`, 0)
	if !strings.HasSuffix(got, "... +12") || utf8.RuneCountInString(got) != previewMax {
		t.Errorf("13 URLs: got %q (%d runes)", got, utf8.RuneCountInString(got))
	}

	got = ArgPreview("web_fetch", `{"urls":["https://例え.jp/ドキュメント/はじめに/クラウドフレア/ワーカーの設定/詳細","https://x.org"]}`, 0)
	if !strings.HasSuffix(got, "... +1") || utf8.RuneCountInString(got) > previewMax || !utf8.ValidString(got) {
		t.Errorf("multibyte URL: got %q (%d runes)", got, utf8.RuneCountInString(got))
	}
}

func TestArgPreviewLimit(t *testing.T) {
	long := "https://hono.dev/docs/getting-started/cloudflare-workers"
	cases := []struct {
		tool, args string
		limit      int
		want       string
	}{
		{"web_fetch", `{"urls":["` + long + `"]}`, 20, "https://hono.dev/..."},
		{"web_fetch", `{"urls":["` + long + `","https://a.org"]}`, 20, "https://hono.d... +1"},
		{"delegate_task", `{"goal":"Find the Hono version"}`, 10, "Find th..."},
		{"delegate_task", `{"tasks":[{"goal":"Find the Hono version"}]}`, 10, "Find th..."},
		{"mcp_x_search", `{"x":"abcdefghijkl"}`, 8, "abcde..."},
		{"echo", `{"path":"` + strings.Repeat("a", 60) + `"}`, 0, strings.Repeat("a", 37) + "..."},
		{"echo", `{"path":"` + strings.Repeat("a", 60) + `"}`, -5, strings.Repeat("a", 37) + "..."},
		{"echo", `{"path":"` + strings.Repeat("a", 60) + `"}`, 50, strings.Repeat("a", 47) + "..."},
		{"todo_list", `{"todos":[{"id":"1"}]}`, 4, "planning 1 task(s)"},
	}
	for _, c := range cases {
		if got := ArgPreview(c.tool, c.args, c.limit); got != c.want {
			t.Errorf("ArgPreview(%q, %s, %d) = %q, want %q", c.tool, c.args, c.limit, got, c.want)
		}
	}
}
