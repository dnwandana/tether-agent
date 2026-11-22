package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/acp"
	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/mcp"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// SystemSources are the parts of the gateway that the System page reads. A nil field is a
// part that is off.
type SystemSources struct {
	Doctor  func(ctx context.Context) ([]DoctorCheck, time.Time, error)
	MCP     func() []mcp.ServerStatus
	Batches func(ctx context.Context, limit int) ([]agent.BatchInfo, error)
	ACP     func() []acp.SessionInfo
	Procs   func() []tools.ProcInfo
	Tail    func(pid, n int) string
	Browser func() bool
}

// DoctorView is the last kept doctor result. TaskID is the id of the last doctor task.
type DoctorView struct {
	Checks []DoctorCheck `json:"checks"`
	RanAt  time.Time     `json:"ran_at"`
	TaskID string        `json:"task_id"`
}

// McpRow is the state of one MCP server.
type McpRow struct {
	Name        string         `json:"name"`
	Transport   string         `json:"transport"`
	Connected   bool           `json:"connected"`
	Tools       int            `json:"tools"`
	Classes     map[string]int `json:"classes"`
	LastError   string         `json:"last_error"`
	LastErrorAt time.Time      `json:"last_error_at"`
}

// RuntimeView is the running work of the gateway. Running is the sum that the status bar shows.
type RuntimeView struct {
	Batches   []RuntimeBatch `json:"batches"`
	ACP       []RuntimeACP   `json:"acp"`
	Processes []RuntimeProc  `json:"processes"`
	Browser   bool           `json:"browser"`
	Running   int            `json:"running"`
}

// RuntimeBatch is one background delegate_task batch.
type RuntimeBatch struct {
	ID      string          `json:"id"`
	Key     string          `json:"key"`
	Status  string          `json:"status"`
	Tasks   []RuntimeTask   `json:"tasks"`
	Results []RuntimeResult `json:"results"`
	Created time.Time       `json:"created"`
}

// RuntimeTask is one task of a batch.
type RuntimeTask struct {
	Name string `json:"name"`
	Goal string `json:"goal"`
}

// RuntimeResult is the result of one task of a batch.
type RuntimeResult struct {
	Status    string  `json:"status"`
	Summary   string  `json:"summary"`
	Error     string  `json:"error"`
	ToolCalls int     `json:"tool_calls"`
	DurationS float64 `json:"duration_s"`
}

// RuntimeACP is one session of a coding agent.
type RuntimeACP struct {
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
	Cwd       string `json:"cwd"`
	Busy      bool   `json:"busy"`
}

// RuntimeProc is one background process.
type RuntimeProc struct {
	PID       int       `json:"pid"`
	SessionID string    `json:"session_id"`
	Command   string    `json:"command"`
	Started   time.Time `json:"started"`
	Ended     time.Time `json:"ended"`
	Exited    bool      `json:"exited"`
	ExitCode  int       `json:"exit_code"`
	Tail      string    `json:"tail"`
}

func init() { registerAPI(DoctorView{}, McpRow{}, RuntimeView{}) }

// SystemAPI serves the doctor, the MCP status, and the running work.
type SystemAPI struct {
	src    SystemSources
	tasks  *Tasks
	redact func(string) string

	mu     sync.Mutex
	last   []DoctorCheck
	lastAt time.Time
	taskID string
	// lastStart is the time of the last start of the doctor task. The zero time means no start.
	lastStart time.Time
	now       func() time.Time // a test replaces it
}

func NewSystemAPI(src SystemSources, tasks *Tasks, redact func(string) string) *SystemAPI {
	return &SystemAPI{src: src, tasks: tasks, redact: redact, now: time.Now}
}

// Register adds GET and POST /api/doctor, GET /api/mcp, and GET /api/runtime.
func (a *SystemAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/doctor", a.getDoctor)
	mux.HandleFunc("POST /api/doctor", a.postDoctor)
	mux.HandleFunc("GET /api/mcp", a.getMCP)
	mux.HandleFunc("GET /api/runtime", a.getRuntime)
}

func systemJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func systemErr(w http.ResponseWriter, status int, msg string) {
	systemJSON(w, status, map[string]string{"error": msg})
}

func (a *SystemAPI) getDoctor(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	v := DoctorView{Checks: append([]DoctorCheck{}, a.last...), RanAt: a.lastAt, TaskID: a.taskID}
	a.mu.Unlock()
	systemJSON(w, http.StatusOK, v)
}

