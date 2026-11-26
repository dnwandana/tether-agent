package tools

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/dnwandana/tether-agent/internal/memory"
	"github.com/dnwandana/tether-agent/internal/provider"
)

// frozenNote follows a result that changed memory.md, user_profile.md, or rules.md. The
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
	// Quotes checks the quote of a write to the user profile or the rules. A nil
	// value makes a required check refuse the write.
	Quotes QuoteSource
	// Held keeps a write that waits for the owner after outside content. A nil value
	// makes such a write refused.
	Held HeldStore
}

type memoryArgs struct {
	Action  string `json:"action"`
	Target  string `json:"target"`
	Content string `json:"content"`
	OldText string `json:"old_text"`
	Topic   string `json:"topic"`
	Query   string `json:"query"`
	From    int    `json:"from"`
	Quote   string `json:"quote"`
	Until   string `json:"until"`
	To      string `json:"to"`
}

// Name returns "memory".
func (MemoryTool) Name() string { return "memory" }

// ReadOnlyCall reports whether the call only reads the memory: the actions read and search. It
// decodes args with the struct of Run, so it sees the same action.
func (MemoryTool) ReadOnlyCall(args json.RawMessage) bool {
	var a memoryArgs
	if err := decodeArgs(args, &a); err != nil {
		return false
	}
	return a.Action == "read" || a.Action == "search"
}

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
			"- 'memory': your facts: environment facts, project conventions, tool quirks, lessons.\n" +
			"- 'rule': a standing rule of the owner. Save it only when the owner states it in a chat turn, " +
			"and give the exact words of the owner in quote.\n\n" +
			"ACTIONS: add (a new entry), replace (change the entry that old_text finds), " +
			"remove (delete the entry that old_text finds, or the blocked entry with the ID m-<id>), " +
			"move (move the entry that old_text finds to a note with to 'note', or to the file of the target with to 'file'), " +
			"search (find entries by words), read (open a note by its topic).\n\n" +
			"A full rules file refuses an add. The entry never goes to a note. " +
			"For a snapshot that stops being true on a day, give until as YYYY-MM-DD.\n\n" +
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
    "action": {"type": "string", "enum": ["add", "replace", "remove", "move", "search", "read"], "description": "The action to do."},
    "target": {"type": "string", "enum": ["memory", "user", "rule"], "description": "The store: 'memory' for your facts, 'user' for the user profile, 'rule' for the rules of the owner. Required for add, replace, remove, and move."},
    "content": {"type": "string", "description": "The entry text. Required for add and replace."},
    "old_text": {"type": "string", "description": "A short unique part of the entry to replace or remove. For remove, old_text can also be the m-<id> of a [BLOCKED m-<id>: …] line."},
    "topic": {"type": "string", "description": "The note topic: lowercase words joined by '-', for example 'rpc-providers'. For add, the entry goes to this note. For replace and remove, only this note is searched. For read, the note to open."},
    "query": {"type": "string", "description": "The words to find. Required for search."},
    "from": {"type": "integer", "description": "For read: the number of the first entry to show. The default is 1."},
    "quote": {"type": "string", "description": "The exact words of the owner in this chat that the entry rests on. Required for a rule."},
    "until": {"type": "string", "description": "A date YYYY-MM-DD after today. The entry stops loading after this day. Use it for a snapshot."},
    "to": {"type": "string", "enum": ["note", "file"], "description": "For move: 'note' moves the entry to the note of topic, 'file' moves it to the file of target."}
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
	switch target {
	case memory.TargetUser:
		file = "user_profile.md"
	case memory.TargetRule:
		file = "rules.md"
	}
	return "", toolErr("no entry in %s has the ID %s", file, id)
}

// Run searches, reads, or changes the memory.
func (t MemoryTool) Run(ctx context.Context, raw json.RawMessage, env *Env) (string, error) {
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
		if heldTopic(a.Topic) {
			return "", heldNoteErr(a.Topic)
		}
		out, err := t.Store.Read(a.Topic, a.From, t.MaxChars)
		if err != nil {
			return "", toolErr("%v", err)
		}
		return out, nil
	case "add", "replace", "remove", "move":
		source := memory.SourceOwner
		if env != nil {
			var err error
			if source, err = MemorySource(env.Kind); err != nil {
				return "", toolErr("%v", err)
			}
		}
		if heldTopic(a.Topic) {
			return "", heldNoteErr(a.Topic)
		}
		if a.Target == "" {
			return "", toolErr("target is required for %s; use memory, user, or rule", a.Action)
		}
		if a.Action != "remove" && a.Action != "move" {
			if msg := memoryThreat(a.Content, limit); msg != "" {
				return "", toolErr("%s", msg)
			}
		}
		if a.Target == memory.TargetRule && (source == memory.SourceReview || source == memory.SourceCron) {
			return "", toolErr("Owner rules come only from the owner's words in a chat turn.")
		}
		if a.Target == memory.TargetRule && source == memory.SourceTidy && !(a.Action == memory.ActionMove && a.To == "file") {
			return "", toolErr("In a memory tidy, a rule comes only from a move of an owner rule from a note.")
		}
		if a.Action == memory.ActionMove {
			if source == memory.SourceCron {
				return "", toolErr("A cron run cannot move memory entries.")
			}
			if a.To != "note" && a.To != "file" {
				return "", toolErr(`to must be "note" or "file" for move`)
			}
		}
		sessionID := ""
		if env != nil {
			sessionID = env.SessionID
		}
		// Mask keeps the raw text of a blocked entry out of an error text.
		op := memory.Op{Action: a.Action, Target: a.Target, Topic: a.Topic, Content: a.Content, OldText: a.OldText, Mask: mask,
			Source: source, SessionID: sessionID, Quote: a.Quote, Until: a.Until, To: a.To}
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
		if err := t.checkQuote(ctx, env, &op); err != nil {
			return "", err
		}
		if out, held, err := t.maybeHold(ctx, env, op); held || err != nil {
			return out, err
		}
		res, err := t.Store.ApplyContext(ctx, op)
		if err != nil {
			return "", toolErr("%v", err)
		}
		for _, id := range res.EditIDs {
			AddEdit(env, "memory", id)
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
		return "", toolErr("unknown action %q; use add, replace, remove, move, search, or read", a.Action)
	}
}
