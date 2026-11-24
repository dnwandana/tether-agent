package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// tasksRecorder keeps each event that a Tasks publishes.
type tasksRecorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *tasksRecorder) publish(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *tasksRecorder) views() []TaskView {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []TaskView
	for _, e := range r.events {
		if e.Name != "task" {
			continue
		}
		out = append(out, e.Data.(TaskView))
	}
	return out
}

// tasksNew returns a Tasks over a new hub and a recorder that gets each event.
func tasksNew(t *testing.T, work context.Context) (*Tasks, *tasksRecorder) {
	t.Helper()
	tk := NewTasks(NewHub(1<<20), func() context.Context { return work })
	rec := &tasksRecorder{}
	tk.publish = rec.publish
	return tk, rec
}

// tasksWait waits up to 2 s until the task id is not running, and returns its view.
func tasksWait(t *testing.T, tk *Tasks, id string) TaskView {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := tk.Get(id); ok && v.State != "running" {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s did not end", id)
	return TaskView{}
}

func TestTasksRunProgressDone(t *testing.T) {
	tk, rec := tasksNew(t, context.Background())
	id, err := tk.Start("doctor", func(ctx context.Context, progress func(string)) (string, error) {
		progress("step 1 of 2")
		return "All checks passed.", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(id) {
		t.Fatalf("id %q", id)
	}
	got := tasksWait(t, tk, id)
	want := TaskView{ID: id, Kind: "doctor", State: "done", Text: "All checks passed."}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	views := rec.views()
	wantViews := []TaskView{
		{ID: id, Kind: "doctor", State: "running", Text: ""},
		{ID: id, Kind: "doctor", State: "running", Text: "step 1 of 2"},
		want,
	}
	if len(views) != len(wantViews) {
		t.Fatalf("events %+v", views)
	}
	for i := range wantViews {
		if views[i] != wantViews[i] {
			t.Fatalf("event %d: got %+v, want %+v", i, views[i], wantViews[i])
		}
	}
}

func TestTasksErrorAndPanic(t *testing.T) {
	tk, _ := tasksNew(t, context.Background())
	id, _ := tk.Start("backup", func(context.Context, func(string)) (string, error) {
		return "", errors.New("backup: the disk is full")
	})
	if got := tasksWait(t, tk, id); got.State != "error" || got.Text != "backup: the disk is full" {
		t.Fatalf("%+v", got)
	}
	id, _ = tk.Start("backup", func(context.Context, func(string)) (string, error) {
		panic("boom")
	})
	if got := tasksWait(t, tk, id); got.State != "error" || got.Text != "The task stopped with an internal error." {
		t.Fatalf("%+v", got)
	}
	if _, ok := tk.Get("0000000000000000"); ok {
		t.Fatal("an unknown id must give false")
	}
}

func TestTasksOnePerKind(t *testing.T) {
	tk, _ := tasksNew(t, context.Background())
	release := make(chan struct{})
	first, err := tk.Start("skills", func(context.Context, func(string)) (string, error) {
		<-release
		return "ok", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tk.Start("skills", func(context.Context, func(string)) (string, error) { return "", nil }); !errors.Is(err, ErrTaskBusy) {
		t.Fatalf("a second task of the same kind: %v, want ErrTaskBusy", err)
	}
	other, err := tk.Start("doctor", func(context.Context, func(string)) (string, error) { return "ok", nil })
	if err != nil {
		t.Fatalf("another kind must start: %v", err)
	}
	tasksWait(t, tk, other)
	close(release)
	tasksWait(t, tk, first)
	if _, err := tk.Start("skills", func(context.Context, func(string)) (string, error) { return "", nil }); err != nil {
		t.Fatalf("the kind is free after the end: %v", err)
	}
}

// A run lives on the work context, so a gateway stop ends it. A stopped work context starts no run.
func TestTasksUseWorkContext(t *testing.T) {
	work, cancel := context.WithCancel(context.Background())
	tk, _ := tasksNew(t, work)
	id, err := tk.Start("update", func(ctx context.Context, _ func(string)) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if got := tasksWait(t, tk, id); got.State != "error" || got.Text != "context canceled" {
		t.Fatalf("%+v", got)
	}
	if _, err := tk.Start("update", func(context.Context, func(string)) (string, error) { return "", nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestTasksRoute(t *testing.T) {
	h := NewHub(1 << 20)
	t.Cleanup(h.Close)
	tasks := NewTasks(h, func() context.Context { return context.Background() })
	mux := http.NewServeMux()
	NewTasksAPI(tasks).Register(mux)
	id, err := tasks.Start("demo", func(context.Context, func(string)) (string, error) {
		return "", errors.New("the work failed")
	})
	if err != nil {
		t.Fatal(err)
	}
	var v TaskView
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/tasks/"+id, nil))
		if rec.Code != 200 {
			t.Fatal(rec.Code, rec.Body)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		if v.State != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the task did not end")
		}
	}
	if v.ID != id || v.Kind != "demo" || v.State != "error" || v.Text != "the work failed" {
		t.Fatalf("%+v", v)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/tasks/nope", nil))
	if !strings.Contains(rec.Body.String(), `"error"`) || rec.Code != http.StatusNotFound {
		t.Fatal(rec.Code, rec.Body)
	}
}

// The handle sets the waiting flag, the code, and the backup report, and each change is a task event.
func TestTasksStartWithHandle(t *testing.T) {
	tk, rec := tasksNew(t, context.Background())
	release := make(chan struct{})
	report := BackupReport{Size: 10, Sessions: 2, Messages: 5, CronJobs: 1,
		Replaced: []string{"providers.primary.api_key"}, Warnings: []string{}, Skipped: []string{}}
	id, err := tk.StartWith(TaskOpts{Kind: "skills", Subject: "pdf"}, func(ctx context.Context, h TaskHandle) (string, error) {
		h.Waiting(true)
		<-release
		h.Waiting(false)
		h.Code("need_force")
		h.Backup(report)
		h.Progress("step 2")
		return "", errors.New("blocked")
	})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if v, _ := tk.Get(id); v.Waiting {
			if v.Subject != "pdf" || v.State != "running" || v.Kind != "skills" {
				t.Fatalf("%+v", v)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the task never waited")
		}
	}
	close(release)
	got := tasksWait(t, tk, id)
	if got.State != "error" || got.Text != "blocked" || got.Code != "need_force" || got.Waiting ||
		got.Subject != "pdf" || got.Backup == nil || got.Backup.Sessions != 2 || got.Backup.Replaced[0] != "providers.primary.api_key" {
		t.Fatalf("%+v", got)
	}
	views := rec.views()
	if len(views) != 7 || !views[1].Waiting || views[2].Waiting || views[3].Code != "need_force" ||
		views[4].Backup == nil || views[5].Text != "step 2" || views[5].Code != "need_force" {
		t.Fatalf("one event for each change, and each keeps the earlier fields: %+v", views)
	}
}

// GET /api/tasks lists the tasks of a kind, newest first. No kind lists all tasks.
func TestTasksListRoute(t *testing.T) {
	tk, _ := tasksNew(t, context.Background())
	a, _ := tk.Start("doctor", func(context.Context, func(string)) (string, error) { return "a", nil })
	tasksWait(t, tk, a)
	b, _ := tk.StartWith(TaskOpts{Kind: "skills", Subject: "pdf"}, func(context.Context, TaskHandle) (string, error) { return "b", nil })
	tasksWait(t, tk, b)
	c, _ := tk.StartWith(TaskOpts{Kind: "skills", Subject: "git"}, func(context.Context, TaskHandle) (string, error) { return "c", nil })
	tasksWait(t, tk, c)
	mux := http.NewServeMux()
	NewTasksAPI(tk).Register(mux)
	get := func(path string) (string, []TaskView) {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		var out []TaskView
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatal(rec.Code, rec.Body)
		}
		return strings.TrimSpace(rec.Body.String()), out
	}
	if _, got := get("/api/tasks?kind=skills"); len(got) != 2 || got[0].ID != c || got[0].Subject != "git" || got[1].ID != b {
		t.Fatalf("%+v", got)
	}
	if _, got := get("/api/tasks"); len(got) != 3 || got[0].ID != c || got[2].ID != a {
		t.Fatalf("%+v", got)
	}
	if body, _ := get("/api/tasks?kind=backup"); body != "[]" {
		t.Fatalf("no task gives [] and not null: %s", body)
	}
}