func (a *SystemAPI) postDoctor(w http.ResponseWriter, _ *http.Request) {
	if a.src.Doctor == nil {
		systemErr(w, http.StatusServiceUnavailable, "The doctor is not available.")
		return
	}
	// Hold the lock while the task starts, so the task cannot set its id before the
	// start call returns and writes the id again.
	a.mu.Lock()
	defer a.mu.Unlock()
	// A running doctor gives 409 from Start. The limit applies to a start after it ended.
	now := a.now()
	if v, ok := a.tasks.Get(a.taskID); !ok || v.State != "running" {
		if !a.lastStart.IsZero() && now.Sub(a.lastStart) < time.Minute {
			systemErr(w, http.StatusTooManyRequests, "The doctor ran less than a minute ago. Wait one minute and try again.")
			return
		}
	}
	id, err := a.tasks.Start("doctor", a.runDoctor)
	if errors.Is(err, ErrTaskBusy) {
		systemErr(w, http.StatusConflict, "The doctor runs already.")
		return
	}
	if err != nil {
		systemErr(w, http.StatusInternalServerError, a.redact(err.Error()))
		return
	}
	a.taskID, a.lastStart = id, now
	systemJSON(w, http.StatusAccepted, map[string]string{"task_id": id})
}

func (a *SystemAPI) runDoctor(ctx context.Context, _ func(string)) (string, error) {
	checks, at, err := a.src.Doctor(ctx)
	if !at.IsZero() {
		kept := make([]DoctorCheck, 0, len(checks))
		for _, c := range checks {
			c.Detail, c.Next = a.redact(c.Detail), a.redact(c.Next)
			kept = append(kept, c)
		}
		a.mu.Lock()
		a.last, a.lastAt = kept, at
		a.mu.Unlock()
	}
	if err != nil {
		return "", errors.New(a.redact(err.Error()))
	}
	var ok, warn, fail int
	for _, c := range checks {
		switch c.Status {
		case "ok":
			ok++
		case "warn":
			warn++
		case "fail":
			fail++
		}
	}
	return fmt.Sprintf("Doctor: %d ok, %d warn, %d fail.", ok, warn, fail), nil
}

func (a *SystemAPI) getMCP(w http.ResponseWriter, _ *http.Request) {
	rows := []McpRow{}
	if a.src.MCP != nil {
		for _, s := range a.src.MCP() {
			rows = append(rows, McpRow{Name: s.Name, Transport: s.Transport, Connected: s.Connected,
				Tools: s.Tools, Classes: s.Classes, LastError: a.redact(s.LastError), LastErrorAt: s.LastErrorAt})
		}
	}
	systemJSON(w, http.StatusOK, rows)
}

func (a *SystemAPI) getRuntime(w http.ResponseWriter, r *http.Request) {
	v := RuntimeView{Batches: []RuntimeBatch{}, ACP: []RuntimeACP{}, Processes: []RuntimeProc{}}
	if a.src.Batches != nil {
		list, err := a.src.Batches(r.Context(), 20)
		if err != nil {
			systemErr(w, http.StatusInternalServerError, a.redact(err.Error()))
			return
		}
		for _, b := range list {
			v.Batches = append(v.Batches, a.runtimeBatch(b))
			if b.Status == agent.BatchRunning {
				v.Running++
			}
		}
	}
	if a.src.ACP != nil {
		for _, s := range a.src.ACP() {
			v.ACP = append(v.ACP, RuntimeACP{SessionID: s.SessionID, Agent: s.Agent, Cwd: s.Cwd, Busy: s.Busy})
			if s.Busy {
				v.Running++
			}
		}
	}
	if a.src.Procs != nil {
		for _, p := range a.src.Procs() {
			row := RuntimeProc{PID: p.PID, SessionID: p.SessionID, Command: a.redact(p.Command),
				Started: p.StartedAt, Ended: p.EndedAt, Exited: p.Exited, ExitCode: p.ExitCode}
			if a.src.Tail != nil {
				row.Tail = a.redact(a.src.Tail(p.PID, 2048))
			}
			v.Processes = append(v.Processes, row)
			if !p.Exited {
				v.Running++
			}
		}
	}
	v.Browser = a.src.Browser != nil && a.src.Browser()
	systemJSON(w, http.StatusOK, v)
}

func (a *SystemAPI) runtimeBatch(b agent.BatchInfo) RuntimeBatch {
	out := RuntimeBatch{ID: b.ID, Key: b.Key, Status: b.Status, Created: b.Created,
		Tasks: []RuntimeTask{}, Results: []RuntimeResult{}}
	var tasks []RuntimeTask
	if json.Unmarshal(b.Tasks, &tasks) == nil {
		for _, t := range tasks {
			out.Tasks = append(out.Tasks, RuntimeTask{Name: t.Name, Goal: a.redact(t.Goal)})
		}
	}
	// The stored results have the field names of tools.ChildResultLite, with no JSON tags.
	var results []struct {
		Status, Summary, Error string
		ToolCalls              int
		DurationS              float64
	}
	if len(b.Results) > 0 && json.Unmarshal(b.Results, &results) == nil {
		for _, r := range results {
			out.Results = append(out.Results, RuntimeResult{Status: r.Status, Summary: a.redact(r.Summary),
				Error: a.redact(r.Error), ToolCalls: r.ToolCalls, DurationS: r.DurationS})
		}
	}
	return out
}
