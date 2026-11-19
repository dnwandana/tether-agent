package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
)

// ModeReader is an optional interface of Env.Gate. It returns the /mode value of a scope, or
// "" for guarded, and the query error.
type ModeReader interface {
	ModeCheck(ctx context.Context, scope string) (string, error)
}

// ReadOnlyCaller is an optional interface of a tool with read and write actions. It reports
// whether the call args only read. It decodes args with the same struct as Run.
type ReadOnlyCaller interface {
	ReadOnlyCall(args json.RawMessage) bool
}

// MCPClassed is an optional interface of an MCP tool. It returns the class of the tool: read,
// write, send, or human-only.
type MCPClassed interface {
	MCPClass() string
}

// readOnlyTools are the whole tools that run in read-only mode.
var readOnlyTools = []string{
	"read_file", "web_search", "web_fetch", "view_image", "session_search", "skill_view",
	"skills_list", "clarify", "todo_list", "browser_navigate", "browser_read",
	"browser_take_screenshot", "transcribe_audio", "text_to_speech", "generate_image",
	"delegate_task",
}

// readOnlyDeniedTools are the whole tools that read-only mode hides.
var readOnlyDeniedTools = []string{
	"terminal", "execute_code", "write_file", "patch", "skill_manage", "acp_delegate",
	"browser_click", "browser_fill", "browser_run_js", "request_secret",
}

// readOnlyMixedTools have read and write actions. Each one decides through ReadOnlyCaller.
var readOnlyMixedTools = []string{"memory", "cronjob", "browser_cookies"}

// ReadOnlyDenied returns the sorted names of the whole tools that read-only mode hides.
func ReadOnlyDenied() []string {
	out := slices.Clone(readOnlyDeniedTools)
	slices.Sort(out)
	return out
}

// ReadOnlyAllowed reports whether the call of t with args may run in read-only mode.
func ReadOnlyAllowed(t Tool, args json.RawMessage) bool {
	if m, ok := t.(MCPClassed); ok {
		return m.MCPClass() == "read"
	}
	name := t.Name()
	if slices.Contains(readOnlyMixedTools, name) {
		c, ok := t.(ReadOnlyCaller)
		return ok && c.ReadOnlyCall(args)
	}
	return slices.Contains(readOnlyTools, name)
}

// readOnlyRefusal returns the text for the model when read-only mode refuses a call.
func readOnlyRefusal(t Tool, args json.RawMessage) string {
	part := ""
	if m, ok := t.(MCPClassed); ok {
		part = m.MCPClass()
	} else if slices.Contains(readOnlyMixedTools, t.Name()) {
		// This decode is only for the text. It never decides the call.
		var a struct {
			Action string `json:"action"`
		}
		_ = json.Unmarshal(args, &a)
		part = a.Action
	}
	if part != "" {
		part = " (" + part + ")"
	}
	return fmt.Sprintf("This topic is read-only. The tool %s%s cannot run here. Tell the owner, who can send /mode guarded.", t.Name(), part)
}
