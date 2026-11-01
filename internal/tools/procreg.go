package tools

import (
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// procKeep is the time that the registry keeps a process after its exit.
const procKeep = time.Hour

// ProcInfo describes one background process of a session.
type ProcInfo struct {
	PID                         int
	SessionID, Command, LogPath string
	StartedAt, EndedAt          time.Time
	Exited                      bool
	ExitCode                    int
}

// ProcRegistry keeps the background processes of each session in memory.
// It is safe for concurrent use.
type ProcRegistry struct {
	mu     sync.Mutex
	procs  map[int]*ProcInfo
	onExit func(sessionID string, pid int)
}

// NewProcRegistry returns an empty registry.
func NewProcRegistry() *ProcRegistry {
	return &ProcRegistry{procs: map[int]*ProcInfo{}}
}

// Add records a started process. It replaces an entry with the same pid,
// because the kernel can reuse a pid.
func (r *ProcRegistry) Add(sessionID string, pid int, command, logPath string, started time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.procs[pid] = &ProcInfo{PID: pid, SessionID: sessionID, Command: command, LogPath: logPath, StartedAt: started}
	r.pruneLocked(started)
}

// Exit records the exit of a process and then calls the OnExit callback. It
// does nothing for an unknown pid or for a process that exited already.
func (r *ProcRegistry) Exit(pid, code int, at time.Time) {
	r.mu.Lock()
	p, ok := r.procs[pid]
	if !ok || p.Exited {
		r.mu.Unlock()
		return
	}
	p.Exited = true
	p.ExitCode = code
	p.EndedAt = at
	sessionID := p.SessionID
	r.pruneLocked(at)
	f := r.onExit
	r.mu.Unlock()
	// The callback runs with no lock, so it can read the registry.
	if f != nil {
		f(sessionID, pid)
	}
}

// Running returns the processes of the session that did not exit, sorted by
// the start time and then by the pid.
func (r *ProcRegistry) Running(sessionID string) []ProcInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []ProcInfo
	for _, p := range r.procs {
		if p.SessionID == sessionID && !p.Exited {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.Before(out[j].StartedAt)
		}
		return out[i].PID < out[j].PID
	})
	return out
}

// Get returns a copy of the entry of pid.
func (r *ProcRegistry) Get(pid int) (ProcInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.procs[pid]
	if !ok {
		return ProcInfo{}, false
	}
	return *p, true
}

// Tail returns the last n bytes of the log of pid. It returns "" when n <= 0,
// when the pid is unknown, when the process has no log, or when the read fails.
func (r *ProcRegistry) Tail(pid, n int) string {
	if n <= 0 {
		return ""
	}
	r.mu.Lock()
	var path string
	if p, ok := r.procs[pid]; ok {
		path = p.LogPath
	}
	r.mu.Unlock()
	if path == "" {
		return ""
	}
	// The file read can be slow, so it runs with no lock.
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	off := max(st.Size()-int64(n), 0)
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(n)))
	if err != nil {
		return ""
	}
	// The cut can split a rune.
	return strings.ToValidUTF8(string(b), "")
}

// OnExit sets the callback that Exit calls. A second call replaces the first
// callback.
func (r *ProcRegistry) OnExit(f func(sessionID string, pid int)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onExit = f
}

// pruneLocked removes each exited entry that ended more than procKeep before
// now. The caller holds r.mu.
func (r *ProcRegistry) pruneLocked(now time.Time) {
	for pid, p := range r.procs {
		if p.Exited && now.Sub(p.EndedAt) > procKeep {
			delete(r.procs, pid)
		}
	}
}
