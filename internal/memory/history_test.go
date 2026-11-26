package memory

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHistory keeps the rows in memory. It is safe for concurrent use.
type fakeHistory struct {
	mu        sync.Mutex
	edits     []Edit
	blocks    []Block
	cleared   []string
	deletedOf []int64
	recordErr error
	ctxErrs   []error
}

func (f *fakeHistory) Record(ctx context.Context, e Edit) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ctxErrs = append(f.ctxErrs, ctx.Err())
	if f.recordErr != nil {
		return 0, f.recordErr
	}
	e.ID = int64(len(f.edits) + 1)
	f.edits = append(f.edits, e)
	return e.ID, nil
}

func (f *fakeHistory) Get(_ context.Context, id int64) (Edit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id < 1 || int(id) > len(f.edits) {
		return Edit{}, fmt.Errorf("no edit %d", id)
	}
	return f.edits[id-1], nil
}

func (f *fakeHistory) MarkUndone(_ context.Context, id, undoID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits[id-1].UndoneBy = undoID
	return nil
}

func (f *fakeHistory) Recent(_ context.Context, limit int) ([]Edit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := slices.Clone(f.edits)
	slices.Reverse(out)
	return out[:min(limit, len(out))], nil
}

func (f *fakeHistory) Blocked(_ context.Context, norm string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range f.blocks {
		if blockMatch(b.Norm, norm) {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeHistory) AddBlock(_ context.Context, b Block) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blocks = append(f.blocks, b)
	return nil
}

func (f *fakeHistory) ClearBlock(_ context.Context, norm string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared = append(f.cleared, norm)
	f.blocks = slices.DeleteFunc(f.blocks, func(b Block) bool { return b.Norm == norm })
	return nil
}

func (f *fakeHistory) DeleteBlockOf(_ context.Context, editID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletedOf = append(f.deletedOf, editID)
	f.blocks = slices.DeleteFunc(f.blocks, func(b Block) bool { return b.EditID == editID })
	return nil
}

// openHist returns a store with a fake history and a fixed clock.
func openHist(t *testing.T, mem, user, rule int) (*Store, *fakeHistory) {
	t.Helper()
	s, err := Open(t.TempDir(), mem, user, rule)
	if err != nil {
		t.Fatal(err)
	}
	h := &fakeHistory{}
	s.History = h
	s.now = func() time.Time { return time.Date(2026, 11, 26, 10, 0, 0, 0, time.Local) }
	return s, h
}

var stampRe = regexp.MustCompile(`^[0-9a-f]{4}$`)

func TestApplyRecordsAdd(t *testing.T) {
	s, h := openHist(t, 2200, 1375, 1000)
	res, err := s.ApplyContext(context.Background(), Op{Action: "add", Target: TargetMemory, Content: "VPS = hetzner",
		Source: SourceChat, SessionID: "s1", Quote: "my vps is hetzner", Outside: []string{"web_fetch"}})
	if err != nil || !slices.Equal(res.EditIDs, []int64{1}) {
		t.Fatalf("%+v %v", res, err)
	}
	e := h.edits[0]
	if e.Target != "memory" || e.Idx != -1 || e.Action != "add" || e.OldText != "" || e.NewText != "VPS = hetzner" ||
		e.Source != "chat" || e.SessionID != "s1" || e.Quote != "my vps is hetzner" ||
		!slices.Equal(e.Outside, []string{"web_fetch"}) || e.GroupID != 0 || !stampRe.MatchString(e.Stamp) ||
		!e.At.Equal(time.Date(2026, 11, 26, 10, 0, 0, 0, time.Local)) {
		t.Fatalf("%+v", e)
	}
}

func TestApplyRecordsReplaceAndRemove(t *testing.T) {
	s, h := openHist(t, 2200, 1375, 1000)
	apply(t, s, Op{Action: "add", Target: TargetUser, Content: "a"})
	apply(t, s, Op{Action: "add", Target: TargetUser, Content: "VPS = ovh"})
	apply(t, s, Op{Action: "replace", Target: TargetUser, OldText: "ovh", Content: "VPS = hetzner", Source: SourceReview})
	apply(t, s, Op{Action: "remove", Target: TargetUser, OldText: "a", Source: SourceOwner})
	if r := h.edits[2]; r.Action != "replace" || r.Target != "user" || r.Idx != 1 || r.OldText != "VPS = ovh" ||
		r.NewText != "VPS = hetzner" || r.Source != "review" {
		t.Fatalf("%+v", r)
	}
	if r := h.edits[3]; r.Action != "remove" || r.Idx != 0 || r.OldText != "a" || r.NewText != "" || r.Source != "owner" {
		t.Fatalf("%+v", r)
	}
}

func TestApplyRecordsNoteTarget(t *testing.T) {
	s, h := openHist(t, 2200, 1375, 1000)
	apply(t, s, Op{Action: "add", Target: TargetMemory, Topic: "rpc", Content: "Helius RPC"})
	apply(t, s, Op{Action: "replace", Target: TargetMemory, Topic: "rpc", OldText: "Helius", Content: "QuickNode RPC"})
	apply(t, s, Op{Action: "remove", Target: TargetMemory, OldText: "QuickNode"})
	for i, want := range []Edit{
		{Target: "note:rpc", Idx: -1, Action: "add", NewText: "Helius RPC"},
		{Target: "note:rpc", Idx: 0, Action: "replace", OldText: "Helius RPC", NewText: "QuickNode RPC"},
		{Target: "note:rpc", Idx: 0, Action: "remove", OldText: "QuickNode RPC"},
	} {
		g := h.edits[i]
		if g.Target != want.Target || g.Idx != want.Idx || g.Action != want.Action || g.OldText != want.OldText || g.NewText != want.NewText {
			t.Fatalf("%d: %+v", i, g)
		}
	}
}

// A spill add is one row on the inbox note. A spill replace is two rows: the add to the
// note first, then the remove from tier 1 with the group id of the first row.
func TestApplyRecordsSpill(t *testing.T) {
	s, h := openHist(t, 10, 10, 10)
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "abc"})
	res := apply(t, s, Op{Action: "replace", Target: TargetMemory, OldText: "abc", Content: strings.Repeat("z", 11)})
	if !slices.Equal(res.EditIDs, []int64{2, 3}) {
		t.Fatal(res.EditIDs)
	}
	if r := h.edits[1]; r.Action != "add" || r.Target != "note:inbox-memory" || r.NewText != strings.Repeat("z", 11) || r.GroupID != 0 {
		t.Fatalf("%+v", r)
	}
	if r := h.edits[2]; r.Action != "remove" || r.Target != "memory" || r.Idx != 0 || r.OldText != "abc" || r.GroupID != 2 {
		t.Fatalf("%+v", r)
	}
	res = apply(t, s, Op{Action: "add", Target: TargetMemory, Content: strings.Repeat("y", 11)})
	if r := h.edits[3]; len(h.edits) != 4 || r.Target != "note:inbox-memory" || r.Action != "add" || !slices.Equal(res.EditIDs, []int64{4}) {
		t.Fatalf("%+v", h.edits)
	}
}

