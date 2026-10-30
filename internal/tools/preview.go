package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// previewMax is the maximum number of characters (runes) in a tool preview.
const previewMax = 40

// PreviewKeys maps a tool name to the argument that ArgPreview shows for the tool. The keys
// come from Hermes build_tool_preview where Hermes has the tool. These keys are different
// from Hermes:
//   - browser_fill shows the selector, not the text. The text can be a secret.
//   - view_image and transcribe_audio show the path, not the question. The path tells which
//     file the tool reads.
//
// delegate_task maps to goal, and ArgPreview adds a rule for its tasks list.
var PreviewKeys = map[string]string{
	"terminal":         "command",
	"web_search":       "query",
	"web_fetch":        "urls",
	"read_file":        "path",
	"write_file":       "path",
	"patch":            "path",
	"browser_navigate": "url",
	"browser_click":    "selector",
	"browser_fill":     "selector",
	"browser_run_js":   "code",
	"execute_code":     "code",
	"browser_cookies":  "action",
	"cronjob":          "action",
	"memory":           "action",
	"generate_image":   "prompt",
	"text_to_speech":   "text",
	"transcribe_audio": "path",
	"view_image":       "path",
	"skill_view":       "name",
	"skill_manage":     "name",
	"clarify":          "question",
	"session_search":   "query",
	"delegate_task":    "goal",
}

// previewFallbackKeys are the keys that ArgPreview tries, in this order, for a tool that is
// not in PreviewKeys or a call that has no value for the key of its tool (Hermes).
var previewFallbackKeys = []string{"query", "text", "command", "path", "name", "prompt", "code", "goal"}

// previewArg is one argument of a tool call, in JSON order.
type previewArg struct {
	key   string
	value json.RawMessage
}

// ArgPreview returns the main argument of a call of tool, on one line and cut to previewMax
// characters. It returns "" when args is not a JSON object or has no argument to show.
func ArgPreview(tool, args string) string {
	list, ok := previewArgs(args)
	if !ok {
		return ""
	}
	if tool == "delegate_task" {
		if p := delegatePreview(list); p != "" {
			return p
		}
	}
	if key, ok := PreviewKeys[tool]; ok {
		if p := valuePreview(argValue(list, key)); p != "" {
			return p
		}
	}
	for _, key := range previewFallbackKeys {
		if p := valuePreview(argValue(list, key)); p != "" {
			return p
		}
	}
	// An MCP tool has no key, so it shows its first string argument. A null value
	// decodes into a string with no error, so check for the quote.
	for _, a := range list {
		var s string
		if len(a.value) > 0 && a.value[0] == '"' && json.Unmarshal(a.value, &s) == nil {
			return cutPreview(oneLine(s), previewMax)
		}
	}
	return ""
}

// previewArgs returns the arguments of the JSON object args in JSON order. It returns false
// when args is not a JSON object. A broken object gives the arguments before the error, so
// the cut arguments of a model still get a preview.
func previewArgs(args string) ([]previewArg, bool) {
	dec := json.NewDecoder(strings.NewReader(args))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false
	}
	var list []previewArg
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		key, _ := tok.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			break
		}
		list = append(list, previewArg{key, v})
	}
	return list, true
}

// argValue returns the value of key in list, or nil. A key that occurs two times gives its
// last value, as encoding/json gives it to the tool.
func argValue(list []previewArg, key string) json.RawMessage {
	var v json.RawMessage
	for _, a := range list {
		if a.key == key {
			v = a.value
		}
	}
	return v
}

// delegatePreview returns the goal of a delegate_task call, the goal of its only task, or
// "N tasks" for a batch. Each child line names its child, so the batch line does not repeat
// the goals.
func delegatePreview(list []previewArg) string {
	if p := stringPreview(argValue(list, "goal")); p != "" {
		return p
	}
	var tasks []json.RawMessage
	if json.Unmarshal(argValue(list, "tasks"), &tasks) != nil {
		return ""
	}
	switch {
	case len(tasks) == 1:
		var task struct {
			Goal json.RawMessage `json:"goal"`
		}
		if json.Unmarshal(tasks[0], &task) != nil {
			return ""
		}
		return stringPreview(task.Goal)
	case len(tasks) >= 2:
		return fmt.Sprintf("%d tasks", len(tasks))
	}
	return ""
}

// valuePreview returns the preview of one argument value. A string gives its text. A list of
// strings gives its first item that is not empty, and "+N" for the N other items. The "+N"
// part is never cut. Any other value gives "".
func valuePreview(v json.RawMessage) string {
	if p := stringPreview(v); p != "" {
		return p
	}
	var items []json.RawMessage
	if json.Unmarshal(v, &items) != nil {
		return ""
	}
	for _, item := range items {
		var s string
		if json.Unmarshal(item, &s) != nil {
			continue
		}
		if s = oneLine(s); s == "" {
			continue
		}
		if len(items) == 1 {
			return cutPreview(s, previewMax)
		}
		more := fmt.Sprintf(" +%d", len(items)-1)
		return cutPreview(s, previewMax-utf8.RuneCountInString(more)) + more
	}
	return ""
}

// stringPreview returns the preview of a string value, or "" for any other value.
func stringPreview(v json.RawMessage) string {
	var s string
	if v == nil || json.Unmarshal(v, &s) != nil {
		return ""
	}
	return cutPreview(oneLine(s), previewMax)
}

// cutPreview cuts s to limit runes. A cut string ends with "...". The cut never splits a rune.
func cutPreview(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	if limit <= 3 {
		return string([]rune(s)[:max(limit, 0)])
	}
	return string([]rune(s)[:limit-3]) + "..."
}
