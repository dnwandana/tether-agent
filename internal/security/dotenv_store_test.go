package security

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

// envRecorder records the onChange calls.
type envRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *envRecorder) on(name, value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name+"="+value)
}

func (r *envRecorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

func TestDotEnvStoreRefresh(t *testing.T) {
	p := writeDotEnv(t, "A=value-1111\n")
	initial, err := LoadDotEnv(p)
	if err != nil {
		t.Fatal(err)
	}
	rec := &envRecorder{}
	s := NewDotEnvStore(p, initial, rec.on)
	if s.Refresh() {
		t.Fatal("no change must give false")
	}
	if v, ok := s.Get("A"); !ok || v != "value-1111" {
		t.Fatal(v, ok)
	}
	old := s.Snapshot()
	// Another process saves a new name.
	if err := SaveDotEnv(p, "B", "second-value-0002"); err != nil {
		t.Fatal(err)
	}
	if !s.Refresh() {
		t.Fatal("a save by another process must reload")
	}
	if v, _ := s.Get("B"); v != "second-value-0002" {
		t.Fatal(v)
	}
	if _, ok := old["B"]; ok {
		t.Fatal("an old snapshot must not change")
	}
	if s.Refresh() {
		t.Fatal("the second refresh has no change")
	}
	// A value with the same length: the rename gives a new file.
	if err := SaveDotEnv(p, "A", "value-2222"); err != nil {
		t.Fatal(err)
	}
	if !s.Refresh() {
		t.Fatal("a new file with the same size must reload")
	}
	if v, _ := s.Get("A"); v != "value-2222" {
		t.Fatal(v)
	}
	// A removed name goes from the store, and onChange is not called for it.
	if err := os.WriteFile(p, []byte("A=value-2222\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !s.Refresh() {
		t.Fatal("a removed name must reload")
	}
	if _, ok := s.Get("B"); ok {
		t.Fatal("B must be gone")
	}
	if want := []string{"B=second-value-0002", "A=value-2222"}; !slices.Equal(rec.got(), want) {
		t.Fatalf("onChange calls %v, want %v", rec.got(), want)
	}
}

func TestDotEnvStoreBadFileKeepsValues(t *testing.T) {
	p := writeDotEnv(t, "A=value-1111\n")
	s := NewDotEnvStore(p, map[string]string{"A": "value-1111"}, nil)
	if err := os.WriteFile(p, []byte("A=value-1111\nB=value-3333\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if s.Refresh() {
		t.Fatal("a file with mode 0644 must not load")
	}
	if v, _ := s.Get("A"); v != "value-1111" {
		t.Fatal(v)
	}
	// chmod changes no size and no mtime. The store tries again, because it did not record the stat.
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	if !s.Refresh() {
		t.Fatal("the fixed file must load")
	}
	if v, _ := s.Get("B"); v != "value-3333" {
		t.Fatal(v)
	}
}

func TestDotEnvStoreMissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	s := NewDotEnvStore(p, nil, nil)
	if s.Refresh() {
		t.Fatal("a missing file that stays missing has no change")
	}
	if err := os.WriteFile(p, []byte("A=value-1111\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !s.Refresh() {
		t.Fatal("a new file must load")
	}
	if v, _ := s.Get("A"); v != "value-1111" {
		t.Fatal(v)
	}
}

func TestDotEnvStoreSet(t *testing.T) {
	init := map[string]string{"A": "1"}
	rec := &envRecorder{}
	s := NewDotEnvStore(filepath.Join(t.TempDir(), ".env"), init, rec.on)
	init["A"] = "changed by the caller"
	snap := s.Snapshot()
	s.Set("B", "value-b-0001")
	s.Set("B", "value-b-0001") // the same value: no second call
	if _, ok := snap["B"]; ok {
		t.Fatal("an old snapshot must not change")
	}
	if v, _ := s.Get("A"); v != "1" {
		t.Fatal("the store must copy the initial map:", v)
	}
	if v, _ := s.Get("B"); v != "value-b-0001" {
		t.Fatal(v)
	}
	if want := []string{"B=value-b-0001"}; !slices.Equal(rec.got(), want) {
		t.Fatalf("onChange calls %v, want %v", rec.got(), want)
	}
}

// Run with -race.
func TestDotEnvStoreConcurrent(t *testing.T) {
	p := writeDotEnv(t, "A=value-1111\n")
	s := NewDotEnvStore(p, nil, func(string, string) {})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			s.Refresh()
			s.Set("K", "v")
			_, _ = s.Get("A")
			_ = len(s.Snapshot())
		})
	}
	wg.Wait()
}
