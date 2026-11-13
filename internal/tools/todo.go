package tools

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// TodoItem is one item of the task list of a session. The list position is the
// priority, and Parent nests a subtask under another item.
type TodoItem struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"`
	Parent  string `json:"parent,omitempty"`
}

// The caps keep the list small, because the list comes back after each
// compaction and a large list would undo the compaction.
const (
	TodoMaxItems   = 256
	TodoMaxContent = 4000
)

// TodoLimits holds the limits of one todo store. A zero field uses TodoMaxItems
// or TodoMaxContent.
type TodoLimits struct {
	MaxItems int
	MaxChars int
}

const (
	todoPending    = "pending"
	todoInProgress = "in_progress"
	todoCompleted  = "completed"
	todoCancelled  = "cancelled"

	// todoStoreKey is the Env.Values key of the per-session TodoStore.
	todoStoreKey = "todo_store"
	// todoTruncated ends a content that capTodo cut. It is 13 runes.
	todoTruncated = "… [truncated]"
)

// todoStatusValid reports whether s is one of the four statuses.
func todoStatusValid(s string) bool {
	switch s {
	case todoPending, todoInProgress, todoCompleted, todoCancelled:
		return true
	}
	return false
}

// TodoStore is the task list of one session. It has its own mutex, because a
// /todo read can come while a todo_list call of the turn writes the list.
type TodoStore struct {
	mu       sync.Mutex
	items    []TodoItem
	revision int64
	loaded   bool
	// before is the list just before the last successful Write. A caller reads
	// it after a call, because a restore inside the call can fill an empty store.
	before []TodoItem
	// writes counts the successful Write calls. revision cannot do this job,
	// because a Restore sets revision from history and makes no Write.
	writes int64
	// lim holds the limits of the store. The mutex guards it.
	lim TodoLimits
}

// maxItems returns the item count limit. The caller holds s.mu.
func (s *TodoStore) maxItems() int { return positiveOr(s.lim.MaxItems, TodoMaxItems) }

// maxChars returns the item length limit. The caller holds s.mu.
func (s *TodoStore) maxChars() int { return positiveOr(s.lim.MaxChars, TodoMaxContent) }

// TodoStoreFor returns the TodoStore of the session that owns values, and
// creates it on the first call. It holds valuesMu only for the lookup, so a
// /todo read never blocks the other tools of the session. A nil map gives a
// new store that is not kept.
func TodoStoreFor(values map[string]any) *TodoStore {
	return TodoStoreWith(values, TodoLimits{})
}

// TodoStoreWith returns the TodoStore of values like TodoStoreFor does. It
// then sets each limit of lim that is above 0. A zero field keeps the limit
// that the store has.
func TodoStoreWith(values map[string]any, lim TodoLimits) *TodoStore {
	var s *TodoStore
	if values == nil {
		s = &TodoStore{}
	} else {
		valuesMu.Lock()
		var ok bool
		s, ok = values[todoStoreKey].(*TodoStore)
		if !ok {
			s = &TodoStore{}
			values[todoStoreKey] = s
		}
		valuesMu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if lim.MaxItems > 0 {
		s.lim.MaxItems = lim.MaxItems
	}
	if lim.MaxChars > 0 {
		s.lim.MaxChars = lim.MaxChars
	}
	return s
}

// Items returns a copy of the list, so the caller can read it without the lock.
// loaded is false until the first successful Write or Restore, so a caller
// knows when to restore the list from history.
func (s *TodoStore) Items() (items []TodoItem, revision int64, loaded bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.items), s.revision, s.loaded
}

// Change returns copies of the list before the last successful Write, the list now,
// and the number of successful Write calls. A caller compares writes before and
// after a call to know if the call wrote the list.
func (s *TodoStore) Change() (before, items []TodoItem, writes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.before), slices.Clone(s.items), s.writes
}

// Write replaces the list, or merges raw into it by id when merge is true. On
// an error the list and the revision stay as they were, so no partial write
// happens. The revision goes up only when the list changes.
func (s *TodoStore) Write(raw []json.RawMessage, merge bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := make([]todoInput, len(raw))
	for i, r := range raw {
		in[i] = parseTodoInput(r)
	}
	var (
		next []TodoItem
		err  error
	)
	if merge {
		next, err = mergeTodos(s.items, in, s.maxChars())
	} else {
		next, err = freshTodos(in, false, s.maxChars())
	}
	if err != nil {
		return err
	}
	if len(next) > s.maxItems() {
		next = next[:s.maxItems()]
	}
	sanitizeTodoParents(next)
	if !slices.Equal(next, s.items) {
		s.revision++
	}
	// A write with no change also counts, so the caller sees that the call wrote.
	s.before = s.items
	s.writes++
	s.items = next
	s.loaded = true
	return nil
}

