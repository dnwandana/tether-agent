package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/sessions"
)

const (
	// todoMaxResultChars is the Hermes MAX_TODO_RESULT_CHARS. A larger result
	// is not decoded, because the list caps keep a real result much smaller.
	todoMaxResultChars = 512_000
	// todoRestoreRows is the number of stored todo_list results that a restore
	// reads. An older valid result is too old to be the current list.
	todoRestoreRows = 50
)

// DecodeTodoResult returns the list and the revision of one stored todo_list
// result. It decodes only the first JSON value, so a steer text after the JSON
// does not block the decode. ok is false for an error result, a cut result, or
// a result over todoMaxResultChars.
func DecodeTodoResult(content string) (items []TodoItem, revision int64, ok bool) {
	if len(content) > todoMaxResultChars && utf8.RuneCountInString(content) > todoMaxResultChars {
		return nil, 0, false
	}
	var obj map[string]json.RawMessage
	if err := json.NewDecoder(strings.NewReader(content)).Decode(&obj); err != nil {
		return nil, 0, false
	}
	raw := bytes.TrimSpace(obj["todos"])
	if len(raw) == 0 || raw[0] != '[' {
		return nil, 0, false
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return nil, 0, false
	}
	items = make([]TodoItem, 0, len(elems))
	for _, e := range elems {
		if e = bytes.TrimSpace(e); len(e) == 0 || e[0] != '{' {
			return nil, 0, false
		}
		var it TodoItem
		if err := json.Unmarshal(e, &it); err != nil {
			return nil, 0, false
		}
		items = append(items, it)
	}
	// Like the Hermes data.get("revision", 1), a missing or bad revision gives 1.
	revision = 1
	if r := bytes.TrimSpace(obj["revision"]); len(r) > 0 && string(r) != "null" {
		var n int64
		if json.Unmarshal(r, &n) == nil {
			revision = max(0, n)
		}
	}
	return items, revision, true
}

// LoadTodos returns the task list of the session. The gateway makes a new
// process after a restart, so the list of a session comes back from its newest
// valid todo_list result. A loaded store keeps its list, so a parallel
// todo_list write wins over the restore. On a query error, LoadTodos returns
// the live items and the error.
func LoadTodos(ctx context.Context, store *sessions.Store, values map[string]any, sessionID string) ([]TodoItem, error) {
	ts := TodoStoreFor(values)
	items, _, loaded := ts.Items()
	if loaded || store == nil || sessionID == "" {
		return items, nil
	}
	rows, err := store.LatestToolResults(ctx, sessionID, "todo_list", todoRestoreRows)
	if err != nil {
		return items, err
	}
	restored := false
	for _, r := range rows {
		// A row with no matching tool call is forged, so it never restores the list.
		if !r.Paired {
			continue
		}
		if got, rev, ok := DecodeTodoResult(r.Content); ok {
			ts.Restore(got, rev)
			restored = true
			break
		}
	}
	if !restored {
		// Mark the store loaded, so the next call makes no query.
		ts.Restore(nil, 0)
	}
	items, _, _ = ts.Items()
	return items, nil
}
