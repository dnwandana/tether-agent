package tools

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// todoRaw returns the JSON texts as raw items for TodoStore.Write.
func todoRaw(t *testing.T, items ...string) []json.RawMessage {
	t.Helper()
	out := make([]json.RawMessage, len(items))
	for i, s := range items {
		if !json.Valid([]byte(s)) {
			t.Fatalf("bad JSON in the test: %s", s)
		}
		out[i] = json.RawMessage(s)
	}
	return out
}

// todoIDs returns the ids of items, joined with commas.
func todoIDs(items []TodoItem) string {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	return strings.Join(ids, ",")
}

// newTodoStore returns a store with items written in replace mode.
func newTodoStore(t *testing.T, items ...string) *TodoStore {
	t.Helper()
	s := TodoStoreFor(map[string]any{})
	if err := s.Write(todoRaw(t, items...), false); err != nil {
		t.Fatal(err)
	}
	return s
}

func todoItem(id, status, parent string) string {
	b, _ := json.Marshal(map[string]string{"id": id, "content": "task " + id, "status": status, "parent": parent})
	return string(b)
}

func TestTodoReplaceMovesInProgressBeforePending(t *testing.T) {
	s := newTodoStore(t,
		`{"id":"1","content":"Already done","status":"completed"}`,
		`{"id":"2","content":"Verify freed space","status":"pending"}`,
		`{"id":"3","content":"Move archives to Trash","status":"in_progress"}`)
	items, rev, loaded := s.Items()
	if todoIDs(items) != "1,3,2" || rev != 1 || !loaded {
		t.Fatal(todoIDs(items), rev, loaded)
	}
}

func TestTodoDuplicateIDLastWinsInItsPlace(t *testing.T) {
	s := newTodoStore(t,
		`{"id":"1","content":"First version","status":"pending"}`,
		`{"id":"2","content":"Other task","status":"pending"}`,
		`{"id":"1","content":"Latest version","status":"in_progress"}`)
	items, _, _ := s.Items()
	want := []TodoItem{{ID: "1", Content: "Latest version", Status: "in_progress"}, {ID: "2", Content: "Other task", Status: "pending"}}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("%+v", items)
	}
}

func TestTodoNewItemDefaults(t *testing.T) {
	s := newTodoStore(t, `"not-a-dict"`, `42`, `{"content":"no id","status":"bogus"}`,
		`{"id":7,"content":"  number id  ","status":" In_Progress "}`)
	items, _, _ := s.Items()
	want := []TodoItem{
		{ID: "7", Content: "number id", Status: "in_progress"},
		{ID: "?", Content: "(invalid item)", Status: "pending"},
		{ID: "?", Content: "(invalid item)", Status: "pending"},
		{ID: "?", Content: "no id", Status: "pending"},
	}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("%+v", items)
	}
}

func TestTodoMerge(t *testing.T) {
	s := newTodoStore(t, `{"id":"1","content":"Original","status":"pending"}`)
	if err := s.Write(todoRaw(t, `{"id":"1","status":"completed"}`), true); err != nil {
		t.Fatal(err)
	}
	items, rev, _ := s.Items()
	if len(items) != 1 || items[0].Content != "Original" || items[0].Status != "completed" || rev != 2 {
		t.Fatalf("status-only merge: %+v %d", items, rev)
	}
	// An unknown status, a blank content, and an item with no id change nothing.
	if err := s.Write(todoRaw(t, `{"id":"1","status":"bogus","content":"   "}`, `{"content":"x","status":"pending"}`, `"bad"`), true); err != nil {
		t.Fatal(err)
	}
	if items, rev, _ = s.Items(); items[0].Status != "completed" || items[0].Content != "Original" || len(items) != 1 || rev != 2 {
		t.Fatalf("no-op merge: %+v %d", items, rev)
	}
	if err := s.Write(todoRaw(t, `{"id":"2","content":"Second","status":"pending"}`), true); err != nil {
		t.Fatal(err)
	}
	if items, _, _ = s.Items(); todoIDs(items) != "1,2" {
		t.Fatalf("append: %+v", items)
	}
}

