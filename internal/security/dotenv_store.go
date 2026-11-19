package security

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"sort"
	"sync"
)

// DotEnvStore holds the values of .env. It loads the file again when its size, its mtime, or
// its identity changes, so a value that another process saves arrives with no restart. It is
// safe for concurrent use.
type DotEnvStore struct {
	path     string
	onChange func(name, value string)

	mu   sync.Mutex
	vals map[string]string // replaced on each change, never changed in place
	stat os.FileInfo       // the stat of the last load; nil when the file was missing
}

// NewDotEnvStore returns a store with a copy of initial. It records the stat of path now, so
// the first Refresh with no change does nothing. onChange gets each new or changed value.
func NewDotEnvStore(path string, initial map[string]string, onChange func(name, value string)) *DotEnvStore {
	vals := maps.Clone(initial)
	if vals == nil {
		vals = map[string]string{}
	}
	fi, err := os.Stat(path)
	if err != nil {
		fi = nil
	}
	return &DotEnvStore{path: path, onChange: onChange, vals: vals, stat: fi}
}

// Get returns the value of name.
func (s *DotEnvStore) Get(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.vals[name]
	return v, ok
}

// Snapshot returns the current values. Do not change the map.
func (s *DotEnvStore) Snapshot() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.vals
}

// Set puts name=value into the store, and calls onChange when the value is new or changed.
// It does not write .env. Use SaveDotEnv for that.
func (s *DotEnvStore) Set(name, value string) {
	s.mu.Lock()
	old, had := s.vals[name]
	if had && old == value {
		s.mu.Unlock()
		return
	}
	next := maps.Clone(s.vals)
	next[name] = value
	s.vals = next
	s.mu.Unlock()
	if s.onChange != nil {
		s.onChange(name, value)
	}
}

// sameStat reports whether a and b describe the same file with the same size and mtime.
// nil means the file was missing.
func sameStat(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// Refresh loads .env again when its size, its mtime, or its identity changed, and reports
// whether a value is new, changed, or removed.
func (s *DotEnvStore) Refresh() bool {
	fi, err := os.Stat(s.path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		fi = nil
	}
	s.mu.Lock()
	if sameStat(s.stat, fi) {
		s.mu.Unlock()
		return false
	}
	next, err := LoadDotEnv(s.path) // a missing file gives an empty map
	if err != nil {
		s.mu.Unlock()
		return false
	}
	var changed []string
	for n, v := range next {
		if old, ok := s.vals[n]; !ok || old != v {
			changed = append(changed, n)
		}
	}
	sort.Strings(changed)
	removed := false
	for n := range s.vals {
		if _, ok := next[n]; !ok {
			removed = true
			break
		}
	}
	s.vals, s.stat = next, fi
	s.mu.Unlock()
	if s.onChange != nil {
		for _, n := range changed {
			s.onChange(n, next[n])
		}
	}
	return len(changed) > 0 || removed
}