// Restore sets a trusted list from history and keeps its revision. It skips an
// item with a blank content, because an old result must not fail the restore.
// A loaded store keeps its list, so a restore never overwrites a newer write.
func (s *TodoStore) Restore(items []TodoItem, revision int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return
	}
	in := make([]todoInput, 0, len(items))
	for _, it := range items {
		b, err := json.Marshal(it)
		if err != nil {
			continue
		}
		in = append(in, parseTodoInput(b))
	}
	next, _ := freshTodos(in, true, s.maxChars())
	if len(next) > s.maxItems() {
		next = next[:s.maxItems()]
	}
	sanitizeTodoParents(next)
	s.items = next
	s.revision = max(0, revision)
	s.loaded = true
}

// TodoSummary returns the number of completed items and the number of items
// that count. A cancelled item does not count, because nobody will do it.
func TodoSummary(items []TodoItem) (done, total int) {
	for _, it := range items {
		switch it.Status {
		case todoCancelled:
			continue
		case todoCompleted:
			done++
		}
		total++
	}
	return done, total
}

// TodoLevel returns the number of parents above an item. The /todo reply and the
// Telegram plan text indent a subtask by this level. It stops after limit steps, so a
// parent cycle cannot loop.
func TodoLevel(parents map[string]string, parent string, limit int) int {
	level := 0
	for parent != "" && level < limit {
		next, ok := parents[parent]
		if !ok {
			break
		}
		level++
		parent = next
	}
	return level
}

// todoInput is one raw item of a write. obj is nil when the raw value is not a
// JSON object.
type todoInput struct {
	obj map[string]json.RawMessage
}