func TestTodoMergeMovesInProgressBeforePending(t *testing.T) {
	s := newTodoStore(t,
		`{"id":"1","content":"Completed","status":"completed"}`,
		`{"id":"2","content":"Verify freed space","status":"pending"}`,
		`{"id":"3","content":"Move archives to Trash","status":"pending"}`)
	if err := s.Write(todoRaw(t, `{"id":"3","status":"in_progress"}`), true); err != nil {
		t.Fatal(err)
	}
	if items, _, _ := s.Items(); todoIDs(items) != "1,3,2" {
		t.Fatal(todoIDs(items))
	}
}

func TestTodoMergeParent(t *testing.T) {
	s := newTodoStore(t, todoItem("a", "pending", ""), todoItem("b", "pending", "a"))
	if err := s.Write(todoRaw(t, `{"id":"b","parent":""}`), true); err != nil {
		t.Fatal(err)
	}
	if items, _, _ := s.Items(); items[1].Parent != "" {
		t.Fatalf("an empty parent must clear: %+v", items)
	}
	if err := s.Write(todoRaw(t, `{"id":"b","parent":"a"}`, todoItem("a2", "pending", "a")), true); err != nil {
		t.Fatal(err)
	}
	items, _, _ := s.Items()
	if items[1].Parent != "a" || items[2] != (TodoItem{ID: "a2", Content: "task a2", Status: "pending", Parent: "a"}) {
		t.Fatalf("%+v", items)
	}
}

func TestTodoParentRules(t *testing.T) {
	if items, _, _ := newTodoStore(t, todoItem("a", "pending", "a")).Items(); items[0].Parent != "" {
		t.Fatal("a self-parent must go")
	}
	if items, _, _ := newTodoStore(t, todoItem("a", "pending", "ghost")).Items(); items[0].Parent != "" {
		t.Fatal("a missing parent must go")
	}
	items, _, _ := newTodoStore(t, todoItem("a", "pending", "b"), todoItem("b", "pending", "a")).Items()
	byID := map[string]TodoItem{}
	for _, it := range items {
		byID[it.ID] = it
	}
	for _, it := range items {
		seen := map[string]bool{}
		for n := it; n.Parent != ""; n = byID[n.Parent] {
			if seen[n.Parent] {
				t.Fatalf("a cycle stays: %+v", items)
			}
			seen[n.ID] = true
		}
	}
	nested, _, _ := newTodoStore(t, todoItem("a", "pending", ""), todoItem("b", "in_progress", ""), todoItem("b1", "pending", "b")).Items()
	if todoIDs(nested) != "a,b,b1" {
		t.Fatal("a nested list keeps its order:", todoIDs(nested))
	}
}

