package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/acp"
	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/mcp"
	"github.com/dnwandana/tether-agent/internal/tools"
)

var systemT0 = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

func systemRedact(s string) string { return strings.ReplaceAll(s, "sk-secret", "[REDACTED]") }

func systemTasks(t *testing.T) *Tasks {
	t.Helper()
	h := NewHub(1 << 20)
	t.Cleanup(h.Close)
	return NewTasks(h, func() context.Context { return context.Background() })
}

func systemDo(t *testing.T, a *SystemAPI, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	a.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func systemDecode[T any](t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) T {
	t.Helper()
	var v T
	if rec.Code != wantStatus {
		t.Fatalf("status %d, want %d: %s", rec.Code, wantStatus, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	return v
}

func TestSystemDoctorRunAndView(t *testing.T) {
	src := SystemSources{Doctor: func(context.Context) ([]DoctorCheck, time.Time, error) {
		return []DoctorCheck{
			{Status: "ok", Name: "config", Detail: "loaded /home/u/.tether/config.yaml"},
			{Status: "fail", Name: "provider primary", Detail: "HTTP 401 for key sk-secret", Next: "check providers.primary.api_key"},
		}, systemT0, nil
	}}
	a := NewSystemAPI(src, systemTasks(t), systemRedact)
	if v := systemDecode[DoctorView](t, systemDo(t, a, "GET", "/api/doctor"), 200); len(v.Checks) != 0 || !v.RanAt.IsZero() {
		t.Fatalf("no run yet: %+v", v)
	}
	started := systemDecode[map[string]string](t, systemDo(t, a, "POST", "/api/doctor"), 202)
	if started["task_id"] == "" {
		t.Fatal(started)
	}
	var v DoctorView
	for deadline := time.Now().Add(5 * time.Second); v.RanAt.IsZero(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the doctor task did not keep a result")
		}
		v = systemDecode[DoctorView](t, systemDo(t, a, "GET", "/api/doctor"), 200)
	}
	if !v.RanAt.Equal(systemT0) || v.TaskID != started["task_id"] || len(v.Checks) != 2 {
		t.Fatalf("%+v", v)
	}
	if v.Checks[1].Detail != "HTTP 401 for key [REDACTED]" || v.Checks[1].Next != "check providers.primary.api_key" {
		t.Fatalf("%+v", v.Checks[1])
	}
}

func TestSystemDoctorBusyAndOff(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	src := SystemSources{Doctor: func(context.Context) ([]DoctorCheck, time.Time, error) {
		started <- struct{}{}
		<-release
		return nil, systemT0, nil
	}}
	a := NewSystemAPI(src, systemTasks(t), systemRedact)
	defer close(release)
	systemDecode[map[string]string](t, systemDo(t, a, "POST", "/api/doctor"), 202)
	<-started
	if e := systemDecode[map[string]string](t, systemDo(t, a, "POST", "/api/doctor"), 409); e["error"] != "The doctor runs already." {
		t.Fatal(e)
	}
	off := NewSystemAPI(SystemSources{}, systemTasks(t), systemRedact)
	if e := systemDecode[map[string]string](t, systemDo(t, off, "POST", "/api/doctor"), 503); e["error"] != "The doctor is not available." {
		t.Fatal(e)
	}
}

func TestSystemMCP(t *testing.T) {
	src := SystemSources{MCP: func() []mcp.ServerStatus {
		return []mcp.ServerStatus{{Name: "gh", Transport: "stdio", Connected: true, Tools: 2,
			Classes: map[string]int{"read": 1, "write": 1}, LastError: "dial https://x.io/?key=sk-secret failed", LastErrorAt: systemT0}}
	}}
	got := systemDecode[[]McpRow](t, systemDo(t, NewSystemAPI(src, systemTasks(t), systemRedact), "GET", "/api/mcp"), 200)
	want := []McpRow{{Name: "gh", Transport: "stdio", Connected: true, Tools: 2, Classes: map[string]int{"read": 1, "write": 1},
		LastError: "dial https://x.io/?key=[REDACTED] failed", LastErrorAt: systemT0}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	rec := systemDo(t, NewSystemAPI(SystemSources{}, systemTasks(t), systemRedact), "GET", "/api/mcp")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestSystemRuntime(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "p.log")
	if err := os.WriteFile(logPath, []byte("start\nexport TOKEN=sk-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewProcRegistry()
	reg.Add("s1", 4242, "deploy --token sk-secret", logPath, systemT0)
	reg.Add("s1", 4243, "make test", "", systemT0.Add(time.Second))
	reg.Exit(4243, 1, systemT0.Add(2*time.Second))
	src := SystemSources{
		Batches: func(context.Context, int) ([]agent.BatchInfo, error) {
			return []agent.BatchInfo{
				{ID: "b2", Key: "dashboard:owner", Status: agent.BatchRunning,
					Tasks: json.RawMessage(`[{"name":"1/1","goal":"scan sk-secret"}]`), Created: systemT0},
				{ID: "b1", Key: "dashboard:owner", Status: agent.BatchDone, Tasks: json.RawMessage(`[{"name":"1/1","goal":"g1"}]`),
					Results: json.RawMessage(`[{"Status":"ok","Summary":"did g1","ToolCalls":3,"DurationS":1.5}]`), Created: systemT0.Add(-time.Hour)},
			}, nil
		},
		ACP: func() []acp.SessionInfo {
			return []acp.SessionInfo{{SessionID: "s1", Agent: "codex", Cwd: "/srv/app", Busy: true},
				{SessionID: "s2", Agent: "claude-code", Cwd: "/srv/web"}}
		},
		Procs:   reg.All,
		Tail:    reg.Tail,
		Browser: func() bool { return true },
	}
	v := systemDecode[RuntimeView](t, systemDo(t, NewSystemAPI(src, systemTasks(t), systemRedact), "GET", "/api/runtime"), 200)
	if v.Running != 3 || !v.Browser || len(v.Batches) != 2 || len(v.ACP) != 2 || len(v.Processes) != 2 {
		t.Fatalf("%+v", v)
	}
	if v.Batches[0].ID != "b2" || v.Batches[0].Tasks[0].Goal != "scan [REDACTED]" || len(v.Batches[0].Results) != 0 {
		t.Fatalf("%+v", v.Batches[0])
	}
	if v.Batches[1].Results[0] != (RuntimeResult{Status: "ok", Summary: "did g1", ToolCalls: 3, DurationS: 1.5}) {
		t.Fatalf("%+v", v.Batches[1])
	}
	p := v.Processes[0]
	if p.PID != 4242 || p.Command != "deploy --token [REDACTED]" || p.Tail != "start\nexport TOKEN=[REDACTED]\n" || p.Exited {
		t.Fatalf("%+v", p)
	}
	if q := v.Processes[1]; !q.Exited || q.ExitCode != 1 || q.Tail != "" {
		t.Fatalf("%+v", q)
	}
	if v.ACP[0] != (RuntimeACP{SessionID: "s1", Agent: "codex", Cwd: "/srv/app", Busy: true}) {
		t.Fatalf("%+v", v.ACP)
	}
	empty := systemDo(t, NewSystemAPI(SystemSources{}, systemTasks(t), systemRedact), "GET", "/api/runtime").Body.String()
	for _, part := range []string{`"batches":[]`, `"acp":[]`, `"processes":[]`, `"browser":false`, `"running":0`} {
		if !strings.Contains(empty, part) {
			t.Fatalf("%s has no %s", empty, part)
		}
	}
}

// A second start within one minute gets 429 before a task starts.
func TestSystemDoctorRateLimitBeforeTask(t *testing.T) {
	var runs atomic.Int32
	src := SystemSources{Doctor: func(context.Context) ([]DoctorCheck, time.Time, error) {
		runs.Add(1)
		return nil, systemT0, nil
	}}
	tasks := systemTasks(t)
	a := NewSystemAPI(src, tasks, systemRedact)
	clock := systemT0
	a.now = func() time.Time { return clock }
	first := systemDecode[map[string]string](t, systemDo(t, a, "POST", "/api/doctor"), 202)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if v, ok := tasks.Get(first["task_id"]); ok && v.State != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the doctor task did not end")
		}
	}
	clock = systemT0.Add(59 * time.Second)
	e := systemDecode[map[string]string](t, systemDo(t, a, "POST", "/api/doctor"), 429)
	if e["error"] != "The doctor ran less than a minute ago. Wait one minute and try again." || runs.Load() != 1 {
		t.Fatal(e, runs.Load())
	}
	clock = systemT0.Add(61 * time.Second)
	systemDecode[map[string]string](t, systemDo(t, a, "POST", "/api/doctor"), 202)
}
