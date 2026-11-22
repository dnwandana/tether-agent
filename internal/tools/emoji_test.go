package tools

import "testing"

func TestEmoji(t *testing.T) {
	for name, want := range map[string]string{
		"terminal":                "💻",
		"read_file":               "📖",
		"write_file":              "✍️",
		"patch":                   "🔧",
		"web_search":              "🔍",
		"web_fetch":               "📄",
		"session_search":          "🔍",
		"delegate_task":           "🔀",
		"acp_delegate":            "🤖",
		"execute_code":            "🐍",
		"browser_read":            "📸",
		"browser_take_screenshot": "👁️",
		"todo_list":               "📋",
		"mcp_github_search":       "🔧",
		"":                        "🔧",
	} {
		if got := Emoji(name); got != want {
			t.Errorf("Emoji(%q) = %q, want %q", name, got, want)
		}
	}
	if len(toolEmoji) != 26 {
		t.Fatalf("the table has %d entries, want the 26 entries of the Telegram table", len(toolEmoji))
	}
}
