package tools

import (
	"errors"

	"github.com/dnwandana/tether-agent/internal/memory"
)

// EditRef is the change of one tool call that a card can undo, or a held write.
// Kind is "memory", "skill", or "held".
type EditRef struct {
	Kind string
	ID   int64
}

// editsKey is the Env.Values key of a []EditRef.
const editsKey = "edits"

// AddEdit adds a change to the edit sink of env. A nil env does nothing.
func AddEdit(env *Env, kind string, id int64) {
	if env == nil {
		return
	}
	valuesMu.Lock()
	defer valuesMu.Unlock()
	if env.Values == nil {
		env.Values = map[string]any{}
	}
	refs, _ := env.Values[editsKey].([]EditRef)
	env.Values[editsKey] = append(refs, EditRef{Kind: kind, ID: id})
}

// Edits returns a copy of the edit sink of env, or nil when the sink is empty.
func Edits(env *Env) []EditRef {
	if env == nil {
		return nil
	}
	valuesMu.Lock()
	defer valuesMu.Unlock()
	refs, _ := env.Values[editsKey].([]EditRef)
	if len(refs) == 0 {
		return nil
	}
	return append([]EditRef(nil), refs...)
}

// ResetEdits empties the edit sink of env.
func ResetEdits(env *Env) {
	if env == nil {
		return
	}
	valuesMu.Lock()
	defer valuesMu.Unlock()
	delete(env.Values, editsKey)
}

// MemorySource maps a run kind to the source of a memory change. A kind that cannot
// write memory returns an error.
func MemorySource(kind string) (string, error) {
	switch kind {
	case "chat":
		return memory.SourceChat, nil
	case "review":
		return memory.SourceReview, nil
	case "tidy":
		return memory.SourceTidy, nil
	case "cron":
		return memory.SourceCron, nil
	}
	return "", errors.New("memory: this run kind cannot write memory")
}