func TestTodoRejectsBlankContent(t *testing.T) {
	s := newTodoStore(t, `{"id":"1","content":"Keep me","status":"pending"}`, `{"id":"2","content":"Also keep me","status":"completed"}`)
	for _, bad := range []string{`{"id":"2","content":"","status":"pending"}`, `{"id":"2","status":"pending"}`, `{"id":"2","content":"   ","status":"pending"}`} {
		err := s.Write(todoRaw(t, `{"id":"1","content":"Updated","status":"pending"}`, bad), false)
		if err == nil || !strings.Contains(err.Error(), "empty or missing content") {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	if err := s.Write(todoRaw(t, `{"id":"3","content":" ","status":"pending"}`), true); err == nil {
		t.Fatal("a merge of a new blank item must fail")
	}
	items, rev, _ := s.Items()
	if todoIDs(items) != "1,2" || items[0].Content != "Keep me" || items[1].Status != "completed" || rev != 1 {
		t.Fatalf("the list must not change: %+v %d", items, rev)
	}
}

func TestTodoLimits(t *testing.T) {
	many := make([]string, 5000)
	for i := range many {
		many[i] = todoItem(strings.Repeat("x", i%7)+string(rune('a'+i%26))+strings.Repeat("y", i/26), "pending", "")
	}
	if items, _, _ := newTodoStore(t, many...).Items(); len(items) != TodoMaxItems {
		t.Fatal(len(items))
	}
	long, _ := json.Marshal(map[string]string{"id": "1", "content": strings.Repeat("A", 50001), "status": "pending"})
	items, _, _ := newTodoStore(t, string(long)).Items()
	if n := utf8.RuneCountInString(items[0].Content); n != TodoMaxContent || !strings.HasSuffix(items[0].Content, "… [truncated]") {
		t.Fatal(n)
	}
	plain, _, _ := newTodoStore(t, `{"id":"1","content":"write the report","status":"in_progress"}`, `{"id":"2","content":"review PR","status":"pending"}`).Items()
	if plain[0].Content != "write the report" || plain[1].Content != "review PR" {
		t.Fatalf("%+v", plain)
	}
}

func TestTodoRevisionAndRestore(t *testing.T) {
	item := `{"id":"1","content":"Task","status":"pending"}`
	s := newTodoStore(t, item)
	if err := s.Write(todoRaw(t, item), false); err != nil {
		t.Fatal(err)
	}
	if _, rev, _ := s.Items(); rev != 1 {
		t.Fatal("the same list must keep the revision:", rev)
	}
	r := TodoStoreFor(map[string]any{})
	if _, _, loaded := r.Items(); loaded {
		t.Fatal("a new store is not loaded")
	}
	r.Restore([]TodoItem{{ID: "1", Content: "Task", Status: "pending"}, {ID: "2", Content: " ", Status: "pending"}}, 7)
	if items, rev, loaded := r.Items(); len(items) != 1 || rev != 7 || !loaded {
		t.Fatal(items, rev, loaded)
	}
	if err := r.Write(todoRaw(t, `{"id":"1","content":"Task","status":"completed"}`), false); err != nil {
		t.Fatal(err)
	}
	r.Restore(nil, 99)
	if items, rev, _ := r.Items(); rev != 8 || len(items) != 1 {
		t.Fatal("a loaded store must keep its list:", items, rev)
	}
	empty := TodoStoreFor(map[string]any{})
	empty.Restore(nil, -3)
	if _, rev, loaded := empty.Items(); rev != 0 || !loaded {
		t.Fatal(rev, loaded)
	}
}

func TestTodoSummary(t *testing.T) {
	done, total := TodoSummary([]TodoItem{{Status: "completed"}, {Status: "cancelled"}, {Status: "pending"}, {Status: "in_progress"}})
	if done != 1 || total != 3 {
		t.Fatal(done, total)
	}
}

func TestTodoLevel(t *testing.T) {
	parents := map[string]string{
		"a": "",
		"b": "a",
		"m": "gone",
		"x": "y",
		"y": "x",
	}
	cases := []struct {
		name   string
		parent string
		limit  int
		want   int
	}{
		{"no parent", "", 5, 0},
		{"one level", "a", 5, 1},
		{"two levels", "b", 5, 2},
		{"a missing parent", "gone", 5, 0},
		{"a parent with a missing parent", "m", 5, 1},
		{"a parent cycle stops at the limit", "x", 4, 4},
		{"a limit of zero", "b", 0, 0},
	}
	for _, c := range cases {
		if got := TodoLevel(parents, c.parent, c.limit); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestTodoStoreForCreatesOnce(t *testing.T) {
	values := map[string]any{}
	env := &Env{Values: values}
	stores := make([]*TodoStore, 32)
	var wg sync.WaitGroup
	for i := range stores {
		wg.Go(func() {
			stores[i] = TodoStoreFor(values)
			readState(env) // another Values writer of the same session
		})
	}
	wg.Wait()
	for _, s := range stores {
		if s == nil || s != stores[0] {
			t.Fatal("TodoStoreFor must return one store for each map")
		}
	}
	if TodoStoreFor(nil) == nil {
		t.Fatal("a nil map gives a new store")
	}
}

// A /todo read during a todo_list write must wait for the write. The race detector needs
// CGO, so the test holds the store mutex like a writer and checks that the read blocks.
func TestTodoReadWaitsForTheWriter(t *testing.T) {
	s := newTodoStore(t, `{"id":"1","content":"Find the flaky test","status":"pending"}`)
	s.mu.Lock() // a writer holds the store
	got := make(chan int, 1)
	go func() {
		items, _, _ := s.Items()
		got <- len(items)
	}()
	select {
	case <-got:
		t.Fatal("Items did not wait for the writer")
	case <-time.After(50 * time.Millisecond):
	}
	s.mu.Unlock()
	select {
	case n := <-got:
		if n != 1 {
			t.Fatal(n)
		}
	case <-time.After(time.Second):
		t.Fatal("Items did not return after the writer")
	}
	// Reads and writes at the same time must not crash the process.
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 200 {
				if i%2 == 0 {
					_ = s.Write(todoRaw(t, todoItem("w", "pending", "")), j%3 == 0)
				} else {
					s.Items()
				}
			}
		})
	}
	wg.Wait()
}

func TestTodoChangeAfterWrite(t *testing.T) {
	s := TodoStoreFor(map[string]any{})
	if before, items, writes := s.Change(); len(before) != 0 || len(items) != 0 || writes != 0 {
		t.Fatal("a new store:", before, items, writes)
	}
	pending := []TodoItem{{ID: "1", Content: "task 1", Status: "pending"}}
	done := []TodoItem{{ID: "1", Content: "task 1", Status: "completed"}}
	if err := s.Write(todoRaw(t, todoItem("1", "pending", "")), false); err != nil {
		t.Fatal(err)
	}
	if before, items, writes := s.Change(); len(before) != 0 || !reflect.DeepEqual(items, pending) || writes != 1 {
		t.Fatal("the first write:", before, items, writes)
	}
	if err := s.Write(todoRaw(t, `{"id":"1","status":"completed"}`), true); err != nil {
		t.Fatal(err)
	}
	before, items, writes := s.Change()
	if !reflect.DeepEqual(before, pending) || !reflect.DeepEqual(items, done) || writes != 2 {
		t.Fatal("a merge write:", before, items, writes)
	}
	// Change returns copies, so a change of the result does not change the store.
	before[0].Status, items[0].Status = "cancelled", "cancelled"
	if before, items, _ := s.Change(); before[0].Status != "pending" || items[0].Status != "completed" {
		t.Fatal("Change must return copies:", before, items)
	}
}

func TestTodoChangeAfterSameWrite(t *testing.T) {
	item := todoItem("1", "pending", "")
	s := newTodoStore(t, item)
	if err := s.Write(todoRaw(t, item), false); err != nil {
		t.Fatal(err)
	}
	want := []TodoItem{{ID: "1", Content: "task 1", Status: "pending"}}
	before, items, writes := s.Change()
	if !reflect.DeepEqual(before, want) || !reflect.DeepEqual(items, want) || writes != 2 {
		t.Fatal("a write with no change still counts:", before, items, writes)
	}
	if _, rev, _ := s.Items(); rev != 1 {
		t.Fatal("the same list must keep the revision:", rev)
	}
}

func TestTodoChangeAfterRestore(t *testing.T) {
	pending := []TodoItem{{ID: "1", Content: "task 1", Status: "pending"}}
	s := TodoStoreFor(map[string]any{})
	s.Restore(pending, 7)
	if before, items, writes := s.Change(); len(before) != 0 || !reflect.DeepEqual(items, pending) || writes != 0 {
		t.Fatal("a restore must not change before or writes:", before, items, writes)
	}
	if err := s.Write(todoRaw(t, todoItem("1", "completed", "")), false); err != nil {
		t.Fatal(err)
	}
	// A restore of a loaded store changes nothing.
	s.Restore(nil, 99)
	before, items, writes := s.Change()
	if !reflect.DeepEqual(before, pending) || len(items) != 1 || items[0].Status != "completed" || writes != 1 {
		t.Fatal("the write after a restore:", before, items, writes)
	}
}

func TestTodoChangeAfterFailedWrite(t *testing.T) {
	s := newTodoStore(t, todoItem("1", "pending", ""))
	if err := s.Write(todoRaw(t, `{"id":"2","content":" "}`), false); err == nil {
		t.Fatal("a blank content must fail the write")
	}
	want := []TodoItem{{ID: "1", Content: "task 1", Status: "pending"}}
	before, items, writes := s.Change()
	if len(before) != 0 || !reflect.DeepEqual(items, want) || writes != 1 {
		t.Fatal("a failed write must change nothing:", before, items, writes)
	}
}