func TestApplyNoChangeNoRow(t *testing.T) {
	s, h := openHist(t, 2200, 1375, 1000)
	apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "x1"})
	res := apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "x1"})
	if len(h.edits) != 1 || res.EditIDs != nil {
		t.Fatalf("%+v %+v", h.edits, res)
	}
}

func TestApplyNilHistory(t *testing.T) {
	s := open(t)
	res := apply(t, s, Op{Action: "add", Target: TargetMemory, Content: "x1"})
	if res.EditIDs != nil || s.Memory() != "x1" {
		t.Fatalf("%+v", res)
	}
}

// A failed Record keeps the file write, because the file is the source of truth.
func TestRecordFailureKeepsWrite(t *testing.T) {
	s, h := openHist(t, 2200, 1375, 1000)
	h.recordErr = errors.New("db is gone")
	res, err := s.ApplyContext(context.Background(), Op{Action: "add", Target: TargetMemory, Content: "x1"})
	if err != nil || res.EditIDs != nil || s.Memory() != "x1" {
		t.Fatalf("%+v %v", res, err)
	}
}

// Record gets a context with no cancel, so a /stop does not lose the row.
func TestRecordContextHasNoCancel(t *testing.T) {
	s, h := openHist(t, 2200, 1375, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ApplyContext(ctx, Op{Action: "add", Target: TargetMemory, Content: "x1"}); err != nil {
		t.Fatal(err)
	}
	if len(h.ctxErrs) != 1 || h.ctxErrs[0] != nil {
		t.Fatal(h.ctxErrs)
	}
}

// Two stores on one folder share one history, as two processes share state.db. The rows
// are in the order of the file writes.
func TestRowsInWriteOrder(t *testing.T) {
	dir := t.TempDir()
	h := &fakeHistory{}
	a, _ := Open(dir, 100000, 1375, 1000)
	b, _ := Open(dir, 100000, 1375, 1000)
	a.History, b.History = h, h
	var wg sync.WaitGroup
	for i, s := range []*Store{a, b} {
		wg.Go(func() {
			for j := range 20 {
				if _, err := s.Apply(Op{Action: "add", Target: TargetMemory, Content: fmt.Sprintf("s%d entry %d", i, j)}); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	var rows []string
	for _, e := range h.edits {
		rows = append(rows, e.NewText)
	}
	if got := a.Entries(TargetMemory); !slices.Equal(got, rows) {
		t.Fatalf("file %q\nrows %q", got, rows)
	}
}
