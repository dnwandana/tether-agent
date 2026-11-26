package tools

import (
	"context"
	"strings"

	"github.com/dnwandana/tether-agent/internal/memory"
)

// HeldResultPrefix starts the tool text of a memory write that waits for the owner.
const HeldResultPrefix = "Held for the owner:"

// HeldStore keeps a memory write that waits for the owner. It returns the id of the held
// row.
type HeldStore interface {
	Hold(ctx context.Context, op memory.Op, matched, sessionKey string, sources, hosts []string) (id int64, err error)
}

// HoldSources returns the outside sources of f that make a write wait for the owner. A
// source that starts with "terminal:" does not.
func HoldSources(f OutsideFacts) []string {
	var out []string
	for _, s := range f.Sources {
		if !strings.HasPrefix(s, "terminal:") {
			out = append(out, s)
		}
	}
	return out
}

// normTopic lower-cases topic and turns each run of other runes than a-z and 0-9 into one "-".
func normTopic(topic string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(topic) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// heldTopic reports whether topic names a note that keeps held writes.
func heldTopic(topic string) bool {
	return memory.IsHeldNote(normTopic(topic))
}

// heldNoteErr is the error for a read or a write of a held note.
func heldNoteErr(topic string) error {
	return toolErr("the note %s holds writes that wait for the owner; the agent cannot read or change it", normTopic(topic))
}

// maybeHold holds a write of a chat or review run that read outside content. It returns
// held true with the tool text when it holds the write. It returns an error when the block
// list or the missing store refuses the write.
func (t MemoryTool) maybeHold(ctx context.Context, env *Env, op memory.Op) (out string, held bool, err error) {
	if op.Source != memory.SourceChat && op.Source != memory.SourceReview {
		return "", false, nil
	}
	facts := Outside(env)
	sources := HoldSources(facts)
	if len(sources) == 0 {
		return "", false, nil
	}
	if err := t.Store.CheckBlocked(ctx, op); err != nil {
		return "", false, toolErr("%v", err)
	}
	// The store runs this check only at the write, and a held add or replace goes to a note at
	// the sweep. Refuse the text now, so that the held write can be saved later.
	if separatorLine(op.Content) {
		return "", false, toolErr("content must not have a line that holds only §, because § separates the entries")
	}
	if t.Held == nil {
		return "", false, toolErr("this turn read outside content, and the held-write store is not set; the write is refused")
	}
	matched := ""
	if op.Topic == "" && (op.Action == memory.ActionReplace || op.Action == memory.ActionRemove ||
		(op.Action == memory.ActionMove && op.To == "note")) {
		matched = t.matchedEntry(op)
	}
	op.Outside = sources
	id, err := t.Held.Hold(ctx, op, matched, env.SessionKey, sources, facts.Hosts)
	if err != nil {
		return "", false, toolErr("%v", err)
	}
	AddEdit(env, "held", id)
	out = HeldResultPrefix + " this turn read outside content (" + strings.Join(sources, ", ")
	if len(facts.Hosts) > 0 {
		out += " " + strings.Join(facts.Hosts, ", ")
	}
	out += "). The owner decides with a card. Do not try again, and do not write the text to a file."
	return out, true, nil
}

// matchedEntry returns the one tier-1 entry that op.OldText selects, or "" when none or
// several match.
func (t MemoryTool) matchedEntry(op memory.Op) string {
	entries, err := t.Store.ReadEntries(op.Target)
	if err != nil {
		return ""
	}
	found := ""
	n := 0
	for _, e := range entries {
		if (op.Exact && e == op.OldText) || (!op.Exact && strings.Contains(e, op.OldText)) {
			found = e
			n++
		}
	}
	if n != 1 {
		return ""
	}
	return found
}

// separatorLine reports whether s has a line that holds only "§", the separator of the
// memory entries.
func separatorLine(s string) bool {
	for line := range strings.SplitSeq(s, "\n") {
		if strings.TrimSpace(line) == "§" {
			return true
		}
	}
	return false
}
