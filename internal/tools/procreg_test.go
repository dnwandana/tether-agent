package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProcRegistryAddExitAndPrune(t *testing.T) {
	r := NewProcRegistry()
	t0 := time.Unix(1_800_000_000, 0)
	var mu sync.Mutex
	var exits []string
	r.OnExit(func(sessionID string, pid int) {
		mu.Lock()
		defer mu.Unlock()
		exits = append(exits, fmt.Sprintf("%s:%d", sessionID, pid))
		_ = r.Running(sessionID) // the callback can read the registry
	})
	r.Add("s1", 101, "make build", "/tmp/bg-101.log", t0)
	r.Add("s1", 102, "sleep 60", "/tmp/bg-102.log", t0.Add(time.Second))
	r.Add("s2", 201, "npm start", "", t0)

	run := r.Running("s1")
	if len(run) != 2 || run[0].PID != 101 || run[1].PID != 102 {
		t.Fatalf("%+v", run)
	}
	if run[0] != (ProcInfo{PID: 101, SessionID: "s1", Command: "make build", LogPath: "/tmp/bg-101.log", StartedAt: t0}) {
		t.Fatalf("%+v", run[0])
	}
	r.Exit(101, 2, t0.Add(time.Minute))
	if run := r.Running("s1"); len(run) != 1 || run[0].PID != 102 {
		t.Fatalf("an exited process is not running: %+v", run)
	}
	p, ok := r.Get(101)
	if !ok || !p.Exited || p.ExitCode != 2 || !p.EndedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("%+v %v", p, ok)
	}
	r.Exit(999, 0, t0.Add(time.Minute)) // an unknown pid calls no callback
	mu.Lock()
	if len(exits) != 1 || exits[0] != "s1:101" {
		t.Fatal(exits)
	}
	mu.Unlock()

	r.Add("s1", 103, "true", "", t0.Add(time.Minute+time.Hour+time.Second))
	if _, ok := r.Get(101); ok {
		t.Fatal("a process that ended more than 1 h ago is removed")
	}
	if _, ok := r.Get(102); !ok {
		t.Fatal("a running process is never removed")
	}
}

func TestProcRegistryTail(t *testing.T) {
	log := filepath.Join(t.TempDir(), "bg-7.log")
	if err := os.WriteFile(log, []byte("line1\nline2\nEND"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewProcRegistry()
	r.Add("s1", 7, "make", log, time.Now())
	r.Add("s1", 8, "make", filepath.Join(t.TempDir(), "missing.log"), time.Now())
	if got := r.Tail(7, 3); got != "END" {
		t.Fatalf("%q", got)
	}
	if got := r.Tail(7, 1000); got != "line1\nline2\nEND" {
		t.Fatalf("%q", got)
	}
	if r.Tail(8, 10) != "" || r.Tail(9, 10) != "" || r.Tail(7, 0) != "" {
		t.Fatal("a missing log, an unknown pid, and n = 0 give no text")
	}
}

func TestBackgroundTerminalCallRegisters(t *testing.T) {
	procs := NewProcRegistry()
	exited := make(chan int, 1)
	procs.OnExit(func(sessionID string, pid int) {
		if sessionID == "s1" {
			exited <- pid
		}
	})
	tool := &TerminalTool{DefaultTimeout: time.Minute, SpillDir: t.TempDir(), Procs: procs}
	env := newEnv(t)
	env.Gate = allowAll{}
	env.SessionID = "s1"
	out := runToolEnv(t, tool, `{"command":"sleep 0.3; exit 3","background":true}`, env)
	var r struct {
		PID     int    `json:"pid"`
		LogPath string `json:"log_path"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err, out)
	}
	p, ok := procs.Get(r.PID)
	if !ok || p.SessionID != "s1" || p.Command != "sleep 0.3; exit 3" || p.LogPath != r.LogPath || p.StartedAt.IsZero() || p.Exited {
		t.Fatalf("%+v %v", p, ok)
	}
	if run := procs.Running("s1"); len(run) != 1 || run[0].PID != r.PID {
		t.Fatalf("%+v", run)
	}
	select {
	case pid := <-exited:
		if pid != r.PID {
			t.Fatal(pid, r.PID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the exit callback did not run")
	}
	if p, _ := procs.Get(r.PID); !p.Exited || p.ExitCode != 3 || p.EndedAt.IsZero() {
		t.Fatalf("%+v", p)
	}
}

func TestScriptBackgroundTerminalCallRegisters(t *testing.T) {
	pythonOrSkip(t)
	procs := NewProcRegistry()
	reg := regWith(&TerminalTool{DefaultTimeout: 10 * time.Second, SpillDir: t.TempDir(), Procs: procs})
	src := `from tether_tools import terminal
print(terminal("sleep 0.3", background=True))`
	out := runToolEnv(t, scriptTool(t, reg), code(src), visEnv("terminal"))
	var r struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &r); err != nil {
		t.Fatal(err, out)
	}
	if p, ok := procs.Get(r.PID); !ok || p.SessionID != "s1" || p.Command != "sleep 0.3" {
		t.Fatalf("%+v %v", p, ok)
	}
}
