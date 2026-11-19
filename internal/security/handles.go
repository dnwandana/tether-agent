package security

import (
	"regexp"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

// Handles maps the credentials that a root session saw to stable names. It keeps the values
// in memory only. It is safe for concurrent use.
type Handles struct {
	mu    sync.Mutex
	roots map[string]*handleSet
}

// handleSet holds the handles of one root session.
type handleSet struct {
	byValue map[string]int
	byNum   map[int]string
	order   []string // the values in the order of creation, for Values
}

const (
	// maxHandles is the most handles of one root session.
	maxHandles = 64
	// minHandleRunes is the shortest value that gets a handle.
	minHandleRunes = 8
)

// handleSeq gives the number of each new handle. One counter serves the whole process, so two
// root sessions never use the same name.
var handleSeq atomic.Int64

// handleRef matches $TMP_SECRET_<n> and ${TMP_SECRET_<n>}.
var handleRef = regexp.MustCompile(`\$(?:TMP_SECRET_([0-9]+)\b|\{TMP_SECRET_([0-9]+)\})`)

// NewHandles returns an empty store.
func NewHandles() *Handles {
	return &Handles{roots: map[string]*handleSet{}}
}

// Swap replaces each credential that r finds in s: an exact value with its label, and each
// other value with its handle "$TMP_SECRET_<n>". The same value gets the same handle.
func (h *Handles) Swap(root, s string, r *Redactor) string {
	if r == nil {
		return s
	}
	ms := r.Find(s)
	if len(ms) == 0 {
		return s
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.roots[root]
	// Make the handles from the start to the end, so the numbers follow the text order.
	repl := make([]string, len(ms))
	for i, m := range ms {
		if m.Kind == "exact" {
			if m.Name != "" {
				repl[i] = Label(m.Name)
			}
			continue
		}
		v := s[m.Start:m.End]
		if utf8.RuneCountInString(v) < minHandleRunes {
			continue
		}
		if set != nil {
			if n, ok := set.byValue[v]; ok {
				repl[i] = "$TMP_SECRET_" + strconv.Itoa(n)
				continue
			}
			if len(set.order) >= maxHandles {
				continue
			}
		}
		if set == nil {
			set = &handleSet{byValue: map[string]int{}, byNum: map[int]string{}}
			h.roots[root] = set
		}
		n := int(handleSeq.Add(1))
		set.byValue[v] = n
		set.byNum[n] = v
		set.order = append(set.order, v)
		repl[i] = "$TMP_SECRET_" + strconv.Itoa(n)
	}
	out := s
	for i := range slices.Backward(ms) {
		if repl[i] == "" {
			continue
		}
		out = out[:ms[i].Start] + repl[i] + out[ms[i].End:]
	}
	return out
}

// Env returns "TMP_SECRET_<n>=<value>" and the name for each handle of root that a word of p
// names, and each other handle name in unknown.
func (h *Handles) Env(root string, p Parsed) (env []string, names []string, unknown []string) {
	if !p.Read {
		return nil, nil, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.roots[root]
	seen := map[string]bool{}
	scan := func(w string) {
		for _, m := range handleRef.FindAllStringSubmatch(w, -1) {
			digits := m[1]
			if digits == "" {
				digits = m[2]
			}
			name := "TMP_SECRET_" + digits
			if seen[name] {
				continue
			}
			seen[name] = true
			n, err := strconv.Atoi(digits)
			var v string
			ok := false
			if err == nil && set != nil {
				v, ok = set.byNum[n]
			}
			if ok {
				env = append(env, name+"="+v)
				names = append(names, name)
			} else {
				unknown = append(unknown, name)
			}
		}
	}
	for _, c := range p.Cmds {
		for _, w := range c.Argv {
			scan(w)
		}
		for _, w := range c.Redirs {
			scan(w)
		}
	}
	return env, names, unknown
}

// Values returns the values of the handles of root, in the order of creation.
func (h *Handles) Values(root string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.roots[root]
	if set == nil {
		return nil
	}
	return append([]string(nil), set.order...)
}

// Forget drops the handles of root.
func (h *Handles) Forget(root string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.roots, root)
}