// parseTodoInput decodes raw. A value that is not a JSON object gives an input
// with no fields, which validateTodo turns into the "(invalid item)" item.
func parseTodoInput(raw json.RawMessage) todoInput {
	b := bytes.TrimSpace(raw)
	if len(b) == 0 || b[0] != '{' {
		return todoInput{}
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(b, &obj); err != nil || obj == nil {
		return todoInput{}
	}
	return todoInput{obj: obj}
}

// field returns the text of the field name and whether the key is present.
func (in todoInput) field(name string) (string, bool) {
	v, ok := in.obj[name]
	if !ok {
		return "", false
	}
	return todoText(v), true
}

// todoText returns the text of a JSON value. A model can send a number or a
// boolean for a text field, so those give their JSON text. Other values give "".
func todoText(v json.RawMessage) string {
	b := bytes.TrimSpace(v)
	if len(b) == 0 {
		return ""
	}
	switch c := b[0]; {
	case c == '"':
		var s string
		if json.Unmarshal(b, &s) != nil {
			return ""
		}
		return s
	case c == '-' || (c >= '0' && c <= '9'):
		if _, err := strconv.ParseFloat(string(b), 64); err != nil {
			return ""
		}
		return string(b)
	case string(b) == "true" || string(b) == "false":
		return string(b)
	}
	return ""
}

// validateTodo makes a new item from in. The model sees the error text, so the
// text says how to fix the item.
func validateTodo(in todoInput, maxChars int) (TodoItem, error) {
	if in.obj == nil {
		return TodoItem{ID: "?", Content: "(invalid item)", Status: todoPending}, nil
	}
	id, _ := in.field("id")
	id = strings.TrimSpace(id)
	if id == "" {
		id = "?"
	}
	content, _ := in.field("content")
	content = strings.TrimSpace(content)
	if content == "" {
		return TodoItem{}, toolErr("Todo item '%s' has empty or missing content. Each item must have a non-empty description.", id)
	}
	status, ok := in.field("status")
	if !ok {
		status = todoPending
	}
	status = strings.ToLower(strings.TrimSpace(status))
	if !todoStatusValid(status) {
		status = todoPending
	}
	it := TodoItem{ID: id, Content: capTodo(content, maxChars), Status: status}
	parent, _ := in.field("parent")
	if parent = strings.TrimSpace(parent); parent != "" && parent != id {
		it.Parent = parent
	}
	return it, nil
}

// capTodo keeps the head of a long content, because the head is the part that
// says what to do. The cut never splits a rune.
func capTodo(content string, max int) string {
	if utf8.RuneCountInString(content) <= max {
		return content
	}
	keep := max - utf8.RuneCountInString(todoTruncated)
	n := 0
	for i := range content {
		if n == keep {
			return content[:i] + todoTruncated
		}
		n++
	}
	return content
}

// dedupeTodos keeps the last item of each id, in the place of that last item.
// A value that is not an object has its own key, so two of them stay two items.
func dedupeTodos(in []todoInput) []todoInput {
	last := map[string]int{}
	for i, it := range in {
		key := "\x00invalid" + strconv.Itoa(i)
		if it.obj != nil {
			id, _ := it.field("id")
			if key = strings.TrimSpace(id); key == "" {
				key = "?"
			}
		}
		last[key] = i
	}
	keep := make([]int, 0, len(last))
	for _, i := range last {
		keep = append(keep, i)
	}
	slices.Sort(keep)
	out := make([]todoInput, len(keep))
	for j, i := range keep {
		out[j] = in[i]
	}
	return out
}

// orderTodos moves the first in_progress item to the place of the first
// pending item before it, so the current step leads the list. A nested list
// keeps its order, because a move would take a subtask from its siblings.
func orderTodos(items []TodoItem) []TodoItem {
	active := -1
	for i, it := range items {
		if it.Parent != "" {
			return items
		}
		if active < 0 && it.Status == todoInProgress {
			active = i
		}
	}
	if active < 0 {
		return items
	}
	pending := slices.IndexFunc(items[:active], func(it TodoItem) bool { return it.Status == todoPending })
	if pending < 0 {
		return items
	}
	moved := items[active]
	copy(items[pending+1:active+1], items[pending:active])
	items[pending] = moved
	return items
}

// freshTodos makes a whole new list with the replace rules. When skipBlank is
// true, an item with a blank content is left out instead of an error.
func freshTodos(in []todoInput, skipBlank bool, maxChars int) ([]TodoItem, error) {
	deduped := dedupeTodos(in)
	items := make([]TodoItem, 0, len(deduped))
	for _, d := range deduped {
		it, err := validateTodo(d, maxChars)
		if err != nil {
			if skipBlank {
				continue
			}
			return nil, err
		}
		items = append(items, it)
	}
	return orderTodos(items), nil
}

// mergeTodos changes only the given fields of the known items and adds the
// unknown items at the end. It returns a new slice and leaves cur as it was.
func mergeTodos(cur []TodoItem, in []todoInput, maxChars int) ([]TodoItem, error) {
	items := slices.Clone(cur)
	existing := make(map[string]int, len(items))
	for i, it := range items {
		existing[it.ID] = i
	}
	for _, d := range dedupeTodos(in) {
		if d.obj == nil {
			continue
		}
		id, _ := d.field("id")
		if id = strings.TrimSpace(id); id == "" {
			continue // a merge needs an id
		}
		i, ok := existing[id]
		if !ok {
			it, err := validateTodo(d, maxChars)
			if err != nil {
				return nil, err
			}
			existing[it.ID] = len(items)
			items = append(items, it)
			continue
		}
		if content, _ := d.field("content"); strings.TrimSpace(content) != "" {
			items[i].Content = capTodo(strings.TrimSpace(content), maxChars)
		}
		if status, _ := d.field("status"); todoStatusValid(strings.ToLower(strings.TrimSpace(status))) {
			items[i].Status = strings.ToLower(strings.TrimSpace(status))
		}
		if parent, ok := d.field("parent"); ok {
			items[i].Parent = strings.TrimSpace(parent)
		}
	}
	// Keep one item for each id, at its first place.
	seen := make(map[string]bool, len(items))
	rebuilt := make([]TodoItem, 0, len(items))
	for _, it := range items {
		if seen[it.ID] {
			continue
		}
		seen[it.ID] = true
		rebuilt = append(rebuilt, items[existing[it.ID]])
	}
	return orderTodos(rebuilt), nil
}

// sanitizeTodoParents drops each parent that names no item and breaks each
// parent cycle, so each item reaches a root. The changed items become roots.
func sanitizeTodoParents(items []TodoItem) {
	byID := make(map[string]int, len(items))
	for i, it := range items {
		byID[it.ID] = i
	}
	for i := range items {
		if p := items[i].Parent; p != "" {
			if _, ok := byID[p]; !ok {
				items[i].Parent = ""
			}
		}
	}
	for i := range items {
		seen := map[string]bool{items[i].ID: true}
		node := i
		for items[node].Parent != "" {
			p := items[node].Parent
			if seen[p] {
				items[i].Parent = ""
				break
			}
			seen[p] = true
			node = byID[p]
		}
	}
}
