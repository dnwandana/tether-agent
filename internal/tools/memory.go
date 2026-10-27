package tools

import (
	"context"
	"encoding/json"

	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/provider"
)

// frozenNote ends each memory tool result. The system prompt keeps a frozen
// copy of the memory, so a change shows only after the next prompt build.
const frozenNote = "This change shows in the system prompt from the next session or prompt rebuild."

// MemoryTool is the memory tool.
type MemoryTool struct {
	Store *memory.Store
}

type memoryArgs struct {
	Action  string `json:"action"`
	Target  string `json:"target"`
	Content string `json:"content"`
	OldText string `json:"old_text"`
}

// Name returns "memory".
func (MemoryTool) Name() string { return "memory" }

// Toolset returns the memory toolset.
func (MemoryTool) Toolset() string { return SetMemory }

// ParallelSafe returns false, because the tool changes a file.
func (MemoryTool) ParallelSafe() bool { return false }

// Schema returns the tool schema for the model. The text follows Hermes
// tools/memory_tool.py.
func (MemoryTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{
		Name: "memory",
		Description: "Save durable facts to persistent memory. Memory goes into the system prompt of later " +
			"sessions, so keep it short and keep only facts that stay useful.\n\n" +
			"WHEN TO SAVE (do it without a request):\n" +
			"- The user corrects you, or says 'remember this' or 'do not do that again'.\n" +
			"- The user tells you a preference, a habit, or a personal detail (name, role, time zone, coding style).\n" +
			"- You find a fact about the environment (OS, installed tools, project structure).\n" +
			"- You learn a convention, an API quirk, or a workflow of this setup.\n" +
			"- You find a stable fact that is useful again in later sessions.\n\n" +
			"PRIORITY: user preferences and corrections first, then environment facts, then procedures. " +
			"The best memory stops the user from saying the same thing again.\n\n" +
			"Do NOT save task progress, session results, logs of finished work, or temporary TODO state. " +
			"Use session_search to find those in old sessions. When you find a new way to do a task, " +
			"save it as a skill with skill_manage.\n\n" +
			"TWO TARGETS:\n" +
			"- 'user': who the user is: name, role, preferences, communication style, dislikes.\n" +
			"- 'memory': your notes: environment facts, project conventions, tool quirks, lessons.\n\n" +
			"ACTIONS: add (a new entry), replace (change the entry that old_text finds), " +
			"remove (delete the entry that old_text finds). The memory has a character limit. " +
			"When an add goes over the limit, the error lists the entries; merge or remove one first.\n\n" +
			"SKIP: trivial facts, facts that are easy to find again, raw data, and temporary task state.",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "action": {"type": "string", "enum": ["add", "replace", "remove"], "description": "The action to do."},
    "target": {"type": "string", "enum": ["memory", "user"], "description": "The store: 'memory' for your notes, 'user' for the user profile."},
    "content": {"type": "string", "description": "The entry text. Required for add and replace."},
    "old_text": {"type": "string", "description": "A short unique part of the entry to replace or remove."}
  },
  "required": ["action", "target"]
}`),
	}
}

// Run applies the change of the call to the memory files.
func (t MemoryTool) Run(_ context.Context, raw json.RawMessage, _ *Env) (string, error) {
	var a memoryArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if t.Store == nil {
		return "", toolErr("memory is not available in this session")
	}
	sum, err := t.Store.Apply(memory.Op{Action: a.Action, Target: a.Target, Content: a.Content, OldText: a.OldText})
	if err != nil {
		return "", toolErr("%v", err)
	}
	return sum + "\n" + frozenNote, nil
}
