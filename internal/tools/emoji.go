package tools

// toolEmoji maps a tool name to the emoji of its status line. The values come
// from the Hermes tool registry (agent/display.py get_tool_emoji). Where
// Tether Agent has a different name for the same tool, the Hermes tool is in
// the comment. Other tools use defaultEmoji.
var toolEmoji = map[string]string{
	"terminal":                "💻",
	"read_file":               "📖",
	"write_file":              "✍️",
	"patch":                   "🔧",
	"web_search":              "🔍",
	"web_fetch":               "📄", // web_extract
	"clarify":                 "❓",
	"memory":                  "🧠",
	"session_search":          "🔍",
	"skills_list":             "📚",
	"skill_view":              "📚",
	"skill_manage":            "📝",
	"delegate_task":           "🔀",
	"acp_delegate":            "🤖", // Tether Agent only
	"cronjob":                 "⏰",
	"execute_code":            "🐍",
	"view_image":              "👁️", // vision_analyze
	"text_to_speech":          "🔊",
	"generate_image":          "🎨",
	"browser_navigate":        "🌐",
	"browser_click":           "👆",
	"browser_fill":            "⌨️", // browser_type
	"browser_read":            "📸",  // browser_snapshot
	"browser_take_screenshot": "👁️", // browser_vision
	"browser_run_js":          "🖥️", // browser_console
	"todo_list":               "📋",  // todo
}

// defaultEmoji is the emoji of a tool that is not in toolEmoji.
const defaultEmoji = "🔧"

// Emoji returns the emoji of the tool name. Telegram and the dashboard use the same table.
func Emoji(name string) string {
	if e, ok := toolEmoji[name]; ok {
		return e
	}
	return defaultEmoji
}
