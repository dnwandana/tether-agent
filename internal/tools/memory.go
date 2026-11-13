package tools

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/provider"
)

// frozenNote follows a result that changed memory.md or user_profile.md. The
// system prompt keeps a frozen copy of the memory, so a change shows only after
// the next prompt build.
const frozenNote = "This change shows in the system prompt from the next session or prompt rebuild."

// noteChangeLine follows a result that changed a note.
const noteChangeLine = "A search and a read show this change at once. A new note shows in the topic list of the system prompt from the next session or prompt rebuild."

// MemoryTool is the memory tool.
type MemoryTool struct {
	Store *memory.Store
	// MaxChars caps a read result (context.tool_output_max_chars). Store.Read
	// also caps it at memory.MaxReadChars.
	MaxChars int
}

type memoryArgs struct {
	Action  string `json:"action"`
	Target  string `json:"target"`
	Content string `json:"content"`
	OldText string `json:"old_text"`
	Topic   string `json:"topic"`
	Query   string `json:"query"`
	From    int    `json:"from"`
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
			"- 'memory': your facts: environment facts, project conventions, tool quirks, lessons.\n\n" +
			"ACTIONS: add (a new entry), replace (change the entry that old_text finds), " +
			"remove (delete the entry that old_text finds, or the blocked entry with the ID m-<id>), search (find entries by words), " +
			"read (open a note by its topic).\n\n" +
			"NOTES: memory and user have a character limit. When an add does not fit, the entry goes to " +
			"a memory note (inbox-memory or inbox-user), and the add still succeeds. Give a topic to put " +
			"related facts in one note, for example topic 'rpc' for RPC provider facts. Give no topic for " +
			"a fact that each session needs, such as the name of the user or a correction: a note does " +
			"not load into the prompt. The prompt lists the topics of the notes. Use search to find a " +
			"fact, and read to open a note. Before you add a fact, search the notes for the same subject. " +
			"When a note has an older version, replace it.\n\n" +
			"SKIP: trivial facts, facts that are easy to find again, raw data, and temporary task state.",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "action": {"type": "string", "enum": ["add", "replace", "remove", "search", "read"], "description": "The action to do."},
    "target": {"type": "string", "enum": ["memory", "user"], "description": "The store: 'memory' for your facts, 'user' for the user profile. Required for add, replace, and remove."},
    "content": {"type": "string", "description": "The entry text. Required for add and replace."},
    "old_text": {"type": "string", "description": "A short unique part of the entry to replace or remove. For remove, old_text can also be the m-<id> of a [BLOCKED m-<id>: …] line."},
    "topic": {"type": "string", "description": "The note topic: lowercase words joined by '-', for example 'rpc-providers'. For add, the entry goes to this note. For replace and remove, only this note is searched. For read, the note to open."},
    "query": {"type": "string", "description": "The words to find. Required for search."},
    "from": {"type": "integer", "description": "For read: the number of the first entry to show. The default is 1."}
  },
  "required": ["action"]
}`),
	}
}

// memoryEntryIDRe matches the ID of a blocked entry.
var memoryEntryIDRe = regexp.MustCompile(`^m-[0-9a-f]{8}$`)

// entryByID returns the entry of the target file that has the ID id.
func (t MemoryTool) entryByID(target, id string) (string, error) {
	entries, err := t.Store.ReadEntries(target)
	if err != nil {
		return "", toolErr("%v", err)
	}
	for _, e := range entries {
		if MemoryEntryID(e) == id {
			return e, nil
		}
	}
	file := "memory.md"
	if target == memory.TargetUser {
		file = "user_profile.md"
	}
	return "", toolErr("no entry in %s has the ID %s", file, id)
}

// Run searches, reads, or changes the memory.
func (t MemoryTool) Run(_ context.Context, raw json.RawMessage, _ *Env) (string, error) {
	var a memoryArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if t.Store == nil {
		return "", toolErr("memory is not available in this session")
	}
	limit := t.Store.EntryLimit()
	mask := func(e string) (string, bool) { return MemoryBlockedLineWithin(e, limit) }
	switch a.Action {
	case "search":
		out, err := t.Store.SearchMasked(a.Query, mask)
		if err != nil {
			return "", toolErr("%v", err)
		}
		return out, nil
	case "read":
		out, err := t.Store.Read(a.Topic, a.From, t.MaxChars)
		if err != nil {
			return "", toolErr("%v", err)
		}
		return out, nil
	case "add", "replace", "remove":
		if a.Target == "" {
			return "", toolErr("target is required for %s; use memory or user", a.Action)
		}
		if a.Action != "remove" {
			if msg := memoryThreat(a.Content, limit); msg != "" {
				return "", toolErr("%s", msg)
			}
		}
		// Mask keeps the raw text of a blocked entry out of an error text.
		op := memory.Op{Action: a.Action, Target: a.Target, Topic: a.Topic, Content: a.Content, OldText: a.OldText, Mask: mask}
		if a.Action == "remove" && a.Topic == "" {
			if id := strings.TrimSpace(a.OldText); memoryEntryIDRe.MatchString(id) {
				entry, err := t.entryByID(a.Target, id)
				if err != nil {
					return "", err
				}
				// A blocked entry can be a part of another entry, so the
				// remove matches the whole entry.
				op.OldText, op.Exact = entry, true
			}
		}
		res, err := t.Store.Apply(op)
		if err != nil {
			return "", toolErr("%v", err)
		}
		out := res.Text
		if res.Tier1Changed {
			out += "\n" + frozenNote
		}
		if res.Note != "" {
			out += "\n" + noteChangeLine
		}
		return out, nil
	default:
		return "", toolErr("unknown action %q; use add, replace, remove, search, or read", a.Action)
	}
}
